package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestResumableChunkPlacement(t *testing.T) {
	for _, tc := range []struct {
		name, contentRange  string
		chunkLen, received  int64
		start, total        int64
		status, wantRefused bool
	}{
		{name: "first chunk", contentRange: "bytes 0-9/*", chunkLen: 10, start: 0, total: -1},
		{name: "next chunk", contentRange: "bytes 10-19/30", chunkLen: 10, received: 10, start: 10, total: 30},
		{name: "retried chunk overlaps", contentRange: "bytes 5-14/*", chunkLen: 10, received: 10, start: 5, total: -1},
		{name: "chunk past the received bytes", contentRange: "bytes 20-29/30", chunkLen: 10, received: 10, wantRefused: true},
		{name: "range disagrees with the body", contentRange: "bytes 0-9/10", chunkLen: 4, wantRefused: true},
		{name: "streamed chunk", contentRange: "bytes 0-9/10", chunkLen: -1, start: 0, total: 10},
		{name: "finish with no bytes", contentRange: "bytes */30", received: 30, start: 30, total: 30},
		{name: "finish carrying bytes", contentRange: "bytes */30", chunkLen: 3, received: 30, wantRefused: true},
		{name: "status", contentRange: "bytes */*", received: 12, start: 12, total: -1, status: true},
		{name: "status carrying bytes", contentRange: "bytes */*", chunkLen: 1, wantRefused: true},
		{name: "whole object", contentRange: "", chunkLen: 7, start: 0, total: 7},
		{name: "malformed", contentRange: "items 0-1/2", chunkLen: 2, wantRefused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, total, status, err := resumableChunkPlacement(tc.contentRange, tc.chunkLen, tc.received)
			if tc.wantRefused {
				if err == nil {
					t.Fatalf("placed at %d of %d, want a refusal", start, total)
				}
				return
			}
			if err != nil || start != tc.start || total != tc.total || status != tc.status {
				t.Fatalf("= (%d, %d, %v, %v), want (%d, %d, %v)", start, total, status, err, tc.start, tc.total, tc.status)
			}
		})
	}
}

// apitools' flow for objects.insert, which gcloud drives past its resumable
// threshold: the session begins on the /resumable/upload path, its chunks are
// PUTs to the session URI, and a status query names the bytes received.
func TestGCSResumableUploadOnResumablePath(t *testing.T) {
	c := newGCSTestClient(t)
	c.createBucket("apitools", "")
	const path = "/resumable/upload/storage/v1/b/apitools/o"
	start := func(name string) string {
		t.Helper()
		resp, out := c.do(http.MethodPost, path+"?uploadType=resumable&alt=json&name="+name,
			map[string]string{"Content-Type": "application/json", "X-Upload-Content-Type": "text/plain"}, []byte(`{"contentType":"text/plain"}`))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("initiate: %d %s", resp.StatusCode, out)
		}
		location, err := url.Parse(resp.Header.Get("Location"))
		if err != nil || location.Path != path || location.Query().Get("upload_id") == "" || location.Query().Get("name") != name {
			t.Fatalf("session URI %q", resp.Header.Get("Location"))
		}
		return location.RequestURI()
	}
	content := []byte(strings.Repeat("apitools chunk\n", 40))
	total := len(content)
	session := start("big.txt")
	put := func(first, last int) *http.Response {
		resp, _ := c.do(http.MethodPut, session, map[string]string{
			"Content-Range": fmt.Sprintf("bytes %d-%d/%d", first, last, total),
		}, content[first:last+1])
		return resp
	}
	if resp := put(0, 199); resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Range") != "bytes=0-199" {
		t.Fatalf("first chunk: %d Range %q", resp.StatusCode, resp.Header.Get("Range"))
	}
	if resp := put(300, 399); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a chunk past the received bytes answered %d", resp.StatusCode)
	}
	resp, _ := c.do(http.MethodPut, session, map[string]string{"Content-Range": "bytes */*"}, nil)
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Range") != "bytes=0-199" {
		t.Fatalf("status: %d Range %q", resp.StatusCode, resp.Header.Get("Range"))
	}
	resp = put(200, total-1)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("last chunk answered %d", resp.StatusCode)
	}
	if got := c.download("apitools", "big.txt"); got != string(content) {
		t.Fatalf("the object holds %d bytes, want %d", len(got), total)
	}
	resp, finished := c.do(http.MethodPut, session, map[string]string{"Content-Range": "bytes */*"}, nil)
	var object map[string]any
	if resp.StatusCode != http.StatusOK || json.Unmarshal(finished, &object) != nil ||
		object["name"] != "big.txt" || object["size"] != fmt.Sprint(total) || object["contentType"] != "text/plain" {
		t.Fatalf("status of a finished session: %d %s", resp.StatusCode, finished)
	}

	session = start("cancelled.txt")
	if resp := put(0, 99); resp.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("chunk answered %d", resp.StatusCode)
	}
	if resp, out := c.do(http.MethodDelete, session, nil, nil); resp.StatusCode != 499 {
		t.Fatalf("cancel: %d %s", resp.StatusCode, out)
	}
	if resp := put(100, 199); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a chunk to a cancelled session answered %d", resp.StatusCode)
	}

	if resp, out := c.do(http.MethodPost, path+"?uploadType=media&name=simple.txt",
		map[string]string{"Content-Type": "text/plain"}, []byte("x")); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a simple upload on the resumable path: %d %s", resp.StatusCode, out)
	}
	if resp, out := c.do(http.MethodPut, path, nil, []byte("x")); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a PUT without upload_id: %d %s", resp.StatusCode, out)
	}
}

const bqTestHost = "bigquery.googleapis.com"

func bqTestSend(t *testing.T, srv *sim.Server, method, target string, headers map[string]string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	if !strings.HasPrefix(target, "http") {
		target = "http://" + bqTestHost + target
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// bqTestJob waits for the job jobs.insert answered with to finish and returns
// it as jobs.get then reports it.
func bqTestJob(t *testing.T, srv *sim.Server, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("jobs.insert answered %d %s", rec.Code, rec.Body)
	}
	var job map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatalf("job %s: %v", rec.Body, err)
	}
	ref, _ := job["jobReference"].(map[string]any)
	project, _ := ref["projectId"].(string)
	jobID, _ := ref["jobId"].(string)
	return bqAwaitJob(t, srv, project, jobID)
}

// bqAwaitJob blocks until the job's run ends and returns it from jobs.get.
func bqAwaitJob(t *testing.T, srv *sim.Server, project, jobID string) map[string]any {
	t.Helper()
	if run, ok := bqRunning.Load(bqJobKey(project, jobID)); ok {
		<-run.(*bqRun).done
	}
	return gcpHostOK(t, srv, bqTestHost, http.MethodGet, "/bigquery/v2/projects/"+project+"/jobs/"+jobID, ``)
}

func bqTestRows(t *testing.T, srv *sim.Server, table string) []any {
	t.Helper()
	out := gcpHostOK(t, srv, bqTestHost, http.MethodGet, "/bigquery/v2/projects/p/datasets/loads/tables/"+table+"/data", ``)
	rows, _ := out["rows"].([]any)
	return rows
}

func bqTestMultipart(job string, data []byte) (string, []byte) {
	var body bytes.Buffer
	body.WriteString("--b\r\nContent-Type: application/json\r\n\r\n" + job + "\r\n--b\r\nContent-Type: application/octet-stream\r\n\r\n")
	body.Write(data)
	body.WriteString("\r\n--b--\r\n")
	return "multipart/related; boundary=b", body.Bytes()
}

// A load job carries its source data on jobs.insert's media paths: in a
// resumable session begun on /resumable/upload (apitools) or /upload (the Go
// client), or in one multipart request.
func TestBQLoadJobFromMedia(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	gcpHostOK(t, srv, bqTestHost, http.MethodPost, "/bigquery/v2/projects/p/datasets", `{"datasetReference":{"datasetId":"loads"}}`)
	const csvJob = `{"jobReference":{"jobId":"load-csv"},"configuration":{"load":{
		"destinationTable":{"projectId":"p","datasetId":"loads","tableId":"people"},
		"schema":{"fields":[{"name":"name","type":"STRING","mode":"REQUIRED"},{"name":"age","type":"INTEGER"}]},
		"skipLeadingRows":1}}}`
	csvData := []byte("name,age\nada,36\ngrace,\nalan,41\n")

	for _, path := range []string{"/resumable/upload/bigquery/v2/projects/p/jobs", "/upload/bigquery/v2/projects/p/jobs"} {
		rec := bqTestSend(t, srv, http.MethodPost, path+"?uploadType=resumable",
			map[string]string{"Content-Type": "application/json", "X-Upload-Content-Type": "text/csv"}, []byte(csvJob))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: start answered %d %s", path, rec.Code, rec.Body)
		}
		session := rec.Header().Get("Location")
		if parsed, err := url.Parse(session); err != nil || parsed.Path != path {
			t.Fatalf("%s: session URI %q", path, session)
		}
		rec = bqTestSend(t, srv, http.MethodPut, session, map[string]string{"Content-Range": fmt.Sprintf("bytes 0-9/%d", len(csvData))}, csvData[:10])
		if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Range") != "bytes=0-9" {
			t.Fatalf("%s: first chunk answered %d Range %q", path, rec.Code, rec.Header().Get("Range"))
		}
		job := bqTestJob(t, srv, bqTestSend(t, srv, http.MethodPut, session,
			map[string]string{"Content-Range": fmt.Sprintf("bytes 10-%d/%d", len(csvData)-1, len(csvData))}, csvData[10:]))
		status, _ := job["status"].(map[string]any)
		stats, _ := job["statistics"].(map[string]any)
		load, _ := stats["load"].(map[string]any)
		if status["state"] != "DONE" || status["errorResult"] != nil || load["outputRows"] != "3" || load["inputFileBytes"] != fmt.Sprint(len(csvData)) {
			t.Fatalf("%s: job %v", path, job)
		}
		if code, out := gcpHostCall(t, srv, bqTestHost, http.MethodDelete, "/bigquery/v2/projects/p/jobs/load-csv/delete", ``); code != http.StatusNoContent {
			t.Fatalf("jobs.delete answered %d %v", code, out)
		}
	}
	rows := bqTestRows(t, srv, "people")
	if len(rows) != 6 {
		t.Fatalf("two appending loads left %d rows: %v", len(rows), rows)
	}
	if first := fmt.Sprint(rows[0]); first != "map[f:[map[v:ada] map[v:36]]]" {
		t.Fatalf("first row %s", first)
	}
	if second := fmt.Sprint(rows[1]); second != "map[f:[map[v:grace] map[v:<nil>]]]" {
		t.Fatalf("an empty CSV field loads as NULL: %s", second)
	}
	table := gcpHostOK(t, srv, bqTestHost, http.MethodGet, "/bigquery/v2/projects/p/datasets/loads/tables/people", ``)
	if table["numRows"] != "6" {
		t.Fatalf("table numRows %v", table["numRows"])
	}

	// WRITE_TRUNCATE replaces the rows; the source is newline-delimited JSON
	// sent in one multipart request.
	contentType, body := bqTestMultipart(`{"configuration":{"load":{"sourceFormat":"NEWLINE_DELIMITED_JSON",
		"destinationTable":{"datasetId":"loads","tableId":"people"},"writeDisposition":"WRITE_TRUNCATE"}}}`,
		[]byte("{\"name\":\"edsger\",\"age\":72}\n\n{\"name\":\"barbara\"}\n"))
	job := bqTestJob(t, srv, bqTestSend(t, srv, http.MethodPost, "/upload/bigquery/v2/projects/p/jobs?uploadType=multipart",
		map[string]string{"Content-Type": contentType}, body))
	if load := job["statistics"].(map[string]any)["load"].(map[string]any); load["outputRows"] != "2" {
		t.Fatalf("multipart load %v", job)
	}
	if rows := bqTestRows(t, srv, "people"); len(rows) != 2 || fmt.Sprint(rows[0]) != "map[f:[map[v:edsger] map[v:72]]]" {
		t.Fatalf("rows after WRITE_TRUNCATE %v", rows)
	}

	// A load that fails on its data finishes DONE with an errorResult and
	// writes nothing.
	failing := func(load string, data string) map[string]any {
		t.Helper()
		contentType, body := bqTestMultipart(`{"configuration":{"load":`+load+`}}`, []byte(data))
		job := bqTestJob(t, srv, bqTestSend(t, srv, http.MethodPost, "/upload/bigquery/v2/projects/p/jobs?uploadType=multipart",
			map[string]string{"Content-Type": contentType}, body))
		status := job["status"].(map[string]any)
		errorResult, _ := status["errorResult"].(map[string]any)
		if status["state"] != "DONE" || errorResult == nil {
			t.Fatalf("load %s finished %v", load, status)
		}
		return errorResult
	}
	if e := failing(`{"sourceFormat":"NEWLINE_DELIMITED_JSON","destinationTable":{"datasetId":"loads","tableId":"people"}}`,
		"{\"name\":\"x\",\"shoe\":9}\n"); e["reason"] != "invalid" || !strings.Contains(fmt.Sprint(e["message"]), "shoe") {
		t.Fatalf("unknown field: %v", e)
	}
	if e := failing(`{"destinationTable":{"datasetId":"loads","tableId":"people"}}`, ",5\n"); e["reason"] != "invalid" {
		t.Fatalf("missing required field: %v", e)
	}
	if e := failing(`{"destinationTable":{"datasetId":"loads","tableId":"people"},"writeDisposition":"WRITE_EMPTY"}`, "x,1\n"); e["reason"] != "duplicate" {
		t.Fatalf("WRITE_EMPTY into a table with rows: %v", e)
	}
	if e := failing(`{"destinationTable":{"datasetId":"loads","tableId":"absent"},"createDisposition":"CREATE_NEVER"}`, "x\n"); e["reason"] != "notFound" {
		t.Fatalf("CREATE_NEVER into a missing table: %v", e)
	}
	if e := failing(`{"destinationTable":{"datasetId":"loads","tableId":"schemaless"}}`, "x\n"); e["reason"] != "invalid" {
		t.Fatalf("a new table with no schema: %v", e)
	}
	if rows := bqTestRows(t, srv, "people"); len(rows) != 2 {
		t.Fatalf("failed loads changed the rows: %v", rows)
	}
	// maxBadRecords lets a load skip that many bad records.
	contentType, body = bqTestMultipart(`{"configuration":{"load":{"destinationTable":{"datasetId":"loads","tableId":"people"},"maxBadRecords":1}}}`,
		[]byte("ok,1\ntoo,many,values\n"))
	job = bqTestJob(t, srv, bqTestSend(t, srv, http.MethodPost, "/upload/bigquery/v2/projects/p/jobs?uploadType=multipart",
		map[string]string{"Content-Type": contentType}, body))
	if load := job["statistics"].(map[string]any)["load"].(map[string]any); load["outputRows"] != "1" || load["badRecords"] != "1" {
		t.Fatalf("load with a tolerated bad record %v", job)
	}

	// The media paths take load jobs only, and the resumable one takes only
	// the resumable protocol.
	contentType, body = bqTestMultipart(`{"configuration":{"query":{"query":"SELECT 1"}}}`, []byte("x"))
	if rec := bqTestSend(t, srv, http.MethodPost, "/upload/bigquery/v2/projects/p/jobs?uploadType=multipart",
		map[string]string{"Content-Type": contentType}, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("a query job with media answered %d %s", rec.Code, rec.Body)
	}
	if rec := bqTestSend(t, srv, http.MethodPost, "/resumable/upload/bigquery/v2/projects/p/jobs?uploadType=multipart",
		map[string]string{"Content-Type": contentType}, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("a multipart request on the resumable path answered %d %s", rec.Code, rec.Body)
	}

	// Cancelling a session answers 499 and forgets it.
	rec := bqTestSend(t, srv, http.MethodPost, "/resumable/upload/bigquery/v2/projects/p/jobs?uploadType=resumable",
		map[string]string{"Content-Type": "application/json"}, []byte(csvJob))
	session := rec.Header().Get("Location")
	if rec := bqTestSend(t, srv, http.MethodDelete, session, nil, nil); rec.Code != 499 {
		t.Fatalf("cancel answered %d", rec.Code)
	}
	if rec := bqTestSend(t, srv, http.MethodPut, session, map[string]string{"Content-Range": "bytes */*"}, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("a cancelled session answered %d", rec.Code)
	}
}
