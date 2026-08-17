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

// TestLogging_Wire_WriteAndRead drives entries:write and entries:list on the
// wire, where a batch of entries can be written in one request and the list
// request body can carry an arbitrary filter — neither of which `gcloud
// logging write` and `gcloud logging read` expose. The CLI round trip over the
// same two routes is TestLogging_CLI_WriteAndRead.
func TestLogging_Wire_WriteAndRead(t *testing.T) {
	writeURL := fmt.Sprintf("%s/v2/entries:write", baseURL)
	httpDoJSON(t, "POST", writeURL, fmt.Sprintf(`{
		"logName": "projects/%s/logs/cli-test-log",
		"resource": {"type": "global"},
		"entries": [
			{"textPayload": "Hello from CLI test", "severity": "INFO"},
			{"textPayload": "Second log message", "severity": "WARNING"}
		]
	}`, project))

	// Read log entries
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

// TestLogging_CLI_WriteAndRead drives the same two routes through the vendor
// CLI. `gcloud logging write` and `gcloud logging read` both ride the
// CLOUDSDK_API_ENDPOINT_OVERRIDES_LOGGING coordinate, so the write and the
// read that finds it are real gcloud invocations.
func TestLogging_CLI_WriteAndRead(t *testing.T) {
	const logID = "cli-gcloud-write-log"
	const payload = "written by the gcloud CLI"

	runCLI(t, gcloudCLI("logging", "write", logID, payload,
		"--severity=NOTICE",
		"--payload-type=text",
	))

	// Ingestion is asynchronous, so the read is polled; the assertion is on an
	// entry whose textPayload is the message that was written.
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

	// The severity the write carried survives the round trip.
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
		"filter": "ERROR"
	}`, project))

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
