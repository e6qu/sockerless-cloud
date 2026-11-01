package gcp_cli_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogging_Wire_WriteAndRead drives entries:write and entries:list with a
// multi-entry batch and an arbitrary filter, neither of which `gcloud logging
// write` and `gcloud logging read` expose.
func TestLogging_Wire_WriteAndRead(t *testing.T) {
	t.Parallel()
	writeURL := fmt.Sprintf("%s/v2/entries:write", baseURL)
	httpDoJSON(t, "POST", writeURL, fmt.Sprintf(`{
		"logName": "projects/%s/logs/cli-test-log",
		"resource": {"type": "global"},
		"entries": [
			{"textPayload": "Hello from CLI test", "severity": "INFO"},
			{"textPayload": "Second log message", "severity": "WARNING"}
		]
	}`, project))

	listURL := fmt.Sprintf("%s/v2/entries:list", baseURL)
	out := httpDoJSON(t, "POST", listURL, fmt.Sprintf(`{
		"resourceNames": ["projects/%s"],
		"filter": "cli-test-log"
	}`, project))

	var result struct {
		Entries []struct {
			TextPayload string `json:"textPayload"`
			Severity    string `json:"severity"`
		} `json:"entries"`
	}
	parseJSON(t, out, &result)
	require.GreaterOrEqual(t, len(result.Entries), 2)

	messages := make([]string, len(result.Entries))
	for i, e := range result.Entries {
		messages[i] = e.TextPayload
	}
	assert.Contains(t, messages, "Hello from CLI test")
	assert.Contains(t, messages, "Second log message")
}

// TestLogging_CLI_WriteAndRead drives the same two routes through gcloud, over
// the CLOUDSDK_API_ENDPOINT_OVERRIDES_LOGGING coordinate.
func TestLogging_CLI_WriteAndRead(t *testing.T) {
	t.Parallel()
	const logID = "cli-gcloud-write-log"
	const payload = "written by the gcloud CLI"

	runCLI(t, gcloudCLI("logging", "write", logID, payload,
		"--severity=NOTICE",
		"--payload-type=text",
	))

	// Ingestion is asynchronous, so poll for an entry whose textPayload is the
	// written message.
	logName := fmt.Sprintf("projects/%s/logs/%s", project, logID)
	var out string
	var payloads []string
	require.Eventually(t, func() bool {
		out = runCLI(t, gcloudCLI("logging", "read",
			fmt.Sprintf("logName=%q", logName),
			"--format", "json",
		))
		payloads = logTextPayloads(out)
		return slices.Contains(payloads, payload)
	}, 60*time.Second, 250*time.Millisecond,
		"the entry gcloud wrote never came back from gcloud logging read")
	assert.Contains(t, payloads, payload, "read output: %s", out)

	var entries []struct {
		LogName     string `json:"logName"`
		Severity    string `json:"severity"`
		TextPayload string `json:"textPayload"`
	}
	parseJSON(t, out, &entries)
	found := false
	for _, e := range entries {
		if strings.TrimSpace(e.TextPayload) != payload {
			continue
		}
		found = true
		assert.Equal(t, logName, e.LogName)
		assert.Equal(t, "NOTICE", e.Severity)
	}
	require.True(t, found, "read output: %s", out)
}

func TestLogging_ReadWithFilter(t *testing.T) {
	t.Parallel()
	writeURL := fmt.Sprintf("%s/v2/entries:write", baseURL)
	httpDoJSON(t, "POST", writeURL, fmt.Sprintf(`{
		"logName": "projects/%s/logs/filter-test-log",
		"resource": {"type": "global"},
		"entries": [
			{"textPayload": "ERROR something failed", "severity": "ERROR"},
			{"textPayload": "INFO everything is fine", "severity": "INFO"}
		]
	}`, project))

	listURL := fmt.Sprintf("%s/v2/entries:list", baseURL)
	out := httpDoJSON(t, "POST", listURL, fmt.Sprintf(`{
		"resourceNames": ["projects/%s"],
		"filter": "logName=\"projects/%s/logs/filter-test-log\" AND ERROR"
	}`, project, project))

	var result struct {
		Entries []struct {
			TextPayload string `json:"textPayload"`
		} `json:"entries"`
	}
	parseJSON(t, out, &result)
	require.GreaterOrEqual(t, len(result.Entries), 1)

	for _, e := range result.Entries {
		assert.Contains(t, e.TextPayload, "ERROR")
	}
}

// `gcloud logging sinks create` asks for a unique writer identity through the
// uniqueWriterIdentity parameter, and the sink writes as the project's Cloud
// Logging service agent.
func TestLogging_CLI_SinkWriterIdentityIsTheServiceAgent(t *testing.T) {
	t.Parallel()
	number := strings.TrimSpace(runCLI(t, gcloudCLI("projects", "describe", project, "--format=value(projectNumber)")))
	runCLI(t, gcloudCLI("logging", "sinks", "create", "cli-writer-sink",
		"storage.googleapis.com/cli-writer-sink-bucket", "--quiet"))
	t.Cleanup(func() { runCLI(t, gcloudCLI("logging", "sinks", "delete", "cli-writer-sink", "--quiet")) })
	writer := strings.TrimSpace(runCLI(t, gcloudCLI("logging", "sinks", "describe", "cli-writer-sink",
		"--format=value(writerIdentity)")))
	assert.Equal(t, "serviceAccount:service-"+number+"@gcp-sa-logging.iam.gserviceaccount.com", writer)
}
