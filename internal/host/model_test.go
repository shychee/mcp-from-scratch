package host

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/mcpserver"
)

func TestRunHTTPModelDemoUsesRealOpenAICompatibleWireFlow(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []openAIChatRequest
	)
	modelServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer model-secret" {
			t.Errorf("model Authorization = %q", request.Header.Get("Authorization"))
		}
		var body openAIChatRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode model request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		turn := len(requests)
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		if turn == 1 {
			_, _ = io.WriteString(writer, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"echo","arguments":"{\"text\":\"hello through MCP\"}"}}]}}]}`)
			return
		}
		_, _ = io.WriteString(writer, `{"choices":[{"message":{"role":"assistant","content":"The tool returned hello through MCP."}}]}`)
	}))
	defer modelServer.Close()

	mcpServer := httptest.NewServer(mcpserver.New().HTTPHandler())
	defer mcpServer.Close()
	adapter, err := NewOpenAICompatibleAdapter(OpenAICompatibleConfig{
		Endpoint: modelServer.URL, Model: "fixture-model", APIKey: "model-secret", HTTPClient: modelServer.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := RunHTTPModelDemo(context.Background(), mcpServer.URL, adapter, "Echo a greeting")
	if err != nil {
		t.Fatal(err)
	}
	if transcript.FinalModel.Content != "The tool returned hello through MCP." {
		t.Fatalf("final content = %q", transcript.FinalModel.Content)
	}
	if transcript.ToolResponse.Error != nil || !strings.Contains(string(transcript.ToolResponse.Result), "hello through MCP") {
		t.Fatalf("tool response = %#v", transcript.ToolResponse)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("model request count = %d", len(requests))
	}
	if requests[0].Model != "fixture-model" || len(requests[0].Tools) == 0 || requests[0].Tools[0].Function.Name == "" {
		t.Fatalf("first model request = %#v", requests[0])
	}
	if len(requests[1].Messages) != 3 || requests[1].Messages[2].Role != "tool" || requests[1].Messages[2].ToolCallID != "call-1" {
		t.Fatalf("second model messages = %#v", requests[1].Messages)
	}
}

func TestOpenAICompatibleAdapterRejectsInvalidBoundaries(t *testing.T) {
	tests := []OpenAICompatibleConfig{
		{Endpoint: "http://model.example/v1/chat/completions", Model: "model"},
		{Endpoint: "https://user@model.example/v1/chat/completions", Model: "model"},
		{Endpoint: "https://model.example/v1/chat/completions#fragment", Model: "model"},
		{Endpoint: "https://model.example/v1/chat/completions"},
		{Endpoint: "https://model.example/v1/chat/completions", Model: "model", MaxResponseBytes: -1},
	}
	for index, config := range tests {
		if _, err := NewOpenAICompatibleAdapter(config); err == nil {
			t.Fatalf("case %d accepted invalid config", index)
		}
	}
}

func TestOpenAICompatibleAdapterRejectsMalformedCallsAndLargeResponses(t *testing.T) {
	for name, response := range map[string]string{
		"malformed arguments": `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call","type":"function","function":{"name":"echo","arguments":"not-json"}}]}}]}`,
		"multiple choices":    `{"choices":[{"message":{"role":"assistant","content":"one"}},{"message":{"role":"assistant","content":"two"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { _, _ = io.WriteString(writer, response) }))
			defer server.Close()
			adapter, err := NewOpenAICompatibleAdapter(OpenAICompatibleConfig{Endpoint: server.URL, Model: "fixture", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.Complete(context.Background(), ModelRequest{}); err == nil {
				t.Fatal("Complete accepted malformed response")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, strings.Repeat("x", 33))
	}))
	defer server.Close()
	adapter, err := NewOpenAICompatibleAdapter(OpenAICompatibleConfig{Endpoint: server.URL, Model: "fixture", HTTPClient: server.Client(), MaxResponseBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Complete(context.Background(), ModelRequest{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Complete error = %v", err)
	}
}

func TestOpenAICompatibleAdapterDoesNotForwardAPIKeyAcrossRedirect(t *testing.T) {
	redirectTargetCalled := false
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirectTargetCalled = true
		if request.Header.Get("Authorization") != "" {
			t.Errorf("redirected Authorization = %q", request.Header.Get("Authorization"))
		}
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer redirector.Close()
	adapter, err := NewOpenAICompatibleAdapter(OpenAICompatibleConfig{Endpoint: redirector.URL, Model: "fixture", APIKey: "secret", HTTPClient: redirector.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Complete(context.Background(), ModelRequest{}); err == nil || !strings.Contains(err.Error(), "status 302") {
		t.Fatalf("Complete error = %v", err)
	}
	if redirectTargetCalled {
		t.Fatal("model redirect target was called")
	}
}

func TestRunHTTPModelDemoFeedsMCPValidationErrorBackToModel(t *testing.T) {
	var second openAIChatRequest
	var turn int
	modelServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		turn++
		var body openAIChatRequest
		_ = json.NewDecoder(request.Body).Decode(&body)
		if turn == 1 {
			_, _ = io.WriteString(writer, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"bad-call","type":"function","function":{"name":"echo","arguments":"{}"}}]}}]}`)
			return
		}
		second = body
		_, _ = io.WriteString(writer, `{"choices":[{"message":{"role":"assistant","content":"The tool rejected the missing text argument."}}]}`)
	}))
	defer modelServer.Close()
	mcpServer := httptest.NewServer(mcpserver.New().HTTPHandler())
	defer mcpServer.Close()
	adapter, err := NewOpenAICompatibleAdapter(OpenAICompatibleConfig{Endpoint: modelServer.URL, Model: "fixture", HTTPClient: modelServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := RunHTTPModelDemo(context.Background(), mcpServer.URL, adapter, "Echo without arguments")
	if err != nil {
		t.Fatal(err)
	}
	if transcript.ToolResponse.Error == nil || transcript.ToolResponse.Error.Code != -32602 {
		t.Fatalf("tool error = %#v", transcript.ToolResponse.Error)
	}
	if len(second.Messages) != 3 || !strings.Contains(second.Messages[2].Content, `"code":-32602`) {
		t.Fatalf("tool feedback = %#v", second.Messages)
	}
}
