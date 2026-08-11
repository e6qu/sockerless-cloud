package main

import "testing"

// FuzzParseKQL fuzzes the Azure Monitor / Log Analytics KQL query parser, which
// runs over an untrusted query string from the Logs Query data plane. Neither
// parseKQL nor matchesFilters may panic on malformed input.
func FuzzParseKQL(f *testing.F) {
	seeds := []string{
		"",
		"AppTraces",
		"AppTraces | where Message == 'x'",
		"T | where TimeGenerated > datetime(2026-01-01)",
		"T | take 5 | project a, b, c",
		"T | limit 99999999999999999999",
		"| where",
		"where ==",
		"T | where >=",
		"T | where == ==",
		"|||||||",
		"T | where datetime(",
		"T | project ,,,,",
		"\xff\xfe | where x == 'y'",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, query string) {
		q := parseKQL(query)
		row := monitorLogRow{"Message": "x", "TimeGenerated": "2026-01-01T00:00:00Z"}
		_ = row.matchesFilters(q.Filters)
	})
}
