package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

type observabilityTool struct {
	trace TraceContext
}

func (tool *observabilityTool) Definition() ToolDefinition {
	return ToolDefinition{Name: "observe", InputSchema: map[string]any{"type": "object"}}
}

func (tool *observabilityTool) Call(invocation ToolInvocation) (ToolResult, error) {
	tool.trace = TraceFromContext(invocation.Context)
	Log(invocation.Context, "error", "observe", map[string]any{
		"message":      "request observed",
		"requestState": "must-not-leak",
	})
	return ToolResult{Content: []ContentBlock{{Type: "text", Text: "done"}}}, nil
}

func TestObservabilityContextPropagatesValidTraceAndIgnoresMalformedFields(t *testing.T) {
	validParent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	ctx := withObservabilityContext(context.Background(), observabilityParams(t, map[string]any{
		"traceparent": validParent,
		"tracestate":  "vendor=value",
		"baggage":     "tenant=demo",
	}))
	if got := TraceFromContext(ctx); got != (TraceContext{TraceParent: validParent, TraceState: "vendor=value", Baggage: "tenant=demo"}) {
		t.Fatalf("trace context = %#v", got)
	}

	malformed := withObservabilityContext(context.Background(), observabilityParams(t, map[string]any{
		"traceparent": "00-zero",
		"tracestate":  "broken",
		"baggage":     "bad\nheader",
	}))
	if got := TraceFromContext(malformed); got != (TraceContext{}) {
		t.Fatalf("malformed trace context = %#v, want empty", got)
	}
}

func TestTraceStateRejectsInvalidMembers(t *testing.T) {
	tests := map[string]string{
		"invalid key":      "bad key=value",
		"invalid value":    "vendor=bad=value",
		"duplicate key":    "vendor=one,vendor=two",
		"too many members": strings.Repeat("a=1,", 32) + "z=1",
	}
	for name, traceState := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := withObservabilityContext(context.Background(), observabilityParams(t, map[string]any{
				"tracestate": traceState,
			}))
			if got := TraceFromContext(ctx).TraceState; got != "" {
				t.Fatalf("tracestate = %q, want ignored", got)
			}
		})
	}
}

func TestLogRequiresOptInFiltersLevelsAndRedactsSensitiveData(t *testing.T) {
	var notifications []protocol.Notification
	ctx := WithNotificationSink(context.Background(), func(notification protocol.Notification) bool {
		notifications = append(notifications, notification)
		return true
	})
	ctx = withObservabilityContext(ctx, observabilityParams(t, map[string]any{
		"io.modelcontextprotocol/logLevel": "warning",
	}))
	if Log(ctx, "info", "demo", map[string]any{"message": "hidden by level"}) {
		t.Fatal("info log emitted below warning threshold")
	}
	if !Log(ctx, "error", "demo", map[string]any{
		"message":       "failed safely",
		"authorization": "Bearer secret",
		"nested":        map[string]any{"requestState": "opaque", "safe": "visible"},
		"arguments":     map[string]any{"password": "value"},
	}) {
		t.Fatal("error log was not emitted")
	}
	if len(notifications) != 1 || notifications[0].Method != "notifications/message" {
		t.Fatalf("notifications = %#v", notifications)
	}
	encoded := string(notifications[0].Params)
	for _, secret := range []string{"Bearer secret", "opaque", "password", "value"} {
		if containsJSONText(encoded, secret) {
			t.Fatalf("log params leaked %q: %s", secret, encoded)
		}
	}
	if !containsJSONText(encoded, "failed safely") || !containsJSONText(encoded, "visible") {
		t.Fatalf("log params lost safe fields: %s", encoded)
	}
}

func TestLogWithoutRequestOptInDoesNotEmit(t *testing.T) {
	emitted := false
	ctx := WithNotificationSink(context.Background(), func(protocol.Notification) bool { emitted = true; return true })
	ctx = withObservabilityContext(ctx, observabilityParams(t, nil))
	if Log(ctx, "error", "demo", "message") || emitted {
		t.Fatal("log emitted without request logLevel")
	}
}

func TestServeRoutesTraceAndLogsOnOriginatingRequest(t *testing.T) {
	tool := &observabilityTool{}
	server := New(tool)
	params := observabilityParams(t, map[string]any{
		"traceparent":                      "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"tracestate":                       "vendor=value",
		"baggage":                          "tenant=demo",
		"io.modelcontextprotocol/logLevel": "warning",
	})
	var decoded map[string]any
	if err := json.Unmarshal(params, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["name"] = "observe"
	decoded["arguments"] = map[string]any{}
	requestParams, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/call", Params: requestParams,
	})
	if err != nil {
		t.Fatal(err)
	}
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background(), inputReader, outputWriter) }()
	if _, err := inputWriter.Write(append(request, '\n')); err != nil {
		t.Fatalf("write request: %v", err)
	}
	reader := bufio.NewReader(outputReader)
	logLine, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read log notification: %v", err)
	}
	responseLine, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read final response: %v", err)
	}
	if !strings.Contains(string(logLine), `"method":"notifications/message"`) ||
		strings.Contains(string(logLine), "must-not-leak") || !strings.Contains(string(logLine), redactedValue) {
		t.Fatalf("log line = %s", logLine)
	}
	var response protocol.Response
	if err := json.Unmarshal(responseLine, &response); err != nil || response.Error != nil {
		t.Fatalf("response = %#v, err=%v", response, err)
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatalf("close input: %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if tool.trace.TraceState != "vendor=value" || tool.trace.Baggage != "tenant=demo" {
		t.Fatalf("tool trace = %#v", tool.trace)
	}
}

func TestLoggingSetLevelRemainsUnsupported(t *testing.T) {
	response := New().Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "logging/setLevel",
		Params: observabilityParams(t, nil),
	})
	if response.Error == nil || response.Error.Code != protocol.CodeMethodNotFound {
		t.Fatalf("response error = %#v, want method not found", response.Error)
	}
}

func TestDiscoveryAdvertisesRequestScopedLogging(t *testing.T) {
	response := New().Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "server/discover",
		Params: observabilityParams(t, nil),
	})
	if response.Error != nil {
		t.Fatalf("discovery error = %v", response.Error)
	}
	var result struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode discovery: %v", err)
	}
	if got, ok := result.Capabilities["logging"]; !ok || string(got) != `{}` {
		t.Fatalf("logging capability = %s, present=%v, want {}", got, ok)
	}
}

func TestRequestMetadataRejectsInvalidLogLevelAndProgressToken(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]any
	}{
		{name: "log level", extra: map[string]any{"io.modelcontextprotocol/logLevel": "verbose"}},
		{name: "progress token", extra: map[string]any{"progressToken": map[string]any{"bad": true}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := New().Handle(context.Background(), protocol.Request{
				JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/list",
				Params: observabilityParams(t, test.extra),
			})
			if response.Error == nil || response.Error.Code != protocol.CodeInvalidParams {
				t.Fatalf("response error = %#v, want invalid params", response.Error)
			}
		})
	}
}

func observabilityParams(t *testing.T, extra map[string]any) json.RawMessage {
	t.Helper()
	meta := map[string]any{
		"io.modelcontextprotocol/protocolVersion":    protocol.Version20260728,
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	for key, value := range extra {
		meta[key] = value
	}
	encoded, err := json.Marshal(map[string]any{"_meta": meta})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func containsJSONText(encoded, text string) bool {
	var value any
	if json.Unmarshal([]byte(encoded), &value) != nil {
		return false
	}
	return containsValue(value, text)
}

func containsValue(value any, text string) bool {
	switch typed := value.(type) {
	case string:
		return typed == text
	case map[string]any:
		for key, child := range typed {
			if key == text || containsValue(child, text) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsValue(child, text) {
				return true
			}
		}
	}
	return false
}
