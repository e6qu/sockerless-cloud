package gcp_cli_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `gcloud run services replace` and `gcloud run jobs executions cancel` — two
// real CLI invocations against the Cloud Run Admin v1 (Knative) surface.
//
// Both resolve the regional Cloud Run host (`{region}-run.googleapis.com`),
// which gcloud builds by prefixing the region onto the configured endpoint. The
// coordinate that carries those bytes to a loopback simulator is gcloud's own
// HTTP proxy setting, so the CLI, the URL it builds, the body it sends and the
// credential it presents are all unmodified — only where the bytes are
// delivered differs. TestGCloudRegionPrefixedEndpointHost pins that coordinate.

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

// TestCloudRunV1_CLI_ServicesReplaceSendsResourceVersion drives the real
// `gcloud run services replace`. gcloud reads the service, copies the
// resourceVersion it read into the replace body, and PUTs it — so the command
// exercises the enforced path with the version the service is actually at, and
// the replace must succeed and move the version on. The other half of the
// contract — the version a concurrent client is still holding — is the same PUT
// with the version from before that replace.
func TestCloudRunV1_CLI_ServicesReplaceSendsResourceVersion(t *testing.T) {
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

	// The version the CLI read before that replace is now stale, and a client
	// still holding it is refused rather than overwriting the service.
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

// TestCloudRunV1_CLI_ExecutionsCancel drives the published verb on the
// executions collection through the real CLI. The simulator serves the
// collection's custom methods from one handler per API version, so the CLI's
// own call is the proof that tightening those handlers to the verbs Cloud Run
// publishes left the published one working.
func TestCloudRunV1_CLI_ExecutionsCancel(t *testing.T) {
	const jobID = "cli-verb-job"
	httpDoJSON(t, "POST", jobsBaseURL()+"?jobId="+jobID, `{
		"template": {"template": {"containers": [
			{"image": "alpine:latest", "command": ["sleep"], "args": ["30"]}
		]}}
	}`)
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", jobURL(jobID), "")
		if err == nil {
			resp.Body.Close()
		}
	})

	out := httpDoJSON(t, "POST", jobURL(jobID+":run"), "")
	var lro struct {
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	parseJSON(t, out, &lro)
	require.NotEmpty(t, lro.Response.Name)
	execID := lro.Response.Name[strings.LastIndex(lro.Response.Name, "/")+1:]

	runCLI(t, gcloudRegionalRunCLI("run", "jobs", "executions", "cancel", execID,
		"--region="+location, "--format=value(metadata.name)"))

	require.Eventually(t, func() bool {
		var execution struct {
			CompletionTime string `json:"completionTime"`
		}
		parseJSON(t, httpDoJSON(t, "GET", baseURL+"/v2/"+lro.Response.Name, ""), &execution)
		return execution.CompletionTime != ""
	}, 60*time.Second, 250*time.Millisecond, "the cancel the CLI sent must settle the execution")

	// A verb the service does not publish is refused on the same collection
	// rather than silently cancelling: no CLI spells an unpublished method, so
	// the request a client of one would send is put on the wire directly.
	resp, err := httpDo("POST", fmt.Sprintf(
		"%s/apis/run.googleapis.com/v1/namespaces/%s/executions/%s:teleport", baseURL, project, execID), "{}")
	require.NoError(t, err)
	v1Body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode, "body: %s", v1Body)
	assert.Contains(t, string(v1Body), "unknown action")

	resp, err = httpDo("POST", baseURL+"/v2/"+lro.Response.Name+":teleport", "{}")
	require.NoError(t, err)
	v2Body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode, "body: %s", v2Body)
	assert.Contains(t, string(v2Body), "unknown action")
}
