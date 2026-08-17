package gcp_cli_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloudRun_CLI_ArithmeticEval(t *testing.T) {
	jobID := "cli-arith-crj"
	createBody := fmt.Sprintf(`{
		"template": {
			"taskCount": 1,
			"template": {
				"containers": [{
					"name": "app",
					"image": %q,
					"args": ["(3 + 4) * 2"]
				}],
				"maxRetries": 0,
				"timeout": "10s"
			}
		}
	}`, evalImageName)
	httpDoJSON(t, "POST", jobsBaseURL()+"?jobId="+jobID, createBody)

	// Run the job
	out := httpDoJSON(t, "POST", jobURL(jobID+":run"), "")

	var lro struct {
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	parseJSON(t, out, &lro)
	require.NotEmpty(t, lro.Response.Name)

	// Poll until the execution reaches a terminal state (succeeded+failed > 0)
	// rather than racing on a fixed sleep that a loaded CI runner could exceed.
	exec := waitForExecution(t, lro.Response.Name)
	assert.Equal(t, 1, exec.SucceededCount, "expected job to succeed")
	assert.Equal(t, 0, exec.FailedCount)

	// Poll Cloud Logging until the job's own output is ingested, then assert on
	// the entry that carries it. "14" occurs all over a log payload — inside
	// timestamps, inside insert ids — so a substring search over the whole
	// document is satisfied without the job's output ever being found; the
	// assertion is on an entry whose textPayload is the evaluated result and
	// nothing else.
	var payloads []string
	require.Eventually(t, func() bool {
		out = runCLI(t, gcloudCLI("logging", "read",
			`resource.type="cloud_run_job" AND resource.labels.job_name="`+jobID+`"`,
			"--format", "json",
		))
		payloads = logTextPayloads(out)
		return slices.Contains(payloads, "14")
	}, 60*time.Second, 250*time.Millisecond,
		"the job's evaluated result never reached Cloud Logging")
	assert.Contains(t, payloads, "14",
		"expected a Cloud Logging entry whose textPayload is the evaluated result: %s", out)

	// Cleanup
	httpDoJSON(t, "DELETE", jobURL(jobID), "")
}

// logTextPayloads returns the trimmed textPayload of every entry in a
// `gcloud logging read --format json` document. Output that is not a JSON array
// of entries yields no payloads, so a caller polling for one keeps polling
// rather than failing from inside the poll.
func logTextPayloads(out string) []string {
	start := strings.IndexAny(out, "[{")
	if start < 0 {
		return nil
	}
	var entries []struct {
		TextPayload string `json:"textPayload"`
	}
	if json.Unmarshal([]byte(out[start:]), &entries) != nil {
		return nil
	}
	payloads := make([]string, 0, len(entries))
	for _, e := range entries {
		payloads = append(payloads, strings.TrimSpace(e.TextPayload))
	}
	return payloads
}

type cloudRunExecutionCounts struct {
	SucceededCount int `json:"succeededCount"`
	FailedCount    int `json:"failedCount"`
}

// waitForExecution polls the Cloud Run execution resource until it reaches a
// terminal state (at least one task succeeded or failed). This replaces a fixed
// sleep, which races on a loaded CI runner where the job may not finish in time.
func waitForExecution(t *testing.T, execName string) cloudRunExecutionCounts {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var exec cloudRunExecutionCounts
	for time.Now().Before(deadline) {
		out := httpDoJSON(t, "GET", baseURL+"/v2/"+execName, "")
		exec = cloudRunExecutionCounts{}
		parseJSON(t, out, &exec)
		if exec.SucceededCount+exec.FailedCount > 0 {
			return exec
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("execution %s did not reach a terminal state within deadline", execName)
	return exec
}

func TestCloudRun_CLI_ArithmeticInvalid(t *testing.T) {
	jobID := "cli-arith-crj-fail"
	createBody := fmt.Sprintf(`{
		"template": {
			"taskCount": 1,
			"template": {
				"containers": [{
					"name": "app",
					"image": %q,
					"args": ["3 +"]
				}],
				"maxRetries": 0,
				"timeout": "10s"
			}
		}
	}`, evalImageName)
	httpDoJSON(t, "POST", jobsBaseURL()+"?jobId="+jobID, createBody)

	// Run the job
	out := httpDoJSON(t, "POST", jobURL(jobID+":run"), "")

	var lro struct {
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	parseJSON(t, out, &lro)
	require.NotEmpty(t, lro.Response.Name)

	// Poll until the execution reaches a terminal state (succeeded+failed > 0)
	// rather than racing on a fixed sleep that a loaded CI runner could exceed.
	exec := waitForExecution(t, lro.Response.Name)
	assert.Equal(t, 0, exec.SucceededCount)
	assert.Equal(t, 1, exec.FailedCount, "expected job to fail")

	// Cleanup
	httpDoJSON(t, "DELETE", jobURL(jobID), "")
}
