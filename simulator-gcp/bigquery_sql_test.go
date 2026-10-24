package main

import (
	"context"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func withBQQueryTables(t *testing.T) {
	t.Helper()
	tables, rows := bqTables, bqRows
	t.Cleanup(func() { bqTables, bqRows = tables, rows })
	bqTables = sim.MakeStore[BQTable](nil, "test_bq_tables")
	bqRows = sim.MakeStore[BQRowSet](nil, "test_bq_rows")

	people := []BQFieldSchema{
		{Name: "name", Type: "STRING"},
		{Name: "age", Type: "INTEGER"},
		{Name: "active", Type: "BOOLEAN"},
		{Name: "joined", Type: "TIMESTAMP"},
		{Name: "team", Type: "STRING"},
		{Name: "tags", Type: "STRING", Mode: "REPEATED"},
	}
	bqTables.Put(bqTableKey("p", "ds", "people"), BQTable{Schema: &BQSchema{Fields: people}})
	bqRows.Put(bqTableKey("p", "ds", "people"), BQRowSet{Rows: []map[string]any{
		{"name": "ada", "age": "36", "active": "true", "joined": "1700000000000000", "team": "core", "tags": []any{"math"}},
		{"name": "bob", "age": "29", "active": "false", "joined": "1700000100000000", "team": "core", "tags": []any{}},
		{"name": "cy", "age": "41", "active": "true", "joined": "1700000200000000", "team": "edge", "tags": []any{"ops", "net"}},
	}})
	teams := []BQFieldSchema{{Name: "team", Type: "STRING"}, {Name: "floor", Type: "INTEGER"}}
	bqTables.Put(bqTableKey("p", "ds", "teams"), BQTable{Schema: &BQSchema{Fields: teams}})
	bqRows.Put(bqTableKey("p", "ds", "teams"), BQRowSet{Rows: []map[string]any{
		{"team": "core", "floor": "3"}, {"team": "edge", "floor": "7"},
	}})
}

func runBQQuery(t *testing.T, query string) (*BQSchema, []map[string]any) {
	t.Helper()
	schema, rows, err := bqEvaluateSQL(context.Background(), "p", "", query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return schema, rows
}

func TestBQSQLProjectsFiltersOrdersAndLimits(t *testing.T) {
	withBQQueryTables(t)
	schema, rows := runBQQuery(t, "SELECT name, age FROM `p.ds.people` WHERE age > 30 ORDER BY age DESC LIMIT 1")
	if len(schema.Fields) != 2 || schema.Fields[0].Name != "name" || schema.Fields[1].Type != "INTEGER" {
		t.Fatalf("schema %+v", schema.Fields)
	}
	if len(rows) != 1 || rows[0]["name"] != "cy" || rows[0]["age"] != "41" {
		t.Fatalf("rows %v", rows)
	}

	_, rows = runBQQuery(t, "SELECT name, active, joined, tags FROM ds.people WHERE NOT active")
	if len(rows) != 1 || rows[0]["name"] != "bob" || rows[0]["active"] != "false" || rows[0]["joined"] != "1700000100000000" {
		t.Fatalf("typed columns come back in their stored form: %v", rows)
	}

	_, rows = runBQQuery(t, "select * from ds.people where name = 'ada'")
	if len(rows) != 1 || len(rows[0]) != 6 {
		t.Fatalf("SELECT * answers every column: %v", rows)
	}
}

func TestBQSQLAggregatesAndJoins(t *testing.T) {
	withBQQueryTables(t)
	schema, rows := runBQQuery(t, "SELECT team, COUNT(*) AS n, AVG(age) AS mean FROM ds.people GROUP BY team ORDER BY team")
	if schema.Fields[1].Type != "INTEGER" || schema.Fields[2].Type != "FLOAT" {
		t.Fatalf("computed columns take their values' types: %+v", schema.Fields)
	}
	if len(rows) != 2 || rows[0]["n"] != "2" || rows[0]["mean"] != "32.5" || rows[1]["team"] != "edge" {
		t.Fatalf("rows %v", rows)
	}

	_, rows = runBQQuery(t, "SELECT p.name, t.floor FROM ds.people AS p JOIN `p.ds.teams` t ON p.team = t.team WHERE t.floor > 5")
	if len(rows) != 1 || rows[0]["name"] != "cy" || rows[0]["floor"] != "7" {
		t.Fatalf("join rows %v", rows)
	}
	_, rows = runBQQuery(t, "SELECT people.name FROM ds.people, ds.teams WHERE people.team = teams.team AND teams.floor = 3 ORDER BY 1")
	if len(rows) != 2 || rows[0]["name"] != "ada" {
		t.Fatalf("a table referenced without an alias answers by its name: %v", rows)
	}
}

func TestBQSQLRefusesWhatItCannotRun(t *testing.T) {
	withBQQueryTables(t)
	for query, want := range map[string]string{
		"SELECT * FROM ds.missing":                  "Not found: Table p:ds.missing",
		"SELECT * FROM people":                      "must be qualified with a dataset",
		"SELECT nosuchcolumn FROM ds.people":        "no such column",
		"SELECT name FROM ds.people WHERE 'open":    "Unclosed string literal",
		"INSERT INTO ds.people (name) VALUES ('x')": "Expected a query statement",
		"SELECT 1; SELECT 2":                        "Expected end of input",
	} {
		if _, _, err := bqEvaluateSQL(context.Background(), "p", "", query); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", query, err, want)
		}
	}
	if _, rows, err := bqEvaluateSQL(context.Background(), "p", "ds", "SELECT name FROM people ORDER BY name"); err != nil || len(rows) != 3 {
		t.Fatalf("a default dataset qualifies a bare table name: %v %v", rows, err)
	}
}

func TestBQSQLTypesPredicatesBool(t *testing.T) {
	withBQQueryTables(t)
	schema, rows := runBQQuery(t, "SELECT name, age > 30 AS senior, NOT active inactive, age AS years, age + 1 AS next, name IS NULL FROM ds.people ORDER BY name")
	types := map[string]string{}
	for _, f := range schema.Fields {
		types[f.Name] = f.Type
	}
	if types["senior"] != "BOOLEAN" || types["inactive"] != "BOOLEAN" || types["years"] != "INTEGER" || types["next"] != "INTEGER" || types["name"] != "STRING" {
		t.Fatalf("schema %+v", schema.Fields)
	}
	if rows[0]["senior"] != "true" || rows[1]["inactive"] != "true" || rows[1]["senior"] != "false" {
		t.Fatalf("predicates answer BOOL cells: %v", rows)
	}

	schema, _ = runBQQuery(t, "SELECT CASE WHEN age > 30 THEN 'old' ELSE 'young' END AS band, COUNT(*) AS n FROM ds.people GROUP BY band")
	if schema.Fields[0].Type != "STRING" || schema.Fields[1].Type != "INTEGER" {
		t.Fatalf("a comparison inside CASE leaves the item its branches' type: %+v", schema.Fields)
	}
	schema, _ = runBQQuery(t, "SELECT age > 30 AS age FROM ds.people")
	if schema.Fields[0].Type != "BOOLEAN" {
		t.Fatalf("an alias that names a source column does not take its type: %+v", schema.Fields)
	}
}
