package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

func TestHTTPTaskMethodsRequireTaskIDNameHeader(t *testing.T) {
	server := New()
	if err := server.SetExtensions(protocol.Extensions{protocol.TasksExtensionID: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"tasks/get", "tasks/update", "tasks/cancel"} {
		t.Run(method, func(t *testing.T) {
			params := `{"taskId":"task-123"}`
			request := modernHTTPRequest(t, 1, method, params)
			response := httptest.NewRecorder()
			server.HTTPHandler().ServeHTTP(response, request)
			assertHTTPRPCError(t, response, http.StatusBadRequest, protocol.CodeHeaderMismatch)

			request = modernHTTPRequest(t, 2, method, params)
			request.Header.Set(protocol.HeaderName, "task-123")
			response = httptest.NewRecorder()
			server.HTTPHandler().ServeHTTP(response, request)
			var rpcResponse protocol.Response
			if err := json.Unmarshal(response.Body.Bytes(), &rpcResponse); err != nil {
				t.Fatal(err)
			}
			if rpcResponse.Error == nil || rpcResponse.Error.Code != protocol.CodeMissingRequiredTaskCapability {
				t.Fatalf("response = %#v, want missing task capability before lookup", rpcResponse)
			}
		})
	}
}
