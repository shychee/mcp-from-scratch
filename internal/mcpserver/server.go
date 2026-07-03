package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

const protocolVersion = "2025-06-18"

type Server struct{}

type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	ServerInfo      serverInfo   `json:"serverInfo"`
	Capabilities    capabilities `json:"capabilities"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type capabilities struct {
	Tools map[string]any `json:"tools"`
}

type toolsListResult struct {
	Tools []tool `json:"tools"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type toolCallResult struct {
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type echoArguments struct {
	Text string `json:"text"`
}

func New() *Server {
	return &Server{}
}

// Handle dispatches valid JSON-RPC requests to the MCP method implementation.
func (s *Server) Handle(_ context.Context, request protocol.Request) protocol.Response {
	response := protocol.Response{
		JSONRPC: "2.0",
		ID:      request.ID,
	}

	switch request.Method {
	case "initialize":
		response.Result = mustMarshal(initializeResult{
			ProtocolVersion: protocolVersion,
			ServerInfo: serverInfo{
				Name:    "mcp-from-scratch",
				Version: "0.1.0",
			},
			Capabilities: capabilities{
				Tools: map[string]any{},
			},
		})
	case "tools/list":
		response.Result = mustMarshal(toolsListResult{
			Tools: []tool{echoTool()},
		})
	case "tools/call":
		result, err := callTool(request.Params)
		if err != nil {
			response.Error = protocol.NewError(protocol.CodeInvalidParams, err.Error())
			return response
		}
		response.Result = mustMarshal(result)
	default:
		response.Error = protocol.NewError(protocol.CodeMethodNotFound, "method not found")
	}

	return response
}

func echoTool() tool {
	return tool{
		Name:        "echo",
		Description: "Return the text argument back to the caller.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "Text to return.",
				},
			},
			"required": []string{"text"},
		},
	}
}

func callTool(raw json.RawMessage) (toolCallResult, error) {
	var params toolCallParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return toolCallResult{}, fmt.Errorf("decode tool call params: %w", err)
	}

	switch params.Name {
	case "echo":
		var args echoArguments
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return toolCallResult{}, fmt.Errorf("decode echo arguments: %w", err)
		}
		return toolCallResult{
			Content: []contentBlock{
				{
					Type: "text",
					Text: args.Text,
				},
			},
		}, nil
	default:
		return toolCallResult{}, fmt.Errorf("unknown tool %q", params.Name)
	}
}

func mustMarshal(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
