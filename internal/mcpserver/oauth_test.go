package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResourceServerServesProtectedResourceMetadata(t *testing.T) {
	clock := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	validator := &fakeOAuthValidator{claims: map[string]TokenClaims{
		"valid": validOAuthClaims(clock),
	}}
	server := newOAuthTestServer(t, validator, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Fatal("metadata request reached protected handler")
	}), clock)

	request := httptest.NewRequest(http.MethodGet, "https://resource.example/.well-known/oauth-protected-resource?format=json", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var metadata ProtectedResourceMetadata
	if err := json.Unmarshal(response.Body.Bytes(), &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	want := ProtectedResourceMetadata{
		Resource:               "https://resource.example/mcp",
		AuthorizationServers:   []string{"https://issuer.example"},
		ScopesSupported:        []string{"mcp:read", "mcp:write"},
		BearerMethodsSupported: []string{"header"},
	}
	if got, wantJSON := marshalForTest(t, metadata), marshalForTest(t, want); string(got) != string(wantJSON) {
		t.Fatalf("metadata = %s, want %s", got, wantJSON)
	}
	if validator.calls() != 0 {
		t.Fatalf("validator calls = %d, want 0", validator.calls())
	}
}

func TestResourceServerAcceptsValidTokenAndInjectsPrincipal(t *testing.T) {
	clock := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	validator := &fakeOAuthValidator{claims: map[string]TokenClaims{
		"valid": validOAuthClaims(clock),
	}}
	var (
		dispatched       bool
		gotPrincipal     Principal
		gotAuthorization string
	)
	next := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		dispatched = true
		gotPrincipal, _ = PrincipalFromContext(request.Context())
		if executionPrincipal, ok := ExecutionPrincipalFromContext(request.Context()); !ok || executionPrincipal != "https://issuer.example\x00user-1" {
			t.Errorf("execution principal = %q, present=%v", executionPrincipal, ok)
		}
		gotAuthorization = request.Header.Get("Authorization")
		writer.WriteHeader(http.StatusNoContent)
	})
	server := newOAuthTestServer(t, validator, next, clock)

	request := protectedOAuthRequest(http.MethodPost, "https://resource.example/mcp", "valid")
	request.Header.Set("Mcp-Method", "tools/list")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent || !dispatched {
		t.Fatalf("status/dispatched = %d/%v, want 204/true", response.Code, dispatched)
	}
	if gotAuthorization != "" {
		t.Fatalf("delegated Authorization = %q, want empty", gotAuthorization)
	}
	if gotPrincipal.Subject != "user-1" || gotPrincipal.Issuer != "https://issuer.example" {
		t.Fatalf("principal = %#v, want validated identity", gotPrincipal)
	}
	if !containsString(gotPrincipal.Scopes, "mcp:read") || len(gotPrincipal.Audience) != 1 {
		t.Fatalf("principal = %#v, want scopes and audience", gotPrincipal)
	}
	if got := validator.lastToken(); got != "valid" {
		t.Fatalf("validator token = %q, want valid", got)
	}
}

func TestResourceServerRejectsMissingMalformedAndInvalidBearer(t *testing.T) {
	clock := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	validator := &fakeOAuthValidator{
		claims: map[string]TokenClaims{"valid": validOAuthClaims(clock)},
		errors: map[string]error{"invalid": errors.New("signature mismatch")},
	}
	server := newOAuthTestServer(t, validator, countingHandler(t), clock)

	tests := []struct {
		name             string
		authorization    string
		wantError        string
		wantValidatorHit bool
	}{
		{name: "missing", wantValidatorHit: false},
		{name: "basic", authorization: "Basic abc", wantError: OAuthInvalidTokenError, wantValidatorHit: false},
		{name: "empty bearer", authorization: "Bearer", wantError: OAuthInvalidTokenError, wantValidatorHit: false},
		{name: "too many values", authorization: "Bearer valid, Bearer invalid", wantError: OAuthInvalidTokenError, wantValidatorHit: false},
		{name: "validator error", authorization: "Bearer invalid", wantError: OAuthInvalidTokenError, wantValidatorHit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := validator.calls()
			request := protectedOAuthRequest(http.MethodPost, "https://resource.example/mcp", "")
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			assertOAuthChallenge(t, response, http.StatusUnauthorized, test.wantError, "")
			if got := validator.calls() - before; (got == 1) != test.wantValidatorHit {
				t.Fatalf("validator calls delta = %d, want hit %v", got, test.wantValidatorHit)
			}
		})
	}
}

func TestResourceServerRejectsExpiredIssuerAndAudienceConfusion(t *testing.T) {
	clock := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	base := validOAuthClaims(clock)
	validator := &fakeOAuthValidator{claims: map[string]TokenClaims{
		"expired": func() TokenClaims {
			claims := base
			claims.ExpiresAt = clock
			return claims
		}(),
		"wrong-issuer": func() TokenClaims {
			claims := base
			claims.Issuer = "https://attacker.example"
			return claims
		}(),
		"wrong-audience": func() TokenClaims {
			claims := base
			claims.Audience = []string{"https://other-resource.example/mcp"}
			return claims
		}(),
		"active-false": func() TokenClaims {
			claims := base
			claims.Active = false
			return claims
		}(),
	}}
	server := newOAuthTestServer(t, validator, countingHandler(t), clock)

	for _, token := range []string{"expired", "wrong-issuer", "wrong-audience", "active-false"} {
		t.Run(token, func(t *testing.T) {
			request := protectedOAuthRequest(http.MethodPost, "https://resource.example/mcp", token)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			assertOAuthChallenge(t, response, http.StatusUnauthorized, OAuthInvalidTokenError, "")
		})
	}
}

func TestResourceServerRejectsQueryAccessTokenBeforeValidation(t *testing.T) {
	clock := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	claims := validOAuthClaims(clock)
	claims.Scopes = []string{"mcp:read"}
	validator := &fakeOAuthValidator{claims: map[string]TokenClaims{"valid": claims}}
	server := newOAuthTestServer(t, validator, countingHandler(t), clock)

	request := httptest.NewRequest(http.MethodPost, "https://resource.example/mcp?access_token=valid", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	assertOAuthChallenge(t, response, http.StatusUnauthorized, OAuthInvalidTokenError, "")
	if validator.calls() != 0 {
		t.Fatalf("validator calls = %d, want 0 for query token", validator.calls())
	}
}

func TestResourceServerRequiresCompleteOperationScopeSet(t *testing.T) {
	clock := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	claims := validOAuthClaims(clock)
	claims.Scopes = []string{"mcp:read"}
	validator := &fakeOAuthValidator{claims: map[string]TokenClaims{"read-only": claims}}
	server := newOAuthTestServer(t, validator, countingHandler(t), clock)

	request := protectedOAuthRequest(http.MethodPost, "https://resource.example/mcp", "read-only")
	request.Header.Set("Mcp-Method", "tools/call")
	request.Header.Set("Mcp-Name", "echo")
	request.Body = io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	assertOAuthChallenge(t, response, http.StatusForbidden, OAuthScopeError, "mcp:read mcp:write")
}

func TestResourceServerDelegatesHeaderBodyMismatchAfterAuthentication(t *testing.T) {
	clock := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	validator := &fakeOAuthValidator{claims: map[string]TokenClaims{"valid": validOAuthClaims(clock)}}
	called := false
	next := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		called = true
		if _, ok := PrincipalFromContext(request.Context()); !ok {
			t.Error("principal missing from delegated context")
		}
		writer.WriteHeader(http.StatusTeapot)
	})
	server := newOAuthTestServer(t, validator, next, clock)
	request := protectedOAuthRequest(http.MethodPost, "https://resource.example/mcp", "valid")
	request.Header.Set("Mcp-Method", "tools/list")
	request.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	request.Body = io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusTeapot || !called {
		t.Fatalf("status/called = %d/%v, want delegated 418/true", response.Code, called)
	}
}

func TestResourceServerScopesUseBodyOperationInsteadOfSpoofedHeaders(t *testing.T) {
	clock := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	claims := validOAuthClaims(clock)
	claims.Scopes = []string{"mcp:read"}
	server := newOAuthTestServer(t, &fakeOAuthValidator{claims: map[string]TokenClaims{"read": claims}}, countingHandler(t), clock)
	request := protectedOAuthRequest(http.MethodPost, "https://resource.example/mcp", "read")
	request.Header.Set("Mcp-Method", "tools/list")
	request.Body = io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	assertOAuthChallenge(t, response, http.StatusForbidden, OAuthScopeError, "mcp:read mcp:write")
}

func TestResourceServerProtectedResponsesAreNotCacheable(t *testing.T) {
	clock := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	server := newOAuthTestServer(t, &fakeOAuthValidator{}, countingHandler(t), clock)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, protectedOAuthRequest(http.MethodPost, "https://resource.example/mcp", ""))
	if response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Header().Get("Vary"), "Authorization") {
		t.Fatalf("cache headers = %q/%q", response.Header().Get("Cache-Control"), response.Header().Get("Vary"))
	}
}

func TestResourceServerRejectsUnsafeURLSchemes(t *testing.T) {
	_, err := NewResourceServer(ResourceServerConfig{
		CanonicalResourceURI: "file:///tmp/mcp", ProtectedResourceMetadataURL: "https://resource.example/.well-known/oauth-protected-resource",
		AuthorizationServers: []string{"https://issuer.example"}, Validator: TokenValidatorFunc(func(context.Context, string) (TokenClaims, error) { return TokenClaims{}, nil }),
	}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err == nil {
		t.Fatal("NewResourceServer accepted file resource URI")
	}
}

func TestOAuthTaskPrincipalSeparatesIssuersAndEmptySubjects(t *testing.T) {
	issuerA := TokenClaims{Issuer: "https://issuer-a.example", Subject: "shared"}
	issuerB := TokenClaims{Issuer: "https://issuer-b.example", Subject: "shared"}
	if oauthTaskPrincipal(issuerA) == oauthTaskPrincipal(issuerB) {
		t.Fatal("task principal collided across issuers")
	}
}

func TestResourceServerRejectsTokenWithoutStableSubject(t *testing.T) {
	clock := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	claims := validOAuthClaims(clock)
	claims.Subject = ""
	server := newOAuthTestServer(t, &fakeOAuthValidator{claims: map[string]TokenClaims{"no-sub": claims}}, countingHandler(t), clock)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, protectedOAuthRequest(http.MethodPost, "https://resource.example/mcp", "no-sub"))
	assertOAuthChallenge(t, response, http.StatusUnauthorized, OAuthInvalidTokenError, "")
}

func TestResourceServerEscapesMetadataURLInChallenge(t *testing.T) {
	clock := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	claims := validOAuthClaims(clock)
	claims.Scopes = []string{"mcp:read"}
	validator := &fakeOAuthValidator{claims: map[string]TokenClaims{"valid": claims}}
	config := ResourceServerConfig{
		CanonicalResourceURI:         "https://resource.example/mcp",
		ProtectedResourceMetadataURL: `https://resource.example/.well-known/oauth-protected-resource?next=%22quoted%22`,
		AuthorizationServers:         []string{"https://issuer.example"},
		ScopesSupported:              []string{"mcp:read", "mcp:write"},
		MethodScopes:                 map[string][]string{"tools/call:echo": {"mcp:read", "mcp:write"}},
		Validator:                    validator,
		Now:                          func() time.Time { return clock },
	}
	server, err := NewResourceServer(config, countingHandler(t))
	if err != nil {
		t.Fatal(err)
	}
	request := protectedOAuthRequest(http.MethodPost, "https://resource.example/mcp", "valid")
	request.Header.Set("Mcp-Method", "tools/call")
	request.Header.Set("Mcp-Name", "echo")
	request.Body = io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	challenge := response.Header().Get("WWW-Authenticate")
	if !strings.Contains(challenge, `resource_metadata="https://resource.example/.well-known/oauth-protected-resource?next=%22quoted%22"`) {
		t.Fatalf("challenge = %q, want escaped metadata URL", challenge)
	}
}

func newOAuthTestServer(t *testing.T, validator TokenValidator, next http.Handler, clock time.Time) *ResourceServer {
	t.Helper()
	server, err := NewResourceServer(ResourceServerConfig{
		CanonicalResourceURI:         "https://resource.example/mcp",
		ProtectedResourceMetadataURL: "https://resource.example/.well-known/oauth-protected-resource?format=json",
		AuthorizationServers:         []string{"https://issuer.example"},
		ScopesSupported:              []string{"mcp:read", "mcp:write"},
		MethodScopes: map[string][]string{
			"tools/list":      {"mcp:read"},
			"tools/call:echo": {"mcp:read", "mcp:write"},
		},
		Validator: validator,
		Now:       func() time.Time { return clock },
	}, next)
	if err != nil {
		t.Fatalf("NewResourceServer: %v", err)
	}
	return server
}

func validOAuthClaims(clock time.Time) TokenClaims {
	return TokenClaims{
		Active:    true,
		ExpiresAt: clock.Add(time.Hour),
		Issuer:    "https://issuer.example",
		Audience:  []string{"https://resource.example/mcp"},
		Scopes:    []string{"mcp:read", "mcp:write"},
		Subject:   "user-1",
	}
}

func protectedOAuthRequest(method, target, token string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}

func assertOAuthChallenge(t *testing.T, response *httptest.ResponseRecorder, status int, errorCode, scope string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; challenge = %q", response.Code, status, response.Header().Get("WWW-Authenticate"))
	}
	challenge := response.Header().Get("WWW-Authenticate")
	if !strings.HasPrefix(challenge, "Bearer ") {
		t.Fatalf("challenge = %q, want Bearer challenge", challenge)
	}
	if errorCode == "" {
		if strings.Contains(challenge, `error="`) {
			t.Fatalf("challenge = %q, want no error parameter", challenge)
		}
	} else if !strings.Contains(challenge, `error="`+errorCode+`"`) {
		t.Fatalf("challenge = %q, want error %q", challenge, errorCode)
	}
	if scope != "" && !strings.Contains(challenge, `scope="`+scope+`"`) {
		t.Fatalf("challenge = %q, want scope %q", challenge, scope)
	}
	if !strings.Contains(challenge, `resource_metadata="https://resource.example/.well-known/oauth-protected-resource?format=json"`) &&
		!strings.Contains(challenge, `resource_metadata="https://resource.example/.well-known/oauth-protected-resource?next=%22quoted%22"`) {
		t.Fatalf("challenge = %q, want resource_metadata", challenge)
	}
}

func marshalForTest(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func countingHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Errorf("protected handler unexpectedly called")
		writer.WriteHeader(http.StatusInternalServerError)
	})
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

type fakeOAuthValidator struct {
	mu     sync.Mutex
	claims map[string]TokenClaims
	errors map[string]error
	seen   []string
}

func (validator *fakeOAuthValidator) ValidateToken(_ context.Context, token string) (TokenClaims, error) {
	validator.mu.Lock()
	defer validator.mu.Unlock()
	validator.seen = append(validator.seen, token)
	if err := validator.errors[token]; err != nil {
		return TokenClaims{}, err
	}
	claims, ok := validator.claims[token]
	if !ok {
		return TokenClaims{}, errors.New("unknown token")
	}
	return claims, nil
}

func (validator *fakeOAuthValidator) calls() int {
	validator.mu.Lock()
	defer validator.mu.Unlock()
	return len(validator.seen)
}

func (validator *fakeOAuthValidator) lastToken() string {
	validator.mu.Lock()
	defer validator.mu.Unlock()
	if len(validator.seen) == 0 {
		return ""
	}
	return validator.seen[len(validator.seen)-1]
}
