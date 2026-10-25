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
	byValue := gcpAsc(func(v string) string { return v })
	for _, tok := range []string{"x", "-1", "Mg==", "99", "YWZ0ZXIAWyJhIiwiYiJd"} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/x?pageSize=1&pageToken="+tok, nil)
		if _, _, ok := paginateList(rec, r, items, byValue); ok {
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
		page, next, ok := paginateList(rec, httptest.NewRequest("GET", "/x?pageSize=2&pageToken="+tok, nil), items, byValue)
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

// A Google Cloud list token resumes after the last item the previous page
// returned, so a client walking the list sees every item that stayed in it
// exactly once, whatever was created or deleted between its page reads.
func TestGCPListPagesResumeAfterTheLastItemReturned(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sizeName string
		page     func(w http.ResponseWriter, r *http.Request, items []string) ([]string, string, bool)
	}{
		{"pageSize", "pageSize", func(w http.ResponseWriter, r *http.Request, items []string) ([]string, string, bool) {
			return paginateList(w, r, items, gcpAsc(func(v string) string { return v }))
		}},
		{"maxResults", "maxResults", func(w http.ResponseWriter, r *http.Request, items []string) ([]string, string, bool) {
			return paginateListCompute(w, r, items, gcpAsc(func(v string) string { return v }))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := map[string]bool{"b": true, "d": true, "f": true, "h": true, "j": true}
			read := func(token string) ([]string, string) {
				items := make([]string, 0, len(stored))
				for name := range stored {
					items = append(items, name)
				}
				rec := httptest.NewRecorder()
				page, next, ok := tc.page(rec, httptest.NewRequest("GET", "/x?"+tc.sizeName+"=2&pageToken="+token, nil), items)
				if !ok {
					t.Fatalf("token %q rejected: %s", token, rec.Body.String())
				}
				return page, next
			}
			seen := map[string]int{}
			page, next := read("")
			for _, name := range page {
				seen[name]++
			}
			// Between the reads a client deletes the last item it was given and
			// one it has not reached, and creates one on each side of the boundary.
			delete(stored, "d")
			delete(stored, "h")
			stored["a"], stored["e"] = true, true
			for next != "" {
				page, next = read(next)
				for _, name := range page {
					seen[name]++
				}
			}
			for name, count := range seen {
				if count != 1 {
					t.Errorf("%s was listed %d times", name, count)
				}
			}
			for _, stable := range []string{"b", "f", "j"} {
				if seen[stable] != 1 {
					t.Errorf("%s, which stayed in the list, was skipped: %v", stable, seen)
				}
			}
			if seen["a"] != 0 {
				t.Errorf("a, created before the position already read, was listed: %v", seen)
			}
			if seen["e"] != 1 {
				t.Errorf("e, created after the position already read, was skipped: %v", seen)
			}
		})
	}
}

// A client's orderBy leads the order a token resumes in.
func TestGCPListPagesFollowOrderBy(t *testing.T) {
	type item struct {
		Name string `json:"name"`
		Size int    `json:"size"`
	}
	items := []item{{"a", 3}, {"b", 1}, {"c", 2}, {"d", 1}}
	var got []string
	token := ""
	for {
		rec := httptest.NewRecorder()
		page, next, ok := paginateList(rec, httptest.NewRequest("GET", "/x?orderBy=size%20desc&pageSize=1&pageToken="+token, nil), items,
			gcpAsc(func(v item) string { return v.Name }))
		if !ok {
			t.Fatalf("token %q rejected: %s", token, rec.Body.String())
		}
		for _, v := range page {
			got = append(got, v.Name)
		}
		if next == "" {
			break
		}
		token = next
	}
	if strings.Join(got, "") != "acbd" {
		t.Fatalf("orderBy=size desc listed %v, want a c b d", got)
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
	useLoggingMemoryStores(t)
	if _, _, err := listLogEntries("", nil, 1, "bogus", ""); !errors.Is(err, listq.ErrBadToken) {
		t.Fatalf("got %v, want listq.ErrBadToken", err)
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
