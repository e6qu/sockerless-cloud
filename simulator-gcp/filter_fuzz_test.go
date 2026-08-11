package main

import "testing"

// FuzzGCPParseFilterExpr fuzzes the GCP list `filter` (AIP-160) parser+evaluator.
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
		if node != nil {
			_ = node.eval(m)
		}
	})
}
