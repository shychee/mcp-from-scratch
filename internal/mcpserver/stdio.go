package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

const maxConcurrentStdioRequests = 32

const maxActiveStdioRequestIDs = 64

const codeServerOverloaded protocol.ErrorCode = -32024

// Serve owns stdio framing, JSON parsing, and JSON-RPC envelope validation.
// Valid requests are passed to Handle for MCP method dispatch.
func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	return s.serveStdio(ctx, input, output, stdioCompatibilityState{})
}

func (s *Server) serveStdio(ctx context.Context, input io.Reader, output io.Writer, compatibility stdioCompatibilityState) (serveErr error) {
	scanner := bufio.NewScanner(input)
	encoder := json.NewEncoder(output)
	var encoderMu sync.Mutex
	encode := func(value any) error {
		encoderMu.Lock()
		defer encoderMu.Unlock()
		return encoder.Encode(value)
	}

	activeSubscriptions := make(map[protocol.RequestID]*subscription)
	activeIDs := make(map[protocol.RequestID]struct{})
	var activeMu sync.Mutex
	type activeCall struct {
		mu        sync.Mutex
		cancel    context.CancelFunc
		cancelled bool
		finished  bool
	}
	activeCalls := make(map[protocol.RequestID]*activeCall)
	activeNotifications := make(map[*activeCall]struct{})
	executionSlots := make(chan struct{}, maxConcurrentStdioRequests)
	registerID := func(id protocol.RequestID) (registered, overloaded bool) {
		activeMu.Lock()
		defer activeMu.Unlock()
		if _, exists := activeIDs[id]; exists {
			return false, false
		}
		if len(activeIDs) >= maxActiveStdioRequestIDs {
			return false, true
		}
		activeIDs[id] = struct{}{}
		return true, false
	}
	releaseID := func(id protocol.RequestID) {
		activeMu.Lock()
		delete(activeIDs, id)
		activeMu.Unlock()
	}
	removeCall := func(id protocol.RequestID, expected *activeCall) {
		activeMu.Lock()
		defer activeMu.Unlock()
		if current := activeCalls[id]; expected == nil || current == expected {
			delete(activeCalls, id)
			delete(activeIDs, id)
		}
	}
	removeNotification := func(expected *activeCall) {
		activeMu.Lock()
		delete(activeNotifications, expected)
		activeMu.Unlock()
	}
	cancelCall := func(id protocol.RequestID) {
		activeMu.Lock()
		call := activeCalls[id]
		activeMu.Unlock()
		if call != nil {
			call.mu.Lock()
			if !call.finished && !call.cancelled {
				call.cancelled = true
				call.cancel()
			}
			call.mu.Unlock()
		}
	}
	removeSubscription := func(id protocol.RequestID, expected *subscription) *subscription {
		activeMu.Lock()
		defer activeMu.Unlock()
		subscriber := activeSubscriptions[id]
		if expected != nil && subscriber != expected {
			return nil
		}
		delete(activeSubscriptions, id)
		delete(activeIDs, id)
		return subscriber
	}
	var subscriptionWriters sync.WaitGroup
	requestErrors := make(chan error, 1)
	defer func() {
		activeMu.Lock()
		calls := make([]*activeCall, 0, len(activeCalls))
		for _, call := range activeCalls {
			calls = append(calls, call)
		}
		for call := range activeNotifications {
			calls = append(calls, call)
		}
		activeCalls = make(map[protocol.RequestID]*activeCall)
		activeNotifications = make(map[*activeCall]struct{})
		activeMu.Unlock()

		// A finite in-memory reader is commonly used to run a complete stdio
		// exchange. Give already-running work one connection-wide grace period;
		// real closable transports are treated as disconnected immediately.
		deadline := time.NewTimer(100 * time.Millisecond)
		ticker := time.NewTicker(time.Millisecond)
		deadlineExpired := false
		waitForActiveCalls := func() {
			if deadlineExpired {
				return
			}
		waitForCalls:
			for len(executionSlots) > 0 {
				select {
				case <-deadline.C:
					deadlineExpired = true
					break waitForCalls
				case <-ticker.C:
				}
			}
		}
		if _, closable := input.(io.Closer); !closable && len(calls) > 0 {
			waitForActiveCalls()
		}
		for _, call := range calls {
			call.mu.Lock()
			if !call.finished && !call.cancelled {
				call.cancelled = true
				call.cancel()
			}
			call.mu.Unlock()
		}
		waitForActiveCalls()
		ticker.Stop()
		if !deadline.Stop() {
			select {
			case <-deadline.C:
			default:
			}
		}
		activeMu.Lock()
		subscribers := make([]*subscription, 0, len(activeSubscriptions))
		for _, subscriber := range activeSubscriptions {
			subscribers = append(subscribers, subscriber)
		}
		activeSubscriptions = make(map[protocol.RequestID]*subscription)
		activeMu.Unlock()
		for _, subscriber := range subscribers {
			s.unsubscribe(subscriber)
		}
		subscriptionWriters.Wait()
		if serveErr == nil {
			select {
			case serveErr = <-requestErrors:
			default:
			}
		}
	}()
	reportAsyncError := func(err error) {
		select {
		case requestErrors <- err:
		default:
		}
		if closer, ok := input.(io.Closer); ok {
			_ = closer.Close()
		}
	}

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("server context canceled: %w", err)
		}

		var request protocol.Request
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			response := protocol.Response{
				JSONRPC: "2.0",
				Error:   protocol.NewError(protocol.CodeParseError, "parse error"),
			}
			if encodeErr := encode(response); encodeErr != nil {
				return fmt.Errorf("encode parse error response: %w", encodeErr)
			}
			continue
		}
		if requestError := protocol.ValidateRequest(request); requestError != nil {
			response := protocol.Response{
				JSONRPC: "2.0",
				ID:      request.ID,
				Error:   requestError,
			}
			if encodeErr := encode(response); encodeErr != nil {
				return fmt.Errorf("encode invalid request error response: %w", encodeErr)
			}
			continue
		}
		if response, handled := s.handleStdioCompatibility(ctx, request, &compatibility); handled {
			// Compatibility lifecycle notifications intentionally have no response.
			if request.ID == nil {
				continue
			}
			if err := encode(response); err != nil {
				return fmt.Errorf("encode compatibility response: %w", err)
			}
			continue
		}
		if request.ID == nil && request.Method == "notifications/cancelled" {
			var params struct {
				RequestID *protocol.RequestID `json:"requestId"`
			}
			if err := json.Unmarshal(request.Params, &params); err == nil && params.RequestID != nil {
				cancelCall(*params.RequestID)
				if subscriber := removeSubscription(*params.RequestID, nil); subscriber != nil {
					s.unsubscribe(subscriber)
				}
			}
			continue
		}
		if request.Method == "subscriptions/listen" {
			if request.ID == nil {
				continue
			}
			registered, overloaded := registerID(*request.ID)
			if !registered {
				message := "subscription request ID is already active"
				code := protocol.CodeInvalidParams
				if overloaded {
					message, code = "server overloaded", codeServerOverloaded
				}
				response := protocol.Response{
					JSONRPC: "2.0",
					ID:      request.ID,
					Error:   protocol.NewError(code, message),
				}
				if err := encode(response); err != nil {
					return fmt.Errorf("encode duplicate subscription error response: %w", err)
				}
				continue
			}
			subscriber, acknowledged, err := s.subscribe(ctx, request.ID, request.Params)
			if err != nil {
				releaseID(*request.ID)
				response := protocol.Response{
					JSONRPC: "2.0",
					ID:      request.ID,
					Error:   err,
				}
				if encodeErr := encode(response); encodeErr != nil {
					return fmt.Errorf("encode subscription error response: %w", encodeErr)
				}
				continue
			}
			activeMu.Lock()
			activeSubscriptions[*request.ID] = subscriber
			activeMu.Unlock()
			if err := encode(acknowledged); err != nil {
				s.unsubscribe(subscriber)
				removeSubscription(*request.ID, subscriber)
				return fmt.Errorf("encode subscription acknowledgement: %w", err)
			}
			subscriptionWriters.Add(1)
			go func(subscriptionID protocol.RequestID, subscriber *subscription) {
				defer subscriptionWriters.Done()
				defer func() {
					removeSubscription(subscriptionID, subscriber)
					s.unsubscribe(subscriber)
				}()
				for {
					select {
					case <-ctx.Done():
						return
					case <-subscriber.done:
						return
					case <-subscriber.complete:
						if err := encode(subscriptionCompleteResponse(subscriptionID)); err != nil {
							reportAsyncError(fmt.Errorf("encode subscription completion: %w", err))
						}
						return
					case message := <-subscriber.events:
						if err := encode(message); err != nil {
							reportAsyncError(fmt.Errorf("encode subscription message: %w", err))
							return
						}
					}
				}
			}(*request.ID, subscriber)
			continue
		}
		// Reject malformed modern metadata before queueing concurrent work. This
		// keeps deterministic protocol errors ahead of subsequent responses while
		// valid tool calls remain fully concurrent.
		if request.ID != nil && validateRequestMetadata(request.Params) != nil {
			requestContext, cancel := context.WithCancel(ctx)
			runtimeContext := withRequestRuntime(requestContext, request.Params, func(notification protocol.Notification) bool {
				return encode(notification) == nil
			})
			response := s.Handle(runtimeContext, request)
			finishRequestRuntime(runtimeContext)
			cancel()
			if err := encode(response); err != nil {
				return fmt.Errorf("encode response: %w", err)
			}
			continue
		}
		// JSON-RPC notifications have no ID and must not receive responses.
		select {
		case executionSlots <- struct{}{}:
		default:
			if request.ID != nil {
				response := protocol.Response{JSONRPC: "2.0", ID: request.ID, Error: protocol.NewError(codeServerOverloaded, "server overloaded")}
				if err := encode(response); err != nil {
					return fmt.Errorf("encode overload response: %w", err)
				}
			}
			continue
		}
		requestContext, cancel := context.WithCancel(ctx)
		runtimeContext := withRequestRuntime(requestContext, request.Params, func(notification protocol.Notification) bool {
			if err := encode(notification); err != nil {
				cancel()
				return false
			}
			return true
		})
		call := &activeCall{cancel: cancel}
		if request.ID != nil {
			registered, overloaded := registerID(*request.ID)
			if !registered {
				cancel()
				<-executionSlots
				message := "duplicate request ID"
				code := protocol.CodeInvalidRequest
				if overloaded {
					message, code = "server overloaded", codeServerOverloaded
				}
				response := protocol.Response{JSONRPC: "2.0", ID: request.ID, Error: protocol.NewError(code, message)}
				if err := encode(response); err != nil {
					return fmt.Errorf("encode duplicate request error: %w", err)
				}
				continue
			}
			activeMu.Lock()
			activeCalls[*request.ID] = call
			activeMu.Unlock()
		} else {
			activeMu.Lock()
			activeNotifications[call] = struct{}{}
			activeMu.Unlock()
		}
		go func(request protocol.Request, call *activeCall, requestContext context.Context, cancel context.CancelFunc) {
			defer func() { <-executionSlots }()
			defer cancel()
			if request.ID != nil {
				defer removeCall(*request.ID, call)
			} else {
				defer removeNotification(call)
			}
			response := s.Handle(requestContext, request)
			finishRequestRuntime(requestContext)
			call.mu.Lock()
			if request.ID == nil || call.cancelled {
				call.finished = true
				call.mu.Unlock()
				return
			}
			if err := encode(response); err != nil {
				call.finished = true
				call.mu.Unlock()
				reportAsyncError(fmt.Errorf("encode response: %w", err))
				return
			}
			call.finished = true
			call.mu.Unlock()
		}(request, call, runtimeContext, cancel)
	}

	select {
	case err := <-requestErrors:
		return err
	default:
	}
	if err := scanner.Err(); err != nil {
		activeMu.Lock()
		for _, call := range activeCalls {
			call.cancel()
		}
		activeMu.Unlock()
		return fmt.Errorf("read request: %w", err)
	}
	return nil
}
