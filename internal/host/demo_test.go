package host

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

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
	if len(transcript.Exchanges) != 3 {
		t.Fatalf("exchange count = %d, want 3", len(transcript.Exchanges))
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
	if len(transcript.DiscoveredTools) != 1 {
		t.Fatalf("discovered tool count = %d, want 1", len(transcript.DiscoveredTools))
	}
	if transcript.DiscoveredTools[0].Name != "echo" {
		t.Fatalf("discovered tool name = %q, want echo", transcript.DiscoveredTools[0].Name)
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
