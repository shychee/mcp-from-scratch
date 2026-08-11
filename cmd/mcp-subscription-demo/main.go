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
)

func main() {
	server := mcpserver.New()
	httpServer := httptest.NewServer(server.HTTPHandler())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	subscription, err := host.OpenHTTPToolsSubscription(ctx, httpServer.URL, 100)
	if err != nil {
		fail("open tools subscription", err)
	}
	defer subscription.Close()
	printJSON("subscription acknowledged", subscription.Acknowledged())

	if err := server.RegisterTool(mcpserver.NewEchoTool("late_echo")); err != nil {
		fail("register late_echo", err)
	}
	changed, refreshed, err := subscription.RefreshOnNextToolsListChanged(101)
	if err != nil {
		fail("refresh tools", err)
	}
	printJSON("tools list changed", changed)
	printJSON("refreshed tools list", refreshed)
}

func printJSON(label string, value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fail("marshal "+label, err)
	}
	fmt.Printf("=== %s ===\n%s\n\n", label, data)
}

func fail(action string, err error) {
	fmt.Fprintf(os.Stderr, "mcp-subscription-demo: %s: %v\n", action, err)
	os.Exit(1)
}
