package main

import "testing"

// TestPartiQLInListTakesEitherDelimiter proves the IN list form AWS documents
// parses. The PartiQL SELECT reference writes it in brackets -- "WHERE OrderID
// IN [100, 300, 234]" -- and this parser accepted only parentheses, so a client
// sending the documented statement was answered with a ValidationException.
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

	// A list that closes with the other delimiter is not a statement AWS
	// accepts either, so it must still be refused rather than guessed at.
	for _, statement := range []string{
		`SELECT * FROM "orders" WHERE "id" IN [100, 300)`,
		`SELECT * FROM "orders" WHERE "id" IN (100, 300]`,
	} {
		if _, err := parsePartiQL(statement, nil); err == nil {
			t.Errorf("parse %s: accepted mismatched delimiters", statement)
		}
	}
}
