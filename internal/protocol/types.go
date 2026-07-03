package protocol

import "encoding/json"

// ErrorCode identifies a JSON-RPC 2.0 error condition.
type ErrorCode int

// Standard JSON-RPC 2.0 error codes used by this project.
const (
	CodeParseError     ErrorCode = -32700
	CodeInvalidRequest ErrorCode = -32600
	CodeMethodNotFound ErrorCode = -32601
	CodeInvalidParams  ErrorCode = -32602
	CodeInternalError  ErrorCode = -32603
)

// Request is the subset of JSON-RPC 2.0 request fields this learning project needs.
// IDs are limited to integer values for now; real JSON-RPC also permits string IDs.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// ValidateRequest checks the JSON-RPC envelope before MCP method dispatch.
func ValidateRequest(request Request) *Error {
	if request.JSONRPC != "2.0" {
		return NewError(CodeInvalidRequest, "invalid request")
	}
	if request.Method == "" {
		return NewError(CodeInvalidRequest, "invalid request")
	}
	return nil
}

// Response is the subset of JSON-RPC 2.0 response fields this learning project needs.
// ID must be explicit null when the server cannot determine the request ID.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// ID returns a pointer for JSON-RPC request and response IDs.
func ID(value int) *int {
	return &value
}

// Error follows the JSON-RPC 2.0 error object shape.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// NewError creates a JSON-RPC error object.
func NewError(code ErrorCode, message string) *Error {
	return &Error{
		Code:    code,
		Message: message,
	}
}
