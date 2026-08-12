package simulator

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReadJSONCapsBody verifies ReadJSON rejects an oversized body instead of
// reading it unbounded into memory.
func TestReadJSONCapsBody(t *testing.T) {
	// A body just over the cap: a valid-ish JSON whitespace-padded blob.
	big := strings.Repeat(" ", maxQueryJSONBody+10) + "{}"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	var v map[string]any
	if err := ReadJSON(req, &v); err == nil {
		t.Fatal("expected ReadJSON to reject a body over the cap")
	}

	// A small body still decodes.
	req2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"k":"v"}`))
	var v2 map[string]any
	if err := ReadJSON(req2, &v2); err != nil {
		t.Fatalf("small body should decode: %v", err)
	}
	if v2["k"] != "v" {
		t.Errorf("decode mismatch: %v", v2)
	}
}
