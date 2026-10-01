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

	exec, op := runJobToCompletion(t, jobID)
	require.Nil(t, op.Error, "the execution succeeded, so its RunJob operation did")
	assert.Equal(t, 1, exec.SucceededCount, "expected job to succeed")
	assert.Equal(t, 0, exec.FailedCount)

	// Poll Cloud Logging until the job's own output is ingested, then assert on
	// the entry that carries it. "14" occurs all over a log payload — inside
	// timestamps, inside insert ids — so a substring search over the whole
	// document is satisfied without the job's output ever being found; the
	// assertion is on an entry whose textPayload is the evaluated result and
	// nothing else.
	var out string
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

// runJobToCompletion runs a job, waits on the RunJob operation until the
// execution finishes, and returns the settled execution with the operation.
func runJobToCompletion(t *testing.T, jobID string) (cloudRunExecutionCounts, runOperationResult) {
	t.Helper()
	run := runJob(t, jobID)
	op := waitRunOperation(t, run.Operation)
	var exec cloudRunExecutionCounts
	parseJSON(t, httpDoJSON(t, "GET", baseURL+"/v2/"+run.Execution, ""), &exec)
	return exec, op
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

	exec, op := runJobToCompletion(t, jobID)
	require.NotNil(t, op.Error, "a failed execution fails its RunJob operation")
	assert.Equal(t, 0, exec.SucceededCount)
	assert.Equal(t, 1, exec.FailedCount, "expected job to fail")

	// Cleanup
	httpDoJSON(t, "DELETE", jobURL(jobID), "")
}
