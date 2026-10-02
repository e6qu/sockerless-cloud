package gcp_tf_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTerraformEventarcAuditLogTrigger applies a google_eventarc_trigger whose
// matching_criteria select Cloud Storage's storage.buckets.create audit
// entries for buckets matching a path pattern, delivering to a Cloud Run
// service, then a google_storage_bucket that matches: Cloud Storage writes the
// Admin Activity entry, and the service receives it as a
// google.cloud.audit.log.v1.written CloudEvent.
func TestTerraformEventarcAuditLogTrigger(t *testing.T) {
	image := buildProbeImage(t)
	fixtureDir := filepath.Join("fixtures", "eventarc-audit-log")
	cleanTerraformFixture(t, fixtureDir)
	withImage := func(cmd *exec.Cmd) *exec.Cmd {
		cmd.Env = append(cmd.Env, "TF_VAR_image="+image)
		return cmd
	}
	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", withImage(terraformCmdInDir(fixtureDir, "destroy", "-auto-approve")))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})
	out, err = runTimed(t, "terraform apply", withImage(terraformCmdInDir(fixtureDir, "apply", "-auto-approve")))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	outputs := readOutputsInDir(t, fixtureDir)
	serviceID := outputs.must(t, "service_name")
	bucket := outputs.must(t, "bucket")
	assert.True(t, strings.HasPrefix(outputs.must(t, "transport_topic"), "projects/test-project/topics/eventarc-us-central1-tf-audit-bucket-created-"),
		"Eventarc provisions the trigger's transport topic")

	out, err = runTimed(t, "terraform plan", withImage(terraformCmdInDir(fixtureDir, "plan", "-detailed-exitcode")))
	require.NoError(t, err, "a second plan must find nothing to change:\n%s", out)

	type entry struct {
		LogName      string `json:"logName"`
		TextPayload  string `json:"textPayload"`
		ProtoPayload struct {
			MethodName   string `json:"methodName"`
			ResourceName string `json:"resourceName"`
		} `json:"protoPayload"`
	}
	list := func(filter string) []entry {
		body, err := json.Marshal(map[string]any{"resourceNames": []string{"projects/test-project"}, "filter": filter})
		require.NoError(t, err)
		var page struct {
			Entries []entry `json:"entries"`
		}
		require.NoError(t, json.Unmarshal(simCall(t, "POST", "/v2/entries:list", string(body)), &page))
		return page.Entries
	}

	audited := list(`logName="projects/test-project/logs/cloudaudit.googleapis.com%2Factivity" AND protoPayload.methodName="storage.buckets.create" AND protoPayload.resourceName="projects/_/buckets/` + bucket + `"`)
	require.Len(t, audited, 1, "Cloud Storage writes one Admin Activity entry for the bucket terraform created")

	// Cloud Logging ingests the service's stdout asynchronously, so the read
	// repeats until the line the workload wrote for the event arrives.
	var event struct {
		Attributes map[string]string `json:"attributes"`
		Data       string            `json:"data"`
	}
	require.Eventually(t, func() bool {
		for _, e := range list(`resource.type="cloud_run_revision" AND resource.labels.service_name="` + serviceID + `"`) {
			if line, ok := strings.CutPrefix(e.TextPayload, "CLOUDEVENT "); ok {
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				return true
			}
		}
		return false
	}, 90*time.Second, 500*time.Millisecond, "the service never received the audit-log CloudEvent")
	assert.Equal(t, "google.cloud.audit.log.v1.written", event.Attributes["ce-type"])
	assert.Equal(t, "//cloudaudit.googleapis.com/projects/test-project/logs/activity", event.Attributes["ce-source"])
	assert.Equal(t, "storage.googleapis.com/projects/_/buckets/"+bucket, event.Attributes["ce-subject"])
	assert.Equal(t, "storage.buckets.create", event.Attributes["ce-methodname"])
	assert.Equal(t, "projects/_/buckets/"+bucket, event.Attributes["ce-resourcename"])
	assert.Contains(t, event.Data, `"methodName":"storage.buckets.create"`)
}
