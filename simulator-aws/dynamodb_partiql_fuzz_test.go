package main

import "testing"

// The parser must never panic, overflow or hang; a malformed statement returns
// an error.
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
	params := []map[string]any{
		{"S": "a"},
		{"N": "3"},
		{"BOOL": true},
	}
	f.Fuzz(func(t *testing.T, statement string) {
		_, _ = parsePartiQL(statement, params)
	})
}
