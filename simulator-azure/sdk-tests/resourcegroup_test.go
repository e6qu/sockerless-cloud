package azure_sdk_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceGroup_CreateAndGet(t *testing.T) {
	t.Parallel()
	client, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	resp, err := client.CreateOrUpdate(ctx, "test-rg-4a8c", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "test-rg-4a8c", *resp.Name)
	assert.Equal(t, "eastus", *resp.Location)

	getResp, err := client.Get(ctx, "test-rg-4a8c", nil)
	require.NoError(t, err)
	assert.Equal(t, "test-rg-4a8c", *getResp.Name)
}

// Deleting a resource group is a long-running operation that takes its
// resources with it; the poller follows the Location
// (/subscriptions/{subscriptionId}/operationresults/{operationId}) until the
// group is gone.
func TestResourceGroup_Delete(t *testing.T) {
	t.Parallel()
	client, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	identities, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	group := uniquename.New("sdk-del-rg")
	_, err = client.CreateOrUpdate(ctx, group, armresources.ResourceGroup{
		Location: ptrStr("westus"),
	}, nil)
	require.NoError(t, err)
	identity := uniquename.New("sdk-del-uai")
	_, err = identities.CreateOrUpdate(ctx, group, identity, armmsi.Identity{Location: ptrStr("westus")}, nil)
	require.NoError(t, err)

	var accepted *http.Response
	poller, err := client.BeginDelete(policy.WithCaptureResponse(ctx, &accepted), group, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, accepted.StatusCode)
	location, err := url.Parse(accepted.Header.Get("Location"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(location.Path, "/subscriptions/"+subscriptionID+"/operationresults/"),
		"the delete's Location %q", location)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	_, err = client.Get(ctx, group, nil)
	var missing *azcore.ResponseError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode)
	assert.Equal(t, "ResourceGroupNotFound", missing.ErrorCode)
	_, err = identities.Get(ctx, group, identity, nil)
	var gone *azcore.ResponseError
	require.ErrorAs(t, err, &gone, "the identity must go with its group")
	assert.Equal(t, http.StatusNotFound, gone.StatusCode)
}

func TestResourceGroup_DeleteMissingGroup(t *testing.T) {
	t.Parallel()
	client, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	_, err = client.BeginDelete(ctx, uniquename.New("sdk-never-rg"), nil)
	var missing *azcore.ResponseError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode)
	assert.Equal(t, "ResourceGroupNotFound", missing.ErrorCode)
}

// Azure Resource Manager refuses a request for a resource in a missing group
// before the resource provider sees it.
func TestResourceGroup_MissingGroupRefusesItsResources(t *testing.T) {
	t.Parallel()
	identities, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	group := uniquename.New("sdk-never-rg")

	_, err = identities.CreateOrUpdate(ctx, group, "orphan", armmsi.Identity{Location: ptrStr("eastus")}, nil)
	var refused *azcore.ResponseError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, http.StatusNotFound, refused.StatusCode)
	assert.Equal(t, "ResourceGroupNotFound", refused.ErrorCode)
	assert.Contains(t, err.Error(), "Resource group '"+group+"' could not be found.")

	_, err = identities.Get(ctx, group, "orphan", nil)
	var missing *azcore.ResponseError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode)
	assert.Equal(t, "ResourceGroupNotFound", missing.ErrorCode)
}

func TestResourceGroup_CheckExistence(t *testing.T) {
	t.Parallel()
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
