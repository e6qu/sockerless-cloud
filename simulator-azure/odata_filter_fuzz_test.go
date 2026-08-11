package main

import "testing"

// FuzzAzureParseODataFilter fuzzes the Azure ARM `$filter` (OData) parser+evaluator.
func FuzzAzureParseODataFilter(f *testing.F) {
	seeds := []string{
		"",
		"name eq 'foo'",
		"name ne 'foo' and state eq 'Running'",
		"startswith(name, 'foo')",
		"endswith(name, 'bar')",
		"contains(name, 'oo')",
		"substringof('oo', name)",
		"not (a eq '1' or b eq '2')",
		"properties/state eq 'Running'",
		"count gt 5",
		"startswith(",
		"substringof(",
		"name eq",
		"'unterminated",
		"'it''s escaped'",
		"(((((((((((",
		"name eq 'foo",
		",",
		"é eq '1'",
		"\xff\xfe",
		"\\",
		"count gt 99999999999999999999999",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	m := map[string]any{
		"name":       "foo",
		"count":      float64(10),
		"properties": map[string]any{"state": "Running"},
	}
	f.Fuzz(func(t *testing.T, expr string) {
		node, err := azureParseODataFilter(expr)
		if err == nil && node != nil {
			_ = node.eval(m)
		}
	})
}
