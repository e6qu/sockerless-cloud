package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestSMPayloadPersistenceRoundTrip checks smPayloadRecord keeps Data through
// a JSON round-trip, as sim.Store persists it, so payloads survive a
// SIM_PERSIST=true restart.
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

// TestSMVersionWireShapeIsPayloadFree checks the SecretVersion type carries no
// payload: GetSecretVersion and ListSecretVersions return metadata only. It
// walks the declared JSON tags, since a payload-free instance proves nothing
// about a member added later.
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
