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

func TestServer_DiscoverReturnsSupportedVersionAndServerInfo(t *testing.T) {
	t.Parallel()

	server := New()
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "server/discover",
		Params: json.RawMessage(`{
			"_meta": {
				"io.modelcontextprotocol/protocolVersion": "2026-07-28",
				"io.modelcontextprotocol/clientInfo": {
					"name": "test-host",
					"version": "0.1.0"
				},
				"io.modelcontextprotocol/clientCapabilities": {}
			}
		}`),
	})

	if response.Error != nil {
		t.Fatalf("Handle(server/discover) error = %v, want nil", response.Error)
	}

	var result struct {
		ResultType        string       `json:"resultType"`
		SupportedVersions []string     `json:"supportedVersions"`
		Capabilities      capabilities `json:"capabilities"`
		TTLMillis         int          `json:"ttlMs"`
		CacheScope        string       `json:"cacheScope"`
		Meta              struct {
			ServerInfo protocol.Implementation `json:"io.modelcontextprotocol/serverInfo"`
		} `json:"_meta"`
	}
	mustUnmarshalResult(t, response.Result, &result)

	if result.ResultType != "complete" {
		t.Fatalf("resultType = %q, want complete", result.ResultType)
	}
	if len(result.SupportedVersions) != 1 || result.SupportedVersions[0] != "2026-07-28" {
		t.Fatalf("supportedVersions = %v, want [2026-07-28]", result.SupportedVersions)
	}
	if result.Meta.ServerInfo.Name != "mcp-from-scratch" {
		t.Fatalf("serverInfo.name = %q, want %q", result.Meta.ServerInfo.Name, "mcp-from-scratch")
	}
	if result.Capabilities.Tools == nil {
		t.Fatal("capabilities.tools = nil, want object")
	}
	if result.TTLMillis != 3600000 {
		t.Fatalf("ttlMs = %d, want 3600000", result.TTLMillis)
	}
	if result.CacheScope != "public" {
		t.Fatalf("cacheScope = %q, want public", result.CacheScope)
	}
}

func TestServer_RejectsIncompleteRequestMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		params json.RawMessage
	}{
		{name: "missing metadata", method: "tools/list"},
		{
			name:   "missing protocol version",
			method: "tools/call",
			params: json.RawMessage(`{"_meta":{"io.modelcontextprotocol/clientCapabilities":{}}}`),
		},
		{
			name:   "missing client capabilities",
			method: "server/discover",
			params: json.RawMessage(`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			response := New().Handle(context.Background(), protocol.Request{
				JSONRPC: "2.0",
				ID:      protocol.ID(2),
				Method:  tt.method,
				Params:  tt.params,
			})

			if response.Error == nil {
				t.Fatalf("Handle(%s) error = nil, want invalid params error", tt.method)
			}
			if response.Error.Code != protocol.CodeInvalidParams {
				t.Fatalf("error code = %d, want %d", response.Error.Code, protocol.CodeInvalidParams)
			}
		})
	}
}

func TestServer_RejectsUnsupportedProtocolVersion(t *testing.T) {
	t.Parallel()

	server := New()
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "server/discover",
		Params: json.RawMessage(`{
			"_meta": {
				"io.modelcontextprotocol/protocolVersion": "2025-06-18",
				"io.modelcontextprotocol/clientCapabilities": {}
			}
		}`),
	})

	if response.Error == nil {
		t.Fatal("Handle(server/discover) error = nil, want unsupported version error")
	}
	if response.Error.Code != -32022 {
		t.Fatalf("error code = %d, want -32022", response.Error.Code)
	}

	encoded, err := json.Marshal(response.Error)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var errorObject struct {
		Data struct {
			Supported []string `json:"supported"`
			Requested string   `json:"requested"`
		} `json:"data"`
	}
	if err := json.Unmarshal(encoded, &errorObject); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if errorObject.Data.Requested != "2025-06-18" {
		t.Fatalf("data.requested = %q, want 2025-06-18", errorObject.Data.Requested)
	}
	if len(errorObject.Data.Supported) != 1 || errorObject.Data.Supported[0] != "2026-07-28" {
		t.Fatalf("data.supported = %v, want [2026-07-28]", errorObject.Data.Supported)
	}
}

func TestServer_LegacyInitializeIsNotAvailable(t *testing.T) {
	t.Parallel()

	response := New().Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "initialize",
		Params:  modernRequestParams(t, `{}`),
	})

	if response.Error == nil {
		t.Fatal("Handle(initialize) error = nil, want method-not-found error")
	}
	if response.Error.Code != protocol.CodeMethodNotFound {
		t.Fatalf("error code = %d, want %d", response.Error.Code, protocol.CodeMethodNotFound)
	}
}

func TestServer_ListsEchoTool(t *testing.T) {
	t.Parallel()

	server := New()
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(2),
		Method:  "tools/list",
		Params:  modernRequestParams(t, `{}`),
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

	var cacheHints struct {
		TTLMillis  int    `json:"ttlMs"`
		CacheScope string `json:"cacheScope"`
	}
	mustUnmarshalResult(t, response.Result, &cacheHints)
	if cacheHints.TTLMillis != 300000 {
		t.Fatalf("ttlMs = %d, want 300000", cacheHints.TTLMillis)
	}
	if cacheHints.CacheScope != protocol.CacheScopePublic {
		t.Fatalf("cacheScope = %q, want public", cacheHints.CacheScope)
	}
}

func TestServer_SuccessfulResultsIdentifyServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		params string
	}{
		{name: "list tools", method: "tools/list", params: `{}`},
		{name: "call tool", method: "tools/call", params: `{"name":"echo","arguments":{"text":"hello"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			response := New().Handle(context.Background(), protocol.Request{
				JSONRPC: "2.0",
				ID:      protocol.ID(1),
				Method:  tt.method,
				Params:  modernRequestParams(t, tt.params),
			})
			if response.Error != nil {
				t.Fatalf("Handle(%s) error = %v, want nil", tt.method, response.Error)
			}

			var result protocol.Result
			mustUnmarshalResult(t, response.Result, &result)
			if result.ResultType != protocol.ResultTypeComplete {
				t.Fatalf("resultType = %q, want complete", result.ResultType)
			}
			if result.Meta.ServerInfo.Name != "mcp-from-scratch" {
				t.Fatalf("serverInfo.name = %q, want mcp-from-scratch", result.Meta.ServerInfo.Name)
			}
		})
	}
}

func TestServer_CallsEchoTool(t *testing.T) {
	t.Parallel()

	server := New()
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(3),
		Method:  "tools/call",
		Params:  modernRequestParams(t, `{"name":"echo","arguments":{"text":"hello mcp"}}`),
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
		Params:  modernRequestParams(t, `{}`),
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

func modernRequestParams(t *testing.T, raw string) json.RawMessage {
	t.Helper()

	params := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		t.Fatalf("unmarshal request params: %v", err)
	}
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion": protocol.Version20260728,
		"io.modelcontextprotocol/clientInfo": map[string]string{
			"name":    "test-host",
			"version": "0.1.0",
		},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}

	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal request params: %v", err)
	}
	return encoded
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
		Params:  modernRequestParams(t, `{}`),
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

func TestServer_ListsToolsByName(t *testing.T) {
	t.Parallel()

	response := New(
		fakeTool{name: "zeta", description: "Last tool."},
		fakeTool{name: "alpha", description: "First tool."},
	).Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "tools/list",
		Params:  modernRequestParams(t, `{}`),
	})

	if response.Error != nil {
		t.Fatalf("Handle(tools/list) error = %v, want nil", response.Error)
	}
	var result toolsListResult
	mustUnmarshalResult(t, response.Result, &result)
	if len(result.Tools) != 2 {
		t.Fatalf("tool count = %d, want 2", len(result.Tools))
	}
	if result.Tools[0].Name != "alpha" || result.Tools[1].Name != "zeta" {
		t.Fatalf("tool names = [%s %s], want [alpha zeta]", result.Tools[0].Name, result.Tools[1].Name)
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
		Params:  modernRequestParams(t, `{"name":"reverse","arguments":{"text":"hello"}}`),
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
		Params:  modernRequestParams(t, `{"name":"reverse","arguments":"not an object"}`),
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
		Params:  modernRequestParams(t, `{"arguments":{"text":"hello"}}`),
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
		Params:  modernRequestParams(t, `{"name":"missing","arguments":{}}`),
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
		Params:  modernRequestParams(t, `{"name":"validated","arguments":{}}`),
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
		Params:  modernRequestParams(t, `{"name":"validated","arguments":{"text":"hello"}}`),
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
		Params:  modernRequestParams(t, `{"name":"validated","arguments":"not an object"}`),
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
		Params:  modernRequestParams(t, `{"name":"validated","arguments":{"text":123}}`),
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
		Params:  modernRequestParams(t, `{"name":"echo","arguments":"not an object"}`),
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
