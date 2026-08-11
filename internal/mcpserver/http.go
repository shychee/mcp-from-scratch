package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

// HTTPHandler serves the stateless MCP Streamable HTTP endpoint.
func (s *Server) HTTPHandler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

func (s *Server) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !validRequestOrigin(request) {
		writer.WriteHeader(http.StatusForbidden)
		return
	}

	var rpcRequest protocol.Request
	if err := json.NewDecoder(request.Body).Decode(&rpcRequest); err != nil {
		writeHTTPRPCResponse(writer, http.StatusBadRequest, protocol.Response{
			JSONRPC: "2.0",
			Error:   protocol.NewError(protocol.CodeParseError, "parse error"),
		})
		return
	}
	if requestError := protocol.ValidateRequest(rpcRequest); requestError != nil {
		writeHTTPRPCResponse(writer, http.StatusBadRequest, protocol.Response{
			JSONRPC: "2.0",
			ID:      rpcRequest.ID,
			Error:   requestError,
		})
		return
	}
	if headerError := validateHTTPHeaders(request, rpcRequest); headerError != nil {
		writeHTTPRPCResponse(writer, http.StatusBadRequest, protocol.Response{
			JSONRPC: "2.0",
			ID:      rpcRequest.ID,
			Error:   headerError,
		})
		return
	}

	response := s.Handle(request.Context(), rpcRequest)
	if rpcRequest.ID == nil {
		writer.WriteHeader(http.StatusAccepted)
		return
	}

	status := http.StatusOK
	if response.Error != nil {
		switch response.Error.Code {
		case protocol.CodeMethodNotFound:
			status = http.StatusNotFound
		case protocol.CodeUnsupportedProtocolVersion:
			status = http.StatusBadRequest
		}
	}
	writeHTTPRPCResponse(writer, status, response)
}

func validateHTTPHeaders(request *http.Request, rpcRequest protocol.Request) *protocol.Error {
	var params struct {
		protocol.RequestParams
		Name string `json:"name"`
		URI  string `json:"uri"`
	}
	if err := json.Unmarshal(rpcRequest.Params, &params); err != nil {
		return protocol.NewError(protocol.CodeHeaderMismatch, "MCP transport header mismatch")
	}

	headerVersion := request.Header.Get(protocol.HeaderProtocolVersion)
	if headerVersion == "" || headerVersion != params.Meta.ProtocolVersion {
		return protocol.NewError(protocol.CodeHeaderMismatch, "MCP transport header mismatch")
	}
	if headerVersion != protocolVersion {
		return protocol.NewErrorWithData(
			protocol.CodeUnsupportedProtocolVersion,
			"unsupported protocol version",
			unsupportedProtocolVersionData{
				Supported: []string{protocolVersion},
				Requested: headerVersion,
			},
		)
	}

	if headerMethod := request.Header.Get(protocol.HeaderMethod); headerMethod == "" || headerMethod != rpcRequest.Method {
		return protocol.NewError(protocol.CodeHeaderMismatch, "MCP transport header mismatch")
	}
	if methodHasName(rpcRequest.Method) {
		expectedName := params.Name
		if rpcRequest.Method == "resources/read" {
			expectedName = params.URI
		}
		if headerName := request.Header.Get(protocol.HeaderName); headerName == "" || headerName != expectedName {
			return protocol.NewError(protocol.CodeHeaderMismatch, "MCP transport header mismatch")
		}
	}
	return nil
}

func methodHasName(method string) bool {
	switch method {
	case "tools/call", "resources/read", "prompts/get":
		return true
	default:
		return false
	}
}

func validRequestOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Scheme != "" && strings.EqualFold(parsed.Host, request.Host)
}

func writeHTTPRPCResponse(writer http.ResponseWriter, status int, response protocol.Response) {
	writer.Header().Set("Content-Type", protocol.MediaTypeJSON)
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(response)
}
