package gcp_sdk_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cloudfunctions "google.golang.org/api/cloudfunctions/v2"
	"google.golang.org/api/option"
)

// These tests exercise the Cloud Functions v2 surface through the official
// Discovery REST client (google.golang.org/api/cloudfunctions/v2): the
// locations / operations / runtimes list collections, the AIP-141 IAM verbs,
// generateUploadUrl / generateDownloadUrl, and an upgrade-lifecycle verb.

func newCloudFunctionsV2Service(t *testing.T) *cloudfunctions.Service {
	t.Helper()
	svc, err := cloudfunctions.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

// createV2Function creates a function via the REST client and waits out the LRO
// by returning its name. The LRO is synchronous in the sim (done=true).
func createV2Function(t *testing.T, svc *cloudfunctions.Service, parent, id string) string {
	t.Helper()
	op, err := svc.Projects.Locations.Functions.Create(parent, &cloudfunctions.Function{
		BuildConfig: &cloudfunctions.BuildConfig{
			Runtime:    "go122",
			EntryPoint: "Handler",
		},
	}).FunctionId(id).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	return parent + "/functions/" + id
}

func TestSDK_CloudFunctionsV2_ListLocations(t *testing.T) {
	svc := newCloudFunctionsV2Service(t)

	const parent = "projects/test-project"
	resp, err := svc.Projects.Locations.List(parent).Do()
	require.NoError(t, err)
	require.NotEmpty(t, resp.Locations, "an empty list would satisfy every per-item check below")

	ids := make([]string, 0, len(resp.Locations))
	for _, loc := range resp.Locations {
		require.NotEmpty(t, loc.LocationId)
		ids = append(ids, loc.LocationId)
		// A Location's name is the parent plus its own id; a mismatch would
		// break every client that derives one from the other.
		assert.Equal(t, parent+"/locations/"+loc.LocationId, loc.Name)
		assert.Equal(t, loc.LocationId, loc.DisplayName)
		assert.Equal(t, loc.LocationId, loc.Labels["cloud.googleapis.com/region"])
	}
	// Cloud Functions is offered on at least one region of each continent the
	// runtime targets; the deployment region the rest of this suite uses must
	// be among them.
	assert.Subset(t, ids, []string{"us-central1", "europe-west1", "asia-east1"})
	assert.Len(t, ids, len(resp.Locations), "every location must carry a locationId")
}

func TestSDK_CloudFunctionsV2_ListRuntimes(t *testing.T) {
	svc := newCloudFunctionsV2Service(t)

	resp, err := svc.Projects.Locations.Runtimes.List("projects/test-project/locations/us-central1").Do()
	require.NoError(t, err)
	require.NotEmpty(t, resp.Runtimes, "an empty list would satisfy every per-item check below")

	names := make([]string, 0, len(resp.Runtimes))
	for _, rt := range resp.Runtimes {
		require.NotEmpty(t, rt.Name)
		assert.NotEmpty(t, rt.DisplayName, "runtime %s carries no displayName", rt.Name)
		// Every runtime the v2 API lists is a 2nd-gen runtime.
		assert.Equal(t, "GEN_2", rt.Environment, "runtime %s", rt.Name)
		assert.Equal(t, "GA", rt.Stage, "runtime %s", rt.Name)
		names = append(names, rt.Name)
	}
	// The runtimes createV2Function deploys with must be offered, alongside a
	// representative of each other language family.
	assert.Subset(t, names, []string{"go122", "go121", "nodejs20", "python312", "java21"})
}

func TestSDK_CloudFunctionsV2_ListAndGetOperations(t *testing.T) {
	svc := newCloudFunctionsV2Service(t)
	parent := "projects/test-project/locations/us-central1"

	// Creating a function records an LRO under the location's operations
	// collection; list it back and Get it by name.
	op, err := svc.Projects.Locations.Functions.Create(parent, &cloudfunctions.Function{
		BuildConfig: &cloudfunctions.BuildConfig{Runtime: "go122", EntryPoint: "Handler"},
	}).FunctionId("sdk-v2-ops-fn").Do()
	require.NoError(t, err)
	require.NotEmpty(t, op.Name)

	listResp, err := svc.Projects.Locations.Operations.List(parent).Do()
	require.NoError(t, err)
	require.NotEmpty(t, listResp.Operations)
	var listed bool
	for _, o := range listResp.Operations {
		if o.Name == op.Name {
			listed = true
		}
	}
	assert.True(t, listed, "expected the create LRO in the operations list")

	got, err := svc.Projects.Locations.Operations.Get(op.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, op.Name, got.Name)
	assert.True(t, got.Done)
}

func TestSDK_CloudFunctionsV2_IAMPolicy(t *testing.T) {
	svc := newCloudFunctionsV2Service(t)
	parent := "projects/test-project/locations/us-central1"
	name := createV2Function(t, svc, parent, "sdk-v2-iam-fn")

	// setIamPolicy then getIamPolicy round-trips the binding.
	setResp, err := svc.Projects.Locations.Functions.SetIamPolicy(name, &cloudfunctions.SetIamPolicyRequest{
		Policy: &cloudfunctions.Policy{
			Bindings: []*cloudfunctions.Binding{{
				Role:    "roles/cloudfunctions.invoker",
				Members: []string{"user:alice@example.com"},
			}},
		},
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, setResp.Bindings)

	getResp, err := svc.Projects.Locations.Functions.GetIamPolicy(name).Do()
	require.NoError(t, err)
	require.Len(t, getResp.Bindings, 1)
	assert.Equal(t, "roles/cloudfunctions.invoker", getResp.Bindings[0].Role)
	assert.Equal(t, []string{"user:alice@example.com"}, getResp.Bindings[0].Members)

	// testIamPermissions echoes the admin-allowed subset.
	tip, err := svc.Projects.Locations.Functions.TestIamPermissions(name, &cloudfunctions.TestIamPermissionsRequest{
		Permissions: []string{"cloudfunctions.functions.invoke"},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"cloudfunctions.functions.invoke"}, tip.Permissions)
}

// generateUploadURL calls generateUploadUrl for a location. The source bucket it
// names is the location's own, so a second endpoint's URL can be checked against
// it rather than against a bucket name spelled out in the test.
func generateUploadURL(t *testing.T, svc *cloudfunctions.Service, parent string) *cloudfunctions.GenerateUploadUrlResponse {
	t.Helper()
	resp, err := svc.Projects.Locations.Functions.GenerateUploadUrl(parent, &cloudfunctions.GenerateUploadUrlRequest{
		Environment: "GEN_2",
	}).Do()
	require.NoError(t, err)
	return resp
}

// endpointHost is the host:port the simulator serves on — the host of every URL
// the simulator hands back to a client.
func endpointHost(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	return u.Host
}

func TestSDK_CloudFunctionsV2_GenerateUploadUrl(t *testing.T) {
	svc := newCloudFunctionsV2Service(t)
	parent := "projects/test-project/locations/us-central1"

	resp := generateUploadURL(t, svc, parent)
	require.NotNil(t, resp.StorageSource)
	require.NotEmpty(t, resp.StorageSource.Bucket)
	require.NotEmpty(t, resp.StorageSource.Object)
	require.NotEmpty(t, resp.UploadUrl)

	// The upload URL and the storageSource describe one destination: the client
	// PUTs the function's zip at the URL and then hands the storageSource to
	// CreateFunction, so a URL addressing any other bucket or object would
	// create a function whose source went somewhere else.
	upload, err := url.Parse(resp.UploadUrl)
	require.NoError(t, err, "uploadUrl must be an absolute URL: %q", resp.UploadUrl)
	assert.Equal(t, endpointHost(t), upload.Host, "the upload URL must address the service that issued it")
	assert.Equal(t, "/upload/storage/v1/b/"+resp.StorageSource.Bucket+"/o", upload.Path,
		"the upload URL must target the bucket the response names")
	assert.Equal(t, "resumable", upload.Query().Get("uploadType"),
		"a source upload is a resumable session the client PUTs chunks to")
	assert.Equal(t, resp.StorageSource.Object, upload.Query().Get("name"),
		"the upload URL must target the object the response names")

	// Two calls hand out two objects: concurrent deployments must not overwrite
	// each other's source.
	other := generateUploadURL(t, svc, parent)
	assert.Equal(t, resp.StorageSource.Bucket, other.StorageSource.Bucket,
		"a location has one function-source bucket")
	assert.NotEqual(t, resp.StorageSource.Object, other.StorageSource.Object,
		"each generateUploadUrl must hand out an object of its own")
}

func TestSDK_CloudFunctionsV2_GenerateDownloadUrl(t *testing.T) {
	svc := newCloudFunctionsV2Service(t)
	parent := "projects/test-project/locations/us-central1"
	name := createV2Function(t, svc, parent, "sdk-v2-download-fn")
	otherName := createV2Function(t, svc, parent, "sdk-v2-download-fn-2")

	resp, err := svc.Projects.Locations.Functions.GenerateDownloadUrl(name, &cloudfunctions.GenerateDownloadUrlRequest{}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, resp.DownloadUrl)

	download, err := url.Parse(resp.DownloadUrl)
	require.NoError(t, err, "downloadUrl must be an absolute URL: %q", resp.DownloadUrl)
	assert.Equal(t, endpointHost(t), download.Host, "the download URL must address the service that issued it")
	assert.Equal(t, "media", download.Query().Get("alt"),
		"a source download reads the object's bytes, not its metadata")

	// The archive lives in the location's function-source bucket — the same one
	// generateUploadUrl writes to.
	sourceBucket := generateUploadURL(t, svc, parent).StorageSource.Bucket
	assert.True(t, strings.HasPrefix(download.Path, "/download/storage/v1/b/"+sourceBucket+"/o/"),
		"download path %q must address the function-source bucket %q", download.Path, sourceBucket)
	assert.True(t, strings.HasSuffix(download.Path, ".zip"),
		"a function's source archive is a zip: %q", download.Path)

	// Each function's source archive is its own: two functions must never be
	// handed the same download target.
	otherResp, err := svc.Projects.Locations.Functions.GenerateDownloadUrl(otherName, &cloudfunctions.GenerateDownloadUrlRequest{}).Do()
	require.NoError(t, err)
	assert.NotEqual(t, resp.DownloadUrl, otherResp.DownloadUrl,
		"two functions must not share a source-download URL")

	// A function the service does not hold has no source to download.
	_, err = svc.Projects.Locations.Functions.GenerateDownloadUrl(
		parent+"/functions/sdk-v2-no-such-fn", &cloudfunctions.GenerateDownloadUrlRequest{}).Do()
	requireGoogleErr(t, err, 404, "NOT_FOUND")
}

func TestSDK_CloudFunctionsV2_UpgradeLifecycle(t *testing.T) {
	svc := newCloudFunctionsV2Service(t)
	parent := "projects/test-project/locations/us-central1"
	name := createV2Function(t, svc, parent, "sdk-v2-upgrade-fn")

	// setupFunctionUpgradeConfig drives upgradeInfo.upgradeState to the
	// setup-complete state; the LRO resolves done with the Function in
	// its response.
	op, err := svc.Projects.Locations.Functions.SetupFunctionUpgradeConfig(name,
		&cloudfunctions.SetupFunctionUpgradeConfigRequest{}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	got, err := svc.Projects.Locations.Functions.Get(name).Do()
	require.NoError(t, err)
	require.NotNil(t, got.UpgradeInfo)
	assert.Equal(t, "SETUP_FUNCTION_UPGRADE_CONFIG_SUCCESSFUL", got.UpgradeInfo.UpgradeState)

	// redirectFunctionUpgradeTraffic advances the state.
	_, err = svc.Projects.Locations.Functions.RedirectFunctionUpgradeTraffic(name,
		&cloudfunctions.RedirectFunctionUpgradeTrafficRequest{}).Do()
	require.NoError(t, err)
	got, err = svc.Projects.Locations.Functions.Get(name).Do()
	require.NoError(t, err)
	require.NotNil(t, got.UpgradeInfo)
	assert.Equal(t, "REDIRECT_FUNCTION_UPGRADE_TRAFFIC_SUCCESSFUL", got.UpgradeInfo.UpgradeState)

	// commitFunctionUpgrade finalizes the migration: upgradeInfo is cleared.
	op, err = svc.Projects.Locations.Functions.CommitFunctionUpgrade(name,
		&cloudfunctions.CommitFunctionUpgradeRequest{}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	got, err = svc.Projects.Locations.Functions.Get(name).Do()
	require.NoError(t, err)
	assert.Nil(t, got.UpgradeInfo)
}
