package main

import (
	"context"
	"fmt"
	"os"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoInput struct {
	Text string `json:"text" jsonschema:"text to echo"`
}

type previewInput struct {
	Preview string `json:"preview" jsonschema:"preview to confirm"`
}

func main() {
	server := newServer()
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-official-fixture: %v\n", err)
		os.Exit(1)
	}
}

func newServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "official-go-sdk-fixture",
		Version: "v1.7.0",
	}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "Echo a message",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input echoInput) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: input.Text}},
		}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "confirm_preview",
		Description: "Confirm a preview through MRTR elicitation",
	}, func(_ context.Context, request *mcp.CallToolRequest, input previewInput) (*mcp.CallToolResult, any, error) {
		if len(request.Params.InputResponses) == 0 {
			return &mcp.CallToolResult{
				InputRequests: mcp.InputRequestMap{
					"confirm_preview": &mcp.ElicitParams{
						Message: "Confirm this preview",
						RequestedSchema: &jsonschema.Schema{
							Type: "object",
							Properties: map[string]*jsonschema.Schema{
								"confirm": {Type: "boolean"},
							},
							Required: []string{"confirm"},
						},
					},
				},
				RequestState: "official-fixture-preview",
			}, nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "confirmed: " + input.Preview}},
		}, nil, nil
	})
	return server
}
