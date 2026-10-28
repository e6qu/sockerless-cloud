package gcp_cli_test

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The `gcloud` CLI has two official non-interactive credential paths: a
// service-account key file (covered by TestIAMServiceAccountKeysActivateCLI)
// and the metadata server, which is how it authenticates on a workload Google
// Cloud runs. This file drives the second with the real CLI from inside a
// Cloud Run task, carrying no pre-minted access token: the credential gcloud
// uses is the one it reads from the metadata server, which answers the
// workloads Google Cloud runs and nothing else.

// gcloudSDKRoot is the installation the suite's gcloud runs from.
func gcloudSDKRoot(t *testing.T) string {
	t.Helper()
	root := strings.TrimSpace(runCLI(t, exec.Command("gcloud", "info", "--format=value(installation.sdk_root)")))
	require.NotEmpty(t, root)
	return root
}

// gcloudPythonImage runs the suite's gcloud inside a workload: the
// installation brings everything but an interpreter.
const gcloudPythonImage = "mirror.gcr.io/library/python:3.12-slim"

// gcloudInWorkload runs the suite's gcloud as a process of container, a
// workload's container, with the metadata coordinate the workload was given
// and nothing else to authenticate with, and returns its standard output.
func gcloudInWorkload(t *testing.T, container string, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", container).Output()
	require.NoError(t, err)
	var metadataHost string
	for _, entry := range strings.Split(string(out), "\n") {
		if value, ok := strings.CutPrefix(entry, "GCE_METADATA_HOST="); ok {
			metadataHost = value
		}
	}
	require.NotEmpty(t, metadataHost, "the workload was given no metadata server")
	cmd := exec.Command("docker", append([]string{"run", "--rm",
		"--network", "container:" + container,
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--volume", gcloudSDKRoot(t) + ":/google-cloud-sdk:ro",
		"--volume", t.TempDir() + ":/gcloud-config",
		"--env", "HOME=/tmp",
		"--env", "CLOUDSDK_CONFIG=/gcloud-config",
		"--env", "CLOUDSDK_PYTHON=python3",
		"--env", "CLOUDSDK_CORE_DISABLE_PROMPTS=1",
		"--env", "CLOUDSDK_COMPONENT_MANAGER_DISABLE_UPDATE_CHECK=1",
		"--env", "GCE_METADATA_HOST=" + metadataHost,
		"--env", "GCE_METADATA_IP=" + metadataHost,
		"--env", "GCE_METADATA_ROOT=" + metadataHost,
		gcloudPythonImage, "/google-cloud-sdk/bin/gcloud"}, args...)...)
	return runCLI(t, cmd)
}

// TestGCEMetadataServerGcloudCredentials drives the on-workload gcloud
// credential path end to end. gcloud in a Cloud Run task discovers the task's
// identity by walking the metadata server's directory listings, reads it
// recursively and mints access tokens from it, and a native command
// authenticates with one.
func TestGCEMetadataServerGcloudCredentials(t *testing.T) {
	t.Parallel()
	const accountID = "metadata-adc-sa"
	email := accountID + "@" + project + ".iam.gserviceaccount.com"
	runCLI(t, gcloudCLI("iam", "service-accounts", "create", accountID,
		"--display-name=Metadata ADC SA", "--format=json"))
	grantProjectRole(t, "serviceAccount:"+email, "roles/iam.serviceAccountViewer")

	const job = "cli-metadata-gcloud"
	runCLI(t, gcloudRegionalRunCLI("run", "jobs", "create", job,
		"--region="+location,
		"--image="+commandImageName,
		"--args=hold",
		"--service-account="+email,
		"--task-timeout=600s",
		"--max-retries=0",
		"--quiet",
	))
	t.Cleanup(func() {
		runCLI(t, gcloudRegionalRunCLI("run", "jobs", "delete", job, "--region="+location, "--quiet"))
	})
	execution := runJob(t, job).Execution
	t.Cleanup(func() { httpDoJSON(t, "POST", fmt.Sprintf("%s/v2/%s:cancel", baseURL, execution), `{}`) })
	container := jobContainerName(execution)
	// Cloud Run reports no readiness for a task, so the test waits on its
	// container.
	require.Eventually(t, func() bool {
		out, err := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", container).Output()
		return err == nil && strings.TrimSpace(string(out)) == "true"
	}, 2*time.Minute, 250*time.Millisecond, "the task's container runs")

	// gcloud reports the identity it read from the metadata server as its
	// active account: the account the job runs as.
	accounts := gcloudInWorkload(t, container, "auth", "list", "--format=value(account)")
	assert.Equal(t, email, strings.TrimSpace(accounts))

	// A native command authenticated by the token gcloud minted there.
	token := strings.TrimSpace(gcloudInWorkload(t, container, "auth", "print-access-token"))
	require.NotEmpty(t, token)
	list := gcloudCLI("iam", "service-accounts", "list", "--format=value(email)")
	list.Env = append(list.Env, "CLOUDSDK_AUTH_ACCESS_TOKEN="+token)
	assert.Contains(t, runCLI(t, list), email)

	// Application Default Credentials resolve through the same path, and the
	// token they yield is accepted by the data plane.
	adcToken := strings.TrimSpace(gcloudInWorkload(t, container, "auth", "application-default", "print-access-token"))
	require.NotEmpty(t, adcToken, "ADC must resolve to a token through the metadata server")
	assertTokenAuthenticates(t, adcToken)
}

// assertTokenAuthenticates proves a token is one the simulator's data plane
// accepts, by presenting it on a real API read the way any client would.
func assertTokenAuthenticates(t *testing.T, token string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/projects/"+project+"/serviceAccounts", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"the data plane must accept the token: %s", string(body))
}

// TestGCloudRegionPrefixedEndpointHost proves the simulator answers the host
// form gcloud builds for a regional service. Given an endpoint override,
// gcloud prefixes the region onto the host — `us-central1-<host>` — exactly as
// it does for the real `us-central1-run.googleapis.com`. The coordinate that
// carries that request to the simulator is gcloud's own HTTP proxy setting, so
// the CLI, the URL it builds and the credential it presents are all unmodified;
// only where the bytes are delivered differs.
func TestGCloudRegionPrefixedEndpointHost(t *testing.T) {
	t.Parallel()
	const serviceName = "region-prefixed-host-svc"
	httpDoJSON(t, http.MethodPost,
		fmt.Sprintf("%s/apis/serving.knative.dev/v1/namespaces/%s/services", baseURL, project),
		fmt.Sprintf(`{
			"apiVersion": "serving.knative.dev/v1",
			"kind": "Service",
			"metadata": {"name": %q, "namespace": %q},
			"spec": {"template": {"spec": {"containers": [{"image": "gcr.io/test-project/hello"}]}}}
		}`, serviceName, project))

	listed := runCLI(t, gcloudRegionalRunCLI("run", "services", "list",
		"--region="+location, "--format=value(metadata.name)"))

	assert.Contains(t, listed, serviceName,
		"the simulator must answer the %s-prefixed host gcloud builds for a regional service, got %q",
		location, listed)
}
