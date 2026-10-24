package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/hamba/avro/v2/ocf"
)

// A load applies its timePartitioning and clustering to the table it creates,
// reads CSV dates and timestamps under its format and zone options, and adds a
// column under ALLOW_FIELD_ADDITION.
func TestBQLoadOptionsShapeTheTable(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	gcpHostOK(t, srv, bqTestHost, http.MethodPost, "/bigquery/v2/projects/p/datasets", `{"datasetReference":{"datasetId":"loads"}}`)
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project=test-project", `{"name":"opts"}`)
	bqTestPutObject(t, srv, "opts", "a.csv", []byte("1,25/12/2008,2008-12-25 07:30:00\n"))
	bqTestPutObject(t, srv, "opts", "b.csv", []byte("2,26/12/2008,2008-12-26 07:30:00,x\n"))

	job := bqTestRunJob(t, srv, `{"configuration":{"load":{"sourceUris":["gs://opts/a.csv"],
		"schema":{"fields":[{"name":"id","type":"INTEGER"},{"name":"d","type":"DATE"},{"name":"at","type":"TIMESTAMP"}]},
		"dateFormat":"%d/%m/%Y","timeZone":"America/Los_Angeles",
		"timePartitioning":{"type":"DAY","field":"at"},"clustering":{"fields":["id"]},
		"destinationTable":{"datasetId":"loads","tableId":"opts"}}}}`)
	if e := bqTestJobError(t, job); e != nil {
		t.Fatalf("load failed: %v", e)
	}
	table := bqTestTable(t, srv, "opts")
	if bqMustJSON(table["timePartitioning"]) != `{"field":"at","type":"DAY"}` || bqMustJSON(table["clustering"]) != `{"fields":["id"]}` {
		t.Fatalf("the created table carries the load's layout: %v %v", table["timePartitioning"], table["clustering"])
	}
	set, _ := bqRows.Get(bqTableKey("p", "loads", "opts"))
	// 07:30 in Los Angeles in December is 15:30 UTC.
	if set.Rows[0]["d"] != "2008-12-25" || set.Rows[0]["at"] != "1230219000000000" {
		t.Fatalf("formats and zone apply to CSV values: %v", set.Rows[0])
	}

	job = bqTestRunJob(t, srv, `{"configuration":{"load":{"sourceUris":["gs://opts/b.csv"],"writeDisposition":"WRITE_APPEND",
		"schema":{"fields":[{"name":"id","type":"INTEGER"},{"name":"d","type":"DATE"},{"name":"at","type":"TIMESTAMP"},{"name":"note","type":"STRING"}]},
		"dateFormat":"%d/%m/%Y","destinationTable":{"datasetId":"loads","tableId":"opts"}}}}`)
	if e := bqTestJobError(t, job); e == nil {
		t.Fatal("a load adding a column without ALLOW_FIELD_ADDITION fails")
	}
	job = bqTestRunJob(t, srv, `{"configuration":{"load":{"sourceUris":["gs://opts/b.csv"],"writeDisposition":"WRITE_APPEND","schemaUpdateOptions":["ALLOW_FIELD_ADDITION"],
		"schema":{"fields":[{"name":"id","type":"INTEGER"},{"name":"d","type":"DATE"},{"name":"at","type":"TIMESTAMP"},{"name":"note","type":"STRING"}]},
		"dateFormat":"%d/%m/%Y","destinationTable":{"datasetId":"loads","tableId":"opts"}}}}`)
	if e := bqTestJobError(t, job); e != nil {
		t.Fatalf("ALLOW_FIELD_ADDITION load failed: %v", e)
	}
	if got := bqTestSchema(bqTestTable(t, srv, "opts")); !strings.Contains(got, `"name":"note"`) {
		t.Fatalf("the added field joins the schema: %s", got)
	}
	if e := bqTestJobError(t, bqTestRunJob(t, srv, `{"configuration":{"load":{"sourceUris":["gs://opts/a.csv"],
		"schema":{"fields":[{"name":"id","type":"INTEGER"},{"name":"d","type":"STRING"},{"name":"at","type":"STRING"}]},
		"timePartitioning":{"type":"DAY","field":"d"},"destinationTable":{"datasetId":"loads","tableId":"badpart"}}}}`)); e == nil || e["reason"] != "invalid" {
		t.Fatalf("partitioning on a STRING column fails: %v", e)
	}
}

// A SNAPSHOT copy makes a read-only SNAPSHOT table carrying its base table,
// a RESTORE copy makes a TABLE of it again, and destinationExpirationTime
// sets the destination's expiration.
func TestBQCopyOperationTypes(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	gcpHostOK(t, srv, bqTestHost, http.MethodPost, "/bigquery/v2/projects/p/datasets", `{"datasetReference":{"datasetId":"loads"}}`)
	gcpHostOK(t, srv, bqTestHost, http.MethodPost, "/bigquery/v2/projects/p/datasets/loads/tables",
		`{"tableReference":{"tableId":"base"},"schema":{"fields":[{"name":"n","type":"INTEGER"}]}}`)
	gcpHostOK(t, srv, bqTestHost, http.MethodPost, "/bigquery/v2/projects/p/datasets/loads/tables/base/insertAll", `{"rows":[{"json":{"n":1}},{"json":{"n":2}}]}`)

	job := bqTestRunJob(t, srv, `{"configuration":{"copy":{"operationType":"SNAPSHOT","sourceTable":{"datasetId":"loads","tableId":"base"},
		"destinationTable":{"datasetId":"loads","tableId":"snap"},"destinationExpirationTime":"2099-01-01T00:00:00Z"}}}`)
	if e := bqTestJobError(t, job); e != nil {
		t.Fatalf("snapshot failed: %v", e)
	}
	snap := bqTestTable(t, srv, "snap")
	def, _ := snap["snapshotDefinition"].(map[string]any)
	if snap["type"] != "SNAPSHOT" || def == nil || def["snapshotTime"] == "" || snap["expirationTime"] != "4070908800000" || snap["numRows"] != "2" {
		t.Fatalf("snapshot table: %v", snap)
	}
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project=test-project", `{"name":"snapsrc"}`)
	bqTestPutObject(t, srv, "snapsrc", "x.csv", []byte("3\n"))
	if e := bqTestJobError(t, bqTestRunJob(t, srv, `{"configuration":{"load":{"sourceUris":["gs://snapsrc/x.csv"],"writeDisposition":"WRITE_APPEND",
		"destinationTable":{"datasetId":"loads","tableId":"snap"}}}}`)); e == nil || e["reason"] != "invalid" {
		t.Fatalf("a snapshot refuses a load: %v", e)
	}
	if e := bqTestJobError(t, bqTestRunJob(t, srv, `{"configuration":{"copy":{"operationType":"RESTORE","sourceTable":{"datasetId":"loads","tableId":"base"},
		"destinationTable":{"datasetId":"loads","tableId":"restored"}}}}`)); e == nil || e["reason"] != "invalid" {
		t.Fatalf("RESTORE needs a snapshot source: %v", e)
	}
	job = bqTestRunJob(t, srv, `{"configuration":{"copy":{"operationType":"RESTORE","sourceTable":{"datasetId":"loads","tableId":"snap"},
		"destinationTable":{"datasetId":"loads","tableId":"restored"}}}}`)
	if e := bqTestJobError(t, job); e != nil {
		t.Fatalf("restore failed: %v", e)
	}
	if restored := bqTestTable(t, srv, "restored"); restored["type"] != "TABLE" || restored["numRows"] != "2" {
		t.Fatalf("restored table: %v", restored)
	}
	job = bqTestRunJob(t, srv, `{"configuration":{"copy":{"operationType":"CLONE","sourceTable":{"datasetId":"loads","tableId":"base"},
		"destinationTable":{"datasetId":"loads","tableId":"clone"}}}}`)
	if clone := bqTestTable(t, srv, "clone"); clone["type"] != "TABLE" || clone["cloneDefinition"] == nil {
		t.Fatalf("clone table: %v %v", clone, bqTestJobError(t, job))
	}
}

// The extract shard writer writes each run of table data up to its limit as
// a complete file of its format.
func TestBQEncodeShardsWritesCompleteFiles(t *testing.T) {
	fields := []BQFieldSchema{{Name: "s", Type: "STRING"}}
	rows := []map[string]any{{"s": "aaaa"}, {"s": "bbbb"}, {"s": "cccc"}}
	// Each row is 2+4 logical bytes, so a 12-byte limit holds two rows.
	files, err := bqEncodeExtract(bqExtractConfig{}, fields, rows, "AVRO", "NONE", ',', 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("%d files, want 2", len(files))
	}
	var counts []int
	for _, data := range files {
		dec, err := ocf.NewDecoder(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("a shard is not a complete Avro file: %v", err)
		}
		n := 0
		for dec.HasNext() {
			var v map[string]any
			if err := dec.Decode(&v); err != nil {
				t.Fatal(err)
			}
			n++
		}
		counts = append(counts, n)
	}
	if counts[0] != 2 || counts[1] != 1 {
		t.Fatalf("rows per shard %v", counts)
	}
	files, err = bqEncodeExtract(bqExtractConfig{}, fields, rows, "PARQUET", "NONE", ',', 12)
	if err != nil || len(files) != 2 || !bytes.HasPrefix(files[1], []byte("PAR1")) || !bytes.HasSuffix(files[1], []byte("PAR1")) {
		t.Fatalf("parquet shards: %d %v", len(files), err)
	}
}

func TestBQDecimalTargetType(t *testing.T) {
	for _, c := range []struct {
		allowed          []string
		precision, scale int
		want             string
	}{
		{[]string{"NUMERIC", "BIGNUMERIC"}, 38, 9, "NUMERIC"},
		{[]string{"NUMERIC", "BIGNUMERIC"}, 39, 9, "BIGNUMERIC"},
		{[]string{"NUMERIC", "BIGNUMERIC"}, 38, 10, "BIGNUMERIC"},
		{[]string{"NUMERIC", "BIGNUMERIC"}, 77, 38, "BIGNUMERIC"},
		{nil, 77, 38, "NUMERIC"},
		{[]string{"NUMERIC", "STRING"}, 77, 38, "STRING"},
	} {
		if got := bqDecimalTargetType(c.allowed, c.precision, c.scale); got != c.want {
			t.Errorf("%v (%d,%d): %s, want %s", c.allowed, c.precision, c.scale, got, c.want)
		}
	}
}

// insertAll keeps one row per insertId and streams into <table><suffix>
// created from the template's schema.
func TestBQInsertAllInsertIDsAndTemplateSuffix(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	gcpHostOK(t, srv, bqTestHost, http.MethodPost, "/bigquery/v2/projects/p/datasets", `{"datasetReference":{"datasetId":"stream"}}`)
	gcpHostOK(t, srv, bqTestHost, http.MethodPost, "/bigquery/v2/projects/p/datasets/stream/tables",
		`{"tableReference":{"tableId":"base"},"schema":{"fields":[{"name":"n","type":"INTEGER"}]}}`)
	insert := `{"rows":[{"insertId":"a","json":{"n":1}},{"insertId":"a","json":{"n":1}},{"insertId":"b","json":{"n":2}}]}`
	gcpHostOK(t, srv, bqTestHost, http.MethodPost, "/bigquery/v2/projects/p/datasets/stream/tables/base/insertAll", insert)
	gcpHostOK(t, srv, bqTestHost, http.MethodPost, "/bigquery/v2/projects/p/datasets/stream/tables/base/insertAll", insert)
	if set, _ := bqRows.Get(bqTableKey("p", "stream", "base")); len(set.Rows) != 2 {
		t.Fatalf("a retried insertId stores no second row: %v", set.Rows)
	}
	gcpHostOK(t, srv, bqTestHost, http.MethodPost, "/bigquery/v2/projects/p/datasets/stream/tables/base/insertAll",
		`{"templateSuffix":"_2024","rows":[{"json":{"n":7}}]}`)
	instance := gcpHostOK(t, srv, bqTestHost, http.MethodGet, "/bigquery/v2/projects/p/datasets/stream/tables/base_2024", ``)
	if instance["numRows"] != "1" || bqTestSchema(instance) != `{"fields":[{"name":"n","type":"INTEGER"}]}` {
		t.Fatalf("the instance table: %v", instance)
	}
	if rec := bqTestSend(t, srv, http.MethodPost, "/bigquery/v2/projects/p/datasets/stream/tables/base/insertAll", nil,
		[]byte(`{"templateSuffix":"bad-suffix","rows":[{"json":{"n":7}}]}`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("an invalid suffix answered %d", rec.Code)
	}
}
