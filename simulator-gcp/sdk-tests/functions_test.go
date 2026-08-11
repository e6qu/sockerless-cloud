package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"cloud.google.com/go/logging/logadmin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
)

func TestCloudFunctions_InvokeInjectsLogEntries(t *testing.T) {
	// Create a function
	fn := map[string]any{
		"buildConfig": map[string]any{
			"runtime":    "go121",
			"entryPoint": "Handler",
		},
	}
	body, _ := json.Marshal(fn)
	createReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/functions?functionId=log-test-fn",
		strings.NewReader(string(body)))
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	// Invoke the function
	invokeReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2-functions-invoke/log-test-fn",
		strings.NewReader("{}"))
	invokeReq.Header.Set("Content-Type", "application/json")
	invokeResp, err := http.DefaultClient.Do(invokeReq)
	require.NoError(t, err)
	invokeResp.Body.Close()
	require.Equal(t, http.StatusOK, invokeResp.StatusCode)

	// Query log entries using logadmin with the same filter the backend uses
	client := logadminClient(t)
	filter := `resource.type="cloud_run_revision" AND resource.labels.service_name="log-test-fn"`
	it := client.Entries(ctx, logadmin.Filter(filter))

	var messages []string
	for {
		entry, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)

		// Verify resource type and label
		assert.Equal(t, "cloud_run_revision", entry.Resource.Type)
		assert.Equal(t, "log-test-fn", entry.Resource.Labels["service_name"])

		if entry.Payload != nil {
			if s, ok := entry.Payload.(string); ok {
				messages = append(messages, s)
			}
		}
	}

	require.GreaterOrEqual(t, len(messages), 1, "should have at least one log entry from invocation")
	assert.Equal(t, "Function invoked", messages[0])
}

func TestCloudFunctions_InvokeURLMatchesEndpoint(t *testing.T) {
	// Create a function
	fn := map[string]any{
		"buildConfig": map[string]any{
			"runtime":    "go121",
			"entryPoint": "Handler",
		},
	}
	body, _ := json.Marshal(fn)
	createReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v2/projects/test-project/locations/us-central1/functions?functionId=url-test-fn",
		strings.NewReader(string(body)))
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	// Parse LRO response to get the function's ServiceConfig.Uri
	var lro map[string]any
	data, _ := io.ReadAll(createResp.Body)
	require.NoError(t, json.Unmarshal(data, &lro))
	response := lro["response"].(map[string]any)
	svcConfig := response["serviceConfig"].(map[string]any)
	uri := svcConfig["uri"].(string)

	// Verify the URI contains the expected invoke path
	assert.Contains(t, uri, "/v2-functions-invoke/url-test-fn")

	// POST to the returned URI — it should be reachable
	invokeResp, err := http.DefaultClient.Post(uri, "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer invokeResp.Body.Close()
	assert.Equal(t, http.StatusOK, invokeResp.StatusCode)
}
