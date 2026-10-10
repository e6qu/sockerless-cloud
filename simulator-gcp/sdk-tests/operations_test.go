package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestGCP_Operations_List exercises the AIP-151 /v1/operations
// endpoint, verifying:
//  1. Empty initial state returns 200 + `{"operations": []}`.
//  2. After creating LRO-emitting resources across multiple services,
//     the list contains operations from all of them.
//  3. Unknown `/v1/...` paths route past the GCS XML-API catch-all
//     (i.e. `/v1/unknown` must not come back shaped like
//     "object 'unknown' not found in bucket 'v1'").
func TestGCP_Operations_List(t *testing.T) {
	// Empty list — fresh sim with no LROs.
	req, _ := http.NewRequest("GET", baseURL+"/v1/operations", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"/v1/operations must 200, not GCS-shape 404")
	require.Equal(t, "application/json", strings.Split(resp.Header.Get("Content-Type"), ";")[0])

	var firstList struct {
		Operations []map[string]any `json:"operations"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&firstList))
	initial := len(firstList.Operations)

	// Trigger LROs across two services: Memorystore Redis Create
	// and Cloud Run Jobs Create. Both go through newLRO which writes
	// to the shared crOperations store.
	createRedis := `{"displayName":"opsTest"}`
	rr, _ := http.NewRequest("POST",
		baseURL+"/v1/projects/p1/locations/us-central1/instances?instanceId=ops-test-redis",
		strings.NewReader(createRedis))
	rr.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(rr)
	require.NoError(t, err)
	resp1.Body.Close()
	require.Equal(t, http.StatusOK, resp1.StatusCode, "memorystore create must succeed")
	deleteRedisOnCleanup(t, redisService(t), "projects/p1/locations/us-central1/instances/ops-test-redis")

	// Two Cloud Run job creates in two regions: their operations land in two
	// different project-scoped collections, which is what makes the `name`
	// filter below discriminating rather than a pass-through.
	centralJobOp := gcpCreateJobOperation(t, "us-central1", "ops-test-job")
	europeJobOp := gcpCreateJobOperation(t, "europe-west1", "ops-test-job-eu")
	require.NotEqual(t, centralJobOp, europeJobOp)

	// List again — must contain at least the 3 new ops.
	req2, _ := http.NewRequest("GET", baseURL+"/v1/operations", nil)
	resp3, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp3.Body.Close()
	require.Equal(t, http.StatusOK, resp3.StatusCode)

	var afterList struct {
		Operations []map[string]any `json:"operations"`
	}
	require.NoError(t, json.NewDecoder(resp3.Body).Decode(&afterList))
	require.GreaterOrEqual(t, len(afterList.Operations), initial+3,
		"List must include the 3 new LROs from Memorystore + the two Cloud Run Jobs")

	// Every entry has a `name` carrying an operations collection. Most
	// services use the project-scoped `projects/.../operations/{id}` form;
	// Bigtable admin legitimately uses the flat `operations/{id}` collection
	// (AIP-151 allows both), so accept either.
	for _, op := range afterList.Operations {
		name, _ := op["name"].(string)
		assert.True(t,
			strings.Contains(name, "/operations/") || strings.HasPrefix(name, "operations/"),
			"op name must carry an operations collection, got %q", name)
	}

	// `filter=done:true` and `filter=done:false` answer about the field they
	// name: each member's body carries the filtered value, no operation is in
	// both, and this test's own operations, which have completed, are under
	// done:true. Other tests' Cloud Run RunJob operations run until their
	// executions end, so done:false need not be empty.
	doneNames := map[string]bool{}
	for _, op := range gcpListOperations(t, "?filter=done:true") {
		assert.Equal(t, true, op["done"], "an operation under done:true reports done in its body")
		name, _ := op["name"].(string)
		doneNames[name] = true
	}
	for _, name := range []string{centralJobOp, europeJobOp} {
		assert.True(t, doneNames[name], "the completed job create %q is under done:true", name)
	}
	for _, op := range gcpListOperations(t, "?filter=done:false") {
		assert.NotEqual(t, true, op["done"], "an operation under done:false reports not done in its body")
		name, _ := op["name"].(string)
		assert.False(t, doneNames[name], "%q is under both done:true and done:false", name)
	}

	// The `name` filter discriminates on the operation's own collection. The
	// two jobs created above are in two regions, so a prefix naming one
	// region's collection selects that job's operation and excludes the
	// other's — a filter that returned everything would fail on the exclusion.
	const scoped = "projects/p1/locations/us-central1/operations/"
	scopedList := gcpListOperations(t, "?name="+scoped)
	scopedNames := make([]string, 0, len(scopedList))
	for _, op := range scopedList {
		name, _ := op["name"].(string)
		assert.True(t, strings.HasPrefix(name, scoped),
			"the name filter must not return an operation outside the prefix, got %q", name)
		scopedNames = append(scopedNames, name)
	}
	assert.Contains(t, scopedNames, centralJobOp,
		"the us-central1 job's operation is in the us-central1 collection")
	assert.NotContains(t, scopedNames, europeJobOp,
		"the europe-west1 job's operation belongs to another collection")
}

// TestGCP_Operations_ListPagesAndFiltersDone pages the AIP-151 collection with
// pageSize and nextPageToken, and filters `done` as the proto bool it is.
func TestGCP_Operations_ListPagesAndFiltersDone(t *testing.T) {
	const location = "asia-south2"
	const scoped = "projects/p1/locations/" + location + "/operations/"
	created := map[string]bool{}
	for _, job := range []string{"page-a", "page-b", "page-c"} {
		created[gcpCreateJobOperation(t, location, job)] = true
	}

	seen := map[string]bool{}
	pages, token := 0, ""
	for {
		query := url.Values{"name": {scoped}, "pageSize": {"2"}}
		if token != "" {
			query.Set("pageToken", token)
		}
		page := gcpListOperationsPage(t, "?"+query.Encode())
		require.LessOrEqual(t, len(page.Operations), 2, "a page holds at most pageSize operations")
		for _, op := range page.Operations {
			name, _ := op["name"].(string)
			assert.False(t, seen[name], "operation %q listed on two pages", name)
			seen[name] = true
		}
		pages++
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}
	assert.Equal(t, 2, pages, "three operations at pageSize 2 fill two pages")
	assert.Equal(t, created, seen, "paging visits every operation in the collection exactly once")

	for filter, want := range map[string]int{"done = true": 3, "done:true": 3, "done = false": 0, "NOT done": 0} {
		page := gcpListOperationsPage(t, "?"+url.Values{"name": {scoped}, "filter": {filter}}.Encode())
		assert.Len(t, page.Operations, want, "filter %q", filter)
	}
}

type gcpOperationsPage struct {
	Operations    []map[string]any `json:"operations"`
	NextPageToken string           `json:"nextPageToken"`
}

func gcpListOperationsPage(t *testing.T, rawQuery string) gcpOperationsPage {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/operations"+rawQuery, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "list operations: %s", body)
	var page gcpOperationsPage
	require.NoError(t, json.Unmarshal(body, &page))
	return page
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

// gcpListOperations reads the AIP-151 /v1/operations collection with the given
// raw query and returns the operations it reported.
func gcpListOperations(t *testing.T, rawQuery string) []map[string]any {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/operations"+rawQuery, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list struct {
		Operations []map[string]any `json:"operations"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	return list.Operations
}

// TestGCP_UnknownV1PathNotGCSShape locks the routing invariant: the
// GCS XML-API catch-all only matches paths whose first segment is a
// registered bucket. An unknown `/v1/xyz` is a route miss — a 404 — and
// must NOT come back as "object xyz not found in bucket v1", which would
// tell a client the path resolved to a bucket that does not exist.
func TestGCP_UnknownV1PathNotGCSShape(t *testing.T) {
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
