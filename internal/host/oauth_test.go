package host

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeOAuthWorld struct {
	server           *httptest.Server
	mu               sync.Mutex
	challenge        string
	tokenScopes      []string
	wrongAudience    bool
	stepUp           bool
	missingPKCE      bool
	metadataIssuer   string
	zeroTokenTTL     bool
	resourceFallback bool
	tokenCalls       int

	callbackMutate func(OAuthCallback) OAuthCallback
}

func newFakeOAuthWorld(t *testing.T) *fakeOAuthWorld {
	t.Helper()
	world := &fakeOAuthWorld{}
	world.server = httptest.NewServer(http.HandlerFunc(world.serveHTTP))
	t.Cleanup(world.server.Close)
	return world
}

func (world *fakeOAuthWorld) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	issuer := world.server.URL
	switch request.URL.Path {
	case "/mcp":
		authorization := request.Header.Get("Authorization")
		if authorization == "" {
			writer.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+issuer+`/.well-known/oauth-protected-resource"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		world.mu.Lock()
		stepUp := world.stepUp && world.tokenCalls == 1
		world.mu.Unlock()
		if stepUp {
			writer.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="mcp:read mcp:write", resource_metadata="`+issuer+`/.well-known/oauth-protected-resource"`)
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, `{"ok":true}`)
	case "/.well-known/oauth-protected-resource":
		if world.resourceFallback && request.URL.Query().Get("fallback") == "" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"resource": issuer + "/mcp", "authorization_servers": []string{issuer}})
	case "/.well-known/oauth-authorization-server":
		methods := []string{"S256"}
		if world.missingPKCE {
			methods = nil
		}
		metadataIssuer := issuer
		if world.metadataIssuer != "" {
			metadataIssuer = world.metadataIssuer
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"issuer": metadataIssuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token",
			"code_challenge_methods_supported": methods, "authorization_response_iss_parameter_supported": true,
		})
	case "/token":
		_ = request.ParseForm()
		world.mu.Lock()
		defer world.mu.Unlock()
		hash := sha256.Sum256([]byte(request.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(hash[:]) != world.challenge || request.Form.Get("resource") != issuer+"/mcp" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		world.tokenCalls++
		audience := []string{issuer + "/mcp"}
		if world.wrongAudience {
			audience = []string{issuer + "/other"}
		}
		expiresIn := 300
		if world.zeroTokenTTL {
			expiresIn = 0
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "token", "expires_in": expiresIn, "scope": strings.Join(world.tokenScopes, " "), "aud": audience})
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func TestOAuthHostRegistrationPrecedenceAndDCRApplicationType(t *testing.T) {
	for name, redirect := range map[string]string{"native": "http://127.0.0.1/callback", "web": "https://client.example/callback"} {
		t.Run(name, func(t *testing.T) {
			var gotApplicationType string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/register" {
					var body map[string]any
					_ = json.NewDecoder(request.Body).Decode(&body)
					gotApplicationType, _ = body["application_type"].(string)
					_ = json.NewEncoder(writer).Encode(map[string]string{"client_id": "dynamic"})
					return
				}
				writer.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()
			host, err := NewOAuthHost(OAuthHostConfig{HTTPClient: server.Client(), CanonicalResourceURI: "https://resource.example/mcp", RedirectURI: redirect, Store: NewMemoryOAuthStore(), OpenAuthorization: func(string) (OAuthCallback, error) { return OAuthCallback{}, nil }, AllowLoopback: true})
			if err != nil {
				t.Fatal(err)
			}
			clientID, err := host.resolveClientID(context.Background(), authorizationServerMetadata{Issuer: "https://issuer.example", RegistrationEndpoint: server.URL + "/register"}, "https://resource.example/mcp")
			if err != nil || clientID != "dynamic" || gotApplicationType != name {
				t.Fatalf("client/type/error = %q/%q/%v", clientID, gotApplicationType, err)
			}
		})
	}
}

func TestOAuthHostPrefersConfiguredClientBeforeDCR(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	host, err := NewOAuthHost(OAuthHostConfig{HTTPClient: server.Client(), CanonicalResourceURI: "https://resource.example/mcp", RedirectURI: "http://127.0.0.1/callback", ClientRegistrations: []OAuthClientRegistration{{Issuer: "https://issuer.example", Resource: "https://resource.example/mcp", ClientID: "registered"}}, Store: NewMemoryOAuthStore(), OpenAuthorization: func(string) (OAuthCallback, error) { return OAuthCallback{}, nil }, AllowLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	clientID, err := host.resolveClientID(context.Background(), authorizationServerMetadata{Issuer: "https://issuer.example", RegistrationEndpoint: server.URL}, "https://resource.example/mcp")
	if err != nil || clientID != "registered" || called {
		t.Fatalf("client/called/error = %q/%v/%v", clientID, called, err)
	}
}

func TestOAuthHostUsesValidatedClientIDMetadataBeforeDCR(t *testing.T) {
	var dcrCalled bool
	var metadataURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/client-metadata":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"client_id": metadataURL, "client_name": "MCP test host",
				"redirect_uris": []string{"http://127.0.0.1/callback"},
			})
		case "/register":
			dcrCalled = true
			_ = json.NewEncoder(writer).Encode(map[string]string{"client_id": "dynamic"})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	metadataURL = server.URL + "/client-metadata"
	host, err := NewOAuthHost(OAuthHostConfig{
		HTTPClient: server.Client(), CanonicalResourceURI: "https://resource.example/mcp",
		RedirectURI: "http://127.0.0.1/callback", ClientIDMetadataURL: metadataURL,
		Store: NewMemoryOAuthStore(), OpenAuthorization: func(string) (OAuthCallback, error) { return OAuthCallback{}, nil },
		AllowLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clientID, err := host.resolveClientID(context.Background(), authorizationServerMetadata{
		Issuer: "https://issuer.example", ClientIDMetadataSupported: true, RegistrationEndpoint: server.URL + "/register",
	}, "https://resource.example/mcp")
	if err != nil || clientID != metadataURL || dcrCalled {
		t.Fatalf("client/dcr/error = %q/%v/%v", clientID, dcrCalled, err)
	}
}

func (world *fakeOAuthWorld) host(t *testing.T) *OAuthHost {
	t.Helper()
	host, err := NewOAuthHost(OAuthHostConfig{
		HTTPClient: world.server.Client(), CanonicalResourceURI: world.server.URL + "/mcp",
		RedirectURI:         "http://127.0.0.1/callback",
		ClientRegistrations: []OAuthClientRegistration{{Issuer: world.server.URL, Resource: world.server.URL + "/mcp", ClientID: "client"}},
		Store:               NewMemoryOAuthStore(), AllowLoopback: true,
		OpenAuthorization: func(raw string) (OAuthCallback, error) {
			parsed, err := url.Parse(raw)
			if err != nil {
				return OAuthCallback{}, err
			}
			world.mu.Lock()
			world.challenge = parsed.Query().Get("code_challenge")
			world.tokenScopes = strings.Fields(parsed.Query().Get("scope"))
			world.mu.Unlock()
			callback := OAuthCallback{Code: "code", State: parsed.Query().Get("state"), Issuer: world.server.URL}
			if world.callbackMutate != nil {
				callback = world.callbackMutate(callback)
			}
			return callback, nil
		},
		Now: func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func (world *fakeOAuthWorld) request(t *testing.T) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, world.server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestOAuthHostCompletesPKCEAndReplaysRequest(t *testing.T) {
	world := newFakeOAuthWorld(t)
	response, err := world.host(t).Do(context.Background(), world.request(t), []string{"mcp:read"})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || world.tokenCalls != 1 {
		t.Fatalf("status/token calls = %d/%d", response.StatusCode, world.tokenCalls)
	}
}

func TestOAuthHostRejectsStateAndIssuerMixup(t *testing.T) {
	for name, mutate := range map[string]func(OAuthCallback) OAuthCallback{
		"state": func(callback OAuthCallback) OAuthCallback { callback.State = "wrong"; return callback },
		"issuer": func(callback OAuthCallback) OAuthCallback {
			callback.Issuer = "https://attacker.example"
			return callback
		},
	} {
		t.Run(name, func(t *testing.T) {
			world := newFakeOAuthWorld(t)
			world.callbackMutate = mutate
			if _, err := world.host(t).Do(context.Background(), world.request(t), []string{"mcp:read"}); err == nil {
				t.Fatal("Do() accepted mixed-up callback")
			}
		})
	}
}

func TestOAuthHostRejectsWrongTokenAudience(t *testing.T) {
	world := newFakeOAuthWorld(t)
	world.wrongAudience = true
	if _, err := world.host(t).Do(context.Background(), world.request(t), []string{"mcp:read"}); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("Do() error = %v", err)
	}
}

func TestOAuthHostPerformsOneScopeUpgrade(t *testing.T) {
	world := newFakeOAuthWorld(t)
	world.stepUp = true
	response, err := world.host(t).Do(context.Background(), world.request(t), []string{"mcp:read"})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || world.tokenCalls != 2 || !containsStringValue(world.tokenScopes, "mcp:write") {
		t.Fatalf("status/calls/scopes = %d/%d/%v", response.StatusCode, world.tokenCalls, world.tokenScopes)
	}
}

func TestOAuthHostRequiresPKCES256(t *testing.T) {
	world := newFakeOAuthWorld(t)
	world.missingPKCE = true
	if _, err := world.host(t).Do(context.Background(), world.request(t), []string{"mcp:read"}); err == nil || !strings.Contains(err.Error(), "S256") {
		t.Fatalf("Do() error = %v", err)
	}
}

func TestOAuthHostRejectsMetadataIssuerMismatch(t *testing.T) {
	world := newFakeOAuthWorld(t)
	world.metadataIssuer = "https://attacker.example"
	if _, err := world.host(t).Do(context.Background(), world.request(t), []string{"mcp:read"}); err == nil || !strings.Contains(err.Error(), "issuer mismatch") {
		t.Fatalf("Do() error = %v", err)
	}
}

func TestOAuthHostRejectsNonExpiringTokenResponse(t *testing.T) {
	world := newFakeOAuthWorld(t)
	world.zeroTokenTTL = true
	if _, err := world.host(t).Do(context.Background(), world.request(t), []string{"mcp:read"}); err == nil || !strings.Contains(err.Error(), "token exchange") {
		t.Fatalf("Do() error = %v", err)
	}
}

func TestOAuthHostRejectsUnsafeEndpointsAndRedirects(t *testing.T) {
	base := OAuthHostConfig{
		CanonicalResourceURI: "https://resource.example/mcp", RedirectURI: "http://127.0.0.1/callback",
		Store: NewMemoryOAuthStore(), OpenAuthorization: func(string) (OAuthCallback, error) { return OAuthCallback{}, nil },
	}
	for name, mutate := range map[string]func(*OAuthHostConfig){
		"resource scheme": func(config *OAuthHostConfig) { config.CanonicalResourceURI = "http://resource.example/mcp" },
		"redirect host":   func(config *OAuthHostConfig) { config.RedirectURI = "http://client.example/callback" },
		"private IP":      func(config *OAuthHostConfig) { config.ClientIDMetadataURL = "https://169.254.169.254/client" },
		"userinfo":        func(config *OAuthHostConfig) { config.ClientIDMetadataURL = "https://user@example.com/client" },
		"fragment":        func(config *OAuthHostConfig) { config.ClientIDMetadataURL = "https://example.com/client#secret" },
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			if _, err := NewOAuthHost(config); err == nil {
				t.Fatal("NewOAuthHost accepted unsafe endpoint")
			}
		})
	}

	redirectTargetCalled := false
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectTargetCalled = true }))
	defer redirectTarget.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, redirectTarget.URL, http.StatusFound)
	}))
	defer redirector.Close()
	host, err := NewOAuthHost(OAuthHostConfig{
		HTTPClient: redirector.Client(), CanonicalResourceURI: "https://resource.example/mcp",
		RedirectURI: "http://127.0.0.1/callback", ClientRegistrations: []OAuthClientRegistration{{Issuer: "https://issuer.example", Resource: "https://resource.example/mcp", ClientID: "client"}}, Store: NewMemoryOAuthStore(), AllowLoopback: true,
		OpenAuthorization: func(string) (OAuthCallback, error) { return OAuthCallback{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	var metadata protectedResourceMetadata
	if err := fetchJSON(context.Background(), host.config.HTTPClient, redirector.URL, &metadata); err == nil {
		t.Fatal("fetchJSON followed an OAuth metadata redirect")
	}
	if redirectTargetCalled {
		t.Fatal("redirect target was called")
	}
}

func TestOAuthHostRejectsOversizedReplayBody(t *testing.T) {
	world := newFakeOAuthWorld(t)
	request, err := http.NewRequest(http.MethodPost, world.server.URL+"/mcp", strings.NewReader(strings.Repeat("x", maxOAuthReplayBodyBytes+1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := world.host(t).Do(context.Background(), request, nil); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Do() error = %v", err)
	}
}

func TestOAuthHostBindsCanonicalResourceQuery(t *testing.T) {
	host, err := NewOAuthHost(OAuthHostConfig{
		CanonicalResourceURI: "https://resource.example/mcp?tenant=a", RedirectURI: "http://127.0.0.1/callback",
		Store: NewMemoryOAuthStore(), OpenAuthorization: func(string) (OAuthCallback, error) { return OAuthCallback{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, "https://resource.example/mcp?tenant=b", nil)
	if _, err := host.Do(context.Background(), request, nil); err == nil || !strings.Contains(err.Error(), "endpoint mismatch") {
		t.Fatalf("Do() error = %v", err)
	}
}

func TestOAuthHostRejectsDNSResolvedPrivateAddress(t *testing.T) {
	host, err := NewOAuthHost(OAuthHostConfig{
		CanonicalResourceURI: "https://resource.example/mcp", RedirectURI: "http://127.0.0.1/callback",
		Store: NewMemoryOAuthStore(), OpenAuthorization: func(string) (OAuthCallback, error) { return OAuthCallback{}, nil },
		lookupIP: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("10.0.0.1")}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, "https://resource.example/mcp", nil)
	if _, err := host.Do(context.Background(), request, nil); err == nil || !strings.Contains(err.Error(), "disallowed address") {
		t.Fatalf("Do() error = %v", err)
	}
}

func TestOAuthIPPolicyRejectsSpecialUseRanges(t *testing.T) {
	for _, raw := range []string{"100.100.100.200", "100.64.0.1", "192.0.2.1", "198.18.0.1", "203.0.113.1", "2001:db8::1"} {
		if oauthIPAllowed(net.ParseIP(raw), false) {
			t.Fatalf("oauthIPAllowed(%s) = true", raw)
		}
	}
	if !oauthIPAllowed(net.ParseIP("8.8.8.8"), false) {
		t.Fatal("oauthIPAllowed(public) = false")
	}
}

func TestOAuthHostRejectsCustomTLSDialer(t *testing.T) {
	client := &http.Client{Transport: &http.Transport{DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, fmt.Errorf("must not run")
	}}}
	_, err := NewOAuthHost(OAuthHostConfig{
		HTTPClient: client, CanonicalResourceURI: "https://resource.example/mcp", RedirectURI: "http://127.0.0.1/callback",
		Store: NewMemoryOAuthStore(), OpenAuthorization: func(string) (OAuthCallback, error) { return OAuthCallback{}, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "custom TLS dialer") {
		t.Fatalf("NewOAuthHost error = %v", err)
	}
}

func TestParseBearerChallenge(t *testing.T) {
	tests := []struct {
		name         string
		challenge    string
		wantMetadata string
		wantScopes   []string
		wantErr      bool
	}{
		{name: "empty fallback"},
		{name: "lowercase", challenge: `bearer resource_metadata="https://resource.example/meta", scope="read write"`, wantMetadata: "https://resource.example/meta", wantScopes: []string{"read", "write"}},
		{name: "quoted comma", challenge: `Bearer resource_metadata="https://resource.example/meta?a=1,b=2"`, wantMetadata: "https://resource.example/meta?a=1,b=2"},
		{name: "escaped quote", challenge: `Bearer resource_metadata="https://resource.example/\"meta"`, wantMetadata: `https://resource.example/"meta`},
		{name: "basic only", challenge: `Basic realm="test"`, wantErr: true},
		{name: "multiple schemes", challenge: `Bearer scope="read", Basic realm="test"`, wantScopes: []string{"read"}},
		{name: "duplicate", challenge: `Bearer scope="read", scope="write"`, wantErr: true},
		{name: "malformed quote", challenge: `Bearer scope="read`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := []string{test.challenge}
			if test.challenge == "" {
				values = nil
			}
			metadata, scopes, err := parseBearerChallenges(values)
			if (err != nil) != test.wantErr || metadata != test.wantMetadata || strings.Join(scopes, " ") != strings.Join(test.wantScopes, " ") {
				t.Fatalf("parse = %q/%v/%v", metadata, scopes, err)
			}
		})
	}
}

func TestParseBearerChallengesSelectsBearerAmongOtherSchemes(t *testing.T) {
	metadata, scopes, err := parseBearerChallenges([]string{
		`Basic realm="test"`,
		`Bearer resource_metadata="https://resource.example/meta", scope="read write"`,
	})
	if err != nil || metadata != "https://resource.example/meta" || strings.Join(scopes, " ") != "read write" {
		t.Fatalf("parse = %q/%v/%v", metadata, scopes, err)
	}
	metadata, scopes, err = parseBearerChallenges([]string{`Basic realm="test", Bearer scope="read", DPoP algs="ES256"`})
	if err != nil || metadata != "" || strings.Join(scopes, " ") != "read" {
		t.Fatalf("combined parse = %q/%v/%v", metadata, scopes, err)
	}
}

func TestDecodeOAuthJSONRejectsOversizedResponse(t *testing.T) {
	var target map[string]any
	if err := decodeOAuthJSON(strings.NewReader(strings.Repeat("x", maxOAuthReplayBodyBytes+1)), &target); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("decode error = %v", err)
	}
}

func TestOAuthHostCleansPendingFlowWhenBrowserFails(t *testing.T) {
	world := newFakeOAuthWorld(t)
	store := NewMemoryOAuthStore()
	host, err := NewOAuthHost(OAuthHostConfig{
		HTTPClient: world.server.Client(), CanonicalResourceURI: world.server.URL + "/mcp",
		RedirectURI:         "http://127.0.0.1/callback",
		ClientRegistrations: []OAuthClientRegistration{{Issuer: world.server.URL, Resource: world.server.URL + "/mcp", ClientID: "client"}},
		Store:               store, AllowLoopback: true,
		OpenAuthorization: func(string) (OAuthCallback, error) { return OAuthCallback{}, fmt.Errorf("browser closed") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Do(context.Background(), world.request(t), []string{"mcp:read"}); err == nil {
		t.Fatal("Do() accepted browser failure")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.flows) != 0 {
		t.Fatalf("pending flows = %d, want 0", len(store.flows))
	}
}

func TestOAuthHostDoesNotReuseStaticClientAcrossIssuers(t *testing.T) {
	host, err := NewOAuthHost(OAuthHostConfig{
		CanonicalResourceURI: "https://resource.example/mcp", RedirectURI: "http://127.0.0.1/callback",
		ClientRegistrations: []OAuthClientRegistration{{Issuer: "https://issuer-a.example", Resource: "https://resource.example/mcp", ClientID: "client-a"}},
		Store:               NewMemoryOAuthStore(), OpenAuthorization: func(string) (OAuthCallback, error) { return OAuthCallback{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.resolveClientID(context.Background(), authorizationServerMetadata{Issuer: "https://issuer-b.example"}, "https://resource.example/mcp"); err == nil {
		t.Fatal("resolveClientID reused another issuer's registration")
	}
}
