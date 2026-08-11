package gcp_sdk_test

import (
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

	resp, err := svc.Projects.Locations.List("projects/test-project").Do()
	require.NoError(t, err)
	require.NotEmpty(t, resp.Locations)

	var found bool
	for _, loc := range resp.Locations {
		assert.NotEmpty(t, loc.Name)
		assert.NotEmpty(t, loc.LocationId)
		if loc.LocationId == "us-central1" {
			found = true
			assert.Equal(t, "projects/test-project/locations/us-central1", loc.Name)
		}
	}
	assert.True(t, found, "expected us-central1 in the location list")
}

func TestSDK_CloudFunctionsV2_ListRuntimes(t *testing.T) {
	svc := newCloudFunctionsV2Service(t)

	resp, err := svc.Projects.Locations.Runtimes.List("projects/test-project/locations/us-central1").Do()
	require.NoError(t, err)
	require.NotEmpty(t, resp.Runtimes)

	var sawGo bool
	for _, rt := range resp.Runtimes {
		assert.NotEmpty(t, rt.Name)
		assert.NotEmpty(t, rt.DisplayName)
		if strings.HasPrefix(rt.Name, "go") {
			sawGo = true
			assert.Equal(t, "GEN_2", rt.Environment)
			assert.Equal(t, "GA", rt.Stage)
		}
	}
	assert.True(t, sawGo, "expected at least one Go runtime")
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

func TestSDK_CloudFunctionsV2_GenerateUploadUrl(t *testing.T) {
	svc := newCloudFunctionsV2Service(t)
	parent := "projects/test-project/locations/us-central1"

	resp, err := svc.Projects.Locations.Functions.GenerateUploadUrl(parent, &cloudfunctions.GenerateUploadUrlRequest{
		Environment: "GEN_2",
	}).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, resp.UploadUrl)
	require.NotNil(t, resp.StorageSource)
	assert.NotEmpty(t, resp.StorageSource.Bucket)
	assert.NotEmpty(t, resp.StorageSource.Object)
}

func TestSDK_CloudFunctionsV2_GenerateDownloadUrl(t *testing.T) {
	svc := newCloudFunctionsV2Service(t)
	parent := "projects/test-project/locations/us-central1"
	name := createV2Function(t, svc, parent, "sdk-v2-download-fn")

	resp, err := svc.Projects.Locations.Functions.GenerateDownloadUrl(name, &cloudfunctions.GenerateDownloadUrlRequest{}).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, resp.DownloadUrl)
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
