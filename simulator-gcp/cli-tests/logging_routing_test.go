package gcp_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogging_CLI_DefaultSinkExclusionsAndViews adds an exclusion to a
// project's _Default sink with gcloud, reads it back from the sink, and lists
// the built-in views of the project's _Default bucket.
func TestLogging_CLI_DefaultSinkExclusionsAndViews(t *testing.T) {
	t.Parallel()
	const project = "cli-logging-routing"

	runCLI(t, gcloudCLI("logging", "sinks", "update", "_Default",
		"--add-exclusion=name=drop-debug,filter=severity=DEBUG", "--project="+project, "--quiet"))
	var sink struct {
		Exclusions []struct {
			Name   string `json:"name"`
			Filter string `json:"filter"`
		} `json:"exclusions"`
		Destination string `json:"destination"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("logging", "sinks", "describe", "_Default", "--project="+project, "--format=json")), &sink)
	require.Len(t, sink.Exclusions, 1)
	assert.Equal(t, "drop-debug", sink.Exclusions[0].Name)
	assert.Equal(t, "logging.googleapis.com/projects/"+project+"/locations/global/buckets/_Default", sink.Destination)

	var views []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("logging", "views", "list", "--bucket=_Default", "--location=global",
		"--project="+project, "--format=json")), &views)
	var names []string
	for _, v := range views {
		names = append(names, v.Name)
	}
	bucket := "projects/" + project + "/locations/global/buckets/_Default/views/"
	assert.ElementsMatch(t, []string{bucket + "_AllLogs", bucket + "_Default"}, names)
}
