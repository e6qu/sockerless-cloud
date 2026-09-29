package main

import "testing"

// FuzzGCPParseFilterExpr fuzzes the GCP list `filter` (AIP-160) parser+evaluator.
//
// Two properties beyond "does not panic". The parser yields either a node or
// an error, never neither, so a handler never evaluates a nil node. And
// parsing and evaluating are pure: the same filter over the same resource
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
		node, err := gcpParseFilterExpr(expr)
		if (node == nil) == (err == nil) {
			t.Fatalf("gcpParseFilterExpr(%q) must return exactly one of a node and an error: %v, %v", expr, node, err)
		}
		if err != nil {
			if _, again := gcpParseFilterExpr(expr); again == nil {
				t.Fatalf("re-parsing %q accepted a filter the first parse rejected", expr)
			}
			return
		}
		got := node.Eval(m)
		if again := node.Eval(m); again != got {
			t.Fatalf("evaluating %q twice disagreed: %v then %v", expr, got, again)
		}
		reparsed, err := gcpParseFilterExpr(expr)
		if err != nil || reparsed.Eval(m) != got {
			t.Fatalf("re-parsing %q changed its verdict", expr)
		}
	})
}
