package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	serviceusage "google.golang.org/api/serviceusage/v1"
)

// awaitLRO polls a long-running operation to completion and returns the record
// the operations collection finally reported. The operation a Google Cloud
// method answers with is a handle, and the outcome belongs to the collection,
// not to the create response: a client polls until the record reports done and
// reads the result off that. Asserting through the poll keeps the assertion
// about the outcome instead of about how many round trips the service needed to
// reach it, and it fails loudly if the name the method handed out does not
// resolve.
func awaitLRO[T any](t *testing.T, name string, get func() (T, error), isDone func(T) bool) T {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		op, err := get()
		require.NoError(t, err, "polling operation %q", name)
		if isDone(op) {
			return op
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %q never reported done", name)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestGCP_Operations_List drives Service Usage's operations.list, the one
// method that answers GET /v1/operations: it lists the operations Service Usage
// started and no other service's, each other service's operations staying in
// that service's own collection.
func TestGCP_Operations_List(t *testing.T) {
	t.Parallel()
	svc := serviceUsageService(t)
	const project = "projects/su-ops-list"
	enabled := map[string]bool{}
	for _, service := range []string{"pubsub.googleapis.com", "run.googleapis.com"} {
		op, err := svc.Services.Enable(project+"/services/"+service, &serviceusage.EnableServiceRequest{}).Do()
		require.NoError(t, err)
		enabled[op.Name] = true
	}
	jobOp := gcpCreateJobOperation(t, "us-central1", uniqueName("ops-list-job"))

	listed := serviceUsageOperations(t, svc.Operations.List())
	for name := range enabled {
		require.Contains(t, listed, name, "Service Usage lists the enable it started")
	}
	for name, op := range listed {
		var metadata map[string]any
		require.NoError(t, json.Unmarshal(op.Metadata, &metadata), "operation %s metadata", name)
		assert.Equal(t, "type.googleapis.com/google.api.serviceusage.v1.OperationMetadata", metadata["@type"],
			"Service Usage lists only operations it started, got %s", name)
	}
	require.NotContains(t, listed, jobOp, "a Cloud Run operation is not Service Usage's")

	// The Cloud Run job's operation stays in its location's own collection.
	resp, err := http.Get(baseURL + "/v2/projects/p1/locations/us-central1/operations")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var location struct {
		Operations []struct {
			Name string `json:"name"`
		} `json:"operations"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&location))
	var locationNames []string
	for _, op := range location.Operations {
		locationNames = append(locationNames, op.Name)
	}
	assert.Contains(t, locationNames, jobOp, "the location's operations collection lists the job create")

	// `filter` reads done as the proto bool it is; `name` scopes to a prefix.
	for name := range serviceUsageOperations(t, svc.Operations.List().Filter("done:false")) {
		assert.False(t, enabled[name], "the finished enable %s is not under done:false", name)
	}
	finished := serviceUsageOperations(t, svc.Operations.List().Filter("done:true"))
	for name := range enabled {
		assert.Contains(t, finished, name, "the finished enable is under done:true")
		scoped := serviceUsageOperations(t, svc.Operations.List().Name(name))
		assert.Len(t, scoped, 1, "the name %s scopes the list to itself", name)
		assert.Contains(t, scoped, name)
	}
}

// TestGCP_Operations_ListPagesAndFiltersDone pages Service Usage's
// operations.list with pageSize and nextPageToken. Other tests enable services
// while the pages are read, and each operation still lists exactly once.
func TestGCP_Operations_ListPagesAndFiltersDone(t *testing.T) {
	t.Parallel()
	svc := serviceUsageService(t)
	created := map[string]bool{}
	for _, service := range []string{"pubsub.googleapis.com", "run.googleapis.com", "storage.googleapis.com"} {
		op, err := svc.Services.Enable("projects/su-ops-pages/services/"+service, &serviceusage.EnableServiceRequest{}).Do()
		require.NoError(t, err)
		created[op.Name] = true
	}

	seen := map[string]int{}
	require.NoError(t, svc.Operations.List().PageSize(2).Pages(ctx, func(page *serviceusage.ListOperationsResponse) error {
		assert.LessOrEqual(t, len(page.Operations), 2, "a page holds at most pageSize operations")
		for _, op := range page.Operations {
			seen[op.Name]++
		}
		return nil
	}))
	for name, count := range seen {
		assert.Equal(t, 1, count, "operation %s listed on %d pages", name, count)
	}
	for name := range created {
		assert.Contains(t, seen, name, "paging visits the enable %s", name)
	}
}

// serviceUsageOperations reads every page of a Service Usage operations.list
// call, keyed by operation name.
func serviceUsageOperations(t *testing.T, call *serviceusage.OperationsListCall) map[string]*serviceusage.Operation {
	t.Helper()
	listed := map[string]*serviceusage.Operation{}
	require.NoError(t, call.Pages(ctx, func(page *serviceusage.ListOperationsResponse) error {
		for _, op := range page.Operations {
			listed[op.Name] = op
		}
		return nil
	}))
	return listed
}

// gcpCreateJobOperation creates a Cloud Run job in the given region and returns
// the name of the long-running operation the create answered with.
func gcpCreateJobOperation(t *testing.T, location, jobID string) string {
	t.Helper()
	body := `{"template":{"template":{"containers":[{"image":"public.ecr.aws/docker/library/alpine:latest"}]}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/v2/projects/p1/locations/"+location+"/jobs?jobId="+jobID,
		strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "cloudrun job create must succeed: %s", payload)
	var op struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(payload, &op))
	require.NotEmpty(t, op.Name, "a create answers with the operation it started")
	return op.Name
}

// TestGCP_UnknownV1PathNotGCSShape locks the routing invariant: the
// GCS XML-API catch-all only matches paths whose first segment is a
// registered bucket. An unknown `/v1/xyz` is a route miss — a 404 — and
// must NOT come back as "object xyz not found in bucket v1", which would
// tell a client the path resolved to a bucket that does not exist.
func TestGCP_UnknownV1PathNotGCSShape(t *testing.T) {
	t.Parallel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/this-does-not-exist", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	// A route the service does not serve is not found — not a 200 with nothing
	// in it, and not an authentication failure either.
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "unrouted /v1/ path: %s", body)
	assert.NotContains(t, string(body), `not found in bucket "v1"`,
		"GCS XML-API catch-all must not swallow unhandled /v1/ paths")
	assert.NotContains(t, string(body), `"this-does-not-exist"`,
		"the reply must not name the path segment as a storage object")
}
