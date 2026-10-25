package gcp_tf_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformCloudTasksQueues creates a queue with rate limits and retry
// parameters and a paused queue with queue-level routing through
// google_cloud_tasks_queue: the provider reads both back without drift, the
// queues Cloud Tasks holds carry the configuration, and the paused queue
// holds a task it is given, carrying the queue's header override.
func TestTerraformCloudTasksQueues(t *testing.T) {
	outputs := applyFixture(t, "cloud-tasks")
	require.Equal(t, "projects/test-project/locations/us-central1/queues/tf-tasks-orders", outputs.must(t, "orders_id"))
	require.Equal(t, "RUNNING", outputs.must(t, "orders_state"))
	require.EqualValues(t, 5, outputs.mustValue(t, "orders_burst"))
	require.Equal(t, "PAUSED", outputs.must(t, "routed_state"))
	requireNoDrift(t, "cloud-tasks")

	var queue struct {
		RateLimits struct {
			MaxConcurrentDispatches int `json:"maxConcurrentDispatches"`
		} `json:"rateLimits"`
		RetryConfig struct {
			MaxAttempts      int    `json:"maxAttempts"`
			MaxRetryDuration string `json:"maxRetryDuration"`
			MaxDoublings     int    `json:"maxDoublings"`
		} `json:"retryConfig"`
	}
	require.NoError(t, json.Unmarshal(simCall(t, "GET", "/v2/projects/test-project/locations/us-central1/queues/tf-tasks-orders", ""), &queue))
	require.Equal(t, 3, queue.RateLimits.MaxConcurrentDispatches)
	require.Equal(t, 6, queue.RetryConfig.MaxAttempts)
	require.Equal(t, "120s", queue.RetryConfig.MaxRetryDuration)
	require.Equal(t, 2, queue.RetryConfig.MaxDoublings)

	simCall(t, "POST", "/v2/projects/test-project/locations/us-central1/queues/tf-tasks-routed/tasks",
		`{"task":{"name":"projects/test-project/locations/us-central1/queues/tf-tasks-routed/tasks/tf-probe","httpRequest":{"url":"https://placeholder.example/"}}}`)
	var task struct {
		DispatchCount int `json:"dispatchCount"`
		HTTPRequest   struct {
			Headers map[string]string `json:"headers"`
		} `json:"httpRequest"`
	}
	require.NoError(t, json.Unmarshal(simCall(t, "GET", "/v2/projects/test-project/locations/us-central1/queues/tf-tasks-routed/tasks/tf-probe", ""), &task))
	require.Zero(t, task.DispatchCount, "a paused queue holds its tasks")
	require.Equal(t, "terraform", task.HTTPRequest.Headers["X-Source"])
	simCall(t, "DELETE", "/v2/projects/test-project/locations/us-central1/queues/tf-tasks-routed/tasks/tf-probe", "")
}
