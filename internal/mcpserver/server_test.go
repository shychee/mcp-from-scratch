package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

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
