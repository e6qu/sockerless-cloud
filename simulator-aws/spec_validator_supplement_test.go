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

// A supplement corrects a shape whose declared pattern is stricter than the
// service it describes. It exists so the member stays validated: listing it as
// an accepted violation instead would stop the value being checked at all.
// These tests hold the two properties that make the mechanism safe — the
// corrected pattern is applied and still enforced, and a correction written
// against text the model no longer declares fails loudly.

const supplementModelPattern = `^[\w-_]*$`

func writeSupplementedModel(t *testing.T, declaredPattern, supplement string) (map[string]smithyShapeDef, error) {
	t.Helper()
	dir := t.TempDir()
	model := filepath.Join(dir, "sample.smithy.json.gz")

	shapes := map[string]any{
		"com.amazonaws.sample#ResourceType": map[string]any{
			"type":   "string",
			"traits": map[string]any{"smithy.api#pattern": declaredPattern},
		},
	}
	f, err := os.Create(model)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if err := json.NewEncoder(gz).Encode(map[string]any{"shapes": shapes}); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if supplement != "" {
		if err := os.WriteFile(filepath.Join(dir, "sample.supplement.json"), []byte(supplement), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var doc struct {
		Shapes map[string]smithyShapeDef `json:"shapes"`
	}
	encoded, err := json.Marshal(map[string]any{"shapes": shapes})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Shapes, applySmithySupplement(model, doc.Shapes, map[string][]int{})
}

func sampleCorrection(replaces string) string {
	body, err := json.Marshal(map[string]any{
		"shapes": map[string]any{
			"com.amazonaws.sample#ResourceType": map[string]any{
				"pattern": map[string]any{"replaces": replaces, "with": `^[\w\-_:]*$`},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func TestSmithySupplement_CorrectsThePatternAndKeepsEnforcingIt(t *testing.T) {
	shapes, err := writeSupplementedModel(t, supplementModelPattern, sampleCorrection(supplementModelPattern))
	if err != nil {
		t.Fatalf("applying the correction: %v", err)
	}
	idx := &smithyModelIndex{shapes: shapes}
	const id = "com.amazonaws.sample#ResourceType"

	// The value the service really returns now matches, where the declared
	// pattern rejected it.
	validate := func(value string) []sim.SpecViolation {
		var out []sim.SpecViolation
		validateSmithyValue(idx, "Sample.Op", id, "$.resourceType", value, &out)
		return out
	}
	if corrected := validate("AWS::WAFv2::WebACL"); len(corrected) != 0 {
		t.Fatalf("the corrected pattern must admit what the service returns, got %+v", corrected)
	}

	// And the pattern is still a pattern: a value neither the declared nor the
	// corrected form admits is still reported. An allowlist entry would have
	// accepted this.
	if bad := validate("has spaces and $igns"); len(bad) != 1 || bad[0].Kind != "pattern-mismatch" {
		t.Fatalf("a corrected shape must still enforce its pattern, got %+v", bad)
	}
}

func TestSmithySupplement_RefusesACorrectionForTextTheModelNoLongerDeclares(t *testing.T) {
	// Upstream widened the pattern: the correction is now written against text
	// that is not there, so it fails rather than being applied blind.
	_, err := writeSupplementedModel(t, `^[\w\-_:]*$`, sampleCorrection(supplementModelPattern))
	if err == nil {
		t.Fatal("a correction pinned to text the model no longer declares must fail")
	}
	if !strings.Contains(err.Error(), "recheck whether it is still needed") {
		t.Fatalf("the failure must say what to do about it, got %v", err)
	}

	// A correction naming a shape the model does not define is equally loud.
	_, err = writeSupplementedModel(t, supplementModelPattern,
		`{"shapes":{"com.amazonaws.sample#Absent":{"pattern":{"replaces":"a","with":"b"}}}}`)
	if err == nil || !strings.Contains(err.Error(), "does not define") {
		t.Fatalf("correcting an undefined shape must fail, got %v", err)
	}
}
