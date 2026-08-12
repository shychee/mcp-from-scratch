package protocol

import (
	"encoding/json"
	"testing"
)

func TestValidateExtensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		extensions Extensions
		wantError  bool
	}{
		{name: "official", extensions: Extensions{"io.modelcontextprotocol/tasks": json.RawMessage(`{"ttlMs":1000}`)}},
		{name: "third party", extensions: Extensions{"com.example/acme-task": json.RawMessage(`{"mode":"fast"}`)}},
		{name: "empty object", extensions: Extensions{"org.example/feature": json.RawMessage(`{}`)}},
		{name: "missing slash", extensions: Extensions{"com.example.feature": json.RawMessage(`{}`)}, wantError: true},
		{name: "non reverse dns", extensions: Extensions{"example/feature": json.RawMessage(`{}`)}, wantError: true},
		{name: "uppercase", extensions: Extensions{"com.Example/feature": json.RawMessage(`{}`)}, wantError: true},
		{name: "non object settings", extensions: Extensions{"com.example/feature": json.RawMessage(`[]`)}, wantError: true},
		{name: "malformed settings", extensions: Extensions{"com.example/feature": json.RawMessage(`{`)}, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.extensions.Validate()
			if (err != nil) != test.wantError {
				t.Fatalf("Validate() error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}

func TestIntersectExtensionsPreservesServerSettings(t *testing.T) {
	t.Parallel()

	client := Extensions{
		"com.example/shared":      json.RawMessage(`{"client":true}`),
		"com.example/client-only": json.RawMessage(`{"ignored":true}`),
	}
	server := Extensions{
		"com.example/shared":      json.RawMessage(`{"server":{"unknown":[1,2,3]}}`),
		"com.example/server-only": json.RawMessage(`{"ignored":true}`),
	}

	got := IntersectExtensions(client, server)
	if len(got) != 1 || string(got["com.example/shared"]) != `{"server":{"unknown":[1,2,3]}}` {
		t.Fatalf("IntersectExtensions() = %#v", got)
	}

	server["com.example/shared"][0] = '['
	if string(got["com.example/shared"]) != `{"server":{"unknown":[1,2,3]}}` {
		t.Fatal("IntersectExtensions() did not clone opaque settings")
	}
}

func TestIntersectExtensionsCoreFallback(t *testing.T) {
	t.Parallel()

	got := IntersectExtensions(
		Extensions{"com.example/client": json.RawMessage(`{}`)},
		Extensions{"com.example/server": json.RawMessage(`{}`)},
	)
	if len(got) != 0 {
		t.Fatalf("IntersectExtensions() = %#v, want empty core fallback", got)
	}
}
