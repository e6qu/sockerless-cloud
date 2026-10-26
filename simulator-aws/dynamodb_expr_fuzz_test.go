package main

import "testing"

// A malformed or hostile expression must never panic.
func FuzzDDBEvalExpr(f *testing.F) {
	seeds := []string{
		"",
		"a = :v",
		"attribute_exists(a)",
		"attribute_not_exists(#n)",
		"begins_with(a, :p)",
		"contains(a, :v) AND size(b) > :n",
		"a BETWEEN :lo AND :hi",
		"a IN (:x, :y, :z)",
		"NOT a = :v OR (b < :c AND c >= :d)",
		"size(",
		"begins_with(",
		"a BETWEEN",
		"a IN (",
		"(((((((((((",
		"a.b[0].c = :v",
		"a[",
		"a[999999999999999999999999] = :v",
		"\\",
		"é = :v",
		"\xff\xfe = :v",
		"a <",
		"<=",
		",",
		"NOT NOT NOT",
		"size(a) = size(b)",
		// A malformed PutItem can store a bare scalar or list as an attribute value.
		"contains(c, :v)",
		"contains(d, :v)",
		"contains(e, :v)",
		"begins_with(c, :p)",
		"attribute_type(d, :v)",
		"contains(a.b, :v)",
		"contains(b, :v)",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	items := []map[string]any{
		{
			"a": map[string]any{"S": "hello"},
			"b": map[string]any{"L": []any{map[string]any{"N": "1"}}},
		},
		{
			"a": map[string]any{"S": "hello"},
			"c": "raw-string-not-a-map",
			"d": float64(42),
			"e": []any{"x", "y"},
			"b": nil,
		},
		{
			"a": []any{map[string]any{"S": "x"}},
			"b": map[string]any{"M": "not-a-map"},
		},
	}
	names := map[string]string{"#n": "a"}
	values := map[string]any{":v": map[string]any{"S": "hello"}, ":p": map[string]any{"S": "he"}, ":n": map[string]any{"N": "0"}}
	f.Fuzz(func(t *testing.T, expr string) {
		for _, item := range items {
			_, _ = ddbEvalExpr(item, true, expr, names, values)
			_, _ = ddbEvalExpr(item, false, expr, names, values)
		}
	})
}
