package main

import (
	"context"
	"fmt"
	"os"

	"github.com/shychee/mcp-from-scratch/internal/mcpserver"
)

func main() {
	server := mcpserver.New()
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-server: %v\n", err)
		os.Exit(1)
	}
}
