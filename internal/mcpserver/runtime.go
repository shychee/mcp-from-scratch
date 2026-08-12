package mcpserver

import (
	"context"
	"encoding/json"
	"math"
	"sync"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

// NotificationEmitter receives protocol notifications produced while a
// request is running. It returns false when the transport can no longer
// accept events (for example, after a client disconnect).
type NotificationEmitter func(protocol.Notification) bool

type runtimeContextKey struct{}

type requestRuntime struct {
	ctx   context.Context
	emit  NotificationEmitter
	mu    sync.Mutex
	token json.RawMessage
	last  float64
	done  bool
}

// WithNotificationSink attaches a transport event sink to a request context.
// It is exported so other protocol notification producers can share the same
// request-scoped delivery path.
func WithNotificationSink(ctx context.Context, emit NotificationEmitter) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, runtimeContextKey{}, &requestRuntime{
		ctx:  ctx,
		emit: emit,
		last: math.Inf(-1),
	})
}

// EmitNotification sends a notification through the request's transport sink.
// It is safe for concurrent producers and returns false when no sink exists or
// the sink has rejected the notification.
func EmitNotification(ctx context.Context, notification protocol.Notification) bool {
	if ctx == nil {
		return false
	}
	runtime, ok := ctx.Value(runtimeContextKey{}).(*requestRuntime)
	if !ok || runtime == nil || runtime.emit == nil || runtime.ctx == nil {
		return false
	}
	if ctx.Err() != nil || runtime.ctx.Err() != nil {
		return false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.done || runtime.ctx.Err() != nil {
		return false
	}
	return runtime.emit(notification)
}

// ReportProgress emits notifications/progress for the request's progress
// token. Tokens are limited to the protocol's string/integer forms, values are
// monotonic per request, and reports after the final response are suppressed.
func ReportProgress(ctx context.Context, progress, total float64, message string) bool {
	if ctx == nil {
		return false
	}
	runtime, ok := ctx.Value(runtimeContextKey{}).(*requestRuntime)
	if !ok || runtime == nil || runtime.emit == nil || runtime.ctx == nil || len(runtime.token) == 0 {
		return false
	}
	if ctx.Err() != nil || runtime.ctx.Err() != nil {
		return false
	}
	if math.IsNaN(progress) || math.IsInf(progress, 0) || progress < 0 {
		return false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.done || ctx.Err() != nil || runtime.ctx.Err() != nil || progress < runtime.last {
		return false
	}
	params := struct {
		Meta struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
		Progress float64  `json:"progress"`
		Total    *float64 `json:"total,omitempty"`
		Message  string   `json:"message,omitempty"`
	}{
		Progress: progress,
		Message:  message,
	}
	params.Meta.ProgressToken = append(json.RawMessage(nil), runtime.token...)
	if !math.IsNaN(total) && !math.IsInf(total, 0) && total >= 0 {
		params.Total = &total
	}
	notification := protocol.Notification{
		JSONRPC: "2.0",
		Method:  "notifications/progress",
		Params:  mustMarshal(params),
	}
	if !runtime.emit(notification) {
		return false
	}
	runtime.last = progress
	return true
}

func withRequestRuntime(ctx context.Context, rawParams json.RawMessage, emit NotificationEmitter) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	runtime := &requestRuntime{ctx: ctx, emit: emit, last: math.Inf(-1)}
	var fields struct {
		Meta struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(rawParams, &fields); err == nil && validProgressToken(fields.Meta.ProgressToken) {
		runtime.token = append(json.RawMessage(nil), fields.Meta.ProgressToken...)
	}
	return context.WithValue(ctx, runtimeContextKey{}, runtime)
}

func validProgressToken(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	if raw[0] == '"' {
		var value string
		return json.Unmarshal(raw, &value) == nil
	}
	var value protocol.RequestID
	return json.Unmarshal(raw, &value) == nil && !value.IsString()
}

func finishRequestRuntime(ctx context.Context) {
	runtime, ok := ctx.Value(runtimeContextKey{}).(*requestRuntime)
	if !ok || runtime == nil {
		return
	}
	runtime.mu.Lock()
	runtime.done = true
	runtime.mu.Unlock()
}
