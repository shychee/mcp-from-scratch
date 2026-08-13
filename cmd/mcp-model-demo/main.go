package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"time"

	"github.com/shychee/mcp-from-scratch/internal/host"
	"github.com/shychee/mcp-from-scratch/internal/mcpserver"
)

func main() {
	var turn atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if turn.Add(1) == 1 {
			_, _ = io.WriteString(writer, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"demo-call","type":"function","function":{"name":"echo","arguments":"{\"text\":\"hello from the model adapter\"}"}}]}}]}`)
			return
		}
		_, _ = io.WriteString(writer, `{"choices":[{"message":{"role":"assistant","content":"The MCP tool returned: hello from the model adapter"}}]}`)
	}))
	defer modelServer.Close()
	mcpServer := httptest.NewServer(mcpserver.New().HTTPHandler())
	defer mcpServer.Close()

	adapter, err := host.NewOpenAICompatibleAdapter(host.OpenAICompatibleConfig{
		Endpoint: modelServer.URL, Model: "local-fixture", HTTPClient: modelServer.Client(),
	})
	if err != nil {
		fail("configure model adapter", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transcript, err := host.RunHTTPModelDemo(ctx, mcpServer.URL, adapter, "Echo a greeting")
	if err != nil {
		fail("run model flow", err)
	}
	output, err := json.MarshalIndent(map[string]any{
		"tool":  transcript.FirstModel.ToolCalls[0].Name,
		"final": transcript.FinalModel.Content,
	}, "", "  ")
	if err != nil {
		fail("encode output", err)
	}
	fmt.Println(string(output))
}

func fail(action string, err error) {
	fmt.Fprintf(os.Stderr, "mcp-model-demo: %s: %v\n", action, err)
	os.Exit(1)
}
