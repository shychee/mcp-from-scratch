package protocol

import (
	"encoding/json"
	"fmt"
	"strconv"
)

const (
	Version20260728         = "2026-07-28"
	ResultTypeComplete      = "complete"
	ResultTypeInputRequired = "input_required"
	CacheScopePublic        = "public"
	HeaderProtocolVersion   = "MCP-Protocol-Version"
	HeaderMethod            = "Mcp-Method"
	HeaderName              = "Mcp-Name"
	HeaderSessionID         = "Mcp-Session-Id"
	MediaTypeJSON           = "application/json"
	MediaTypeSSE            = "text/event-stream"
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
	TraceParent        string          `json:"traceparent,omitempty"`
	TraceState         string          `json:"tracestate,omitempty"`
	Baggage            string          `json:"baggage,omitempty"`
	LogLevel           string          `json:"io.modelcontextprotocol/logLevel,omitempty"`
	ProgressToken      any             `json:"progressToken,omitempty"`
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

	CodeHeaderMismatch                  ErrorCode = -32020
	CodeMissingRequiredClientCapability ErrorCode = -32021
	CodeUnsupportedProtocolVersion      ErrorCode = -32022
)

// RequestID is a JSON-RPC request identifier. JSON-RPC permits string and
// number identifiers; this project intentionally accepts integer numbers only.
// Its representation is comparable, so it can be used safely as a map key.
type RequestID struct {
	kind   uint8
	number int64
	text   string
}

const (
	requestIDNumber uint8 = 1
	requestIDString uint8 = 2
)

// ID creates an integer JSON-RPC request identifier.
func ID(value int) *RequestID {
	id := RequestID{kind: requestIDNumber, number: int64(value)}
	return &id
}

// StringID creates a string JSON-RPC request identifier.
func StringID(value string) *RequestID {
	id := RequestID{kind: requestIDString, text: value}
	return &id
}

// NewRequestID creates a request identifier from an int, int64, or string.
func NewRequestID(value any) (RequestID, error) {
	switch typed := value.(type) {
	case string:
		return RequestID{kind: requestIDString, text: typed}, nil
	case int:
		return RequestID{kind: requestIDNumber, number: int64(typed)}, nil
	case int64:
		return RequestID{kind: requestIDNumber, number: typed}, nil
	default:
		return RequestID{}, fmt.Errorf("request ID must be a string or integer, got %T", value)
	}
}

// IsString reports whether the identifier is a string identifier.
func (id RequestID) IsString() bool { return id.kind == requestIDString }

// Int64 returns the integer identifier and whether it is an integer ID.
func (id RequestID) Int64() (int64, bool) { return id.number, id.kind == requestIDNumber }

// String returns the source value of the identifier.
func (id RequestID) String() string {
	if id.kind == requestIDString {
		return id.text
	}
	if id.kind == requestIDNumber {
		return strconv.FormatInt(id.number, 10)
	}
	return ""
}

// MarshalJSON encodes a JSON-RPC identifier as a string or integer.
func (id RequestID) MarshalJSON() ([]byte, error) {
	switch id.kind {
	case requestIDNumber:
		return []byte(strconv.FormatInt(id.number, 10)), nil
	case requestIDString:
		return json.Marshal(id.text)
	default:
		return nil, fmt.Errorf("invalid request ID")
	}
}

// UnmarshalJSON decodes a JSON-RPC string or integer identifier.
func (id *RequestID) UnmarshalJSON(data []byte) error {
	if id == nil {
		return fmt.Errorf("cannot unmarshal request ID into nil pointer")
	}
	if len(data) == 0 || string(data) == "null" {
		return fmt.Errorf("request ID cannot be null")
	}
	if data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return fmt.Errorf("decode string request ID: %w", err)
		}
		id.kind, id.number, id.text = requestIDString, 0, text
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return fmt.Errorf("request ID must be a string or integer: %w", err)
	}
	value, err := strconv.ParseInt(string(number), 10, 64)
	if err != nil {
		return fmt.Errorf("request ID must be an integer: %w", err)
	}
	id.kind, id.number, id.text = requestIDNumber, value, ""
	return nil
}

// Request is the subset of JSON-RPC 2.0 request fields this learning project needs.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *RequestID      `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	idNull  bool
}

// UnmarshalJSON distinguishes an absent request ID (a notification) from an
// explicit null ID, which this project rejects as an invalid request.
func (request *Request) UnmarshalJSON(data []byte) error {
	type requestFields struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	var fields requestFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	request.JSONRPC = fields.JSONRPC
	request.Method = fields.Method
	request.Params = fields.Params
	request.ID = nil
	request.idNull = string(fields.ID) == "null"
	if len(fields.ID) == 0 || request.idNull {
		return nil
	}
	var id RequestID
	if err := json.Unmarshal(fields.ID, &id); err != nil {
		return err
	}
	request.ID = &id
	return nil
}

// Notification is a JSON-RPC message that does not carry an ID or receive a response.
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
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
	if request.idNull {
		return NewError(CodeInvalidRequest, "invalid request")
	}
	return nil
}

// Response is the subset of JSON-RPC 2.0 response fields this learning project needs.
// ID must be explicit null when the server cannot determine the request ID.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *RequestID      `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// MethodUsesNameHeader reports whether Streamable HTTP mirrors a name or URI.
func MethodUsesNameHeader(method string) bool {
	switch method {
	case "tools/call", "resources/read", "prompts/get", "tasks/get", "tasks/update", "tasks/cancel":
		return true
	default:
		return false
	}
}

// Error follows the JSON-RPC 2.0 error object shape.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Data    any       `json:"data,omitempty"`
}

// Error formats the JSON-RPC error without discarding its machine-readable data.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Data == nil {
		return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
	}

	data, err := json.Marshal(e.Data)
	if err != nil {
		return fmt.Sprintf("JSON-RPC error %d: %s (data: %v)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("JSON-RPC error %d: %s (data: %s)", e.Code, e.Message, data)
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
