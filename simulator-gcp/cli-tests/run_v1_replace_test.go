package gcp_cli_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `gcloud run services replace` and `gcloud run jobs executions cancel` resolve
// the regional Cloud Run host, `{region}-run.googleapis.com`. gcloud's own HTTP
// proxy setting carries those bytes to a loopback simulator, so the URL, the
// body and the credential stay unmodified; TestGCloudRegionPrefixedEndpointHost
// pins that coordinate.

// gcloudRegionalRunCLI is gcloudCLI with the proxy coordinate that delivers the
// region-prefixed Cloud Run host to the simulator.
func gcloudRegionalRunCLI(args ...string) *exec.Cmd {
	cmd := gcloudCLI(args...)
	cmd.Env = append(cmd.Env, "HTTP_PROXY="+baseURL, "NO_PROXY=")
	return cmd
}

func knativeServiceURL(name string) string {
	return fmt.Sprintf("%s/apis/serving.knative.dev/v1/namespaces/%s/services/%s", baseURL, project, name)
}

// TestCloudRunV1_CLI_ServicesReplaceSendsResourceVersion: gcloud reads the
// service, copies its resourceVersion into the replace body and PUTs it, so the
// replace must succeed and move the version on. A PUT carrying the version from
// before that replace, as a concurrent client holds it, must be refused.
func TestCloudRunV1_CLI_ServicesReplaceSendsResourceVersion(t *testing.T) {
	t.Parallel()
	const name = "cli-rv-service"
	body := func(image string) string {
		return fmt.Sprintf(`{
			"apiVersion": "serving.knative.dev/v1",
			"kind": "Service",
			"metadata": {"name": %q, "namespace": %q},
			"spec": {"template": {"spec": {"containers": [{"image": %q}]}}}
		}`, name, project, image)
	}
	created := httpDoJSON(t, "POST",
		fmt.Sprintf("%s/apis/serving.knative.dev/v1/namespaces/%s/services", baseURL, project),
		body("gcr.io/test-project/hello"))
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", knativeServiceURL(name), "")
		if err == nil {
			resp.Body.Close()
		}
	})

	var before struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	parseJSON(t, created, &before)
	require.NotEmpty(t, before.Metadata.ResourceVersion)

	manifest := filepath.Join(tmpDir, "cli-rv-service.yaml")
	require.NoError(t, os.WriteFile(manifest, []byte(fmt.Sprintf(`apiVersion: serving.knative.dev/v1
kind: Service
metadata:
  name: %s
  namespace: %s
spec:
  template:
    spec:
      containers:
      - image: gcr.io/test-project/hello2
`, name, project)), 0o600))

	out := runCLI(t, gcloudRegionalRunCLI("run", "services", "replace", manifest,
		"--region="+location, "--format=value(metadata.resourceVersion)"))
	moved := strings.TrimSpace(out)
	require.NotEmpty(t, moved)
	assert.NotEqual(t, before.Metadata.ResourceVersion, moved,
		"the replace gcloud sent was accepted and moved the service on")

	stale := fmt.Sprintf(`{
		"apiVersion": "serving.knative.dev/v1",
		"kind": "Service",
		"metadata": {"name": %q, "namespace": %q, "resourceVersion": %q},
		"spec": {"template": {"spec": {"containers": [{"image": "gcr.io/test-project/hello3"}]}}}
	}`, name, project, before.Metadata.ResourceVersion)
	resp, err := httpDo("PUT", knativeServiceURL(name), stale)
	require.NoError(t, err)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, 409, resp.StatusCode, "a stale resourceVersion must be refused: %s", data)
	var conflict struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(data, &conflict))
	assert.Equal(t, "ABORTED", conflict.Error.Status)
	assert.Contains(t, conflict.Error.Message, "resourceVersion")
}

// TestCloudRunV1_CLI_ExecutionsCancel drives the published cancel verb on the
// executions collection through gcloud.
func TestCloudRunV1_CLI_ExecutionsCancel(t *testing.T) {
	t.Parallel()
	// Cloud Logging keeps a deleted job's entries, so a reused job name would
	// read an earlier run's lines.
	jobID := fmt.Sprintf("cli-verb-job-%d", time.Now().UnixNano())
	// Nothing but the cancel settles the execution: the step outlives the test,
	// announces its SIGTERM handler and exits through it.
	httpDoJSON(t, "POST", jobsBaseURL()+"?jobId="+jobID, `{
		"template": {"template": {"containers": [
			{"image": "`+cliWorkloadImage+`", "command": ["sh", "-c"],
			 "args": ["trap 'echo stopping; exit 143' TERM; echo started; sleep 3600 & wait $!"]}
		]}}
	}`)
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", jobURL(jobID), "")
		if err == nil {
			resp.Body.Close()
		}
	})

	run := runJob(t, jobID)
	execID := run.Execution[strings.LastIndex(run.Execution, "/")+1:]

	type v2Execution struct {
		RunningCount   int    `json:"runningCount"`
		CancelledCount int    `json:"cancelledCount"`
		SucceededCount int    `json:"succeededCount"`
		FailedCount    int    `json:"failedCount"`
		CompletionTime string `json:"completionTime"`
		Conditions     []struct {
			Type   string `json:"type"`
			State  string `json:"state"`
			Reason string `json:"reason"`
		} `json:"conditions"`
	}
	readExecution := func() v2Execution {
		var execution v2Execution
		parseJSON(t, httpDoJSON(t, "GET", baseURL+"/v2/"+run.Execution, ""), &execution)
		return execution
	}
	// The container's own log line says its SIGTERM handler is in place. Match the
	// whole line: the job's log also holds lines the service writes.
	require.Eventually(t, func() bool {
		out := runCLI(t, gcloudCLI("logging", "read",
			`resource.type="cloud_run_job" AND resource.labels.job_name="`+jobID+`"`,
			"--format=value(textPayload)"))
		return slices.Contains(strings.Split(out, "\n"), "started")
	}, 60*time.Second, 250*time.Millisecond, "the execution's workload container never came up")
	require.Equal(t, 1, readExecution().RunningCount,
		"the execution's task must still be running when the cancel is sent")

	runCLI(t, gcloudRegionalRunCLI("run", "jobs", "executions", "cancel", execID,
		"--region="+location, "--format=value(metadata.name)"))

	// The RunJob operation completes CANCELLED once the cancelled workload has
	// stopped and the execution and its task have settled.
	op := waitRunOperation(t, run.Operation)
	require.NotNil(t, op.Error, "the cancel the CLI sent must end the RunJob operation")
	assert.Equal(t, 1, op.Error.Code, "google.rpc.Code.CANCELLED")
	settled := readExecution()
	require.NotEmpty(t, settled.CompletionTime, "the cancel the CLI sent must settle the execution")

	// A cancelled execution counts its stopped task as cancelled, none succeeded
	// or failed, and both terminal conditions carry the cancellation reason.
	assert.Equal(t, 1, settled.CancelledCount, "the running task is counted as cancelled")
	assert.Equal(t, 0, settled.RunningCount)
	assert.Equal(t, 0, settled.SucceededCount, "nothing succeeded — the task was stopped")
	assert.Equal(t, 0, settled.FailedCount)
	byType := map[string]string{}
	for _, c := range settled.Conditions {
		byType[c.Type] = c.State + "/" + c.Reason
	}
	assert.Equal(t, "CONDITION_FAILED/Cancelled", byType["Ready"])
	assert.Equal(t, "CONDITION_FAILED/Cancelled", byType["Completed"])

	// Cancelling must also stop the workload container, or `sleep 3600` keeps
	// running behind an execution both API versions call cancelled. The task
	// settles from the container's exit, with the code its SIGTERM handler
	// returned.
	var task struct {
		CompletionTime    string `json:"completionTime"`
		LastAttemptResult *struct {
			ExitCode int `json:"exitCode"`
		} `json:"lastAttemptResult"`
	}
	var tasks struct {
		Tasks []json.RawMessage `json:"tasks"`
	}
	parseJSON(t, httpDoJSON(t, "GET", baseURL+"/v2/"+run.Execution+"/tasks", ""), &tasks)
	require.Len(t, tasks.Tasks, 1)
	require.NoError(t, json.Unmarshal(tasks.Tasks[0], &task))
	require.NotEmpty(t, task.CompletionTime, "the cancelled execution's task settled")
	require.NotNil(t, task.LastAttemptResult)
	assert.Equal(t, 143, task.LastAttemptResult.ExitCode,
		"the cancel must stop the workload container through its SIGTERM handler, not just the record")

	// No CLI spells an unpublished verb, so send it on the wire: the collection
	// refuses it rather than cancelling.
	resp, err := httpDo("POST", fmt.Sprintf(
		"%s/apis/run.googleapis.com/v1/namespaces/%s/executions/%s:teleport", baseURL, project, execID), "{}")
	require.NoError(t, err)
	v1Body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode, "body: %s", v1Body)
	assert.Contains(t, string(v1Body), "unknown action")

	resp, err = httpDo("POST", baseURL+"/v2/"+run.Execution+":teleport", "{}")
	require.NoError(t, err)
	v2Body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode, "body: %s", v2Body)
	assert.Contains(t, string(v2Body), "unknown action")
}
