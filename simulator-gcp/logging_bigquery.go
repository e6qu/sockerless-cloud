package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// LoggingBigQueryOptions is a sink's bigqueryOptions.
type LoggingBigQueryOptions struct {
	UsePartitionedTables            bool `json:"usePartitionedTables,omitempty"`
	UsesTimestampColumnPartitioning bool `json:"usesTimestampColumnPartitioning,omitempty"`
}

// loggingBigQueryTimestampFields are the LogEntry fields the Discovery
// document formats as google-datetime, which a dataset holds as TIMESTAMP.
var loggingBigQueryTimestampFields = map[string]bool{"timestamp": true, "receiveTimestamp": true}

// loggingBigQueryName turns a log ID or payload field name into a BigQuery
// name: every character other than a letter, digit or underscore becomes an
// underscore, and a name starting with a digit gains a leading underscore.
func loggingBigQueryName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || ('0' <= out[0] && out[0] <= '9') {
		out = "_" + out
	}
	return out
}

// loggingBigQueryTable names the table an entry lands in: its log ID as a
// BigQuery name and, without partitioned tables, the UTC date of its
// timestamp, as in syslog_20170523.
func loggingBigQueryTable(entry LogEntry, partitioned bool) (string, error) {
	_, logID, _ := strings.Cut(entry.LogName, "/logs/")
	if decoded, err := url.PathUnescape(logID); err == nil {
		logID = decoded
	}
	table := loggingBigQueryName(logID)
	if partitioned {
		return table, nil
	}
	ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil {
		return "", fmt.Errorf("entry %s has timestamp %q: %w", entry.InsertID, entry.Timestamp, err)
	}
	return table + "_" + ts.UTC().Format("20060102"), nil
}

// loggingBigQueryRow is an entry as a table row: its LogEntry fields as
// columns, with the keys of maps beneath them as BigQuery names.
func loggingBigQueryRow(entry LogEntry) (map[string]any, error) {
	raw, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, err
	}
	for k, v := range row {
		row[k] = loggingBigQueryValue(v)
	}
	return row, nil
}

func loggingBigQueryValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, inner := range v {
			out[loggingBigQueryName(k)] = loggingBigQueryValue(inner)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, inner := range v {
			out[i] = loggingBigQueryValue(inner)
		}
		return out
	}
	return v
}

// loggingBigQueryFields derives the columns a row needs, sorted by name.
func loggingBigQueryFields(row map[string]any, top bool) []BQFieldSchema {
	names := make([]string, 0, len(row))
	for k := range row {
		names = append(names, k)
	}
	sort.Strings(names)
	var fields []BQFieldSchema
	for _, name := range names {
		field, ok := loggingBigQueryField(name, row[name])
		if !ok {
			continue
		}
		if top && loggingBigQueryTimestampFields[name] {
			field.Type = "TIMESTAMP"
		}
		fields = append(fields, field)
	}
	return fields
}

func loggingBigQueryField(name string, v any) (BQFieldSchema, bool) {
	field := BQFieldSchema{Name: name, Mode: "NULLABLE"}
	if list, ok := v.([]any); ok {
		var elem *BQFieldSchema
		for _, item := range list {
			inner, ok := loggingBigQueryField(name, item)
			if !ok {
				return field, false
			}
			if elem == nil {
				elem = &inner
				continue
			}
			if inner.Type != elem.Type {
				return field, false
			}
			if inner.Type == "RECORD" {
				merged, err := loggingMergeBigQueryFields(elem.Fields, inner.Fields, "")
				if err != nil {
					return field, false
				}
				elem.Fields = merged
			}
		}
		if elem == nil {
			return field, false
		}
		elem.Mode = "REPEATED"
		return *elem, true
	}
	switch v := v.(type) {
	case string:
		field.Type = "STRING"
	case bool:
		field.Type = "BOOLEAN"
	case float64:
		field.Type = "FLOAT"
	case map[string]any:
		field.Type = "RECORD"
		field.Fields = loggingBigQueryFields(v, false)
		if len(field.Fields) == 0 {
			return field, false
		}
	default:
		return field, false
	}
	return field, true
}

// loggingMergeBigQueryFields adds to have the columns want names that it
// lacks, as Cloud Logging widens a table's schema for new payload fields, and
// reports a column whose type differs.
func loggingMergeBigQueryFields(have, want []BQFieldSchema, path string) ([]BQFieldSchema, error) {
	for _, w := range want {
		i := -1
		for j := range have {
			if have[j].Name == w.Name {
				i = j
				break
			}
		}
		if i < 0 {
			have = append(have, w)
			continue
		}
		if have[i].Type != w.Type || have[i].Mode != w.Mode {
			return nil, fmt.Errorf("column %s%s is %s %s, the entry carries %s %s", path, w.Name, have[i].Mode, have[i].Type, w.Mode, w.Type)
		}
		if w.Type == "RECORD" {
			merged, err := loggingMergeBigQueryFields(have[i].Fields, w.Fields, path+w.Name+".")
			if err != nil {
				return nil, err
			}
			have[i].Fields = merged
		}
	}
	return have, nil
}

// loggingExportToBigQuery streams an entry into the table for its log in the
// sink's dataset, creating the table or widening its schema as the entry
// needs.
func loggingExportToBigQuery(rt loggingRoute, entry LogEntry) error {
	project, dataset, ok := loggingBigQueryDataset(rt.target)
	if !ok {
		return fmt.Errorf("destination %s names no dataset", rt.target)
	}
	if _, ok := bqDatasets.Get(bqDatasetKey(project, dataset)); !ok {
		return fmt.Errorf("dataset %s:%s does not exist", project, dataset)
	}
	partitioned := false
	if sink, ok := logSinks.Get(rt.sink); ok && sink.BigqueryOptions != nil {
		partitioned = sink.BigqueryOptions.UsePartitionedTables
	}
	table, err := loggingBigQueryTable(entry, partitioned)
	if err != nil {
		return err
	}
	row, err := loggingBigQueryRow(entry)
	if err != nil {
		return err
	}
	ref := BQTableRef{ProjectID: project, DatasetID: dataset, TableID: table}
	key := bqTableKey(project, dataset, table)
	return bqWriteTable(context.Background(), ref, func() error {
		t, exists := bqTables.Get(key)
		if !exists && partitioned {
			t.TimePartitioning = json.RawMessage(`{"type":"DAY","field":"timestamp"}`)
		}
		if t.Schema == nil {
			t.Schema = &BQSchema{}
		}
		fields, err := loggingMergeBigQueryFields(t.Schema.Fields, loggingBigQueryFields(row, true), "")
		if err != nil {
			return fmt.Errorf("table %s: %w", table, err)
		}
		stored, err := bqCoerceRecord("", fields, row, true)
		if err != nil {
			return fmt.Errorf("table %s: %w", table, err)
		}
		t.Schema.Fields = fields
		rows, _ := bqRows.Get(key)
		rows.Rows = append(rows.Rows, stored)
		bqRows.Put(key, rows)
		bqTables.Put(key, bqApplyTableDefaults(&http.Request{Host: "bigquery.googleapis.com"}, t, project, dataset, table))
		return nil
	})
}

// loggingBigQueryDataset splits projects/P/datasets/D.
func loggingBigQueryDataset(target string) (string, string, bool) {
	parts := strings.Split(target, "/")
	if len(parts) != 4 || parts[0] != "projects" || parts[2] != "datasets" || parts[1] == "" || parts[3] == "" {
		return "", "", false
	}
	return parts[1], parts[3], true
}

// settleBigQueryOptions sets the output-only usesTimestampColumnPartitioning,
// which every sink created now has whenever it uses partitioned tables.
func (s *LoggingSink) settleBigQueryOptions() {
	if s.BigqueryOptions != nil {
		s.BigqueryOptions.UsesTimestampColumnPartitioning = s.BigqueryOptions.UsePartitionedTables
	}
}
