package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
	serviceUsage := map[string]any{"@type": serviceUsageOperationMetadataType}
	cloudRun := map[string]any{"@type": "type.googleapis.com/google.cloud.run.v2.Service"}
	for _, op := range []Operation{
		{Name: "projects/p/locations/us-central1/operations/a", Done: true, Metadata: cloudRun},
		{Name: "projects/p/locations/us-central1/operations/b", Metadata: cloudRun},
		{Name: "projects/p/locations/us-central1/operations/c", Done: true, Metadata: cloudRun},
		{Name: "projects/p/locations/europe-west1/operations/d", Metadata: cloudRun},
		{Name: "operations/su-a", Done: true, Metadata: serviceUsage},
		{Name: "operations/su-b", Metadata: serviceUsage},
		{Name: "operations/su-c", Done: true, Metadata: serviceUsage},
		{Name: "operations/su-d", Metadata: serviceUsage},
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
	running := []string{"operations/su-b", "operations/su-d"}
	finished := []string{"operations/su-a", "operations/su-c"}
	for filter, want := range map[string][]string{
		"done = false":               running,
		"done:false":                 running,
		"NOT done":                   running,
		"-done":                      running,
		"done != true":               running,
		"done = true":                finished,
		"done:true":                  finished,
		"done":                       finished,
		"done = false AND name:su-d": {"operations/su-d"},
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
		query := url.Values{"pageSize": {"3"}, "name": {"operations/su-"}}
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
	want := []string{"operations/su-a", "operations/su-b", "operations/su-c", "operations/su-d"}
	if pages != 2 || !gcpEqualNames(seen, want) {
		t.Fatalf("paged %d pages %v, want 2 pages %v", pages, seen, want)
	}

	code, _, body := gcpListOperationsPage(t, srv, url.Values{"pageToken": {"not-a-token"}})
	if code != http.StatusBadRequest {
		t.Fatalf("a token the service never issued answered %d %s, want 400", code, body)
	}
}

// operations.list answers google.longrunning.Operation as Service Usage
// declares it, for the operations Service Usage started only. Every other
// service's operation stays in that service's own collection: a Cloud Storage
// bucket operation under its bucket, a Cloud Run operation under its location.
func TestGCPListOperationsAnswersServiceUsageOperationsOnly(t *testing.T) {
	srv := gcpOperationsListFixture(t)
	gcpTestCreateProject(t, srv, gcpTestProjectID)
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project="+gcpTestProjectID, `{"name":"relocated"}`)
	storageOp := Operation{
		Name:     gcsOperationName("relocated", "relocate-1"),
		Done:     true,
		Metadata: map[string]any{"@type": "type.googleapis.com/google.storage.control.v2.RelocateBucketMetadata"},
		Kind:     "storage#operation",
		SelfLink: "http://storage.googleapis.com/storage/v1/b/relocated/operations/relocate-1",
	}
	crOperations.Put(storageOp.Name, storageOp)

	code, page, body := gcpListOperationsPage(t, srv, url.Values{})
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	want := []string{"operations/su-a", "operations/su-b", "operations/su-c", "operations/su-d"}
	if got := gcpOperationNames(page); !gcpEqualNames(got, want) {
		t.Fatalf("Service Usage listed %v, want only its own operations %v", got, want)
	}
	for _, op := range page.Operations {
		for field := range op {
			switch field {
			case "name", "metadata", "done", "response", "error":
			default:
				t.Errorf("operation lists undeclared field %q: %s", field, body)
			}
		}
	}

	bucketOps := gcpHostOK(t, srv, "storage.googleapis.com", http.MethodGet, "/storage/v1/b/relocated/operations", "")
	if listed, _ := bucketOps["operations"].([]any); len(listed) != 1 || listed[0].(map[string]any)["name"] != storageOp.Name {
		t.Fatalf("the bucket's own collection listed %v, want %s", bucketOps, storageOp.Name)
	}
	locationOps := gcpHostOK(t, srv, "run.googleapis.com", http.MethodGet, "/v2/projects/p/locations/europe-west1/operations", "")
	if listed, _ := locationOps["operations"].([]any); len(listed) != 1 ||
		listed[0].(map[string]any)["name"] != "projects/p/locations/europe-west1/operations/d" {
		t.Fatalf("the location's own collection listed %v, want projects/p/locations/europe-west1/operations/d", locationOps)
	}

	projectOp := Operation{Name: "operations/cp.create-1", Done: true,
		Metadata: map[string]any{"@type": "type.googleapis.com/google.cloud.resourcemanager.v3.CreateProjectMetadata"}}
	crOperations.Put(projectOp.Name, projectOp)
	for name, startedByServiceUsage := range map[string]bool{projectOp.Name: false, "operations/su-a": true} {
		gcpHostOK(t, srv, "serviceusage.googleapis.com", http.MethodDelete, "/v1/operations/"+strings.TrimPrefix(name, "operations/"), "")
		if _, kept := crOperations.Get(name); kept == startedByServiceUsage {
			t.Errorf("Service Usage operations.delete on %s left it stored = %v", name, kept)
		}
	}
}

// Each service serves projects.locations.operations.list from its own host, so
// one location's collection lists, per host, the operations that service
// started; every service that publishes the collection still reaches its own.
func TestGCPLocationOperationsListOnlyTheirServicesOperations(t *testing.T) {
	srv := gcpOperationsListFixture(t)
	const location = "projects/p/locations/us-east4/operations/"
	stored := map[string]string{
		"eventarc":       "google.cloud.eventarc.v1.OperationMetadata",
		"redis-instance": "google.cloud.redis.v1.OperationMetadata",
		"redis-cluster":  "google.cloud.redis.cluster.v1.OperationMetadata",
		"apigateway":     "google.cloud.apigateway.v1.OperationMetadata",
		"vpcaccess":      "google.cloud.vpcaccess.v1.OperationMetadata",
		"functions":      "google.cloud.functions.v2.OperationMetadata",
		"run":            "google.cloud.run.v2.Service",
		"kms":            "google.cloud.kms.v1.CreateCryptoKeyMetadata",
	}
	for id, message := range stored {
		crOperations.Put(location+id, Operation{Name: location + id, Done: true,
			Metadata: map[string]any{"@type": "type.googleapis.com/" + message}})
	}
	loggingOp := Operation{Name: location + "logging", Done: true,
		Metadata: map[string]any{"@type": "type.googleapis.com/google.logging.v2.CopyLogEntriesMetadata"}}
	logOperations.Put(loggingOp.Name, loggingOp)
	t.Cleanup(func() { logOperations.Delete(loggingOp.Name) })

	list := func(host, version string) []string {
		t.Helper()
		body := gcpHostOK(t, srv, host, http.MethodGet, "/"+version+"/projects/p/locations/us-east4/operations", "")
		listed, _ := body["operations"].([]any)
		ids := make([]string, 0, len(listed))
		for _, op := range listed {
			ids = append(ids, strings.TrimPrefix(op.(map[string]any)["name"].(string), location))
		}
		return ids
	}
	for _, tc := range []struct {
		host, version string
		want          []string
	}{
		{"eventarc.googleapis.com", "v1", []string{"eventarc"}},
		{"redis.googleapis.com", "v1", []string{"redis-cluster", "redis-instance"}},
		{"apigateway.googleapis.com", "v1", []string{"apigateway"}},
		{"vpcaccess.googleapis.com", "v1", []string{"vpcaccess"}},
		{"run.googleapis.com", "v1", []string{"run"}},
		{"us-east4-run.googleapis.com", "v2", []string{"run"}},
		{"cloudfunctions.googleapis.com", "v2", []string{"functions"}},
		{"logging.googleapis.com", "v2", []string{"logging"}},
		// A bare address names no service: the collection holds the operations
		// of every service publishing it at that version.
		{"127.0.0.1:4567", "v1", []string{"apigateway", "eventarc", "redis-cluster", "redis-instance", "run", "vpcaccess"}},
		{"127.0.0.1:4567", "v2", []string{"functions", "logging", "run"}},
	} {
		if got := list(tc.host, tc.version); !gcpEqualNames(got, tc.want) {
			t.Errorf("%s /%s listed %v, want %v", tc.host, tc.version, got, tc.want)
		}
	}

	if code, _ := gcpHostCall(t, srv, "eventarc.googleapis.com", http.MethodDelete,
		"/v1/projects/p/locations/us-east4/operations/redis-instance", ""); code != http.StatusNotFound {
		t.Errorf("Eventarc deleting a Memorystore operation answered %d, want 404", code)
	}
	if _, kept := crOperations.Get(location + "redis-instance"); !kept {
		t.Fatal("Eventarc's operations.delete removed a Memorystore operation")
	}
	gcpHostOK(t, srv, "redis.googleapis.com", http.MethodDelete, "/v1/projects/p/locations/us-east4/operations/redis-instance", "")
	if _, kept := crOperations.Get(location + "redis-instance"); kept {
		t.Fatal("Memorystore's operations.delete left its operation stored")
	}
}
