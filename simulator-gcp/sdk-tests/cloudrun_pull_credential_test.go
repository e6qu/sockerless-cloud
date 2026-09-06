package gcp_sdk_test

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Cloud Run job whose image lives in Artifact Registry is started by a host
// that pulls the image itself, and Artifact Registry refuses an anonymous
// pull, so the host has to pull as the identity real Cloud Run pulls with —
// the project's Cloud Run service agent. The registry's data plane is the one
// the job's image names, reached at this simulator's own coordinate.
func TestCloudRun_JobPullsItsImageFromArtifactRegistryAsTheServiceAgent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("platform gate: the container engine pulls the image itself, and on a host whose engine runs inside its own virtual machine it has no route to this host's loopback, where the simulator's registry listens. Linux hosts — and the repository's Linux container path, `make docker-test` — share one loopback between engine, simulator and client.")
	}
	const (
		project  = "ar-pull-project"
		location = "us-central1"
	)
	arCreateRepository(t, project, location, "docker-hub")

	// The registry serves the docker-hub remote repository's content from
	// what the host holds under the upstream's own name.
	pullImageWithRetry(t, "public.ecr.aws/docker/library/alpine:3.20")
	_, err := dockerCLIWithTimeout(60*time.Second, "tag", "public.ecr.aws/docker/library/alpine:3.20", "alpine:3.20")
	require.NoError(t, err)

	registry := strings.TrimPrefix(baseURL, "http://")
	image := fmt.Sprintf("%s/%s/docker-hub/library/alpine:3.20", registry, project)
	// The host must fetch, not find: a copy under the registry's name would
	// be a pull that never reached the registry.
	_, _ = dockerCLIWithTimeout(60*time.Second, "image", "rm", "-f", image)
	t.Cleanup(func() { _, _ = dockerCLIWithTimeout(60*time.Second, "image", "rm", "-f", image) })

	// The registry refuses the anonymous pull the host would otherwise make.
	resp, body := arRawDo(t, http.MethodGet, fmt.Sprintf("%s/v2/%s/docker-hub/library/alpine/manifests/3.20", baseURL, project), "", nil, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, body)

	execName := createAndRunJobInProject(t, project, "ar-pull-job", image, []string{"echo", "pulled as the service agent"}, "60s")
	execution := waitExecutionDone(t, execName)
	assert.Equal(t, float64(1), execution["succeededCount"], "execution: %v", execution)
	assert.Equal(t, float64(0), execution["failedCount"], "execution: %v", execution)
}
