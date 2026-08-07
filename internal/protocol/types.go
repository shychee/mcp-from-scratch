package protocol

import "encoding/json"

const (
	Version20260728    = "2026-07-28"
	ResultTypeComplete = "complete"
	CacheScopePublic   = "public"
)

// Implementation identifies MCP client or server software.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// RequestMeta carries the protocol context required on each modern MCP request.
type RequestMeta struct {
	ProtocolVersion    string          `json:"io.modelcontextprotocol/protocolVersion"`
	ClientInfo         *Implementation `json:"io.modelcontextprotocol/clientInfo,omitempty"`
	ClientCapabilities map[string]any  `json:"io.modelcontextprotocol/clientCapabilities"`
}

// RequestParams contains the common fields shared by modern MCP requests.
type RequestParams struct {
	Meta RequestMeta `json:"_meta"`
}

// ResultMeta identifies the server that produced a result.
type ResultMeta struct {
	ServerInfo Implementation `json:"io.modelcontextprotocol/serverInfo"`
}

// Result contains fields shared by modern MCP results.
type Result struct {
	ResultType string     `json:"resultType"`
	Meta       ResultMeta `json:"_meta"`
}

// CacheableResult adds cache hints to results that can be reused by clients.
type CacheableResult struct {
	Result
	TTLMillis  int    `json:"ttlMs"`
	CacheScope string `json:"cacheScope"`
}

// ErrorCode identifies a JSON-RPC 2.0 error condition.
type ErrorCode int

// Standard JSON-RPC 2.0 error codes used by this project.
const (
	CodeParseError     ErrorCode = -32700
	CodeInvalidRequest ErrorCode = -32600
	CodeMethodNotFound ErrorCode = -32601
	CodeInvalidParams  ErrorCode = -32602
	CodeInternalError  ErrorCode = -32603

	CodeUnsupportedProtocolVersion ErrorCode = -32022
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
	Data    any       `json:"data,omitempty"`
}

// NewError creates a JSON-RPC error object.
func NewError(code ErrorCode, message string) *Error {
	return &Error{
		Code:    code,
		Message: message,
	}
}

// NewErrorWithData creates a JSON-RPC error object with machine-readable data.
func NewErrorWithData(code ErrorCode, message string, data any) *Error {
	return &Error{
		Code:    code,
		Message: message,
		Data:    data,
	}
}
