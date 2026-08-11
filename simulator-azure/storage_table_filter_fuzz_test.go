package main

import (
	"encoding/json"
	"testing"
)

// FuzzTableEntityFilter fuzzes the Azure Tables Query-Entities $filter path:
// an attacker controls both the stored entity's property JSON and the $filter
// expression. tableEntityFilterMap builds the property map and the OData
// evaluator runs against it — neither may panic on malformed input.
func FuzzTableEntityFilter(f *testing.F) {
	type seed struct {
		props  string
		filter string
	}
	seeds := []seed{
		{`{"Color":"red"}`, "Color eq 'red'"},
		{`{"Count":5}`, "Count gt 3"},
		{`{}`, "PartitionKey eq 'p1'"},
		{`{"x":null}`, "x eq 'y'"},
		{`{"a":{"b":1}}`, "a/b eq '1'"},
		{`not-json`, "RowKey eq 'r'"},
		{`{"k":"v"`, "k eq 'v'"},
		{`{"k":"v"}`, "((((startswith(k,'v"},
		{`{"k":"v"}`, "substringof('v', k)"},
		{`{"big":1e308}`, "big gt '0'"},
	}
	for _, s := range seeds {
		f.Add(s.props, s.filter)
	}
	f.Fuzz(func(t *testing.T, props, filter string) {
		var raw map[string]json.RawMessage
		// Best-effort: a malformed body yields a nil/partial map, which the
		// handler tolerates — mirror that here.
		_ = json.Unmarshal([]byte(props), &raw)
		e := TableEntity{
			Account:      "acct",
			Table:        "tbl",
			PartitionKey: "p1",
			RowKey:       "r1",
			Properties:   raw,
			Timestamp:    "2026-01-01T00:00:00Z",
		}
		m := tableEntityFilterMap(e)
		node, err := azureParseODataFilter(filter)
		if err == nil && node != nil {
			_ = node.eval(m)
		}
	})
}
