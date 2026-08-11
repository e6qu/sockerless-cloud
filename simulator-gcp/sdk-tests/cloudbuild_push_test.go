package gcp_sdk_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/registrytrust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudBuild_FaithfulBuildPush exercises the Cloud Build slice exactly as
// the cloudrun / cloudrun-functions backends do for the reverse-agent overlay:
// a `docker build -t <ref>` step followed by a `docker push <ref>` step. Faithful
// to real Cloud Build, the sim pushes the built image to its registry and drops
// the local copy, so the workload pulls it from the registry over /v2/ — not a
// local-daemon shortcut. We point the ref at a throwaway registry the build host
// can reach (a stand-in for the AR /v2/ a workload would pull from) and assert
// the image landed there and is gone from the local daemon.
func TestCloudBuild_FaithfulBuildPush(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for Cloud Build push test (no fallback): %v", err)
	}

	const regPort = "5098"
	startThrowawayRegistry(t, regPort)
	cleanupTrust, err := registrytrust.ConfigureLoopbackHTTPRegistry(context.Background(), "127.0.0.1:"+regPort)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanupTrust()) })
	// Pre-pull the build's base image so the sim's `docker build` uses the
	// local cache instead of racing a throttle-prone public-mirror pull.
	pullImageWithRetry(t, "public.ecr.aws/docker/library/alpine:3.20")

	imageName := fmt.Sprintf("127.0.0.1:%s/sockerless-overlay/cloudrun:test-%d", regPort, time.Now().UnixNano())

	project := "cb-push-project"
	bucket := "cb-push-bucket"
	createBucket(t, project, bucket)
	objectName := fmt.Sprintf("cb-push-%d.tar.gz", time.Now().UnixNano())
	tarball := makeTarGz(t, map[string]string{
		"Dockerfile": "FROM public.ecr.aws/docker/library/alpine:3.20\nRUN echo cloudbuild-ok > /opt/payload\n",
	})
	uploadGCSObject(t, bucket, objectName, tarball)

	buildURL := fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project)
	body := fmt.Sprintf(`{
		"source":{"storageSource":{"bucket":%q,"object":%q}},
		"steps":[
			{"name":"gcr.io/cloud-builders/docker","args":["build","-t",%q,"."]},
			{"name":"gcr.io/cloud-builders/docker","args":["push",%q]}
		],
		"images":[%q]
	}`, bucket, objectName, imageName, imageName, imageName)
	// Bound the synchronous build+push: on a healthy runtime it completes in
	// <15s, but a wedged container runtime (e.g. Podman-on-macOS in its gvproxy
	// 500 state) or a stalled registry would otherwise hang the push forever
	// and consume the whole suite's -timeout. A 120s budget is generous for a
	// real build yet fails fast with a clear deadline error instead of an 8m
	// hang. The server-side push goroutine outlives this client
	// give-up, but the sim process is torn down at suite end regardless.
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer buildCancel()
	breq, _ := http.NewRequestWithContext(buildCtx, http.MethodPost, buildURL, strings.NewReader(body))
	breq.Header.Set("Content-Type", "application/json")
	bresp, err := http.DefaultClient.Do(breq)
	require.NoError(t, err, "build+push request (deadline 120s; a timeout means the container runtime or registry is unresponsive)")
	defer bresp.Body.Close()
	resp, _ := io.ReadAll(bresp.Body)
	require.Contains(t, string(resp), `"status":"SUCCESS"`, "build+push should succeed: %s", string(resp))

	// Faithful build→push: the image must live in the registry (pullable via
	// /v2/), NOT on the build host's local daemon.
	t.Cleanup(func() { _, _ = dockerCLIWithTimeout(60*time.Second, "image", "rm", "-f", imageName) })
	tagOnly := imageName[strings.LastIndex(imageName, ":")+1:]
	manifestURL := fmt.Sprintf("http://127.0.0.1:%s/v2/sockerless-overlay/cloudrun/manifests/%s", regPort, tagOnly)
	mreq, _ := http.NewRequest(http.MethodGet, manifestURL, nil)
	mreq.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")
	mresp, err := http.DefaultClient.Do(mreq)
	require.NoError(t, err)
	defer mresp.Body.Close()
	require.Equal(t, http.StatusOK, mresp.StatusCode, "built image must be present in the registry (/v2/ manifest)")
	_, inspectErr := dockerCLIWithTimeout(60*time.Second, "image", "inspect", imageName)
	assert.Error(t, inspectErr,
		"built overlay image %s must NOT remain on the local daemon after push", imageName)
}

// pullImageWithRetry pulls an image with bounded exponential backoff so a
// transient public-mirror throttle doesn't flake docker-dependent setup. Each
// attempt carries its own deadline (dockerCLIWithTimeout) so a wedged container
// runtime fails fast instead of hanging the whole suite.
func pullImageWithRetry(t *testing.T, image string) {
	t.Helper()
	var lastErr error
	delay := time.Second
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(delay)
			if delay < 8*time.Second {
				delay *= 2
			}
		}
		out, err := dockerCLIWithTimeout(180*time.Second, "pull", image)
		if err == nil {
			return
		}
		lastErr = fmt.Errorf("%v: %s", err, out)
	}
	t.Fatalf("pull %s after retries: %v", image, lastErr)
}

// startThrowawayRegistry runs a real registry:2 on 127.0.0.1:<port> for the
// duration of the test — a reachable stand-in for the Artifact Registry `/v2/`
// endpoint the simulated Cloud Build pushes to. Every Docker call carries a
// deadline so a wedged runtime fails fast.
func startThrowawayRegistry(t *testing.T, port string) {
	t.Helper()
	const regImage = "public.ecr.aws/docker/library/registry:2"
	pullImageWithRetry(t, regImage)
	name := "gcp-cb-sdktest-reg-" + port
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		_, _ = dockerCLIWithTimeout(60*time.Second, "rm", "-f", "-v", name)
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		out, err := dockerCLIWithTimeout(60*time.Second, "create", "--name", name,
			"-p", port+":5000", regImage)
		if err != nil {
			lastErr = fmt.Errorf("create registry container: %v: %s", err, out)
			continue
		}
		out, err = dockerCLIWithTimeout(60*time.Second, "start", name)
		if err != nil {
			lastErr = fmt.Errorf("start registry container: %v: %s", err, out)
			continue
		}
		lastErr = nil
		break
	}
	require.NoError(t, lastErr, "start throwaway registry")
	t.Cleanup(func() { _, _ = dockerCLIWithTimeout(60*time.Second, "rm", "-f", "-v", name) })
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, derr := http.Get(fmt.Sprintf("http://127.0.0.1:%s/v2/", port))
		if derr == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("throwaway registry on :%s did not become ready", port)
}

// dockerCLIWithTimeout runs `docker <args...>` with a hard deadline. A wedged
// container runtime (Podman-on-macOS gvproxy 500 state, stalled daemon) makes
// an unbounded `docker` call hang forever; bounding every call keeps a broken
// runtime from monopolising the suite — the test fails in <deadline instead of
// at the go-test -timeout.
func dockerCLIWithTimeout(deadline time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}
