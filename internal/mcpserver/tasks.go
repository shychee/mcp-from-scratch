package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

const (
	tasksDefaultTTL      int64 = 5 * 60 * 1000
	tasksDefaultPoll     int64 = 25
	deferredEchoToolName       = "deferred_echo"
	deferredEchoInputKey       = "approval"
)

var (
	ErrTaskNotFound = errors.New("task not found")
	ErrTaskExpired  = errors.New("task expired")
	ErrTaskForeign  = errors.New("task belongs to another owner")
	ErrTaskTerminal = errors.New("task is terminal")
)

// Clock is the small time boundary used by the repository. Production uses
// RealClock; tests can provide a deterministic clock without sleeping.
type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

// TaskRecord is the persistence boundary for the Tasks extension. Owner is a
// client principal for this learning project; production must use an
// authenticated principal rather than a user-controlled clientInfo.name.
type TaskRecord struct {
	Task     protocol.DetailedTask
	Owner    string
	Metadata map[string]string
}

// TaskRepository stores task state durably enough for a single process. The
// interface intentionally keeps persistence separate from transport/session
// state so it can later be backed by a database or a distributed store.
type TaskRepository interface {
	Create(context.Context, TaskRecord) error
	Get(context.Context, string, string) (TaskRecord, error)
	Update(context.Context, string, string, map[string]json.RawMessage) (TaskRecord, error)
	Cancel(context.Context, string, string) (TaskRecord, error)
}

// MemoryTaskRepository is a concurrent in-memory repository with TTL checks
// on every read and mutation. It is intentionally process-local and is not a
// production durability implementation.
type MemoryTaskRepository struct {
	mu    sync.RWMutex
	clock Clock
	tasks map[string]TaskRecord
}

func NewMemoryTaskRepository(clock Clock) *MemoryTaskRepository {
	if clock == nil {
		clock = RealClock{}
	}
	return &MemoryTaskRepository{clock: clock, tasks: make(map[string]TaskRecord)}
}

// Now exposes the repository clock to the server so task creation and expiry
// use one consistent time source.
func (r *MemoryTaskRepository) Now() time.Time {
	return r.clock.Now().UTC()
}

func (r *MemoryTaskRepository) Create(ctx context.Context, record TaskRecord) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if record.Owner == "" {
		return fmt.Errorf("task owner is required")
	}
	if err := record.Task.Validate(); err != nil {
		return err
	}
	if record.Task.Status.Terminal() {
		return fmt.Errorf("new task cannot be terminal")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tasks[record.Task.TaskID]; exists {
		return fmt.Errorf("task %q already exists", record.Task.TaskID)
	}
	record.Task = cloneTask(record.Task)
	record.Task.InputResponses = nil
	record.Metadata = cloneStringMap(record.Metadata)
	r.tasks[record.Task.TaskID] = record
	return nil
}

func (r *MemoryTaskRepository) Get(ctx context.Context, taskID, owner string) (TaskRecord, error) {
	if err := contextErr(ctx); err != nil {
		return TaskRecord{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.lookupLocked(taskID, owner)
	if err != nil {
		return TaskRecord{}, err
	}
	return cloneRecord(record), nil
}

func (r *MemoryTaskRepository) Update(ctx context.Context, taskID, owner string, inputResponses map[string]json.RawMessage) (TaskRecord, error) {
	if err := contextErr(ctx); err != nil {
		return TaskRecord{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.lookupLocked(taskID, owner)
	if err != nil {
		return TaskRecord{}, err
	}
	if record.Task.Status.Terminal() {
		return TaskRecord{}, ErrTaskTerminal
	}
	if record.Task.Status != protocol.TaskStatusInputRequired {
		return TaskRecord{}, fmt.Errorf("task is not awaiting input")
	}
	if len(inputResponses) == 0 {
		return TaskRecord{}, fmt.Errorf("inputResponses is required")
	}
	for key, raw := range inputResponses {
		if _, requested := record.Task.InputRequests[key]; !requested {
			return TaskRecord{}, fmt.Errorf("input response %q was not requested", key)
		}
		if !json.Valid(raw) {
			return TaskRecord{}, fmt.Errorf("input response %q is invalid JSON", key)
		}
	}
	for key, raw := range inputResponses {
		if key != deferredEchoInputKey {
			return TaskRecord{}, fmt.Errorf("unsupported input response %q", key)
		}
		var approval struct {
			Action  string `json:"action"`
			Content struct {
				Confirm bool `json:"confirm"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &approval); err != nil || approval.Action != "accept" || !approval.Content.Confirm {
			return TaskRecord{}, fmt.Errorf("approval must accept with confirm=true")
		}
	}
	record.Task.InputResponses = cloneRawMap(inputResponses)
	record.Task.InputRequests = nil
	record.Task.Status = protocol.TaskStatusCompleted
	record.Task.StatusMessage = "deferred echo completed"
	now := r.clock.Now().UTC().Format(time.RFC3339Nano)
	record.Task.LastUpdatedAt = now
	text := record.Metadata["deferred_echo_text"]
	if text == "" {
		text = "deferred echo approved"
	}
	record.Task.Result = mustMarshal(ToolResult{
		Result:  newResult(),
		Content: []ContentBlock{{Type: "text", Text: text}},
	})
	r.tasks[taskID] = record
	return cloneRecord(record), nil
}

func (r *MemoryTaskRepository) Cancel(ctx context.Context, taskID, owner string) (TaskRecord, error) {
	if err := contextErr(ctx); err != nil {
		return TaskRecord{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.lookupLocked(taskID, owner)
	if err != nil {
		return TaskRecord{}, err
	}
	if record.Task.Status.Terminal() {
		return TaskRecord{}, ErrTaskTerminal
	}
	record.Task.InputRequests = nil
	record.Task.Status = protocol.TaskStatusCancelled
	record.Task.StatusMessage = "task cancelled"
	record.Task.LastUpdatedAt = r.clock.Now().UTC().Format(time.RFC3339Nano)
	r.tasks[taskID] = record
	return cloneRecord(record), nil
}

func (r *MemoryTaskRepository) lookupLocked(taskID, owner string) (TaskRecord, error) {
	if taskID == "" {
		return TaskRecord{}, ErrTaskNotFound
	}
	record, ok := r.tasks[taskID]
	if !ok {
		return TaskRecord{}, ErrTaskNotFound
	}
	if owner == "" || record.Owner != owner {
		return TaskRecord{}, ErrTaskForeign
	}
	if expired(record.Task, r.clock.Now()) {
		delete(r.tasks, taskID)
		return TaskRecord{}, ErrTaskExpired
	}
	return record, nil
}

func expired(task protocol.DetailedTask, now time.Time) bool {
	if task.TTLMS == nil || *task.TTLMS < 0 {
		return false
	}
	created, err := time.Parse(time.RFC3339Nano, task.CreatedAt)
	if err != nil {
		return true
	}
	return !now.Before(created.Add(time.Duration(*task.TTLMS) * time.Millisecond))
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func cloneRecord(record TaskRecord) TaskRecord {
	record.Task = cloneTask(record.Task)
	record.Metadata = cloneStringMap(record.Metadata)
	return record
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneTask(task protocol.DetailedTask) protocol.DetailedTask {
	task.InputRequests = cloneInputRequests(task.InputRequests)
	task.InputResponses = cloneRawMap(task.InputResponses)
	task.Result = append(json.RawMessage(nil), task.Result...)
	if task.TTLMS != nil {
		value := *task.TTLMS
		task.TTLMS = &value
	}
	if task.Error != nil {
		errorCopy := *task.Error
		task.Error = &errorCopy
	}
	return task
}

func cloneInputRequests(input map[string]protocol.TaskInputRequest) map[string]protocol.TaskInputRequest {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]protocol.TaskInputRequest, len(input))
	for key, request := range input {
		request.Params.RequestedSchema = cloneAnyMap(request.Params.RequestedSchema)
		output[key] = request
	}
	return output
}

func cloneRawMap(input map[string]json.RawMessage) map[string]json.RawMessage {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]json.RawMessage, len(input))
	for key, value := range input {
		output[key] = append(json.RawMessage(nil), value...)
	}
	return output
}

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func newTaskID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate task ID: %w", err)
	}
	return hex.EncodeToString(random[:]), nil
}

func tasksCapability(capabilities map[string]any) bool {
	rawExtensions, ok := capabilities["extensions"]
	if !ok {
		return false
	}
	encoded, err := json.Marshal(rawExtensions)
	if err != nil {
		return false
	}
	var extensions protocol.Extensions
	if json.Unmarshal(encoded, &extensions) != nil || extensions.Validate() != nil {
		return false
	}
	_, ok = extensions[protocol.TasksExtensionID]
	return ok
}

func (s *Server) tasksEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.extensions[protocol.TasksExtensionID]
	return ok
}

func taskOwner(ctx context.Context, params protocol.RequestMeta) string {
	if principal, ok := ExecutionPrincipalFromContext(ctx); ok {
		return principal
	}
	if params.ClientInfo == nil {
		return ""
	}
	return params.ClientInfo.Name
}

func (s *Server) taskCapabilityError(params protocol.RequestMeta) *protocol.Error {
	if !s.tasksEnabled() || !tasksCapability(params.ClientCapabilities) {
		return protocol.MissingTaskCapabilityError()
	}
	return nil
}

func (s *Server) taskRepository() TaskRepository {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tasks
}

// SetTaskRepository replaces the process-local task store. Callers should set
// a durable implementation before serving requests in a multi-instance
// deployment; this method is primarily useful for tests and demonstrations.
func (s *Server) SetTaskRepository(repository TaskRepository) error {
	if repository == nil {
		return fmt.Errorf("task repository is required")
	}
	s.mu.Lock()
	s.tasks = repository
	s.mu.Unlock()
	return nil
}

func (s *Server) taskNow() time.Time {
	if repository, ok := s.taskRepository().(interface{ Now() time.Time }); ok {
		return repository.Now().UTC()
	}
	return RealClock{}.Now()
}

func taskResult(task protocol.DetailedTask) json.RawMessage {
	result := protocol.GetTaskResult{Result: newResult(), DetailedTask: cloneTask(task)}
	return mustMarshal(result)
}

func createTaskResult(task protocol.DetailedTask) json.RawMessage {
	result := protocol.CreateTaskResult{Result: newResult(), DetailedTask: cloneTask(task)}
	result.ResultType = "task"
	return mustMarshal(result)
}

func emptyTaskResult() json.RawMessage {
	return mustMarshal(protocol.TaskUpdateResult{Result: newResult()})
}

func taskProtocolError(err error) *protocol.Error {
	switch {
	case errors.Is(err, ErrTaskNotFound), errors.Is(err, ErrTaskExpired), errors.Is(err, ErrTaskForeign), errors.Is(err, ErrTaskTerminal):
		return protocol.NewError(protocol.CodeInvalidParams, "invalid task")
	default:
		return protocol.NewError(protocol.CodeInvalidParams, err.Error())
	}
}

func (s *Server) getTask(ctx context.Context, raw json.RawMessage) (json.RawMessage, *protocol.Error) {
	var params protocol.GetTaskParams
	if err := json.Unmarshal(raw, &params); err != nil || params.TaskID == "" {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "taskId is required")
	}
	if err := s.taskCapabilityError(params.Meta); err != nil {
		return nil, err
	}
	record, err := s.taskRepository().Get(ctx, params.TaskID, taskOwner(ctx, params.Meta))
	if err != nil {
		return nil, taskProtocolError(err)
	}
	return taskResult(record.Task), nil
}

func (s *Server) updateTask(ctx context.Context, raw json.RawMessage) (json.RawMessage, *protocol.Error) {
	var params protocol.UpdateTaskParams
	if err := json.Unmarshal(raw, &params); err != nil || params.TaskID == "" {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "taskId is required")
	}
	if err := s.taskCapabilityError(params.Meta); err != nil {
		return nil, err
	}
	record, err := s.taskRepository().Update(ctx, params.TaskID, taskOwner(ctx, params.Meta), params.InputResponses)
	if err != nil {
		return nil, taskProtocolError(err)
	}
	s.publishTaskChanged(record.Owner, record.Task)
	return emptyTaskResult(), nil
}

func (s *Server) cancelTask(ctx context.Context, raw json.RawMessage) (json.RawMessage, *protocol.Error) {
	var params protocol.CancelTaskParams
	if err := json.Unmarshal(raw, &params); err != nil || params.TaskID == "" {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "taskId is required")
	}
	if err := s.taskCapabilityError(params.Meta); err != nil {
		return nil, err
	}
	record, err := s.taskRepository().Cancel(ctx, params.TaskID, taskOwner(ctx, params.Meta))
	if err != nil {
		return nil, taskProtocolError(err)
	}
	s.publishTaskChanged(record.Owner, record.Task)
	return emptyTaskResult(), nil
}

func (s *Server) callDeferredEchoTask(ctx context.Context, raw json.RawMessage) (json.RawMessage, *protocol.Error, bool) {
	var params struct {
		protocol.RequestParams
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.Name != deferredEchoToolName {
		return nil, nil, false
	}
	if _, registered := s.tool(deferredEchoToolName); !registered {
		return nil, nil, false
	}
	if err := s.taskCapabilityError(params.Meta); err != nil {
		return nil, err, true
	}
	owner := taskOwner(ctx, params.Meta)
	if owner == "" {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "clientInfo.name is required for tasks"), true
	}
	var arguments struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(params.Arguments, &arguments); err != nil || arguments.Text == "" {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "deferred_echo requires a non-empty text"), true
	}
	taskID, err := newTaskID()
	if err != nil {
		return nil, protocol.NewError(protocol.CodeInternalError, err.Error()), true
	}
	now := s.taskNow().Format(time.RFC3339Nano)
	ttl := tasksDefaultTTL
	task := protocol.DetailedTask{
		TaskID:         taskID,
		Status:         protocol.TaskStatusInputRequired,
		StatusMessage:  "approval is required",
		CreatedAt:      now,
		LastUpdatedAt:  now,
		TTLMS:          &ttl,
		PollIntervalMS: tasksDefaultPoll,
		InputRequests: map[string]protocol.TaskInputRequest{
			deferredEchoInputKey: {
				Method: "elicitation/create",
				Params: protocol.ElicitationRequest{
					Mode:    "form",
					Message: "Approve deferred echo: " + arguments.Text,
					RequestedSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"confirm": map[string]any{"type": "boolean"},
						},
						"required": []string{"confirm"},
					},
				},
			},
		},
	}
	if err := s.taskRepository().Create(ctx, TaskRecord{Task: task, Owner: owner, Metadata: map[string]string{"deferred_echo_text": arguments.Text}}); err != nil {
		return nil, protocol.NewError(protocol.CodeInternalError, err.Error()), true
	}
	s.publishTaskChanged(owner, task)
	return createTaskResult(task), nil, true
}

// RegisterDeferredEchoTool makes the bounded task workflow discoverable. The
// task implementation lives in the server lifecycle, so the tool handler is
// intentionally inert and is only used for tools/list metadata.
func (s *Server) RegisterDeferredEchoTool() error {
	return s.RegisterTool(deferredEchoTool{})
}

type deferredEchoTool struct{}

func (deferredEchoTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        deferredEchoToolName,
		Description: "Create a durable task that waits for approval before echoing.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string"},
			},
			"required": []string{"text"},
		},
	}
}

func (deferredEchoTool) Call(ToolInvocation) (ToolResult, error) {
	return ToolResult{}, fmt.Errorf("deferred_echo must be invoked through the Tasks extension")
}
