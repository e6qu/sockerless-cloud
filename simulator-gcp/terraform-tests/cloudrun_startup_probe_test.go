package gcp_tf_test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTerraformCloudRunV2StartupProbe applies google_cloud_run_v2_service
// resources whose containers carry template.containers.startup_probe and
// requests each at the uri the provider read back: the service whose HTTP
// probe the container passes serves the request, and the one whose TCP probe
// names a port nothing listens on answers 503. A service account granted
// roles/run.invoker on both presents the ID tokens the requests carry.
func TestTerraformCloudRunV2StartupProbe(t *testing.T) {
	image := buildProbeImage(t)
	fixtureDir := filepath.Join("fixtures", "cloudrun-startup-probe")
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
	status, body := invokeWithIDToken(t, outputs.must(t, "probed_uri"), "/probed", outputs.must(t, "probed_id_token"))
	require.Equal(t, http.StatusOK, status, "body=%q", body)
	assert.Equal(t, "GET /probed", body)

	status, body = invokeWithIDToken(t, outputs.must(t, "never_ready_uri"), "/probed", outputs.must(t, "never_ready_id_token"))
	assert.Equal(t, http.StatusServiceUnavailable, status, "body=%q", body)
}

// buildProbeImage builds testdata/http-localhost-probe into a local image for
// the services to run.
func buildProbeImage(t *testing.T) string {
	t.Helper()
	const image = "sockerless-http-localhost-probe:gcp-terraform"
	sourceDir, err := filepath.Abs("../../testdata/http-localhost-probe")
	require.NoError(t, err)
	buildDir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(buildDir, "http-localhost-probe"), ".")
	build.Dir = sourceDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build the probe workload:\n%s", out)

	dockerfile := "FROM scratch\nCOPY http-localhost-probe /usr/local/bin/http-localhost-probe\nENTRYPOINT [\"/usr/local/bin/http-localhost-probe\"]\n"
	args := []string{"build"}
	if exec.Command("docker", "buildx", "version").Run() == nil {
		args = []string{"buildx", "build", "--load"}
	}
	args = append(args, "--platform", "linux/"+runtime.GOARCH, "-t", image, "-f", "-", buildDir)
	docker := exec.Command("docker", args...)
	docker.Stdin = strings.NewReader(dockerfile)
	out, err = docker.CombinedOutput()
	require.NoError(t, err, "build the probe image:\n%s", out)
	return image
}
