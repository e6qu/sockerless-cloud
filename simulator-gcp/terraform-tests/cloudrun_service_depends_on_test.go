package gcp_tf_test

import (
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTerraformCloudRunV2ServiceDependsOn applies a google_cloud_run_v2_service
// whose ingress container depends_on a sidecar with a startup_probe and
// requests it at the uri the provider read back: the ingress serves only when
// it starts after the sidecar passed its probe.
func TestTerraformCloudRunV2ServiceDependsOn(t *testing.T) {
	image := buildProbeImage(t)
	fixtureDir := filepath.Join("fixtures", "cloudrun-service-depends-on")
	cleanTerraformFixture(t, fixtureDir)

	withImage := func(cmd *exec.Cmd) *exec.Cmd {
		cmd.Env = append(cmd.Env, "TF_VAR_image="+image)
		return cmd
	}
	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", withImage(terraformCmdInDir(fixtureDir, "destroy", "-auto-approve")))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})
	out, err = runTimed(t, "terraform apply", withImage(terraformCmdInDir(fixtureDir, "apply", "-auto-approve")))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	outputs := readOutputsInDir(t, fixtureDir)
	assert.Equal(t, []any{"sidecar"}, outputs.mustValue(t, "depends_on"), "the provider reads depends_on back")
	status, body := invokeWithIDToken(t, outputs.must(t, "uri"), "/", outputs.must(t, "id_token"))
	require.Equal(t, http.StatusOK, status, "body=%q", body)
	assert.Equal(t, "tf-sidecar-started-first", body)
}
