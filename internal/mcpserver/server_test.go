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

func (t *schemaValidatedTool) Call(_ toolCallInvocation) (toolCallResult, error) {
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
	if result.Capabilities.Tools["listChanged"] != true {
		t.Fatalf("capabilities.tools.listChanged = %v, want true", result.Capabilities.Tools["listChanged"])
	}
	if result.TTLMillis != 3600000 {
		t.Fatalf("ttlMs = %d, want 3600000", result.TTLMillis)
	}
	if result.CacheScope != "public" {
		t.Fatalf("cacheScope = %q, want public", result.CacheScope)
	}
}

func TestServer_DiscoverAdvertisesConfiguredExtensions(t *testing.T) {
	t.Parallel()

	server := New()
	if err := server.SetExtensions(protocol.Extensions{
		"io.modelcontextprotocol/tasks": json.RawMessage(`{"ttlMs":1000}`),
	}); err != nil {
		t.Fatalf("SetExtensions() error = %v", err)
	}
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "server/discover",
		Params:  modernRequestParams(t, `{}`),
	})
	if response.Error != nil {
		t.Fatalf("Handle(server/discover) error = %v", response.Error)
	}

	var result struct {
		Capabilities struct {
			Extensions protocol.Extensions `json:"extensions"`
		} `json:"capabilities"`
	}
	mustUnmarshalResult(t, response.Result, &result)
	if got := string(result.Capabilities.Extensions["io.modelcontextprotocol/tasks"]); got != `{"ttlMs":1000}` {
		t.Fatalf("tasks settings = %s", got)
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

func TestServer_ListsDefaultTools(t *testing.T) {
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

	if len(result.Tools) != 2 {
		t.Fatalf("tool count = %d, want 2", len(result.Tools))
	}
	if result.Tools[0].Name != "confirm_preview" || result.Tools[1].Name != "echo" {
		t.Fatalf("tool names = [%s %s], want [confirm_preview echo]", result.Tools[0].Name, result.Tools[1].Name)
	}
	if result.Tools[1].InputSchema["type"] != "object" {
		t.Fatalf("echo inputSchema.type = %v, want object", result.Tools[1].InputSchema["type"])
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

func TestServer_ConfirmPreviewReturnsInputRequiredAndFreshRetryCompletes(t *testing.T) {
	t.Parallel()

	initial := New().Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(10),
		Method:  "tools/call",
		Params:  confirmPreviewRequestParams(t, "delete demo item", "", false),
	})
	if initial.Error != nil {
		t.Fatalf("initial confirm_preview error = %v, want nil", initial.Error)
	}

	var required struct {
		ResultType    string `json:"resultType"`
		InputRequests map[string]struct {
			Method string `json:"method"`
			Params struct {
				Mode            string         `json:"mode"`
				Message         string         `json:"message"`
				RequestedSchema map[string]any `json:"requestedSchema"`
			} `json:"params"`
		} `json:"inputRequests"`
		RequestState string `json:"requestState"`
	}
	mustUnmarshalResult(t, initial.Result, &required)
	if required.ResultType != "input_required" {
		t.Fatalf("resultType = %q, want input_required", required.ResultType)
	}
	request, ok := required.InputRequests["confirm_preview"]
	if !ok || len(required.InputRequests) != 1 {
		t.Fatalf("inputRequests = %#v, want only confirm_preview", required.InputRequests)
	}
	if request.Method != "elicitation/create" {
		t.Fatalf("input request method = %q, want elicitation/create", request.Method)
	}
	if request.Params.Mode != "form" || request.Params.Message == "" {
		t.Fatalf("input request params = %#v, want form message", request.Params)
	}
	if request.Params.RequestedSchema["type"] != "object" {
		t.Fatalf("requestedSchema.type = %v, want object", request.Params.RequestedSchema["type"])
	}
	properties, ok := request.Params.RequestedSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("requestedSchema.properties = %T, want object", request.Params.RequestedSchema["properties"])
	}
	confirm, ok := properties["confirm"].(map[string]any)
	if !ok || confirm["type"] != "boolean" {
		t.Fatalf("requestedSchema.properties.confirm = %#v, want boolean", properties["confirm"])
	}
	if required.RequestState == "" {
		t.Fatal("requestState is empty, want opaque state")
	}

	retry := New().Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(11),
		Method:  "tools/call",
		Params:  confirmPreviewRequestParams(t, "delete demo item", required.RequestState, true),
	})
	if retry.Error != nil {
		t.Fatalf("fresh confirm_preview retry error = %v, want nil", retry.Error)
	}
	var result struct {
		ResultType string `json:"resultType"`
		Content    []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	mustUnmarshalResult(t, retry.Result, &result)
	if result.ResultType != protocol.ResultTypeComplete {
		t.Fatalf("retry resultType = %q, want complete", result.ResultType)
	}
	if len(result.Content) != 1 || result.Content[0].Text == "" {
		t.Fatalf("retry content = %#v, want confirmed action result", result.Content)
	}
}

func TestServer_ConfirmPreviewRequiresFormCapability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		capabilities map[string]any
	}{
		{name: "missing elicitation", capabilities: map[string]any{}},
		{name: "url only", capabilities: map[string]any{
			"elicitation": map[string]any{"url": map[string]any{}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := New().Handle(context.Background(), protocol.Request{
				JSONRPC: "2.0",
				ID:      protocol.ID(12),
				Method:  "tools/call",
				Params: modernRequestParamsWithCapabilities(
					t,
					`{"name":"confirm_preview","arguments":{"preview":"delete demo item"}}`,
					tt.capabilities,
				),
			})
			if response.Error == nil {
				t.Fatal("confirm_preview without form capability error = nil, want protocol error")
			}
			if response.Error.Code != protocol.CodeMissingRequiredClientCapability {
				t.Fatalf("error code = %d, want %d", response.Error.Code, protocol.CodeMissingRequiredClientCapability)
			}
			if len(response.Result) != 0 {
				t.Fatalf("confirm_preview without form capability result = %s, want empty", response.Result)
			}
		})
	}
}

func TestServer_ConfirmPreviewHandlesElicitationOutcomes(t *testing.T) {
	t.Parallel()

	initial := New().Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(15),
		Method:  "tools/call",
		Params:  confirmPreviewRequestParams(t, "demo preview", "", false),
	})
	var required struct {
		RequestState string `json:"requestState"`
	}
	mustUnmarshalResult(t, initial.Result, &required)

	tests := []struct {
		name    string
		action  string
		confirm bool
		want    string
	}{
		{name: "accept false", action: "accept", want: "preview was not confirmed"},
		{name: "decline", action: "decline", want: "preview declined"},
		{name: "cancel", action: "cancel", want: "preview canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := New().Handle(context.Background(), protocol.Request{
				JSONRPC: "2.0",
				ID:      protocol.ID(16),
				Method:  "tools/call",
				Params:  confirmPreviewOutcomeParams(t, "demo preview", required.RequestState, tt.action, tt.confirm),
			})
			if response.Error != nil {
				t.Fatalf("outcome error = %v", response.Error)
			}
			var result struct {
				Content []contentBlock `json:"content"`
			}
			mustUnmarshalResult(t, response.Result, &result)
			if len(result.Content) != 1 || result.Content[0].Text != tt.want {
				t.Fatalf("content = %#v, want %q", result.Content, tt.want)
			}
		})
	}
}

func TestServer_ConfirmPreviewRejectsInvalidRequestStateWithoutActionResult(t *testing.T) {
	t.Parallel()

	initial := New().Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(13),
		Method:  "tools/call",
		Params:  confirmPreviewRequestParams(t, "delete demo item", "", false),
	})
	var required struct {
		RequestState string `json:"requestState"`
	}
	mustUnmarshalResult(t, initial.Result, &required)

	tests := []struct {
		name            string
		preview         string
		requestState    string
		includeResponse bool
	}{
		{name: "missing state", preview: "delete demo item", includeResponse: true},
		{name: "tampered state", preview: "delete demo item", requestState: "tampered", includeResponse: true},
		{name: "modified preview", preview: "delete another item", requestState: required.RequestState, includeResponse: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := New().Handle(context.Background(), protocol.Request{
				JSONRPC: "2.0",
				ID:      protocol.ID(14),
				Method:  "tools/call",
				Params:  confirmPreviewRequestParams(t, tt.preview, tt.requestState, tt.includeResponse),
			})
			if response.Error == nil {
				t.Fatal("invalid requestState error = nil, want protocol error")
			}
			if response.Error.Code != protocol.CodeInvalidParams {
				t.Fatalf("error code = %d, want %d", response.Error.Code, protocol.CodeInvalidParams)
			}
			if len(response.Result) != 0 {
				t.Fatalf("invalid requestState result = %s, want empty", response.Result)
			}
		})
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
	return modernRequestParamsWithCapabilities(t, raw, map[string]any{})
}

func modernRequestParamsWithCapabilities(t *testing.T, raw string, capabilities map[string]any) json.RawMessage {
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
		"io.modelcontextprotocol/clientCapabilities": capabilities,
	}

	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal request params: %v", err)
	}
	return encoded
}

func confirmPreviewRequestParams(t *testing.T, preview, requestState string, includeResponse bool) json.RawMessage {
	t.Helper()

	params := map[string]any{
		"name": "confirm_preview",
		"arguments": map[string]any{
			"preview": preview,
		},
	}
	if requestState != "" {
		params["requestState"] = requestState
	}
	if includeResponse {
		params["inputResponses"] = map[string]any{
			"confirm_preview": map[string]any{
				"action": "accept",
				"content": map[string]any{
					"confirm": true,
				},
			},
		}
	}

	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal confirm_preview params: %v", err)
	}
	return modernRequestParamsWithCapabilities(t, string(encoded), map[string]any{
		"elicitation": map[string]any{
			"form": map[string]any{},
		},
	})
}

func confirmPreviewOutcomeParams(t *testing.T, preview, requestState, action string, confirm bool) json.RawMessage {
	t.Helper()

	inputResponse := map[string]any{"action": action}
	if action == "accept" {
		inputResponse["content"] = map[string]any{"confirm": confirm}
	}
	params := map[string]any{
		"name":         "confirm_preview",
		"arguments":    map[string]any{"preview": preview},
		"requestState": requestState,
		"inputResponses": map[string]any{
			"confirm_preview": inputResponse,
		},
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal confirm_preview outcome params: %v", err)
	}
	return modernRequestParamsWithCapabilities(t, string(encoded), map[string]any{
		"elicitation": map[string]any{"form": map[string]any{}},
	})
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

func (t fakeTool) Call(_ toolCallInvocation) (toolCallResult, error) {
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
