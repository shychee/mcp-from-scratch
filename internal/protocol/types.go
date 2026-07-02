package protocol

import "encoding/json"

// Request is the subset of JSON-RPC 2.0 request fields this learning project needs.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is the subset of JSON-RPC 2.0 response fields this learning project needs.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error follows the JSON-RPC 2.0 error object shape.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
