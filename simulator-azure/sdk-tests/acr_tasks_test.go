package azure_sdk_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/e6qu/sockerless-cloud/testutil/registrytrust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestACRTasks_ScheduleRunDockerBuild exercises the ACR Tasks quick-build
// slice exactly as backends/aca does for the reverse-agent bootstrap
// overlay: upload a build context to blob storage, then
// RegistriesClient.BeginScheduleRun with a DockerBuildRequest pointing at
// that blob. The simulator fetches the context, runs `docker build` on the
// host engine, and — faithful to real ACR Tasks with IsPushEnabled —
// `docker push`es the result to the registry and removes the local copy.
// We point the image at a throwaway registry the engine can reach (a
// stand-in for the ACR `/v2/` endpoint a workload would pull from) and
// assert the image landed there and is gone from the local daemon.
func TestACRTasks_ScheduleRunDockerBuild(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for ACR Tasks build test (no fallback): %v", err)
	}

	const (
		rg      = "acr-tasks-rg"
		account = "acrbuildacct"
		regName = "acrbuildreg"
		ctr     = "build-context"
		regPort = "5099"
	)
	// A real registry the build host can push to / the test can read.
	startThrowawayRegistry(t, regPort)
	cleanupTrust, err := registrytrust.ConfigureLoopbackHTTPRegistry(ctx, "127.0.0.1:"+regPort)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanupTrust()) })
	// Pre-pull the build's base image so the sim's `docker build` uses the
	// local cache instead of racing a fresh (throttle-prone) public-mirror
	// pull mid-build.
	pullImageWithRetry(t, "public.ecr.aws/docker/library/alpine:3.20")
	imageName := fmt.Sprintf("127.0.0.1:%s/sockerless-overlay/aca:test-%d", regPort, time.Now().UnixNano())

	// 1. Upload a build context (Dockerfile + a file COPY'd in, mirroring
	// the bootstrap overlay shape) to the sim's blob storage.
	blobClient, err := azblob.NewClientWithNoCredential(storageSDKURL(t, account, "blob"),
		&azblob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, _ = blobClient.CreateContainer(ctx, ctr, nil)

	context := makeACRBuildContext(t, map[string]string{
		"Dockerfile": "FROM public.ecr.aws/docker/library/alpine:3.20\n" +
			"COPY payload /opt/sockerless/payload\n" +
			"RUN chmod +x /opt/sockerless/payload\n" +
			"ENTRYPOINT [\"/opt/sockerless/payload\"]\n",
		"payload": "#!/bin/sh\necho overlay-ok\n",
	})
	blobName := fmt.Sprintf("build-context/%d.tar.gz", time.Now().UnixNano())
	_, err = blobClient.UploadBuffer(ctx, ctr, blobName, context, nil)
	require.NoError(t, err)
	sourceLocation := fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s", account, ctr, blobName)

	// 2. BeginScheduleRun with the DockerBuildRequest the backend builds.
	regClient, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := regClient.BeginScheduleRun(ctx, rg, regName, &armcontainerregistry.DockerBuildRequest{
		Type:           to.Ptr("DockerBuildRequest"),
		DockerFilePath: to.Ptr("Dockerfile"),
		ImageNames:     []*string{to.Ptr(imageName)},
		SourceLocation: to.Ptr(sourceLocation),
		IsPushEnabled:  to.Ptr(true),
		Platform: &armcontainerregistry.PlatformProperties{
			OS: to.Ptr(armcontainerregistry.OSLinux),
		},
	}, nil)
	require.NoError(t, err)

	result, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err, "ACR Task run should succeed")
	require.NotNil(t, result.Properties)
	require.NotNil(t, result.Properties.Status)
	assert.Equal(t, armcontainerregistry.RunStatusSucceeded, *result.Properties.Status)
	require.NotNil(t, result.Properties.RunID)
	assert.NotEmpty(t, *result.Properties.RunID)

	// 3. Faithful build→push: the image must live in the registry (pullable
	// via /v2/), NOT on the build host's local daemon.
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", imageName).Run() })
	tagOnly := imageName[strings.LastIndex(imageName, ":")+1:]
	manifestURL := fmt.Sprintf("http://127.0.0.1:%s/v2/sockerless-overlay/aca/manifests/%s", regPort, tagOnly)
	mreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	mreq.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")
	mresp, err := http.DefaultClient.Do(mreq)
	require.NoError(t, err)
	defer mresp.Body.Close()
	require.Equal(t, http.StatusOK, mresp.StatusCode, "built image must be present in the registry (/v2/ manifest)")
	assert.Error(t, exec.Command("docker", "image", "inspect", imageName).Run(),
		"built overlay image %s must NOT remain on the local daemon after push", imageName)

	// 4. GetRun round-trips the run record.
	runsClient, err := armcontainerregistry.NewRunsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	got, err := runsClient.Get(ctx, rg, regName, *result.Properties.RunID, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	assert.Equal(t, armcontainerregistry.RunStatusSucceeded, *got.Properties.Status)

	// 5. The run's build log is retrievable through the advertised log link,
	// as a plain GET of the SAS-style URL — real ACR serves the full docker
	// build/push output there.
	sas, err := runsClient.GetLogSasURL(ctx, rg, regName, *result.Properties.RunID, nil)
	require.NoError(t, err)
	require.NotNil(t, sas.LogLink)
	assert.Contains(t, *sas.LogLink, "/acr/v1/logs/",
		"the log link must point at the sim's run-log endpoint")
	logResp, err := http.Get(*sas.LogLink)
	require.NoError(t, err)
	logBytes, _ := io.ReadAll(logResp.Body)
	logResp.Body.Close()
	require.Equal(t, http.StatusOK, logResp.StatusCode, "advertised logLink must resolve: %s", *sas.LogLink)
	assert.Contains(t, string(logBytes), "The push refers to repository",
		"the run log must carry the docker build/push output")
}

// pullImageWithRetry pulls an image with bounded exponential backoff so a
// transient public-mirror throttle (toomanyrequests / network blip) doesn't
// flake docker-dependent setup — the same strict rate-limit posture the sim
// pull paths take. Fails the test only after exhausting retries.
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
		out, err := exec.Command("docker", "pull", image).CombinedOutput()
		if err == nil {
			return
		}
		lastErr = fmt.Errorf("%v: %s", err, out)
	}
	t.Fatalf("pull %s after retries: %v", image, lastErr)
}

// startThrowawayRegistry runs a real registry:2 on 127.0.0.1:<port> for the
// duration of the test — a reachable, auto-insecure stand-in for the ACR
// `/v2/` endpoint the sim's ACR Tasks pushes to.
func startThrowawayRegistry(t *testing.T, port string) {
	t.Helper()
	const regImage = "public.ecr.aws/docker/library/registry:2"
	// Pull the registry image up front with retries so `docker run` doesn't
	// inline-pull (and exit 125) on a transient public-mirror throttle.
	pullImageWithRetry(t, regImage)
	name := "acr-tasks-sdktest-reg-" + port
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		_ = exec.Command("docker", "rm", "-f", name).Run()
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
			"-p", port+":5000", regImage).CombinedOutput()
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = fmt.Errorf("%v: %s", err, out)
	}
	require.NoError(t, lastErr, "start throwaway registry")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
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

// TestACRTasks_ScheduleRunMissingContextFails asserts the build fails
// loudly when the source context blob doesn't exist, rather than silently
// producing no image. ACR reports a build outcome through the Run's
// `status` (the run resource is returned successfully; it's the *run* that
// failed), which is exactly what backends/aca's ACRBuildService checks to
// surface the error.
func TestACRTasks_ScheduleRunMissingContextFails(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for ACR Tasks build test (no fallback): %v", err)
	}
	regClient, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := regClient.BeginScheduleRun(ctx, "acr-tasks-rg", "acrbuildreg", &armcontainerregistry.DockerBuildRequest{
		Type:           to.Ptr("DockerBuildRequest"),
		DockerFilePath: to.Ptr("Dockerfile"),
		ImageNames:     []*string{to.Ptr("acrbuildreg.azurecr.io/sockerless-overlay/aca:missing")},
		SourceLocation: to.Ptr("https://acrbuildacct.blob.core.windows.net/build-context/does-not-exist.tar.gz"),
		IsPushEnabled:  to.Ptr(true),
	}, nil)
	require.NoError(t, err)

	result, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Properties)
	require.NotNil(t, result.Properties.Status)
	assert.Equal(t, armcontainerregistry.RunStatusFailed, *result.Properties.Status,
		"missing build context must report the run as Failed")
}

func makeACRBuildContext(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}
