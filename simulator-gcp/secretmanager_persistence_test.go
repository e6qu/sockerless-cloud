package main

import (
	"encoding/json"
	"reflect"
	"strings"
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
// The check is over the TYPE, not over one instance: marshalling a
// SecretVersion whose payload members happen to be empty proves nothing,
// because the leak this guards against is a member added to the struct, which
// carries bytes on the first version that has any. Walking the declared JSON
// tags catches that the moment the field appears.
func TestSMVersionWireShapeIsPayloadFree(t *testing.T) {
	assertTypeHasNoJSONKeys(t, reflect.TypeOf(SecretVersion{}), "payload", "secretData", "data")

	// A fully-populated instance is marshalled too, so a member that reaches
	// the wire some other way — an embedded map, a custom marshaller — is
	// caught even though the tag walk cannot see it.
	v := SecretVersion{
		Name:                           "projects/p/secrets/s/versions/1",
		CreateTime:                     "2026-01-01T00:00:00Z",
		State:                          "ENABLED",
		ClientSpecifiedPayloadChecksum: true,
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range []string{`"payload"`, `"secretData"`, `"data"`} {
		if strings.Contains(string(data), leak) {
			t.Errorf("SecretVersion wire shape leaks %s: %s", leak, data)
		}
	}
}
