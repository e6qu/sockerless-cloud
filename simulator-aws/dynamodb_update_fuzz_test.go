package main

import "testing"

// FuzzDDBUpdateExpression fuzzes the DynamoDB UpdateExpression parser+applier
// (SET/REMOVE/ADD/DELETE) with hostile expressions against well-formed and
// malformed items. It must return an error or apply, never panic, hang, or OOM.
func FuzzDDBUpdateExpression(f *testing.F) {
	seeds := []string{
		"",
		"SET a = :v",
		"SET a = b + :v",
		"SET a = :v, b = :w",
		"SET a.b[0] = :v",
		"SET a = list_append(a, :v)",
		"SET a = if_not_exists(a, :v)",
		"REMOVE a",
		"REMOVE a, b.c[1]",
		"ADD count :n",
		"ADD tags :s",
		"DELETE tags :s",
		"SET a = :v ADD b :n REMOVE c DELETE d :s",
		"SET",
		"SET =",
		"SET a =",
		"ADD",
		"DELETE",
		"SET a = (((((",
		"SET a[999999999999] = :v",
		"SET #x = :v",
		"set a = :v add b :n",
		"SET a = :missing",
		"\xff\xfe",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	values := map[string]any{
		":v": map[string]any{"S": "x"},
		":w": map[string]any{"N": "2"},
		":n": map[string]any{"N": "1"},
		":s": map[string]any{"SS": []any{"a", "b"}},
	}
	names := map[string]string{"#x": "a"}
	mkItems := func() []map[string]any {
		return []map[string]any{
			{"a": map[string]any{"S": "hello"}, "count": map[string]any{"N": "5"}, "tags": map[string]any{"SS": []any{"a"}}},
			{"a": "raw-not-a-map", "b": float64(7), "c": []any{1, 2}},
			{},
		}
	}
	f.Fuzz(func(t *testing.T, expr string) {
		for _, item := range mkItems() {
			_ = ddbApplyUpdateExpression(item, expr, names, values)
		}
	})
}
