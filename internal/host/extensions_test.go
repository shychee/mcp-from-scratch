package host

import (
	"encoding/json"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

func TestNegotiatedExtensionsFromDiscovery(t *testing.T) {
	t.Parallel()

	offered := protocol.Extensions{
		"io.modelcontextprotocol/tasks": json.RawMessage(`{"client":true}`),
		"com.example/client-only":       json.RawMessage(`{}`),
	}
	discovery := json.RawMessage(`{
		"capabilities":{"extensions":{
			"io.modelcontextprotocol/tasks":{"ttlMs":1000},
			"com.example/server-only":{"x":1}
		}}
	}`)

	got, err := negotiatedExtensionsFromDiscovery(offered, discovery)
	if err != nil {
		t.Fatalf("negotiatedExtensionsFromDiscovery() error = %v", err)
	}
	if len(got) != 1 || string(got["io.modelcontextprotocol/tasks"]) != `{"ttlMs":1000}` {
		t.Fatalf("negotiated extensions = %#v", got)
	}
}

func TestNegotiatedExtensionsFromDiscoveryRejectsMalformedAdvertisement(t *testing.T) {
	t.Parallel()

	_, err := negotiatedExtensionsFromDiscovery(
		protocol.Extensions{"com.example/shared": json.RawMessage(`{}`)},
		json.RawMessage(`{"capabilities":{"extensions":{"bad":{"x":1}}}}`),
	)
	if err == nil {
		t.Fatal("negotiatedExtensionsFromDiscovery() error = nil")
	}
}

func TestCapabilitiesWithExtensionsPreservesUnknownSettings(t *testing.T) {
	t.Parallel()

	base := map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}
	extensions := protocol.Extensions{"com.example/unknown": json.RawMessage(`{"nested":{"flag":true}}`)}
	got, err := capabilitiesWithExtensions(base, extensions)
	if err != nil {
		t.Fatalf("capabilitiesWithExtensions() error = %v", err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Extensions map[string]json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded.Extensions["com.example/unknown"]) != `{"nested":{"flag":true}}` {
		t.Fatalf("extensions = %s", decoded.Extensions["com.example/unknown"])
	}
	if _, ok := base["extensions"]; ok {
		t.Fatal("capabilitiesWithExtensions() mutated base capabilities")
	}
}
