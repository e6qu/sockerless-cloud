package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	kmspb "cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Google Cloud answers a page token it never issued with INVALID_ARGUMENT.
func TestGCPPaginateListRejectsForeignToken(t *testing.T) {
	items := []string{"a", "b", "c"}
	for _, tok := range []string{"x", "-1", "Mg==", "99"} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/x?pageSize=1&pageToken="+tok, nil)
		if _, _, ok := paginateList(rec, r, items); ok {
			t.Fatalf("token %q accepted", tok)
		}
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_ARGUMENT") {
			t.Errorf("token %q: got %d %s", tok, rec.Code, rec.Body.String())
		}
	}
	var got []string
	tok := ""
	for {
		rec := httptest.NewRecorder()
		page, next, ok := paginateList(rec, httptest.NewRequest("GET", "/x?pageSize=2&pageToken="+tok, nil), items)
		if !ok {
			t.Fatalf("issued token %q rejected: %s", tok, rec.Body.String())
		}
		got = append(got, page...)
		if next == "" {
			break
		}
		tok = next
	}
	if strings.Join(got, "") != "abc" {
		t.Fatalf("paging lost or repeated items: %v", got)
	}
}

func TestCloudKMSListRejectsForeignToken(t *testing.T) {
	saved := kmsKeyRings
	t.Cleanup(func() { kmsKeyRings = saved })
	kmsKeyRings = sim.MakeStore[kmsKeyRing](nil, "test_kms_key_rings")
	for _, id := range []string{"a", "b", "c"} {
		name := "projects/p/locations/global/keyRings/" + id
		kmsKeyRings.Put(name, kmsKeyRing{Name: name})
	}
	s := &cloudKmsGRPC{}
	parent := "projects/p/locations/global"

	first, err := s.ListKeyRings(context.Background(), &kmspb.ListKeyRingsRequest{Parent: parent, PageSize: 2})
	if err != nil || len(first.KeyRings) != 2 || first.NextPageToken == "" {
		t.Fatalf("first page: %v %v", first, err)
	}
	second, err := s.ListKeyRings(context.Background(), &kmspb.ListKeyRingsRequest{Parent: parent, PageSize: 2, PageToken: first.NextPageToken})
	if err != nil || len(second.KeyRings) != 1 || second.NextPageToken != "" {
		t.Fatalf("second page: %v %v", second, err)
	}
	for _, tok := range []string{"not-base64!", "2", "LTE="} {
		_, err := s.ListKeyRings(context.Background(), &kmspb.ListKeyRingsRequest{Parent: parent, PageToken: tok})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("token %q: got %v, want InvalidArgument", tok, err)
		}
	}
}

func TestCloudLoggingListRejectsForeignToken(t *testing.T) {
	saved := logEntries
	t.Cleanup(func() { logEntries = saved })
	logEntries = sim.MakeStore[[]LogEntry](nil, "test_logging_entries")
	if _, _, err := listLogEntries("", nil, 1, "bogus", ""); !errors.Is(err, listq.ErrBadToken) {
		t.Fatalf("got %v, want ErrBadToken", err)
	}
}

// The BigQuery clients read past the first page of tabledata.list by sending
// back the pageToken the previous page carried.
func TestBigQueryTableDataListFollowsPageToken(t *testing.T) {
	savedTables, savedRows := bqTables, bqRows
	t.Cleanup(func() { bqTables, bqRows = savedTables, savedRows })
	bqTables = sim.MakeStore[BQTable](nil, "test_bq_tables")
	bqRows = sim.MakeStore[BQRowSet](nil, "test_bq_rows")
	key := bqTableKey("p", "d", "t")
	bqTables.Put(key, BQTable{Schema: &BQSchema{Fields: []BQFieldSchema{{Name: "n", Type: "INTEGER"}}}})
	bqRows.Put(key, BQRowSet{Rows: []map[string]any{{"n": 1}, {"n": 2}, {"n": 3}}})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/data", handleBQTableDataList)
	get := func(query string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/bigquery/v2/projects/p/datasets/d/tables/t/data?"+query, nil))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return rec.Code, body
	}
	rows := 0
	query := "maxResults=2"
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("tabledata.list never reached its last page")
		}
		code, body := get(query)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %v", query, code, body)
		}
		page, _ := body["rows"].([]any)
		rows += len(page)
		tok, _ := body["pageToken"].(string)
		if tok == "" {
			break
		}
		query = "maxResults=2&pageToken=" + tok
	}
	if rows != 3 {
		t.Fatalf("read %d rows across pages, want 3", rows)
	}
	if code, _ := get("pageToken=nope"); code != http.StatusBadRequest {
		t.Fatalf("a foreign page token answered %d, want 400", code)
	}
}
