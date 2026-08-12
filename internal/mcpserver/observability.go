package mcpserver

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

const redactedValue = "[REDACTED]"

var traceParentPattern = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)
var traceStateKeyPattern = regexp.MustCompile(`^(?:[a-z][a-z0-9_*/-]{0,255}|[a-z0-9][a-z0-9_*/-]{0,240}@[a-z][a-z0-9_*/-]{0,13})$`)

// TraceContext contains validated W3C propagation fields from one MCP request.
// Malformed fields are ignored and never participate in authorization.
type TraceContext struct {
	TraceParent string
	TraceState  string
	Baggage     string
}

type observabilityContextKey struct{}

type requestObservability struct {
	trace    TraceContext
	logLevel string
}

func withObservabilityContext(ctx context.Context, raw json.RawMessage) context.Context {
	var params protocol.RequestParams
	if json.Unmarshal(raw, &params) != nil {
		return ctx
	}
	observability := requestObservability{
		trace: TraceContext{
			TraceParent: validTraceParent(params.Meta.TraceParent),
			TraceState:  validTraceState(params.Meta.TraceState),
			Baggage:     validBaggage(params.Meta.Baggage),
		},
	}
	if _, ok := logLevelRank(params.Meta.LogLevel); ok {
		observability.logLevel = params.Meta.LogLevel
	}
	return context.WithValue(ctx, observabilityContextKey{}, observability)
}

func requestHasLogLevel(raw json.RawMessage) bool {
	var params protocol.RequestParams
	if json.Unmarshal(raw, &params) != nil {
		return false
	}
	_, ok := logLevelRank(params.Meta.LogLevel)
	return ok
}

// TraceFromContext returns the request trace fields that passed validation.
func TraceFromContext(ctx context.Context) TraceContext {
	if ctx == nil {
		return TraceContext{}
	}
	observability, _ := ctx.Value(observabilityContextKey{}).(requestObservability)
	return observability.trace
}

// Log emits a request-scoped notifications/message event when the request
// opted in at or below the supplied severity. Sensitive keys are redacted.
func Log(ctx context.Context, level, logger string, data any) bool {
	if ctx == nil {
		return false
	}
	observability, ok := ctx.Value(observabilityContextKey{}).(requestObservability)
	if !ok || observability.logLevel == "" {
		return false
	}
	wanted, validWanted := logLevelRank(observability.logLevel)
	actual, validActual := logLevelRank(level)
	if !validWanted || !validActual || actual < wanted {
		return false
	}
	params, err := json.Marshal(map[string]any{
		"level":  level,
		"logger": logger,
		"data":   redactValue(data),
	})
	if err != nil {
		return false
	}
	return EmitNotification(ctx, protocol.Notification{
		JSONRPC: "2.0",
		Method:  "notifications/message",
		Params:  params,
	})
}

func validTraceParent(value string) string {
	if value == "" || !traceParentPattern.MatchString(value) {
		return ""
	}
	parts := strings.Split(value, "-")
	if parts[1] == strings.Repeat("0", 32) || parts[2] == strings.Repeat("0", 16) {
		return ""
	}
	return value
}

func validTraceState(value string) string {
	if value == "" || len(value) > 512 || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	members := strings.Split(value, ",")
	if len(members) > 32 {
		return ""
	}
	seen := make(map[string]struct{}, len(members))
	for _, member := range members {
		parts := strings.SplitN(strings.TrimSpace(member), "=", 2)
		if len(parts) != 2 || !traceStateKeyPattern.MatchString(parts[0]) || !validTraceStateValue(parts[1]) {
			return ""
		}
		if _, duplicate := seen[parts[0]]; duplicate {
			return ""
		}
		seen[parts[0]] = struct{}{}
	}
	return value
}

func validTraceStateValue(value string) bool {
	if value == "" || value[0] == ' ' || value[len(value)-1] == ' ' {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e || character == ',' || character == '=' {
			return false
		}
	}
	return true
}

func validBaggage(value string) string {
	if value == "" || len(value) > 8192 || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	for _, member := range strings.Split(value, ",") {
		pair := strings.SplitN(strings.TrimSpace(member), ";", 2)[0]
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 || parts[0] == "" {
			return ""
		}
	}
	return value
}

func logLevelRank(level string) (int, bool) {
	levels := map[string]int{
		"debug": 0, "info": 1, "notice": 2, "warning": 3,
		"error": 4, "critical": 5, "alert": 6, "emergency": 7,
	}
	rank, ok := levels[level]
	return rank, ok
}

func redactValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return redactedValue
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var normalized any
	if decoder.Decode(&normalized) != nil {
		return redactedValue
	}
	return redactJSONValue(normalized)
}

func redactJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, child := range typed {
			if sensitiveLogKey(key) {
				redacted[key] = redactedValue
			} else {
				redacted[key] = redactJSONValue(child)
			}
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, child := range typed {
			redacted[index] = redactJSONValue(child)
		}
		return redacted
	default:
		return value
	}
}

func sensitiveLogKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
	for _, sensitive := range []string{"authorization", "token", "password", "secret", "requeststate", "arguments"} {
		if strings.Contains(normalized, sensitive) {
			return true
		}
	}
	return false
}
