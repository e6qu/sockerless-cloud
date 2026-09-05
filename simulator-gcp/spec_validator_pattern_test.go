package main

import (
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A Discovery schema's `pattern` is part of the contract, and it is where the
// identity-bearing strings are pinned: a resource name, a fingerprint, an
// address. Checking only that a member is a string accepts a value no client
// could send back. These tests hold the check itself, so deleting it fails here
// rather than going unnoticed because the simulator happens to be clean today.

// gcpPatternSchema is the shape of a Google Compute Engine resource name, taken
// verbatim from the vendored Discovery documents.
const gcpPatternSchema = `[a-z](?:[-a-z0-9]{0,61}[a-z0-9])?`

func gcpStringSchema(pattern string) *discoverySchema {
	return &discoverySchema{Type: "string", Pattern: pattern}
}

func gcpValidate(t *testing.T, sch *discoverySchema, value any) []sim.SpecViolation {
	t.Helper()
	doc := &discoverySpecDoc{file: "test", schemas: map[string]*discoverySchema{}}
	var out []sim.SpecViolation
	validateDiscoveryValue(doc, "test.op", sch, "TestSchema", "$.name", value, &out)
	return out
}

func TestDiscoveryValidatorFlagsAPatternMismatch(t *testing.T) {
	violations := gcpValidate(t, gcpStringSchema(gcpPatternSchema), "Not-A-Valid-Name")
	if len(violations) != 1 {
		t.Fatalf("got %d violation(s), want 1: %+v", len(violations), violations)
	}
	if violations[0].Kind != "pattern-mismatch" {
		t.Errorf("kind = %q, want pattern-mismatch", violations[0].Kind)
	}
	if !strings.Contains(violations[0].Detail, gcpPatternSchema) {
		t.Errorf("the violation does not name the pattern it failed: %q", violations[0].Detail)
	}
	if !strings.Contains(violations[0].Detail, `"Not-A-Valid-Name"`) {
		t.Errorf("the violation does not name the offending value: %q", violations[0].Detail)
	}
}

func TestDiscoveryValidatorAcceptsAValueThePatternAdmits(t *testing.T) {
	if v := gcpValidate(t, gcpStringSchema(gcpPatternSchema), "instance-1"); len(v) != 0 {
		t.Errorf("a valid name was reported: %+v", v)
	}
}

// The anchoring is the whole point on Discovery: its patterns are written
// without ^ and $ but describe an entire value, so a substring match would
// admit any text that merely contains one. An address pattern is the clearest
// case — "garbage 10.0.0.1 garbage" is not an address.
func TestDiscoveryValidatorAnchorsThePattern(t *testing.T) {
	address := `[0-9]{1,3}(?:\.[0-9]{1,3}){3}`
	if v := gcpValidate(t, gcpStringSchema(address), "10.0.0.1"); len(v) != 0 {
		t.Fatalf("a valid address was reported: %+v", v)
	}
	v := gcpValidate(t, gcpStringSchema(address), "garbage 10.0.0.1 garbage")
	if len(v) != 1 {
		t.Fatalf("a value merely containing an address was accepted: %+v", v)
	}
}

// A schema with no pattern is unconstrained beyond its type, and a pattern the
// Go engine cannot compile constrains nothing rather than failing every value.
func TestDiscoveryValidatorIgnoresWhatItCannotRead(t *testing.T) {
	if v := gcpValidate(t, gcpStringSchema(""), "anything at all ~!@#"); len(v) != 0 {
		t.Errorf("an unpatterned string was reported: %+v", v)
	}
	// RE2 has no lookahead.
	if v := gcpValidate(t, gcpStringSchema(`(?=x)y`), "whatever"); len(v) != 0 {
		t.Errorf("an uncompilable pattern was treated as a constraint: %+v", v)
	}
}

// The compiled expression is memoized, because the validator re-reaches the
// same schemas on every response.
func TestDiscoveryValidatorMemoizesTheCompiledPattern(t *testing.T) {
	sch := gcpStringSchema(gcpPatternSchema)
	first := sch.patternRE()
	if first == nil {
		t.Fatal("the declared pattern did not compile")
	}
	if again := sch.patternRE(); again != first {
		t.Error("the compiled pattern is not memoized, so every response recompiles it")
	}
}
