package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// withKQLRows installs a log store holding the given rows for the test's
// duration.
func withKQLRows(t *testing.T, table string, rows ...monitorLogRow) {
	t.Helper()
	saved := monitorLogs
	monitorLogs = sim.MakeStore[[]monitorLogRow](nil, "kql_test_logs")
	t.Cleanup(func() { monitorLogs = saved })
	monitorLogs.Put("default:"+table, rows)
}

func kqlTraces(t *testing.T) {
	withKQLRows(t, "AppTraces",
		monitorLogRow{"TimeGenerated": "2026-01-01T00:00:00Z", "AppRoleName": "api", "Message": "GET /a | ok"},
		monitorLogRow{"TimeGenerated": "2026-01-01T00:10:00Z", "AppRoleName": "api", "Message": "an error: disk full"},
		monitorLogRow{"TimeGenerated": "2026-01-01T01:05:00Z", "AppRoleName": "worker", "Message": "Errors counted"},
		monitorLogRow{"TimeGenerated": "2026-01-01T01:30:00Z", "AppRoleName": "worker", "Message": "it's \"quoted\""},
	)
}

func mustQuery(t *testing.T, query string) Table {
	t.Helper()
	result, err := runKQLQuery("default", query, "")
	if err != nil {
		t.Fatalf("query %q failed: %v", query, err.body())
	}
	if len(result.Tables) != 1 {
		t.Fatalf("query %q answered %d tables", query, len(result.Tables))
	}
	return result.Tables[0]
}

func columnValues(t *testing.T, table Table, name string) []any {
	t.Helper()
	for i, c := range table.Columns {
		if c.Name == name {
			out := make([]any, len(table.Rows))
			for r, row := range table.Rows {
				out[r] = row[i]
			}
			return out
		}
	}
	t.Fatalf("no column %q in %+v", name, table.Columns)
	return nil
}

func assertValues(t *testing.T, got []any, want ...any) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Fatalf("got %s, want %s", g, w)
	}
}

func TestKQL_WhereBooleanOperators(t *testing.T) {
	kqlTraces(t)
	cases := []struct {
		query string
		want  []any
	}{
		{`AppTraces | where AppRoleName == "api" and Message contains "error"`, []any{"an error: disk full"}},
		{`AppTraces | where AppRoleName == "worker" or Message startswith "GET"`, []any{"GET /a | ok", "Errors counted", "it's \"quoted\""}},
		{`AppTraces | where AppRoleName == "api" and (Message has "ok" or Message has "disk")`, []any{"GET /a | ok", "an error: disk full"}},
		{`AppTraces | where not(AppRoleName == "api") and Message !has "errors"`, []any{"it's \"quoted\""}},
		{`AppTraces | where Message == "GET /a | ok"`, []any{"GET /a | ok"}},
		{`AppTraces | where Message == 'it\'s "quoted"'`, []any{"it's \"quoted\""}},
		{`AppTraces | where Message == @'c:\no' or Message == @"GET /a | ok"`, []any{"GET /a | ok"}},
		{`AppTraces | where Message has "error"`, []any{"an error: disk full"}},
		{`AppTraces | where Message == @'it''s "quoted"' or Message == h@"GET /a | ok"`, []any{"GET /a | ok", "it's \"quoted\""}},
		{`AppTraces | where Message has_cs "Errors"`, []any{"Errors counted"}},
		{`AppTraces | where AppRoleName =~ "API" and Message !contains "disk"`, []any{"GET /a | ok"}},
		{`AppTraces | where AppRoleName in ("worker", "nobody") and Message endswith "counted"`, []any{"Errors counted"}},
		{`AppTraces | where AppRoleName !in ("worker")`, []any{"GET /a | ok", "an error: disk full"}},
		{`AppTraces | where AppRoleName in~ ("WORKER") | where Message matches regex "^E[a-z]+s "`, []any{"Errors counted"}},
		{`AppTraces | where TimeGenerated between (datetime(2026-01-01T00:05:00Z) .. 1h)`, []any{"an error: disk full", "Errors counted"}},
		{`AppTraces | where TimeGenerated >= datetime(2026-01-01 01:00) // a comment | not an operator
		  | where Message != "Errors counted"`, []any{"it's \"quoted\""}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			assertValues(t, columnValues(t, mustQuery(t, tc.query), "Message"), tc.want...)
		})
	}
}

func TestKQL_TabularOperators(t *testing.T) {
	kqlTraces(t)

	table := mustQuery(t, `AppTraces | project Role = AppRoleName, Message | take 1`)
	if len(table.Columns) != 2 || table.Columns[0].Name != "Role" || table.Columns[1].Name != "Message" {
		t.Fatalf("project columns = %+v", table.Columns)
	}
	assertValues(t, columnValues(t, table, "Role"), "api")

	table = mustQuery(t, `AppTraces | project-away Message, TimeGenerated | distinct AppRoleName`)
	assertValues(t, columnValues(t, table, "AppRoleName"), "api", "worker")

	table = mustQuery(t, `AppTraces | extend Len = strlen(Message), Upper = toupper(AppRoleName) | order by Len asc | take 2`)
	assertValues(t, columnValues(t, table, "Len"), 11, 13)
	assertValues(t, columnValues(t, table, "Upper"), "API", "WORKER")
	if table.Columns[3].Type != "long" {
		t.Fatalf("strlen column type = %q", table.Columns[3].Type)
	}

	table = mustQuery(t, `AppTraces | sort by TimeGenerated | limit 1 | project TimeGenerated`)
	assertValues(t, columnValues(t, table, "TimeGenerated"), "2026-01-01T01:30:00Z")

	table = mustQuery(t, `AppTraces | top 2 by strlen(Message) asc | project Message`)
	assertValues(t, columnValues(t, table, "Message"), "GET /a | ok", "it's \"quoted\"")

	table = mustQuery(t, `AppTraces | where AppRoleName == "api" | count`)
	assertValues(t, columnValues(t, table, "Count"), 2)

	table = mustQuery(t, `AppTraces | summarize count(), Errors = countif(Message contains "error") by AppRoleName | order by AppRoleName asc`)
	assertValues(t, columnValues(t, table, "AppRoleName"), "api", "worker")
	assertValues(t, columnValues(t, table, "count_"), 2, 2)
	assertValues(t, columnValues(t, table, "Errors"), 1, 1)

	table = mustQuery(t, `AppTraces | summarize n = count() by bin(TimeGenerated, 1h)`)
	assertValues(t, columnValues(t, table, "TimeGenerated"), "2026-01-01T00:00:00Z", "2026-01-01T01:00:00Z")
	assertValues(t, columnValues(t, table, "n"), 2, 2)

	table = mustQuery(t, `AppTraces | summarize dcount(AppRoleName), Last = max(TimeGenerated)`)
	assertValues(t, columnValues(t, table, "dcount_AppRoleName"), 2)
	assertValues(t, columnValues(t, table, "Last"), "2026-01-01T01:30:00Z")

	table = mustQuery(t, `AppTraces | where AppRoleName == "nobody" | summarize count()`)
	assertValues(t, columnValues(t, table, "count_"), 0)

	table = mustQuery(t, `AppTraces | extend Len = strlen(Message), Twice = Len * 2, Len = Len + 1 | top 1 by Len desc | project Len, Twice`)
	assertValues(t, columnValues(t, table, "Len"), 20)
	assertValues(t, columnValues(t, table, "Twice"), 38)

	table = mustQuery(t, `AppTraces | project-rename Role = AppRoleName | extend Gap = datetime(2026-01-01T02:00:00Z) - TimeGenerated | where Role == "worker" | project Gap`)
	assertValues(t, columnValues(t, table, "Gap"), "00:55:00", "00:30:00")
}

func TestKQL_TimespanBoundsTheRows(t *testing.T) {
	kqlTraces(t)
	result, err := runKQLQuery("default", "AppTraces | project Message", "2026-01-01T00:05:00Z/PT1H")
	if err != nil {
		t.Fatal(err.body())
	}
	assertValues(t, columnValues(t, result.Tables[0], "Message"), "an error: disk full", "Errors counted")

	if _, err := runKQLQuery("default", "AppTraces", "yesterday"); err == nil || err.kind != "" {
		t.Fatalf("an unreadable timespan must be refused as a bad argument, got %v", err)
	}
}

func TestKQL_RefusesWhatItCannotRun(t *testing.T) {
	kqlTraces(t)
	cases := []struct {
		query, kind, code string
	}{
		{`AppTraces | where AppRoleName == "x" and`, "SyntaxError", "SYN0002"},
		{`AppTraces | where Message == "unterminated`, "SyntaxError", "SYN0002"},
		{`AppTraces | join (AppTraces) on AppRoleName`, "SyntaxError", "SYN0002"},
		{`AppTraces | Where Message == "x"`, "SyntaxError", "SYN0002"},
		{`AppTraces | where Message == "x" garbage`, "SyntaxError", "SYN0002"},
		{`AppTraces | take 5m`, "SyntaxError", "SYN0002"},
		{`AppTraces | where NoSuchColumn == "x"`, "SemanticError", "SEM0100"},
		{`NoSuchTable | take 1`, "SemanticError", "SEM0100"},
		{`AppTraces | extend x = frobnicate(Message)`, "SemanticError", "SEM0100"},
		{`AppTraces | where Message == 5`, "SemanticError", ""},
		{`AppTraces | where Message`, "SemanticError", ""},
		{`AppTraces | summarize strlen(Message)`, "SemanticError", "SEM0100"},
		{`AppTraces | summarize count() * 2`, "SemanticError", ""},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			_, err := runKQLQuery("default", tc.query, "")
			if err == nil {
				t.Fatalf("query was answered, want a %s", tc.kind)
			}
			if err.kind != tc.kind || err.code != tc.code {
				t.Fatalf("got %s/%s (%s), want %s/%s", err.kind, err.code, err.message, tc.kind, tc.code)
			}
		})
	}
}

func TestKQL_SyntaxErrorResponseShape(t *testing.T) {
	kqlTraces(t)
	recorder := httptest.NewRecorder()
	writeKQLResult(recorder, "default", "AppTraces\n| where Message == \"x\" or", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body struct {
		Error struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			InnerError struct {
				Code       string `json:"code"`
				InnerError struct {
					Code  string `json:"code"`
					Line  int    `json:"line"`
					Pos   int    `json:"pos"`
					Token string `json:"token"`
				} `json:"innererror"`
			} `json:"innererror"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "BadArgumentError" || body.Error.InnerError.Code != "SyntaxError" ||
		body.Error.InnerError.InnerError.Code != "SYN0002" {
		t.Fatalf("error = %s", recorder.Body.String())
	}
	if d := body.Error.InnerError.InnerError; d.Line != 2 || d.Pos != 26 || d.Token != "<EOF>" {
		t.Fatalf("position = %+v", d)
	}
}

func TestKQL_LexerLiterals(t *testing.T) {
	toks, err := lexKQL(`"a\"|b" 'c\\d' @"e\f" h'g' 1.5h 2d 10 3.25 datetime(2026-01-01) !contains in~ project-away a-b`)
	if err != nil {
		t.Fatal(err.message)
	}
	want := []struct {
		kind kqlTokenKind
		text string
	}{
		{kqlString, ""}, {kqlString, ""}, {kqlString, ""}, {kqlString, ""},
		{kqlTimespan, "1.5h"}, {kqlTimespan, "2d"}, {kqlLong, "10"}, {kqlReal, "3.25"},
		{kqlDatetime, "datetime(2026-01-01)"}, {kqlIdent, "!contains"}, {kqlIdent, "in~"},
		{kqlIdent, "project-away"}, {kqlIdent, "a"}, {kqlPunct, "-"}, {kqlIdent, "b"}, {kqlEOF, ""},
	}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens: %+v", len(toks), toks)
	}
	for i, w := range want {
		if toks[i].kind != w.kind || (w.text != "" && toks[i].text != w.text) {
			t.Fatalf("token %d = %+v, want %+v", i, toks[i], w)
		}
	}
	for i, s := range []string{`a"|b`, `c\d`, `e\f`, "g"} {
		if toks[i].str != s {
			t.Fatalf("string %d = %q, want %q", i, toks[i].str, s)
		}
	}
	if toks[4].span != 90*time.Minute || toks[5].span != 48*time.Hour {
		t.Fatalf("timespans = %v, %v", toks[4].span, toks[5].span)
	}
}

func TestKQL_HasHonoursNonASCIITerms(t *testing.T) {
	withKQLRows(t, "AppTraces",
		monitorLogRow{"TimeGenerated": "2026-01-01T00:00:00Z", "AppRoleName": "api", "Message": "Ärger über café"},
		monitorLogRow{"TimeGenerated": "2026-01-01T00:00:01Z", "AppRoleName": "api", "Message": "décafé"},
	)
	assertValues(t, columnValues(t, mustQuery(t, `AppTraces | where Message has "café"`), "Message"), "Ärger über café")
	if rows := mustQuery(t, `AppTraces | where Message has "rger"`).Rows; len(rows) != 0 {
		t.Fatalf("has matched inside a term: %v", rows)
	}
}
