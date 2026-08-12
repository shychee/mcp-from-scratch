package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"time"

	"github.com/shychee/mcp-from-scratch/internal/host"
	"github.com/shychee/mcp-from-scratch/internal/mcpserver"
	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

func main() {
	server := mcpserver.New()
	if err := server.SetExtensions(protocol.Extensions{protocol.TasksExtensionID: json.RawMessage(`{}`)}); err != nil {
		fail("configure Tasks extension", err)
	}
	if err := server.RegisterDeferredEchoTool(); err != nil {
		fail("register deferred_echo", err)
	}
	httpServer := httptest.NewServer(server.HTTPHandler())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transcript, err := host.RunHTTPTaskDemo(ctx, httpServer.URL)
	if err != nil {
		fail("run task demo", err)
	}
	data, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		fail("encode transcript", err)
	}
	fmt.Println(string(data))
}

func fail(action string, err error) {
	fmt.Fprintf(os.Stderr, "mcp-task-demo: %s: %v\n", action, err)
	os.Exit(1)
}
