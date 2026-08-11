package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceGroup_CreateAndGet(t *testing.T) {
	client, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	resp, err := client.CreateOrUpdate(ctx, "test-rg", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "test-rg", *resp.Name)
	assert.Equal(t, "eastus", *resp.Location)

	getResp, err := client.Get(ctx, "test-rg", nil)
	require.NoError(t, err)
	assert.Equal(t, "test-rg", *getResp.Name)
}

func TestResourceGroup_Delete(t *testing.T) {
	client, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	_, err = client.CreateOrUpdate(ctx, "del-rg", armresources.ResourceGroup{
		Location: ptrStr("westus"),
	}, nil)
	require.NoError(t, err)

	poller, err := client.BeginDelete(ctx, "del-rg", nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func TestResourceGroup_CheckExistence(t *testing.T) {
	client, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	_, err = client.CreateOrUpdate(ctx, "exists-rg", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	resp, err := client.CheckExistence(ctx, "exists-rg", nil)
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func ptrStr(s string) *string {
	return &s
}
