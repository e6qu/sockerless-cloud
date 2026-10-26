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

// TestACRTasks_ScheduleRunDockerBuild uploads a build context to Azure Blob
// Storage and schedules a DockerBuildRequest on it. As ACR Tasks does with
// IsPushEnabled, the simulator builds on the host engine, pushes the result to
// the registry and removes the local copy. The image targets a throwaway
// registry the engine can reach.
func TestACRTasks_ScheduleRunDockerBuild(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for ACR Tasks build test (no fallback): %v", err)
	}

	const (
		rg      = "acr-tasks-rg-2966"
		account = "acrbuildacct"
		regName = "acrbuildreg"
		ctr     = "build-context"
		regPort = "5099"
	)
	acrEnsureRegistry(t, rg, regName)
	// The throwaway registry serves TLS like every Azure Container Registry login
	// server, so the push takes the certificate-verifying path.
	coordinate := "127.0.0.1:" + regPort
	authority := startThrowawayRegistry(t, regPort)

	// Negative control for the trust installed below: the absent tag never
	// resolves, so what matters is why. A certificate refusal must turn into the
	// registry's own "manifest unknown" once trust is installed. dockerd treats
	// loopback registries as insecure and answers "manifest unknown" at once;
	// there the installation cannot be falsified at this coordinate, and
	// testutil/registrytrust proves the mechanism instead.
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
		// The registry, not the certificate, now refuses the pull.
		trusted, err := registrytrust.PullFromRegistryUnderTest(ctx, coordinate+"/sockerless-overlay/aca:absent")
		require.Error(t, err, "the absent tag must still not resolve: %s", trusted)
		assert.NotContains(t, string(trusted), "certificate signed by unknown authority",
			"installing the authority must stop the engine refusing the registry's certificate")
		assert.Contains(t, string(trusted), "manifest unknown",
			"after trust is installed the refusal must come from the registry, not the certificate")
	}
	// Pre-pull the base image so the build does not race a throttle-prone mirror
	// pull.
	pullImageWithRetry(t, "mirror.gcr.io/library/alpine:3.20")
	imageName := fmt.Sprintf("%s/sockerless-overlay/aca:test-%d", coordinate, time.Now().UnixNano())

	// A Dockerfile plus a file it COPYs.
	blobClient, err := azblob.NewClientWithNoCredential(storageSDKURL(t, account, "blob"),
		&azblob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, _ = blobClient.CreateContainer(ctx, ctr, nil)

	context := makeACRBuildContext(t, map[string]string{
		"Dockerfile": "FROM mirror.gcr.io/library/alpine:3.20\n" +
			"COPY payload /opt/sockerless/payload\n" +
			"RUN chmod +x /opt/sockerless/payload\n" +
			"ENTRYPOINT [\"/opt/sockerless/payload\"]\n",
		"payload": "#!/bin/sh\necho overlay-ok\n",
	})
	blobName := fmt.Sprintf("build-context/%d.tar.gz", time.Now().UnixNano())
	_, err = blobClient.UploadBuffer(ctx, ctr, blobName, context, nil)
	require.NoError(t, err)
	sourceLocation := fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s", account, ctr, blobName)

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

	// The image lives in the registry, not on the build host's daemon.
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

	// Read the run's log blob as `az acr build` streams it: get-properties, whose
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

	// A run that never happened has no log link: its endpoint would answer 404.
	_, err = runsClient.GetLogSasURL(ctx, rg, regName, "ca-does-not-exist", nil)
	require.Error(t, err, "a run nobody scheduled has no log link")
}

// pullImageWithRetry pulls with bounded exponential backoff so a transient
// mirror throttle does not flake setup.
func pullImageWithRetry(t *testing.T, image string) {
	t.Helper()
	if err := baseimage.Ensure(image); err != nil {
		t.Fatalf("%v", err)
	}
}

// startThrowawayRegistry runs registry:2 on 127.0.0.1:<port> over HTTPS from a
// certificate this test issues, standing in for an ACR login server, and
// returns the authority the engine and the test must trust. `docker cp` stages
// the certificate because the engine resolves bind-mount sources on its own
// host, not in the harness container this test may run in.
func startThrowawayRegistry(t *testing.T, port string) []byte {
	t.Helper()
	const regImage = "mirror.gcr.io/library/registry:2"
	// Pre-pull so `docker run` does not inline-pull and exit 125 on a throttle.
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

// issueRegistryCertificate mints an authority and a serving certificate, and
// returns the authority's PEM and a directory holding registry.crt and
// registry.key.
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

// registryClient verifies the throwaway registry's certificate as the
// container engine does.
func registryClient(t *testing.T, authority []byte) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(authority), "certificate authority must parse")
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 60 * time.Second}
}

// TestACRTasks_ScheduleRunMissingContextFails: a missing source context fails
// the run. ACR returns the run resource successfully and reports the outcome
// in its status.
func TestACRTasks_ScheduleRunMissingContextFails(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for ACR Tasks build test (no fallback): %v", err)
	}
	const (
		rg      = "acr-tasks-rg-0107"
		regName = "acrmissingctxreg"
	)
	acrEnsureRegistry(t, rg, regName)
	regClient, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := regClient.BeginScheduleRun(ctx, rg, regName, &armcontainerregistry.DockerBuildRequest{
		Type:           to.Ptr("DockerBuildRequest"),
		DockerFilePath: to.Ptr("Dockerfile"),
		ImageNames:     []*string{to.Ptr(regName + ".azurecr.io/sockerless-overlay/aca:missing")},
		SourceLocation: to.Ptr("https://acrmissingctxacct.blob.core.windows.net/build-context/does-not-exist.tar.gz"),
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
	result := awaitACRRun(t, runsClient, rg, regName, *queued.Properties.RunID)
	assert.Equal(t, armcontainerregistry.RunStatusFailed, ptrVal(result.Properties.Status),
		"missing build context must report the run as Failed")
	assert.Contains(t, ptrVal(result.Properties.RunErrorMessage), "not found",
		"the run says why it failed")
}

// TestACRTasks_CancelStopsARunningBuild cancels a ten-minute build once it is
// Running: Runs_Cancel answers once the build stopped, the run reads Canceled,
// and no image was built.
func TestACRTasks_CancelStopsARunningBuild(t *testing.T) {
	t.Parallel()
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
	pullImageWithRetry(t, "mirror.gcr.io/library/alpine:3.20")

	blobClient, err := azblob.NewClientWithNoCredential(storageSDKURL(t, account, "blob"),
		&azblob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, _ = blobClient.CreateContainer(ctx, ctr, nil)
	blobName := fmt.Sprintf("build-context/%d.tar.gz", time.Now().UnixNano())
	_, err = blobClient.UploadBuffer(ctx, ctr, blobName, makeACRBuildContext(t, map[string]string{
		"Dockerfile": "FROM mirror.gcr.io/library/alpine:3.20\nRUN echo build-started && sleep 600\n",
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

	// Follow the log until the build's own step starts, so the cancel lands on a
	// step in flight.
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
// blob carries no `Complete` metadata, at the cadence `az acr build` streams.
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

// awaitACRRun polls the run's status: ACR Tasks offers no waiter or operation
// for a run once it is queued.
func awaitACRRun(t *testing.T, runs *armcontainerregistry.RunsClient, rg, registry, runID string) armcontainerregistry.Run {
	t.Helper()
	return awaitACRRunLeaves(t, runs, rg, registry, runID,
		armcontainerregistry.RunStatusQueued, armcontainerregistry.RunStatusStarted, armcontainerregistry.RunStatusRunning)
}

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

// The Blob service does not fix the casing of the `Complete` metadata key.
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
