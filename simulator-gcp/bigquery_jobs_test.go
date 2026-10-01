package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func bqTestPutObject(t *testing.T, srv *sim.Server, bucket, name string, data []byte) {
	t.Helper()
	rec := bqTestSend(t, srv, http.MethodPost,
		"http://storage.googleapis.com/upload/storage/v1/b/"+bucket+"/o?uploadType=media&name="+name,
		map[string]string{"Content-Type": "application/octet-stream"}, data)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload %s/%s answered %d %s", bucket, name, rec.Code, rec.Body)
	}
}

func bqTestGetObject(t *testing.T, bucket, name string) []byte {
	t.Helper()
	data, err := GCSObjectBytes(bucket, name)
	if err != nil {
		t.Fatalf("read %s/%s: %v", bucket, name, err)
	}
	return data
}

// bqTestRunJob inserts a job, checks jobs.insert answered it PENDING, and
// returns it finished.
func bqTestRunJob(t *testing.T, srv *sim.Server, body string) map[string]any {
	t.Helper()
	rec := bqTestSend(t, srv, http.MethodPost, "/bigquery/v2/projects/p/jobs", map[string]string{"Content-Type": "application/json"}, []byte(body))
	if rec.Code == http.StatusOK && !strings.Contains(rec.Body.String(), `"state":"PENDING"`) {
		t.Fatalf("jobs.insert answered a job that is not PENDING: %s", rec.Body)
	}
	job := bqTestJob(t, srv, rec)
	if status := job["status"].(map[string]any); status["state"] != "DONE" {
		t.Fatalf("job did not finish: %v", job)
	}
	return job
}

func bqTestJobError(t *testing.T, job map[string]any) map[string]any {
	t.Helper()
	e, _ := job["status"].(map[string]any)["errorResult"].(map[string]any)
	return e
}

func bqTestStats(job map[string]any, kind string) map[string]any {
	stats, _ := job["statistics"].(map[string]any)[kind].(map[string]any)
	return stats
}

func bqTestGunzip(t *testing.T, data []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func bqTestTable(t *testing.T, srv *sim.Server, table string) map[string]any {
	t.Helper()
	return gcpHostOK(t, srv, bqTestHost, http.MethodGet, "/bigquery/v2/projects/p/datasets/loads/tables/"+table, ``)
}

func bqTestSchema(table map[string]any) string {
	var schema BQSchema
	if err := json.Unmarshal([]byte(bqMustJSON(table["schema"])), &schema); err != nil {
		return err.Error()
	}
	return bqMustJSON(schema)
}

// Load, copy and extract jobs read and write real data: a load reads the
// Cloud Storage objects its URIs match, a copy moves rows between tables, and
// an extract writes the rows back to Cloud Storage.
func TestBQJobsMoveDataThroughCloudStorage(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	gcpHostOK(t, srv, bqTestHost, http.MethodPost, "/bigquery/v2/projects/p/datasets", `{"datasetReference":{"datasetId":"loads"}}`)
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project=p", `{"name":"data"}`)

	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	_, _ = zw.Write([]byte("name,age\ngrace,41\n"))
	_ = zw.Close()
	bqTestPutObject(t, srv, "data", "people/part-1.csv", []byte("name,age\nada,36\n"))
	bqTestPutObject(t, srv, "data", "people/part-2.csv", zipped.Bytes())
	bqTestPutObject(t, srv, "data", "people/readme.txt", []byte("not matched"))

	job := bqTestRunJob(t, srv, `{"configuration":{"load":{"sourceUris":["gs://data/people/part-*"],"autodetect":true,
		"destinationTable":{"datasetId":"loads","tableId":"people"}}}}`)
	if e := bqTestJobError(t, job); e != nil {
		t.Fatalf("load failed: %v", e)
	}
	if load := bqTestStats(job, "load"); load["inputFiles"] != "2" || load["outputRows"] != "2" || load["outputBytes"] != "28" {
		t.Fatalf("load statistics %v", load)
	}
	table := bqTestTable(t, srv, "people")
	if got := bqTestSchema(table); got != `{"fields":[{"name":"name","type":"STRING","mode":"NULLABLE"},{"name":"age","type":"INTEGER","mode":"NULLABLE"}]}` {
		t.Fatalf("detected schema %s", got)
	}
	// STRING is 2 bytes plus its length, INT64 8 bytes: ada,36 and grace,41.
	if table["numRows"] != "2" || table["numBytes"] != "28" {
		t.Fatalf("table size numRows=%v numBytes=%v", table["numRows"], table["numBytes"])
	}

	if e := bqTestJobError(t, bqTestRunJob(t, srv, `{"configuration":{"load":{"sourceUris":["gs://data/absent-*"],
		"destinationTable":{"datasetId":"loads","tableId":"people"}}}}`)); e["reason"] != "notFound" {
		t.Fatalf("a pattern matching nothing: %v", e)
	}
	if rec := bqTestSend(t, srv, http.MethodPost, "/bigquery/v2/projects/p/jobs", nil,
		[]byte(`{"configuration":{"load":{"destinationTable":{"datasetId":"loads","tableId":"people"}}}}`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("a load without sourceUris or media answered %d", rec.Code)
	}
	if rec := bqTestSend(t, srv, http.MethodPost, "/bigquery/v2/projects/p/jobs", nil,
		[]byte(`{"configuration":{"load":{"sourceFormat":"ORC","sourceUris":["gs://data/x"],"destinationTable":{"datasetId":"loads","tableId":"people"}}}}`)); rec.Code != http.StatusNotImplemented {
		t.Fatalf("an ORC load answered %d", rec.Code)
	}

	// Newline-delimited JSON detects nested and repeated fields.
	bqTestPutObject(t, srv, "data", "events.json", []byte(
		`{"id":1,"tags":["a","b"],"meta":{"k":"v","n":2.5},"at":"2024-01-02 03:04:05 UTC","ok":true}`+"\n"+
			`{"id":2,"tags":[],"meta":{"k":"w"},"at":"2024-01-03T00:00:00Z"}`+"\n"))
	job = bqTestRunJob(t, srv, `{"configuration":{"load":{"sourceUris":["gs://data/events.json"],"sourceFormat":"NEWLINE_DELIMITED_JSON",
		"autodetect":true,"destinationTable":{"datasetId":"loads","tableId":"events"}}}}`)
	if e := bqTestJobError(t, job); e != nil {
		t.Fatalf("JSON load failed: %v", e)
	}
	wantEvents := `{"fields":[{"name":"id","type":"INTEGER","mode":"NULLABLE"},{"name":"tags","type":"STRING","mode":"REPEATED"},` +
		`{"name":"meta","type":"RECORD","mode":"NULLABLE","fields":[{"name":"k","type":"STRING","mode":"NULLABLE"},{"name":"n","type":"FLOAT","mode":"NULLABLE"}]},` +
		`{"name":"at","type":"TIMESTAMP","mode":"NULLABLE"},{"name":"ok","type":"BOOLEAN","mode":"NULLABLE"}]}`
	if got := bqTestSchema(bqTestTable(t, srv, "events")); got != wantEvents {
		t.Fatalf("detected schema %s", got)
	}

	// A copy writes the rows of tables sharing a schema; the default
	// WRITE_EMPTY refuses a destination that holds rows.
	job = bqTestRunJob(t, srv, `{"configuration":{"copy":{"sourceTables":[{"datasetId":"loads","tableId":"people"},{"datasetId":"loads","tableId":"people"}],
		"destinationTable":{"datasetId":"loads","tableId":"people_copy"}}}}`)
	if e := bqTestJobError(t, job); e != nil {
		t.Fatalf("copy failed: %v", e)
	}
	if c := bqTestStats(job, "copy"); c["copiedRows"] != "4" || c["copiedLogicalBytes"] != "56" {
		t.Fatalf("copy statistics %v", c)
	}
	if got := bqTestTable(t, srv, "people_copy"); got["numRows"] != "4" || bqTestSchema(got) != bqTestSchema(table) {
		t.Fatalf("copied table %v", got)
	}
	if e := bqTestJobError(t, bqTestRunJob(t, srv, `{"configuration":{"copy":{"sourceTable":{"datasetId":"loads","tableId":"people"},
		"destinationTable":{"datasetId":"loads","tableId":"people_copy"}}}}`)); e["reason"] != "duplicate" {
		t.Fatalf("WRITE_EMPTY copy into a table with rows: %v", e)
	}
	if e := bqTestJobError(t, bqTestRunJob(t, srv, `{"configuration":{"copy":{"sourceTables":[{"datasetId":"loads","tableId":"people"},{"datasetId":"loads","tableId":"events"}],
		"destinationTable":{"datasetId":"loads","tableId":"mixed"}}}}`)); e["reason"] != "invalid" {
		t.Fatalf("copying tables of different schemas: %v", e)
	}
	job = bqTestRunJob(t, srv, `{"configuration":{"copy":{"sourceTable":{"datasetId":"loads","tableId":"people"},"writeDisposition":"WRITE_TRUNCATE",
		"destinationTable":{"datasetId":"loads","tableId":"people_copy"}}}}`)
	if got := bqTestTable(t, srv, "people_copy"); bqTestJobError(t, job) != nil || got["numRows"] != "2" {
		t.Fatalf("WRITE_TRUNCATE copy: %v %v", job, got)
	}

	// An extract writes CSV, compressed and sharded through the wildcard.
	job = bqTestRunJob(t, srv, `{"configuration":{"extract":{"sourceTable":{"datasetId":"loads","tableId":"people"},
		"destinationUris":["gs://data/out/people-*.csv.gz"],"compression":"GZIP"}}}`)
	if e := bqTestJobError(t, job); e != nil {
		t.Fatalf("extract failed: %v", e)
	}
	if x := bqTestStats(job, "extract"); fmt.Sprint(x["destinationUriFileCounts"]) != "[1]" || x["inputBytes"] != "28" {
		t.Fatalf("extract statistics %v", x)
	}
	if got := bqTestGunzip(t, bqTestGetObject(t, "data", "out/people-000000000000.csv.gz")); got != "name,age\nada,36\ngrace,41\n" {
		t.Fatalf("extracted CSV %q", got)
	}
	bqTestRunJob(t, srv, `{"configuration":{"extract":{"sourceTable":{"datasetId":"loads","tableId":"events"},
		"destinationUris":["gs://data/out/events.json"],"destinationFormat":"NEWLINE_DELIMITED_JSON"}}}`)
	wantJSON := `{"id":"1","tags":["a","b"],"meta":{"k":"v","n":2.5},"at":"2024-01-02 03:04:05 UTC","ok":true}` + "\n" +
		`{"id":"2","tags":[],"meta":{"k":"w"},"at":"2024-01-03 00:00:00 UTC"}` + "\n"
	if got := string(bqTestGetObject(t, "data", "out/events.json")); got != wantJSON {
		t.Fatalf("extracted JSON\n%s\nwant\n%s", got, wantJSON)
	}
	if e := bqTestJobError(t, bqTestRunJob(t, srv, `{"configuration":{"extract":{"sourceTable":{"datasetId":"loads","tableId":"events"},
		"destinationUris":["gs://data/out/events.csv"]}}}`)); e["reason"] != "invalid" {
		t.Fatalf("a CSV extract of a nested table: %v", e)
	}

	// Avro and Parquet extracts load back into the same schema and rows.
	for _, format := range []struct{ name, compression string }{{"AVRO", "DEFLATE"}, {"PARQUET", "SNAPPY"}} {
		uri := "gs://data/out/events." + strings.ToLower(format.name)
		job = bqTestRunJob(t, srv, `{"configuration":{"extract":{"sourceTable":{"datasetId":"loads","tableId":"events"},
			"destinationUris":["`+uri+`"],"destinationFormat":"`+format.name+`","compression":"`+format.compression+`","useAvroLogicalTypes":true}}}`)
		if e := bqTestJobError(t, job); e != nil {
			t.Fatalf("%s extract failed: %v", format.name, e)
		}
		reloaded := "events_" + strings.ToLower(format.name)
		load := `{"sourceUris":["` + uri + `"],"sourceFormat":"` + format.name + `","useAvroLogicalTypes":true,"destinationTable":{"datasetId":"loads","tableId":"` + reloaded + `"}}`
		if format.name == "PARQUET" {
			load = strings.Replace(load, `"useAvroLogicalTypes":true`, `"parquetOptions":{"enableListInference":true}`, 1)
		}
		job = bqTestRunJob(t, srv, `{"configuration":{"load":`+load+`}}`)
		if e := bqTestJobError(t, job); e != nil {
			t.Fatalf("%s load failed: %v", format.name, e)
		}
		if got := bqTestSchema(bqTestTable(t, srv, reloaded)); got != wantEvents {
			t.Fatalf("%s schema\n%s\nwant\n%s", format.name, got, wantEvents)
		}
		want, _ := bqRows.Get(bqTableKey("p", "loads", "events"))
		got, _ := bqRows.Get(bqTableKey("p", "loads", reloaded))
		if !reflect.DeepEqual(bqMustJSON(got.Rows), bqMustJSON(want.Rows)) {
			t.Fatalf("%s rows\n%s\nwant\n%s", format.name, bqMustJSON(got.Rows), bqMustJSON(want.Rows))
		}
	}
}

// A running job stops when it is cancelled or when its jobTimeoutMs elapses,
// and finishes DONE with the reason.
func TestBQJobStopsOnCancelAndTimeout(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	req := httpRequestFor(t)
	started := make(chan struct{})
	waitForStop := func(ctx context.Context, stats map[string]any) error {
		close(started)
		<-ctx.Done()
		return context.Cause(ctx)
	}
	job, err := bqStartJob(bqNewJob(req, "p", BQJob{JobReference: BQJobRef{JobID: "held"}}), "load", waitForStop)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status["state"] != "PENDING" {
		t.Fatalf("a started job is %v", job.Status)
	}
	<-started
	if running := gcpHostOK(t, srv, bqTestHost, http.MethodGet, "/bigquery/v2/projects/p/jobs/held", ``); running["status"].(map[string]any)["state"] != "RUNNING" {
		t.Fatalf("a job carrying out its work is %v", running["status"])
	}
	gcpHostOK(t, srv, bqTestHost, http.MethodPost, "/bigquery/v2/projects/p/jobs/held/cancel", ``)
	if e := bqTestJobError(t, bqAwaitJob(t, srv, "p", "held")); e["reason"] != "stopped" {
		t.Fatalf("a cancelled job finished with %v", e)
	}
	if rec := bqTestSend(t, srv, http.MethodPost, "/bigquery/v2/projects/p/jobs", nil,
		[]byte(`{"jobReference":{"jobId":"held"},"configuration":{"query":{"query":"SELECT 1"}}}`)); rec.Code != http.StatusConflict {
		t.Fatalf("reusing a job ID answered %d", rec.Code)
	}

	started = make(chan struct{})
	if _, err := bqStartJob(bqNewJob(req, "p", BQJob{JobReference: BQJobRef{JobID: "timed"},
		Configuration: map[string]any{"jobTimeoutMs": "1"}}), "load", waitForStop); err != nil {
		t.Fatal(err)
	}
	if e := bqTestJobError(t, bqAwaitJob(t, srv, "p", "timed")); e["reason"] != "timeout" {
		t.Fatalf("a job past its jobTimeoutMs finished with %v", e)
	}
}

func httpRequestFor(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+bqTestHost+"/bigquery/v2/projects/p/jobs", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// A restart settles the jobs it caught unfinished.
func TestRecoverBQJobsFinishesInterruptedJobs(t *testing.T) {
	buildOperationsTestSimulator(t)
	bqJobs.Put(bqJobKey("p", "orphan"), storedBQJob{BQJob: BQJob{JobReference: BQJobRef{ProjectID: "p", JobID: "orphan"},
		Status: map[string]any{"state": "RUNNING"}, Statistics: map[string]any{"creationTime": "1"}}})
	recoverBQJobs()
	job, _ := bqJobs.Get(bqJobKey("p", "orphan"))
	if job.Status["state"] != "DONE" || job.Status["errorResult"] == nil || job.Statistics["endTime"] == nil {
		t.Fatalf("recovered job %v", job.BQJob)
	}
}

func TestBQCellSize(t *testing.T) {
	schema := []BQFieldSchema{
		{Name: "s", Type: "STRING"}, {Name: "i", Type: "INTEGER"}, {Name: "b", Type: "BOOLEAN"},
		{Name: "n", Type: "NUMERIC"}, {Name: "bn", Type: "BIGNUMERIC"}, {Name: "by", Type: "BYTES"},
		{Name: "g", Type: "GEOGRAPHY"}, {Name: "r", Type: "RECORD", Fields: []BQFieldSchema{{Name: "f", Type: "FLOAT"}}},
		{Name: "a", Type: "INT64", Mode: "REPEATED"}, {Name: "null", Type: "STRING"},
	}
	row := map[string]any{"s": "héllo", "i": "1", "b": "true", "n": "1.5", "bn": "2", "by": "AAEC",
		"g": "LINESTRING(1 2, 3 4)", "r": map[string]any{"f": "1.5"}, "a": []any{"1", "2", "3"}, "null": nil}
	// 2+6 + 8 + 1 + 16 + 32 + 2+3 + 16+24*2 + 8 + 3*8 + 0
	if got := bqRowSize(schema, row); got != 166 {
		t.Fatalf("row size %d", got)
	}
}

func TestBQSplitCSV(t *testing.T) {
	cases := []struct {
		name           string
		text           string
		quote          rune
		quotedNewlines bool
		want           string
	}{
		{"quoted separator and doubled quote", "a,\"b,\"\"c\"\"\"\n", '"', false, `[{1 [a b,"c"] }]`},
		{"quoted newline refused", "\"a\nb\",c\nd,e\n", '"', false, `[{1 [a] Missing close double quote (") character.} {2 [b" c] } {3 [d e] }]`},
		{"quoted newline allowed", "\"a\nb\",c\nd,e\n", '"', true, "[{1 [a\nb c] } {3 [d e] }]"},
		{"quoting off", "\"a\",b\r\n\n", 0, false, `[{1 ["a" b] }]`},
		{"data after a closing quote", "\"a\"x,b\nc,d", '"', false, `[{1 [a] Data between close double quote (") and field separator.} {2 [c d] }]`},
	}
	for _, c := range cases {
		if got := fmt.Sprint(bqSplitCSV(c.text, ',', c.quote, c.quotedNewlines)); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestBQCanonicalValues(t *testing.T) {
	cases := []struct {
		typ  string
		in   any
		want any
	}{
		{"INT64", "42", "42"}, {"INT64", float64(7), "7"}, {"FLOAT64", "1.50", "1.5"}, {"BOOL", "T", "true"},
		{"NUMERIC", "1.1234567891", "1.123456789"}, {"DATE", "2024-1-2", "2024-01-02"},
		{"TIMESTAMP", "2024-01-02 03:04:05.5+01:00", "1704161045500000"}, {"TIMESTAMP", "2024-01-02", "1704153600000000"},
		{"DATETIME", "2024-01-02 03:04:05", "2024-01-02T03:04:05"}, {"TIME", "03:04:05.250", "03:04:05.25"},
		{"JSON", `{"a": [1, 2]}`, `{"a":[1,2]}`},
	}
	for _, c := range cases {
		got, err := bqCanonical(c.typ, c.in)
		if err != nil || got != c.want {
			t.Errorf("%s %v: %v %v, want %v", c.typ, c.in, got, err, c.want)
		}
	}
	for _, bad := range []struct{ typ, in string }{{"INT64", "1.5"}, {"BOOL", "maybe"}, {"NUMERIC", "1e40"}, {"DATE", "2024-13-01"}} {
		if _, err := bqCanonical(bad.typ, bad.in); err == nil {
			t.Errorf("%s accepted %q", bad.typ, bad.in)
		}
	}
}
