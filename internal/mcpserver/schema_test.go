package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

type schemaContractTool struct {
	definition ToolDefinition
	result     ToolResult
}

func TestJSONSchema202012ValidatesNestedCompositionAndLocalRef(t *testing.T) {
	called := false
	tool := schemaContractTool{definition: ToolDefinition{
		Name: "complex",
		InputSchema: map[string]any{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"type":    "object",
			"$defs": map[string]any{
				"item": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"count":   map[string]any{"type": "number"},
						"enabled": map[string]any{"type": "boolean"},
					},
					"required":             []string{"count", "enabled"},
					"additionalProperties": false,
				},
			},
			"properties": map[string]any{
				"items": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/item"}},
			},
			"required": []string{"items"},
			"allOf":    []any{map[string]any{"minProperties": 1}},
		},
	}}
	tool.result = ToolResult{Content: []ContentBlock{{Type: "text", Text: "ok"}}}
	server := New(tool)
	entry, _ := server.tool("complex")
	entry.tool = toolCallFunc{definition: tool.definition, call: func(ToolInvocation) (ToolResult, error) {
		called = true
		return tool.result, nil
	}}
	server.tools["complex"] = entry

	valid := server.Handle(context.Background(), protocol.Request{JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/call", Params: schemaRequestParams(t, `{"name":"complex","arguments":{"items":[{"count":2.5,"enabled":true}]}}`)})
	if valid.Error != nil || !called {
		t.Fatalf("valid complex schema response = %#v, called=%v", valid, called)
	}
	called = false
	invalid := server.Handle(context.Background(), protocol.Request{JSONRPC: "2.0", ID: protocol.ID(2), Method: "tools/call", Params: schemaRequestParams(t, `{"name":"complex","arguments":{"items":[{"count":"bad","enabled":true,"extra":1}]}}`)})
	if invalid.Error == nil || invalid.Error.Code != protocol.CodeInvalidParams || called {
		t.Fatalf("invalid complex schema response = %#v, called=%v", invalid, called)
	}
}

type toolCallFunc struct {
	definition ToolDefinition
	call       func(ToolInvocation) (ToolResult, error)
}

func (tool toolCallFunc) Definition() ToolDefinition { return tool.definition }
func (tool toolCallFunc) Call(invocation ToolInvocation) (ToolResult, error) {
	return tool.call(invocation)
}

func TestSchemaAndArgumentLimits(t *testing.T) {
	oversized := strings.Repeat("x", maxToolSchemaBytes)
	if _, err := NewServer(schemaContractTool{definition: ToolDefinition{Name: "oversized", InputSchema: map[string]any{"type": "object", "description": oversized}}}); err == nil {
		t.Fatal("NewServer() error = nil, want oversized schema rejection")
	}

	server := New(schemaContractTool{definition: ToolDefinition{Name: "bounded", InputSchema: map[string]any{"type": "object"}}})
	arguments := `{"value":"` + strings.Repeat("x", maxToolArgumentBytes) + `"}`
	response := server.Handle(context.Background(), protocol.Request{JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/call", Params: schemaRequestParams(t, `{"name":"bounded","arguments":`+arguments+`}`)})
	if response.Error == nil || response.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("oversized arguments response = %#v", response)
	}
}

func (tool schemaContractTool) Definition() ToolDefinition { return tool.definition }
func (tool schemaContractTool) Call(ToolInvocation) (ToolResult, error) {
	return tool.result, nil
}

func schemaRequestParams(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	var params map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    protocol.Version20260728,
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("encode params: %v", err)
	}
	return encoded
}

func TestRegisterToolRejectsInvalidSchemaAtomically(t *testing.T) {
	server := New()
	tool := schemaContractTool{definition: ToolDefinition{
		Name:        "invalid",
		InputSchema: map[string]any{"type": "object", "$ref": "https://example.test/schema"},
	}}
	if err := server.RegisterTool(tool); err == nil {
		t.Fatal("RegisterTool() error = nil, want external reference rejection")
	}
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/list", Params: schemaRequestParams(t, `{}`),
	})
	if response.Error != nil {
		t.Fatalf("tools/list error = %v", response.Error)
	}
	var result toolsListResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	for _, definition := range result.Tools {
		if definition.Name == "invalid" {
			t.Fatal("invalid tool became visible after rejected registration")
		}
	}
}

func TestRegisterToolRejectsDuplicateWithoutReplacingExistingTool(t *testing.T) {
	server := New()
	first := schemaContractTool{definition: ToolDefinition{Name: "duplicate", InputSchema: map[string]any{"type": "object"}, Description: "first"}}
	second := schemaContractTool{definition: ToolDefinition{Name: "duplicate", InputSchema: map[string]any{"type": "object"}, Description: "second"}}
	if err := server.RegisterTool(first); err != nil {
		t.Fatalf("first RegisterTool() error = %v", err)
	}
	if err := server.RegisterTool(second); err == nil {
		t.Fatal("duplicate RegisterTool() error = nil, want error")
	}
	entry, ok := server.tool("duplicate")
	if !ok || entry.definition.Description != "first" {
		t.Fatalf("registered entry = %#v, want first tool", entry.definition)
	}
}

func TestToolOutputSchemaMismatchReturnsInternalErrorWithoutResult(t *testing.T) {
	server := New(schemaContractTool{
		definition: ToolDefinition{
			Name:        "structured",
			InputSchema: map[string]any{"type": "object"},
			OutputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
				"required":   []string{"ok"},
			},
		},
		result: ToolResult{StructuredContent: map[string]any{"ok": "wrong"}},
	})
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/call",
		Params: schemaRequestParams(t, `{"name":"structured","arguments":{}}`),
	})
	if response.Error == nil || response.Error.Code != protocol.CodeInternalError {
		t.Fatalf("response error = %#v, want internal error", response.Error)
	}
	if len(response.Result) != 0 {
		t.Fatalf("response result = %s, want empty on output mismatch", response.Result)
	}
}

func TestToolOutputSchemaRequiresStructuredContent(t *testing.T) {
	server := New(schemaContractTool{
		definition: ToolDefinition{
			Name:         "structured",
			InputSchema:  map[string]any{"type": "object"},
			OutputSchema: map[string]any{"type": "object"},
		},
	})
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/call",
		Params: schemaRequestParams(t, `{"name":"structured","arguments":{}}`),
	})
	if response.Error == nil || response.Error.Code != protocol.CodeInternalError || len(response.Result) != 0 {
		t.Fatalf("response = %#v, want internal error without result", response)
	}
}

func TestToolSchemaUsesExactJSONIntegerValues(t *testing.T) {
	server := New(schemaContractTool{
		definition: ToolDefinition{
			Name: "exact-integer",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"maximum": json.Number("9007199254740992")}},
				"required":   []string{"value"},
			},
		},
		result: ToolResult{Content: []ContentBlock{{Type: "text", Text: "ok"}}},
	})
	valid := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/call",
		Params: schemaRequestParams(t, `{"name":"exact-integer","arguments":{"value":9007199254740992}}`),
	})
	if valid.Error != nil {
		t.Fatalf("exact integer response error = %v", valid.Error)
	}
	invalid := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(2), Method: "tools/call",
		Params: schemaRequestParams(t, `{"name":"exact-integer","arguments":{"value":9007199254740993}}`),
	})
	if invalid.Error == nil || invalid.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("adjacent integer response = %#v, want invalid params", invalid)
	}
}

func TestRegisterToolClonesSchemaDefinition(t *testing.T) {
	inputSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"value": map[string]any{"type": "string"}},
		"required":   []string{"value"},
	}
	server := New(schemaContractTool{
		definition: ToolDefinition{Name: "stable", InputSchema: inputSchema},
		result:     ToolResult{Content: []ContentBlock{{Type: "text", Text: "ok"}}},
	})
	inputSchema["required"] = []string{"changed"}
	entry, ok := server.tool("stable")
	if !ok {
		t.Fatal("registered tool missing")
	}
	required, _ := entry.definition.InputSchema["required"].([]any)
	if len(required) != 1 || required[0] != "value" {
		t.Fatalf("registered required = %#v, want cloned value", required)
	}
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/call",
		Params: schemaRequestParams(t, `{"name":"stable","arguments":{"value":"ok"}}`),
	})
	if response.Error != nil {
		t.Fatalf("call after source mutation error = %v", response.Error)
	}
}

func TestToolResultSupportsErrorAndStructuredContentFields(t *testing.T) {
	encoded, err := json.Marshal(ToolResult{
		IsError:           true,
		StructuredContent: map[string]any{"value": 1},
		Content:           []ContentBlock{{Type: "text", Text: "failed"}},
	})
	if err != nil {
		t.Fatalf("marshal ToolResult: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode ToolResult: %v", err)
	}
	if fields["isError"] != true || fields["structuredContent"] == nil {
		t.Fatalf("encoded ToolResult = %s, want isError and structuredContent", encoded)
	}
}
