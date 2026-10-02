package main

import "testing"

// FuzzParseKQL fuzzes the Azure Monitor / Log Analytics KQL query engine, which
// runs over an untrusted query string from the Logs Query data plane. Neither
// the parser nor the evaluator may panic on malformed input.
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
		`AppTraces | where AppRoleName == "a|b" and (Message has "x" or Message !contains @"c:\d")`,
		"AppTraces | summarize n = count(), d = dcount(Message) by bin(TimeGenerated, 1h) | top 3 by n desc",
		"AppTraces | extend L = strlen(Message) * 2 - 1 | order by L asc nulls first | distinct L",
		"AppTraces | where TimeGenerated between (ago(1d) .. now()) | count",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, query string) {
		q, err := parseKQL(query)
		if err != nil {
			return
		}
		set := kqlResultSet{
			columns: kqlTableSchemas["AppTraces"],
			rows: [][]any{
				monitorLogRow{"Message": "x", "TimeGenerated": "2026-01-01T00:00:00Z"}.typedRow(kqlTableSchemas["AppTraces"]),
				monitorLogRow{"Message": "y"}.typedRow(kqlTableSchemas["AppTraces"]),
			},
		}
		b := &kqlBinder{src: query}
		for _, op := range q.ops {
			if set, err = b.apply(op, set); err != nil {
				return
			}
		}
	})
}
