package gcp_cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tokenJobManifest(t *testing.T, name, token, script string) string {
	t.Helper()
	manifest := filepath.Join(tmpDir, name+"-"+fmt.Sprint(len(token))+".yaml")
	require.NoError(t, os.WriteFile(manifest, []byte(fmt.Sprintf(`apiVersion: run.googleapis.com/v1
kind: Job
metadata:
  name: %s
  namespace: %s
spec:
  runExecutionToken: %s
  template:
    spec:
      taskCount: 1
      template:
        spec:
          maxRetries: 0
          containers:
          - image: alpine:latest
            command: [sh, -c, %q]
`, name, project, token, script)), 0o600))
	return manifest
}

// `gcloud run jobs replace` sends the Knative job's spec.runExecutionToken,
// and waits for the job's Ready condition, which Cloud Run holds until the
// execution <job>-<token> has completed; that execution has therefore
// succeeded when the command returns. A job name and token of 63 characters
// or more are refused.
func TestCloudRunJobs_CLI_ReplaceWithRunExecutionToken(t *testing.T) {
	const job = "cli-token-job"
	runCLI(t, gcloudRegionalRunCLI("run", "jobs", "replace", tokenJobManifest(t, job, "first", "echo ran"),
		"--region="+location, "--quiet"))
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", jobURL(job), "")
		if err == nil {
			resp.Body.Close()
		}
	})

	var execution struct {
		Status struct {
			SucceededCount int    `json:"succeededCount"`
			CompletionTime string `json:"completionTime"`
		} `json:"status"`
	}
	parseJSON(t, runCLI(t, gcloudRegionalRunCLI("run", "jobs", "executions", "describe", job+"-first",
		"--region="+location, "--format=json")), &execution)
	assert.Equal(t, 1, execution.Status.SucceededCount, "the job was ready only once its execution completed")
	assert.NotEmpty(t, execution.Status.CompletionTime)

	described := runCLI(t, gcloudRegionalRunCLI("run", "jobs", "describe", job, "--region="+location, "--format=value(spec.runExecutionToken)"))
	assert.Equal(t, "first", strings.TrimSpace(described))

	refused := gcloudCLIFails(t, gcloudRegionalRunCLI("run", "jobs", "replace",
		tokenJobManifest(t, job, strings.Repeat("t", 63-len(job)), "echo ran"), "--region="+location, "--quiet"))
	assert.Contains(t, refused, "63 characters")
}
