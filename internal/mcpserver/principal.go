package mcpserver

import "context"

type executionPrincipalKey struct{}

// WithExecutionPrincipal attaches a transport-authenticated principal to tool
// and task execution. Unauthenticated transports may explicitly fall back to
// client metadata, but they cannot override a principal already in context.
func WithExecutionPrincipal(ctx context.Context, principal string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, executionPrincipalKey{}, principal)
}

// ExecutionPrincipalFromContext returns the authenticated execution identity.
func ExecutionPrincipalFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	principal, ok := ctx.Value(executionPrincipalKey{}).(string)
	return principal, ok && principal != ""
}
