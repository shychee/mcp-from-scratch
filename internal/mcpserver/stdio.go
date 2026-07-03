package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

// Serve owns stdio framing, JSON parsing, and JSON-RPC envelope validation.
// Valid requests are passed to Handle for MCP method dispatch.
func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	encoder := json.NewEncoder(output)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("server context canceled: %w", err)
		}

		var request protocol.Request
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			response := protocol.Response{
				JSONRPC: "2.0",
				Error:   protocol.NewError(protocol.CodeParseError, "parse error"),
			}
			if encodeErr := encoder.Encode(response); encodeErr != nil {
				return fmt.Errorf("encode parse error response: %w", encodeErr)
			}
			continue
		}
		if requestError := protocol.ValidateRequest(request); requestError != nil {
			response := protocol.Response{
				JSONRPC: "2.0",
				ID:      request.ID,
				Error:   requestError,
			}
			if encodeErr := encoder.Encode(response); encodeErr != nil {
				return fmt.Errorf("encode invalid request error response: %w", encodeErr)
			}
			continue
		}
		if err := encoder.Encode(s.Handle(ctx, request)); err != nil {
			return fmt.Errorf("encode response: %w", err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	return nil
}
