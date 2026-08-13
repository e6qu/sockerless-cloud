package azure_sdk_test

import (
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAzureResources_Providers(t *testing.T) {
	client, err := armresources.NewProvidersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	reg, err := client.Register(ctx, "Microsoft.DBforPostgreSQL", nil)
	require.NoError(t, err)
	assert.Equal(t, "Microsoft.DBforPostgreSQL", *reg.Namespace)
	assert.Equal(t, "Registered", *reg.RegistrationState)

	got, err := client.Get(ctx, "Microsoft.Insights", nil)
	require.NoError(t, err)
	assert.Equal(t, "Microsoft.Insights", *got.Namespace)
	assert.NotEmpty(t, got.ResourceTypes)

	tenantGot, err := client.GetAtTenantScope(ctx, "Microsoft.Storage", nil)
	require.NoError(t, err)
	assert.Equal(t, "Microsoft.Storage", *tenantGot.Namespace)

	pager := client.NewListPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, page.Value)

	tenantPager := client.NewListAtTenantScopePager(nil)
	tenantPage, err := tenantPager.NextPage(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, tenantPage.Value)

	perms, err := client.ProviderPermissions(ctx, "Microsoft.Resources", nil)
	require.NoError(t, err)
	assert.NotNil(t, perms)

	unreg, err := client.Unregister(ctx, "Microsoft.DBforPostgreSQL", nil)
	require.NoError(t, err)
	assert.Equal(t, "Unregistered", *unreg.RegistrationState)
}

func TestAzureResources_ProviderResourceTypes(t *testing.T) {
	client, err := armresources.NewProviderResourceTypesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	resp, err := client.List(ctx, "Microsoft.DBforPostgreSQL", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Value)
}

func TestAzureResources_Operations(t *testing.T) {
	client, err := armresources.NewOperationsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pager := client.NewListPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, page.Value)
}

func TestAzureResources_PredefinedTagNames(t *testing.T) {
	client, err := armresources.NewTagsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	created, err := client.CreateOrUpdate(ctx, "costCenter", nil)
	require.NoError(t, err)
	assert.Equal(t, "costCenter", *created.TagName)

	val, err := client.CreateOrUpdateValue(ctx, "costCenter", "engineering", nil)
	require.NoError(t, err)
	assert.Equal(t, "engineering", *val.TagValue.TagValue)

	pager := client.NewListPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	found := false
	for _, td := range page.Value {
		if *td.TagName == "costCenter" {
			found = true
			require.Len(t, td.Values, 1)
			assert.Equal(t, "engineering", *td.Values[0].TagValue)
		}
	}
	assert.True(t, found, "costCenter must appear in tagNames list")

	_, err = client.DeleteValue(ctx, "costCenter", "engineering", nil)
	require.NoError(t, err)
	_, err = client.Delete(ctx, "costCenter", nil)
	require.NoError(t, err)
}

func TestAzureResources_ResourceGroupUpdateAndExport(t *testing.T) {
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	rg := "resources-rg-update"
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	updated, err := rgClient.Update(ctx, rg, armresources.ResourceGroupPatchable{
		Tags: map[string]*string{"team": to.Ptr("platform")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "platform", *updated.Tags["team"])

	poller, err := rgClient.BeginExportTemplate(ctx, rg, armresources.ExportTemplateRequest{
		Resources: []*string{to.Ptr("*")},
	}, nil)
	require.NoError(t, err)
	exported, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.NotNil(t, exported.Template)
}

func TestAzureResources_MoveResources(t *testing.T) {
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "move-src-rg", armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "move-dst-rg", armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)

	// The move operates on the real stores, so the moved resource must
	// exist: an App Service plan created under the source group.
	planID := webMoreEnsurePlan(t, "move-src-rg", "move-plan")

	client, err := armresources.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	move := armresources.MoveInfo{
		Resources:           []*string{to.Ptr(planID)},
		TargetResourceGroup: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/move-dst-rg"),
	}

	valPoller, err := client.BeginValidateMoveResources(ctx, "move-src-rg", move, nil)
	require.NoError(t, err)
	_, err = valPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	movePoller, err := client.BeginMoveResources(ctx, "move-src-rg", move, nil)
	require.NoError(t, err)
	_, err = movePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// The plan now answers under the destination group and is gone from the
	// source group.
	plans, err := armappservice.NewPlansClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	moved, err := plans.Get(ctx, "move-dst-rg", "move-plan", nil)
	require.NoError(t, err)
	assert.Contains(t, *moved.ID, "/resourceGroups/move-dst-rg/")
	if stale, err := plans.Get(ctx, "move-src-rg", "move-plan", nil); err == nil {
		t.Fatalf("plan still resolves under the source group after the move: %+v", stale)
	}

	// A resource that does not exist refuses to validate — the same
	// pre-flight real Azure Resource Manager runs.
	missing := armresources.MoveInfo{
		Resources:           []*string{to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/move-src-rg/providers/Microsoft.Web/serverfarms/never-created")},
		TargetResourceGroup: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/move-dst-rg"),
	}
	_, err = client.BeginValidateMoveResources(ctx, "move-src-rg", missing, nil)
	require.Error(t, err, "validating a move of an absent resource must fail")
}

// TestAzureResources_MoveStorageAccountAndMixedBatch drives the
// Microsoft.Storage move hook end to end: real blob bytes written through the
// storage SDK before the move read back after it, the ARM account record and
// its container child relocate, listKeys serves the same key material under
// the destination group, a mixed batch moves a Microsoft.Web site and plan
// together with the account, and a type Azure Resource Manager does not move
// (an Azure Container Instances container group) still answers
// ResourceMoveNotSupported.
func TestAzureResources_MoveStorageAccountAndMixedBatch(t *testing.T) {
	srcRG, dstRG := "move-mixed-src-rg", "move-mixed-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	account := "movemixedacct"
	accounts := createStorageAccountForARM(t, srcRG, account)

	// An ARM-plane container child and real blob bytes on the data plane,
	// both written before the move.
	bcClient, err := armstorage.NewBlobContainersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	const containerName = "move-mixed-container"
	_, err = bcClient.Create(ctx, srcRG, account, containerName, armstorage.BlobContainer{}, nil)
	require.NoError(t, err)
	blobClient := newBlobTestClient(t, account)
	_, err = blobClient.CreateContainer(ctx, containerName, nil)
	require.NoError(t, err)
	const payload = "bytes written before the move"
	_, err = blobClient.UploadBuffer(ctx, containerName, "moved.txt", []byte(payload), nil)
	require.NoError(t, err)

	keysBefore := listStorageKeys(t, srcRG, account)
	require.Len(t, keysBefore, 2)

	// Tags written at the account's scope before the move — Azure Resource
	// Manager moves a resource's tags with the resource.
	acctID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + srcRG + "/providers/Microsoft.Storage/storageAccounts/" + account
	tagsClient, err := armresources.NewTagsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = tagsClient.CreateOrUpdateAtScope(ctx, acctID, armresources.TagsResource{
		Properties: &armresources.Tags{Tags: map[string]*string{"team": to.Ptr("storage")}},
	}, nil)
	require.NoError(t, err)

	// The mixed batch: a Microsoft.Web site and its plan alongside the account.
	planID := webMoreEnsurePlan(t, srcRG, "move-mixed-plan")
	webApps, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	webMoreCreateSite(t, webApps, srcRG, "move-mixed-site", planID)
	siteID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + srcRG + "/providers/Microsoft.Web/sites/move-mixed-site"

	client, err := armresources.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	move := armresources.MoveInfo{
		Resources:           []*string{to.Ptr(acctID), to.Ptr(siteID), to.Ptr(planID)},
		TargetResourceGroup: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + dstRG),
	}
	valPoller, err := client.BeginValidateMoveResources(ctx, srcRG, move, nil)
	require.NoError(t, err)
	_, err = valPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	movePoller, err := client.BeginMoveResources(ctx, srcRG, move, nil)
	require.NoError(t, err)
	_, err = movePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// The ARM record relocated: the account answers under the destination
	// group with its ID rewritten, and no longer under the source group.
	movedAcct, err := accounts.GetProperties(ctx, dstRG, account, nil)
	require.NoError(t, err)
	require.NotNil(t, movedAcct.ID)
	assert.Contains(t, *movedAcct.ID, "/resourceGroups/"+dstRG+"/")
	_, err = accounts.GetProperties(ctx, srcRG, account, nil)
	require.Error(t, err, "the moved account must no longer resolve under the source group")

	// The ARM container child moved with the account.
	movedContainer, err := bcClient.Get(ctx, dstRG, account, containerName, nil)
	require.NoError(t, err)
	require.NotNil(t, movedContainer.ID)
	assert.Contains(t, *movedContainer.ID, "/resourceGroups/"+dstRG+"/")
	_, err = bcClient.Get(ctx, srcRG, account, containerName, nil)
	require.Error(t, err, "the moved container child must no longer resolve under the source group")

	// The account keys are a property of the account, not of its resource
	// group: listKeys under the destination group serves the same material.
	keysAfter := listStorageKeys(t, dstRG, account)
	assert.Equal(t, keysBefore, keysAfter, "a cross-group move must not rotate the account keys")

	// The tags written at the account's old scope answer at the new one.
	movedTags, err := tagsClient.GetAtScope(ctx, *movedAcct.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, movedTags.Properties)
	require.NotNil(t, movedTags.Properties.Tags["team"])
	assert.Equal(t, "storage", *movedTags.Properties.Tags["team"], "tags must move with the resource")

	// The bytes written before the move read back through the data plane.
	download, err := blobClient.DownloadStream(ctx, containerName, "moved.txt", nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, payload, string(got), "blob bytes must survive the account move")

	// The site in the same batch moved too, with its plan reference following.
	movedSite, err := webApps.Get(ctx, dstRG, "move-mixed-site", nil)
	require.NoError(t, err)
	require.NotNil(t, movedSite.Properties.ServerFarmID)
	assert.Contains(t, *movedSite.Properties.ServerFarmID, "/resourceGroups/"+dstRG+"/")
	_, err = webApps.Get(ctx, srcRG, "move-mixed-site", nil)
	require.Error(t, err, "the moved site must no longer resolve under the source group")

	// A type Azure Resource Manager does not move still answers
	// ResourceMoveNotSupported with the 409 a real validation failure reports.
	_, err = client.BeginValidateMoveResources(ctx, srcRG, armresources.MoveInfo{
		Resources:           []*string{to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + srcRG + "/providers/Microsoft.ContainerInstance/containerGroups/never-movable")},
		TargetResourceGroup: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + dstRG),
	}, nil)
	require.Error(t, err, "validating a move of an unmovable type must fail")
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "want *azcore.ResponseError, got %T: %v", err, err)
	assert.Equal(t, "ResourceMoveNotSupported", respErr.ErrorCode)
	assert.Equal(t, http.StatusConflict, respErr.StatusCode)
}
