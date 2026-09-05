package main

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CloudWatch Logs Insights — StartQuery / GetQueryResults / StopQuery /
// DescribeQueries, with a real executor for the Insights query language
// (pipe-delimited commands: fields | filter | stats | sort | limit | parse |
// dedup). The sim runs the query synchronously at StartQuery time and stores
// the rows; GetQueryResults returns them with status "Complete".

type CWQuery struct {
	QueryID    string             `json:"queryId"`
	QueryStr   string             `json:"queryString"`
	Status     string             `json:"status"`
	Results    [][]map[string]any `json:"results"`
	MatchedN   int                `json:"-"`
	ScannedN   int                `json:"-"`
	CreateTime int64              `json:"-"`
	LogGroups  []string           `json:"-"`
}

var cwQueries sim.Store[CWQuery]

func registerCloudWatchInsights(r *AWSRouter) {
	r.Register("Logs_20140328.StartQuery", handleCWStartQuery)
	r.Register("Logs_20140328.GetQueryResults", handleCWGetQueryResults)
	r.Register("Logs_20140328.StopQuery", handleCWStopQuery)
	r.Register("Logs_20140328.DescribeQueries", handleCWDescribeQueries)
}

func handleCWStartQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName  string   `json:"logGroupName"`
		LogGroupNames []string `json:"logGroupNames"`
		QueryString   string   `json:"queryString"`
		StartTime     int64    `json:"startTime"`
		EndTime       int64    `json:"endTime"`
		Limit         int      `json:"limit"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.QueryString == "" {
		AWSError(w, "InvalidParameterException", "queryString is required", http.StatusBadRequest)
		return
	}
	groups := req.LogGroupNames
	if req.LogGroupName != "" {
		groups = append(groups, req.LogGroupName)
	}
	// Insights times are epoch SECONDS.
	records, scanned := cwGatherRecords(groups, req.StartTime, req.EndTime)
	results, err := cwRunInsightsQuery(req.QueryString, records, req.Limit)
	if err != nil {
		// A malformed query string is a MalformedQueryException in real
		// CloudWatch Logs, not a silently empty (or all-rows) result.
		AWSError(w, "MalformedQueryException", err.Error(), http.StatusBadRequest)
		return
	}

	qid := generateUUID()
	cwQueries.Put(qid, CWQuery{
		QueryID:    qid,
		QueryStr:   req.QueryString,
		Status:     "Complete",
		Results:    results,
		MatchedN:   len(results),
		ScannedN:   scanned,
		CreateTime: time.Now().Unix(),
		LogGroups:  groups,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"queryId": qid})
}

func handleCWGetQueryResults(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueryID string `json:"queryId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	q, ok := cwQueries.Get(req.QueryID)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Query %s does not exist", req.QueryID)
		return
	}
	// Real GetQueryResults returns results/status/statistics — not queryId.
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"status":  q.Status,
		"results": q.Results,
		"statistics": map[string]any{
			"recordsMatched": float64(q.MatchedN),
			"recordsScanned": float64(q.ScannedN),
			"bytesScanned":   float64(q.ScannedN * 256),
		},
	})
}

func handleCWStopQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueryID string `json:"queryId"`
	}
	_ = sim.ReadJSON(r, &req)
	if q, ok := cwQueries.Get(req.QueryID); ok {
		q.Status = "Cancelled"
		cwQueries.Put(q.QueryID, q)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

func handleCWDescribeQueries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName string `json:"logGroupName"`
		Status       string `json:"status"`
	}
	_ = sim.ReadJSON(r, &req)
	out := make([]map[string]any, 0)
	for _, q := range cwQueries.List() {
		if req.LogGroupName != "" && !cwStrIn(req.LogGroupName, q.LogGroups) {
			continue
		}
		if req.Status != "" && req.Status != q.Status {
			continue
		}
		out = append(out, map[string]any{
			"queryId":      q.QueryID,
			"queryString":  q.QueryStr,
			"status":       q.Status,
			"createTime":   float64(q.CreateTime),
			"logGroupName": strings.Join(q.LogGroups, ","),
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"queries": out})
}

func cwStrIn(s string, list []string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ── records ────────────────────────────────────────────────────────────────

// cwInsightsRecord is one log event flattened into Insights fields: the built-in
// @-fields plus any top-level (and dotted-nested) fields parsed from a JSON
// message.
type cwInsightsRecord map[string]string

func cwGatherRecords(groups []string, startSec, endSec int64) ([]cwInsightsRecord, int) {
	var records []cwInsightsRecord
	scanned := 0
	for _, stream := range cwLogStreams.List() {
		if len(groups) > 0 && !cwStrIn(stream.LogGroupName, groups) {
			continue
		}
		events, _ := cwLogEvents.Get(cwEventsKey(stream.LogGroupName, stream.LogStreamName))
		for _, e := range events {
			tsSec := e.Timestamp / 1000
			if startSec > 0 && tsSec < startSec {
				continue
			}
			if endSec > 0 && tsSec > endSec {
				continue
			}
			scanned++
			rec := cwInsightsRecord{
				"@timestamp":     time.UnixMilli(e.Timestamp).UTC().Format("2006-01-02 15:04:05.000"),
				"@message":       e.Message,
				"@logStream":     stream.LogStreamName,
				"@ingestionTime": time.UnixMilli(e.IngestionTime).UTC().Format("2006-01-02 15:04:05.000"),
				"@__ts":          strconv.FormatInt(e.Timestamp, 10),
			}
			cwFlattenJSONInto(rec, e.Message)
			records = append(records, rec)
		}
	}
	return records, scanned
}

// cwFlattenJSONInto parses a JSON message and adds its leaf fields (dotted) to
// the record, so a query can reference `level`, `code`, `req.path`, etc.
func cwFlattenJSONInto(rec cwInsightsRecord, message string) {
	var doc map[string]any
	if json.Unmarshal([]byte(message), &doc) != nil {
		return
	}
	// Bound recursion against a deeply-nested attacker message (planted via
	// PutLogEvents, parsed here at StartQuery). Go's json.Unmarshal already
	// caps nesting at 10000, but the walk is hardened so it can never blow the
	// stack regardless of decode path: past the cap a node is recorded as a
	// scalar rather than descended.
	const maxFlattenDepth = 256
	var walk func(prefix string, v any, depth int)
	walk = func(prefix string, v any, depth int) {
		if m, ok := v.(map[string]any); ok && depth < maxFlattenDepth {
			for k, sub := range m {
				key := k
				if prefix != "" {
					key = prefix + "." + k
				}
				walk(key, sub, depth+1)
			}
			return
		}
		rec[prefix] = cwJSONScalar(v)
	}
	for k, v := range doc {
		walk(k, v, 0)
	}
}

// ── query execution ────────────────────────────────────────────────────────

func cwRunInsightsQuery(query string, records []cwInsightsRecord, reqLimit int) ([][]map[string]any, error) {
	stages := strings.Split(query, "|")
	outputFields := []string{"@timestamp", "@message"}
	limit := 0
	if reqLimit > 0 {
		limit = reqLimit
	}
	statsApplied := false

	for _, raw := range stages {
		cmd := strings.TrimSpace(raw)
		if cmd == "" {
			continue
		}
		kw, rest := cwSplitFirstWord(cmd)
		switch strings.ToLower(kw) {
		case "fields", "display":
			outputFields = cwSplitCommaList(rest)
		case "filter", "where":
			node, err := cwParseInsightsFilter(rest)
			if err != nil {
				return nil, err
			}
			records = cwFilterRecords(records, node)
		case "stats":
			records = cwRunStats(rest, records)
			statsApplied = true
			outputFields = cwStatsOutputFields(rest)
		case "sort", "order":
			records = cwSortRecords(rest, records)
		case "limit":
			if n, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil {
				limit = n
			}
		case "dedup":
			records = cwDedup(records, cwSplitCommaList(rest))
		}
	}
	// Default Insights ordering is newest-first by @timestamp when no sort/stats.
	if !statsApplied {
		sort.SliceStable(records, func(i, j int) bool {
			return cwRecTS(records[i]) > cwRecTS(records[j])
		})
	}
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}

	out := make([][]map[string]any, 0, len(records))
	for _, rec := range records {
		row := make([]map[string]any, 0, len(outputFields))
		for _, f := range outputFields {
			row = append(row, map[string]any{"field": f, "value": rec[f]})
		}
		out = append(out, row)
	}
	return out, nil
}

func cwRecTS(rec cwInsightsRecord) int64 {
	n, _ := strconv.ParseInt(rec["@__ts"], 10, 64)
	return n
}

func cwSplitFirstWord(s string) (string, string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

func cwSplitCommaList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ── stats ──────────────────────────────────────────────────────────────────

// cwRunStats applies `stats fn(field) [as alias] [, ...] by g1, g2`.
func cwRunStats(spec string, records []cwInsightsRecord) []cwInsightsRecord {
	aggPart, byPart := spec, ""
	if i := cwIndexKeyword(spec, "by"); i >= 0 {
		aggPart = strings.TrimSpace(spec[:i])
		byPart = strings.TrimSpace(spec[i+len("by"):])
	}
	groupFields := cwSplitCommaList(byPart)
	aggs := cwParseAggs(aggPart)

	groups := map[string][]cwInsightsRecord{}
	order := []string{}
	for _, rec := range records {
		key := ""
		for _, gf := range groupFields {
			key += rec[gf] + "\x00"
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], rec)
	}
	// No `by`: a single group over all records.
	if len(groupFields) == 0 {
		order = []string{""}
		groups[""] = records
	}

	out := make([]cwInsightsRecord, 0, len(order))
	for _, key := range order {
		grp := groups[key]
		row := cwInsightsRecord{}
		for i, gf := range groupFields {
			parts := strings.Split(key, "\x00")
			if i < len(parts) {
				row[gf] = parts[i]
			}
		}
		for _, a := range aggs {
			row[a.label] = cwComputeAgg(a, grp)
		}
		out = append(out, row)
	}
	return out
}

type cwAgg struct {
	fn    string
	field string
	label string
}

func cwParseAggs(s string) []cwAgg {
	var aggs []cwAgg
	for _, part := range cwSplitCommaList(s) {
		label := part
		expr := part
		if i := cwIndexKeyword(part, "as"); i >= 0 {
			expr = strings.TrimSpace(part[:i])
			label = strings.TrimSpace(part[i+len("as"):])
		}
		fn, field := expr, ""
		if op := strings.IndexByte(expr, '('); op >= 0 && strings.HasSuffix(expr, ")") {
			fn = strings.TrimSpace(expr[:op])
			field = strings.TrimSpace(expr[op+1 : len(expr)-1])
		}
		aggs = append(aggs, cwAgg{fn: strings.ToLower(fn), field: field, label: label})
	}
	return aggs
}

func cwComputeAgg(a cwAgg, grp []cwInsightsRecord) string {
	switch a.fn {
	case "count":
		// `count(*)` and a bare `count` count every record in the group; a
		// named field counts the records that carry it.
		if a.field == "" || a.field == "*" {
			return strconv.Itoa(len(grp))
		}
		n := 0
		for _, r := range grp {
			if _, ok := r[a.field]; ok {
				n++
			}
		}
		return strconv.Itoa(n)
	case "count_distinct":
		seen := map[string]struct{}{}
		for _, r := range grp {
			seen[r[a.field]] = struct{}{}
		}
		return strconv.Itoa(len(seen))
	case "sum", "avg", "min", "max":
		var vals []float64
		for _, r := range grp {
			if f, err := strconv.ParseFloat(r[a.field], 64); err == nil {
				vals = append(vals, f)
			}
		}
		if len(vals) == 0 {
			return "0"
		}
		switch a.fn {
		case "sum":
			s := 0.0
			for _, v := range vals {
				s += v
			}
			return cwNumStr(s)
		case "avg":
			s := 0.0
			for _, v := range vals {
				s += v
			}
			return cwNumStr(s / float64(len(vals)))
		case "min":
			m := vals[0]
			for _, v := range vals {
				m = math.Min(m, v)
			}
			return cwNumStr(m)
		case "max":
			m := vals[0]
			for _, v := range vals {
				m = math.Max(m, v)
			}
			return cwNumStr(m)
		}
	}
	return ""
}

func cwNumStr(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func cwStatsOutputFields(spec string) []string {
	aggPart, byPart := spec, ""
	if i := cwIndexKeyword(spec, "by"); i >= 0 {
		aggPart = strings.TrimSpace(spec[:i])
		byPart = strings.TrimSpace(spec[i+len("by"):])
	}
	fields := cwSplitCommaList(byPart)
	for _, a := range cwParseAggs(aggPart) {
		fields = append(fields, a.label)
	}
	return fields
}

// cwIndexKeyword finds a whitespace-delimited keyword (case-insensitive). The
// returned index slices the ORIGINAL s, so it folds with sim.ASCIIFold (byte-
// length preserving) — strings.ToLower can change the byte length on
// multibyte / invalid-UTF-8 input and shift the index past len(s).
func cwIndexKeyword(s, kw string) int {
	lower := sim.ASCIIFold(s)
	target := " " + kw + " "
	if i := strings.Index(lower, target); i >= 0 {
		return i + 1
	}
	return -1
}

// ── sort / dedup ───────────────────────────────────────────────────────────

func cwSortRecords(spec string, records []cwInsightsRecord) []cwInsightsRecord {
	field, desc := cwParseSortSpec(spec)
	sort.SliceStable(records, func(i, j int) bool {
		a, b := records[i][field], records[j][field]
		if af, ae := strconv.ParseFloat(a, 64); ae == nil {
			if bf, be := strconv.ParseFloat(b, 64); be == nil {
				if desc {
					return af > bf
				}
				return af < bf
			}
		}
		if desc {
			return a > b
		}
		return a < b
	})
	return records
}

func cwParseSortSpec(spec string) (field string, desc bool) {
	spec = strings.TrimSpace(strings.Split(spec, ",")[0])
	low := strings.ToLower(spec)
	if strings.HasSuffix(low, " desc") {
		return strings.TrimSpace(spec[:len(spec)-5]), true
	}
	if strings.HasSuffix(low, " asc") {
		return strings.TrimSpace(spec[:len(spec)-4]), false
	}
	return spec, false
}

func cwDedup(records []cwInsightsRecord, fields []string) []cwInsightsRecord {
	if len(fields) == 0 {
		return records
	}
	seen := map[string]struct{}{}
	out := records[:0]
	for _, r := range records {
		key := ""
		for _, f := range fields {
			key += r[f] + "\x00"
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, r)
	}
	return out
}

func cwFilterRecords(records []cwInsightsRecord, node cwInsightsNode) []cwInsightsRecord {
	out := records[:0]
	for _, r := range records {
		if node.eval(r) {
			out = append(out, r)
		}
	}
	return out
}
