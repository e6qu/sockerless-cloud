package main

import "testing"

func cwRunQuery(t *testing.T, q string, recs []cwInsightsRecord, limit int) [][]map[string]any {
	t.Helper()
	out, err := cwRunInsightsQuery(q, recs, limit)
	if err != nil {
		t.Fatalf("cwRunInsightsQuery(%q): %v", q, err)
	}
	return out
}

func cwRowVal(row []map[string]any, field string) string {
	for _, c := range row {
		if c["field"] == field {
			if s, ok := c["value"].(string); ok {
				return s
			}
		}
	}
	return ""
}

func TestCWInsightsQuery(t *testing.T) {
	recs := []cwInsightsRecord{
		{"@message": "a", "@__ts": "3000", "level": "ERROR", "code": "500", "svc": "api"},
		{"@message": "b", "@__ts": "2000", "level": "INFO", "code": "200", "svc": "api"},
		{"@message": "c", "@__ts": "1000", "level": "ERROR", "code": "503", "svc": "web"},
	}

	// filter + default newest-first ordering
	out := cwRunQuery(t, `fields @message | filter level = "ERROR"`, append([]cwInsightsRecord(nil), recs...), 0)
	if len(out) != 2 || cwRowVal(out[0], "@message") != "a" || cwRowVal(out[1], "@message") != "c" {
		t.Fatalf("filter ERROR → %v", out)
	}

	// numeric comparison
	out = cwRunQuery(t, `filter code >= 500`, append([]cwInsightsRecord(nil), recs...), 0)
	if len(out) != 2 {
		t.Fatalf("filter code>=500 → %d rows", len(out))
	}

	// in
	out = cwRunQuery(t, `filter svc in ["web"]`, append([]cwInsightsRecord(nil), recs...), 0)
	if len(out) != 1 || cwRowVal(out[0], "@message") != "c" {
		t.Fatalf("filter svc in [web] → %v", out)
	}

	// stats count() by + sort desc
	out = cwRunQuery(t, `stats count() as n by level | sort n desc`, append([]cwInsightsRecord(nil), recs...), 0)
	if len(out) != 2 {
		t.Fatalf("stats by level → %d rows", len(out))
	}
	if cwRowVal(out[0], "level") != "ERROR" || cwRowVal(out[0], "n") != "2" {
		t.Fatalf("top group should be ERROR=2; got %v", out[0])
	}

	// stats with grouping over a filtered set
	out = cwRunQuery(t, `filter level = "ERROR" | stats count() by svc`, append([]cwInsightsRecord(nil), recs...), 0)
	if len(out) != 2 {
		t.Fatalf("stats count by svc (ERROR only) → %d rows", len(out))
	}

	// limit
	out = cwRunQuery(t, `fields @message | limit 1`, append([]cwInsightsRecord(nil), recs...), 0)
	if len(out) != 1 {
		t.Fatalf("limit 1 → %d rows", len(out))
	}

	// AND/OR/NOT + parens
	out = cwRunQuery(t, `filter (level = "ERROR" or level = "INFO") and not svc = "web"`, append([]cwInsightsRecord(nil), recs...), 0)
	if len(out) != 2 {
		t.Fatalf("boolean filter → %d rows", len(out))
	}
}
