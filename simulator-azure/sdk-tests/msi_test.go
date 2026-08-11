package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ensureMSIResourceGroup(t *testing.T, name string) {
	t.Helper()
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, name, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)
}

func TestMSI_UpdateAndList(t *testing.T) {
	rg := "msi-list-rg"
	ensureMSIResourceGroup(t, rg)
	client, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	_, err = client.CreateOrUpdate(ctx, rg, "msi-listed", armmsi.Identity{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	// PATCH (Update) sets tags.
	upd, err := client.Update(ctx, rg, "msi-listed", armmsi.IdentityUpdate{
		Tags: map[string]*string{"env": ptrStr("test")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, upd.Tags["env"])
	assert.Equal(t, "test", *upd.Tags["env"])

	// ListByResourceGroup.
	rgPager := client.NewListByResourceGroupPager(rg, nil)
	found := false
	for rgPager.More() {
		page, err := rgPager.NextPage(ctx)
		require.NoError(t, err)
		for _, i := range page.Value {
			if *i.Name == "msi-listed" {
				found = true
			}
		}
	}
	assert.True(t, found, "identity must appear in ListByResourceGroup")

	// ListBySubscription.
	subPager := client.NewListBySubscriptionPager(nil)
	for subPager.More() {
		_, err := subPager.NextPage(ctx)
		require.NoError(t, err)
	}
}

func TestMSI_FederatedIdentityCredentials(t *testing.T) {
	rg := "msi-fic-rg"
	ensureMSIResourceGroup(t, rg)
	uaClient, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = uaClient.CreateOrUpdate(ctx, rg, "msi-fic", armmsi.Identity{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	ficClient, err := armmsi.NewFederatedIdentityCredentialsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	created, err := ficClient.CreateOrUpdate(ctx, rg, "msi-fic", "gh-actions", armmsi.FederatedIdentityCredential{
		Properties: &armmsi.FederatedIdentityCredentialProperties{
			Issuer:    ptrStr("https://token.actions.githubusercontent.com"),
			Subject:   ptrStr("repo:octo-org/octo-repo:ref:refs/heads/main"),
			Audiences: []*string{ptrStr("api://AzureADTokenExchange")},
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "gh-actions", *created.Name)

	got, err := ficClient.Get(ctx, rg, "msi-fic", "gh-actions", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	assert.Equal(t, "https://token.actions.githubusercontent.com", *got.Properties.Issuer)
	assert.Equal(t, "repo:octo-org/octo-repo:ref:refs/heads/main", *got.Properties.Subject)
	require.Len(t, got.Properties.Audiences, 1)

	listPager := ficClient.NewListPager(rg, "msi-fic", nil)
	count := 0
	for listPager.More() {
		page, err := listPager.NextPage(ctx)
		require.NoError(t, err)
		count += len(page.Value)
	}
	assert.GreaterOrEqual(t, count, 1)

	_, err = ficClient.Delete(ctx, rg, "msi-fic", "gh-actions", nil)
	require.NoError(t, err)
}

func TestMSI_OperationsAndSystemAssigned(t *testing.T) {
	opsClient, err := armmsi.NewOperationsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pager := opsClient.NewListPager(nil)
	count := 0
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		count += len(page.Value)
	}
	assert.Greater(t, count, 0, "Microsoft.ManagedIdentity operations list must be non-empty")

	saClient, err := armmsi.NewSystemAssignedIdentitiesClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	resp, err := saClient.GetByScope(ctx, "subscriptions/"+subscriptionID, nil)
	require.NoError(t, err)
	require.NotNil(t, resp.Properties)
	assert.NotEmpty(t, *resp.Properties.PrincipalID)
	assert.NotEmpty(t, *resp.Properties.ClientID)
}
