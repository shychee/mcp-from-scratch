package host

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

// ServerEra identifies the request lifecycle selected for one stdio process.
type ServerEra string

const (
	ServerEraModern ServerEra = "modern"
	ServerEraLegacy ServerEra = "legacy"
)

// CompatibilityResult is cached by a host for the lifetime of one server
// process. Legacy fallback is deliberately outside the stateless HTTP path.
type CompatibilityResult struct {
	Era             ServerEra
	ProtocolVersion string
	Discovery       protocol.Response
}

type compatibilityNegotiator struct {
	once   sync.Once
	result CompatibilityResult
	err    error
}

func (n *compatibilityNegotiator) detect(
	client protocolClient,
	supportedVersions []string,
	initializeLegacy func() error,
) (CompatibilityResult, error) {
	n.once.Do(func() {
		n.result, n.err = detectServerEra(client, supportedVersions, initializeLegacy)
	})
	return n.result, n.err
}

func detectServerEra(
	client protocolClient,
	supportedVersions []string,
	initializeLegacy func() error,
) (CompatibilityResult, error) {
	if len(supportedVersions) == 0 {
		return CompatibilityResult{}, fmt.Errorf("detect server era: no supported protocol versions")
	}

	requested := supportedVersions[0]
	response, err := callDiscovery(client, 1, requested)
	if err == nil {
		if validDiscoveryEnvelope(response, 1) && validModernDiscovery(response.Result, requested) {
			return CompatibilityResult{Era: ServerEraModern, ProtocolVersion: requested, Discovery: response}, nil
		}
		if validDiscoveryEnvelope(response, 1) && validLegacyDiscovery(response.Result) {
			return selectLegacyEra(initializeLegacy, response)
		}
		return CompatibilityResult{}, fmt.Errorf("server/discover returned an invalid result")
	}

	if response.Error == nil {
		return CompatibilityResult{}, fmt.Errorf("server/discover: %w", err)
	}
	if !validRPCErrorEnvelope(response, 1) {
		return CompatibilityResult{}, fmt.Errorf("server/discover returned an invalid JSON-RPC error envelope")
	}
	switch response.Error.Code {
	case protocol.CodeMethodNotFound:
		return selectLegacyEra(initializeLegacy, response)
	case protocol.CodeUnsupportedProtocolVersion:
		retryVersion, retry := advertisedCommonVersion(response.Error.Data, supportedVersions, requested)
		if !retry {
			return CompatibilityResult{}, fmt.Errorf("server/discover: %w", err)
		}
		retryResponse, retryErr := callDiscovery(client, 2, retryVersion)
		if retryErr != nil {
			return CompatibilityResult{}, fmt.Errorf("server/discover retry %q: %w", retryVersion, retryErr)
		}
		if !validDiscoveryEnvelope(retryResponse, 2) || !validModernDiscovery(retryResponse.Result, retryVersion) {
			return CompatibilityResult{}, fmt.Errorf("server/discover retry %q returned an invalid result", retryVersion)
		}
		return CompatibilityResult{
			Era:             ServerEraModern,
			ProtocolVersion: retryVersion,
			Discovery:       retryResponse,
		}, nil
	default:
		return CompatibilityResult{}, fmt.Errorf("server/discover: %w", err)
	}
}

func validDiscoveryEnvelope(response protocol.Response, id int) bool {
	return response.JSONRPC == "2.0" && integerIDEquals(response.ID, id) && response.Error == nil
}

func validRPCErrorEnvelope(response protocol.Response, id int) bool {
	return response.JSONRPC == "2.0" && integerIDEquals(response.ID, id) && response.Error != nil
}

func integerIDEquals(requestID *protocol.RequestID, value int) bool {
	if requestID == nil {
		return false
	}
	number, ok := requestID.Int64()
	return ok && number == int64(value)
}

func callDiscovery(client protocolClient, id int, version string) (protocol.Response, error) {
	params, err := json.Marshal(protocol.RequestParams{Meta: requestMetaForVersion(version)})
	if err != nil {
		return protocol.Response{}, fmt.Errorf("encode discovery metadata: %w", err)
	}
	return client.call(protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(id),
		Method:  "server/discover",
		Params:  params,
	})
}

func selectLegacyEra(
	initializeLegacy func() error,
	discovery protocol.Response,
) (CompatibilityResult, error) {
	if initializeLegacy == nil {
		return CompatibilityResult{}, fmt.Errorf("legacy MCP server detected but no initializer is configured")
	}
	if err := initializeLegacy(); err != nil {
		return CompatibilityResult{}, fmt.Errorf("initialize legacy server: %w", err)
	}
	return CompatibilityResult{
		Era:             ServerEraLegacy,
		ProtocolVersion: "2025-06-18",
		Discovery:       discovery,
	}, nil
}

func legacyInitializeCall(client protocolClient) error {
	response, err := client.call(protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(2),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcp-from-scratch-host","version":"0.1.0"}}`),
	})
	if err != nil {
		return fmt.Errorf("legacy initialize: %w", err)
	}
	if !validLegacyInitializeResult(response.Result) {
		return fmt.Errorf("legacy initialize returned an invalid result")
	}
	_, err = client.call(protocol.Request{JSONRPC: "2.0", Method: "notifications/initialized"})
	return err
}

func validLegacyInitializeResult(raw json.RawMessage) bool {
	var result struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ServerInfo      protocol.Implementation    `json:"serverInfo"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false
	}
	return result.ProtocolVersion != "" && result.Capabilities != nil && result.ServerInfo.Name != ""
}

func advertisedCommonVersion(data any, supported []string, requested string) (string, bool) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return "", false
	}
	var versionData struct {
		Supported []string `json:"supported"`
	}
	if err := json.Unmarshal(encoded, &versionData); err != nil {
		return "", false
	}
	advertised := make(map[string]struct{}, len(versionData.Supported))
	for _, version := range versionData.Supported {
		advertised[version] = struct{}{}
	}
	for _, version := range supported {
		if version == requested {
			continue
		}
		if _, ok := advertised[version]; ok {
			return version, true
		}
	}
	return "", false
}

func validModernDiscovery(raw json.RawMessage, requestedVersion string) bool {
	var result struct {
		ResultType        string                     `json:"resultType"`
		SupportedVersions []string                   `json:"supportedVersions"`
		Capabilities      map[string]json.RawMessage `json:"capabilities"`
		Meta              protocol.ResultMeta        `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false
	}
	if result.ResultType != protocol.ResultTypeComplete || result.Capabilities == nil ||
		result.Meta.ServerInfo.Name == "" || result.Meta.ServerInfo.Version == "" {
		return false
	}
	for _, version := range result.SupportedVersions {
		if version == requestedVersion {
			return true
		}
	}
	return false
}

func validLegacyDiscovery(raw json.RawMessage) bool {
	var result struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ServerInfo      protocol.Implementation    `json:"serverInfo"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false
	}
	version, err := time.Parse("2006-01-02", result.ProtocolVersion)
	legacyCutoff, cutoffErr := time.Parse("2006-01-02", "2025-11-25")
	return err == nil && cutoffErr == nil && !version.After(legacyCutoff) && result.Capabilities != nil &&
		result.ServerInfo.Name != "" && result.ServerInfo.Version != ""
}

func requestMetaForVersion(version string) protocol.RequestMeta {
	meta := clientRequestMeta()
	meta.ProtocolVersion = version
	return meta
}
