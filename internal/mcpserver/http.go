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
	if rpcRequest.Method == "subscriptions/listen" {
		s.serveHTTPSubscription(writer, request, rpcRequest)
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

func (s *Server) serveHTTPSubscription(writer http.ResponseWriter, request *http.Request, rpcRequest protocol.Request) {
	if !acceptsMediaType(request.Header.Values("Accept"), protocol.MediaTypeJSON) ||
		!acceptsMediaType(request.Header.Values("Accept"), protocol.MediaTypeSSE) {
		writer.WriteHeader(http.StatusNotAcceptable)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}

	subscriber, acknowledged, err := s.subscribe(rpcRequest.ID, rpcRequest.Params)
	if err != nil {
		writeHTTPRPCResponse(writer, http.StatusOK, protocol.Response{
			JSONRPC: "2.0",
			ID:      rpcRequest.ID,
			Error:   err,
		})
		return
	}
	defer s.unsubscribe(subscriber)

	writer.Header().Set("Content-Type", protocol.MediaTypeSSE)
	writer.Header().Set("Cache-Control", "no-cache")
	writer.WriteHeader(http.StatusOK)
	if err := writeSSEMessage(writer, acknowledged); err != nil {
		return
	}
	flusher.Flush()

	for {
		select {
		case <-request.Context().Done():
			return
		case message := <-subscriber.events:
			if err := writeSSEMessage(writer, message.value); err != nil {
				return
			}
			flusher.Flush()
			if message.complete {
				return
			}
		}
	}
}

func acceptsMediaType(values []string, wanted string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			mediaType := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
			if strings.EqualFold(mediaType, wanted) {
				return true
			}
		}
	}
	return false
}

func writeSSEMessage(writer http.ResponseWriter, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = writer.Write(append(append([]byte("data: "), data...), '\n', '\n'))
	return err
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
	if protocol.MethodUsesNameHeader(rpcRequest.Method) {
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
