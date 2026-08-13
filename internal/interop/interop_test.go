package interop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shychee/mcp-from-scratch/internal/host"
	"github.com/shychee/mcp-from-scratch/internal/mcpserver"
	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

const officialSDKVersion = "v1.7.0"

func TestOfficialClientUsesServerOverStdio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	command := exec.Command("go", "run", "./cmd/mcp-server")
	command.Dir = projectRoot(t)
	client := mcp.NewClient(&mcp.Implementation{Name: "interop-client", Version: officialSDKVersion}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("official client connect: %v", err)
	}
	defer session.Close()

	assertOfficialClientFlow(t, ctx, session)
}

func TestOfficialClientUsesServerOverStreamableHTTP(t *testing.T) {
	server := httptest.NewServer(mcpserver.New().HTTPHandler())
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "interop-client", Version: officialSDKVersion}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             server.URL,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("official client connect: %v", err)
	}
	defer session.Close()

	assertOfficialClientFlow(t, ctx, session)
}

func TestHostUsesOfficialServerOverStdio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	transcript, err := host.RunDemo(ctx, host.ServerCommand{
		Name: "go",
		Args: []string{"run", "./cmd/mcp-official-fixture"},
		Dir:  projectRoot(t),
	})
	if err != nil {
		t.Fatalf("host with official stdio fixture: %v", err)
	}
	assertHostTranscript(t, transcript)
}

func TestHostUsesOfficialServerOverStreamableHTTP(t *testing.T) {
	official := newOfficialServer()
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return official },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transcript, err := host.RunHTTPDemo(ctx, server.URL)
	if err != nil {
		t.Fatalf("host with official HTTP fixture: %v", err)
	}
	assertHostTranscript(t, transcript)
}

func TestHostExercisesLegacyFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	transcript, err := host.RunDemo(ctx, host.ServerCommand{
		Name: "go",
		Args: []string{"run", "./cmd/mcp-server", "--legacy"},
		Dir:  projectRoot(t),
	})
	if err != nil {
		t.Fatalf("host legacy fallback: %v", err)
	}
	if transcript.Discovery.Result != nil {
		t.Fatalf("legacy transcript unexpectedly retained modern discovery: %s", transcript.Discovery.Result)
	}
	if len(transcript.DiscoveredTools) == 0 {
		t.Fatal("legacy fallback discovered no tools")
	}
}

func TestNegativeHTTPInteroperabilityMatrix(t *testing.T) {
	server := httptest.NewServer(mcpserver.New().HTTPHandler())
	defer server.Close()

	tests := []struct {
		name    string
		method  string
		params  string
		headers map[string]string
		status  int
		code    protocol.ErrorCode
	}{
		{
			name:   "unsupported version",
			method: "server/discover",
			params: `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientCapabilities":{}}}`,
			headers: map[string]string{
				"Mcp-Protocol-Version": "2099-01-01",
				"Mcp-Method":           "server/discover",
			},
			status: http.StatusBadRequest,
			code:   protocol.CodeUnsupportedProtocolVersion,
		},
		{
			name:   "header mismatch",
			method: "tools/list",
			params: modernParamsJSON(),
			headers: map[string]string{
				"Mcp-Protocol-Version": protocol.Version20260728,
				"Mcp-Method":           "resources/list",
			},
			status: http.StatusBadRequest,
			code:   protocol.CodeHeaderMismatch,
		},
		{
			name:   "invalid params",
			method: "tools/call",
			params: `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"name":"echo","arguments":{"message":1}}`,
			headers: map[string]string{
				"Mcp-Protocol-Version": protocol.Version20260728,
				"Mcp-Method":           "tools/call",
				"Mcp-Name":             "echo",
			},
			status: http.StatusOK,
			code:   protocol.CodeInvalidParams,
		},
		{
			name:   "unknown method status mapping",
			method: "unknown/method",
			params: modernParamsJSON(),
			headers: map[string]string{
				"Mcp-Protocol-Version": protocol.Version20260728,
				"Mcp-Method":           "unknown/method",
			},
			status: http.StatusNotFound,
			code:   protocol.CodeMethodNotFound,
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, index+1, test.method, test.params)
			request, err := http.NewRequest(http.MethodPost, server.URL, bytes.NewBufferString(body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			for name, value := range test.headers {
				request.Header.Set(name, value)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.status {
				data, _ := io.ReadAll(response.Body)
				t.Fatalf("HTTP status = %d, want %d; body=%s", response.StatusCode, test.status, data)
			}
			var rpcResponse protocol.Response
			if err := json.NewDecoder(response.Body).Decode(&rpcResponse); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if rpcResponse.Error == nil || rpcResponse.Error.Code != test.code {
				t.Fatalf("JSON-RPC error = %#v, want code %d", rpcResponse.Error, test.code)
			}
		})
	}
}

func TestMissingMetadataAndResultDecodingCompatibility(t *testing.T) {
	response := mcpserver.New().Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "server/discover",
		Params:  json.RawMessage(`{}`),
	})
	if response.Error == nil || response.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("missing metadata error = %#v, want %d", response.Error, protocol.CodeInvalidParams)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	legacy, err := host.RunDemo(ctx, host.ServerCommand{
		Name: "go",
		Args: []string{"run", "./cmd/mcp-server", "--legacy"},
		Dir:  projectRoot(t),
	})
	if err != nil {
		t.Fatalf("decode legacy result without resultType: %v", err)
	}
	if len(legacy.DiscoveredTools) == 0 {
		t.Fatal("legacy result decoding produced no tools")
	}
}

func TestBilingualSupportMatricesStayAligned(t *testing.T) {
	root := projectRoot(t)
	english := readFile(t, filepath.Join(root, "docs", "support-matrix.md"))
	chinese := readFile(t, filepath.Join(root, "docs", "support-matrix.zh.md"))

	for _, required := range []string{
		"2026-07-28",
		"v1.7.0",
		"make smoke",
		"make interop",
		"MCP Apps",
		"logging/setLevel",
	} {
		if !strings.Contains(english, required) || !strings.Contains(chinese, required) {
			t.Fatalf("support matrices must both contain %q", required)
		}
	}
	if englishRows, chineseRows := tableRowCount(english), tableRowCount(chinese); englishRows != chineseRows {
		t.Fatalf("support matrix row count: English=%d Chinese=%d", englishRows, chineseRows)
	}
}

func assertOfficialClientFlow(t *testing.T, ctx context.Context, session *mcp.ClientSession) {
	t.Helper()
	if got := session.InitializeResult().ProtocolVersion; got != protocol.Version20260728 {
		t.Fatalf("negotiated protocol version = %q, want %q", got, protocol.Version20260728)
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("official client tools/list: %v", err)
	}
	var foundEcho bool
	for _, tool := range listed.Tools {
		foundEcho = foundEcho || tool.Name == "echo"
	}
	if !foundEcho {
		t.Fatalf("official client tools/list = %#v, want echo", listed.Tools)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"text": "official client"},
	})
	if err != nil {
		t.Fatalf("official client tools/call: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("official client content count = %d, want 1", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != "official client" {
		t.Fatalf("official client content = %#v", result.Content[0])
	}
}

func assertHostTranscript(t *testing.T, transcript host.Transcript) {
	t.Helper()
	if transcript.Discovery.Error != nil || len(transcript.Discovery.Result) == 0 {
		t.Fatalf("host discovery = %#v", transcript.Discovery)
	}
	if transcript.EchoCall.Error != nil || len(transcript.EchoCall.Result) == 0 {
		t.Fatalf("host echo call = %#v", transcript.EchoCall)
	}
	if transcript.PreviewConfirmation.Error != nil || len(transcript.PreviewConfirmation.Result) == 0 {
		t.Fatalf("host MRTR completion = %#v", transcript.PreviewConfirmation)
	}
}

func newOfficialServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "official-go-sdk-fixture", Version: officialSDKVersion}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo a message"},
		func(_ context.Context, _ *mcp.CallToolRequest, input struct {
			Text string `json:"text"`
		}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: input.Text}}}, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "confirm_preview", Description: "Confirm a preview"},
		func(_ context.Context, request *mcp.CallToolRequest, input struct {
			Preview string `json:"preview"`
		}) (*mcp.CallToolResult, any, error) {
			if len(request.Params.InputResponses) == 0 {
				return &mcp.CallToolResult{
					InputRequests: mcp.InputRequestMap{"confirm_preview": &mcp.ElicitParams{
						Message: "Confirm this preview",
						RequestedSchema: &jsonschema.Schema{
							Type:       "object",
							Properties: map[string]*jsonschema.Schema{"confirm": {Type: "boolean"}},
							Required:   []string{"confirm"},
						},
					}},
					RequestState: "official-fixture-preview",
				}, nil, nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "confirmed: " + input.Preview}}}, nil, nil
		})
	return server
}

func projectRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve interop test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func tableRowCount(document string) int {
	rows := 0
	for _, line := range strings.Split(document, "\n") {
		if strings.HasPrefix(line, "| ") && !strings.HasPrefix(line, "| ---") && !strings.HasPrefix(line, "| Area ") && !strings.HasPrefix(line, "| Feature ") && !strings.HasPrefix(line, "| 范围 ") && !strings.HasPrefix(line, "| 能力 ") {
			rows++
		}
	}
	return rows
}

func modernParamsJSON() string {
	return `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}`
}
