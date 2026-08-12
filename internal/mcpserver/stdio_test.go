package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

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

type progressTool struct{}

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buf.Write(data)
}

func (buffer *synchronizedBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.buf.Bytes()...)
}

func (buffer *synchronizedBuffer) String() string { return string(buffer.Bytes()) }

func (progressTool) Definition() ToolDefinition {
	return ToolDefinition{Name: "progress", Description: "reports progress", InputSchema: map[string]any{"type": "object"}}
}

func (progressTool) Call(invocation ToolInvocation) (ToolResult, error) {
	for i := 1; i <= 3; i++ {
		if !ReportProgress(invocation.Context, float64(i), 3, "step") {
			return ToolResult{}, context.Canceled
		}
	}
	return ToolResult{Content: []ContentBlock{{Type: "text", Text: "done"}}}, nil
}

func TestServe_EmitsProgressBeforeFinalResponse(t *testing.T) {
	server := New(progressTool{})
	reader, writer := io.Pipe()
	var output synchronizedBuffer
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background(), reader, &output) }()
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"progressToken":"job"},"name":"progress","arguments":{}}}` + "\n"
	if _, err := io.WriteString(writer, request); err != nil {
		t.Fatal(err)
	}
	for {
		if strings.Count(output.String(), "\n") >= 4 {
			break
		}
	}
	_ = writer.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	var messages []map[string]any
	for decoder.More() {
		var message map[string]any
		if err := decoder.Decode(&message); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	if len(messages) != 4 {
		t.Fatalf("message count = %d, want 4: %s", len(messages), output.String())
	}
	for i := 0; i < 3; i++ {
		if messages[i]["method"] != "notifications/progress" {
			t.Fatalf("message %d = %#v, want progress notification", i, messages[i])
		}
	}
	if messages[3]["result"] == nil {
		t.Fatalf("final message = %#v, want result", messages[3])
	}
}

type cancellableTool struct {
	started chan struct{}
}

func (tool cancellableTool) Definition() ToolDefinition {
	return ToolDefinition{Name: "block", Description: "waits for cancellation", InputSchema: map[string]any{"type": "object"}}
}

func (tool cancellableTool) Call(invocation ToolInvocation) (ToolResult, error) {
	close(tool.started)
	<-invocation.Context.Done()
	return ToolResult{}, invocation.Context.Err()
}

func TestServe_CancelledRequestIsIdempotentAndDoesNotReturnFinal(t *testing.T) {
	tool := cancellableTool{started: make(chan struct{})}
	server := New(tool)
	reader, writer := io.Pipe()
	var output synchronizedBuffer
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background(), reader, &output) }()
	call := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"name":"block","arguments":{}}}` + "\n"
	if _, err := io.WriteString(writer, call); err != nil {
		t.Fatal(err)
	}
	<-tool.started
	cancel := `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}` + "\n"
	if _, err := io.WriteString(writer, cancel+cancel); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if got := output.String(); got != "" {
		t.Fatalf("cancelled request output = %q, want empty", got)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestServe_ReturnsAsynchronousResponseWriteError(t *testing.T) {
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n")
	err := New().Serve(context.Background(), input, failingWriter{})
	if err == nil || !strings.Contains(err.Error(), "encode response") {
		t.Fatalf("Serve() error = %v, want asynchronous encode error", err)
	}
}

type concurrencyLimitTool struct {
	started chan struct{}
	release chan struct{}
}

func (tool concurrencyLimitTool) Definition() ToolDefinition {
	return ToolDefinition{Name: "limited", InputSchema: map[string]any{"type": "object"}}
}

func (tool concurrencyLimitTool) Call(invocation ToolInvocation) (ToolResult, error) {
	tool.started <- struct{}{}
	select {
	case <-tool.release:
		return ToolResult{Content: []ContentBlock{{Type: "text", Text: "done"}}}, nil
	case <-invocation.Context.Done():
		return ToolResult{}, invocation.Context.Err()
	}
}

func TestServe_BoundsConcurrentRequestsAndReturnsOverload(t *testing.T) {
	tool := concurrencyLimitTool{started: make(chan struct{}, maxConcurrentStdioRequests), release: make(chan struct{})}
	reader, writer := io.Pipe()
	var output synchronizedBuffer
	serveDone := make(chan error, 1)
	go func() { serveDone <- New(tool).Serve(context.Background(), reader, &output) }()
	for id := 1; id <= maxConcurrentStdioRequests+1; id++ {
		request := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"name":"limited","arguments":{}}}`+"\n", id)
		if _, err := io.WriteString(writer, request); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < maxConcurrentStdioRequests; i++ {
		select {
		case <-tool.started:
		case <-time.After(time.Second):
			t.Fatalf("started %d requests, want %d", i, maxConcurrentStdioRequests)
		}
	}
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(output.String(), `"code":-32024`) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(output.String(), `"code":-32024`) {
		t.Fatalf("output = %q, want overload response", output.String())
	}
	_ = writer.Close()
	close(tool.release)
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestServe_RejectsCallUsingActiveSubscriptionID(t *testing.T) {
	reader, writer := io.Pipe()
	var output synchronizedBuffer
	serveDone := make(chan error, 1)
	go func() { serveDone <- New().Serve(context.Background(), reader, &output) }()
	listen := `{"jsonrpc":"2.0","id":7,"method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"notifications":{"toolsListChanged":true}}}` + "\n"
	if _, err := io.WriteString(writer, listen); err != nil {
		t.Fatal(err)
	}
	waitForOutputLines(t, &output, 1)
	call := `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n"
	if _, err := io.WriteString(writer, call); err != nil {
		t.Fatal(err)
	}
	waitForOutputLines(t, &output, 2)
	_ = writer.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	var acknowledged protocol.Notification
	var duplicate protocol.Response
	if err := decoder.Decode(&acknowledged); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&duplicate); err != nil {
		t.Fatal(err)
	}
	if acknowledged.Method != "notifications/subscriptions/acknowledged" || duplicate.Error == nil || duplicate.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("messages = %#v %#v", acknowledged, duplicate)
	}
}

func waitForOutputLines(t *testing.T, output *synchronizedBuffer, lines int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for strings.Count(output.String(), "\n") < lines && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := strings.Count(output.String(), "\n"); got < lines {
		t.Fatalf("output lines = %d, want at least %d: %s", got, lines, output.String())
	}
}
