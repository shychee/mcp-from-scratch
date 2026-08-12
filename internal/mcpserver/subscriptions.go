package mcpserver

import (
	"encoding/json"
	"sync"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

type subscriptionFilter struct {
	ToolsListChanged     bool `json:"toolsListChanged,omitempty"`
	ResourcesListChanged bool `json:"resourcesListChanged,omitempty"`
	PromptsListChanged   bool `json:"promptsListChanged,omitempty"`
}

type listChangeKind int

const (
	toolListChange listChangeKind = iota + 1
	resourceListChange
	promptListChange
)

type subscriptionListenParams struct {
	protocol.RequestParams
	Notifications *subscriptionFilter `json:"notifications"`
}

type subscriptionMeta struct {
	SubscriptionID protocol.RequestID `json:"io.modelcontextprotocol/subscriptionId"`
}

type subscription struct {
	id            protocol.RequestID
	notifications subscriptionFilter
	events        chan protocol.Notification
	complete      chan struct{}
	done          chan struct{}
	stopOnce      sync.Once
	completeOnce  sync.Once
}

// RegisterEchoTool adds a named echo tool and publishes a list-change event.
func (s *Server) RegisterEchoTool(name string) error {
	return s.registerTool(newEchoTool(name))
}

func (s *Server) registerTool(registeredTool Tool) error {
	return s.RegisterTool(registeredTool)
}

func (s *Server) subscribe(id *protocol.RequestID, rawParams json.RawMessage) (*subscription, protocol.Notification, *protocol.Error) {
	if id == nil {
		return nil, protocol.Notification{}, protocol.NewError(protocol.CodeInvalidRequest, "subscriptions/listen requires a request ID")
	}
	if metadataError := validateRequestMetadata(rawParams); metadataError != nil {
		return nil, protocol.Notification{}, metadataError
	}

	var params subscriptionListenParams
	if err := json.Unmarshal(rawParams, &params); err != nil || params.Notifications == nil {
		return nil, protocol.Notification{}, protocol.NewError(protocol.CodeInvalidParams, "missing or invalid notifications filter")
	}
	agreed := subscriptionFilter{
		ToolsListChanged:     params.Notifications.ToolsListChanged,
		ResourcesListChanged: params.Notifications.ResourcesListChanged,
		PromptsListChanged:   params.Notifications.PromptsListChanged,
	}
	subscriber := &subscription{
		id:            *id,
		notifications: agreed,
		events:        make(chan protocol.Notification, 1),
		complete:      make(chan struct{}),
		done:          make(chan struct{}),
	}

	s.mu.Lock()
	s.subscriptions[subscriber] = struct{}{}
	s.mu.Unlock()
	return subscriber, subscriptionAcknowledgedNotification(*id, agreed), nil
}

func (s *Server) unsubscribe(subscriber *subscription) {
	if subscriber == nil {
		return
	}
	s.mu.Lock()
	delete(s.subscriptions, subscriber)
	s.mu.Unlock()
	subscriber.stopOnce.Do(func() {
		close(subscriber.done)
	})
}

// CloseSubscriptions gracefully completes every active listen request.
func (s *Server) CloseSubscriptions() {
	s.mu.RLock()
	subscribers := make([]*subscription, 0, len(s.subscriptions))
	for subscriber := range s.subscriptions {
		subscribers = append(subscribers, subscriber)
	}
	s.mu.RUnlock()
	for _, subscriber := range subscribers {
		subscriber.completeOnce.Do(func() {
			close(subscriber.complete)
		})
	}
}

func (s *subscription) notify(message protocol.Notification) {
	select {
	case s.events <- message:
	case <-s.done:
	default:
	}
}

func (s *Server) publishListChanged(kind listChangeKind) {
	s.mu.RLock()
	subscribers := make([]*subscription, 0, len(s.subscriptions))
	for subscriber := range s.subscriptions {
		if subscriber.notifications.accepts(kind) {
			subscribers = append(subscribers, subscriber)
		}
	}
	s.mu.RUnlock()

	for _, subscriber := range subscribers {
		subscriber.notify(listChangedNotification(subscriber.id, kind))
	}
}

func (f subscriptionFilter) accepts(kind listChangeKind) bool {
	switch kind {
	case toolListChange:
		return f.ToolsListChanged
	case resourceListChange:
		return f.ResourcesListChanged
	case promptListChange:
		return f.PromptsListChanged
	default:
		return false
	}
}

func subscriptionAcknowledgedNotification(id protocol.RequestID, notifications subscriptionFilter) protocol.Notification {
	return protocol.Notification{
		JSONRPC: "2.0",
		Method:  "notifications/subscriptions/acknowledged",
		Params: mustMarshal(struct {
			Meta          subscriptionMeta   `json:"_meta"`
			Notifications subscriptionFilter `json:"notifications"`
		}{
			Meta:          subscriptionMeta{SubscriptionID: id},
			Notifications: notifications,
		}),
	}
}

func toolsListChangedNotification(id protocol.RequestID) protocol.Notification {
	return listChangedNotification(id, toolListChange)
}

func listChangedNotification(id protocol.RequestID, kind listChangeKind) protocol.Notification {
	method := ""
	switch kind {
	case toolListChange:
		method = "notifications/tools/list_changed"
	case resourceListChange:
		method = "notifications/resources/list_changed"
	case promptListChange:
		method = "notifications/prompts/list_changed"
	}
	return protocol.Notification{
		JSONRPC: "2.0",
		Method:  method,
		Params: mustMarshal(struct {
			Meta subscriptionMeta `json:"_meta"`
		}{Meta: subscriptionMeta{SubscriptionID: id}}),
	}
}

func subscriptionCompleteResponse(id protocol.RequestID) protocol.Response {
	return protocol.Response{
		JSONRPC: "2.0",
		ID:      &id,
		Result: mustMarshal(struct {
			ResultType string           `json:"resultType"`
			Meta       subscriptionMeta `json:"_meta"`
		}{
			ResultType: protocol.ResultTypeComplete,
			Meta:       subscriptionMeta{SubscriptionID: id},
		}),
	}
}
