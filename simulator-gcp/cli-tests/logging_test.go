package gcp_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogging_WriteAndRead(t *testing.T) {
	// Write log entries via direct HTTP (gcloud logging write may not support endpoint overrides)
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
