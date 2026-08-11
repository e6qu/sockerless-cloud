package main

import (
	"encoding/json"
	"testing"
)

// TestPrivateEndpointAlwaysReportsBothConnectionCollections pins a shape the
// HashiCorp provider depends on: it walks both privateLinkServiceConnections
// and manualPrivateLinkServiceConnections on delete, dereferencing each
// without checking for absence. The service always reports both — empty when
// unused — so a document that omits either faults the provider rather than
// returning an error a caller could act on.
func TestPrivateEndpointAlwaysReportsBothConnectionCollections(t *testing.T) {
	var pe PrivateEndpoint
	projectPrivateEndpoint(&pe)

	raw, err := json.Marshal(pe.Properties)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, member := range []string{"privateLinkServiceConnections", "manualPrivateLinkServiceConnections"} {
		value, present := doc[member]
		if !present {
			t.Fatalf("%s must always be reported; a client that walks it without a presence check faults", member)
		}
		if value == nil {
			t.Fatalf("%s must be an empty array, never null", member)
		}
		if _, ok := value.([]any); !ok {
			t.Fatalf("%s must be an array, got %T", member, value)
		}
	}
}
