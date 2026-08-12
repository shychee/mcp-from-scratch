package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os/exec"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

type ServerCommand struct {
	Name string
	Args []string
	Dir  string
}

type ToolDescription struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type toolsListResult struct {
	Tools      []ToolDescription `json:"tools"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

type toolCallRequestParams struct {
	protocol.RequestParams
	Name           string                     `json:"name"`
	Arguments      json.RawMessage            `json:"arguments"`
	InputResponses map[string]json.RawMessage `json:"inputResponses,omitempty"`
	RequestState   string                     `json:"requestState,omitempty"`
}

type ToolCallDecision struct {
	ToolName  string
	Arguments json.RawMessage
}

type Transcript struct {
	Discovery            protocol.Response
	ToolsList            protocol.Response
	EchoCall             protocol.Response
	PreviewInputRequired protocol.Response
	PreviewConfirmation  protocol.Response
	Exchanges            []Exchange
	DiscoveredTools      []ToolDescription
	NegotiatedExtensions protocol.Extensions
}

type inputRequiredResult struct {
	InputRequests map[string]struct {
		Method string `json:"method"`
	} `json:"inputRequests"`
	RequestState string `json:"requestState"`
}

type Exchange struct {
	Name     string             `json:"name"`
	Request  protocol.Request   `json:"request"`
	Response *protocol.Response `json:"response,omitempty"`
}

func RunDemo(ctx context.Context, serverCommand ServerCommand) (Transcript, error) {
	cmd := exec.CommandContext(ctx, serverCommand.Name, serverCommand.Args...)
	cmd.Dir = serverCommand.Dir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Transcript{}, fmt.Errorf("open server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Transcript{}, fmt.Errorf("open server stdout: %w", err)
	}

	var stderrBuffer bytes.Buffer
	cmd.Stderr = &stderrBuffer

	if err := cmd.Start(); err != nil {
		return Transcript{}, fmt.Errorf("start server: %w", err)
	}

	client := rpcClient{
		encoder: json.NewEncoder(stdin),
		decoder: json.NewDecoder(stdout),
	}

	var negotiator compatibilityNegotiator
	compatibility, err := negotiator.detect(&client, []string{protocol.Version20260728}, func() error {
		return legacyInitializeCall(&client)
	})
	var transcript Transcript
	if err == nil {
		if compatibility.Era == ServerEraModern {
			transcript, err = runProtocolDemoFromDiscovery(&client, compatibility.Discovery, compatibility.ProtocolVersion)
		} else {
			transcript, err = runLegacyProtocolDemo(&client)
		}
	}
	closeErr := stdin.Close()
	waitErr := cmd.Wait()

	if err != nil {
		return Transcript{}, err
	}
	if closeErr != nil {
		return Transcript{}, fmt.Errorf("close server stdin: %w", closeErr)
	}
	if waitErr != nil {
		return Transcript{}, fmt.Errorf("wait for server: %w; stderr: %s", waitErr, stderrBuffer.String())
	}

	return transcript, nil
}

type rpcClient struct {
	encoder *json.Encoder
	decoder *json.Decoder
}

type protocolClient interface {
	call(protocol.Request) (protocol.Response, error)
}

type httpRPCClient struct {
	ctx      context.Context
	endpoint string
	client   *http.Client
}

// RunHTTPDemo runs the same stateless protocol flow over Streamable HTTP.
func RunHTTPDemo(ctx context.Context, endpoint string) (Transcript, error) {
	client := &httpRPCClient{
		ctx:      ctx,
		endpoint: endpoint,
		client:   http.DefaultClient,
	}
	return runProtocolDemo(client)
}

func runProtocolDemo(client protocolClient) (Transcript, error) {
	requestParamsJSON, err := json.Marshal(protocol.RequestParams{
		Meta: clientRequestMeta(),
	})
	if err != nil {
		return Transcript{}, fmt.Errorf("encode request metadata: %w", err)
	}

	discoveryRequest := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "server/discover",
		Params:  requestParamsJSON,
	}
	discovery, err := client.call(discoveryRequest)
	if err != nil {
		return Transcript{}, fmt.Errorf("server/discover: %w", err)
	}
	return runProtocolDemoFromDiscovery(client, discovery, protocol.Version20260728)
}

func runProtocolDemoFromDiscovery(client protocolClient, discovery protocol.Response, version string) (Transcript, error) {
	if err := decodeCompleteResult(discovery.Result, &struct{}{}); err != nil {
		return Transcript{}, fmt.Errorf("decode server/discover result: %w", err)
	}
	requestParamsJSON, err := json.Marshal(protocol.RequestParams{Meta: requestMetaForVersion(version)})
	if err != nil {
		return Transcript{}, fmt.Errorf("encode request metadata: %w", err)
	}
	discoveryRequest := protocol.Request{JSONRPC: "2.0", ID: discovery.ID, Method: "server/discover", Params: requestParamsJSON}
	negotiatedExtensions, err := negotiatedExtensionsFromDiscovery(clientExtensions(), discovery.Result)
	if err != nil {
		return Transcript{}, fmt.Errorf("negotiate extensions: %w", err)
	}

	listedTools, toolsListRequest, toolsList, err := listAllTools(client, requestParamsJSON, 2)
	if err != nil {
		return Transcript{}, fmt.Errorf("tools/list: %w", err)
	}

	decision, err := fakeModelDecision(listedTools, "hello from fake model")
	if err != nil {
		return Transcript{}, fmt.Errorf("fake model decision: %w", err)
	}
	toolCallParams := toolCallRequestParams{
		RequestParams: protocol.RequestParams{Meta: requestMetaForVersion(version)},
		Name:          decision.ToolName,
		Arguments:     decision.Arguments,
	}
	toolCallParamsJSON, err := json.Marshal(toolCallParams)
	if err != nil {
		return Transcript{}, fmt.Errorf("encode tools/call params: %w", err)
	}
	echoCallRequest := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(3),
		Method:  "tools/call",
		Params:  toolCallParamsJSON,
	}
	echoCall, err := client.call(echoCallRequest)
	if err != nil {
		return Transcript{}, fmt.Errorf("tools/call: %w", err)
	}
	if err := decodeCompleteResult(echoCall.Result, &struct{}{}); err != nil {
		return Transcript{}, fmt.Errorf("decode tools/call result: %w", err)
	}

	previewArguments, err := json.Marshal(map[string]string{
		"preview": "archive demo preview",
	})
	if err != nil {
		return Transcript{}, fmt.Errorf("encode confirm_preview arguments: %w", err)
	}
	previewRequestParams := toolCallRequestParams{
		RequestParams: protocol.RequestParams{Meta: requestMetaForVersion(version)},
		Name:          "confirm_preview",
		Arguments:     previewArguments,
	}
	previewRequestParamsJSON, err := json.Marshal(previewRequestParams)
	if err != nil {
		return Transcript{}, fmt.Errorf("encode initial confirm_preview params: %w", err)
	}
	previewRequest := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(4),
		Method:  "tools/call",
		Params:  previewRequestParamsJSON,
	}
	previewInputRequired, err := client.call(previewRequest)
	if err != nil {
		return Transcript{}, fmt.Errorf("initial confirm_preview call: %w", err)
	}
	var required inputRequiredResult
	if err := decodeInputRequiredResult(previewInputRequired.Result, &required); err != nil {
		return Transcript{}, fmt.Errorf("decode confirm_preview input-required result: %w", err)
	}
	inputRequest, ok := required.InputRequests["confirm_preview"]
	if !ok || inputRequest.Method != "elicitation/create" {
		return Transcript{}, fmt.Errorf("confirm_preview did not request elicitation/create")
	}

	confirmationResponse, err := json.Marshal(map[string]any{
		"action": "accept",
		"content": map[string]bool{
			"confirm": true,
		},
	})
	if err != nil {
		return Transcript{}, fmt.Errorf("encode confirm_preview input response: %w", err)
	}
	previewRetryParams := previewRequestParams
	previewRetryParams.InputResponses = map[string]json.RawMessage{
		"confirm_preview": confirmationResponse,
	}
	previewRetryParams.RequestState = required.RequestState
	previewRetryParamsJSON, err := json.Marshal(previewRetryParams)
	if err != nil {
		return Transcript{}, fmt.Errorf("encode confirm_preview retry params: %w", err)
	}
	previewRetryRequest := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(5),
		Method:  "tools/call",
		Params:  previewRetryParamsJSON,
	}
	previewConfirmation, err := client.call(previewRetryRequest)
	if err != nil {
		return Transcript{}, fmt.Errorf("retry confirm_preview call: %w", err)
	}
	if err := decodeCompleteResult(previewConfirmation.Result, &struct{}{}); err != nil {
		return Transcript{}, fmt.Errorf("decode confirm_preview result: %w", err)
	}

	return Transcript{
		Discovery:            discovery,
		ToolsList:            toolsList,
		EchoCall:             echoCall,
		PreviewInputRequired: previewInputRequired,
		PreviewConfirmation:  previewConfirmation,
		DiscoveredTools:      listedTools,
		NegotiatedExtensions: negotiatedExtensions,
		Exchanges: []Exchange{
			{Name: "server/discover", Request: discoveryRequest, Response: &discovery},
			{Name: "tools/list", Request: toolsListRequest, Response: &toolsList},
			{Name: "tools/call", Request: echoCallRequest, Response: &echoCall},
			{Name: "confirm_preview input required", Request: previewRequest, Response: &previewInputRequired},
			{Name: "confirm_preview retry", Request: previewRetryRequest, Response: &previewConfirmation},
		},
	}, nil
}

func runLegacyProtocolDemo(client protocolClient) (Transcript, error) {
	params := json.RawMessage(`{}`)
	request := protocol.Request{JSONRPC: "2.0", ID: protocol.ID(3), Method: "tools/list", Params: params}
	list, err := client.call(request)
	if err != nil {
		return Transcript{}, fmt.Errorf("legacy tools/list: %w", err)
	}
	var listed toolsListResult
	if err := decodeCompleteResult(list.Result, &listed); err != nil {
		return Transcript{}, fmt.Errorf("decode legacy tools/list: %w", err)
	}
	return Transcript{
		ToolsList:       list,
		DiscoveredTools: listed.Tools,
		Exchanges:       []Exchange{{Name: "legacy tools/list", Request: request, Response: &list}},
	}, nil
}

func listAllTools(client protocolClient, requestParamsJSON json.RawMessage, firstID int) ([]ToolDescription, protocol.Request, protocol.Response, error) {
	var (
		allTools      []ToolDescription
		cursor        string
		firstRequest  protocol.Request
		firstResponse protocol.Response
	)
	seenCursors := make(map[string]struct{})
	params := map[string]any{}
	if err := json.Unmarshal(requestParamsJSON, &params); err != nil {
		return nil, protocol.Request{}, protocol.Response{}, fmt.Errorf("decode tools/list params: %w", err)
	}
	for page := 0; ; page++ {
		if cursor != "" {
			params["cursor"] = cursor
		}
		paramsJSON, err := json.Marshal(params)
		if err != nil {
			return nil, protocol.Request{}, protocol.Response{}, fmt.Errorf("encode tools/list params: %w", err)
		}
		request := protocol.Request{
			JSONRPC: "2.0",
			ID:      protocol.ID(firstID + page*1000),
			Method:  "tools/list",
			Params:  paramsJSON,
		}
		response, err := client.call(request)
		if err != nil {
			return nil, protocol.Request{}, protocol.Response{}, err
		}
		var listed toolsListResult
		if err := decodeCompleteResult(response.Result, &listed); err != nil {
			return nil, protocol.Request{}, protocol.Response{}, fmt.Errorf("decode tools/list result: %w", err)
		}
		if page == 0 {
			firstRequest, firstResponse = request, response
		}
		allTools = append(allTools, listed.Tools...)
		if listed.NextCursor == "" {
			return allTools, firstRequest, firstResponse, nil
		}
		if _, repeated := seenCursors[listed.NextCursor]; repeated {
			return nil, protocol.Request{}, protocol.Response{}, fmt.Errorf("tools/list repeated nextCursor %q", listed.NextCursor)
		}
		seenCursors[listed.NextCursor] = struct{}{}
		cursor = listed.NextCursor
	}
}

func decodeCompleteResult(raw json.RawMessage, target any) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode result envelope: %w", err)
	}
	if envelope == nil {
		return fmt.Errorf("decode result envelope: result must be an object")
	}

	resultType := protocol.ResultTypeComplete
	if rawResultType, ok := envelope["resultType"]; ok {
		var decodedResultType any
		if err := json.Unmarshal(rawResultType, &decodedResultType); err != nil {
			return fmt.Errorf("decode result envelope: invalid result type: %w", err)
		}
		var valid bool
		resultType, valid = decodedResultType.(string)
		if !valid {
			return fmt.Errorf("decode result envelope: result type must be a string")
		}
		if resultType == "" {
			return fmt.Errorf("decode result envelope: result type must not be empty")
		}
	}
	if resultType != protocol.ResultTypeComplete {
		return fmt.Errorf("unsupported result type %q", resultType)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode result body: %w", err)
	}
	return nil
}

func decodeInputRequiredResult(raw json.RawMessage, target any) error {
	var envelope struct {
		ResultType string `json:"resultType"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode result envelope: %w", err)
	}
	if envelope.ResultType != protocol.ResultTypeInputRequired {
		return fmt.Errorf("unexpected result type %q", envelope.ResultType)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode result body: %w", err)
	}
	return nil
}

func clientRequestMeta() protocol.RequestMeta {
	capabilities, err := capabilitiesWithExtensions(map[string]any{
		"elicitation": map[string]any{
			"form": map[string]any{},
		},
	}, clientExtensions())
	if err != nil {
		panic(err)
	}
	return protocol.RequestMeta{
		ProtocolVersion: protocol.Version20260728,
		ClientInfo: &protocol.Implementation{
			Name:    "mcp-from-scratch-host",
			Version: "0.1.0",
		},
		ClientCapabilities: capabilities,
	}
}

func clientExtensions() protocol.Extensions {
	return protocol.Extensions{
		"io.modelcontextprotocol/tasks": json.RawMessage(`{}`),
	}
}

func (c *rpcClient) call(request protocol.Request) (protocol.Response, error) {
	if err := c.encoder.Encode(request); err != nil {
		return protocol.Response{}, fmt.Errorf("encode request: %w", err)
	}
	if request.ID == nil {
		return protocol.Response{}, nil
	}

	var response protocol.Response
	if err := c.decoder.Decode(&response); err != nil {
		return protocol.Response{}, fmt.Errorf("decode response: %w", err)
	}
	if response.Error != nil {
		return response, response.Error
	}
	return response, nil
}

func (c *httpRPCClient) call(rpcRequest protocol.Request) (protocol.Response, error) {
	request, err := c.newRequest(rpcRequest)
	if err != nil {
		return protocol.Response{}, err
	}

	httpResponse, err := c.client.Do(request)
	if err != nil {
		return protocol.Response{}, fmt.Errorf("send HTTP request: %w", err)
	}
	defer httpResponse.Body.Close()

	mediaType, _, err := mime.ParseMediaType(httpResponse.Header.Get("Content-Type"))
	if err != nil {
		return protocol.Response{}, fmt.Errorf("decode HTTP response content type %q: %w", httpResponse.Header.Get("Content-Type"), err)
	}
	var response protocol.Response
	switch mediaType {
	case protocol.MediaTypeJSON:
		if err := json.NewDecoder(httpResponse.Body).Decode(&response); err != nil {
			return protocol.Response{}, fmt.Errorf("decode HTTP response with status %d: %w", httpResponse.StatusCode, err)
		}
	case protocol.MediaTypeSSE:
		response, err = decodeHTTPSSEResponse(bufio.NewReader(httpResponse.Body), rpcRequest.ID)
		if err != nil {
			return protocol.Response{}, fmt.Errorf("decode HTTP SSE response with status %d: %w", httpResponse.StatusCode, err)
		}
	default:
		return protocol.Response{}, fmt.Errorf("unexpected HTTP response content type %q", mediaType)
	}
	if response.Error != nil {
		return response, response.Error
	}
	if httpResponse.StatusCode != http.StatusOK {
		return response, fmt.Errorf("unexpected HTTP status %d", httpResponse.StatusCode)
	}
	return response, nil
}

func decodeHTTPSSEResponse(reader *bufio.Reader, requestID *protocol.RequestID) (protocol.Response, error) {
	for {
		raw, err := readSSEData(reader)
		if err != nil {
			return protocol.Response{}, err
		}
		var envelope struct {
			ID     *protocol.RequestID `json:"id"`
			Method string              `json:"method"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return protocol.Response{}, fmt.Errorf("decode SSE JSON-RPC message: %w", err)
		}
		if envelope.Method != "" {
			continue
		}
		if requestID == nil || envelope.ID == nil || *requestID != *envelope.ID {
			return protocol.Response{}, fmt.Errorf("unexpected SSE response ID %v", envelope.ID)
		}
		var response protocol.Response
		if err := json.Unmarshal(raw, &response); err != nil {
			return protocol.Response{}, fmt.Errorf("decode SSE JSON-RPC response: %w", err)
		}
		return response, nil
	}
}

func (c *httpRPCClient) newRequest(rpcRequest protocol.Request) (*http.Request, error) {
	body, err := json.Marshal(rpcRequest)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	request, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create HTTP request: %w", err)
	}
	request.Header.Set("Content-Type", protocol.MediaTypeJSON)
	request.Header.Set("Accept", protocol.MediaTypeJSON+", "+protocol.MediaTypeSSE)
	request.Header.Set(protocol.HeaderProtocolVersion, protocol.Version20260728)
	request.Header.Set(protocol.HeaderMethod, rpcRequest.Method)
	if name, ok := requestName(rpcRequest); ok {
		request.Header.Set(protocol.HeaderName, name)
	}
	return request, nil
}

func requestName(request protocol.Request) (string, bool) {
	if !protocol.MethodUsesNameHeader(request.Method) {
		return "", false
	}
	var params struct {
		Name   string `json:"name"`
		URI    string `json:"uri"`
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return "", false
	}
	if request.Method == "resources/read" {
		return params.URI, params.URI != ""
	}
	if request.Method == "tasks/get" || request.Method == "tasks/update" || request.Method == "tasks/cancel" {
		return params.TaskID, params.TaskID != ""
	}
	return params.Name, params.Name != ""
}

func openAIToolsFromToolDescriptions(tools []ToolDescription) []openAITool {
	openAITools := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		openAITools = append(openAITools, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}
	return openAITools
}

func fakeModelDecision(tools []ToolDescription, userText string) (ToolCallDecision, error) {
	for _, tool := range tools {
		if tool.Name == "echo" {
			arguments, err := json.Marshal(map[string]string{
				"text": userText,
			})
			if err != nil {
				return ToolCallDecision{}, fmt.Errorf("encode echo arguments: %w", err)
			}
			return ToolCallDecision{
				ToolName:  tool.Name,
				Arguments: arguments,
			}, nil
		}
	}

	return ToolCallDecision{}, fmt.Errorf("no echo tool discovered")
}
