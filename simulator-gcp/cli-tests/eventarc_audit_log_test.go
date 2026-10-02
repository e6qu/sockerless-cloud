package gcp_cli_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEventarcCLI_AuditLogTrigger creates a Cloud Audit Logs trigger with
// gcloud for Cloud Storage bucket creations whose resource name matches a path
// pattern, delivering to a Cloud Run service as a service account that may
// invoke it, then creates a matching bucket with gcloud: gcloud logging read
// returns the Admin Activity entry Cloud Storage wrote, and the service
// receives the entry as a google.cloud.audit.log.v1.written CloudEvent.
func TestEventarcCLI_AuditLogTrigger(t *testing.T) {
	const (
		serviceID = "cli-audit-receiver"
		trigger   = "cli-audit-trigger"
		bucket    = "cli-audit-bucket"
		// Eventarc's multi-region location for a bucket in the US
		// multi-region.
		triggerLocation = "us"
	)
	services := baseURL + "/v2/projects/" + project + "/locations/" + location + "/services"
	httpDoJSON(t, "POST", services+"?serviceId="+serviceID,
		fmt.Sprintf(`{"template":{"containers":[{"image":%q,"args":["log-cloudevent"]}]}}`, httpProbeImageName))
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", services+"/"+serviceID, "")
		if assert.NoError(t, err) {
			resp.Body.Close()
		}
	})
	invoker := cliInvokerEmail(t)
	addServiceInvoker(t, serviceID, "serviceAccount:"+invoker)

	runCLI(t, gcloudCLI("eventarc", "triggers", "create", trigger,
		"--location", triggerLocation,
		"--destination-run-service", serviceID,
		"--destination-run-region", location,
		"--event-filters", "type=google.cloud.audit.log.v1.written",
		"--event-filters", "serviceName=storage.googleapis.com",
		"--event-filters", "methodName=storage.buckets.create",
		"--event-filters-path-pattern", "resourceName=/projects/_/buckets/cli-audit-*",
		"--service-account", invoker,
		"--format", "json"))
	t.Cleanup(func() {
		runCLI(t, gcloudCLI("eventarc", "triggers", "delete", trigger, "--location", triggerLocation, "--quiet"))
	})
	var described struct {
		EventFilters []struct {
			Attribute string `json:"attribute"`
			Value     string `json:"value"`
			Operator  string `json:"operator"`
		} `json:"eventFilters"`
		Transport struct {
			Pubsub struct {
				Topic string `json:"topic"`
			} `json:"pubsub"`
		} `json:"transport"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("eventarc", "triggers", "describe", trigger,
		"--location", triggerLocation, "--format", "json")), &described)
	assert.NotEmpty(t, described.Transport.Pubsub.Topic, "Eventarc provisions the topic the trigger delivers through")
	var pattern string
	for _, f := range described.EventFilters {
		if f.Attribute == "resourceName" {
			assert.Equal(t, "match-path-pattern", f.Operator)
			pattern = f.Value
		}
	}
	assert.Equal(t, "/projects/_/buckets/cli-audit-*", pattern)

	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us", "--format=json"))
	t.Cleanup(func() { runCLI(t, gcloudCLI("storage", "buckets", "delete", "gs://"+bucket, "--quiet")) })

	var entries []struct {
		LogName      string `json:"logName"`
		Severity     string `json:"severity"`
		ProtoPayload struct {
			Type         string `json:"@type"`
			ServiceName  string `json:"serviceName"`
			MethodName   string `json:"methodName"`
			ResourceName string `json:"resourceName"`
		} `json:"protoPayload"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("logging", "read",
		`logName:cloudaudit AND protoPayload.methodName="storage.buckets.create" AND protoPayload.resourceName="projects/_/buckets/`+bucket+`"`,
		"--format", "json")), &entries)
	require.Len(t, entries, 1, "Cloud Storage writes one Admin Activity entry for the bucket")
	assert.Equal(t, "projects/"+project+"/logs/cloudaudit.googleapis.com%2Factivity", entries[0].LogName)
	assert.Equal(t, "NOTICE", entries[0].Severity)
	assert.Equal(t, "type.googleapis.com/google.cloud.audit.AuditLog", entries[0].ProtoPayload.Type)
	assert.Equal(t, "storage.googleapis.com", entries[0].ProtoPayload.ServiceName)

	// Cloud Logging ingests the service's stdout asynchronously, so the read
	// repeats until the line the workload wrote for the event arrives.
	var event struct {
		Attributes map[string]string `json:"attributes"`
		Data       string            `json:"data"`
	}
	require.Eventually(t, func() bool {
		out := runCLI(t, gcloudCLI("logging", "read",
			`resource.type="cloud_run_revision" AND resource.labels.service_name="`+serviceID+`"`,
			"--format", "json"))
		for _, payload := range logTextPayloads(out) {
			if line, ok := strings.CutPrefix(payload, "CLOUDEVENT "); ok {
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				return true
			}
		}
		return false
	}, 90*time.Second, 500*time.Millisecond, "the service never received the audit-log CloudEvent")
	assert.Equal(t, "google.cloud.audit.log.v1.written", event.Attributes["ce-type"])
	assert.Equal(t, "//cloudaudit.googleapis.com/projects/"+project+"/logs/activity", event.Attributes["ce-source"])
	assert.Equal(t, "storage.googleapis.com/projects/_/buckets/"+bucket, event.Attributes["ce-subject"])
	assert.Equal(t, "storage.buckets.create", event.Attributes["ce-methodname"])
	assert.Contains(t, event.Data, `"methodName":"storage.buckets.create"`)
}
