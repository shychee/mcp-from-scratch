package host

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

func TestTaskRequestUsesFlatMetadataAndTaskRoutingName(t *testing.T) {
	record := taskRequest(7, "tasks/get", map[string]any{"taskId": "task-7"})
	var params map[string]json.RawMessage
	if err := json.Unmarshal(record.Params, &params); err != nil {
		t.Fatal(err)
	}
	if _, nested := params["_meta"]; !nested {
		t.Fatal("task request omitted _meta")
	}
	var metadata protocol.RequestMeta
	if err := json.Unmarshal(params["_meta"], &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.ProtocolVersion != protocol.Version20260728 || metadata.ClientInfo == nil {
		t.Fatalf("metadata = %#v", metadata)
	}
	client := &httpRPCClient{ctx: context.Background(), endpoint: "http://example.test", client: http.DefaultClient}
	request, err := client.newRequest(record)
	if err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get(protocol.HeaderName); got != "task-7" {
		t.Fatalf("Mcp-Name = %q, want task-7", got)
	}
}

func TestValidateTaskSubscriptionAcknowledgementAcceptsSubset(t *testing.T) {
	notification := protocol.Notification{
		JSONRPC: "2.0",
		Method:  "notifications/subscriptions/acknowledged",
		Params: mustMarshalHost(map[string]any{
			"_meta":         map[string]any{"io.modelcontextprotocol/subscriptionId": 11},
			"notifications": map[string]any{"taskIds": []string{"task-1"}},
		}),
	}
	if err := validateTaskSubscriptionAcknowledgement(notification, 11, []string{"task-1", "task-2"}); err != nil {
		t.Fatal(err)
	}
	if err := validateTaskSubscriptionAcknowledgement(notification, 11, []string{"task-2"}); err == nil {
		t.Fatal("acknowledgement accepted an unrequested task")
	}
}

func TestHTTPTaskSubscriptionNextDecodesFullTask(t *testing.T) {
	stream := strings.NewReader("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tasks\",\"params\":{\"_meta\":{\"io.modelcontextprotocol/subscriptionId\":21},\"taskId\":\"task-1\",\"status\":\"completed\",\"createdAt\":\"2026-08-12T12:00:00Z\",\"lastUpdatedAt\":\"2026-08-12T12:00:01Z\",\"ttlMs\":null}}\n\n")
	subscription := &HTTPTaskSubscription{
		id: 21, reader: bufio.NewReader(stream), responseBody: io.NopCloser(strings.NewReader("")),
		taskIDs: map[string]struct{}{"task-1": {}},
	}
	defer subscription.Close()
	_, task, err := subscription.Next()
	if err != nil {
		t.Fatal(err)
	}
	if task.TaskID != "task-1" || task.Status != protocol.TaskStatusCompleted || task.TTLMS != nil {
		t.Fatalf("task = %#v", task)
	}
}

func TestHTTPTaskSubscriptionRejectsWrongSubscriptionOrTask(t *testing.T) {
	tests := []string{
		`{"_meta":{"io.modelcontextprotocol/subscriptionId":99},"taskId":"task-1","status":"completed","createdAt":"2026-08-12T12:00:00Z","lastUpdatedAt":"2026-08-12T12:00:01Z","ttlMs":null}`,
		`{"_meta":{"io.modelcontextprotocol/subscriptionId":21},"taskId":"task-2","status":"completed","createdAt":"2026-08-12T12:00:00Z","lastUpdatedAt":"2026-08-12T12:00:01Z","ttlMs":null}`,
	}
	for _, params := range tests {
		stream := strings.NewReader("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tasks\",\"params\":" + params + "}\n\n")
		subscription := &HTTPTaskSubscription{id: 21, reader: bufio.NewReader(stream), taskIDs: map[string]struct{}{"task-1": {}}}
		if _, _, err := subscription.Next(); err == nil {
			t.Fatalf("Next() accepted params %s", params)
		}
	}
}

func TestTaskSubscriptionSSEReaderIgnoresNonDataLines(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(": keepalive\ndata: {\"x\":1}\n\n"))
	raw, err := readSSEData(reader)
	if err != nil || string(raw) != `{"x":1}` {
		t.Fatalf("readSSEData() = %s, %v", raw, err)
	}
}
