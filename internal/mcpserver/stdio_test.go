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

func TestServe_NotificationDoesNotWriteResponse(t *testing.T) {
	server := New()

	input := strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n")
	var output bytes.Buffer

	err := server.Serve(context.Background(), input, &output)
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	if output.Len() != 0 {
		t.Fatalf("output = %q, want empty", output.String())
	}
}
