package azure_sdk_test

import (
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

	// SystemAssignedIdentities_GetByScope answers the system-assigned
	// identity of the resource the scope names; a subscription carries none.
	rg, name := "msi-scope-rg", "msi-scope-app"
	ensureMSIResourceGroup(t, rg)
	sites, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	t.Cleanup(func() { azureDeleteSite(rg, name) })
	poller, err := sites.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: ptrStr("eastus"),
		Kind:     ptrStr("app,linux"),
		Identity: &armappservice.ManagedServiceIdentity{Type: to.Ptr(armappservice.ManagedServiceIdentityTypeSystemAssigned)},
		Properties: &armappservice.SiteProperties{
			ServerFarmID: ptrStr(webMoreEnsurePlan(t, rg, name+"-plan")),
			SiteConfig:   &armappservice.SiteConfig{LinuxFxVersion: ptrStr("DOCKER|mirror.gcr.io/library/alpine:latest")},
		},
	}, nil)
	require.NoError(t, err)
	site, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, site.Identity)

	saClient, err := armmsi.NewSystemAssignedIdentitiesClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	resp, err := saClient.GetByScope(ctx, strings.TrimPrefix(*site.ID, "/"), nil)
	require.NoError(t, err)
	require.NotNil(t, resp.Properties)
	assert.Equal(t, *site.Identity.PrincipalID, *resp.Properties.PrincipalID)
	assert.Equal(t, *site.Identity.TenantID, *resp.Properties.TenantID)
	assert.NotEmpty(t, *resp.Properties.ClientID)
	assert.Equal(t, *site.ID+"/providers/Microsoft.ManagedIdentity/identities/default", *resp.ID)

	_, err = saClient.GetByScope(ctx, "subscriptions/"+subscriptionID, nil)
	require.Error(t, err, "a subscription has no system-assigned identity")
	assert.Contains(t, err.Error(), "ResourceNotFound")
}
