package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

type taskTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *taskTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *taskTestClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func TestMemoryTaskRepositoryEnforcesOwnerAndTTL(t *testing.T) {
	t.Parallel()
	clock := &taskTestClock{now: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	repository := NewMemoryTaskRepository(clock)
	ttl := int64(100)
	task := protocol.DetailedTask{
		TaskID:        "task-1",
		Status:        protocol.TaskStatusWorking,
		CreatedAt:     clock.Now().Format(time.RFC3339Nano),
		LastUpdatedAt: clock.Now().Format(time.RFC3339Nano),
		TTLMS:         &ttl,
	}
	if err := repository.Create(context.Background(), TaskRecord{Task: task, Owner: "alice"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := repository.Get(context.Background(), "task-1", "bob"); !errors.Is(err, ErrTaskForeign) {
		t.Fatalf("Get(foreign) error = %v, want ErrTaskForeign", err)
	}
	clock.Advance(101 * time.Millisecond)
	if _, err := repository.Get(context.Background(), "task-1", "alice"); !errors.Is(err, ErrTaskExpired) {
		t.Fatalf("Get(expired) error = %v, want ErrTaskExpired", err)
	}
}

func TestServerDeferredEchoTaskLifecycle(t *testing.T) {
	server := New()
	if err := server.SetExtensions(protocol.Extensions{protocol.TasksExtensionID: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("SetExtensions() error = %v", err)
	}
	if err := server.RegisterDeferredEchoTool(); err != nil {
		t.Fatalf("RegisterDeferredEchoTool() error = %v", err)
	}

	initial := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "tools/call",
		Params:  taskRequestParams(t, "alice", map[string]any{"name": deferredEchoToolName, "arguments": map[string]any{"text": "hello"}}, true),
	})
	if initial.Error != nil {
		t.Fatalf("initial tools/call error = %v", initial.Error)
	}
	var created protocol.CreateTaskResult
	if err := json.Unmarshal(initial.Result, &created); err != nil {
		t.Fatalf("decode CreateTaskResult: %v", err)
	}
	if created.ResultType != "task" || created.TaskID == "" || created.Status != protocol.TaskStatusInputRequired {
		t.Fatalf("created task = %#v", created)
	}
	if created.Meta.ServerInfo.Name == "" {
		t.Fatal("CreateTaskResult omitted server metadata")
	}

	get := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(2), Method: "tasks/get",
		Params: taskRequestParams(t, "alice", map[string]any{"taskId": created.TaskID}, true),
	})
	if get.Error != nil {
		t.Fatalf("tasks/get error = %v", get.Error)
	}
	var waiting protocol.GetTaskResult
	if err := json.Unmarshal(get.Result, &waiting); err != nil {
		t.Fatalf("decode tasks/get: %v", err)
	}
	if waiting.ResultType != protocol.ResultTypeComplete || waiting.Status != protocol.TaskStatusInputRequired || waiting.InputRequests[deferredEchoInputKey].Method != "elicitation/create" {
		t.Fatalf("waiting task = %#v", waiting)
	}

	update := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(3), Method: "tasks/update",
		Params: taskRequestParams(t, "alice", map[string]any{
			"taskId": created.TaskID,
			"inputResponses": map[string]any{
				deferredEchoInputKey: map[string]any{"action": "accept", "content": map[string]any{"confirm": true}},
			},
		}, true),
	})
	if update.Error != nil {
		t.Fatalf("tasks/update error = %v", update.Error)
	}
	var ack protocol.TaskUpdateResult
	if err := json.Unmarshal(update.Result, &ack); err != nil {
		t.Fatalf("decode update ack: %v", err)
	}
	if ack.ResultType != protocol.ResultTypeComplete || ack.Meta.ServerInfo.Name == "" {
		t.Fatalf("update ack = %#v", ack)
	}

	completed := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(4), Method: "tasks/get",
		Params: taskRequestParams(t, "alice", map[string]any{"taskId": created.TaskID}, true),
	})
	var final protocol.GetTaskResult
	if completed.Error != nil {
		t.Fatalf("completed tasks/get error = %v", completed.Error)
	}
	if err := json.Unmarshal(completed.Result, &final); err != nil {
		t.Fatalf("decode completed tasks/get: %v", err)
	}
	if final.Status != protocol.TaskStatusCompleted || len(final.DetailedTask.Result) == 0 {
		t.Fatalf("final task = %#v", final)
	}
}

func TestServerTasksRequireNegotiatedCapability(t *testing.T) {
	server := New()
	if err := server.SetExtensions(protocol.Extensions{protocol.TasksExtensionID: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "tasks/get",
		Params: taskRequestParams(t, "alice", map[string]any{"taskId": "missing"}, false),
	})
	if response.Error == nil || response.Error.Code != protocol.CodeMissingRequiredTaskCapability {
		t.Fatalf("tasks/get error = %#v, want missing capability", response.Error)
	}
}

func TestServerDoesNotCreateUnregisteredTaskTool(t *testing.T) {
	server := New()
	if err := server.SetExtensions(protocol.Extensions{protocol.TasksExtensionID: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	response := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/call",
		Params: taskRequestParams(t, "alice", map[string]any{"name": deferredEchoToolName, "arguments": map[string]any{"text": "hidden"}}, true),
	})
	if response.Error == nil || response.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("response error = %#v, want unknown tool", response.Error)
	}
}

func TestServerTasksRejectMalformedExtensionSettings(t *testing.T) {
	server := New()
	if err := server.SetExtensions(protocol.Extensions{protocol.TasksExtensionID: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	params := taskRequestParams(t, "alice", map[string]any{"taskId": "missing"}, false)
	var decoded map[string]any
	if err := json.Unmarshal(params, &decoded); err != nil {
		t.Fatal(err)
	}
	meta := decoded["_meta"].(map[string]any)
	meta["io.modelcontextprotocol/clientCapabilities"].(map[string]any)["extensions"] = map[string]any{protocol.TasksExtensionID: nil}
	response := server.Handle(context.Background(), protocol.Request{JSONRPC: "2.0", ID: protocol.ID(1), Method: "tasks/get", Params: mustMarshal(decoded)})
	if response.Error == nil || response.Error.Code != protocol.CodeMissingRequiredTaskCapability {
		t.Fatalf("response error = %#v", response.Error)
	}
}

func TestExecutionPrincipalOverridesClientInfoForTasks(t *testing.T) {
	server := New()
	if err := server.SetExtensions(protocol.Extensions{protocol.TasksExtensionID: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := server.RegisterDeferredEchoTool(); err != nil {
		t.Fatal(err)
	}
	ctx := WithExecutionPrincipal(context.Background(), "authenticated-alice")
	createdResponse := server.Handle(ctx, protocol.Request{JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/call", Params: taskRequestParams(t, "spoofed", map[string]any{"name": deferredEchoToolName, "arguments": map[string]any{"text": "hello"}}, true)})
	var created protocol.CreateTaskResult
	if err := json.Unmarshal(createdResponse.Result, &created); err != nil {
		t.Fatal(err)
	}
	spoofed := server.Handle(context.Background(), protocol.Request{JSONRPC: "2.0", ID: protocol.ID(2), Method: "tasks/get", Params: taskRequestParams(t, "spoofed", map[string]any{"taskId": created.TaskID}, true)})
	if spoofed.Error == nil || spoofed.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("spoofed access error = %#v", spoofed.Error)
	}
}

func TestMemoryTaskRepositoryClonesMetadata(t *testing.T) {
	repository := NewMemoryTaskRepository(nil)
	ttl := int64(1000)
	record := TaskRecord{Owner: "alice", Metadata: map[string]string{"text": "original"}, Task: protocol.DetailedTask{TaskID: "metadata", Status: protocol.TaskStatusWorking, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), LastUpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), TTLMS: &ttl}}
	if err := repository.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	record.Metadata["text"] = "mutated"
	stored, err := repository.Get(context.Background(), "metadata", "alice")
	if err != nil || stored.Metadata["text"] != "original" {
		t.Fatalf("stored metadata = %#v, err=%v", stored.Metadata, err)
	}
	stored.Metadata["text"] = "again"
	again, _ := repository.Get(context.Background(), "metadata", "alice")
	if again.Metadata["text"] != "original" {
		t.Fatalf("repository metadata mutated through read: %#v", again.Metadata)
	}
}

func TestServerTasksRejectForeignAndTerminalMutation(t *testing.T) {
	server := New()
	if err := server.SetExtensions(protocol.Extensions{protocol.TasksExtensionID: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := server.RegisterDeferredEchoTool(); err != nil {
		t.Fatal(err)
	}
	initial := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/call",
		Params: taskRequestParams(t, "alice", map[string]any{"name": deferredEchoToolName, "arguments": map[string]any{"text": "hello"}}, true),
	})
	var created protocol.CreateTaskResult
	if err := json.Unmarshal(initial.Result, &created); err != nil {
		t.Fatal(err)
	}
	foreign := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(2), Method: "tasks/update",
		Params: taskRequestParams(t, "bob", map[string]any{"taskId": created.TaskID, "inputResponses": map[string]any{deferredEchoInputKey: map[string]any{"action": "accept", "content": map[string]any{"confirm": true}}}}, true),
	})
	if foreign.Error == nil || foreign.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("foreign update error = %#v, want invalid params", foreign.Error)
	}

	accepted := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(3), Method: "tasks/update",
		Params: taskRequestParams(t, "alice", map[string]any{"taskId": created.TaskID, "inputResponses": map[string]any{deferredEchoInputKey: map[string]any{"action": "accept", "content": map[string]any{"confirm": true}}}}, true),
	})
	if accepted.Error != nil {
		t.Fatal(accepted.Error)
	}
	terminal := server.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(4), Method: "tasks/cancel",
		Params: taskRequestParams(t, "alice", map[string]any{"taskId": created.TaskID}, true),
	})
	if terminal.Error == nil || terminal.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("terminal cancel error = %#v, want invalid params", terminal.Error)
	}
}

func TestTasksRepositoryIsSharedAcrossServerInstances(t *testing.T) {
	repository := NewMemoryTaskRepository(nil)
	first := New()
	second := New()
	for _, server := range []*Server{first, second} {
		if err := server.SetExtensions(protocol.Extensions{protocol.TasksExtensionID: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if err := server.SetTaskRepository(repository); err != nil {
			t.Fatal(err)
		}
	}
	if err := first.RegisterDeferredEchoTool(); err != nil {
		t.Fatal(err)
	}
	initial := first.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(1), Method: "tools/call",
		Params: taskRequestParams(t, "shared-owner", map[string]any{"name": deferredEchoToolName, "arguments": map[string]any{"text": "across instances"}}, true),
	})
	var created protocol.CreateTaskResult
	if err := json.Unmarshal(initial.Result, &created); err != nil {
		t.Fatal(err)
	}
	get := second.Handle(context.Background(), protocol.Request{
		JSONRPC: "2.0", ID: protocol.ID(2), Method: "tasks/get",
		Params: taskRequestParams(t, "shared-owner", map[string]any{"taskId": created.TaskID}, true),
	})
	if get.Error != nil {
		t.Fatalf("second server tasks/get error = %v", get.Error)
	}
}

func taskRequestParams(t *testing.T, owner string, fields map[string]any, tasks bool) json.RawMessage {
	t.Helper()
	meta := map[string]any{
		"io.modelcontextprotocol/protocolVersion":    protocol.Version20260728,
		"io.modelcontextprotocol/clientInfo":         map[string]string{"name": owner, "version": "test"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	if tasks {
		meta["io.modelcontextprotocol/clientCapabilities"].(map[string]any)["extensions"] = map[string]any{protocol.TasksExtensionID: map[string]any{}}
	}
	params := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		params[key] = value
	}
	params["_meta"] = meta
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
