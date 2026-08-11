package host

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

type HTTPToolsSubscription struct {
	id           int
	reader       *bufio.Reader
	responseBody io.ReadCloser
	client       *httpRPCClient
	acknowledged protocol.Notification
	closeOnce    sync.Once
	closeErr     error
}

// OpenHTTPToolsSubscription opens an SSE listen request and validates its first ACK.
func OpenHTTPToolsSubscription(ctx context.Context, endpoint string, id int) (*HTTPToolsSubscription, error) {
	params, err := json.Marshal(struct {
		protocol.RequestParams
		Notifications struct {
			ToolsListChanged bool `json:"toolsListChanged"`
		} `json:"notifications"`
	}{
		RequestParams: protocol.RequestParams{Meta: clientRequestMeta()},
		Notifications: struct {
			ToolsListChanged bool `json:"toolsListChanged"`
		}{ToolsListChanged: true},
	})
	if err != nil {
		return nil, fmt.Errorf("encode subscription params: %w", err)
	}
	rpcRequest := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(id),
		Method:  "subscriptions/listen",
		Params:  params,
	}
	client := &httpRPCClient{
		ctx:      ctx,
		endpoint: endpoint,
		client:   http.DefaultClient,
	}
	request, err := client.newRequest(rpcRequest)
	if err != nil {
		return nil, fmt.Errorf("create subscription HTTP request: %w", err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("open subscription: %w", err)
	}
	mediaType, _, mediaTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaTypeErr != nil || mediaType != protocol.MediaTypeSSE {
		response.Body.Close()
		return nil, fmt.Errorf("open subscription: unexpected HTTP response %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}

	subscription := &HTTPToolsSubscription{
		id:           id,
		reader:       bufio.NewReader(response.Body),
		responseBody: response.Body,
		client:       client,
	}
	rawAcknowledged, err := subscription.readSSEData()
	if err != nil {
		subscription.Close()
		return nil, fmt.Errorf("read subscription acknowledgement: %w", err)
	}
	if err := json.Unmarshal(rawAcknowledged, &subscription.acknowledged); err != nil {
		subscription.Close()
		return nil, fmt.Errorf("decode subscription acknowledgement: %w", err)
	}
	if err := validateSubscriptionNotification(subscription.acknowledged, "notifications/subscriptions/acknowledged", id, true); err != nil {
		subscription.Close()
		return nil, err
	}
	return subscription, nil
}

func (s *HTTPToolsSubscription) Acknowledged() protocol.Notification {
	return s.acknowledged
}

// RefreshOnNextToolsListChanged waits for one change event and refreshes tools/list.
func (s *HTTPToolsSubscription) RefreshOnNextToolsListChanged(requestID int) (protocol.Notification, protocol.Response, error) {
	rawChanged, err := s.readSSEData()
	if err != nil {
		return protocol.Notification{}, protocol.Response{}, fmt.Errorf("read tools list change: %w", err)
	}
	var changed protocol.Notification
	if err := json.Unmarshal(rawChanged, &changed); err != nil {
		return protocol.Notification{}, protocol.Response{}, fmt.Errorf("decode tools list change: %w", err)
	}
	if err := validateSubscriptionNotification(changed, "notifications/tools/list_changed", s.id, false); err != nil {
		return protocol.Notification{}, protocol.Response{}, err
	}

	params, err := json.Marshal(protocol.RequestParams{Meta: clientRequestMeta()})
	if err != nil {
		return protocol.Notification{}, protocol.Response{}, fmt.Errorf("encode tools/list params: %w", err)
	}
	refreshed, err := s.client.call(protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(requestID),
		Method:  "tools/list",
		Params:  params,
	})
	if err != nil {
		return protocol.Notification{}, protocol.Response{}, fmt.Errorf("refresh tools/list: %w", err)
	}
	return changed, refreshed, nil
}

func (s *HTTPToolsSubscription) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.responseBody.Close()
	})
	return s.closeErr
}

func (s *HTTPToolsSubscription) readSSEData() (json.RawMessage, error) {
	var dataLines []string
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return nil, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" && len(dataLines) > 0 {
			return json.RawMessage(strings.Join(dataLines, "\n")), nil
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if err != nil {
			if len(dataLines) > 0 {
				return json.RawMessage(strings.Join(dataLines, "\n")), nil
			}
			return nil, err
		}
	}
}

func validateSubscriptionNotification(notification protocol.Notification, method string, id int, requireToolsFilter bool) error {
	if notification.JSONRPC != "2.0" || notification.Method != method {
		return fmt.Errorf("unexpected subscription notification %q", notification.Method)
	}
	var params struct {
		Meta struct {
			SubscriptionID int `json:"io.modelcontextprotocol/subscriptionId"`
		} `json:"_meta"`
		Notifications struct {
			ToolsListChanged bool `json:"toolsListChanged"`
		} `json:"notifications"`
	}
	if err := json.Unmarshal(notification.Params, &params); err != nil {
		return fmt.Errorf("decode subscription notification params: %w", err)
	}
	if params.Meta.SubscriptionID != id {
		return fmt.Errorf("subscription notification ID = %d, want %d", params.Meta.SubscriptionID, id)
	}
	if requireToolsFilter && !params.Notifications.ToolsListChanged {
		return fmt.Errorf("subscription acknowledgement omitted toolsListChanged")
	}
	return nil
}
