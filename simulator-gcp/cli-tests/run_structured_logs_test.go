package gcp_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A JSON line a job's container prints is read back through `gcloud logging
// read` as jsonPayload, found by a jsonPayload.message filter, with its
// severity and labels lifted onto the entry and the message kept in the
// payload. `execute --wait` returns once the execution's log lines are in
// place, so the read follows it directly.
func TestCloudRun_CLI_JobStructuredLogLine(t *testing.T) {
	t.Parallel()
	const job = "cli-job-json-log"
	const line = `{"severity":"ERROR","message":"structured from the job","component":"worker","logging.googleapis.com/labels":{"team":"sim"}}`
	// ^;^ makes ";" the list separator, so the JSON's commas stay in the line.
	runCLI(t, gcloudRegionalRunCLI("run", "jobs", "create", job,
		"--region="+location,
		"--quiet",
		"--image="+commandImageName,
		"--args=^;^log;"+line,
	))
	t.Cleanup(func() {
		runCLI(t, gcloudRegionalRunCLI("run", "jobs", "delete", job, "--region="+location, "--quiet"))
	})
	runCLI(t, gcloudRegionalRunCLI("run", "jobs", "execute", job, "--region="+location, "--wait", "--quiet"))

	var entries []struct {
		Severity    string            `json:"severity"`
		TextPayload string            `json:"textPayload"`
		JsonPayload map[string]any    `json:"jsonPayload"`
		Labels      map[string]string `json:"labels"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("logging", "read",
		`resource.type="cloud_run_job" AND resource.labels.job_name="`+job+`" AND jsonPayload.message="structured from the job"`,
		"--format=json",
	)), &entries)
	require.Len(t, entries, 1, "one entry carries the structured line")
	entry := entries[0]
	assert.Empty(t, entry.TextPayload)
	assert.Equal(t, "ERROR", entry.Severity)
	assert.Equal(t, "structured from the job", entry.JsonPayload["message"])
	assert.Equal(t, "worker", entry.JsonPayload["component"])
	assert.NotContains(t, entry.JsonPayload, "severity")
	assert.NotContains(t, entry.JsonPayload, "logging.googleapis.com/labels")
	assert.Equal(t, "sim", entry.Labels["team"])
}
