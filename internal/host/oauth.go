package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxOAuthReplayBodyBytes = 1 << 20

var oauthDisallowedNetworks = mustParseOAuthNetworks(
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
	"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	"2001:db8::/32", "2001:10::/28", "2001:2::/48",
)

type OAuthCallback struct {
	Code   string
	State  string
	Issuer string
}

type OAuthToken struct {
	AccessToken string
	Issuer      string
	Resource    string
	Audience    []string
	Scopes      []string
	ExpiresAt   time.Time
}

type OAuthClientRegistration struct {
	Issuer   string
	Resource string
	ClientID string
}

type pendingOAuthFlow struct {
	Issuer, Resource, State, Verifier, RedirectURI string
	Scopes                                         []string
	IssuerParameterRequired                        bool
	ExpiresAt                                      time.Time
}

type MemoryOAuthStore struct {
	mu      sync.Mutex
	flows   map[string]pendingOAuthFlow
	tokens  map[string]OAuthToken
	clients map[string]string
}

func NewMemoryOAuthStore() *MemoryOAuthStore {
	return &MemoryOAuthStore{flows: make(map[string]pendingOAuthFlow), tokens: make(map[string]OAuthToken), clients: make(map[string]string)}
}

func oauthStoreKey(issuer, resource string) string { return issuer + "\x00" + resource }

func (s *MemoryOAuthStore) saveFlow(flow pendingOAuthFlow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flows[flow.State] = flow
}
func (s *MemoryOAuthStore) takeFlow(state string) (pendingOAuthFlow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow, ok := s.flows[state]
	delete(s.flows, state)
	return flow, ok
}
func (s *MemoryOAuthStore) saveToken(token OAuthToken) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[oauthStoreKey(token.Issuer, token.Resource)] = token
}
func (s *MemoryOAuthStore) token(issuer, resource string, now time.Time) (OAuthToken, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.tokens[oauthStoreKey(issuer, resource)]
	return token, ok && token.ExpiresAt.After(now)
}

func (s *MemoryOAuthStore) saveClient(issuer, resource, clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[oauthStoreKey(issuer, resource)] = clientID
}

func (s *MemoryOAuthStore) client(issuer, resource string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clientID, ok := s.clients[oauthStoreKey(issuer, resource)]
	return clientID, ok
}

type OAuthHostConfig struct {
	HTTPClient           *http.Client
	CanonicalResourceURI string
	RedirectURI          string
	ClientRegistrations  []OAuthClientRegistration
	ClientIDMetadataURL  string
	UserClientID         func(string) (string, error)
	Store                *MemoryOAuthStore
	OpenAuthorization    func(string) (OAuthCallback, error)
	Now                  func() time.Time
	AllowLoopback        bool
	lookupIP             func(context.Context, string) ([]net.IP, error)
}

type OAuthHost struct{ config OAuthHostConfig }

type protectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

type authorizationServerMetadata struct {
	Issuer                    string   `json:"issuer"`
	AuthorizationEndpoint     string   `json:"authorization_endpoint"`
	TokenEndpoint             string   `json:"token_endpoint"`
	RegistrationEndpoint      string   `json:"registration_endpoint,omitempty"`
	CodeChallengeMethods      []string `json:"code_challenge_methods_supported"`
	IssuerParameterSupported  bool     `json:"authorization_response_iss_parameter_supported"`
	ClientIDMetadataSupported bool     `json:"client_id_metadata_document_supported"`
}

func NewOAuthHost(config OAuthHostConfig) (*OAuthHost, error) {
	if config.CanonicalResourceURI == "" || config.RedirectURI == "" || config.Store == nil || config.OpenAuthorization == nil {
		return nil, fmt.Errorf("oauth host requires resource, redirect, memory store, and authorization callback")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	policy := oauthEndpointPolicy{allowLoopback: config.AllowLoopback}
	if _, err := policy.validate(config.CanonicalResourceURI, "canonical resource URI"); err != nil {
		return nil, err
	}
	if _, err := (oauthEndpointPolicy{allowLoopback: true}).validate(config.RedirectURI, "redirect URI"); err != nil {
		return nil, err
	}
	if config.ClientIDMetadataURL != "" {
		if _, err := policy.validate(config.ClientIDMetadataURL, "client ID metadata URL"); err != nil {
			return nil, err
		}
	}
	for _, registration := range config.ClientRegistrations {
		if registration.Issuer == "" || registration.Resource == "" || registration.ClientID == "" {
			return nil, fmt.Errorf("oauth client registration requires issuer, resource, and client ID")
		}
		if _, err := policy.validate(registration.Issuer, "client registration issuer"); err != nil {
			return nil, err
		}
		if _, err := policy.validate(registration.Resource, "client registration resource"); err != nil {
			return nil, err
		}
	}
	transport, err := secureOAuthTransport(config.HTTPClient.Transport, policy, config.lookupIP)
	if err != nil {
		return nil, err
	}
	client := *config.HTTPClient
	client.Transport = transport
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	config.HTTPClient = &client
	if config.Now == nil {
		config.Now = time.Now
	}
	return &OAuthHost{config: config}, nil
}

// Do sends one protected request and performs at most one authorization or
// scope-upgrade replay. The request body is buffered only for that bounded replay.
func (h *OAuthHost) Do(ctx context.Context, request *http.Request, scopes []string) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, fmt.Errorf("oauth request is required")
	}
	if _, err := h.endpointPolicy().validate(request.URL.String(), "protected resource endpoint"); err != nil {
		return nil, err
	}
	if !sameOAuthResource(request.URL, h.config.CanonicalResourceURI) {
		return nil, fmt.Errorf("protected resource endpoint mismatch")
	}
	body, err := readReplayBody(request)
	if err != nil {
		return nil, err
	}
	var token OAuthToken
	for attempt := 0; attempt < 3; attempt++ {
		clone := request.Clone(ctx)
		clone.Body = io.NopCloser(bytes.NewReader(body))
		clone.Header.Del("Authorization")
		if token.AccessToken != "" {
			clone.Header.Set("Authorization", "Bearer "+token.AccessToken)
		}
		response, err := h.config.HTTPClient.Do(clone)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
			return response, nil
		}
		challenges := response.Header.Values("WWW-Authenticate")
		_ = response.Body.Close()
		if attempt == 2 {
			return nil, fmt.Errorf("oauth authorization retry exhausted")
		}
		metadataURL, challengedScopes, err := parseBearerChallenges(challenges)
		if err != nil {
			return nil, err
		}
		scopes = unionScopes(scopes, challengedScopes)
		token, err = h.authorize(ctx, request.URL, metadataURL, scopes)
		if err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("oauth authorization failed")
}

func (h *OAuthHost) authorize(ctx context.Context, endpoint *url.URL, metadataURL string, scopes []string) (OAuthToken, error) {
	resource, err := h.discoverResource(ctx, endpoint, metadataURL)
	if err != nil {
		return OAuthToken{}, err
	}
	if resource.Resource != h.config.CanonicalResourceURI || len(resource.AuthorizationServers) == 0 {
		return OAuthToken{}, fmt.Errorf("protected resource metadata mismatch")
	}
	server, err := h.discoverAuthorizationServer(ctx, resource.AuthorizationServers[0])
	if err != nil {
		return OAuthToken{}, err
	}
	if token, ok := h.config.Store.token(server.Issuer, resource.Resource, h.config.Now()); ok && containsAll(token.Scopes, scopes) {
		return token, nil
	}
	if !containsStringValue(server.CodeChallengeMethods, "S256") {
		return OAuthToken{}, fmt.Errorf("authorization server does not support PKCE S256")
	}
	clientID, err := h.resolveClientID(ctx, server, resource.Resource)
	if err != nil {
		return OAuthToken{}, err
	}
	state, err := randomURLToken(24)
	if err != nil {
		return OAuthToken{}, err
	}
	verifier, err := randomURLToken(48)
	if err != nil {
		return OAuthToken{}, err
	}
	challenge := sha256.Sum256([]byte(verifier))
	flow := pendingOAuthFlow{Issuer: server.Issuer, Resource: resource.Resource, State: state, Verifier: verifier, RedirectURI: h.config.RedirectURI, Scopes: append([]string(nil), scopes...), IssuerParameterRequired: server.IssuerParameterSupported, ExpiresAt: h.config.Now().Add(5 * time.Minute)}
	h.config.Store.saveFlow(flow)
	defer h.config.Store.takeFlow(state)
	authorizeURL, err := h.endpointPolicy().validate(server.AuthorizationEndpoint, "authorization endpoint")
	if err != nil {
		return OAuthToken{}, err
	}
	query := authorizeURL.Query()
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", h.config.RedirectURI)
	query.Set("resource", resource.Resource)
	query.Set("scope", strings.Join(scopes, " "))
	query.Set("state", state)
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	query.Set("code_challenge_method", "S256")
	authorizeURL.RawQuery = query.Encode()
	callback, err := h.config.OpenAuthorization(authorizeURL.String())
	if err != nil {
		return OAuthToken{}, err
	}
	stored, ok := h.config.Store.takeFlow(state)
	if !ok || callback.State != state || stored.State != state || !stored.ExpiresAt.After(h.config.Now()) {
		return OAuthToken{}, fmt.Errorf("oauth state mismatch or expired")
	}
	if callback.Issuer != "" && callback.Issuer != stored.Issuer {
		return OAuthToken{}, fmt.Errorf("authorization issuer mismatch")
	}
	if stored.IssuerParameterRequired && callback.Issuer == "" {
		return OAuthToken{}, fmt.Errorf("authorization issuer missing")
	}
	token, err := h.exchangeCode(ctx, server.TokenEndpoint, clientID, callback.Code, stored)
	if err != nil {
		return OAuthToken{}, err
	}
	h.config.Store.saveToken(token)
	return token, nil
}

func (h *OAuthHost) discoverResource(ctx context.Context, endpoint *url.URL, advertised string) (protectedResourceMetadata, error) {
	candidates := []string{}
	if advertised != "" {
		candidates = append(candidates, advertised)
	} else {
		base := endpoint.Scheme + "://" + endpoint.Host
		candidates = append(candidates, base+"/.well-known/oauth-protected-resource"+endpoint.EscapedPath(), base+"/.well-known/oauth-protected-resource")
	}
	var metadata protectedResourceMetadata
	for _, candidate := range candidates {
		if _, err := h.endpointPolicy().validate(candidate, "protected resource metadata URL"); err != nil {
			continue
		}
		if fetchJSON(ctx, h.config.HTTPClient, candidate, &metadata) == nil {
			return metadata, nil
		}
	}
	return metadata, fmt.Errorf("protected resource metadata discovery failed")
}

func (h *OAuthHost) discoverAuthorizationServer(ctx context.Context, issuer string) (authorizationServerMetadata, error) {
	parsed, err := h.endpointPolicy().validate(issuer, "authorization server issuer")
	if err != nil {
		return authorizationServerMetadata{}, err
	}
	base := parsed.Scheme + "://" + parsed.Host
	path := strings.TrimSuffix(parsed.EscapedPath(), "/")
	candidates := []string{base + "/.well-known/oauth-authorization-server" + path, strings.TrimSuffix(issuer, "/") + "/.well-known/oauth-authorization-server", base + "/.well-known/openid-configuration" + path, strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"}
	for _, candidate := range candidates {
		var metadata authorizationServerMetadata
		if fetchJSON(ctx, h.config.HTTPClient, candidate, &metadata) == nil {
			if metadata.Issuer != issuer {
				return metadata, fmt.Errorf("authorization server issuer mismatch")
			}
			if _, err := h.endpointPolicy().validate(metadata.AuthorizationEndpoint, "authorization endpoint"); err != nil {
				return metadata, err
			}
			if _, err := h.endpointPolicy().validate(metadata.TokenEndpoint, "token endpoint"); err != nil {
				return metadata, err
			}
			if metadata.RegistrationEndpoint != "" {
				if _, err := h.endpointPolicy().validate(metadata.RegistrationEndpoint, "registration endpoint"); err != nil {
					return metadata, err
				}
			}
			return metadata, nil
		}
	}
	return authorizationServerMetadata{}, fmt.Errorf("authorization server discovery failed")
}

func (h *OAuthHost) resolveClientID(ctx context.Context, server authorizationServerMetadata, resource string) (string, error) {
	if clientID, ok := h.config.Store.client(server.Issuer, resource); ok {
		return clientID, nil
	}
	for _, registration := range h.config.ClientRegistrations {
		if registration.Issuer == server.Issuer && registration.Resource == resource {
			h.config.Store.saveClient(server.Issuer, resource, registration.ClientID)
			return registration.ClientID, nil
		}
	}
	if h.config.ClientIDMetadataURL != "" && server.ClientIDMetadataSupported {
		var document struct {
			ClientID     string   `json:"client_id"`
			ClientName   string   `json:"client_name"`
			RedirectURIs []string `json:"redirect_uris"`
		}
		if err := fetchJSON(ctx, h.config.HTTPClient, h.config.ClientIDMetadataURL, &document); err != nil {
			return "", fmt.Errorf("fetch client ID metadata: %w", err)
		}
		if document.ClientID != h.config.ClientIDMetadataURL || document.ClientName == "" || !containsStringValue(document.RedirectURIs, h.config.RedirectURI) {
			return "", fmt.Errorf("client ID metadata mismatch")
		}
		h.config.Store.saveClient(server.Issuer, resource, document.ClientID)
		return document.ClientID, nil
	}
	if server.RegistrationEndpoint != "" {
		applicationType := "web"
		redirect, _ := url.Parse(h.config.RedirectURI)
		if redirect != nil && (redirect.Hostname() == "localhost" || redirect.Hostname() == "127.0.0.1" || redirect.Hostname() == "::1") {
			applicationType = "native"
		}
		payload, err := json.Marshal(map[string]any{"redirect_uris": []string{h.config.RedirectURI}, "application_type": applicationType, "client_name": "mcp-from-scratch-host"})
		if err != nil {
			return "", err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.RegistrationEndpoint, bytes.NewReader(payload))
		if err != nil {
			return "", err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := h.config.HTTPClient.Do(request)
		if err == nil {
			defer response.Body.Close()
			var result struct {
				ClientID string `json:"client_id"`
			}
			if response.StatusCode < 300 && decodeOAuthJSON(response.Body, &result) == nil && result.ClientID != "" {
				h.config.Store.saveClient(server.Issuer, resource, result.ClientID)
				return result.ClientID, nil
			}
		}
	}
	if h.config.UserClientID != nil {
		clientID, err := h.config.UserClientID(server.Issuer)
		if err == nil && clientID != "" {
			h.config.Store.saveClient(server.Issuer, resource, clientID)
		}
		return clientID, err
	}
	return "", fmt.Errorf("no OAuth client registration available")
}

func (h *OAuthHost) exchangeCode(ctx context.Context, endpoint, clientID, code string, flow pendingOAuthFlow) (OAuthToken, error) {
	if _, err := h.endpointPolicy().validate(endpoint, "token endpoint"); err != nil {
		return OAuthToken{}, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {flow.RedirectURI}, "code_verifier": {flow.Verifier}, "resource": {flow.Resource}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthToken{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := h.config.HTTPClient.Do(request)
	if err != nil {
		return OAuthToken{}, err
	}
	defer response.Body.Close()
	var result struct {
		AccessToken string   `json:"access_token"`
		ExpiresIn   int64    `json:"expires_in"`
		Scope       string   `json:"scope"`
		Audience    []string `json:"aud"`
	}
	if response.StatusCode >= 300 || decodeOAuthJSON(response.Body, &result) != nil || result.AccessToken == "" || result.ExpiresIn <= 0 {
		return OAuthToken{}, fmt.Errorf("token exchange failed")
	}
	if !containsStringValue(result.Audience, flow.Resource) {
		return OAuthToken{}, fmt.Errorf("token audience mismatch")
	}
	return OAuthToken{AccessToken: result.AccessToken, Issuer: flow.Issuer, Resource: flow.Resource, Audience: result.Audience, Scopes: strings.Fields(result.Scope), ExpiresAt: h.config.Now().Add(time.Duration(result.ExpiresIn) * time.Second)}, nil
}

func fetchJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return fmt.Errorf("status %d", response.StatusCode)
	}
	return decodeOAuthJSON(response.Body, target)
}

func decodeOAuthJSON(body io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(body, maxOAuthReplayBodyBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxOAuthReplayBodyBytes {
		return fmt.Errorf("oauth response body exceeds %d bytes", maxOAuthReplayBodyBytes)
	}
	return json.Unmarshal(data, target)
}
func readReplayBody(request *http.Request) ([]byte, error) {
	if request.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxOAuthReplayBodyBytes+1))
	if len(body) > maxOAuthReplayBodyBytes {
		return nil, fmt.Errorf("oauth request body exceeds %d bytes", maxOAuthReplayBodyBytes)
	}
	if err == nil {
		request.Body = io.NopCloser(bytes.NewReader(body))
	}
	return body, err
}

type oauthEndpointPolicy struct{ allowLoopback bool }

func (policy oauthEndpointPolicy) validate(raw, label string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return nil, fmt.Errorf("oauth %s must be an absolute URL", label)
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("oauth %s must not contain userinfo or fragment", label)
	}
	hostname := parsed.Hostname()
	isLoopback := strings.EqualFold(hostname, "localhost")
	if ip := net.ParseIP(hostname); ip != nil {
		isLoopback = ip.IsLoopback()
		if !isLoopback && (ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
			return nil, fmt.Errorf("oauth %s must not target a private or link-local address", label)
		}
	}
	if isLoopback && !policy.allowLoopback {
		return nil, fmt.Errorf("oauth %s must not target loopback", label)
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback && policy.allowLoopback) {
		return nil, fmt.Errorf("oauth %s must use https or loopback http", label)
	}
	return parsed, nil
}

func (h *OAuthHost) endpointPolicy() oauthEndpointPolicy {
	return oauthEndpointPolicy{allowLoopback: h.config.AllowLoopback}
}

func secureOAuthTransport(base http.RoundTripper, policy oauthEndpointPolicy, lookup func(context.Context, string) ([]net.IP, error)) (*http.Transport, error) {
	if base == nil {
		base = http.DefaultTransport
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("oauth HTTP client requires *http.Transport")
	}
	if transport.DialTLS != nil || transport.DialTLSContext != nil {
		return nil, fmt.Errorf("oauth HTTP transport must not define a custom TLS dialer")
	}
	clone := transport.Clone()
	clone.Proxy = nil
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]net.IP, error) {
			if ip := net.ParseIP(host); ip != nil {
				return []net.IP{ip}, nil
			}
			addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			ips := make([]net.IP, 0, len(addresses))
			for _, address := range addresses {
				ips = append(ips, address.IP)
			}
			return ips, nil
		}
	}
	dialer := &net.Dialer{}
	clone.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("oauth endpoint resolved to no addresses")
		}
		for _, ip := range ips {
			if !oauthIPAllowed(ip, policy.allowLoopback) {
				return nil, fmt.Errorf("oauth endpoint resolved to a disallowed address")
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	return clone, nil
}

func oauthIPAllowed(ip net.IP, allowLoopback bool) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return false
	}
	if ip.IsLoopback() {
		return allowLoopback
	}
	for _, network := range oauthDisallowedNetworks {
		if network.Contains(ip) {
			return false
		}
	}
	return ip.IsGlobalUnicast()
}

func mustParseOAuthNetworks(values ...string) []*net.IPNet {
	networks := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			panic(err)
		}
		networks = append(networks, network)
	}
	return networks
}

func sameOAuthResource(endpoint *url.URL, canonical string) bool {
	want, err := url.Parse(canonical)
	if err != nil {
		return false
	}
	return endpoint.Scheme == want.Scheme && endpoint.Host == want.Host && endpoint.EscapedPath() == want.EscapedPath() && endpoint.RawQuery == want.RawQuery
}
func randomURLToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func parseBearerChallenges(values []string) (string, []string, error) {
	if len(values) == 0 {
		return "", nil, nil
	}
	var bearerParams []string
	for _, value := range values {
		parts, err := splitAuthParams(value)
		if err != nil {
			return "", nil, err
		}
		currentBearer := false
		for _, part := range parts {
			name, remainder, challengeStart := splitChallengeStart(part)
			if challengeStart {
				currentBearer = strings.EqualFold(name, "Bearer")
				if currentBearer {
					if bearerParams != nil {
						return "", nil, fmt.Errorf("multiple Bearer challenges")
					}
					bearerParams = []string{}
					if remainder != "" {
						bearerParams = append(bearerParams, remainder)
					}
				}
				continue
			}
			if currentBearer {
				bearerParams = append(bearerParams, part)
			}
		}
	}
	if bearerParams == nil {
		return "", nil, fmt.Errorf("oauth response did not contain a Bearer challenge")
	}
	critical := map[string]string{}
	for _, param := range bearerParams {
		pair := strings.SplitN(param, "=", 2)
		if len(pair) != 2 {
			return "", nil, fmt.Errorf("malformed Bearer challenge")
		}
		name := strings.ToLower(strings.TrimSpace(pair[0]))
		if !validAuthToken(name) {
			return "", nil, fmt.Errorf("malformed Bearer challenge")
		}
		raw := strings.TrimSpace(pair[1])
		field := raw
		if strings.HasPrefix(raw, `"`) {
			decoded, err := strconv.Unquote(raw)
			if err != nil {
				return "", nil, fmt.Errorf("malformed Bearer challenge")
			}
			field = decoded
		} else if strings.ContainsAny(raw, " \t,") {
			return "", nil, fmt.Errorf("malformed Bearer challenge")
		}
		if name == "resource_metadata" || name == "scope" {
			if _, duplicate := critical[name]; duplicate {
				return "", nil, fmt.Errorf("duplicate Bearer challenge parameter %q", name)
			}
			critical[name] = field
		}
	}
	return critical["resource_metadata"], strings.Fields(critical["scope"]), nil
}

func splitChallengeStart(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	space := strings.IndexAny(value, " \t")
	if space < 0 || strings.Contains(value[:space], "=") {
		return "", "", false
	}
	name := value[:space]
	if !validAuthToken(strings.ToLower(name)) {
		return "", "", false
	}
	return name, strings.TrimSpace(value[space+1:]), true
}

func validAuthToken(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", char)) {
			return false
		}
	}
	return true
}

func splitAuthParams(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	var (
		parts   []string
		start   int
		quoted  bool
		escaped bool
	)
	for i, char := range value {
		if escaped {
			escaped = false
			continue
		}
		if quoted && char == '\\' {
			escaped = true
			continue
		}
		if char == '"' {
			quoted = !quoted
			continue
		}
		if char == ',' && !quoted {
			parts = append(parts, strings.TrimSpace(value[start:i]))
			start = i + 1
		}
	}
	if quoted || escaped {
		return nil, fmt.Errorf("malformed Bearer challenge")
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts, nil
}
func unionScopes(left, right []string) []string {
	set := map[string]struct{}{}
	for _, v := range append(append([]string{}, left...), right...) {
		if v != "" {
			set[v] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func containsAll(have, want []string) bool {
	for _, v := range want {
		if !containsStringValue(have, v) {
			return false
		}
	}
	return true
}
func containsStringValue(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
