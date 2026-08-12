package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

func TestServe_ParseErrorRespondsWithNullID(t *testing.T) {
	server := New()

	input := strings.NewReader("not json\n")
	var output bytes.Buffer

	err := server.Serve(context.Background(), input, &output)
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	var response map[string]any
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	id, ok := response["id"]
	if !ok {
		t.Fatalf("response missing id field")
	}
	if id != nil {
		t.Fatalf("id = %v, want nil", id)
	}
}

func TestServe_InvalidRequestReturnsInvalidRequestError(t *testing.T) {
	server := New()

	input := strings.NewReader(`{"jsonrpc":"2.0","id":1}` + "\n")
	var output bytes.Buffer

	err := server.Serve(context.Background(), input, &output)
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	var response map[string]any
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if response["id"] != float64(1) {
		t.Fatalf("id = %v, want 1", response["id"])
	}

	errorObject, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("error = %T, want object", response["error"])
	}

	if errorObject["code"] != float64(protocol.CodeInvalidRequest) {
		t.Fatalf("error.code = %v, want %d", errorObject["code"], protocol.CodeInvalidRequest)
	}
}

func TestServe_NullIDReturnsInvalidRequestInsteadOfExecutingNotification(t *testing.T) {
	server := New()
	input := strings.NewReader(`{"jsonrpc":"2.0","id":null,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n")
	var output bytes.Buffer
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	var response protocol.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error == nil || response.Error.Code != protocol.CodeInvalidRequest || response.ID != nil {
		t.Fatalf("response = %#v, want invalid request with null ID", response)
	}
}

func TestServe_NotificationDoesNotWriteResponse(t *testing.T) {
	server := New()

	input := strings.NewReader(`{"jsonrpc":"2.0","method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n")
	var output bytes.Buffer

	err := server.Serve(context.Background(), input, &output)
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	if output.Len() != 0 {
		t.Fatalf("output = %q, want empty", output.String())
	}
}

func TestServe_RequestDoesNotRequirePriorDiscovery(t *testing.T) {
	server := New()

	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n")
	var output bytes.Buffer

	err := server.Serve(context.Background(), input, &output)
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	var response protocol.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response.Error != nil {
		t.Fatalf("response error = %v, want nil", response.Error)
	}
	if len(response.Result) == 0 {
		t.Fatal("response result is empty")
	}
}

func TestServe_LegacyLifecycleAndBusinessRequest(t *testing.T) {
	server := New()
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"legacy","version":"1"}}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":"list","method":"tools/list","params":{}}` + "\n",
	)
	var output bytes.Buffer
	if err := server.ServeLegacy(context.Background(), input, &output); err != nil {
		t.Fatalf("ServeLegacy() error = %v", err)
	}
	decoder := json.NewDecoder(&output)
	var initialize, list protocol.Response
	if err := decoder.Decode(&initialize); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if err := decoder.Decode(&list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if initialize.Error != nil || list.Error != nil || len(list.Result) == 0 {
		t.Fatalf("legacy responses = %#v %#v", initialize, list)
	}
	var listed toolsListResult
	if err := json.Unmarshal(list.Result, &listed); err != nil || len(listed.Tools) == 0 {
		t.Fatalf("legacy tools/list result = %s, err = %v", list.Result, err)
	}
}

func TestServe_LegacyRejectsBusinessBeforeInitialize(t *testing.T) {
	server := New()
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
	var output bytes.Buffer
	if err := server.ServeLegacy(context.Background(), input, &output); err != nil {
		t.Fatalf("ServeLegacy() error = %v", err)
	}
	var response protocol.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("response error = %#v, want invalid request", response.Error)
	}
}

func TestServe_LegacyAdvertisesFallbackWithMethodNotFound(t *testing.T) {
	server := New()
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n")
	var output bytes.Buffer
	if err := server.ServeLegacy(context.Background(), input, &output); err != nil {
		t.Fatalf("ServeLegacy() error = %v", err)
	}
	var response protocol.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != protocol.CodeMethodNotFound {
		t.Fatalf("response error = %#v, want method not found", response.Error)
	}
}

func TestServe_ModernInitializeReturnsActionableDiagnostic(t *testing.T) {
	server := New()
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n")
	var output bytes.Buffer
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	var response protocol.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != protocol.CodeMethodNotFound || !strings.Contains(response.Error.Message, "server/discover") {
		t.Fatalf("response error = %#v, want actionable method not found", response.Error)
	}
}

func TestServe_MalformedModernRequestDoesNotDowngradeConnection(t *testing.T) {
	server := New()
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n",
	)
	var output bytes.Buffer
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	decoder := json.NewDecoder(&output)
	var malformed, valid protocol.Response
	if err := decoder.Decode(&malformed); err != nil {
		t.Fatalf("decode malformed response: %v", err)
	}
	if err := decoder.Decode(&valid); err != nil {
		t.Fatalf("decode valid response: %v", err)
	}
	if malformed.Error == nil || valid.Error != nil || len(valid.Result) == 0 {
		t.Fatalf("responses = %#v %#v, want malformed rejection then valid modern result", malformed, valid)
	}
}
