package main

import "testing"

// AWS documents the IN list in brackets ("WHERE OrderID IN [100, 300, 234]")
// and accepts parentheses too.
func TestPartiQLInListTakesEitherDelimiter(t *testing.T) {
	for _, statement := range []string{
		`SELECT * FROM "orders" WHERE "id" IN [100, 300, 234]`,
		`SELECT * FROM "orders" WHERE "id" IN (100, 300, 234)`,
	} {
		stmt, err := parsePartiQL(statement, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", statement, err)
		}
		in, ok := stmt.Where.(partiQLIn)
		if !ok {
			t.Fatalf("parse %s: where clause is %T, want an IN list", statement, stmt.Where)
		}
		if len(in.vals) != 3 {
			t.Errorf("parse %s: IN list has %d values, want 3", statement, len(in.vals))
		}
	}

	// Mismatched delimiters are refused.
	for _, statement := range []string{
		`SELECT * FROM "orders" WHERE "id" IN [100, 300)`,
		`SELECT * FROM "orders" WHERE "id" IN (100, 300]`,
	} {
		if _, err := parsePartiQL(statement, nil); err == nil {
			t.Errorf("parse %s: accepted mismatched delimiters", statement)
		}
	}
}
