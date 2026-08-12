package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/shychee/mcp-from-scratch/internal/host"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	transcript, err := host.RunDemo(ctx, host.ServerCommand{
		Name: "go",
		Args: []string{"run", "./cmd/mcp-server", "--legacy"},
		Dir:  ".",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-legacy-demo: %v\n", err)
		os.Exit(1)
	}
	for _, exchange := range transcript.Exchanges {
		printJSON(exchange.Name+" request", exchange.Request)
		if exchange.Response != nil {
			printJSON(exchange.Name+" response", exchange.Response)
		}
	}
}

func printJSON(label string, value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal %s: %v\n", label, err)
		os.Exit(1)
	}
	fmt.Printf("=== %s ===\n%s\n\n", label, data)
}
