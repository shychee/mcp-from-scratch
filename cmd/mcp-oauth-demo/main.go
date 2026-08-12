package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shychee/mcp-from-scratch/internal/host"
	"github.com/shychee/mcp-from-scratch/internal/mcpserver"
)

func main() {
	var (
		mu                sync.Mutex
		expectedChallenge string
		resourceServer    http.Handler
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		issuer := "http://" + request.Host
		switch request.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token",
				"code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": true,
			})
		case "/token":
			_ = request.ParseForm()
			hash := sha256.Sum256([]byte(request.Form.Get("code_verifier")))
			mu.Lock()
			valid := base64.RawURLEncoding.EncodeToString(hash[:]) == expectedChallenge
			mu.Unlock()
			resourceURI := issuer + "/mcp"
			if !valid || request.Form.Get("resource") != resourceURI {
				http.Error(writer, "invalid grant", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"access_token": "demo-access-token", "expires_in": 60, "scope": "mcp:read", "aud": []string{resourceURI},
			})
		default:
			resourceServer.ServeHTTP(writer, request)
		}
	}))
	defer server.Close()
	issuer := server.URL
	resourceURI := issuer + "/mcp"

	validator := mcpserver.TokenValidatorFunc(func(_ context.Context, token string) (mcpserver.TokenClaims, error) {
		if token != "demo-access-token" {
			return mcpserver.TokenClaims{}, fmt.Errorf("unknown token")
		}
		return mcpserver.TokenClaims{
			Active: true, ExpiresAt: time.Now().Add(time.Minute), Issuer: issuer,
			Audience: []string{resourceURI}, Scopes: []string{"mcp:read"}, Subject: "demo-user",
		}, nil
	})
	var err error
	resourceServer, err = mcpserver.NewResourceServer(mcpserver.ResourceServerConfig{
		CanonicalResourceURI: resourceURI, ProtectedResourceMetadataURL: issuer + "/.well-known/oauth-protected-resource",
		AuthorizationServers: []string{issuer}, ScopesSupported: []string{"mcp:read"},
		MethodScopes: map[string][]string{"tools/list": {"mcp:read"}}, Validator: validator,
	}, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, ok := mcpserver.PrincipalFromContext(request.Context())
		if !ok {
			http.Error(writer, "missing principal", http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"authorized": true, "subject": principal.Subject})
	}))
	if err != nil {
		fail("configure resource server", err)
	}

	oauthHost, err := host.NewOAuthHost(host.OAuthHostConfig{
		HTTPClient: server.Client(), CanonicalResourceURI: resourceURI, RedirectURI: "http://127.0.0.1/callback",
		ClientRegistrations: []host.OAuthClientRegistration{{Issuer: issuer, Resource: resourceURI, ClientID: "demo-client"}},
		Store:               host.NewMemoryOAuthStore(), AllowLoopback: true,
		OpenAuthorization: func(raw string) (host.OAuthCallback, error) {
			parsed, err := url.Parse(raw)
			if err != nil {
				return host.OAuthCallback{}, err
			}
			mu.Lock()
			expectedChallenge = parsed.Query().Get("code_challenge")
			mu.Unlock()
			return host.OAuthCallback{Code: "demo-code", State: parsed.Query().Get("state"), Issuer: issuer}, nil
		},
	})
	if err != nil {
		fail("configure OAuth host", err)
	}
	request, err := http.NewRequest(http.MethodPost, resourceURI, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if err != nil {
		fail("create protected request", err)
	}
	response, err := oauthHost.Do(context.Background(), request, []string{"mcp:read"})
	if err != nil {
		fail("complete OAuth flow", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fail("complete OAuth flow", fmt.Errorf("unexpected status %d", response.StatusCode))
	}
	var result map[string]any
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		fail("decode protected response", err)
	}
	output, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(output))
}

func fail(action string, err error) {
	fmt.Fprintf(os.Stderr, "mcp-oauth-demo: %s: %v\n", action, err)
	os.Exit(1)
}
