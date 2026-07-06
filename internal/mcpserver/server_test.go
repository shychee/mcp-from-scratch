package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

type schemaValidatedTool struct {
	called bool
}

func (t *schemaValidatedTool) Definition() tool {
	return tool{
		Name:        "validated",
		Description: "Require text.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type": "string",
				},
			},
			"required": []string{"text"},
		},
	}
}

func (t *schemaValidatedTool) Call(_ json.RawMessage) (toolCallResult, error) {
	t.called = true
	return toolCallResult{}, nil
}

func TestServer_InitializeReturnsServerInfo(t *testing.T) {
	t.Parallel()

	server := New()
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-06-18"}`),
	})

	if response.Error != nil {
		t.Fatalf("Handle(initialize) error = %v, want nil", response.Error)
	}

	var result initializeResult
	mustUnmarshalResult(t, response.Result, &result)

	if result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("protocolVersion = %q, want %q", result.ProtocolVersion, "2025-06-18")
	}
	if result.ServerInfo.Name != "mcp-from-scratch" {
		t.Fatalf("serverInfo.name = %q, want %q", result.ServerInfo.Name, "mcp-from-scratch")
	}
}

func TestServer_ListsEchoTool(t *testing.T) {
	t.Parallel()

	server := New()
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(2),
		Method:  "tools/list",
	})

	if response.Error != nil {
		t.Fatalf("Handle(tools/list) error = %v, want nil", response.Error)
	}

	var result toolsListResult
	mustUnmarshalResult(t, response.Result, &result)

	if len(result.Tools) != 1 {
		t.Fatalf("tool count = %d, want 1", len(result.Tools))
	}
	if result.Tools[0].Name != "echo" {
		t.Fatalf("tool name = %q, want %q", result.Tools[0].Name, "echo")
	}
	if result.Tools[0].InputSchema["type"] != "object" {
		t.Fatalf("inputSchema.type = %v, want object", result.Tools[0].InputSchema["type"])
	}
}

func TestServer_CallsEchoTool(t *testing.T) {
	t.Parallel()

	server := New()
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(3),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"echo","arguments":{"text":"hello mcp"}}`),
	})

	if response.Error != nil {
		t.Fatalf("Handle(tools/call) error = %v, want nil", response.Error)
	}

	var result toolCallResult
	mustUnmarshalResult(t, response.Result, &result)

	if len(result.Content) != 1 {
		t.Fatalf("content count = %d, want 1", len(result.Content))
	}
	if result.Content[0].Type != "text" {
		t.Fatalf("content type = %q, want text", result.Content[0].Type)
	}
	if result.Content[0].Text != "hello mcp" {
		t.Fatalf("content text = %q, want hello mcp", result.Content[0].Text)
	}
}

func TestServer_UnknownMethodReturnsJSONRPCError(t *testing.T) {
	t.Parallel()

	server := New()
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(4),
		Method:  "unknown/method",
	})

	if response.Error == nil {
		t.Fatal("Handle(unknown/method) error = nil, want JSON-RPC method-not-found error")
	}
	if response.Error.Code != -32601 {
		t.Fatalf("error code = %d, want -32601", response.Error.Code)
	}
}

func mustUnmarshalResult(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()

	if len(raw) == 0 {
		t.Fatal("result is empty")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
}

type fakeTool struct {
	name        string
	description string
}

func (t fakeTool) Definition() tool {
	return tool{
		Name:        t.name,
		Description: t.description,
		InputSchema: map[string]any{
			"type": "object",
		},
	}
}

func (t fakeTool) Call(_ json.RawMessage) (toolCallResult, error) {
	return toolCallResult{
		Content: []contentBlock{
			{
				Type: "text",
				Text: t.name + " called",
			},
		},
	}, nil
}

func TestServer_ListsRegisteredTool(t *testing.T) {
	t.Parallel()

	server := New(fakeTool{
		name:        "reverse",
		description: "Reverse text.",
	})

	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "tools/list",
	})

	if response.Error != nil {
		t.Fatalf("Handle(tools/list) error = %v, want nil", response.Error)
	}

	var result toolsListResult
	mustUnmarshalResult(t, response.Result, &result)

	if len(result.Tools) != 1 {
		t.Fatalf("tool count = %d, want 1", len(result.Tools))
	}
	if result.Tools[0].Name != "reverse" {
		t.Fatalf("tool name = %q, want %q", result.Tools[0].Name, "reverse")
	}
}

func TestServer_CallsRegisteredTool(t *testing.T) {
	t.Parallel()

	server := New(fakeTool{
		name:        "reverse",
		description: "Reverse text.",
	})
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(2),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"reverse","arguments":{"text":"hello"}}`),
	})

	if response.Error != nil {
		t.Fatalf("Handle(tools/call) error = %v, want nil", response.Error)
	}

	var result toolCallResult
	mustUnmarshalResult(t, response.Result, &result)

	if len(result.Content) != 1 {
		t.Fatalf("content count = %d, want 1", len(result.Content))
	}
	if result.Content[0].Text != "reverse called" {
		t.Fatalf("content text = %q, want reverse called", result.Content[0].Text)
	}
}

func TestServer_CallToolRejectsNonObjectArgumentsForObjectSchema(t *testing.T) {
	t.Parallel()

	server := New(fakeTool{
		name:        "reverse",
		description: "Reverse text.",
	})
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(2),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"reverse","arguments":"not an object"}`),
	})

	if response.Error == nil {
		t.Fatal("Handle(tools/call) error = nil, want invalid params error")
	}
	if response.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("error code = %d, want %d", response.Error.Code, protocol.CodeInvalidParams)
	}
	if response.Error.Message != "tool arguments must be an object" {
		t.Fatalf("error message = %q, want tool arguments object error", response.Error.Message)
	}
}

func TestServer_CallToolRejectsMissingToolName(t *testing.T) {
	t.Parallel()

	server := New()
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"arguments":{"text":"hello"}}`),
	})

	if response.Error == nil {
		t.Fatal("Handle(tools/call) error = nil, want invalid params error")
	}
	if response.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("error code = %d, want %d", response.Error.Code, protocol.CodeInvalidParams)
	}
	if response.Error.Message != "missing tool name" {
		t.Fatalf("error message = %q, want missing tool name", response.Error.Message)
	}
}

func TestServer_CallToolRejectsUnknownTool(t *testing.T) {
	t.Parallel()

	server := New()
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"missing","arguments":{}}`),
	})

	if response.Error == nil {
		t.Fatal("Handle(tools/call) error = nil, want invalid params error")
	}
	if response.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("error code = %d, want %d", response.Error.Code, protocol.CodeInvalidParams)
	}
	if response.Error.Message != `unknown tool "missing"` {
		t.Fatalf("error message = %q, want unknown tool", response.Error.Message)
	}
}

func TestServer_CallToolRejectsMissingRequiredSchemaArgument(t *testing.T) {
	t.Parallel()

	tool := &schemaValidatedTool{}
	server := New(tool)

	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"validated","arguments":{}}`),
	})

	if response.Error == nil {
		t.Fatal("Handle(tools/call) error = nil, want invalid params error")
	}
	if response.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("error code = %d, want %d", response.Error.Code, protocol.CodeInvalidParams)
	}
	if tool.called {
		t.Fatal("tool was called, want schema validation to reject before dispatch")
	}
}

func TestServer_CallToolAcceptsPresentRequiredSchemaArgument(t *testing.T) {
	t.Parallel()

	tool := &schemaValidatedTool{}
	server := New(tool)

	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"validated","arguments":{"text":"hello"}}`),
	})

	if response.Error != nil {
		t.Fatalf("Handle(tools/call) error = %v, want nil", response.Error)
	}
	if !tool.called {
		t.Fatal("tool was not called, want schema validation to allow dispatch")
	}
}

func TestServer_CallToolRejectsNonObjectSchemaArguments(t *testing.T) {
	t.Parallel()

	tool := &schemaValidatedTool{}
	server := New(tool)

	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"validated","arguments":"not an object"}`),
	})

	if response.Error == nil {
		t.Fatal("Handle(tools/call) error = nil, want invalid params error")
	}
	if response.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("error code = %d, want %d", response.Error.Code, protocol.CodeInvalidParams)
	}
	if tool.called {
		t.Fatal("tool was called, want schema validation to reject before dispatch")
	}
}

func TestServer_CallToolRejectsWrongStringSchemaArgumentType(t *testing.T) {
	t.Parallel()

	tool := &schemaValidatedTool{}
	server := New(tool)

	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"validated","arguments":{"text":123}}`),
	})

	if response.Error == nil {
		t.Fatal("Handle(tools/call) error = nil, want invalid params error")
	}
	if response.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("error code = %d, want %d", response.Error.Code, protocol.CodeInvalidParams)
	}
	if tool.called {
		t.Fatal("tool was called, want schema validation to reject before dispatch")
	}
}

func TestServer_CallEchoRejectsMalformedArguments(t *testing.T) {
	t.Parallel()

	server := New()
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"echo","arguments":"not an object"}`),
	})

	if response.Error == nil {
		t.Fatal("Handle(tools/call) error = nil, want invalid params error")
	}
	if response.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("error code = %d, want %d", response.Error.Code, protocol.CodeInvalidParams)
	}
	if response.Error.Message != "tool arguments must be an object" {
		t.Fatalf("error message = %q, want tool arguments object error", response.Error.Message)
	}
}
