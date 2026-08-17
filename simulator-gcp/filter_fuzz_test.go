package main

import "testing"

// FuzzGCPParseFilterExpr fuzzes the GCP list `filter` (AIP-160) parser+evaluator.
//
// Two properties beyond "does not panic". The parser always yields a node a
// caller can apply — every list handler calls eval on the result without a nil
// check, so a nil would be a panic at request time rather than a parse error.
// And parsing and evaluating are pure: the same filter over the same resource
// always answers the same way, so a list cannot include a resource on one page
// and drop it on the next.
func FuzzGCPParseFilterExpr(f *testing.F) {
	seeds := []string{
		"",
		"name = foo",
		"name != foo AND state = RUNNING",
		"labels.env : prod",
		"-name = foo",
		"NOT (a = 1 OR b = 2)",
		"a.b.c = 1",
		"count > 5",
		"name : *",
		"a = \"quoted value\"",
		"a = 'single'",
		"\"unterminated",
		"'unterminated\\",
		"(((((((((((",
		"a <= ",
		"!=",
		":",
		"-",
		"- -",
		"a = b c = d",
		"é = 1",
		"\xff\xfe",
		"\\",
		"a = 99999999999999999999999",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	m := map[string]any{
		"name":   "foo",
		"state":  "RUNNING",
		"count":  float64(10),
		"labels": map[string]any{"env": "prod"},
		"a":      map[string]any{"b": map[string]any{"c": float64(1)}},
	}
	f.Fuzz(func(t *testing.T, expr string) {
		node := gcpParseFilterExpr(expr)
		if node == nil {
			t.Fatalf("gcpParseFilterExpr(%q) returned no node for a caller to apply", expr)
		}
		got := node.eval(m)
		if again := node.eval(m); again != got {
			t.Fatalf("evaluating %q twice disagreed: %v then %v", expr, got, again)
		}
		if reparsed := gcpParseFilterExpr(expr).eval(m); reparsed != got {
			t.Fatalf("re-parsing %q changed its verdict: %v then %v", expr, got, reparsed)
		}
	})
}
