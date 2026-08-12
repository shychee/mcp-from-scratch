package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

const maxOAuthRequestBodyBytes = 1 << 20

const (
	// BearerMethodsHeader is the only bearer transport accepted by the
	// resource server. Query-string access tokens are deliberately rejected.
	BearerMethodsHeader = "header"

	// OAuthScopeError is the RFC 6750 error used when a token lacks an
	// operation's complete scope set.
	OAuthScopeError = "insufficient_scope"

	// OAuthInvalidTokenError is the RFC 6750 error used for malformed or
	// otherwise unusable credentials.
	OAuthInvalidTokenError = "invalid_token"
)

// TokenClaims contains the claims needed by the resource-server boundary.
//
// Validators are responsible for authenticating and decoding the token. The
// resource server still validates the claims that bind the token to this
// resource: active, expiry, issuer, and audience.
type TokenClaims struct {
	Active    bool      `json:"active"`
	ExpiresAt time.Time `json:"expires_at"`
	Issuer    string    `json:"iss"`
	Audience  []string  `json:"aud"`
	Scopes    []string  `json:"scope"`
	Subject   string    `json:"sub,omitempty"`
}

// TokenValidator authenticates a bearer token and returns its claims. It must
// never be given a token from a query parameter: the wrapper rejects those
// requests before invoking this interface.
type TokenValidator interface {
	ValidateToken(context.Context, string) (TokenClaims, error)
}

// TokenValidatorFunc adapts a function into a TokenValidator.
type TokenValidatorFunc func(context.Context, string) (TokenClaims, error)

// ValidateToken implements TokenValidator.
func (validator TokenValidatorFunc) ValidateToken(ctx context.Context, token string) (TokenClaims, error) {
	return validator(ctx, token)
}

// scopePolicy maps an MCP method or method/name operation to all scopes that
// operation requires. A request is authorized only when it has every listed
// scope. Name-specific entries may use any of the following keys:
// "method:name", "method/name", or "method name"; a method-only entry is
// also accepted.
type scopePolicy map[string][]string

// ResourceServerConfig configures an OAuth protected resource wrapper.
type ResourceServerConfig struct {
	// CanonicalResourceURI is the exact resource identifier sent in protected
	// resource metadata and checked against the token audience.
	CanonicalResourceURI string

	// ProtectedResourceMetadataURL is the metadata URL advertised in bearer
	// challenges. GET requests for this URL are served by the wrapper.
	ProtectedResourceMetadataURL string

	// AuthorizationServers lists the issuer URLs trusted for this resource.
	AuthorizationServers []string

	// ScopesSupported is advertised in RFC 9728 metadata. Every scope named by
	// a configured policy must be present here when this list is non-empty.
	ScopesSupported []string

	// MethodScopes is the operation-to-scope policy. A request must contain
	// every scope in the matching operation's entry. Methods without an entry
	// require no additional operation scope.
	MethodScopes map[string][]string

	// Validator is the injected token validator.
	Validator TokenValidator

	// Now is optional and exists to make expiry behavior deterministic in
	// tests. A nil value uses time.Now.
	Now func() time.Time
}

// ProtectedResourceMetadata is the RFC 9728 protected-resource metadata
// document exposed by ResourceServer.
type ProtectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

// Principal is the authenticated identity placed in the request context.
// Raw bearer tokens are intentionally not retained.
type Principal struct {
	Subject  string
	Issuer   string
	Audience []string
	Scopes   []string
	Claims   TokenClaims
}

type principalContextKey struct{}

// PrincipalFromContext returns the validated principal, if the request passed
// through a ResourceServer.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	if !ok {
		return Principal{}, false
	}
	return principal, true
}

// ResourceServer wraps an existing MCP HTTP handler with OAuth resource-server
// authentication and protected-resource metadata.
type ResourceServer struct {
	next        http.Handler
	config      ResourceServerConfig
	metadata    ProtectedResourceMetadata
	metadataURL *url.URL
	policy      scopePolicy
	now         func() time.Time
}

// NewResourceServer validates config and returns a wrapper around next. The
// existing MCP handler is called only after bearer authentication and scope
// authorization succeed.
func NewResourceServer(config ResourceServerConfig, next http.Handler) (*ResourceServer, error) {
	if next == nil {
		return nil, errors.New("oauth resource server: nil next handler")
	}
	if err := validateResourceURI("canonical resource URI", config.CanonicalResourceURI); err != nil {
		return nil, err
	}
	metadataURL, err := parseAbsoluteURL("protected resource metadata URL", config.ProtectedResourceMetadataURL)
	if err != nil {
		return nil, err
	}
	if len(config.AuthorizationServers) == 0 {
		return nil, errors.New("oauth resource server: at least one authorization server is required")
	}
	authorizationServers := make([]string, len(config.AuthorizationServers))
	for i, issuer := range config.AuthorizationServers {
		if _, err := parseAbsoluteURL(fmt.Sprintf("authorization server %d", i), issuer); err != nil {
			return nil, err
		}
		authorizationServers[i] = issuer
	}
	if config.Validator == nil {
		return nil, errors.New("oauth resource server: token validator is required")
	}

	scopesSupported := cloneStrings(config.ScopesSupported)
	if err := validateUniqueNonEmpty("supported scope", scopesSupported); err != nil {
		return nil, err
	}
	policy := cloneScopePolicy(config.MethodScopes)
	if err := validatePolicy(scopesSupported, policy); err != nil {
		return nil, err
	}

	return &ResourceServer{
		next: next,
		config: ResourceServerConfig{
			CanonicalResourceURI:         config.CanonicalResourceURI,
			ProtectedResourceMetadataURL: config.ProtectedResourceMetadataURL,
			AuthorizationServers:         authorizationServers,
			ScopesSupported:              scopesSupported,
			MethodScopes:                 cloneScopePolicy(config.MethodScopes),
			Validator:                    config.Validator,
			Now:                          config.Now,
		},
		metadata: ProtectedResourceMetadata{
			Resource:               config.CanonicalResourceURI,
			AuthorizationServers:   cloneStrings(authorizationServers),
			ScopesSupported:        scopesSupported,
			BearerMethodsSupported: []string{BearerMethodsHeader},
		},
		metadataURL: metadataURL,
		policy:      policy,
		now:         firstNonNilClock(config.Now),
	}, nil
}

// ProtectedResourceMetadata returns a copy of the RFC 9728 metadata document.
func (server *ResourceServer) ProtectedResourceMetadata() ProtectedResourceMetadata {
	metadata := server.metadata
	metadata.AuthorizationServers = cloneStrings(metadata.AuthorizationServers)
	metadata.ScopesSupported = cloneStrings(metadata.ScopesSupported)
	metadata.BearerMethodsSupported = cloneStrings(metadata.BearerMethodsSupported)
	return metadata
}

// ServeHTTP exposes metadata and protects all other routes.
func (server *ResourceServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if server.isMetadataRequest(request) {
		server.serveMetadata(writer, request)
		return
	}
	server.serveProtected(writer, request)
}

func (server *ResourceServer) isMetadataRequest(request *http.Request) bool {
	if request == nil || request.Method != http.MethodGet || request.URL == nil {
		return false
	}
	return request.URL.Path == server.metadataURL.Path && request.URL.RawQuery == server.metadataURL.RawQuery
}

func (server *ResourceServer) serveMetadata(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "public, max-age=300")
	writer.WriteHeader(http.StatusOK)
	_ = writeJSON(writer, server.metadata)
}

func (server *ResourceServer) serveProtected(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Add("Vary", "Authorization")
	if request == nil {
		writeUnauthorized(writer, server.config.ProtectedResourceMetadataURL, "")
		return
	}
	if request.URL != nil {
		if _, present := request.URL.Query()["access_token"]; present {
			writeUnauthorized(writer, server.config.ProtectedResourceMetadataURL, OAuthInvalidTokenError)
			return
		}
	}

	token, present, valid := parseBearerHeader(request.Header.Values("Authorization"))
	if !present {
		writeUnauthorized(writer, server.config.ProtectedResourceMetadataURL, "")
		return
	}
	if !valid {
		writeUnauthorized(writer, server.config.ProtectedResourceMetadataURL, OAuthInvalidTokenError)
		return
	}

	claims, err := server.config.Validator.ValidateToken(request.Context(), token)
	if err != nil || !server.validClaims(claims) {
		writeUnauthorized(writer, server.config.ProtectedResourceMetadataURL, OAuthInvalidTokenError)
		return
	}

	principal := Principal{
		Subject:  claims.Subject,
		Issuer:   claims.Issuer,
		Audience: cloneStrings(firstAudience(claims)),
		Scopes:   cloneStrings(normalizedScopes(claims)),
		Claims:   claims,
	}
	method, name, err := protectedOperation(request)
	if err != nil {
		http.Error(writer, "invalid protected request", http.StatusBadRequest)
		return
	}
	required := server.requiredScopes(method, name)
	if missing := missingScopes(principal.Scopes, required); len(missing) != 0 {
		writeForbidden(writer, server.config.ProtectedResourceMetadataURL, required)
		return
	}

	// Clone and redact the authorization header before dispatch. Clone keeps
	// the request body and all MCP headers intact, so the core handler remains
	// responsible for detecting header/body mismatches.
	principalContext := context.WithValue(request.Context(), principalContextKey{}, principal)
	delegated := request.Clone(WithExecutionPrincipal(principalContext, oauthTaskPrincipal(claims)))
	delegated.Header.Del("Authorization")
	server.next.ServeHTTP(writer, delegated)
}

func oauthTaskPrincipal(claims TokenClaims) string {
	return claims.Issuer + "\x00" + claims.Subject
}

func (server *ResourceServer) validClaims(claims TokenClaims) bool {
	if !claims.Active || claims.Subject == "" {
		return false
	}
	expiresAt := claims.ExpiresAt
	if expiresAt.IsZero() || !expiresAt.After(server.now()) {
		return false
	}
	issuerOK := false
	for _, configured := range server.config.AuthorizationServers {
		if claims.Issuer == configured {
			issuerOK = true
			break
		}
	}
	if !issuerOK || claims.Issuer == "" {
		return false
	}
	audienceOK := false
	for _, audience := range firstAudience(claims) {
		if audience == server.config.CanonicalResourceURI {
			audienceOK = true
			break
		}
	}
	return audienceOK
}

func (server *ResourceServer) requiredScopes(method, name string) []string {
	if method == "" {
		return nil
	}
	keys := make([]string, 0, 5)
	if name != "" {
		keys = append(keys, method+":"+name, method+"/"+name, method+" "+name)
	}
	keys = append(keys, method)
	for _, key := range keys {
		if scopes, ok := server.policy[key]; ok {
			return cloneStrings(scopes)
		}
	}
	return nil
}

func protectedOperation(request *http.Request) (string, string, error) {
	if request.Body == nil {
		return request.Header.Get(protocol.HeaderMethod), request.Header.Get(protocol.HeaderName), nil
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxOAuthRequestBodyBytes+1))
	if err != nil {
		return "", "", err
	}
	request.Body = io.NopCloser(strings.NewReader(string(body)))
	if len(body) > maxOAuthRequestBodyBytes {
		return "", "", fmt.Errorf("request body too large")
	}
	var rpcRequest protocol.Request
	if json.Unmarshal(body, &rpcRequest) != nil || rpcRequest.Method == "" {
		return "", "", nil
	}
	var params struct {
		Name   string `json:"name"`
		URI    string `json:"uri"`
		TaskID string `json:"taskId"`
	}
	_ = json.Unmarshal(rpcRequest.Params, &params)
	name := params.Name
	if rpcRequest.Method == "resources/read" {
		name = params.URI
	}
	if rpcRequest.Method == "tasks/get" || rpcRequest.Method == "tasks/update" || rpcRequest.Method == "tasks/cancel" {
		name = params.TaskID
	}
	return rpcRequest.Method, name, nil
}

func parseBearerHeader(values []string) (token string, present, valid bool) {
	if len(values) == 0 {
		return "", false, false
	}
	if len(values) != 1 {
		return "", true, false
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", true, false
	}
	return parts[1], true, true
}

func normalizedScopes(claims TokenClaims) []string {
	return uniqueStrings(claims.Scopes)
}

func firstAudience(claims TokenClaims) []string {
	return claims.Audience
}

func missingScopes(granted, required []string) []string {
	if len(required) == 0 {
		return nil
	}
	grantedSet := make(map[string]struct{}, len(granted))
	for _, scope := range granted {
		grantedSet[scope] = struct{}{}
	}
	missing := make([]string, 0, len(required))
	for _, scope := range required {
		if _, ok := grantedSet[scope]; !ok {
			missing = append(missing, scope)
		}
	}
	return missing
}

func writeUnauthorized(writer http.ResponseWriter, metadataURL, errorCode string) {
	writer.Header().Set("WWW-Authenticate", bearerChallenge(metadataURL, errorCode, nil))
	writer.WriteHeader(http.StatusUnauthorized)
}

func writeForbidden(writer http.ResponseWriter, metadataURL string, required []string) {
	writer.Header().Set("WWW-Authenticate", bearerChallenge(metadataURL, OAuthScopeError, required))
	writer.WriteHeader(http.StatusForbidden)
}

func bearerChallenge(metadataURL, errorCode string, scopes []string) string {
	params := make([]string, 0, 3)
	if errorCode != "" {
		params = append(params, `error="`+escapeAuthParam(errorCode)+`"`)
	}
	if len(scopes) != 0 {
		params = append(params, `scope="`+escapeAuthParam(strings.Join(scopes, " "))+`"`)
	}
	params = append(params, `resource_metadata="`+escapeAuthParam(metadataURL)+`"`)
	return "Bearer " + strings.Join(params, ", ")
}

func escapeAuthParam(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func validateResourceURI(label, value string) error {
	_, err := parseAbsoluteURL(label, value)
	return err
}

func parseAbsoluteURL(label, value string) (*url.URL, error) {
	if value == "" {
		return nil, fmt.Errorf("oauth resource server: %s is required", label)
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Scheme == "" {
		return nil, fmt.Errorf("oauth resource server: %s must be an absolute URI", label)
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("oauth resource server: %s must not contain userinfo or fragment", label)
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, fmt.Errorf("oauth resource server: %s must use https or loopback http", label)
	}
	return parsed, nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func validateUniqueNonEmpty(label string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return fmt.Errorf("oauth resource server: %s must not be empty", label)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("oauth resource server: duplicate %s %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validatePolicy(scopesSupported []string, policy scopePolicy) error {
	for key, required := range policy {
		if err := validateUniqueNonEmpty(fmt.Sprintf("policy scope for %q", key), required); err != nil {
			return err
		}
	}
	if len(scopesSupported) == 0 {
		return nil
	}
	supported := make(map[string]struct{}, len(scopesSupported))
	for _, scope := range scopesSupported {
		supported[scope] = struct{}{}
	}
	for _, required := range policy {
		for _, scope := range required {
			if _, ok := supported[scope]; !ok {
				return fmt.Errorf("oauth resource server: policy scope %q is not supported", scope)
			}
		}
	}
	return nil
}

func cloneScopePolicy(policy map[string][]string) scopePolicy {
	cloned := make(scopePolicy, len(policy))
	for key, scopes := range policy {
		cloned[key] = cloneStrings(scopes)
	}
	return cloned
}

func cloneStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string(nil), values...)
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func firstNonNilClock(clock func() time.Time) func() time.Time {
	if clock != nil {
		return clock
	}
	return time.Now
}

func writeJSON(writer http.ResponseWriter, value any) error {
	return json.NewEncoder(writer).Encode(value)
}
