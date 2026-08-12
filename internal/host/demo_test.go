package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shychee/mcp-from-scratch/internal/mcpserver"
	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

func TestRunDemoTalksToServerProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	transcript, err := RunDemo(ctx, ServerCommand{
		Name: "go",
		Args: []string{"run", "./cmd/mcp-server"},
		Dir:  projectRoot(t),
	})
	if err != nil {
		t.Fatalf("RunDemo() error = %v, want nil", err)
	}

	if transcript.Discovery.Error != nil {
		t.Fatalf("server/discover error = %v, want nil", transcript.Discovery.Error)
	}
	if transcript.ToolsList.Error != nil {
		t.Fatalf("tools/list error = %v, want nil", transcript.ToolsList.Error)
	}
	if transcript.EchoCall.Error != nil {
		t.Fatalf("tools/call error = %v, want nil", transcript.EchoCall.Error)
	}
	if transcript.PreviewInputRequired.Error != nil {
		t.Fatalf("confirm_preview initial error = %v, want nil", transcript.PreviewInputRequired.Error)
	}
	if transcript.PreviewConfirmation.Error != nil {
		t.Fatalf("confirm_preview retry error = %v, want nil", transcript.PreviewConfirmation.Error)
	}
	if len(transcript.Exchanges) != 5 {
		t.Fatalf("exchange count = %d, want 5", len(transcript.Exchanges))
	}
	if transcript.Exchanges[0].Request.Method != "server/discover" {
		t.Fatalf("first exchange method = %q, want server/discover", transcript.Exchanges[0].Request.Method)
	}
	for _, exchange := range transcript.Exchanges {
		var params struct {
			Meta protocol.RequestMeta `json:"_meta"`
		}
		if err := json.Unmarshal(exchange.Request.Params, &params); err != nil {
			t.Fatalf("unmarshal %s request params: %v", exchange.Name, err)
		}
		if params.Meta.ProtocolVersion != protocol.Version20260728 {
			t.Fatalf("%s protocol version = %q, want %s", exchange.Name, params.Meta.ProtocolVersion, protocol.Version20260728)
		}
		if params.Meta.ClientInfo == nil || params.Meta.ClientInfo.Name != "mcp-from-scratch-host" {
			t.Fatalf("%s client info = %#v, want mcp-from-scratch-host", exchange.Name, params.Meta.ClientInfo)
		}
		if params.Meta.ClientCapabilities == nil {
			t.Fatalf("%s client capabilities = nil, want object", exchange.Name)
		}
	}
	if len(transcript.DiscoveredTools) != 2 {
		t.Fatalf("discovered tool count = %d, want 2", len(transcript.DiscoveredTools))
	}
	if transcript.DiscoveredTools[0].Name != "confirm_preview" || transcript.DiscoveredTools[1].Name != "echo" {
		t.Fatalf("discovered tool names = [%s %s], want [confirm_preview echo]", transcript.DiscoveredTools[0].Name, transcript.DiscoveredTools[1].Name)
	}

	var echo struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(transcript.EchoCall.Result, &echo); err != nil {
		t.Fatalf("unmarshal echo result: %v", err)
	}
	if len(echo.Content) != 1 {
		t.Fatalf("echo content count = %d, want 1", len(echo.Content))
	}
	if echo.Content[0].Text != "hello from fake model" {
		t.Fatalf("echo text = %q, want hello from fake model", echo.Content[0].Text)
	}

	var required struct {
		ResultType   string `json:"resultType"`
		RequestState string `json:"requestState"`
	}
	if err := json.Unmarshal(transcript.PreviewInputRequired.Result, &required); err != nil {
		t.Fatalf("unmarshal input-required result: %v", err)
	}
	if required.ResultType != protocol.ResultTypeInputRequired || required.RequestState == "" {
		t.Fatalf("input-required result = %#v, want input_required with state", required)
	}

	initialRequest := transcript.Exchanges[3].Request
	retryRequest := transcript.Exchanges[4].Request
	if initialRequest.ID == nil || retryRequest.ID == nil || *initialRequest.ID == *retryRequest.ID {
		t.Fatalf("MRTR request IDs = %v and %v, want different IDs", initialRequest.ID, retryRequest.ID)
	}
	var retryParams struct {
		RequestState string                     `json:"requestState"`
		Responses    map[string]json.RawMessage `json:"inputResponses"`
	}
	if err := json.Unmarshal(retryRequest.Params, &retryParams); err != nil {
		t.Fatalf("unmarshal confirm_preview retry params: %v", err)
	}
	if retryParams.RequestState != required.RequestState {
		t.Fatalf("retry requestState = %q, want exact input-required state", retryParams.RequestState)
	}
	if _, ok := retryParams.Responses["confirm_preview"]; !ok {
		t.Fatalf("retry inputResponses = %#v, want confirm_preview", retryParams.Responses)
	}

	var confirmed struct {
		ResultType string `json:"resultType"`
		Content    []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(transcript.PreviewConfirmation.Result, &confirmed); err != nil {
		t.Fatalf("unmarshal confirmation result: %v", err)
	}
	if confirmed.ResultType != protocol.ResultTypeComplete || len(confirmed.Content) != 1 {
		t.Fatalf("confirmation result = %#v, want complete content", confirmed)
	}
}

func TestRunHTTPDemoTalksToStatelessServer(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(mcpserver.New().HTTPHandler())
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transcript, err := RunHTTPDemo(ctx, server.URL)
	if err != nil {
		t.Fatalf("RunHTTPDemo() error = %v", err)
	}
	if len(transcript.Exchanges) != 5 {
		t.Fatalf("exchange count = %d, want 5", len(transcript.Exchanges))
	}
	for _, exchange := range transcript.Exchanges {
		if exchange.Response == nil || exchange.Response.Error != nil {
			t.Fatalf("%s response = %#v, want success", exchange.Name, exchange.Response)
		}
	}
}

func TestHTTPRPCClientReadsRequestScopedSSEUntilFinalResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var rpcRequest protocol.Request
		if err := json.NewDecoder(request.Body).Decode(&rpcRequest); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		writer.Header().Set("Content-Type", protocol.MediaTypeSSE)
		for _, message := range []any{
			protocol.Notification{
				JSONRPC: "2.0",
				Method:  "notifications/progress",
				Params:  json.RawMessage(`{"progressToken":"demo","progress":1}`),
			},
			protocol.Response{
				JSONRPC: "2.0",
				ID:      rpcRequest.ID,
				Result:  json.RawMessage(`{"resultType":"complete"}`),
			},
		} {
			encoded, err := json.Marshal(message)
			if err != nil {
				t.Errorf("encode SSE message: %v", err)
				return
			}
			if _, err := fmt.Fprintf(writer, "data: %s\n\n", encoded); err != nil {
				t.Errorf("write SSE message: %v", err)
				return
			}
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params, err := json.Marshal(protocol.RequestParams{Meta: clientRequestMeta()})
	if err != nil {
		t.Fatalf("encode params: %v", err)
	}
	client := &httpRPCClient{ctx: ctx, endpoint: server.URL, client: http.DefaultClient}
	response, err := client.call(protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(91),
		Method:  "tools/list",
		Params:  params,
	})
	if err != nil {
		t.Fatalf("call() error = %v", err)
	}
	if response.ID == nil || response.ID.String() != "91" || len(response.Result) == 0 {
		t.Fatalf("response = %#v, want final response ID 91", response)
	}
}

func TestHTTPToolsSubscriptionRefreshesListOnChange(t *testing.T) {
	t.Parallel()

	server := mcpserver.New()
	httpServer := httptest.NewServer(server.HTTPHandler())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	subscription, err := OpenHTTPToolsSubscription(ctx, httpServer.URL, 61)
	if err != nil {
		t.Fatalf("OpenHTTPToolsSubscription() error = %v", err)
	}
	defer subscription.Close()
	if acknowledged := subscription.Acknowledged(); acknowledged.Method != "notifications/subscriptions/acknowledged" {
		t.Fatalf("acknowledged method = %q", acknowledged.Method)
	}

	if err := server.RegisterEchoTool("late_echo"); err != nil {
		t.Fatalf("RegisterEchoTool() error = %v", err)
	}
	changed, refreshed, err := subscription.RefreshOnNextToolsListChanged(62)
	if err != nil {
		t.Fatalf("RefreshOnNextToolsListChanged() error = %v", err)
	}
	if changed.Method != "notifications/tools/list_changed" {
		t.Fatalf("changed method = %q", changed.Method)
	}
	var listed toolsListResult
	if err := decodeCompleteResult(refreshed.Result, &listed); err != nil {
		t.Fatalf("decode refreshed tools/list: %v", err)
	}
	if len(listed.Tools) != 3 || listed.Tools[1].Name != "echo" || listed.Tools[2].Name != "late_echo" {
		t.Fatalf("refreshed tools = %#v, want confirm_preview, echo, late_echo", listed.Tools)
	}
}

func TestToolDescriptionsBecomeOpenAICompatibleTools(t *testing.T) {
	t.Parallel()

	inputSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": "Text to return.",
			},
		},
		"required": []string{"text"},
	}

	tools := openAIToolsFromToolDescriptions([]ToolDescription{
		{
			Name:        "echo",
			Description: "Return the text argument back to the caller.",
			InputSchema: inputSchema,
		},
	})

	if len(tools) != 1 {
		t.Fatalf("tool count = %d, want 1", len(tools))
	}
	if tools[0].Type != "function" {
		t.Fatalf("tool type = %q, want function", tools[0].Type)
	}
	if tools[0].Function.Name != "echo" {
		t.Fatalf("function name = %q, want echo", tools[0].Function.Name)
	}
	if tools[0].Function.Description != "Return the text argument back to the caller." {
		t.Fatalf("function description = %q, want echo description", tools[0].Function.Description)
	}
	if !reflect.DeepEqual(tools[0].Function.Parameters, inputSchema) {
		t.Fatalf("function parameters = %#v, want %#v", tools[0].Function.Parameters, inputSchema)
	}
}

func TestListAllToolsFollowsOpaqueCursors(t *testing.T) {
	t.Parallel()

	client := &pagedToolsClient{responses: []protocol.Response{
		{JSONRPC: "2.0", ID: protocol.ID(2), Result: json.RawMessage(`{"resultType":"complete","tools":[{"name":"alpha"}],"nextCursor":"opaque-1"}`)},
		{JSONRPC: "2.0", ID: protocol.ID(2), Result: json.RawMessage(`{"resultType":"complete","tools":[{"name":"bravo"}]}`)},
	}}
	params, err := json.Marshal(protocol.RequestParams{Meta: clientRequestMeta()})
	if err != nil {
		t.Fatalf("encode params: %v", err)
	}
	tools, firstRequest, _, err := listAllTools(client, params, 2)
	if err != nil {
		t.Fatalf("listAllTools() error = %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "alpha" || tools[1].Name != "bravo" {
		t.Fatalf("tools = %#v, want alpha, bravo", tools)
	}
	if firstRequest.ID == nil || firstRequest.ID.String() != "2" {
		t.Fatalf("first request ID = %v, want 2", firstRequest.ID)
	}
	if len(client.requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(client.requests))
	}
	var secondParams struct {
		Cursor string `json:"cursor"`
	}
	if err := json.Unmarshal(client.requests[1].Params, &secondParams); err != nil {
		t.Fatalf("decode second params: %v", err)
	}
	if secondParams.Cursor != "opaque-1" {
		t.Fatalf("second cursor = %q, want opaque-1", secondParams.Cursor)
	}
}

type pagedToolsClient struct {
	requests  []protocol.Request
	responses []protocol.Response
}

func (c *pagedToolsClient) call(request protocol.Request) (protocol.Response, error) {
	c.requests = append(c.requests, request)
	if len(c.responses) == 0 {
		return protocol.Response{}, fmt.Errorf("unexpected tools/list request")
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func TestFakeModelDecisionChoosesEchoTool(t *testing.T) {
	t.Parallel()

	decision, err := fakeModelDecision(
		[]ToolDescription{
			{
				Name: "echo",
			},
		},
		"hello from fake model",
	)
	if err != nil {
		t.Fatalf("fakeModelDecision() error = %v, want nil", err)
	}

	if decision.ToolName != "echo" {
		t.Fatalf("tool name = %q, want echo", decision.ToolName)
	}

	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(decision.Arguments, &args); err != nil {
		t.Fatalf("unmarshal arguments: %v", err)
	}
	if args.Text != "hello from fake model" {
		t.Fatalf("argument text = %q, want hello from fake model", args.Text)
	}
}

func TestDecodeCompleteResultAcceptsLegacyResultWithoutType(t *testing.T) {
	t.Parallel()

	var result toolsListResult
	err := decodeCompleteResult(json.RawMessage(`{"tools":[]}`), &result)
	if err != nil {
		t.Fatalf("decodeCompleteResult() error = %v, want nil", err)
	}
	if result.Tools == nil {
		t.Fatal("tools = nil, want decoded empty list")
	}
}

func TestDecodeCompleteResultRejectsUnsupportedType(t *testing.T) {
	t.Parallel()

	err := decodeCompleteResult(json.RawMessage(`{"resultType":"input-required"}`), &struct{}{})
	if err == nil {
		t.Fatal("decodeCompleteResult() error = nil, want unsupported result type error")
	}
}

func TestDecodeCompleteResultRejectsMalformedEnvelopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "null result", raw: json.RawMessage(`null`)},
		{name: "null result type", raw: json.RawMessage(`{"resultType":null}`)},
		{name: "empty result type", raw: json.RawMessage(`{"resultType":""}`)},
		{name: "non-string result type", raw: json.RawMessage(`{"resultType":1}`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if err := decodeCompleteResult(test.raw, &struct{}{}); err == nil {
				t.Fatal("decodeCompleteResult() error = nil, want malformed envelope error")
			}
		})
	}
}

func TestRPCClientCallReturnsJSONRPCError(t *testing.T) {
	t.Parallel()

	var request bytes.Buffer
	client := rpcClient{
		encoder: json.NewEncoder(&request),
		decoder: json.NewDecoder(strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"unsupported protocol version","data":{"requested":"2025-03-26","supported":["2026-07-28"]}}}`,
		)),
	}

	response, err := client.call(protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "tools/list",
	})
	if err == nil {
		t.Fatal("call() error = nil, want JSON-RPC error")
	}
	if response.Error == nil {
		t.Fatal("call() response error = nil, want decoded JSON-RPC error")
	}
	if err != response.Error {
		t.Fatalf("call() error = %T %v, want response error", err, err)
	}
	for _, want := range []string{"-32022", "unsupported protocol version", "2025-03-26", "2026-07-28"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("call() error = %q, want substring %q", err, want)
		}
	}
}

func projectRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate current test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
