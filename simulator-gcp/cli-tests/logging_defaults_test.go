package gcp_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogging_CLI_BuiltInBuckets reads a project's built-in buckets and sinks
// with gcloud, changes the _Default bucket's retention, and is refused the
// deletion of the _Required sink.
func TestLogging_CLI_BuiltInBuckets(t *testing.T) {
	t.Parallel()
	const project = "cli-logging-defaults"
	runCLI(t, gcloudCLI("projects", "create", project, "--organization="+cliOrganizationID, "--format=json"))

	var def struct {
		RetentionDays int `json:"retentionDays"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("logging", "buckets", "describe", "_Default",
		"--location=global", "--project="+project, "--format=json")), &def)
	assert.Equal(t, 30, def.RetentionDays)

	var sinks []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("logging", "sinks", "list", "--project="+project, "--format=json")), &sinks)
	var names []string
	for _, s := range sinks {
		names = append(names, s.Name)
	}
	assert.Contains(t, names, "_Default")
	assert.Contains(t, names, "_Required")

	var updated struct {
		RetentionDays int `json:"retentionDays"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("logging", "buckets", "update", "_Default",
		"--location=global", "--retention-days=45", "--project="+project, "--format=json")), &updated)
	require.Equal(t, 45, updated.RetentionDays)

	gcloudCLIFails(t, gcloudCLI("logging", "sinks", "delete", "_Required", "--project="+project, "--quiet"))
}
