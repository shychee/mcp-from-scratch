package host

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

type compatibilityCall struct {
	response protocol.Response
	err      error
}

type compatibilityClient struct {
	calls    []compatibilityCall
	requests []protocol.Request
}

func (c *compatibilityClient) call(request protocol.Request) (protocol.Response, error) {
	c.requests = append(c.requests, request)
	call := c.calls[0]
	c.calls = c.calls[1:]
	return call.response, call.err
}

func TestDetectServerEraModern(t *testing.T) {
	t.Parallel()

	client := &compatibilityClient{calls: []compatibilityCall{{response: compatibilityResponse(1, `{
		"resultType":"complete",
		"supportedVersions":["2026-07-28"],
		"capabilities":{},
		"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"fixture","version":"1"}}
	}`)}}}
	legacyCalls := 0

	result, err := detectServerEra(client, []string{protocol.Version20260728}, func() error {
		legacyCalls++
		return nil
	})
	if err != nil {
		t.Fatalf("detectServerEra() error = %v", err)
	}
	if result.Era != ServerEraModern || result.ProtocolVersion != protocol.Version20260728 {
		t.Fatalf("detectServerEra() = %#v, want modern %s", result, protocol.Version20260728)
	}
	if legacyCalls != 0 || len(client.requests) != 1 {
		t.Fatalf("legacy calls = %d, requests = %d, want 0 and 1", legacyCalls, len(client.requests))
	}
}

func TestDetectServerEraRetriesAdvertisedSupportedVersion(t *testing.T) {
	t.Parallel()

	unsupported := protocol.NewErrorWithData(protocol.CodeUnsupportedProtocolVersion, "unsupported protocol version", map[string]any{
		"requested": "2026-07-28",
		"supported": []string{"2026-06-01", "2026-05-01"},
	})
	client := &compatibilityClient{calls: []compatibilityCall{
		{response: compatibilityErrorResponse(1, unsupported), err: unsupported},
		{response: compatibilityResponse(2, `{
			"resultType":"complete",
			"supportedVersions":["2026-06-01"],
			"capabilities":{},
			"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"fixture","version":"1"}}
		}`)},
	}}

	result, err := detectServerEra(client, []string{"2026-07-28", "2026-06-01"}, nil)
	if err != nil {
		t.Fatalf("detectServerEra() error = %v", err)
	}
	if result.ProtocolVersion != "2026-06-01" || len(client.requests) != 2 {
		t.Fatalf("detectServerEra() = %#v, requests = %d", result, len(client.requests))
	}
	var params protocol.RequestParams
	if err := json.Unmarshal(client.requests[1].Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Meta.ProtocolVersion != "2026-06-01" {
		t.Fatalf("retry protocol version = %q", params.Meta.ProtocolVersion)
	}
}

func TestDetectServerEraFallsBackExactlyOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call compatibilityCall
	}{
		{
			name: "method not found",
			call: compatibilityCall{
				response: compatibilityErrorResponse(1, protocol.NewError(protocol.CodeMethodNotFound, "method not found")),
				err:      protocol.NewError(protocol.CodeMethodNotFound, "method not found"),
			},
		},
		{
			name: "legacy shaped result",
			call: compatibilityCall{response: compatibilityResponse(1, `{
				"protocolVersion":"2025-11-25",
				"capabilities":{},
				"serverInfo":{"name":"legacy","version":"1"}
			}`)},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := &compatibilityClient{calls: []compatibilityCall{test.call}}
			legacyCalls := 0
			result, err := detectServerEra(client, []string{protocol.Version20260728}, func() error {
				legacyCalls++
				return nil
			})
			if err != nil {
				t.Fatalf("detectServerEra() error = %v", err)
			}
			if result.Era != ServerEraLegacy || legacyCalls != 1 || len(client.requests) != 1 {
				t.Fatalf("result = %#v, legacy calls = %d, requests = %d", result, legacyCalls, len(client.requests))
			}
		})
	}
}

func TestDetectServerEraDoesNotDowngradeFailures(t *testing.T) {
	t.Parallel()

	malformed := compatibilityResponse(1, `{"resultType":`)
	badEnvelope := compatibilityResponse(1, `{
		"protocolVersion":"2025-11-25",
		"capabilities":{},
		"serverInfo":{"name":"legacy","version":"1"}
	}`)
	badEnvelope.JSONRPC = "1.0"
	invalidLegacyVersion := compatibilityResponse(1, `{
		"protocolVersion":"not-a-version",
		"capabilities":{},
		"serverInfo":{"name":"legacy","version":"1"}
	}`)
	unrelated := protocol.NewError(protocol.CodeInternalError, "server failed")
	unsupported := protocol.NewErrorWithData(protocol.CodeUnsupportedProtocolVersion, "unsupported", map[string]any{
		"supported": []string{"2025-11-25"},
	})
	tests := []struct {
		name string
		call compatibilityCall
	}{
		{name: "transport", call: compatibilityCall{err: errors.New("EOF")}},
		{name: "unrelated rpc", call: compatibilityCall{response: compatibilityErrorResponse(1, unrelated), err: unrelated}},
		{name: "unsupported without common version", call: compatibilityCall{response: compatibilityErrorResponse(1, unsupported), err: unsupported}},
		{name: "malformed", call: compatibilityCall{response: malformed}},
		{name: "bad envelope", call: compatibilityCall{response: badEnvelope}},
		{name: "invalid legacy version", call: compatibilityCall{response: invalidLegacyVersion}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := &compatibilityClient{calls: []compatibilityCall{test.call}}
			legacyCalls := 0
			_, err := detectServerEra(client, []string{protocol.Version20260728}, func() error {
				legacyCalls++
				return nil
			})
			if err == nil {
				t.Fatal("detectServerEra() error = nil")
			}
			if legacyCalls != 0 {
				t.Fatalf("legacy calls = %d, want 0", legacyCalls)
			}
		})
	}
}

func TestCompatibilityNegotiatorCachesEraPerProcess(t *testing.T) {
	t.Parallel()

	client := &compatibilityClient{calls: []compatibilityCall{{response: compatibilityResponse(1, `{
		"resultType":"complete",
		"supportedVersions":["2026-07-28"],
		"capabilities":{},
		"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"fixture","version":"1"}}
	}`)}}}
	var negotiator compatibilityNegotiator
	for range 2 {
		result, err := negotiator.detect(client, []string{protocol.Version20260728}, nil)
		if err != nil || result.Era != ServerEraModern {
			t.Fatalf("detect() = %#v, %v", result, err)
		}
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d, want one cached probe", len(client.requests))
	}
}

func compatibilityResponse(id int, raw string) protocol.Response {
	return protocol.Response{JSONRPC: "2.0", ID: protocol.ID(id), Result: json.RawMessage(raw)}
}

func compatibilityErrorResponse(id int, rpcError *protocol.Error) protocol.Response {
	return protocol.Response{JSONRPC: "2.0", ID: protocol.ID(id), Error: rpcError}
}
