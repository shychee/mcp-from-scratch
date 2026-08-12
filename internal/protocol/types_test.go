package protocol

import (
	"encoding/json"
	"testing"
)

func TestRequestIDSupportsStringAndIntegerJSONValues(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		text bool
	}{
		{name: "integer", raw: `42`, want: "42"},
		{name: "string", raw: `"request-42"`, want: "request-42", text: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var id RequestID
			if err := json.Unmarshal([]byte(tt.raw), &id); err != nil {
				t.Fatalf("unmarshal RequestID: %v", err)
			}
			if id.String() != tt.want || id.IsString() != tt.text {
				t.Fatalf("id = %#v, want %q (string=%v)", id, tt.want, tt.text)
			}
			encoded, err := json.Marshal(id)
			if err != nil {
				t.Fatalf("marshal RequestID: %v", err)
			}
			if string(encoded) != tt.raw {
				t.Fatalf("encoded ID = %s, want %s", encoded, tt.raw)
			}
		})
	}
}

func TestRequestIDIsComparableForMapKeys(t *testing.T) {
	number := *ID(7)
	text := *StringID("7")
	values := map[RequestID]string{number: "number", text: "string"}
	if values[number] != "number" || values[text] != "string" {
		t.Fatalf("request IDs are not distinct comparable keys: %#v", values)
	}
}

func TestRequestIDRejectsNonIntegerNumbers(t *testing.T) {
	var id RequestID
	if err := json.Unmarshal([]byte(`1.5`), &id); err == nil {
		t.Fatal("unmarshal fractional RequestID error = nil, want error")
	}
}

func TestRequestDistinguishesNullIDFromNotification(t *testing.T) {
	var nullID Request
	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":null,"method":"tools/list"}`), &nullID); err != nil {
		t.Fatalf("unmarshal null ID request: %v", err)
	}
	if rpcError := ValidateRequest(nullID); rpcError == nil || rpcError.Code != CodeInvalidRequest {
		t.Fatalf("ValidateRequest(null ID) = %#v, want invalid request", rpcError)
	}

	var notification Request
	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","method":"tools/list"}`), &notification); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}
	if rpcError := ValidateRequest(notification); rpcError != nil {
		t.Fatalf("ValidateRequest(notification) = %#v, want nil", rpcError)
	}
}
