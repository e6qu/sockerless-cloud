package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// withBQGoogleSQLTables holds every BigQuery store in memory and loads an
// orders table with a RECORD and a REPEATED column beside withBQQueryTables'
// people and teams.
func withBQGoogleSQLTables(t *testing.T) {
	t.Helper()
	withBQQueryTables(t)
	datasets, jobs := bqDatasets, bqJobs
	t.Cleanup(func() { bqDatasets, bqJobs = datasets, jobs })
	bqDatasets = sim.MakeStore[BQDataset](nil, "test_bq_datasets")
	bqJobs = sim.MakeStore[storedBQJob](nil, "test_bq_jobs")
	bqDatasets.Put(bqDatasetKey("p", "ds"), BQDataset{DatasetReference: BQDatasetRef{ProjectID: "p", DatasetID: "ds"}})
	orders := []BQFieldSchema{
		{Name: "id", Type: "INTEGER"},
		{Name: "placed", Type: "TIMESTAMP"},
		{Name: "buyer", Type: "RECORD", Fields: []BQFieldSchema{{Name: "name", Type: "STRING"}, {Name: "city", Type: "STRING"}}},
		{Name: "items", Type: "RECORD", Mode: "REPEATED", Fields: []BQFieldSchema{{Name: "sku", Type: "STRING"}, {Name: "qty", Type: "INTEGER"}}},
		{Name: "tags", Type: "STRING", Mode: "REPEATED"},
	}
	bqTables.Put(bqTableKey("p", "ds", "orders"), BQTable{TableReference: BQTableRef{ProjectID: "p", DatasetID: "ds", TableID: "orders"}, Schema: &BQSchema{Fields: orders}})
	bqRows.Put(bqTableKey("p", "ds", "orders"), BQRowSet{Rows: []map[string]any{
		{"id": "1", "placed": "1700000000000000", "buyer": map[string]any{"name": "ada", "city": "oslo"},
			"items": []any{map[string]any{"sku": "a", "qty": "2"}, map[string]any{"sku": "b", "qty": "1"}}, "tags": []any{"x", "y"}},
		{"id": "2", "placed": "1700086400000000", "buyer": map[string]any{"name": "bob", "city": "rome"},
			"items": []any{map[string]any{"sku": "c", "qty": "5"}}, "tags": []any{}},
	}})
}

func runBQStatement(t *testing.T, statement string) *bqStatementResult {
	t.Helper()
	res, err := bqRunStatement(context.Background(), httptest.NewRequest("POST", "/", nil), bqNewScope("p", "ds"), statement, false)
	if err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
	return res
}

func bqColumnTypes(schema *BQSchema) map[string]string {
	types := map[string]string{}
	for _, f := range schema.Fields {
		types[f.Name] = f.Type
		if f.Mode == "REPEATED" {
			types[f.Name] = "ARRAY<" + f.Type + ">"
		}
	}
	return types
}

func TestBQGoogleSQLDateAndTimeFunctions(t *testing.T) {
	withBQGoogleSQLTables(t)
	schema, rows := runBQQuery(t, `SELECT
		TIMESTAMP '2024-01-02 03:04:05.5+01' AS ts,
		DATE '2024-02-29' AS d,
		UNIX_MILLIS(TIMESTAMP '1970-01-01 00:00:01.8 UTC') AS ms,
		UNIX_SECONDS(TIMESTAMP '1970-01-01 00:00:01.8+00') AS secs,
		TIMESTAMP_TRUNC(TIMESTAMP '2008-12-25 15:30:00+00', DAY) AS day,
		DATE_ADD(DATE '2024-01-31', INTERVAL 1 MONTH) AS month_later,
		DATE_DIFF(DATE '2017-10-15', DATE '2017-10-14', WEEK) AS weeks,
		TIMESTAMP_DIFF(TIMESTAMP '2021-05-01 04:54:00+00', TIMESTAMP '2021-05-01 01:00:00+00', HOUR) AS hours,
		EXTRACT(ISOWEEK FROM TIMESTAMP '2009-12-31 12:00:00+00') AS isoweek,
		EXTRACT(DAY FROM TIMESTAMP '2008-12-25 05:30:00+00' AT TIME ZONE 'America/Los_Angeles') AS la_day,
		FORMAT_TIMESTAMP('%b-%d-%Y', TIMESTAMP '2050-12-25 15:30:55+00') AS formatted,
		PARSE_DATE('%A %b %e %Y', 'Thursday Dec 25 2008') AS parsed,
		TIMESTAMP_ADD(TIMESTAMP_SECONDS(0), INTERVAL 10 MINUTE) AS later`)
	types := bqColumnTypes(schema)
	for col, want := range map[string]string{"ts": "TIMESTAMP", "d": "DATE", "ms": "INTEGER", "day": "TIMESTAMP", "month_later": "DATE", "formatted": "STRING", "parsed": "DATE", "later": "TIMESTAMP"} {
		if types[col] != want {
			t.Errorf("%s is %s, want %s", col, types[col], want)
		}
	}
	row := rows[0]
	for col, want := range map[string]any{
		"ts": "1704161045500000", "d": "2024-02-29", "ms": "1800", "secs": "1", "day": "1230163200000000",
		"month_later": "2024-02-29", "weeks": "1", "hours": "3", "isoweek": "53", "la_day": "24",
		"formatted": "Dec-25-2050", "parsed": "2008-12-25", "later": "600000000",
	} {
		if row[col] != want {
			t.Errorf("%s = %v, want %v", col, row[col], want)
		}
	}
	_, rows = runBQQuery(t, "SELECT name FROM ds.people WHERE joined >= TIMESTAMP_SECONDS(1700000100) AND DATE(joined) = DATE '2023-11-14' ORDER BY name")
	if len(rows) != 2 || rows[0]["name"] != "bob" {
		t.Fatalf("a TIMESTAMP column compares against TIMESTAMP functions: %v", rows)
	}
}

func TestBQGoogleSQLSafeFunctionsAndCasts(t *testing.T) {
	withBQGoogleSQLTables(t)
	_, rows := runBQQuery(t, `SELECT
		SAFE_CAST('apple' AS INT64) AS bad, SAFE_CAST('42' AS INT64) AS good, CAST('0x123' AS INT64) AS hex,
		CAST(1.5 AS INT64) AS rounded, SAFE_DIVIDE(20, 0) AS div0, SAFE_DIVIDE(20, 4) AS div,
		SAFE.PARSE_DATE('%Y', 'nope') AS safe_parse, CAST(TIMESTAMP '2020-06-03 00:00:53.11+00' AS STRING) AS ts_text,
		IFNULL(NULL, 'x') AS ifnull, NULLIF(0, 0) AS nullif, 7 / 2 AS ratio`)
	row := rows[0]
	for col, want := range map[string]any{"bad": nil, "good": "42", "hex": "291", "rounded": "2", "div0": nil, "div": "5", "safe_parse": nil,
		"ts_text": "2020-06-03 00:00:53.110+00", "ifnull": "x", "nullif": nil, "ratio": "3.5"} {
		if row[col] != want {
			t.Errorf("%s = %#v, want %#v", col, row[col], want)
		}
	}
	if _, _, err := bqEvaluateSQL(context.Background(), "p", "ds", "SELECT CAST('apple' AS INT64)"); err == nil || !strings.Contains(err.Error(), "Bad int64 value") {
		t.Fatalf("CAST of a bad value fails the query: %v", err)
	}
	if _, _, err := bqEvaluateSQL(context.Background(), "p", "ds", "SELECT PARSE_DATE('%Y', 'nope')"); err == nil || strings.Contains(err.Error(), "Syntax error") {
		t.Fatalf("a function's runtime error is reported as itself: %v", err)
	}
}

func TestBQGoogleSQLStringArrayJSONAndHashFunctions(t *testing.T) {
	withBQGoogleSQLTables(t)
	schema, rows := runBQQuery(t, `SELECT
		STARTS_WITH('coffee', 'co') AS sw, REGEXP_CONTAINS('foo@example.com', r'@example\.com$') AS rc,
		REGEXP_EXTRACT('foo@example.com', r'^([^@]+)') AS user, REGEXP_REPLACE('abc', 'b(.)', r'X\1') AS rr,
		SPLIT('a,b,c') AS parts, ARRAY_LENGTH(['x', 'y']) AS n, ARRAY_TO_STRING(['coffee', 'tea', NULL], '--', 'MISSING') AS joined,
		GENERATE_ARRAY(1, 5, 2) AS odd, ['coffee', 'tea', 'milk'][SAFE_OFFSET(6)] AS missing, ['coffee', 'tea'][ORDINAL(2)] AS second,
		TO_JSON_STRING(STRUCT(1 AS a, 'x' AS b)) AS js, JSON_VALUE('{"name": "Jakob", "age": 6}', '$.age') AS age,
		JSON_QUERY('{"class": {"students": [{"name": "Jane"}]}}', '$.class.students[0]') AS student,
		FARM_FINGERPRINT('') AS fp, TO_HEX(MD5('')) AS md5hex`)
	row := rows[0]
	for col, want := range map[string]any{"sw": "true", "rc": "true", "user": "foo", "rr": "aXc", "n": "2", "joined": "coffee--tea--MISSING",
		"missing": nil, "second": "tea", "js": `{"a":1,"b":"x"}`, "age": "6", "student": `{"name":"Jane"}`,
		"fp": "-7286425919675154353", "md5hex": "d41d8cd98f00b204e9800998ecf8427e"} {
		if row[col] != want {
			t.Errorf("%s = %#v, want %#v", col, row[col], want)
		}
	}
	types := bqColumnTypes(schema)
	if types["parts"] != "ARRAY<STRING>" || types["odd"] != "ARRAY<INTEGER>" || types["sw"] != "BOOLEAN" {
		t.Fatalf("array and BOOL results keep their types: %v", types)
	}
	if got := row["odd"]; len(got.([]any)) != 3 || got.([]any)[2] != "5" {
		t.Fatalf("GENERATE_ARRAY: %v", got)
	}
	_, rows = runBQQuery(t, "SELECT team, STRING_AGG(name, '|' ORDER BY name) AS names, ARRAY_AGG(age ORDER BY age) AS ages, COUNTIF(active) AS active FROM ds.people GROUP BY team ORDER BY team")
	if rows[0]["names"] != "ada|bob" || rows[0]["active"] != "1" || len(rows[0]["ages"].([]any)) != 2 || rows[0]["ages"].([]any)[0] != "29" {
		t.Fatalf("aggregates: %v", rows)
	}
	if _, _, err := bqEvaluateSQL(context.Background(), "p", "ds", "SELECT ['a'][OFFSET(3)]"); err == nil || !strings.Contains(err.Error(), "out of bounds") {
		t.Fatalf("an out-of-range OFFSET fails: %v", err)
	}
}

func TestBQGoogleSQLUnnestAndStructAccess(t *testing.T) {
	withBQGoogleSQLTables(t)
	schema, rows := runBQQuery(t, "SELECT o.id, o.buyer.name AS buyer, item.sku, item.qty, pos FROM ds.orders o, UNNEST(o.items) AS item WITH OFFSET AS pos ORDER BY o.id, pos")
	if len(rows) != 3 || rows[0]["sku"] != "a" || rows[1]["pos"] != "1" || rows[2]["buyer"] != "bob" || rows[2]["qty"] != "5" {
		t.Fatalf("UNNEST rows: %v", rows)
	}
	types := bqColumnTypes(schema)
	if types["qty"] != "INTEGER" || types["sku"] != "STRING" || types["pos"] != "INTEGER" || types["buyer"] != "STRING" {
		t.Fatalf("UNNEST and field access keep the schema's types: %v", types)
	}
	_, rows = runBQQuery(t, "SELECT x FROM UNNEST([10, 20, 30]) AS x WHERE x > 15 ORDER BY x")
	if len(rows) != 2 || rows[0]["x"] != "20" {
		t.Fatalf("UNNEST of a literal array: %v", rows)
	}
	_, rows = runBQQuery(t, "SELECT id FROM ds.orders WHERE 'y' IN UNNEST(tags)")
	if len(rows) != 1 || rows[0]["id"] != "1" {
		t.Fatalf("IN UNNEST: %v", rows)
	}
	schema, rows = runBQQuery(t, "SELECT * FROM ds.orders WHERE buyer.city = 'rome'")
	if len(rows) != 1 || rows[0]["buyer"].(map[string]any)["name"] != "bob" || bqColumnTypes(schema)["items"] != "ARRAY<RECORD>" {
		t.Fatalf("SELECT * keeps nested columns: %v %v", rows, schema)
	}
	_, rows = runBQQuery(t, "SELECT tags[OFFSET(0)] AS first FROM ds.orders WHERE id = 1")
	if rows[0]["first"] != "x" {
		t.Fatalf("subscript into a REPEATED column: %v", rows)
	}
}

func TestBQGoogleSQLQualifyExceptReplace(t *testing.T) {
	withBQGoogleSQLTables(t)
	schema, rows := runBQQuery(t, "SELECT name, team FROM ds.people QUALIFY ROW_NUMBER() OVER (PARTITION BY team ORDER BY age DESC) = 1 ORDER BY team")
	if len(rows) != 2 || rows[0]["name"] != "ada" || rows[1]["name"] != "cy" || len(schema.Fields) != 2 {
		t.Fatalf("QUALIFY keeps one row per team and no hidden column: %v %v", rows, schema.Fields)
	}
	schema, rows = runBQQuery(t, "SELECT * EXCEPT (tags, joined) REPLACE (UPPER(name) AS name, age * 2 AS age) FROM ds.people WHERE name = 'ada'")
	var names []string
	for _, f := range schema.Fields {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "name,age,active,team" || rows[0]["name"] != "ADA" || rows[0]["age"] != "72" {
		t.Fatalf("EXCEPT drops and REPLACE rewrites columns in place: %v %v", names, rows)
	}
}

func TestBQGoogleSQLDMLStatements(t *testing.T) {
	withBQGoogleSQLTables(t)
	res := runBQStatement(t, "INSERT INTO ds.teams (team, floor) VALUES ('ops', 9), ('qa', 2)")
	if res.StatementType != "INSERT" || *res.NumDMLAffected != 2 || res.DMLStats["insertedRowCount"] != "2" {
		t.Fatalf("INSERT statistics: %+v", res)
	}
	res = runBQStatement(t, "UPDATE ds.teams SET floor = floor + 1 WHERE floor < 5")
	if res.StatementType != "UPDATE" || *res.NumDMLAffected != 2 || res.DMLStats["updatedRowCount"] != "2" {
		t.Fatalf("UPDATE statistics: %+v", res)
	}
	res = runBQStatement(t, "DELETE FROM ds.teams WHERE team = 'qa'")
	if res.DMLStats["deletedRowCount"] != "1" {
		t.Fatalf("DELETE statistics: %+v", res)
	}
	set, _ := bqRows.Get(bqTableKey("p", "ds", "teams"))
	floors := map[any]any{}
	for _, row := range set.Rows {
		floors[row["team"]] = row["floor"]
	}
	if len(set.Rows) != 3 || floors["core"] != "4" || floors["ops"] != "9" {
		t.Fatalf("the table holds what the DML left: %v", set.Rows)
	}
	res = runBQStatement(t, `MERGE ds.teams T USING (SELECT 'core' AS team, 1 AS floor UNION ALL SELECT 'new', 6) S ON T.team = S.team
		WHEN MATCHED THEN UPDATE SET floor = S.floor
		WHEN NOT MATCHED THEN INSERT (team, floor) VALUES (S.team, S.floor)
		WHEN NOT MATCHED BY SOURCE AND T.team = 'ops' THEN DELETE`)
	if res.StatementType != "MERGE" || res.DMLStats["insertedRowCount"] != "1" || res.DMLStats["updatedRowCount"] != "1" || res.DMLStats["deletedRowCount"] != "1" {
		t.Fatalf("MERGE statistics: %+v", res.DMLStats)
	}
	_, rows := runBQQuery(t, "SELECT team, floor FROM ds.teams ORDER BY team")
	if len(rows) != 3 || rows[0]["floor"] != "1" || rows[1]["team"] != "edge" || rows[2]["team"] != "new" {
		t.Fatalf("MERGE result: %v", rows)
	}
	if _, err := bqRunStatement(context.Background(), httptest.NewRequest("POST", "/", nil), bqNewScope("p", "ds"), "DELETE FROM ds.teams", false); err == nil || !strings.Contains(err.Error(), "WHERE") {
		t.Fatalf("DELETE without WHERE is refused: %v", err)
	}
	res = runBQStatement(t, "TRUNCATE TABLE ds.teams")
	if res.StatementType != "TRUNCATE_TABLE" || res.DMLStats["deletedRowCount"] != "3" {
		t.Fatalf("TRUNCATE statistics: %+v", res)
	}
}

func TestBQGoogleSQLDDLStatements(t *testing.T) {
	withBQGoogleSQLTables(t)
	res := runBQStatement(t, `CREATE TABLE ds.events (id INT64 NOT NULL, at TIMESTAMP, tags ARRAY<STRING>, who STRUCT<name STRING, age INT64>)
		PARTITION BY DATE(at) CLUSTER BY id OPTIONS (description = 'event log', labels = [('env', 'test')])`)
	if res.StatementType != "CREATE_TABLE" || res.DDLOperation != "CREATE" || res.DDLTarget.TableID != "events" {
		t.Fatalf("CREATE TABLE statistics: %+v", res)
	}
	table, _ := bqTables.Get(bqTableKey("p", "ds", "events"))
	if string(table.TimePartitioning) != `{"field":"at","type":"DAY"}` || string(table.Clustering) != `{"fields":["id"]}` || table.Description != "event log" || table.Labels["env"] != "test" {
		t.Fatalf("CREATE TABLE options: %+v", table)
	}
	if f := table.Schema.Fields; f[0].Mode != "REQUIRED" || f[2].Mode != "REPEATED" || f[3].Type != "RECORD" || len(f[3].Fields) != 2 {
		t.Fatalf("CREATE TABLE schema: %+v", f)
	}
	if res = runBQStatement(t, "CREATE TABLE IF NOT EXISTS ds.events (x INT64)"); res.DDLOperation != "SKIP" {
		t.Fatalf("IF NOT EXISTS over an existing table skips: %+v", res)
	}
	res = runBQStatement(t, "CREATE OR REPLACE TABLE ds.seniors AS SELECT name AS name, age FROM ds.people WHERE age > 30")
	if res.StatementType != "CREATE_TABLE_AS_SELECT" || res.DDLOperation != "CREATE" {
		t.Fatalf("CTAS statistics: %+v", res)
	}
	if res = runBQStatement(t, "CREATE OR REPLACE TABLE ds.seniors AS SELECT name FROM ds.people"); res.DDLOperation != "REPLACE" {
		t.Fatalf("OR REPLACE over an existing table replaces: %+v", res)
	}
	runBQStatement(t, "ALTER TABLE ds.seniors ADD COLUMN note STRING, ADD COLUMN IF NOT EXISTS name STRING")
	_, rows := runBQQuery(t, "SELECT name, note FROM ds.seniors ORDER BY name")
	if len(rows) != 3 || rows[0]["note"] != nil {
		t.Fatalf("ALTER TABLE ADD COLUMN leaves NULLs: %v", rows)
	}
	runBQStatement(t, "CREATE VIEW ds.edge AS SELECT name FROM ds.people WHERE team = 'edge'")
	_, rows = runBQQuery(t, "SELECT name FROM ds.edge")
	if len(rows) != 1 || rows[0]["name"] != "cy" {
		t.Fatalf("a view runs its query: %v", rows)
	}
	if res = runBQStatement(t, "DROP TABLE ds.seniors"); res.StatementType != "DROP_TABLE" || res.DDLOperation != "DROP" {
		t.Fatalf("DROP TABLE statistics: %+v", res)
	}
	if _, ok := bqTables.Get(bqTableKey("p", "ds", "seniors")); ok {
		t.Fatal("DROP TABLE deletes the table")
	}
	if res = runBQStatement(t, "DROP TABLE IF EXISTS ds.seniors"); res.DDLOperation != "SKIP" {
		t.Fatalf("DROP IF EXISTS of a missing table skips: %+v", res)
	}
}

func TestBQGoogleSQLScriptsRunChildJobs(t *testing.T) {
	withBQGoogleSQLTables(t)
	cfg := bqQueryConfig{Query: `DECLARE threshold INT64 DEFAULT 30;
		SET threshold = threshold + 5;
		IF threshold > 100 THEN
			SELECT 'never' AS name;
		ELSE
			INSERT INTO ds.teams (team, floor) VALUES ('script', threshold);
		END IF;
		SELECT name FROM ds.people WHERE age > threshold ORDER BY name;`}
	run, err := bqRunQueryJob(context.Background(), httptest.NewRequest("POST", "/", nil), "p", "parent", "", map[string]any{"query": map[string]any{"query": cfg.Query}}, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	stats := run.job.Statistics
	if stats["query"].(map[string]any)["statementType"] != "SCRIPT" || stats["numChildJobs"] != "5" {
		t.Fatalf("script statistics: %v", stats)
	}
	if len(run.job.ResultRows) != 2 || run.job.ResultRows[0]["name"] != "ada" {
		t.Fatalf("the script's result is its last statement's: %v", run.job.ResultRows)
	}
	children := bqJobs.Filter(func(j storedBQJob) bool { return j.Statistics["parentJobId"] == "parent" })
	if len(children) != 5 {
		t.Fatalf("each statement and expression runs as a child job: %d", len(children))
	}
	kinds := map[string]int{}
	for _, c := range children {
		kinds[c.Statistics["scriptStatistics"].(map[string]any)["evaluationKind"].(string)]++
	}
	if kinds["STATEMENT"] != 2 || kinds["EXPRESSION"] != 3 {
		t.Fatalf("child job kinds: %v", kinds)
	}
	_, rows := runBQQuery(t, "SELECT floor FROM ds.teams WHERE team = 'script'")
	if len(rows) != 1 || rows[0]["floor"] != "35" {
		t.Fatalf("a variable's value reaches the DML statement: %v", rows)
	}
}

func TestBQGoogleSQLDestinationlessQueryWritesAnAnonymousTable(t *testing.T) {
	withBQGoogleSQLTables(t)
	f := false
	cfg := bqQueryConfig{Query: "SELECT name FROM ds.people WHERE age > 30", UseLegacySQL: &f}
	run, err := bqRunQueryJob(context.Background(), httptest.NewRequest("POST", "/", nil), "p", "anon-job", "", map[string]any{"query": map[string]any{"query": cfg.Query}}, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	dest, ok := run.job.Configuration["query"].(map[string]any)["destinationTable"].(BQTableRef)
	if !ok || !strings.HasPrefix(dest.DatasetID, "_") || !strings.HasPrefix(dest.TableID, "anon") {
		t.Fatalf("the job reports its anonymous destination: %v", run.job.Configuration)
	}
	table, ok := bqLiveTable(bqTableKey(dest.ProjectID, dest.DatasetID, dest.TableID))
	if !ok || table.ExpirationTime == "" {
		t.Fatalf("the anonymous table exists and expires: %+v", table)
	}
	set, _ := bqRows.Get(bqTableKey(dest.ProjectID, dest.DatasetID, dest.TableID))
	if len(set.Rows) != 2 {
		t.Fatalf("the anonymous table holds the result: %v", set.Rows)
	}
}

func TestBQTimestampOutputFormats(t *testing.T) {
	for format, want := range map[string]any{
		"FLOAT64":        "1.7000000001234E9",
		"INT64":          "1700000000123400",
		"ISO8601_STRING": "2023-11-14T22:13:20.123400000000Z",
	} {
		fo, err := bqFormatOptionsOf(format, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := fo.timestampCell("1700000000123400"); got != want {
			t.Errorf("%s: %v, want %v", format, got, want)
		}
	}
	if fo, _ := bqFormatOptionsOf("", true); fo.timestampCell("-1500000") != "-1500000" {
		t.Error("useInt64Timestamp answers microseconds")
	}
	if fo, _ := bqFormatOptionsOf("", false); fo.timestampCell("-1500000") != "-1.5E0" || fo.timestampCell("0") != "0E0" {
		t.Errorf("the default is FLOAT64 seconds: %v %v", fo.timestampCell("-1500000"), fo.timestampCell("0"))
	}
}
