package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

func TestTaskSubscriptionAcknowledgesSubsetAndFiltersOwner(t *testing.T) {
	server := New()
	if err := server.SetExtensions(protocol.Extensions{protocol.TasksExtensionID: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := server.RegisterDeferredEchoTool(); err != nil {
		t.Fatal(err)
	}
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/call",
		Params: taskRequestParams(t, "alice", map[string]any{"name": deferredEchoToolName, "arguments": map[string]any{"text": "hello"}}, true),
	})
	var created protocol.CreateTaskResult
	if err := json.Unmarshal(response.Result, &created); err != nil {
		t.Fatal(err)
	}
	subscriber, acknowledged, taskError := server.subscribe(context.Background(), protocol.ID(2), taskRequestParams(t, "alice", map[string]any{
		"notifications": map[string]any{"taskIds": []string{created.TaskID, "unknown"}},
	}, true))
	if taskError != nil {
		t.Fatalf("subscribe() error = %v", taskError)
	}
	defer server.unsubscribe(subscriber)
	var ack struct {
		Notifications protocol.TaskSubscriptionFilter `json:"notifications"`
	}
	if err := json.Unmarshal(acknowledged.Params, &ack); err != nil {
		t.Fatal(err)
	}
	if len(ack.Notifications.TaskIDs) != 1 || ack.Notifications.TaskIDs[0] != created.TaskID {
		t.Fatalf("ack task IDs = %#v", ack.Notifications.TaskIDs)
	}
	approval, _ := json.Marshal(map[string]any{"action": "accept", "content": map[string]bool{"confirm": true}})
	if _, err := server.taskRepository().Update(context.Background(), created.TaskID, "alice", map[string]json.RawMessage{"approval": approval}); err != nil {
		t.Fatal(err)
	}
	record, _ := server.taskRepository().Get(context.Background(), created.TaskID, "alice")
	server.publishTaskChanged("alice", record.Task)
	notification := <-subscriber.events
	if notification.Method != "notifications/tasks" {
		t.Fatalf("notification method = %q", notification.Method)
	}
	var params protocol.DetailedTask
	if err := json.Unmarshal(notification.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.TaskID != created.TaskID || params.Status != protocol.TaskStatusCompleted {
		t.Fatalf("task notification = %#v", params)
	}
}
