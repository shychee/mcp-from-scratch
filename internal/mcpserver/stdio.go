package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

// Serve owns stdio framing, JSON parsing, and JSON-RPC envelope validation.
// Valid requests are passed to Handle for MCP method dispatch.
func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	return s.serveStdio(ctx, input, output, stdioCompatibilityState{})
}

func (s *Server) serveStdio(ctx context.Context, input io.Reader, output io.Writer, compatibility stdioCompatibilityState) error {
	scanner := bufio.NewScanner(input)
	encoder := json.NewEncoder(output)
	var encoderMu sync.Mutex
	encode := func(value any) error {
		encoderMu.Lock()
		defer encoderMu.Unlock()
		return encoder.Encode(value)
	}

	activeSubscriptions := make(map[protocol.RequestID]*subscription)
	var activeSubscriptionsMu sync.Mutex
	removeSubscription := func(id protocol.RequestID, expected *subscription) *subscription {
		activeSubscriptionsMu.Lock()
		defer activeSubscriptionsMu.Unlock()
		subscriber := activeSubscriptions[id]
		if expected != nil && subscriber != expected {
			return nil
		}
		delete(activeSubscriptions, id)
		return subscriber
	}
	var subscriptionWriters sync.WaitGroup
	defer func() {
		activeSubscriptionsMu.Lock()
		subscribers := make([]*subscription, 0, len(activeSubscriptions))
		for _, subscriber := range activeSubscriptions {
			subscribers = append(subscribers, subscriber)
		}
		activeSubscriptions = make(map[protocol.RequestID]*subscription)
		activeSubscriptionsMu.Unlock()
		for _, subscriber := range subscribers {
			s.unsubscribe(subscriber)
		}
		subscriptionWriters.Wait()
	}()
	subscriptionErrors := make(chan error, 1)
	reportSubscriptionError := func(err error) {
		select {
		case subscriptionErrors <- err:
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
			activeSubscriptionsMu.Lock()
			existing := activeSubscriptions[*request.ID]
			activeSubscriptionsMu.Unlock()
			if existing != nil {
				response := protocol.Response{
					JSONRPC: "2.0",
					ID:      request.ID,
					Error:   protocol.NewError(protocol.CodeInvalidParams, "subscription request ID is already active"),
				}
				if err := encode(response); err != nil {
					return fmt.Errorf("encode duplicate subscription error response: %w", err)
				}
				continue
			}
			subscriber, acknowledged, err := s.subscribe(request.ID, request.Params)
			if err != nil {
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
			activeSubscriptionsMu.Lock()
			activeSubscriptions[*request.ID] = subscriber
			activeSubscriptionsMu.Unlock()
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
							reportSubscriptionError(fmt.Errorf("encode subscription completion: %w", err))
						}
						return
					case message := <-subscriber.events:
						if err := encode(message); err != nil {
							reportSubscriptionError(fmt.Errorf("encode subscription message: %w", err))
							return
						}
					}
				}
			}(*request.ID, subscriber)
			continue
		}
		response := s.Handle(ctx, request)
		// A modern probe that is explicitly not implemented is the only server
		// signal that permits switching this stdio connection into legacy mode.
		// All other failures remain failures and never trigger downgrade.
		if compatibility.era == stdioEraModern && request.Method == "server/discover" &&
			response.Error != nil && response.Error.Code == protocol.CodeMethodNotFound {
			compatibility.era = stdioEraLegacy
		}
		// JSON-RPC notifications have no ID and must not receive responses.
		if request.ID == nil {
			continue
		}
		if err := encode(response); err != nil {
			return fmt.Errorf("encode response: %w", err)
		}
	}

	select {
	case err := <-subscriptionErrors:
		return err
	default:
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	return nil
}
