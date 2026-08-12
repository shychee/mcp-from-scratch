package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/shychee/mcp-from-scratch/internal/mcpserver"
	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

type progressTool struct{}

func (progressTool) Definition() mcpserver.ToolDefinition {
	return mcpserver.ToolDefinition{
		Name: "bounded_progress", Description: "Report three bounded progress steps.",
		InputSchema: map[string]any{"type": "object"},
	}
}

func (progressTool) Call(invocation mcpserver.ToolInvocation) (mcpserver.ToolResult, error) {
	for step := 1; step <= 3; step++ {
		select {
		case <-invocation.Context.Done():
			return mcpserver.ToolResult{}, invocation.Context.Err()
		case <-time.After(20 * time.Millisecond):
		}
		if !mcpserver.ReportProgress(invocation.Context, float64(step), 3, fmt.Sprintf("step %d", step)) {
			return mcpserver.ToolResult{}, context.Canceled
		}
	}
	return mcpserver.ToolResult{Content: []mcpserver.ContentBlock{{Type: "text", Text: "complete"}}}, nil
}

func main() {
	server := httptest.NewServer(mcpserver.New(progressTool{}).HTTPHandler())
	defer server.Close()
	params, _ := json.Marshal(map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    protocol.Version20260728,
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			"progressToken": "demo-progress",
		},
		"name": "bounded_progress", "arguments": map[string]any{},
	})
	body, _ := json.Marshal(protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/call", Params: params,
	})
	request, err := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(body))
	if err != nil {
		fail(err)
	}
	request.Header.Set("Content-Type", protocol.MediaTypeJSON)
	request.Header.Set("Accept", protocol.MediaTypeJSON+", "+protocol.MediaTypeSSE)
	request.Header.Set(protocol.HeaderProtocolVersion, protocol.Version20260728)
	request.Header.Set(protocol.HeaderMethod, "tools/call")
	request.Header.Set(protocol.HeaderName, "bounded_progress")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		fail(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != protocol.MediaTypeSSE {
		fail(fmt.Errorf("unexpected response %d %q", response.StatusCode, response.Header.Get("Content-Type")))
	}
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if bytes.HasPrefix(scanner.Bytes(), []byte("data: ")) {
			fmt.Println(string(bytes.TrimPrefix(scanner.Bytes(), []byte("data: "))))
		}
	}
	if err := scanner.Err(); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "mcp-progress-demo: %v\n", err)
	os.Exit(1)
}
