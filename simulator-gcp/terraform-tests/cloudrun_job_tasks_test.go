package gcp_tf_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTerraformCloudRunV2JobRunsTasksWithRetries applies a
// google_cloud_run_v2_job with task_count, parallelism and max_retries and a
// run_execution_token, so the apply returns once the execution completed: all
// three tasks succeeded, the one that failed its first attempt retried once,
// and a second plan finds the job as configured.
func TestTerraformCloudRunV2JobRunsTasksWithRetries(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "cloudrun-job-tasks")
	cleanTerraformFixture(t, fixtureDir)
	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, "destroy", "-auto-approve"))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})

	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)
	outputs := readOutputsInDir(t, fixtureDir)
	execution := "/v2/" + outputs.must(t, "job_name") + "/executions/tf-crv2-tasks-job-run"

	var exec struct {
		TaskCount      int `json:"taskCount"`
		Parallelism    int `json:"parallelism"`
		SucceededCount int `json:"succeededCount"`
		FailedCount    int `json:"failedCount"`
		RetriedCount   int `json:"retriedCount"`
	}
	require.NoError(t, json.Unmarshal(simCall(t, http.MethodGet, execution, ""), &exec))
	assert.Equal(t, 3, exec.TaskCount)
	assert.Equal(t, 2, exec.Parallelism)
	assert.Equal(t, 3, exec.SucceededCount)
	assert.Equal(t, 0, exec.FailedCount)
	assert.Equal(t, 1, exec.RetriedCount)

	var tasks struct {
		Tasks []struct {
			Index   int `json:"index"`
			Retried int `json:"retried"`
		} `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(simCall(t, http.MethodGet, execution+"/tasks", ""), &tasks))
	require.Len(t, tasks.Tasks, 3)
	for _, task := range tasks.Tasks {
		want := 0
		if task.Index == 1 {
			want = 1
		}
		assert.Equal(t, want, task.Retried, "task %d", task.Index)
	}

	out, err = runTimed(t, "terraform plan", terraformCmdInDir(fixtureDir, "plan", "-detailed-exitcode"))
	require.NoError(t, err, "the job read back differs from its configuration:\n%s", out)
}
