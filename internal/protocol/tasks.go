package protocol

import (
	"encoding/json"
	"fmt"
)

// TasksExtensionID is the negotiated extension identifier for the Tasks
// lifecycle methods.
const TasksExtensionID = "io.modelcontextprotocol/tasks"

// Task statuses are deliberately a closed set. A task may move from working
// to input_required or a terminal status, and input_required may move to
// completed, failed, or cancelled.
type TaskStatus string

const (
	TaskStatusWorking       TaskStatus = "working"
	TaskStatusInputRequired TaskStatus = "input_required"
	TaskStatusCompleted     TaskStatus = "completed"
	TaskStatusFailed        TaskStatus = "failed"
	TaskStatusCancelled     TaskStatus = "cancelled"
)

func (status TaskStatus) Valid() bool {
	switch status {
	case TaskStatusWorking, TaskStatusInputRequired, TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled:
		return true
	default:
		return false
	}
}

func (status TaskStatus) Terminal() bool {
	switch status {
	case TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled:
		return true
	default:
		return false
	}
}

// TaskInputRequest describes a request the task is waiting for. Its shape is
// intentionally open because task workflows may use elicitation or another
// extension-specific request method.
type TaskInputRequest struct {
	Method string             `json:"method"`
	Params ElicitationRequest `json:"params,omitempty"`
}

// ElicitationRequest is the bounded input request used by the demo workflow.
// Keeping the shape explicit makes malformed task updates rejectable without
// accepting arbitrary server-generated protocol messages.
type ElicitationRequest struct {
	Mode            string         `json:"mode"`
	Message         string         `json:"message"`
	RequestedSchema map[string]any `json:"requestedSchema"`
}

// DetailedTask is the complete task state returned by tasks/get and embedded
// in notifications/tasks. The required fields are always present on the wire.
type DetailedTask struct {
	TaskID         string                      `json:"taskId"`
	Status         TaskStatus                  `json:"status"`
	StatusMessage  string                      `json:"statusMessage,omitempty"`
	CreatedAt      string                      `json:"createdAt"`
	LastUpdatedAt  string                      `json:"lastUpdatedAt"`
	TTLMS          *int64                      `json:"ttlMs"`
	PollIntervalMS int64                       `json:"pollIntervalMs,omitempty"`
	InputRequests  map[string]TaskInputRequest `json:"inputRequests,omitempty"`
	InputResponses map[string]json.RawMessage  `json:"-"`
	Result         json.RawMessage             `json:"result,omitempty"`
	Error          *Error                      `json:"error,omitempty"`
}

// Task is kept as a concise alias for callers that use the protocol's short
// name. DetailedTask remains the canonical response shape for this extension.
type Task = DetailedTask

// CreateTaskResult is intentionally flat: resultType identifies the task
// result and the task fields are siblings rather than nested under task.
type CreateTaskResult struct {
	Result
	DetailedTask
}

// GetTaskResult is the complete result envelope returned by tasks/get.
type GetTaskResult struct {
	Result
	DetailedTask
}

// TaskUpdateResult and TaskCancelResult acknowledge a state mutation.
type TaskUpdateResult struct {
	Result
}

type TaskCancelResult struct {
	Result
}

type GetTaskParams struct {
	RequestParams
	TaskID string `json:"taskId"`
}

type UpdateTaskParams struct {
	RequestParams
	TaskID         string                     `json:"taskId"`
	InputResponses map[string]json.RawMessage `json:"inputResponses,omitempty"`
}

type CancelTaskParams struct {
	RequestParams
	TaskID string `json:"taskId"`
}

// TaskSubscriptionFilter is the explicit task-id filter accepted by
// subscriptions/listen. An empty list means no task notifications.
type TaskSubscriptionFilter struct {
	TaskIDs []string `json:"taskIds,omitempty"`
}

// TaskNotificationParams contains a complete task snapshot and the active
// subscription identifier. Embedding keeps the wire shape flat.
type TaskNotificationParams struct {
	DetailedTask
	Meta struct {
		SubscriptionID RequestID `json:"io.modelcontextprotocol/subscriptionId"`
	} `json:"_meta"`
}

// CodeMissingRequiredTaskCapability is returned when a task method is used by
// a peer that did not negotiate io.modelcontextprotocol/tasks.
const CodeMissingRequiredTaskCapability ErrorCode = -32003

// CodeMissingRequiredCapability is an alias used by callers that do not need
// to distinguish the Tasks-specific capability from its protocol meaning.
const CodeMissingRequiredCapability = CodeMissingRequiredTaskCapability

func MissingTaskCapabilityError() *Error {
	return NewErrorWithData(
		CodeMissingRequiredTaskCapability,
		"missing required client capability",
		map[string]any{
			"requiredCapabilities": map[string]any{
				"extensions": map[string]any{
					TasksExtensionID: map[string]any{},
				},
			},
		},
	)
}

func (task DetailedTask) Validate() error {
	if task.TaskID == "" {
		return fmt.Errorf("taskId is required")
	}
	if !task.Status.Valid() {
		return fmt.Errorf("invalid task status %q", task.Status)
	}
	if task.CreatedAt == "" || task.LastUpdatedAt == "" {
		return fmt.Errorf("createdAt and lastUpdatedAt are required")
	}
	if task.TTLMS != nil && *task.TTLMS < 0 {
		return fmt.Errorf("ttlMs must not be negative")
	}
	return nil
}

// TaskExtensionSettings is the canonical empty settings object used during
// capability negotiation. Servers may advertise additional opaque settings,
// but this extension does not require any for the bounded demo workflow.
func TaskExtensionSettings() json.RawMessage { return json.RawMessage(`{}`) }
