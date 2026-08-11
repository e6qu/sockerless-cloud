package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A smithy.api#pattern is part of the wire contract, and it is where the
// identity-bearing strings are pinned: an ARN, an instance id, a lock token.
// Checking only that a member is a string accepts a value no client could send
// back, which is how a malformed ARN reached every AWS Organizations response.
// These tests hold the check itself — deleting it must fail here rather than in
// some distant suite.

func TestSpecValidatorFlagsAPatternMismatch(t *testing.T) {
	st := testSpecState(t)

	// AWS Organizations types an account's id as exactly twelve digits.
	violations := st.validateJSONTarget(
		"AWSOrganizationsV20161128.DescribeAccount", 200, nil,
		[]byte(`{"Account":{"Id":"not-an-account","Arn":"arn:aws:organizations::123456789012:account/o-sim0000000/123456789012"}}`))
	assertViolations(t, violations, []string{"pattern-mismatch $.Account.Id"})

	if !strings.Contains(violations[0].Detail, `^\d{12}$`) {
		t.Errorf("the violation does not name the pattern it failed: %q", violations[0].Detail)
	}
	if !strings.Contains(violations[0].Detail, `"not-an-account"`) {
		t.Errorf("the violation does not name the offending value: %q", violations[0].Detail)
	}
}

func TestSpecValidatorAcceptsAValueThePatternAdmits(t *testing.T) {
	st := testSpecState(t)
	violations := st.validateJSONTarget(
		"AWSOrganizationsV20161128.DescribeAccount", 200, nil,
		[]byte(`{"Account":{"Id":"123456789012","Arn":"arn:aws:organizations::123456789012:account/o-sim0000000/123456789012"}}`))
	assertViolations(t, violations, nil)
}

// A member whose shape declares no pattern is unconstrained beyond its type, so
// the check must not invent a constraint for it.
func TestSpecValidatorIgnoresAnUnpatternedString(t *testing.T) {
	st := testSpecState(t)
	violations := st.validateJSONTarget(
		"AWSOrganizationsV20161128.DescribeAccount", 200, nil,
		[]byte(`{"Account":{"Id":"123456789012","Name":"anything at all, ~!@#$"}}`))
	assertViolations(t, violations, nil)
}

// Smithy patterns match anywhere in the value unless the expression anchors
// itself, which is what MatchString does. A pattern the Go engine cannot
// compile constrains nothing rather than failing every value.
func TestSpecValidatorPatternSemantics(t *testing.T) {
	st := testSpecState(t)
	idx := st.spec.byShort["AWSOrganizationsV20161128"]
	if idx == nil {
		t.Fatal("the vendored corpus carries no AWS Organizations model")
	}
	shapeID := "com.amazonaws.organizations#AccountId"
	shape, ok := idx.shapes[shapeID]
	if !ok {
		t.Fatalf("%s is not in the model", shapeID)
	}
	re := idx.pattern(shapeID, shape)
	if re == nil {
		t.Fatalf("%s declares a pattern the check did not compile", shapeID)
	}
	if again := idx.pattern(shapeID, shape); again != re {
		t.Error("the compiled pattern is not memoized, so every response recompiles it")
	}

	unpatterned := smithyShapeDef{Type: "string"}
	if idx.pattern("com.amazonaws.organizations#NoSuchShape", unpatterned) != nil {
		t.Error("a shape with no pattern was given one")
	}
	uncompilable := smithyShapeDef{
		Type:   "string",
		Traits: map[string]json.RawMessage{"smithy.api#pattern": json.RawMessage(`"^(?=x)y$"`)},
	}
	if idx.pattern("com.amazonaws.organizations#Uncompilable", uncompilable) != nil {
		t.Error("a pattern Go cannot compile was treated as a constraint rather than as unreadable")
	}
}
