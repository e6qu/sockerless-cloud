package azure_sdk_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/baseimage"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
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
	acrEnsureRegistry(t, rg, regName)
	// A real registry the build host can push to / the test can read, served
	// over TLS the way every Azure Container Registry login server is, so the
	// push exercises the certificate-verifying client path a real registry
	// gets.
	coordinate := "127.0.0.1:" + regPort
	authority := startThrowawayRegistry(t, regPort)

	// Negative control for the trust installed below. The pull always fails —
	// that tag does not exist — so what matters is WHY. A certificate refusal
	// means the engine does not trust this registry yet, which makes the
	// installation falsifiable: it must convert that refusal into the
	// registry's own "manifest unknown", asserted immediately after it.
	//
	// Engines genuinely differ here. dockerd treats loopback registries as
	// insecure by default, so it reaches this coordinate with no trust
	// configuration at all and answers "manifest unknown" straight away. Where
	// that happens the installation cannot be falsified at this coordinate, and
	// this test says so rather than asserting another engine's wording;
	// proving the mechanism itself belongs to testutil/registrytrust.
	probe, err := registrytrust.PullFromRegistryUnderTest(ctx, coordinate+"/sockerless-overlay/aca:absent")
	require.Error(t, err, "the absent tag must not resolve before anything is pushed: %s", probe)
	trustIsFalsifiable := strings.Contains(string(probe), "certificate signed by unknown authority")
	if !trustIsFalsifiable {
		require.Contains(t, string(probe), "manifest unknown",
			"the engine neither refused the certificate nor reached the registry: %s", probe)
		t.Logf("engine already trusts %s unconfigured, so the authority installation is not falsifiable here: %s",
			coordinate, strings.TrimSpace(string(probe)))
	}

	cleanupTrust, err := registrytrust.ConfigureTrustedRegistryCA(ctx, coordinate, authority)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanupTrust()) })

	if trustIsFalsifiable {
		// The same pull must now be refused by the registry rather than by the
		// certificate, which is what proves the authority installation worked.
		trusted, err := registrytrust.PullFromRegistryUnderTest(ctx, coordinate+"/sockerless-overlay/aca:absent")
		require.Error(t, err, "the absent tag must still not resolve: %s", trusted)
		assert.NotContains(t, string(trusted), "certificate signed by unknown authority",
			"installing the authority must stop the engine refusing the registry's certificate")
		assert.Contains(t, string(trusted), "manifest unknown",
			"after trust is installed the refusal must come from the registry, not the certificate")
	}
	// Pre-pull the build's base image so the sim's `docker build` uses the
	// local cache instead of racing a fresh (throttle-prone) public-mirror
	// pull mid-build.
	pullImageWithRetry(t, "public.ecr.aws/docker/library/alpine:3.20")
	imageName := fmt.Sprintf("%s/sockerless-overlay/aca:test-%d", coordinate, time.Now().UnixNano())

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

	// The operation completes once the run is queued; the build runs after.
	queued, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, queued.Properties)
	require.NotNil(t, queued.Properties.RunID)
	assert.Equal(t, armcontainerregistry.RunStatusQueued, ptrVal(queued.Properties.Status))

	runsClient, err := armcontainerregistry.NewRunsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	result := awaitACRRun(t, runsClient, rg, regName, *queued.Properties.RunID)
	require.Equal(t, armcontainerregistry.RunStatusSucceeded, ptrVal(result.Properties.Status),
		"ACR Task run should succeed: %s", ptrVal(result.Properties.RunErrorMessage))
	assert.NotNil(t, result.Properties.StartTime)
	assert.NotNil(t, result.Properties.FinishTime)

	// 3. Faithful build→push: the image must live in the registry (pullable
	// via /v2/), NOT on the build host's local daemon.
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", imageName).Run() })
	tagOnly := imageName[strings.LastIndex(imageName, ":")+1:]
	manifestURL := fmt.Sprintf("https://%s/v2/sockerless-overlay/aca/manifests/%s", coordinate, tagOnly)
	mreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	mreq.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")
	mresp, err := registryClient(t, authority).Do(mreq)
	require.NoError(t, err)
	defer mresp.Body.Close()
	require.Equal(t, http.StatusOK, mresp.StatusCode, "built image must be present in the registry (/v2/ manifest)")
	assert.Error(t, exec.Command("docker", "image", "inspect", imageName).Run(),
		"built overlay image %s must NOT remain on the local daemon after push", imageName)

	// 4. The run's build log is a blob at the advertised link, read the way
	// `az acr build` streams it: the Blob service's get-properties, whose
	// `Complete` metadata says the run ended and how, then the content.
	sas, err := runsClient.GetLogSasURL(ctx, rg, regName, *result.Properties.RunID, nil)
	require.NoError(t, err)
	require.NotNil(t, sas.LogLink)
	logBlob, err := blob.NewClientWithNoCredential(*sas.LogLink, nil)
	require.NoError(t, err)
	props, err := logBlob.GetProperties(ctx, nil)
	require.NoError(t, err, "advertised logLink must resolve: %s", *sas.LogLink)
	assert.Equal(t, "Succeeded", acrLogCompletion(props.Metadata))
	download, err := logBlob.DownloadStream(ctx, nil)
	require.NoError(t, err)
	logBytes, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Contains(t, string(logBytes), "The push refers to repository",
		"the run log must carry the docker build/push output")

	// A link to the logs of a run that never happened leads nowhere — the
	// endpoint it points at answers 404 — so the action refuses rather than
	// handing out a URL that reports a log is there.
	_, err = runsClient.GetLogSasURL(ctx, rg, regName, "ca-does-not-exist", nil)
	require.Error(t, err, "a run nobody scheduled has no log link")
}

// pullImageWithRetry pulls an image with bounded exponential backoff so a
// transient public-mirror throttle (toomanyrequests / network blip) doesn't
// flake docker-dependent setup — the same strict rate-limit posture the sim
// pull paths take. Fails the test only after exhausting retries.
func pullImageWithRetry(t *testing.T, image string) {
	t.Helper()
	if err := baseimage.Ensure(image); err != nil {
		t.Fatalf("%v", err)
	}
}

// startThrowawayRegistry runs a real registry:2 on 127.0.0.1:<port> for the
// duration of the test, serving HTTPS from a certificate this test issues — a
// reachable stand-in for the ACR `/v2/` login server the sim's ACR Tasks pushes
// to, which is HTTPS in every region. It returns the certificate authority the
// engine and the test have to trust to reach it.
//
// The certificate and key are staged into the container with `docker cp`
// rather than a bind mount, because the engine resolves a mount source on the
// host it runs on, which is not the filesystem this test process sees when it
// runs inside the Linux harness container.
func startThrowawayRegistry(t *testing.T, port string) []byte {
	t.Helper()
	const regImage = "public.ecr.aws/docker/library/registry:2"
	// Pull the registry image up front with retries so `docker run` doesn't
	// inline-pull (and exit 125) on a transient public-mirror throttle.
	pullImageWithRetry(t, regImage)

	authority, serving := issueRegistryCertificate(t)
	name := "acr-tasks-sdktest-reg-" + port
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		_ = exec.Command("docker", "rm", "-f", name).Run()
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		out, err := exec.Command("docker", "create", "--name", name,
			"-p", port+":5000",
			"-e", "REGISTRY_HTTP_TLS_CERTIFICATE=/certs/registry.crt",
			"-e", "REGISTRY_HTTP_TLS_KEY=/certs/registry.key",
			regImage).CombinedOutput()
		if err != nil {
			lastErr = fmt.Errorf("create: %v: %s", err, out)
			continue
		}
		if out, err := exec.Command("docker", "cp", serving, name+":/certs").CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("stage certificate: %v: %s", err, out)
			continue
		}
		if out, err := exec.Command("docker", "start", name).CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("start: %v: %s", err, out)
			continue
		}
		lastErr = nil
		break
	}
	require.NoError(t, lastErr, "start throwaway registry")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	client := registryClient(t, authority)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, derr := client.Get(fmt.Sprintf("https://127.0.0.1:%s/v2/", port))
		if derr == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return authority
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("throwaway registry on :%s did not become ready", port)
	return nil
}

// issueRegistryCertificate mints a certificate authority and a serving
// certificate for the loopback registry, and returns the authority's PEM plus
// a directory holding registry.crt and registry.key for the registry to serve.
func issueRegistryCertificate(t *testing.T) ([]byte, string) {
	t.Helper()
	authorityKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	authorityTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "sockerless acr-tasks test authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	authorityDER, err := x509.CreateCertificate(rand.Reader, authorityTemplate, authorityTemplate, &authorityKey.PublicKey, authorityKey)
	require.NoError(t, err)
	authority, err := x509.ParseCertificate(authorityDER)
	require.NoError(t, err)

	servingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	servingTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	servingDER, err := x509.CreateCertificate(rand.Reader, servingTemplate, authority, &servingKey.PublicKey, authorityKey)
	require.NoError(t, err)

	directory := t.TempDir()
	certFile, err := os.Create(filepath.Join(directory, "registry.crt"))
	require.NoError(t, err)
	require.NoError(t, pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: servingDER}))
	require.NoError(t, certFile.Close())
	keyFile, err := os.OpenFile(filepath.Join(directory, "registry.key"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(keyFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(servingKey)}))
	require.NoError(t, keyFile.Close())

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: authorityDER}), directory
}

// registryClient returns an HTTP client that verifies the throwaway registry's
// certificate against the authority that issued it — the same verification the
// container engine performs, rather than a skipped one.
func registryClient(t *testing.T, authority []byte) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(authority), "certificate authority must parse")
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 60 * time.Second}
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
	acrEnsureRegistry(t, "acr-tasks-rg", "acrbuildreg")
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

	queued, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, queued.Properties)
	require.NotNil(t, queued.Properties.RunID)
	assert.Equal(t, armcontainerregistry.RunStatusQueued, ptrVal(queued.Properties.Status))

	runsClient, err := armcontainerregistry.NewRunsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	result := awaitACRRun(t, runsClient, "acr-tasks-rg", "acrbuildreg", *queued.Properties.RunID)
	assert.Equal(t, armcontainerregistry.RunStatusFailed, ptrVal(result.Properties.Status),
		"missing build context must report the run as Failed")
	assert.Contains(t, ptrVal(result.Properties.RunErrorMessage), "not found",
		"the run says why it failed")
}

// TestACRTasks_CancelStopsARunningBuild schedules a build that would run for
// ten minutes, waits until it is Running, and cancels it: Runs_Cancel answers
// once the build has stopped, the run reads Canceled, and no image was built.
func TestACRTasks_CancelStopsARunningBuild(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for ACR Tasks build test (no fallback): %v", err)
	}
	const (
		rg      = "acr-tasks-rg"
		account = "acrcancelacct"
		regName = "acrcancelreg"
		ctr     = "build-context"
	)
	acrEnsureRegistry(t, rg, regName)
	createStorageAccount(t, rg, account)
	pullImageWithRetry(t, "public.ecr.aws/docker/library/alpine:3.20")

	blobClient, err := azblob.NewClientWithNoCredential(storageSDKURL(t, account, "blob"),
		&azblob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, _ = blobClient.CreateContainer(ctx, ctr, nil)
	blobName := fmt.Sprintf("build-context/%d.tar.gz", time.Now().UnixNano())
	_, err = blobClient.UploadBuffer(ctx, ctr, blobName, makeACRBuildContext(t, map[string]string{
		"Dockerfile": "FROM public.ecr.aws/docker/library/alpine:3.20\nRUN echo build-started && sleep 600\n",
	}), nil)
	require.NoError(t, err)
	imageName := fmt.Sprintf("%s.azurecr.io/canceled:%d", regName, time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", imageName).Run() })

	regClient, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := regClient.BeginScheduleRun(ctx, rg, regName, &armcontainerregistry.DockerBuildRequest{
		Type:           to.Ptr("DockerBuildRequest"),
		DockerFilePath: to.Ptr("Dockerfile"),
		ImageNames:     []*string{to.Ptr(imageName)},
		SourceLocation: to.Ptr(fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s", account, ctr, blobName)),
		IsPushEnabled:  to.Ptr(false),
		NoCache:        to.Ptr(true),
		Platform:       &armcontainerregistry.PlatformProperties{OS: to.Ptr(armcontainerregistry.OSLinux)},
	}, nil)
	require.NoError(t, err)
	queued, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	runID := *queued.Properties.RunID

	runsClient, err := armcontainerregistry.NewRunsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	running := awaitACRRunLeaves(t, runsClient, rg, regName, runID, armcontainerregistry.RunStatusQueued)
	require.Equal(t, armcontainerregistry.RunStatusRunning, ptrVal(running.Properties.Status))
	require.NotNil(t, running.Properties.StartTime)
	assert.Nil(t, running.Properties.FinishTime, "a running run has not finished")

	// Follow the log the way `az acr build` streams it until the build's own
	// step has started, so the cancel lands on a docker step in flight.
	sas, err := runsClient.GetLogSasURL(ctx, rg, regName, runID, nil)
	require.NoError(t, err)
	logBlob, err := blob.NewClientWithNoCredential(*sas.LogLink, nil)
	require.NoError(t, err)
	awaitACRRunLog(t, logBlob, "build-started")

	cancel, err := runsClient.BeginCancel(ctx, rg, regName, runID, nil)
	require.NoError(t, err)
	_, err = cancel.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	got, err := runsClient.Get(ctx, rg, regName, runID, nil)
	require.NoError(t, err)
	assert.Equal(t, armcontainerregistry.RunStatusCanceled, ptrVal(got.Properties.Status))
	assert.NotNil(t, got.Properties.FinishTime)
	assert.Error(t, exec.Command("docker", "image", "inspect", imageName).Run(),
		"a canceled build produces no image")
	props, err := logBlob.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "Canceled", acrLogCompletion(props.Metadata))
}

// awaitACRRunLog reads a running run's log blob until it holds want, while the
// blob carries no `Complete` metadata: the cadence `az acr build` streams at.
func awaitACRRunLog(t *testing.T, logBlob *blob.Client, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var content []byte
		props, err := logBlob.GetProperties(ctx, nil)
		if err == nil {
			download, err := logBlob.DownloadStream(ctx, nil)
			require.NoError(t, err)
			content, err = io.ReadAll(download.Body)
			require.NoError(t, err)
			require.NoError(t, download.Body.Close())
			if strings.Contains(string(content), want) {
				return
			}
			require.Empty(t, acrLogCompletion(props.Metadata),
				"the run ended before its log held %q: %s", want, content)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run's log never held %q: %s", want, content)
		}
		time.Sleep(time.Second)
	}
}

// awaitACRRun polls the run's own status until it is terminal: ACR Tasks
// offers no waiter or operation for a run once it is queued.
func awaitACRRun(t *testing.T, runs *armcontainerregistry.RunsClient, rg, registry, runID string) armcontainerregistry.Run {
	t.Helper()
	return awaitACRRunLeaves(t, runs, rg, registry, runID,
		armcontainerregistry.RunStatusQueued, armcontainerregistry.RunStatusStarted, armcontainerregistry.RunStatusRunning)
}

// awaitACRRunLeaves polls the run until its status is none of pending.
func awaitACRRunLeaves(t *testing.T, runs *armcontainerregistry.RunsClient, rg, registry, runID string, pending ...armcontainerregistry.RunStatus) armcontainerregistry.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		got, err := runs.Get(ctx, rg, registry, runID, nil)
		require.NoError(t, err)
		status := ptrVal(got.Properties.Status)
		if !slices.Contains(pending, status) {
			return got.Run
		}
		if time.Now().After(deadline) {
			t.Fatalf("ACR Tasks run %s is still %s", runID, status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// acrLogCompletion reads the `Complete` metadata of a run's log blob, whose
// key casing the Blob service does not fix.
func acrLogCompletion(metadata map[string]*string) string {
	for key, value := range metadata {
		if strings.EqualFold(key, "complete") && value != nil {
			return *value
		}
	}
	return ""
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
