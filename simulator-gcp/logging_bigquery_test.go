package main

import "testing"

func TestLoggingBigQueryNamesAndTables(t *testing.T) {
	for in, want := range map[string]string{
		"cloudaudit.googleapis.com/activity": "cloudaudit_googleapis_com_activity",
		"request-id":                         "request_id",
		"@type":                              "_type",
		"9lives":                             "_9lives",
		"plain_Name1":                        "plain_Name1",
	} {
		if got := loggingBigQueryName(in); got != want {
			t.Errorf("loggingBigQueryName(%q) = %q, want %q", in, got, want)
		}
	}
	entry := LogEntry{LogName: "projects/p/logs/syslog", Timestamp: "2017-05-23T23:59:59-02:00"}
	if got, err := loggingBigQueryTable(entry, false); err != nil || got != "syslog_20170524" {
		t.Errorf("dated table = %q, %v; the date is the timestamp's UTC date", got, err)
	}
	if got, err := loggingBigQueryTable(entry, true); err != nil || got != "syslog" {
		t.Errorf("partitioned table = %q, %v", got, err)
	}
}

func TestLoggingBigQuerySchemaWidensAndRefusesTypeChanges(t *testing.T) {
	first, _ := loggingBigQueryRow(LogEntry{LogName: "projects/p/logs/a", Timestamp: "2026-01-01T00:00:00Z", JsonPayload: map[string]any{"n": 1.0}})
	second, _ := loggingBigQueryRow(LogEntry{LogName: "projects/p/logs/a", Timestamp: "2026-01-01T00:00:00Z", JsonPayload: map[string]any{"n": 2.0, "tag": "x"}})
	have := loggingBigQueryFields(first, true)
	merged, err := loggingMergeBigQueryFields(have, loggingBigQueryFields(second, true), "")
	if err != nil {
		t.Fatal(err)
	}
	var payload []BQFieldSchema
	for _, f := range merged {
		if f.Name == "timestamp" && f.Type != "TIMESTAMP" {
			t.Errorf("timestamp column is %s", f.Type)
		}
		if f.Name == "jsonPayload" {
			payload = f.Fields
		}
	}
	if len(payload) != 2 {
		t.Fatalf("a new payload field widens the record: %+v", payload)
	}
	third, _ := loggingBigQueryRow(LogEntry{LogName: "projects/p/logs/a", Timestamp: "2026-01-01T00:00:00Z", JsonPayload: map[string]any{"n": "three"}})
	if _, err := loggingMergeBigQueryFields(merged, loggingBigQueryFields(third, true), ""); err == nil {
		t.Fatal("a field whose type changes is refused")
	}
}
