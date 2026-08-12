package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/shychee/mcp-from-scratch/internal/mcpserver"
)

func main() {
	legacy := flag.Bool("legacy", false, "serve the legacy initialization-based stdio protocol")
	flag.Parse()
	server := mcpserver.New()
	serve := server.Serve
	if *legacy {
		serve = server.ServeLegacy
	}
	if err := serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-server: %v\n", err)
		os.Exit(1)
	}
}
