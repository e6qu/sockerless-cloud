package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAzureSubscriptions_ListAndLocations(t *testing.T) {
	client, err := armsubscriptions.NewClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)

	pager := client.NewListPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, page.Value)

	get, err := client.Get(ctx, subscriptionID, nil)
	require.NoError(t, err)
	assert.Equal(t, subscriptionID, *get.SubscriptionID)

	locPager := client.NewListLocationsPager(subscriptionID, nil)
	locPage, err := locPager.NextPage(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, locPage.Value)
	assert.Equal(t, "eastus", *locPage.Value[0].Name)
}

func TestAzureSubscriptions_Tenants(t *testing.T) {
	client, err := armsubscriptions.NewTenantsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pager := client.NewListPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, page.Value)
	assert.NotEmpty(t, *page.Value[0].TenantID)
}

func TestAzureSubscriptions_Operations(t *testing.T) {
	client, err := armsubscriptions.NewOperationsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pager := client.NewListPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, page.Value)
}

func TestAzureSubscriptions_CheckResourceName(t *testing.T) {
	client, err := armsubscriptions.NewSubscriptionClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)

	allowed, err := client.CheckResourceName(ctx, &armsubscriptions.SubscriptionClientCheckResourceNameOptions{
		ResourceNameDefinition: &armsubscriptions.ResourceName{
			Name: to.Ptr("my-storage-account"),
			Type: to.Ptr("Microsoft.Storage/storageAccounts"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, armsubscriptions.ResourceNameStatus("Allowed"), *allowed.Status)

	reserved, err := client.CheckResourceName(ctx, &armsubscriptions.SubscriptionClientCheckResourceNameOptions{
		ResourceNameDefinition: &armsubscriptions.ResourceName{
			Name: to.Ptr("microsoft"),
			Type: to.Ptr("Microsoft.Storage/storageAccounts"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, armsubscriptions.ResourceNameStatus("Reserved"), *reserved.Status)
}

func TestAzureSubscriptions_CheckZonePeers(t *testing.T) {
	client, err := armsubscriptions.NewClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	resp, err := client.CheckZonePeers(ctx, subscriptionID, armsubscriptions.CheckZonePeersRequest{
		Location:        to.Ptr("eastus"),
		SubscriptionIDs: []*string{to.Ptr("00000000-0000-0000-0000-000000000002")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, subscriptionID, *resp.SubscriptionID)
	assert.NotEmpty(t, resp.AvailabilityZonePeers)
}
