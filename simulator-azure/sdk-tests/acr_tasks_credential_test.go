package azure_sdk_test

import (
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An ACR Tasks run pushes its output into the registry it runs in as the run
// itself, holding the registry's push scope the way a real run does: the
// registry refuses an anonymous push and an anonymous read, and the image the
// run pushed is there for a client that authenticates.
func TestACRTasks_RunPushesIntoItsRegistryAsTheRun(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("platform gate: the container engine pushes to the registry's login server itself, and on a host whose engine runs inside its own virtual machine it has no route to this host's loopback, where the simulator's registry listens. Linux hosts — and the repository's Linux container path, `make docker-test` — share one loopback between engine, simulator and client.")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for ACR Tasks push test (no fallback): %v", err)
	}
	const (
		rg      = "acr-tasks-cred-rg"
		account = "acrtaskcredacct"
		regName = "acrtaskcredreg"
		ctr     = "build-context"
	)
	acrEnsureRegistry(t, rg, regName)
	registriesClient, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	registry, err := registriesClient.Get(ctx, rg, regName, nil)
	require.NoError(t, err)
	loginServer := *registry.Properties.LoginServer
	createStorageAccount(t, rg, account)

	pullImageWithRetry(t, "public.ecr.aws/docker/library/alpine:3.20")
	imageName := fmt.Sprintf("%s/sockerless-overlay/aca:run-%d", loginServer, time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", imageName).Run() })

	blobClient, err := azblob.NewClientWithNoCredential(storageSDKURL(t, account, "blob"),
		&azblob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, _ = blobClient.CreateContainer(ctx, ctr, nil)
	blobName := fmt.Sprintf("build-context/%d.tar.gz", time.Now().UnixNano())
	_, err = blobClient.UploadBuffer(ctx, ctr, blobName, makeACRBuildContext(t, map[string]string{
		"Dockerfile": "FROM public.ecr.aws/docker/library/alpine:3.20\nRUN echo pushed-as-the-run > /opt/payload\n",
	}), nil)
	require.NoError(t, err)

	poller, err := registriesClient.BeginScheduleRun(ctx, rg, regName, &armcontainerregistry.DockerBuildRequest{
		Type:           to.Ptr("DockerBuildRequest"),
		DockerFilePath: to.Ptr("Dockerfile"),
		ImageNames:     []*string{to.Ptr(imageName)},
		SourceLocation: to.Ptr(fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s", account, ctr, blobName)),
		IsPushEnabled:  to.Ptr(true),
		Platform:       &armcontainerregistry.PlatformProperties{OS: to.Ptr(armcontainerregistry.OSLinux)},
	}, nil)
	require.NoError(t, err)
	result, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Properties.Status)
	require.Equal(t, armcontainerregistry.RunStatusSucceeded, *result.Properties.Status, "the run pushes into its registry with the run's own credential")

	tag := imageName[strings.LastIndex(imageName, ":")+1:]
	manifestURL := fmt.Sprintf("http://%s/v2/sockerless-overlay/aca/manifests/%s", loginServer, tag)
	anonymous, err := http.Get(manifestURL)
	require.NoError(t, err)
	anonymous.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, anonymous.StatusCode, "the registry still refuses an anonymous read")

	_, err = acrDataPlaneClient(t, loginServer).GetManifest(ctx, "sockerless-overlay/aca", tag, nil)
	require.NoError(t, err, "the image the run pushed is in the registry for an authenticated client")
}
