package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// kqlError is a query the service refuses. The Log Analytics query API
// answers it with HTTP 400 and an ErrorResponse whose code is
// BadArgumentError, nesting what went wrong as a SyntaxError or SemanticError.
type kqlError struct {
	kind    string // SyntaxError, SemanticError, or empty for a request-level fault
	code    string // the innermost diagnostic code, such as SYN0002
	message string
	line    int
	pos     int
	token   string
	hasPos  bool
}

func (e *kqlError) Error() string { return e.message }

func kqlSyntaxError(src string, offset int, token string) *kqlError {
	line, col := 1, 1
	for _, r := range src[:min(offset, len(src))] {
		if r == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	if token == "" {
		token = "<EOF>"
	}
	return &kqlError{
		kind:    "SyntaxError",
		code:    "SYN0002",
		message: fmt.Sprintf("Query could not be parsed at '%s' on line [%d,%d]", token, line, col),
		line:    line, pos: col, token: token, hasPos: true,
	}
}

func kqlSemanticError(code, message string) *kqlError {
	return &kqlError{kind: "SemanticError", code: code, message: message}
}

// body renders the ErrorResponse the query API answers a refused query with.
func (e *kqlError) body() map[string]any {
	if e.kind == "" {
		return map[string]any{"error": map[string]any{"code": "BadArgumentError", "message": e.message}}
	}
	summary := "A recognition error occurred in the query."
	if e.kind == "SemanticError" {
		summary = "A semantic error occurred."
	}
	inner := map[string]any{"code": e.kind, "message": summary}
	if e.code == "" {
		inner["message"] = e.message
	} else {
		detail := map[string]any{"code": e.code, "message": e.message}
		if e.hasPos {
			detail["line"] = e.line
			detail["pos"] = e.pos
			detail["token"] = e.token
		}
		inner["innererror"] = detail
	}
	return map[string]any{"error": map[string]any{
		"code":       "BadArgumentError",
		"message":    "The request had some invalid properties",
		"innererror": inner,
	}}
}

// writeKQLResult answers a query request with its tables or its error.
func writeKQLResult(w http.ResponseWriter, workspaceID, query, timespan string) {
	result, err := runKQLQuery(workspaceID, query, timespan)
	if err != nil {
		sim.WriteJSON(w, http.StatusBadRequest, err.body())
		return
	}
	sim.WriteJSON(w, http.StatusOK, result)
}

// runKQLQuery executes a KQL query against the workspace's stored log rows and
// returns the QueryResults tabular shape. Both the POST and GET Log Analytics
// query endpoints and each member of a $batch run through here. timespan is
// the request's ISO 8601 interval, which bounds TimeGenerated before the query
// runs.
func runKQLQuery(workspaceID, query, timespan string) (QueryResponse, *kqlError) {
	now := time.Now().UTC()
	var window *[2]time.Time
	if timespan != "" {
		start, end, ok := parseQueryTimespan(timespan, now)
		if !ok {
			return QueryResponse{}, &kqlError{message: fmt.Sprintf("The timespan '%s' is not a valid ISO 8601 interval.", timespan)}
		}
		window = &[2]time.Time{start, end}
	}

	parsed, err := parseKQL(query)
	if err != nil {
		return QueryResponse{}, err
	}
	columns, ok := kqlTableSchemas[parsed.table]
	if !ok {
		message := fmt.Sprintf("Failed to resolve table or column expression named '%s'", parsed.table)
		if len(parsed.ops) > 0 {
			message = fmt.Sprintf("'%s' operator: %s", parsed.ops[0].operatorName(), message)
		}
		return QueryResponse{}, kqlSemanticError("SEM0100", message)
	}

	entries, _ := monitorLogs.Get(workspaceID + ":" + parsed.table)
	if len(entries) == 0 {
		entries, _ = monitorLogs.Get("default:" + parsed.table)
	}
	timeColumn := -1
	for i, c := range columns {
		if c.Name == "TimeGenerated" {
			timeColumn = i
		}
	}
	rows := make([][]any, 0, len(entries))
	for _, entry := range entries {
		row := entry.typedRow(columns)
		if window != nil && timeColumn >= 0 {
			t, ok := row[timeColumn].(time.Time)
			if !ok || t.Before(window[0]) || t.After(window[1]) {
				continue
			}
		}
		rows = append(rows, row)
	}

	set := kqlResultSet{columns: columns, rows: rows}
	binder := &kqlBinder{src: query, now: now}
	for _, op := range parsed.ops {
		if set, err = binder.apply(op, set); err != nil {
			return QueryResponse{}, err
		}
	}

	out := make([][]any, len(set.rows))
	for i, row := range set.rows {
		rendered := make([]any, len(row))
		for j, v := range row {
			rendered[j] = kqlRenderValue(v)
		}
		out[i] = rendered
	}
	return QueryResponse{Tables: []Table{{Name: "PrimaryResult", Columns: set.columns, Rows: out}}}, nil
}

var iso8601Duration = regexp.MustCompile(`^P(?:(\d+)Y)?(?:(\d+)M)?(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

// addISO8601Duration moves an instant by an ISO 8601 duration, forwards for
// sign 1 and backwards for sign -1.
func addISO8601Duration(t time.Time, s string, sign int) (time.Time, bool) {
	m := iso8601Duration.FindStringSubmatch(s)
	if m == nil || s == "P" || strings.HasSuffix(s, "T") {
		return time.Time{}, false
	}
	n := func(v string) int {
		i, _ := strconv.Atoi(v)
		return i
	}
	var secs float64
	if m[7] != "" {
		secs, _ = strconv.ParseFloat(m[7], 64)
	}
	t = t.AddDate(sign*n(m[1]), sign*n(m[2]), sign*(n(m[3])*7+n(m[4])))
	clock := time.Duration(n(m[5]))*time.Hour + time.Duration(n(m[6]))*time.Minute + time.Duration(secs*float64(time.Second))
	return t.Add(time.Duration(sign) * clock), true
}

// parseQueryTimespan reads the query API's timespan: a duration ending now, or
// an interval given as start/end, start/duration or duration/end.
func parseQueryTimespan(s string, now time.Time) (time.Time, time.Time, bool) {
	first, second, isInterval := strings.Cut(s, "/")
	if !isInterval {
		start, ok := addISO8601Duration(now, s, -1)
		return start, now, ok
	}
	startT, startIsTime := parseKQLDatetime(first)
	endT, endIsTime := parseKQLDatetime(second)
	switch {
	case startIsTime && endIsTime:
		return startT, endT, true
	case startIsTime:
		end, ok := addISO8601Duration(startT, second, 1)
		return startT, end, ok
	case endIsTime:
		start, ok := addISO8601Duration(endT, first, -1)
		return start, endT, ok
	}
	return time.Time{}, time.Time{}, false
}

var kqlTableSchemas = map[string][]Column{
	"ContainerAppConsoleLogs_CL": {
		{Name: "TimeGenerated", Type: "datetime"},
		{Name: "ContainerGroupName_s", Type: "string"},
		{Name: "ContainerAppName_s", Type: "string"},
		{Name: "Log_s", Type: "string"},
		{Name: "Stream_s", Type: "string"},
	},
	"AppTraces": {
		{Name: "TimeGenerated", Type: "datetime"},
		{Name: "Message", Type: "string"},
		{Name: "AppRoleName", Type: "string"},
	},
	"AppEvents":              kqlAppTableColumns("Name"),
	"AppPageViews":           kqlAppTableColumns("Name"),
	"AppBrowserTimings":      kqlAppTableColumns("Name"),
	"AppRequests":            kqlAppTableColumns("Name"),
	"AppDependencies":        kqlAppTableColumns("Name"),
	"AppAvailabilityResults": kqlAppTableColumns("Name"),
	"AppExceptions":          kqlAppTableColumns("ExceptionType"),
	"AppPerformanceCounters": append(kqlAppTableColumns("Name"), Column{Name: "Value", Type: "real"}),
	"AppMetrics":             append(kqlAppTableColumns("Name"), Column{Name: "Sum", Type: "real"}),
}

// kqlAppTableColumns is the part of a workspace-based Application Insights
// table's schema the simulator records: when, what, and which role wrote it.
func kqlAppTableColumns(what string) []Column {
	return []Column{
		{Name: "TimeGenerated", Type: "datetime"},
		{Name: what, Type: "string"},
		{Name: "AppRoleName", Type: "string"},
	}
}

// monitorLogRow is a generic log row stored as field→value pairs.
type monitorLogRow map[string]string

// typedRow reads a stored row as the typed cells of the given columns. A
// datetime or number that is absent or unreadable is null; a string is never
// null in Kusto, only empty.
func (row monitorLogRow) typedRow(columns []Column) []any {
	result := make([]any, len(columns))
	for i, col := range columns {
		raw := row[col.Name]
		switch col.Type {
		case "string":
			result[i] = raw
		case "datetime":
			if t, ok := parseKQLDatetime(raw); ok {
				result[i] = t
			}
		case "real":
			if f, err := strconv.ParseFloat(raw, 64); err == nil {
				result[i] = f
			}
		case "long", "int":
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
				result[i] = n
			}
		}
	}
	return result
}
