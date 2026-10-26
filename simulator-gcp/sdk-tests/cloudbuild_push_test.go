package gcp_sdk_test

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"github.com/e6qu/sockerless-cloud/testutil/baseimage"

	"github.com/e6qu/sockerless-cloud/testutil/registrytrust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudBuild_FaithfulBuildPush runs a `docker build -t <ref>` step and a
// `docker push <ref>` step. Like real Cloud Build, the simulator pushes the image
// and drops the local copy, so the image must be in the registry and gone from
// the daemon. The ref names a throwaway registry the build host reaches.
func TestCloudBuild_FaithfulBuildPush(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for Cloud Build push test (no fallback): %v", err)
	}

	const regPort = "5098"
	startThrowawayRegistry(t, regPort)
	cleanupTrust, err := registrytrust.ConfigureLoopbackHTTPRegistry(context.Background(), "127.0.0.1:"+regPort)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanupTrust()) })
	// Pre-pull the base image so the build does not race a throttled mirror pull.
	pullImageWithRetry(t, "mirror.gcr.io/library/alpine:3.20")

	imageName := fmt.Sprintf("127.0.0.1:%s/sockerless-overlay/cloudrun:test-%d", regPort, time.Now().UnixNano())

	project := "cb-push-project"
	bucket := "cb-push-bucket"
	createBucket(t, project, bucket)
	objectName := fmt.Sprintf("cb-push-%d.tar.gz", time.Now().UnixNano())
	tarball := makeTarGz(t, map[string]string{
		"Dockerfile": "FROM mirror.gcr.io/library/alpine:3.20\nRUN echo cloudbuild-ok > /opt/payload\n",
	})
	uploadGCSObject(t, bucket, objectName, tarball)

	buildURL := fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project)
	body := fmt.Sprintf(`{
		"source":{"storageSource":{"bucket":%q,"object":%q}},
		"steps":[
			{"name":"gcr.io/cloud-builders/docker","args":["build","-t",%q,"."]},
			{"name":"gcr.io/cloud-builders/docker","args":["push",%q]}
		],
		"images":[%q],
		"timeout":"120s"
	}`, bucket, objectName, imageName, imageName, imageName)
	// The build's own timeout bounds it: a wedged runtime or stalled registry ends
	// the build TIMEOUT.
	built, err := waitBuild(t, readStartedBuild(t, httpPOST(t, buildURL, body)))
	require.NoError(t, err, "build+push should succeed")
	require.Equal(t, cloudbuildpb.Build_SUCCESS, built.GetStatus())

	t.Cleanup(func() { _, _ = dockerCLIWithTimeout(60*time.Second, "image", "rm", "-f", imageName) })
	tagOnly := imageName[strings.LastIndex(imageName, ":")+1:]
	manifestURL := fmt.Sprintf("http://127.0.0.1:%s/v2/sockerless-overlay/cloudrun/manifests/%s", regPort, tagOnly)
	mreq, _ := http.NewRequest(http.MethodGet, manifestURL, nil)
	// A BuildKit build pushes an OCI image index (the image and its provenance
	// attestation).
	mreq.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, "+
		"application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")
	mresp, err := http.DefaultClient.Do(mreq)
	require.NoError(t, err)
	defer mresp.Body.Close()
	require.Equal(t, http.StatusOK, mresp.StatusCode, "built image must be present in the registry (/v2/ manifest)")
	_, inspectErr := dockerCLIWithTimeout(60*time.Second, "image", "inspect", imageName)
	assert.Error(t, inspectErr,
		"built overlay image %s must NOT remain on the local daemon after push", imageName)
}

// pullImageWithRetry retries with bounded exponential backoff so a public
// mirror's throttle does not fail setup.
func pullImageWithRetry(t *testing.T, image string) {
	t.Helper()
	if err := baseimage.Ensure(image); err != nil {
		t.Fatalf("%v", err)
	}
}

// startThrowawayRegistry runs registry:2 on 127.0.0.1:<port> for the test.
func startThrowawayRegistry(t *testing.T, port string) {
	t.Helper()
	const regImage = "mirror.gcr.io/library/registry:2"
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

// dockerCLIWithTimeout bounds each `docker` call: a wedged runtime (Podman on
// macOS in its gvproxy 500 state, a stalled daemon) otherwise hangs until the
// `go test -timeout`.
func dockerCLIWithTimeout(deadline time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}
