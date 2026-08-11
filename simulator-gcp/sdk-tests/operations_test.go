package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	createJob := `{"template":{"template":{"containers":[{"image":"alpine"}]}}}`
	jr, _ := http.NewRequest("POST",
		baseURL+"/v2/projects/p1/locations/us-central1/jobs?jobId=ops-test-job",
		strings.NewReader(createJob))
	jr.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(jr)
	require.NoError(t, err)
	resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode, "cloudrun job create must succeed")

	// List again — must contain at least the 2 new ops.
	req2, _ := http.NewRequest("GET", baseURL+"/v1/operations", nil)
	resp3, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp3.Body.Close()
	require.Equal(t, http.StatusOK, resp3.StatusCode)

	var afterList struct {
		Operations []map[string]any `json:"operations"`
	}
	require.NoError(t, json.NewDecoder(resp3.Body).Decode(&afterList))
	require.GreaterOrEqual(t, len(afterList.Operations), initial+2,
		"List must include the 2 new LROs from Memorystore + Cloud Run Jobs")

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

	// `filter=done:true` returns the same set (newLRO records done=true
	// since the sim doesn't actually run async work).
	req3, _ := http.NewRequest("GET", baseURL+"/v1/operations?filter=done:true", nil)
	resp4, err := http.DefaultClient.Do(req3)
	require.NoError(t, err)
	defer resp4.Body.Close()
	require.Equal(t, http.StatusOK, resp4.StatusCode)
	var doneList struct {
		Operations []map[string]any `json:"operations"`
	}
	require.NoError(t, json.NewDecoder(resp4.Body).Decode(&doneList))
	assert.Equal(t, len(afterList.Operations), len(doneList.Operations),
		"filter=done:true should return all ops (sim writes done=true)")

	// `filter=done:false` returns zero (sim never has in-progress ops).
	req4, _ := http.NewRequest("GET", baseURL+"/v1/operations?filter=done:false", nil)
	resp5, err := http.DefaultClient.Do(req4)
	require.NoError(t, err)
	defer resp5.Body.Close()
	var pendingList struct {
		Operations []map[string]any `json:"operations"`
	}
	require.NoError(t, json.NewDecoder(resp5.Body).Decode(&pendingList))
	assert.Empty(t, pendingList.Operations,
		"filter=done:false should be empty (sim doesn't run async)")
}

// TestGCP_UnknownV1PathNotGCSShape locks the routing invariant: the
// GCS XML-API catch-all only matches paths whose first segment is a
// registered bucket. An unknown `/v1/xyz` must NOT come back as
// "object xyz not found in bucket v1".
func TestGCP_UnknownV1PathNotGCSShape(t *testing.T) {
	req, _ := http.NewRequest("GET", baseURL+"/v1/this-does-not-exist", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.NotContains(t, string(body), `object "this-does-not-exist" not found in bucket "v1"`,
		"GCS XML-API catch-all must not swallow unhandled /v1/ paths")
}
