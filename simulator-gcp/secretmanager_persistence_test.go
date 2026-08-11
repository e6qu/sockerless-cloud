package main

import (
	"encoding/json"
	"testing"
)

// TestSMPayloadPersistenceRoundTrip asserts that smPayloadRecord
// preserves its Data field through a json.Marshal + json.Unmarshal
// round-trip. sim.Store uses json.Marshal internally; the Data
// field must be exported so persistence keeps the payload bytes
// across SIM_PERSIST=true restarts.
//
// Regression check: secret payloads must survive a sim restart.
func TestSMPayloadPersistenceRoundTrip(t *testing.T) {
	original := []byte("super-secret-payload-bytes-that-must-survive-restart")
	rec := smPayloadRecord{Data: original}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got smPayloadRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(got.Data) != string(original) {
		t.Errorf("payload dropped on round-trip: got %q, want %q", got.Data, original)
	}
}

// TestSMVersionWireShapeIsPayloadFree asserts the SecretVersion wire
// shape carries no payload bytes. Real GCP's GetSecretVersion +
// ListSecretVersions return metadata only; the raw payload appears
// only in `:access` responses (constructed manually as
// `{"name":..., "payload":{"data":"<base64>"}}`). Leaking payload
// bytes from GetSecretVersion would be both a wire drift and a
// security issue (the SDK caches list responses).
func TestSMVersionWireShapeIsPayloadFree(t *testing.T) {
	v := SecretVersion{
		Name:       "projects/p/secrets/s/versions/1",
		CreateTime: "2026-01-01T00:00:00Z",
		State:      "ENABLED",
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	// No `payload`, `data`, or `secretData` key in the wire response.
	for _, leak := range []string{`"payload"`, `"secretData"`, `"data"`} {
		if containsSubstr(got, leak) {
			t.Errorf("SecretVersion wire shape leaks %s: %s", leak, got)
		}
	}
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
