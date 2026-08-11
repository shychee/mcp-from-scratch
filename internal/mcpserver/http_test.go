package mcpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

func TestHTTPHandlerDispatchesStatelessRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		params  string
		mcpName string
	}{
		{name: "discover", method: "server/discover", params: `{}`},
		{name: "list", method: "tools/list", params: `{}`},
		{name: "call", method: "tools/call", params: `{"name":"echo","arguments":{"text":"over http"}}`, mcpName: "echo"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			request := modernHTTPRequest(t, i+1, tt.method, tt.params)
			if tt.mcpName != "" {
				request.Header.Set(protocol.HeaderName, tt.mcpName)
			}

			response := httptest.NewRecorder()
			New().HTTPHandler().ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != protocol.MediaTypeJSON {
				t.Fatalf("Content-Type = %q, want %q", got, protocol.MediaTypeJSON)
			}
			var rpcResponse protocol.Response
			if err := json.Unmarshal(response.Body.Bytes(), &rpcResponse); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if rpcResponse.Error != nil || len(rpcResponse.Result) == 0 {
				t.Fatalf("response = %#v, want successful result", rpcResponse)
			}
		})
	}
}

func TestHTTPHandlerRejectsMissingOrMismatchedHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{name: "missing version", mutate: func(r *http.Request) { r.Header.Del(protocol.HeaderProtocolVersion) }},
		{name: "mismatched version", mutate: func(r *http.Request) { r.Header.Set(protocol.HeaderProtocolVersion, "2025-06-18") }},
		{name: "missing method", mutate: func(r *http.Request) { r.Header.Del(protocol.HeaderMethod) }},
		{name: "mismatched method", mutate: func(r *http.Request) { r.Header.Set(protocol.HeaderMethod, "tools/list") }},
		{name: "missing name", mutate: func(r *http.Request) { r.Header.Del(protocol.HeaderName) }},
		{name: "mismatched name", mutate: func(r *http.Request) { r.Header.Set(protocol.HeaderName, "other") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			request := modernHTTPRequest(t, 1, "tools/call", `{"name":"echo","arguments":{"text":"hello"}}`)
			request.Header.Set(protocol.HeaderName, "echo")
			tt.mutate(request)

			response := httptest.NewRecorder()
			New().HTTPHandler().ServeHTTP(response, request)

			assertHTTPRPCError(t, response, http.StatusBadRequest, protocol.CodeHeaderMismatch)
		})
	}
}

func TestHTTPHandlerRejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()

	request := modernHTTPRequest(t, 1, "tools/list", `{}`)
	request.Header.Set(protocol.HeaderProtocolVersion, "2099-01-01")
	request.Body = io.NopCloser(requestBody(t, 1, "tools/list", `{}`, "2099-01-01"))

	response := httptest.NewRecorder()
	New().HTTPHandler().ServeHTTP(response, request)

	assertHTTPRPCError(t, response, http.StatusBadRequest, protocol.CodeUnsupportedProtocolVersion)
	var rpcResponse protocol.Response
	if err := json.Unmarshal(response.Body.Bytes(), &rpcResponse); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := rpcResponse.Error.Data.(map[string]any)
	if !ok || data["requested"] != "2099-01-01" {
		t.Fatalf("error data = %#v, want requested version", rpcResponse.Error.Data)
	}
}

func TestHTTPHandlerUsesModernStatusForUnknownMethod(t *testing.T) {
	t.Parallel()

	request := modernHTTPRequest(t, 1, "unknown/method", `{}`)
	response := httptest.NewRecorder()
	New().HTTPHandler().ServeHTTP(response, request)

	assertHTTPRPCError(t, response, http.StatusNotFound, protocol.CodeMethodNotFound)
}

func TestHTTPHandlerIgnoresSessionHeaderWithoutEchoingIt(t *testing.T) {
	t.Parallel()

	request := modernHTTPRequest(t, 1, "tools/list", `{}`)
	request.Header.Set(protocol.HeaderSessionID, "legacy-session")
	response := httptest.NewRecorder()
	New().HTTPHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if got := response.Header().Get(protocol.HeaderSessionID); got != "" {
		t.Fatalf("response session ID = %q, want empty", got)
	}
}

func TestHTTPHandlerRejectsNonPOSTMethods(t *testing.T) {
	t.Parallel()

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(method, "http://example.test/mcp", nil)
			response := httptest.NewRecorder()
			New().HTTPHandler().ServeHTTP(response, request)

			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", response.Code)
			}
		})
	}
}

func TestHTTPHandlerRejectsInvalidOrigin(t *testing.T) {
	t.Parallel()

	request := modernHTTPRequest(t, 1, "tools/list", `{}`)
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	New().HTTPHandler().ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.Code)
	}
}

func modernHTTPRequest(t *testing.T, id int, method, params string) *http.Request {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "http://example.test/mcp", nil)
	request.Body = io.NopCloser(requestBody(t, id, method, params, protocol.Version20260728))
	request.Header.Set("Content-Type", protocol.MediaTypeJSON)
	request.Header.Set("Accept", protocol.MediaTypeJSON+", "+protocol.MediaTypeSSE)
	request.Header.Set(protocol.HeaderProtocolVersion, protocol.Version20260728)
	request.Header.Set(protocol.HeaderMethod, method)
	return request
}

func requestBody(t *testing.T, id int, method, params, version string) *bytes.Reader {
	t.Helper()

	var decodedParams map[string]any
	if err := json.Unmarshal([]byte(params), &decodedParams); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	decodedParams["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    version,
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	body, err := json.Marshal(protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(id),
		Method:  method,
		Params:  mustMarshal(decodedParams),
	})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	return bytes.NewReader(body)
}

func assertHTTPRPCError(t *testing.T, response *httptest.ResponseRecorder, status int, code protocol.ErrorCode) {
	t.Helper()

	if response.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, status, response.Body.String())
	}
	var rpcResponse protocol.Response
	if err := json.Unmarshal(response.Body.Bytes(), &rpcResponse); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rpcResponse.Error == nil || rpcResponse.Error.Code != code {
		t.Fatalf("error = %#v, want code %d", rpcResponse.Error, code)
	}
}
