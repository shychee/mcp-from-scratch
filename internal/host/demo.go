package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	Tools []ToolDescription `json:"tools"`
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

	transcript, err := runProtocolDemo(&client)
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

func runProtocolDemo(client *rpcClient) (Transcript, error) {
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
	if err := decodeCompleteResult(discovery.Result, &struct{}{}); err != nil {
		return Transcript{}, fmt.Errorf("decode server/discover result: %w", err)
	}

	toolsListRequest := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(2),
		Method:  "tools/list",
		Params:  requestParamsJSON,
	}
	toolsList, err := client.call(toolsListRequest)
	if err != nil {
		return Transcript{}, fmt.Errorf("tools/list: %w", err)
	}

	var listedTools toolsListResult
	if err := decodeCompleteResult(toolsList.Result, &listedTools); err != nil {
		return Transcript{}, fmt.Errorf("decode tools/list result: %w", err)
	}

	decision, err := fakeModelDecision(listedTools.Tools, "hello from fake model")
	if err != nil {
		return Transcript{}, fmt.Errorf("fake model decision: %w", err)
	}
	toolCallParams := toolCallRequestParams{
		RequestParams: protocol.RequestParams{Meta: clientRequestMeta()},
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
		RequestParams: protocol.RequestParams{Meta: clientRequestMeta()},
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
		DiscoveredTools:      listedTools.Tools,
		Exchanges: []Exchange{
			{Name: "server/discover", Request: discoveryRequest, Response: &discovery},
			{Name: "tools/list", Request: toolsListRequest, Response: &toolsList},
			{Name: "tools/call", Request: echoCallRequest, Response: &echoCall},
			{Name: "confirm_preview input required", Request: previewRequest, Response: &previewInputRequired},
			{Name: "confirm_preview retry", Request: previewRetryRequest, Response: &previewConfirmation},
		},
	}, nil
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
	return protocol.RequestMeta{
		ProtocolVersion: protocol.Version20260728,
		ClientInfo: &protocol.Implementation{
			Name:    "mcp-from-scratch-host",
			Version: "0.1.0",
		},
		ClientCapabilities: map[string]any{
			"elicitation": map[string]any{
				"form": map[string]any{},
			},
		},
	}
}

func (c *rpcClient) call(request protocol.Request) (protocol.Response, error) {
	if err := c.encoder.Encode(request); err != nil {
		return protocol.Response{}, fmt.Errorf("encode request: %w", err)
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
