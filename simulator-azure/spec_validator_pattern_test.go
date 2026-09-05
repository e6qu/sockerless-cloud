package main

import (
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A Swagger schema's `pattern` is part of the contract, and it is where the
// identity-bearing strings are pinned: a resource name, a key, a duration.
// Checking only that a member is a string accepts a value no client could send
// back. These tests hold the check itself, so deleting it fails here rather
// than going unnoticed because the simulator happens to be clean today.

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

// Swagger patterns anchor themselves when they mean to, and the corpus carries
// both forms — so the expression is matched as written rather than anchored on
// the author's behalf. A pattern with a leading ^ and no trailing $ constrains
// only the start, which is what its author asked for.
func TestAzureValidatorMatchesThePatternAsWritten(t *testing.T) {
	prefixOnly := `^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*`
	if v := azureValidateString(t, prefixOnly, "abc-def!!"); len(v) != 0 {
		t.Errorf("a half-anchored pattern was treated as fully anchored: %+v", v)
	}
	if v := azureValidateString(t, prefixOnly, "!!abc"); len(v) != 1 {
		t.Errorf("a value violating the anchored start was accepted: %+v", v)
	}
}

// A schema with no pattern is unconstrained beyond its type, and a pattern the
// Go engine cannot compile constrains nothing rather than failing every value.
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
