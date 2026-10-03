package gcp_tf_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTerraformCloudRunV2JobRunExecutionToken applies a google_cloud_run_v2_job
// with run_execution_token. The apply waits for the job's operation, which
// Cloud Run completes once the execution <job>-<token> has completed, so the
// file that execution wrote through its Cloud Storage volume is an object when
// the apply returns. A new token runs the job again.
func TestTerraformCloudRunV2JobRunExecutionToken(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "cloudrun-job-execution-token")
	cleanTerraformFixture(t, fixtureDir)
	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, "destroy", "-auto-approve", "-var", "token=second"))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})

	for _, token := range []string{"first", "second"} {
		out, err = runTimed(t, "terraform apply ("+token+")", terraformCmdInDir(fixtureDir, "apply", "-auto-approve", "-var", "token="+token))
		require.NoError(t, err, "terraform apply with token %s failed:\n%s", token, out)
		outputs := readOutputsInDir(t, fixtureDir)

		var execution struct {
			SucceededCount int    `json:"succeededCount"`
			CompletionTime string `json:"completionTime"`
		}
		require.NoError(t, json.Unmarshal(simCall(t, http.MethodGet,
			"/v2/"+outputs.must(t, "job_name")+"/executions/tf-crv2-token-job-"+token, ""), &execution))
		assert.Equal(t, 1, execution.SucceededCount, "the apply returned once the execution completed")
		assert.NotEmpty(t, execution.CompletionTime)
		assert.Equal(t, "ran\n", string(simCall(t, http.MethodGet,
			"/storage/v1/b/"+outputs.must(t, "bucket")+"/o/ran-"+token+"?alt=media", "")))
	}
}
