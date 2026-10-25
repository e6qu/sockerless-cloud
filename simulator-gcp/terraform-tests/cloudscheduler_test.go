package gcp_tf_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformCloudSchedulerJobs creates a Pub/Sub job and a paused HTTP
// job through google_cloud_scheduler_job: the provider reads both back
// without drift, and the jobs Cloud Scheduler holds carry the configuration.
func TestTerraformCloudSchedulerJobs(t *testing.T) {
	outputs := applyFixture(t, "cloud-scheduler")
	require.Equal(t, "projects/test-project/locations/us-central1/jobs/tf-scheduler-publish", outputs.must(t, "publish_id"))
	require.Equal(t, "ENABLED", outputs.must(t, "publish_state"))
	require.Equal(t, "PAUSED", outputs.must(t, "call_state"))
	require.Equal(t, "60s", outputs.must(t, "call_deadline"))
	requireNoDrift(t, "cloud-scheduler")

	var job struct {
		TimeZone     string `json:"timeZone"`
		PubsubTarget struct {
			TopicName  string            `json:"topicName"`
			Attributes map[string]string `json:"attributes"`
		} `json:"pubsubTarget"`
		RetryConfig struct {
			RetryCount   int `json:"retryCount"`
			MaxDoublings int `json:"maxDoublings"`
		} `json:"retryConfig"`
	}
	require.NoError(t, json.Unmarshal(simCall(t, "GET", "/v1/projects/test-project/locations/us-central1/jobs/tf-scheduler-publish", ""), &job))
	require.Equal(t, "Europe/London", job.TimeZone)
	require.Equal(t, "projects/test-project/topics/tf-scheduler-ticks", job.PubsubTarget.TopicName)
	require.Equal(t, "terraform", job.PubsubTarget.Attributes["origin"])
	require.Equal(t, 3, job.RetryConfig.RetryCount)
	require.Equal(t, 2, job.RetryConfig.MaxDoublings)
}
