package host

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

// TaskTranscript captures the durable task workflow shown by the host demo.
type TaskTranscript struct {
	Discovery    protocol.Response         `json:"discovery"`
	Created      protocol.CreateTaskResult `json:"created"`
	Waiting      protocol.GetTaskResult    `json:"waiting"`
	Acknowledged protocol.Notification     `json:"acknowledged"`
	Updated      protocol.TaskUpdateResult `json:"updated"`
	Notification protocol.Notification     `json:"notification"`
	Completed    protocol.GetTaskResult    `json:"completed"`
}

const taskApprovalInputKey = "approval"

type HTTPTaskSubscription struct {
	id           int
	reader       *bufio.Reader
	responseBody io.ReadCloser
	acknowledged protocol.Notification
	taskIDs      map[string]struct{}
}

// OpenHTTPTaskSubscription opens a task-filtered subscriptions/listen stream.
// The server acknowledges only task IDs that are both known and owned by the
// caller, so callers should inspect Acknowledged before waiting for events.
func OpenHTTPTaskSubscription(ctx context.Context, endpoint string, id int, taskIDs []string) (*HTTPTaskSubscription, error) {
	params, err := json.Marshal(struct {
		protocol.RequestParams
		Notifications protocol.TaskSubscriptionFilter `json:"notifications"`
	}{
		RequestParams: protocol.RequestParams{Meta: clientRequestMeta()},
		Notifications: protocol.TaskSubscriptionFilter{TaskIDs: append([]string(nil), taskIDs...)},
	})
	if err != nil {
		return nil, fmt.Errorf("encode task subscription params: %w", err)
	}
	rpcRequest := protocol.Request{JSONRPC: "2.0", ID: protocol.ID(id), Method: "subscriptions/listen", Params: params}
	client := &httpRPCClient{ctx: ctx, endpoint: endpoint, client: http.DefaultClient}
	request, err := client.newRequest(rpcRequest)
	if err != nil {
		return nil, fmt.Errorf("create task subscription request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("open task subscription: %w", err)
	}
	mediaType, _, mediaTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaTypeErr != nil || mediaType != protocol.MediaTypeSSE {
		response.Body.Close()
		return nil, fmt.Errorf("open task subscription: unexpected HTTP response %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	subscription := &HTTPTaskSubscription{id: id, reader: bufio.NewReader(response.Body), responseBody: response.Body}
	rawAcknowledged, err := readSSEData(subscription.reader)
	if err != nil {
		_ = response.Body.Close()
		return nil, fmt.Errorf("read task subscription acknowledgement: %w", err)
	}
	if err := json.Unmarshal(rawAcknowledged, &subscription.acknowledged); err != nil {
		_ = response.Body.Close()
		return nil, fmt.Errorf("decode task subscription acknowledgement: %w", err)
	}
	if err := validateTaskSubscriptionAcknowledgement(subscription.acknowledged, id, taskIDs); err != nil {
		_ = response.Body.Close()
		return nil, err
	}
	subscription.taskIDs = acknowledgedTaskIDs(subscription.acknowledged)
	return subscription, nil
}

func (s *HTTPTaskSubscription) Acknowledged() protocol.Notification {
	return s.acknowledged
}

// Next waits for the next notifications/tasks message and decodes its full
// DetailedTask payload.
func (s *HTTPTaskSubscription) Next() (protocol.Notification, protocol.DetailedTask, error) {
	raw, err := readSSEData(s.reader)
	if err != nil {
		return protocol.Notification{}, protocol.DetailedTask{}, fmt.Errorf("read task notification: %w", err)
	}
	var notification protocol.Notification
	if err := json.Unmarshal(raw, &notification); err != nil {
		return protocol.Notification{}, protocol.DetailedTask{}, fmt.Errorf("decode task notification: %w", err)
	}
	if notification.JSONRPC != "2.0" || notification.Method != "notifications/tasks" {
		return protocol.Notification{}, protocol.DetailedTask{}, fmt.Errorf("unexpected task notification %q", notification.Method)
	}
	var params protocol.TaskNotificationParams
	if err := json.Unmarshal(notification.Params, &params); err != nil {
		return protocol.Notification{}, protocol.DetailedTask{}, fmt.Errorf("decode task notification task: %w", err)
	}
	wantedID := protocol.ID(s.id)
	if params.Meta.SubscriptionID != *wantedID {
		return protocol.Notification{}, protocol.DetailedTask{}, fmt.Errorf("task notification subscription ID = %s, want %d", params.Meta.SubscriptionID.String(), s.id)
	}
	if _, ok := s.taskIDs[params.TaskID]; !ok {
		return protocol.Notification{}, protocol.DetailedTask{}, fmt.Errorf("task notification referenced unacknowledged task %q", params.TaskID)
	}
	return notification, params.DetailedTask, nil
}

func (s *HTTPTaskSubscription) Close() error {
	if s == nil || s.responseBody == nil {
		return nil
	}
	return s.responseBody.Close()
}

func GetHTTPTask(ctx context.Context, endpoint string, requestID int, taskID string) (protocol.GetTaskResult, error) {
	client := &httpRPCClient{ctx: ctx, endpoint: endpoint, client: http.DefaultClient}
	response, err := client.call(taskRequest(requestID, "tasks/get", map[string]any{"taskId": taskID}))
	if err != nil {
		return protocol.GetTaskResult{}, err
	}
	var result protocol.GetTaskResult
	if err := decodeCompleteResult(response.Result, &result); err != nil {
		return protocol.GetTaskResult{}, fmt.Errorf("decode tasks/get result: %w", err)
	}
	return result, nil
}

func UpdateHTTPTask(ctx context.Context, endpoint string, requestID int, taskID string, inputResponses map[string]json.RawMessage) (protocol.TaskUpdateResult, error) {
	client := &httpRPCClient{ctx: ctx, endpoint: endpoint, client: http.DefaultClient}
	response, err := client.call(taskRequest(requestID, "tasks/update", map[string]any{"taskId": taskID, "inputResponses": inputResponses}))
	if err != nil {
		return protocol.TaskUpdateResult{}, err
	}
	var result protocol.TaskUpdateResult
	if err := decodeCompleteResult(response.Result, &result); err != nil {
		return protocol.TaskUpdateResult{}, fmt.Errorf("decode tasks/update result: %w", err)
	}
	return result, nil
}

func CancelHTTPTask(ctx context.Context, endpoint string, requestID int, taskID string) (protocol.TaskCancelResult, error) {
	client := &httpRPCClient{ctx: ctx, endpoint: endpoint, client: http.DefaultClient}
	response, err := client.call(taskRequest(requestID, "tasks/cancel", map[string]any{"taskId": taskID}))
	if err != nil {
		return protocol.TaskCancelResult{}, err
	}
	var result protocol.TaskCancelResult
	if err := decodeCompleteResult(response.Result, &result); err != nil {
		return protocol.TaskCancelResult{}, fmt.Errorf("decode tasks/cancel result: %w", err)
	}
	return result, nil
}

func taskRequest(id int, method string, fields map[string]any) protocol.Request {
	fields["_meta"] = clientRequestMeta()
	params, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return protocol.Request{JSONRPC: "2.0", ID: protocol.ID(id), Method: method, Params: params}
}

func validateTaskSubscriptionAcknowledgement(notification protocol.Notification, id int, requested []string) error {
	if notification.JSONRPC != "2.0" || notification.Method != "notifications/subscriptions/acknowledged" {
		return fmt.Errorf("unexpected task subscription acknowledgement %q", notification.Method)
	}
	var params struct {
		Meta struct {
			SubscriptionID protocol.RequestID `json:"io.modelcontextprotocol/subscriptionId"`
		} `json:"_meta"`
		Notifications protocol.TaskSubscriptionFilter `json:"notifications"`
	}
	if err := json.Unmarshal(notification.Params, &params); err != nil {
		return fmt.Errorf("decode task subscription acknowledgement: %w", err)
	}
	if params.Meta.SubscriptionID.String() != protocol.ID(id).String() {
		return fmt.Errorf("task subscription ID = %s, want %d", params.Meta.SubscriptionID.String(), id)
	}
	requestedSet := make(map[string]struct{}, len(requested))
	for _, taskID := range requested {
		requestedSet[taskID] = struct{}{}
	}
	for _, taskID := range params.Notifications.TaskIDs {
		if _, ok := requestedSet[taskID]; !ok {
			return fmt.Errorf("task subscription acknowledged unrequested task %q", taskID)
		}
	}
	return nil
}

func acknowledgedTaskIDs(notification protocol.Notification) map[string]struct{} {
	var params struct {
		Notifications protocol.TaskSubscriptionFilter `json:"notifications"`
	}
	_ = json.Unmarshal(notification.Params, &params)
	taskIDs := make(map[string]struct{}, len(params.Notifications.TaskIDs))
	for _, taskID := range params.Notifications.TaskIDs {
		taskIDs[taskID] = struct{}{}
	}
	return taskIDs
}

// RunHTTPTaskDemo demonstrates the full task lifecycle over Streamable HTTP.
func RunHTTPTaskDemo(ctx context.Context, endpoint string) (TaskTranscript, error) {
	client := &httpRPCClient{ctx: ctx, endpoint: endpoint, client: http.DefaultClient}
	discovery, err := client.call(protocol.Request{JSONRPC: "2.0", ID: protocol.ID(1), Method: "server/discover", Params: mustMarshalHost(protocol.RequestParams{Meta: clientRequestMeta()})})
	if err != nil {
		return TaskTranscript{}, fmt.Errorf("server/discover: %w", err)
	}
	negotiated, err := negotiatedExtensionsFromDiscovery(clientExtensions(), discovery.Result)
	if err != nil {
		return TaskTranscript{}, fmt.Errorf("negotiate task extension: %w", err)
	}
	if _, ok := negotiated[protocol.TasksExtensionID]; !ok {
		return TaskTranscript{}, fmt.Errorf("server did not negotiate %s", protocol.TasksExtensionID)
	}
	createParams := map[string]any{
		"name":      "deferred_echo",
		"arguments": map[string]string{"text": "hello from task demo"},
		"_meta":     clientRequestMeta(),
	}
	createRequest := protocol.Request{JSONRPC: "2.0", ID: protocol.ID(2), Method: "tools/call", Params: mustMarshalHost(createParams)}
	createdResponse, err := client.call(createRequest)
	if err != nil {
		return TaskTranscript{}, fmt.Errorf("create task: %w", err)
	}
	var created protocol.CreateTaskResult
	if err := json.Unmarshal(createdResponse.Result, &created); err != nil || created.ResultType != "task" {
		return TaskTranscript{}, fmt.Errorf("decode task creation result: %w", err)
	}
	subscription, err := OpenHTTPTaskSubscription(ctx, endpoint, 30, []string{created.TaskID})
	if err != nil {
		return TaskTranscript{}, err
	}
	defer subscription.Close()
	waiting, err := GetHTTPTask(ctx, endpoint, 31, created.TaskID)
	if err != nil {
		return TaskTranscript{}, err
	}
	approval, err := json.Marshal(map[string]any{"action": "accept", "content": map[string]bool{"confirm": true}})
	if err != nil {
		return TaskTranscript{}, err
	}
	updated, err := UpdateHTTPTask(ctx, endpoint, 32, created.TaskID, map[string]json.RawMessage{taskApprovalInputKey: approval})
	if err != nil {
		return TaskTranscript{}, err
	}
	notification, _, err := subscription.Next()
	if err != nil {
		return TaskTranscript{}, err
	}
	completed, err := GetHTTPTask(ctx, endpoint, 33, created.TaskID)
	if err != nil {
		return TaskTranscript{}, err
	}
	return TaskTranscript{Discovery: discovery, Created: created, Waiting: waiting, Acknowledged: subscription.Acknowledged(), Updated: updated, Notification: notification, Completed: completed}, nil
}

// WaitForHTTPTask polls until the task reaches a terminal status. It is a
// bounded helper for hosts that do not use subscriptions.
func WaitForHTTPTask(ctx context.Context, endpoint string, requestID int, taskID string) (protocol.GetTaskResult, error) {
	for {
		task, err := GetHTTPTask(ctx, endpoint, requestID, taskID)
		if err != nil {
			return protocol.GetTaskResult{}, err
		}
		if task.Status.Terminal() {
			return task, nil
		}
		interval := time.Duration(task.PollIntervalMS) * time.Millisecond
		if interval <= 0 {
			interval = 25 * time.Millisecond
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return protocol.GetTaskResult{}, ctx.Err()
		case <-timer.C:
		}
		requestID++
	}
}
