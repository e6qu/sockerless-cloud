package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// These tests hold the supplement mechanism's two safety properties: a
// supplemented member is type-checked like any other, and a supplement that
// outlived its gap fails loudly rather than shadowing the published document.

// writeSupplementedDoc lays down a one-schema Discovery document and, when
// supplement is non-empty, its supplement beside it, then loads the pair.
func writeSupplementedDoc(t *testing.T, doc map[string]any, supplement string) (*discoverySpecDoc, error) {
	t.Helper()
	dir := t.TempDir()

	f, err := os.Create(filepath.Join(dir, "sample-v1.discovery.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if err := json.NewEncoder(gz).Encode(doc); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if supplement != "" {
		if err := os.WriteFile(filepath.Join(dir, "sample-v1.supplement.json"), []byte(supplement), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var raw struct {
		Schemas map[string]*discoverySchema `json:"schemas"`
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	loaded := &discoverySpecDoc{file: "sample-v1.discovery.json.gz", schemas: raw.Schemas}
	return loaded, applyDiscoverySupplement(dir, loaded)
}

func sampleScalingDoc() map[string]any {
	return map[string]any{
		"basePath": "/v2/",
		"schemas": map[string]any{
			"Scaling": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"manualInstanceCount": map[string]any{"type": "integer", "format": "int32"},
				},
			},
		},
	}
}

const sampleSupplement = `{
  "schemas": {
    "Scaling": {
      "properties": {
        "maxInstanceCount": {"type": "integer", "format": "int32"}
      }
    }
  }
}`

func TestSpecSupplement_DeclaresTheMemberRatherThanTolerating(t *testing.T) {
	doc, err := writeSupplementedDoc(t, sampleScalingDoc(), sampleSupplement)
	if err != nil {
		t.Fatalf("loading a supplemented document: %v", err)
	}

	// Without the supplement the member is unknown, which is the state an
	// allowlist entry would freeze in place.
	bare, err := writeSupplementedDoc(t, sampleScalingDoc(), "")
	if err != nil {
		t.Fatal(err)
	}
	var unsupplemented []sim.SpecViolation
	validateDiscoveryValue(bare, "test.op", &discoverySchema{Ref: "Scaling"}, "Scaling", "$",
		map[string]any{"maxInstanceCount": float64(4)}, &unsupplemented)
	if len(unsupplemented) != 1 || unsupplemented[0].Kind != "unknown-field" {
		t.Fatalf("without a supplement the member must be unknown, got %+v", unsupplemented)
	}

	// With it, a well-typed value passes.
	var clean []sim.SpecViolation
	validateDiscoveryValue(doc, "test.op", &discoverySchema{Ref: "Scaling"}, "Scaling", "$",
		map[string]any{"maxInstanceCount": float64(4)}, &clean)
	if len(clean) != 0 {
		t.Fatalf("a supplemented member with a well-typed value is not a violation, got %+v", clean)
	}

	// A wrong-typed value is still caught, which an allowlist entry would accept.
	var wrong []sim.SpecViolation
	validateDiscoveryValue(doc, "test.op", &discoverySchema{Ref: "Scaling"}, "Scaling", "$",
		map[string]any{"maxInstanceCount": "four"}, &wrong)
	if len(wrong) != 1 || wrong[0].Kind != "type-mismatch" {
		t.Fatalf("a supplemented member must be type-checked, got %+v", wrong)
	}
}

func TestSpecSupplement_RefusesToShadowThePublishedDocument(t *testing.T) {
	// Once the document declares the member, the supplement entry must go.
	published := sampleScalingDoc()
	published["schemas"].(map[string]any)["Scaling"].(map[string]any)["properties"].(map[string]any)["maxInstanceCount"] =
		map[string]any{"type": "integer", "format": "int32"}

	_, err := writeSupplementedDoc(t, published, sampleSupplement)
	if err == nil {
		t.Fatal("supplementing a member the document declares must fail")
	}
	if !strings.Contains(err.Error(), "delete the supplement entry") {
		t.Fatalf("the failure must say what to do about it, got %v", err)
	}

	// A supplement naming a schema the document does not define is equally a
	// mistake, and equally loud.
	_, err = writeSupplementedDoc(t, sampleScalingDoc(), `{"schemas":{"Absent":{"properties":{"x":{"type":"string"}}}}}`)
	if err == nil || !strings.Contains(err.Error(), "does not define") {
		t.Fatalf("supplementing an undefined schema must fail, got %v", err)
	}
}
