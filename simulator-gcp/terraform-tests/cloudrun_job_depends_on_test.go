package gcp_tf_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTerraformCloudRunV2JobDependsOn applies a google_cloud_run_v2_job whose
// main container depends_on a server container with a startup_probe, runs the
// job, and requires it to succeed: the main container's single connection
// attempt reaches the server only when it starts after the server's probe
// passed.
func TestTerraformCloudRunV2JobDependsOn(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "cloudrun-job-depends-on")
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
	assert.Equal(t, []any{"server"}, outputs.mustValue(t, "depends_on"), "the provider reads depends_on back")

	var run struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(simCall(t, http.MethodPost, "/v2/"+outputs.must(t, "job_name")+":run", "{}"), &run))
	require.NotEmpty(t, run.Name)
	var op struct {
		Done  bool            `json:"done"`
		Error json.RawMessage `json:"error"`
	}
	for !op.Done {
		require.NoError(t, json.Unmarshal(simCall(t, http.MethodPost, "/v2/"+run.Name+":wait", `{"timeout":"120s"}`), &op))
	}
	require.Empty(t, op.Error, "the job's execution failed")
}
