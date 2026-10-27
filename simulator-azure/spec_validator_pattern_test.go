package main

import (
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A schema's `pattern` pins the identity-bearing strings; checking only that a
// member is a string accepts a value no client could send back. These hold the
// check itself, so removing it fails here.

func azureValidateString(t *testing.T, pattern string, value any) []sim.SpecViolation {
	t.Helper()
	idx := &azureSpecIndex{docs: map[string]*azureSpecDoc{}, subtypes: map[string]azureRefSchema{}}
	doc := &azureSpecDoc{file: "test.swagger.json"}
	schema := map[string]any{"type": "string"}
	if pattern != "" {
		schema["pattern"] = pattern
	}
	var out []sim.SpecViolation
	idx.validate("PUT /test", azureRefSchema{doc: doc, s: schema}, "$.name", value, false, &out)
	return out
}

// The pattern most common in the vendored corpus, verbatim.
const azureNamePattern = `^[a-zA-Z0-9]+$`

func TestAzureValidatorFlagsAPatternMismatch(t *testing.T) {
	violations := azureValidateString(t, azureNamePattern, "not a valid name")
	if len(violations) != 1 {
		t.Fatalf("got %d violation(s), want 1: %+v", len(violations), violations)
	}
	if violations[0].Kind != "pattern-mismatch" {
		t.Errorf("kind = %q, want pattern-mismatch", violations[0].Kind)
	}
	if !strings.Contains(violations[0].Detail, azureNamePattern) {
		t.Errorf("the violation does not name the pattern it failed: %q", violations[0].Detail)
	}
	if !strings.Contains(violations[0].Detail, `"not a valid name"`) {
		t.Errorf("the violation does not name the offending value: %q", violations[0].Detail)
	}
}

func TestAzureValidatorAcceptsAValueThePatternAdmits(t *testing.T) {
	if v := azureValidateString(t, azureNamePattern, "account1"); len(v) != 0 {
		t.Errorf("a valid name was reported: %+v", v)
	}
}

// The corpus carries anchored and unanchored patterns, so match as written: a
// leading ^ with no trailing $ constrains only the start.
func TestAzureValidatorMatchesThePatternAsWritten(t *testing.T) {
	prefixOnly := `^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*`
	if v := azureValidateString(t, prefixOnly, "abc-def!!"); len(v) != 0 {
		t.Errorf("a half-anchored pattern was treated as fully anchored: %+v", v)
	}
	if v := azureValidateString(t, prefixOnly, "!!abc"); len(v) != 1 {
		t.Errorf("a value violating the anchored start was accepted: %+v", v)
	}
}

// A pattern RE2 cannot compile constrains nothing rather than failing every
// value.
func TestAzureValidatorIgnoresWhatItCannotRead(t *testing.T) {
	if v := azureValidateString(t, "", "anything at all ~!@#"); len(v) != 0 {
		t.Errorf("an unpatterned string was reported: %+v", v)
	}
	// RE2 has no lookahead.
	if v := azureValidateString(t, `(?=x)y`, "whatever"); len(v) != 0 {
		t.Errorf("an uncompilable pattern was treated as a constraint: %+v", v)
	}
}

func TestAzureValidatorMemoizesTheCompiledPattern(t *testing.T) {
	first := azureCompiledPattern(azureNamePattern)
	if first == nil {
		t.Fatal("the declared pattern did not compile")
	}
	if again := azureCompiledPattern(azureNamePattern); again != first {
		t.Error("the compiled pattern is not memoized, so every response recompiles it")
	}
	if azureCompiledPattern("") != nil {
		t.Error("an empty pattern produced a matcher")
	}
}
