package main

import "testing"

func TestCWLogPatternMatches(t *testing.T) {
	cases := []struct {
		msg, pat string
		want     bool
	}{
		// Unstructured: AND, exclude, optional(OR), quoted phrase.
		{"ERROR disk full", "ERROR", true},
		{"INFO ok", "ERROR", false},
		{"ERROR disk full", "ERROR full", true},     // all terms (AND)
		{"ERROR disk full", "ERROR missing", false}, // a required term absent
		{"ERROR disk full", "ERROR -full", false},   // exclusion
		{"ERROR disk ok", "ERROR -full", true},
		{"a WARN b", "?ERROR ?WARN", true}, // optional OR group
		{"a INFO b", "?ERROR ?WARN", false},
		{"got status 500 now", `"status 500"`, true}, // quoted phrase substring
		// Structured JSON.
		{`{"level":"ERROR","code":500}`, `{ $.level = "ERROR" }`, true},
		{`{"level":"INFO","code":200}`, `{ $.level = "ERROR" }`, false},
		{`{"code":500}`, `{ $.code >= 500 }`, true},
		{`{"code":499}`, `{ $.code >= 500 }`, false},
		{`{"level":"ERROR","code":500}`, `{ $.level = "ERROR" && $.code = 500 }`, true},
		{`{"level":"ERROR","code":499}`, `{ $.level = "ERROR" && $.code = 500 }`, false},
		{`{"level":"WARN"}`, `{ $.level = "ERROR" || $.level = "WARN" }`, true},
		{`{"svc":"api-gw"}`, `{ $.svc = "api-*" }`, true}, // wildcard
		{`{"a":{"b":7}}`, `{ $.a.b > 5 }`, true},          // nested
		{`{"xs":[1,2,3]}`, `{ $.xs[2] = 3 }`, true},       // array index
		{`not json`, `{ $.x = "y" }`, false},              // structured on non-JSON
		// Keyword tests: IS NULL, NOT EXISTS, IS TRUE, IS FALSE.
		{`{"ip":null}`, `{ $.ip IS NULL }`, true},
		{`{"ip":"10.0.0.1"}`, `{ $.ip IS NULL }`, false},
		{`{}`, `{ $.ip IS NULL }`, false}, // absent is not null
		{`{}`, `{ $.ip NOT EXISTS }`, true},
		{`{"ip":null}`, `{ $.ip NOT EXISTS }`, false},
		{`{"a":{"b":1}}`, `{ $.a.c NOT EXISTS }`, true},
		{`{"admin":true}`, `{ $.admin IS TRUE }`, true},
		{`{"admin":"true"}`, `{ $.admin IS TRUE }`, false}, // a string is not a Boolean
		{`{"admin":false}`, `{ $.admin IS TRUE }`, false},
		{`{"admin":false}`, `{ $.admin IS FALSE }`, true},
		{`{"admin":0}`, `{ $.admin IS FALSE }`, false},
		{`{"admin":true,"ip":null}`, `{ $.admin IS TRUE && ($.ip IS NULL || $.ip NOT EXISTS) }`, true},
		{`{"admin":true}`, `{ $.admin IS TRUE && $.ip IS NULL }`, false},
	}
	for _, tc := range cases {
		c, err := cwCompileLogPattern(tc.pat)
		if err != nil {
			t.Errorf("cwCompileLogPattern(%q) unexpected error: %v", tc.pat, err)
			continue
		}
		if got := c.match(tc.msg); got != tc.want {
			t.Errorf("pattern %q on %q = %v, want %v", tc.pat, tc.msg, got, tc.want)
		}
	}
}

// TestCWLogPatternFailsLoud asserts a malformed structured pattern is a loud
// error (the handler turns it into InvalidParameterException), never a silent
// "matches nothing".
func TestCWLogPatternFailsLoud(t *testing.T) {
	bad := []string{
		`{ $.a = }`,      // comparison missing its value
		`{ $.a = 1 && }`, // trailing operator
		`{ ($.a = 1 }`,   // unbalanced parenthesis
		`{ = 1 }`,        // missing selector
		`{ $.a }`,        // selector with no comparison
		`{ $.a IS }`,     // IS with no operand
		`{ $.a IS EMPTY }`,
		`{ $.a NOT NULL }`,
		`{ $.a NOT }`,
	}
	for _, pat := range bad {
		if _, err := cwCompileLogPattern(pat); err == nil {
			t.Errorf("cwCompileLogPattern(%q) = nil error, want a malformed-pattern error", pat)
		}
	}
	// A well-formed pattern that simply matches nothing is not an error.
	if _, err := cwCompileLogPattern(`{ $.a = "z" }`); err != nil {
		t.Errorf("well-formed pattern errored: %v", err)
	}
}
