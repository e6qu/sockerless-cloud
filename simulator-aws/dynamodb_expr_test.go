package main

import "testing"

func TestDDBEvalExpr_FullGrammar(t *testing.T) {
	item := map[string]any{
		"PK":   map[string]any{"S": "row1"},
		"name": map[string]any{"S": "alice"},
		"age":  map[string]any{"N": "30"},
		"tags": map[string]any{"L": []any{map[string]any{"S": "x"}, map[string]any{"S": "y"}}},
		"addr": map[string]any{"M": map[string]any{"city": map[string]any{"S": "NYC"}}},
	}
	names := map[string]string{"#n": "name"}
	values := map[string]any{
		":n":    map[string]any{"S": "alice"},
		":x":    map[string]any{"S": "x"},
		":lo":   map[string]any{"N": "20"},
		":hi":   map[string]any{"N": "40"},
		":over": map[string]any{"N": "99"},
		":pre":  map[string]any{"S": "al"},
		":S":    map[string]any{"S": "S"},
		":two":  map[string]any{"N": "2"},
		":nyc":  map[string]any{"S": "NYC"},
	}

	cases := []struct {
		expr string
		want bool
	}{
		{`#n = :n`, true},                                  // alias + equality
		{`name = :x`, false},                               // not equal
		{`age > :lo AND age < :hi`, true},                  // AND + numeric compare
		{`age BETWEEN :lo AND :hi`, true},                  // BETWEEN
		{`age BETWEEN :hi AND :over`, false},               // out of range
		{`name IN (:n, :x)`, true},                         // IN
		{`name IN (:x)`, false},                            // IN miss
		{`attribute_exists(name)`, true},                   // function
		{`attribute_exists(missing)`, false},               // absent
		{`attribute_not_exists(missing)`, true},            // absent
		{`begins_with(name, :pre)`, true},                  // begins_with
		{`contains(tags, :x)`, true},                       // contains on a list
		{`contains(name, :pre)`, true},                     // contains substring
		{`attribute_type(name, :S)`, true},                 // attribute_type
		{`size(tags) = :two`, true},                        // size()
		{`tags[0] = :x`, true},                             // list index path
		{`addr.city = :nyc`, true},                         // nested map path
		{`NOT attribute_exists(missing)`, true},            // NOT
		{`age > :over OR name = :n`, true},                 // OR
		{`(age > :over OR name = :n) AND age > :lo`, true}, // parentheses + precedence
		{`age > :over AND name = :n`, false},               // AND short-circuit false
	}
	for _, tc := range cases {
		got, err := ddbEvalExpr(item, true, tc.expr, names, values)
		if err != nil {
			t.Errorf("ddbEvalExpr(%q) unexpected error: %v", tc.expr, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ddbEvalExpr(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}

	// Condition on a not-yet-existing item: attribute_not_exists(PK) is the
	// classic put-if-absent guard.
	if got, err := ddbEvalExpr(map[string]any{}, false, "attribute_not_exists(PK)", nil, nil); err != nil || !got {
		t.Errorf("attribute_not_exists(PK) must hold for a new item (got %v, err %v)", got, err)
	}
}

// TestDDBEvalExpr_FailsLoud asserts that a malformed expression, or one
// referencing an undefined #name / :value, is a loud error — never a silent
// non-match returning plausible-wrong data.
func TestDDBEvalExpr_FailsLoud(t *testing.T) {
	item := map[string]any{"PK": map[string]any{"S": "row1"}}
	names := map[string]string{"#n": "name"}
	values := map[string]any{":v": map[string]any{"S": "x"}}

	bad := []string{
		`PK =`,                    // dangling comparator
		`PK == :v`,                // not a real operator (trailing token)
		`attribute_exists(`,       // unterminated function
		`begins_with(PK)`,         // missing second argument
		`PK IN :v`,                // IN without parentheses
		`PK IN (:v`,               // unterminated IN list
		`(PK = :v`,                // unbalanced parenthesis
		`PK`,                      // bare path, no comparison
		`PK = :missing`,           // undefined :value reference
		`#undef = :v`,             // undefined #name reference
		`begins_with(#undef, :v)`, // undefined #name inside a function
	}
	for _, expr := range bad {
		if _, err := ddbEvalExpr(item, true, expr, names, values); err == nil {
			t.Errorf("ddbEvalExpr(%q) = nil error, want a loud ValidationException", expr)
		}
	}

	// A well-formed expression whose defined references simply don't match the
	// item is NOT an error — it's a legitimate non-match.
	if got, err := ddbEvalExpr(item, true, `#n = :v`, names, values); err != nil || got {
		t.Errorf("defined-but-non-matching expr: got %v, err %v; want false, nil", got, err)
	}
}
