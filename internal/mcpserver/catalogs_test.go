package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

func TestServer_ResourcesListReadAndPagination(t *testing.T) {
	t.Parallel()

	server := New()
	for _, resource := range []Resource{
		{URI: "demo://project/c", Name: "C", MIMEType: "text/plain", Text: "third"},
		{URI: "demo://project/a", Name: "A", MIMEType: "text/plain", Text: "first"},
		{URI: "demo://project/b", Name: "B", MIMEType: "text/plain", Text: "second"},
	} {
		if err := server.RegisterResource(resource); err != nil {
			t.Fatalf("RegisterResource(%q) error = %v", resource.URI, err)
		}
	}

	firstRaw, rpcError := server.listResources(modernRequestParams(t, `{}`))
	if rpcError != nil {
		t.Fatalf("listResources(first) error = %v", rpcError)
	}
	var first resourcesListResult
	mustUnmarshalResult(t, firstRaw, &first)
	if len(first.Resources) != catalogPageSize || first.Resources[0].URI != "demo://project/a" || first.Resources[1].URI != "demo://project/b" {
		t.Fatalf("first resource page = %#v, want sorted a,b", first.Resources)
	}
	if first.NextCursor == "" {
		t.Fatal("first nextCursor = empty, want opaque cursor")
	}

	secondRaw, rpcError := server.listResources(modernRequestParams(t, `{"cursor":`+quotedJSON(t, first.NextCursor)+`}`))
	if rpcError != nil {
		t.Fatalf("listResources(second) error = %v", rpcError)
	}
	var second resourcesListResult
	mustUnmarshalResult(t, secondRaw, &second)
	if len(second.Resources) != 2 || second.Resources[0].URI != "demo://project/c" || second.Resources[1].URI != "demo://project/guide" {
		t.Fatalf("second resource page = %#v, want sorted c,guide", second.Resources)
	}
	if second.NextCursor != "" {
		t.Fatalf("terminal nextCursor = %q, want empty", second.NextCursor)
	}

	readRaw, rpcError := server.readResource(modernRequestParams(t, `{"uri":"demo://project/a"}`))
	if rpcError != nil {
		t.Fatalf("readResource() error = %v", rpcError)
	}
	var read resourceReadResult
	mustUnmarshalResult(t, readRaw, &read)
	if read.ResultType != protocol.ResultTypeComplete || len(read.Contents) != 1 || read.Contents[0].Text != "first" {
		t.Fatalf("resources/read result = %#v", read)
	}
}

func TestServer_PromptsListGetAndPagination(t *testing.T) {
	t.Parallel()

	server := New()
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		name := name
		if err := server.RegisterPrompt(Prompt{
			Name: name,
			Render: func(arguments map[string]string) ([]PromptMessage, error) {
				return []PromptMessage{{Role: "user", Content: PromptContent{Type: "text", Text: name + ":" + arguments["value"]}}}, nil
			},
		}); err != nil {
			t.Fatalf("RegisterPrompt(%q) error = %v", name, err)
		}
	}

	firstRaw, rpcError := server.listPrompts(modernRequestParams(t, `{}`))
	if rpcError != nil {
		t.Fatalf("listPrompts(first) error = %v", rpcError)
	}
	var first promptsListResult
	mustUnmarshalResult(t, firstRaw, &first)
	if len(first.Prompts) != catalogPageSize || first.Prompts[0].Name != "alpha" || first.Prompts[1].Name != "bravo" || first.NextCursor == "" {
		t.Fatalf("first prompt page = %#v cursor=%q", first.Prompts, first.NextCursor)
	}

	getRaw, rpcError := server.getPrompt(modernRequestParams(t, `{"name":"explain-topic","arguments":{"topic":"MCP resources"}}`))
	if rpcError != nil {
		t.Fatalf("getPrompt() error = %v", rpcError)
	}
	var get promptGetResult
	mustUnmarshalResult(t, getRaw, &get)
	if get.ResultType != protocol.ResultTypeComplete || len(get.Messages) != 1 || get.Messages[0].Content.Text != "Explain MCP resources with one practical example." {
		t.Fatalf("prompts/get result = %#v", get)
	}

	_, rpcError = server.getPrompt(modernRequestParams(t, `{"name":"explain-topic","arguments":{}}`))
	if rpcError == nil || rpcError.Code != protocol.CodeInvalidParams {
		t.Fatalf("getPrompt(missing topic) error = %#v, want invalid params", rpcError)
	}
}

func TestServer_ListRejectsMalformedAndOutOfRangeCursors(t *testing.T) {
	t.Parallel()

	server := New()
	for _, raw := range []string{`{"cursor":"not-a-cursor"}`, `{"cursor":"b2Zmc2V0Ojk5"}`} {
		_, rpcError := server.listResources(modernRequestParams(t, raw))
		if rpcError == nil || rpcError.Code != protocol.CodeInvalidParams {
			t.Fatalf("listResources(%s) error = %#v, want invalid params", raw, rpcError)
		}
	}
}

func TestServer_ToolsListPaginatesAndDiscoveryMatchesChangeEvents(t *testing.T) {
	t.Parallel()

	server := New()
	if err := server.RegisterEchoTool("late_echo"); err != nil {
		t.Fatalf("RegisterEchoTool() error = %v", err)
	}
	firstRaw, rpcError := server.listTools(modernRequestParams(t, `{}`))
	if rpcError != nil {
		t.Fatalf("listTools(first) error = %v", rpcError)
	}
	var first paginatedToolsListResult
	mustUnmarshalResult(t, firstRaw, &first)
	if len(first.Tools) != catalogPageSize || first.NextCursor == "" {
		t.Fatalf("first tools page = %#v cursor=%q, want two tools and cursor", first.Tools, first.NextCursor)
	}
	secondRaw, rpcError := server.listTools(modernRequestParams(t, `{"cursor":`+quotedJSON(t, first.NextCursor)+`}`))
	if rpcError != nil {
		t.Fatalf("listTools(second) error = %v", rpcError)
	}
	var second paginatedToolsListResult
	mustUnmarshalResult(t, secondRaw, &second)
	if len(second.Tools) != 1 || second.Tools[0].Name != "late_echo" || second.NextCursor != "" {
		t.Fatalf("second tools page = %#v cursor=%q, want terminal late_echo", second.Tools, second.NextCursor)
	}

	discovery := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(9),
		Method:  "server/discover",
		Params:  modernRequestParams(t, `{}`),
	})
	if discovery.Error != nil {
		t.Fatalf("server/discover error = %v", discovery.Error)
	}
	var discovered discoverResult
	mustUnmarshalResult(t, discovery.Result, &discovered)
	for name, capability := range map[string]map[string]any{
		"tools":     discovered.Capabilities.Tools,
		"resources": discovered.Capabilities.Resources,
		"prompts":   discovered.Capabilities.Prompts,
	} {
		if capability["listChanged"] != true {
			t.Fatalf("capabilities.%s.listChanged = %v, want true", name, capability["listChanged"])
		}
	}
}

func TestServer_CatalogMethodsDispatch(t *testing.T) {
	t.Parallel()

	server := New()
	tests := []struct {
		name   string
		method string
		params string
	}{
		{name: "resources list", method: "resources/list", params: `{}`},
		{name: "resources read", method: "resources/read", params: `{"uri":"demo://project/guide"}`},
		{name: "prompts list", method: "prompts/list", params: `{}`},
		{name: "prompts get", method: "prompts/get", params: `{"name":"explain-topic","arguments":{"topic":"MCP"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := server.Handle(context.Background(), protocol.Request{
				JSONRPC: "2.0",
				ID:      protocol.ID(1),
				Method:  tt.method,
				Params:  modernRequestParams(t, tt.params),
			})
			if response.Error != nil || len(response.Result) == 0 {
				t.Fatalf("Handle(%s) = %#v, want result", tt.method, response)
			}
		})
	}
}

func quotedJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal quoted JSON: %v", err)
	}
	return string(encoded)
}
