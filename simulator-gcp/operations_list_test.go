package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type gcpOperationsListPage struct {
	Operations    []map[string]any `json:"operations"`
	NextPageToken string           `json:"nextPageToken"`
}

func gcpOperationsListFixture(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	empty := func() {
		for _, op := range crOperations.List() {
			crOperations.Delete(op.Name)
		}
	}
	empty()
	t.Cleanup(empty)
	for _, op := range []Operation{
		{Name: "projects/p/locations/us-central1/operations/a", Done: true},
		{Name: "projects/p/locations/us-central1/operations/b"},
		{Name: "projects/p/locations/us-central1/operations/c", Done: true},
		{Name: "projects/p/locations/europe-west1/operations/d"},
	} {
		crOperations.Put(op.Name, op)
	}
	return srv
}

func gcpListOperationsPage(t *testing.T, srv *sim.Server, query url.Values) (int, gcpOperationsListPage, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://longrunning.googleapis.com/v1/operations?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var page gcpOperationsListPage
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode list: %v: %s", err, rec.Body)
		}
	}
	return rec.Code, page, rec.Body.String()
}

func gcpOperationNames(page gcpOperationsListPage) []string {
	names := make([]string, 0, len(page.Operations))
	for _, op := range page.Operations {
		name, _ := op["name"].(string)
		names = append(names, name)
	}
	return names
}

func gcpEqualNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The filter reads `done` as the proto bool it is: an operation whose record
// never set it is not done, so every AIP-160 spelling of "not done" selects it.
func TestGCPListOperationsFiltersDoneAsAProtoBool(t *testing.T) {
	srv := gcpOperationsListFixture(t)
	running := []string{
		"projects/p/locations/europe-west1/operations/d",
		"projects/p/locations/us-central1/operations/b",
	}
	finished := []string{
		"projects/p/locations/us-central1/operations/a",
		"projects/p/locations/us-central1/operations/c",
	}
	for filter, want := range map[string][]string{
		"done = false":             running,
		"done:false":               running,
		"NOT done":                 running,
		"-done":                    running,
		"done != true":             running,
		"done = true":              finished,
		"done:true":                finished,
		"done":                     finished,
		"done = false AND name:us": {"projects/p/locations/us-central1/operations/b"},
	} {
		code, page, body := gcpListOperationsPage(t, srv, url.Values{"filter": {filter}})
		if code != http.StatusOK {
			t.Fatalf("filter %q: %d %s", filter, code, body)
		}
		if got := gcpOperationNames(page); !gcpEqualNames(got, want) {
			t.Errorf("filter %q selected %v, want %v", filter, got, want)
		}
	}

	code, _, body := gcpListOperationsPage(t, srv, url.Values{"filter": {"done = (true"}})
	if code != http.StatusBadRequest {
		t.Fatalf("an unparseable filter answered %d %s, want 400", code, body)
	}
	var refusal struct {
		Error struct{ Status string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &refusal); err != nil || refusal.Error.Status != "INVALID_ARGUMENT" {
		t.Fatalf("an unparseable filter answered %s, want INVALID_ARGUMENT", body)
	}

	// The response states done=false outright rather than omitting it.
	_, page, _ := gcpListOperationsPage(t, srv, url.Values{"filter": {"done = false"}})
	for _, op := range page.Operations {
		if done, present := op["done"]; !present || done != false {
			t.Errorf("operation %v reports done=%v (present %v)", op["name"], done, present)
		}
	}
}

// The gRPC google.longrunning.Operations service reads the same AIP-160 filter
// the REST collection does.
func TestGCPGRPCListOperationsFiltersDone(t *testing.T) {
	gcpOperationsListFixture(t)
	list := func(filter string) ([]string, error) {
		resp, err := (&grpcOperationsService{}).ListOperations(context.Background(),
			&longrunningpb.ListOperationsRequest{Name: "projects/p/locations/", Filter: filter})
		if err != nil {
			return nil, err
		}
		names := []string{}
		for _, op := range resp.GetOperations() {
			names = append(names, op.GetName())
		}
		return names, nil
	}
	for filter, want := range map[string][]string{
		"done = false": {"projects/p/locations/europe-west1/operations/d", "projects/p/locations/us-central1/operations/b"},
		"done":         {"projects/p/locations/us-central1/operations/a", "projects/p/locations/us-central1/operations/c"},
		"":             {"projects/p/locations/europe-west1/operations/d", "projects/p/locations/us-central1/operations/a", "projects/p/locations/us-central1/operations/b", "projects/p/locations/us-central1/operations/c"},
	} {
		got, err := list(filter)
		if err != nil {
			t.Fatalf("filter %q: %v", filter, err)
		}
		if !gcpEqualNames(got, want) {
			t.Errorf("filter %q listed %v, want %v", filter, got, want)
		}
	}
	if _, err := list("done = (true"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("an unparseable filter answered %v, want InvalidArgument", err)
	}
}

// pageSize bounds a page and nextPageToken resumes after it, in name order,
// until the last page carries no token.
func TestGCPListOperationsPagesInNameOrder(t *testing.T) {
	srv := gcpOperationsListFixture(t)
	var seen []string
	token := ""
	pages := 0
	for {
		query := url.Values{"pageSize": {"3"}, "name": {"projects/p/locations/"}}
		if token != "" {
			query.Set("pageToken", token)
		}
		code, page, body := gcpListOperationsPage(t, srv, query)
		if code != http.StatusOK {
			t.Fatalf("page %d: %d %s", pages, code, body)
		}
		if len(page.Operations) > 3 {
			t.Fatalf("page %d holds %d operations, over pageSize 3", pages, len(page.Operations))
		}
		seen = append(seen, gcpOperationNames(page)...)
		pages++
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}
	want := []string{
		"projects/p/locations/europe-west1/operations/d",
		"projects/p/locations/us-central1/operations/a",
		"projects/p/locations/us-central1/operations/b",
		"projects/p/locations/us-central1/operations/c",
	}
	if pages != 2 || !gcpEqualNames(seen, want) {
		t.Fatalf("paged %d pages %v, want 2 pages %v", pages, seen, want)
	}

	code, _, body := gcpListOperationsPage(t, srv, url.Values{"pageToken": {"not-a-token"}})
	if code != http.StatusBadRequest {
		t.Fatalf("a token the service never issued answered %d %s, want 400", code, body)
	}
}
