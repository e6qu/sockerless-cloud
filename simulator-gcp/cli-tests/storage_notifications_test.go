package gcp_cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `gcloud storage buckets notifications create` reads the bucket's project
// number, asks Cloud Storage for that project's service agent, grants the agent
// roles/pubsub.publisher on the topic and creates the configuration, which
// Cloud Storage accepts only when the agent it checks is the one gcloud
// granted.
func TestGCSCLI_NotificationGrantsTheServiceAgent(t *testing.T) {
	bucket := "cli-notified-bucket"
	topic := "cli-gcs-notifications"
	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us"))
	t.Cleanup(func() { runCLI(t, gcloudCLI("storage", "buckets", "delete", "gs://"+bucket)) })
	runCLI(t, gcloudCLI("pubsub", "topics", "create", topic))
	t.Cleanup(func() { runCLI(t, gcloudCLI("pubsub", "topics", "delete", topic)) })

	var described struct {
		ProjectNumber string `json:"projectNumber"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, gcloudCLI("projects", "describe", project, "--format=json"))), &described))
	bucketNumber := runCLI(t, gcloudCLI("storage", "buckets", "describe", "gs://"+bucket, "--raw", "--format=value(projectNumber)"))
	assert.Equal(t, described.ProjectNumber, strings.TrimSpace(bucketNumber), "the bucket carries its project's number")

	agent := runCLI(t, gcloudCLI("storage", "service-agent"))
	wantAgent := "service-" + described.ProjectNumber + "@gs-project-accounts.iam.gserviceaccount.com"
	assert.Equal(t, wantAgent, strings.TrimSpace(agent))

	var created struct {
		ID    string `json:"id"`
		Topic string `json:"topic"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, gcloudCLI("storage", "buckets", "notifications", "create", "gs://"+bucket,
		"--topic="+topic, "--event-types=OBJECT_FINALIZE", "--format=json"))), &created))
	assert.Equal(t, "//pubsub.googleapis.com/projects/"+project+"/topics/"+topic, created.Topic)

	policy := runCLI(t, gcloudCLI("pubsub", "topics", "get-iam-policy", topic, "--format=json"))
	assert.Contains(t, policy, "serviceAccount:"+wantAgent, "gcloud granted publish to the agent Cloud Storage checks")

	runCLI(t, gcloudCLI("storage", "buckets", "notifications", "delete", "projects/_/buckets/"+bucket+"/notificationConfigs/"+created.ID))
}
