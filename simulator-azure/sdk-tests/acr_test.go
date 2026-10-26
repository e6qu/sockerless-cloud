package azure_sdk_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/containers/azcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acrDataPlaneClient targets the registry's own login server, so the client
// runs the challenge, token exchange and retry it runs against Azure.
func acrDataPlaneClient(t *testing.T, loginServer string) *azcontainerregistry.Client {
	t.Helper()
	client, err := azcontainerregistry.NewClient("http://"+loginServer, &fakeCredential{}, acrClientOptions())
	require.NoError(t, err)
	return client
}

// sampleManifestJSON is a minimal manifest that needs no layer data.
const sampleManifestJSON = `{
  "schemaVersion": 2,
  "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
  "config": {
    "mediaType": "application/vnd.docker.container.image.v1+json",
    "size": 7023,
    "digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
  },
  "layers": []
}`

func TestACR_ImageManifestPushGetDelete(t *testing.T) {
	t.Parallel()
	const (
		rgName       = "acr-imageops-rg"
		registryName = "imageopsregistry"
		imageName    = "myapp"
		imageTag     = "v1.0"
	)

	ensureRG(t, rgName)

	registriesClient, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	regPoller, err := registriesClient.BeginCreate(ctx, rgName, registryName, armcontainerregistry.Registry{
		Location: ptrStr("eastus"),
		SKU:      &armcontainerregistry.SKU{Name: ptrSKU(armcontainerregistry.SKUNameBasic)},
	}, nil)
	require.NoError(t, err)
	created, err := regPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.LoginServer)

	dp := acrDataPlaneClient(t, *created.Properties.LoginServer)

	// UploadManifest requires an io.ReadSeekCloser.
	uploadResp, err := dp.UploadManifest(ctx, imageName, imageTag,
		azcontainerregistry.ContentTypeApplicationVndDockerDistributionManifestV2JSON,
		nopSeekCloser(strings.NewReader(sampleManifestJSON)), nil)
	require.NoError(t, err)
	require.NotNil(t, uploadResp.DockerContentDigest)
	digest := *uploadResp.DockerContentDigest
	assert.True(t, strings.HasPrefix(digest, "sha256:"), "digest must be sha256: prefixed, got %s", digest)

	getResp, err := dp.GetManifest(ctx, imageName, imageTag, nil)
	require.NoError(t, err)
	getResp.ManifestData.Close()
	require.NotNil(t, getResp.DockerContentDigest)
	assert.Equal(t, digest, *getResp.DockerContentDigest)

	pager := dp.NewListRepositoriesPager(nil)
	var foundRepo bool
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, name := range page.Names {
			if name != nil && *name == imageName {
				foundRepo = true
			}
		}
	}
	assert.True(t, foundRepo, "expected ListRepositories to include %q", imageName)

	tagsPager := dp.NewListTagsPager(imageName, nil)
	var foundTag bool
	for tagsPager.More() {
		page, err := tagsPager.NextPage(ctx)
		require.NoError(t, err)
		for _, tag := range page.Tags {
			if tag != nil && tag.Name != nil && *tag.Name == imageTag {
				foundTag = true
			}
		}
	}
	assert.True(t, foundTag, "expected ListTags to include tag %q", imageTag)

	_, err = dp.DeleteManifest(ctx, imageName, digest, nil)
	require.NoError(t, err)

	_, err = dp.GetManifest(ctx, imageName, imageTag, nil)
	assert.Error(t, err, "GetManifest after DeleteManifest should fail")
}

func TestACR_CacheRuleCRUD(t *testing.T) {
	t.Parallel()
	const (
		rgName       = "acr-cache-rg"
		registryName = "clitestcacheregistry"
		ruleName     = "docker-hub"
	)

	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rgName, armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	// Azure Container Registry validates that the registry exists.
	registriesClient, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	regPoller, err := registriesClient.BeginCreate(ctx, rgName, registryName, armcontainerregistry.Registry{
		Location: ptrStr("eastus"),
		SKU:      &armcontainerregistry.SKU{Name: ptrSKU(armcontainerregistry.SKUNameBasic)},
	}, nil)
	require.NoError(t, err)
	_, err = regPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	rulesClient, err := armcontainerregistry.NewCacheRulesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	createPoller, err := rulesClient.BeginCreate(ctx, rgName, registryName, ruleName, armcontainerregistry.CacheRule{
		Properties: &armcontainerregistry.CacheRuleProperties{
			SourceRepository: ptrStr("docker.io/library/*"),
			TargetRepository: ptrStr("docker-hub/library/*"),
		},
	}, nil)
	require.NoError(t, err)
	createResp, err := createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, ruleName, *createResp.Name)
	require.NotNil(t, createResp.Properties)
	assert.Equal(t, "docker.io/library/*", *createResp.Properties.SourceRepository)
	assert.Equal(t, "docker-hub/library/*", *createResp.Properties.TargetRepository)

	getResp, err := rulesClient.Get(ctx, rgName, registryName, ruleName, nil)
	require.NoError(t, err)
	assert.Equal(t, ruleName, *getResp.Name)
	assert.Equal(t, "docker.io/library/*", *getResp.Properties.SourceRepository)

	pager := rulesClient.NewListPager(rgName, registryName, nil)
	var found bool
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, r := range page.Value {
			if r != nil && r.Name != nil && *r.Name == ruleName {
				found = true
			}
		}
	}
	assert.True(t, found, "expected List to return the created cache rule")

	delPoller, err := rulesClient.BeginDelete(ctx, rgName, registryName, ruleName, nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	_, err = rulesClient.Get(ctx, rgName, registryName, ruleName, nil)
	assert.Error(t, err, "Get after delete should fail")
}

// TestACR_CacheRuleIdempotentUpsert: BeginCreate on an existing rule updates
// it in place.
func TestACR_CacheRuleIdempotentUpsert(t *testing.T) {
	t.Parallel()
	const (
		rgName       = "acr-upsert-rg"
		registryName = "upsertregistry"
		ruleName     = "ghcr-io"
	)

	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rgName, armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	registriesClient, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	regPoller, err := registriesClient.BeginCreate(ctx, rgName, registryName, armcontainerregistry.Registry{
		Location: ptrStr("eastus"),
		SKU:      &armcontainerregistry.SKU{Name: ptrSKU(armcontainerregistry.SKUNameBasic)},
	}, nil)
	require.NoError(t, err)
	_, err = regPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	rulesClient, err := armcontainerregistry.NewCacheRulesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	first, err := rulesClient.BeginCreate(ctx, rgName, registryName, ruleName, armcontainerregistry.CacheRule{
		Properties: &armcontainerregistry.CacheRuleProperties{
			SourceRepository: ptrStr("ghcr.io/owner/app"),
			TargetRepository: ptrStr("ghcr-io/owner/app"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = first.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	second, err := rulesClient.BeginCreate(ctx, rgName, registryName, ruleName, armcontainerregistry.CacheRule{
		Properties: &armcontainerregistry.CacheRuleProperties{
			SourceRepository: ptrStr("ghcr.io/owner/app"),
			TargetRepository: ptrStr("ghcr-io/owner/app-v2"),
		},
	}, nil)
	require.NoError(t, err)
	secondResp, err := second.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "ghcr-io/owner/app-v2", *secondResp.Properties.TargetRepository)
}

// TestACR_LoginServerIsSimulatorCoordinate: the loginServer follows the "acr"
// data-plane host template TestMain configures, not the real-cloud
// `<name>.azurecr.io` host, and reaches this simulator's registry.
func TestACR_LoginServerIsSimulatorCoordinate(t *testing.T) {
	t.Parallel()
	const (
		rgName       = "acr-loginserver-rg"
		registryName = "loginservertestreg"
	)

	ensureRG(t, rgName)

	registriesClient, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := registriesClient.BeginCreate(ctx, rgName, registryName, armcontainerregistry.Registry{
		Location: ptrStr("eastus"),
		SKU:      &armcontainerregistry.SKU{Name: ptrSKU(armcontainerregistry.SKUNameBasic)},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.LoginServer)
	loginServer := *created.Properties.LoginServer

	assert.False(t, strings.HasSuffix(loginServer, ".azurecr.io"),
		"loginServer must not be the unreachable real-cloud host, got %q", loginServer)
	assert.Contains(t, loginServer, registryName+".azurecr.shim.localhost")

	// The registry answers a request at that host with the Bearer challenge
	// naming its own token service.
	req, err := http.NewRequest(http.MethodGet, "http://"+loginServer+"/acr/v1/_catalog", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Www-Authenticate"),
		`service="`+loginServer+`"`, "the challenge must name this registry")
}

func ptrSKU(s armcontainerregistry.SKUName) *armcontainerregistry.SKUName { return &s }

type nopSeekCloserT struct{ io.ReadSeeker }

func (nopSeekCloserT) Close() error                   { return nil }
func nopSeekCloser(r io.ReadSeeker) io.ReadSeekCloser { return nopSeekCloserT{r} }

// TestACR_NameAvailabilityAnswersFromTheRegistriesThatExist: a client calls
// this to avoid a conflict, so answering "available" for every name is
// worthless.
func TestACR_NameAvailabilityAnswersFromTheRegistriesThatExist(t *testing.T) {
	t.Parallel()
	rgName, registryName := "acr-namecheck-rg", uniquename.Alnum("namecheckregistry")
	ensureRG(t, rgName)

	client, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	check := func(name string) armcontainerregistry.RegistryNameStatus {
		t.Helper()
		resp, err := client.CheckNameAvailability(ctx, armcontainerregistry.RegistryNameCheckRequest{
			Name: ptrStr(name),
			Type: ptrStr("Microsoft.ContainerRegistry/registries"),
		}, nil)
		require.NoError(t, err)
		return resp.RegistryNameStatus
	}

	free := check(registryName)
	require.NotNil(t, free.NameAvailable)
	assert.True(t, *free.NameAvailable)

	poller, err := client.BeginCreate(ctx, rgName, registryName, armcontainerregistry.Registry{
		Location: ptrStr("eastus"),
		SKU:      &armcontainerregistry.SKU{Name: ptrSKU(armcontainerregistry.SKUNameBasic)},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	taken := check(registryName)
	require.NotNil(t, taken.NameAvailable)
	assert.False(t, *taken.NameAvailable)
	require.NotNil(t, taken.Reason)
	assert.Equal(t, "AlreadyExists", *taken.Reason)
	require.NotNil(t, taken.Message)
	assert.Contains(t, *taken.Message, registryName)

	// The document's rule allows 5 to 50 alphanumerics; a name outside it is
	// refused for that reason.
	for _, bad := range []string{"abc", "has-a-hyphen", strings.Repeat("a", 51)} {
		invalid := check(bad)
		require.NotNil(t, invalid.NameAvailable, bad)
		assert.False(t, *invalid.NameAvailable, bad)
		require.NotNil(t, invalid.Reason, bad)
		assert.Equal(t, "Invalid", *invalid.Reason, bad)
	}
}
