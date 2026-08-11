package main

import "testing"

// FuzzDDBPartiQLParse feeds arbitrary statement strings (with a small set of
// positional parameters) to the PartiQL parser entrypoint. The parser must never
// panic, stack-overflow, or hang regardless of input — sim.Scanner makes the
// lexer index-safe and sim.ParseGuard bounds recursion/work. A malformed
// statement is expected to return an error, not crash.
func FuzzDDBPartiQLParse(f *testing.F) {
	seeds := []string{
		`SELECT * FROM "t" WHERE pk = 'a'`,
		`SELECT a, b FROM "t"."idx" WHERE pk = ? AND sk > 3 ORDER BY sk DESC`,
		`INSERT INTO "t" VALUE {'pk': 'a', 'n': 1, 'm': {'x': true}, 'l': [1, 'two', null]}`,
		`UPDATE "t" SET a = 1, b = 'x' REMOVE c WHERE pk = 'a' RETURNING ALL NEW *`,
		`DELETE FROM "t" WHERE pk = ? RETURNING ALL OLD *`,
		`SELECT * FROM t WHERE a BETWEEN 1 AND 9 AND b IN ('x','y') AND NOT contains(c, 'z')`,
		`SELECT * FROM t WHERE attribute_exists(a) AND b IS MISSING AND c IS NOT MISSING`,
		`SELECT * FROM t WHERE begins_with(name, 'pre') OR attribute_type(x, 'S')`,
		``,
		`(((((((((((`,
		`{{{{{{{{{{{{`,
		`SELECT ' '' ' FROM "a""b"`,
		`UPDATE`,
		`SELECT * FROM`,
		`INSERT INTO t VALUE`,
		`?????`,
		`SELECT * FROM t WHERE a = 1e999999999`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	// A couple of small parameter fixtures used for every input; ? placeholders
	// bind positionally from these.
	params := []map[string]any{
		{"S": "a"},
		{"N": "3"},
		{"BOOL": true},
	}
	f.Fuzz(func(t *testing.T, statement string) {
		// Must not panic. The result (stmt or error) is irrelevant to fuzzing —
		// only the absence of a crash matters.
		_, _ = parsePartiQL(statement, params)
	})
}
