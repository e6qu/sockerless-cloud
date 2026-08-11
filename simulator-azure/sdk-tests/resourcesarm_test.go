package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
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

	client, err := armresources.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	move := armresources.MoveInfo{
		Resources:           []*string{to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/move-src-rg/providers/Microsoft.Storage/storageAccounts/movesa")},
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
}
