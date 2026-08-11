package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

func TestHTTPSubscriptionAcknowledgesBeforeToolsListChanged(t *testing.T) {
	server := New()
	httpServer := httptest.NewServer(server.HTTPHandler())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response := openHTTPSubscription(t, ctx, httpServer.URL, 41, `{"toolsListChanged":true}`)
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)

	acknowledged := readSSEJSON(t, reader)
	assertSubscriptionNotification(t, acknowledged, "notifications/subscriptions/acknowledged", 41)
	var ackParams struct {
		Notifications subscriptionFilter `json:"notifications"`
	}
	decodeNotificationParams(t, acknowledged, &ackParams)
	if !ackParams.Notifications.ToolsListChanged {
		t.Fatalf("acknowledged notifications = %#v, want toolsListChanged", ackParams.Notifications)
	}

	if err := server.RegisterTool(NewEchoTool("late_echo")); err != nil {
		t.Fatalf("RegisterTool() error = %v", err)
	}
	changed := readSSEJSON(t, reader)
	assertSubscriptionNotification(t, changed, "notifications/tools/list_changed", 41)
}

func TestSubscriptionDoesNotPublishUnrequestedNotificationTypes(t *testing.T) {
	server := New()
	subscription, acknowledged, err := server.subscribe(protocol.ID(42), json.RawMessage(`{
		"_meta": {
			"io.modelcontextprotocol/protocolVersion": "2026-07-28",
			"io.modelcontextprotocol/clientCapabilities": {}
		},
		"notifications": {"promptsListChanged": true}
	}`))
	if err != nil {
		t.Fatalf("subscribe() error = %v", err)
	}
	defer server.unsubscribe(subscription)
	var params struct {
		Notifications subscriptionFilter `json:"notifications"`
	}
	decodeNotificationParams(t, mustMarshal(acknowledged), &params)
	if params.Notifications.ToolsListChanged || bytes.Contains(mustMarshal(acknowledged), []byte("promptsListChanged")) {
		t.Fatalf("agreed notifications = %#v, want empty supported subset", params.Notifications)
	}

	if err := server.RegisterTool(NewEchoTool("late_echo")); err != nil {
		t.Fatalf("RegisterTool() error = %v", err)
	}
	select {
	case message := <-subscription.events:
		t.Fatalf("unrequested subscription message = %#v, want none", message)
	default:
	}
}

func TestSubscriptionPreservesUnsupportedProtocolVersionError(t *testing.T) {
	server := New()
	_, _, protocolError := server.subscribe(protocol.ID(45), json.RawMessage(`{
		"_meta": {
			"io.modelcontextprotocol/protocolVersion": "2099-01-01",
			"io.modelcontextprotocol/clientCapabilities": {}
		},
		"notifications": {"toolsListChanged": true}
	}`))
	if protocolError == nil || protocolError.Code != protocol.CodeUnsupportedProtocolVersion {
		t.Fatalf("subscription error = %#v, want unsupported protocol version", protocolError)
	}
}

func TestSubscriptionPublishesOneTaggedEventPerConcurrentListener(t *testing.T) {
	server := New()
	params := modernRequestParamsWithNotifications(t, map[string]any{"toolsListChanged": true})

	first, _, err := server.subscribe(protocol.ID(71), params)
	if err != nil {
		t.Fatalf("subscribe first: %v", err)
	}
	defer server.unsubscribe(first)
	second, _, err := server.subscribe(protocol.ID(72), params)
	if err != nil {
		t.Fatalf("subscribe second: %v", err)
	}
	defer server.unsubscribe(second)

	if err := server.RegisterTool(NewEchoTool("late_echo")); err != nil {
		t.Fatalf("RegisterTool() error = %v", err)
	}
	for _, tt := range []struct {
		name       string
		subscriber *subscription
		id         int
	}{
		{name: "first", subscriber: first, id: 71},
		{name: "second", subscriber: second, id: 72},
	} {
		t.Run(tt.name, func(t *testing.T) {
			message := <-tt.subscriber.events
			assertSubscriptionNotification(t, mustMarshal(message.value), "notifications/tools/list_changed", tt.id)
			select {
			case extra := <-tt.subscriber.events:
				t.Fatalf("extra subscription event = %#v, want exactly one", extra)
			default:
			}
		})
	}
}

func TestHTTPSubscriptionDisconnectCleansUp(t *testing.T) {
	server := New()
	httpServer := httptest.NewServer(server.HTTPHandler())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response := openHTTPSubscription(t, ctx, httpServer.URL, 43, `{"toolsListChanged":true}`)
	reader := bufio.NewReader(response.Body)
	_ = readSSEJSON(t, reader)
	if got := server.activeSubscriptionCount(); got != 1 {
		t.Fatalf("active subscriptions = %d, want 1", got)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close subscription response: %v", err)
	}
	eventually(t, func() bool { return server.activeSubscriptionCount() == 0 })
}

func TestHTTPSubscriptionGracefulCloseSendsCompleteResult(t *testing.T) {
	server := New()
	httpServer := httptest.NewServer(server.HTTPHandler())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response := openHTTPSubscription(t, ctx, httpServer.URL, 44, `{"toolsListChanged":true}`)
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	_ = readSSEJSON(t, reader)

	server.CloseSubscriptions()
	completed := readSSEJSON(t, reader)
	var rpcResponse struct {
		ID     *int `json:"id"`
		Result struct {
			ResultType string           `json:"resultType"`
			Meta       subscriptionMeta `json:"_meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal(completed, &rpcResponse); err != nil {
		t.Fatalf("decode complete response: %v", err)
	}
	if rpcResponse.ID == nil || *rpcResponse.ID != 44 || rpcResponse.Result.ResultType != protocol.ResultTypeComplete {
		t.Fatalf("complete response = %#v, want id 44 and complete", rpcResponse)
	}
	if rpcResponse.Result.Meta.SubscriptionID != 44 {
		t.Fatalf("complete subscription ID = %d, want 44", rpcResponse.Result.Meta.SubscriptionID)
	}
	eventually(t, func() bool { return server.activeSubscriptionCount() == 0 })
}

func TestServeSubscriptionAcknowledgesPublishesAndCancels(t *testing.T) {
	server := New()
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(context.Background(), inputReader, outputWriter)
	}()

	encoder := json.NewEncoder(inputWriter)
	decoder := json.NewDecoder(outputReader)
	listen := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(51),
		Method:  "subscriptions/listen",
		Params: modernRequestParamsWithNotifications(t, map[string]any{
			"toolsListChanged": true,
		}),
	}
	if err := encoder.Encode(listen); err != nil {
		t.Fatalf("encode listen request: %v", err)
	}

	var acknowledged protocol.Notification
	if err := decoder.Decode(&acknowledged); err != nil {
		t.Fatalf("decode acknowledged notification: %v", err)
	}
	assertSubscriptionNotification(t, mustMarshal(acknowledged), "notifications/subscriptions/acknowledged", 51)

	if err := server.RegisterTool(NewEchoTool("late_echo")); err != nil {
		t.Fatalf("RegisterTool() error = %v", err)
	}
	var changed protocol.Notification
	if err := decoder.Decode(&changed); err != nil {
		t.Fatalf("decode tools list changed notification: %v", err)
	}
	assertSubscriptionNotification(t, mustMarshal(changed), "notifications/tools/list_changed", 51)

	cancelled := protocol.Notification{
		JSONRPC: "2.0",
		Method:  "notifications/cancelled",
		Params:  json.RawMessage(`{"requestId":51,"reason":"test complete"}`),
	}
	if err := encoder.Encode(cancelled); err != nil {
		t.Fatalf("encode cancelled notification: %v", err)
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatalf("close server input: %v", err)
	}

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve() did not stop after input closed")
	}
	if got := server.activeSubscriptionCount(); got != 0 {
		t.Fatalf("active subscriptions = %d, want 0", got)
	}
}

func openHTTPSubscription(t *testing.T, ctx context.Context, endpoint string, id int, notifications string) *http.Response {
	t.Helper()

	var filter map[string]any
	if err := json.Unmarshal([]byte(notifications), &filter); err != nil {
		t.Fatalf("decode notifications: %v", err)
	}
	params := map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    protocol.Version20260728,
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
		"notifications": filter,
	}
	body, err := json.Marshal(protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(id),
		Method:  "subscriptions/listen",
		Params:  mustMarshal(params),
	})
	if err != nil {
		t.Fatalf("encode listen request: %v", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create listen request: %v", err)
	}
	request.Header.Set("Content-Type", protocol.MediaTypeJSON)
	request.Header.Set("Accept", protocol.MediaTypeJSON+", "+protocol.MediaTypeSSE)
	request.Header.Set(protocol.HeaderProtocolVersion, protocol.Version20260728)
	request.Header.Set(protocol.HeaderMethod, "subscriptions/listen")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open subscription: %v", err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != protocol.MediaTypeSSE {
		response.Body.Close()
		t.Fatalf("subscription response = %d %q, want 200 SSE", response.StatusCode, response.Header.Get("Content-Type"))
	}
	return response
}

func readSSEJSON(t *testing.T, reader *bufio.Reader) json.RawMessage {
	t.Helper()

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE event: %v", err)
		}
		if strings.HasPrefix(line, "data: ") {
			return json.RawMessage(strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
		}
	}
}

func assertSubscriptionNotification(t *testing.T, raw json.RawMessage, method string, id int) {
	t.Helper()

	var notification protocol.Notification
	if err := json.Unmarshal(raw, &notification); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if notification.Method != method {
		t.Fatalf("notification method = %q, want %q", notification.Method, method)
	}
	var params struct {
		Meta subscriptionMeta `json:"_meta"`
	}
	decodeNotificationParams(t, raw, &params)
	if params.Meta.SubscriptionID != id {
		t.Fatalf("subscription ID = %d, want %d", params.Meta.SubscriptionID, id)
	}
}

func decodeNotificationParams(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()

	var envelope struct {
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode notification envelope: %v", err)
	}
	if err := json.Unmarshal(envelope.Params, target); err != nil {
		t.Fatalf("decode notification params: %v", err)
	}
}

func modernRequestParamsWithNotifications(t *testing.T, notifications map[string]any) json.RawMessage {
	t.Helper()

	params := map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    protocol.Version20260728,
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
		"notifications": notifications,
	}
	return mustMarshal(params)
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}
