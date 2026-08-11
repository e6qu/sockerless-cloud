package azure_sdk_test

// Microsoft.Subscription alias API (2021-10-01) through the official
// armsubscription SDK module. Routes exercised:
//
//	PUT /providers/Microsoft.Subscription/aliases/{aliasName}
//	GET /providers/Microsoft.Subscription/aliases/{aliasName}
//	DELETE /providers/Microsoft.Subscription/aliases/{aliasName}
//	GET /providers/Microsoft.Subscription/aliases
//	POST /subscriptions/{subscriptionId}/providers/Microsoft.Subscription/cancel
//	POST /subscriptions/{subscriptionId}/providers/Microsoft.Subscription/rename
//	POST /subscriptions/{subscriptionId}/providers/Microsoft.Subscription/enable

import (
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/subscription/armsubscription"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSubscriptionAlias_CreateLifecycle drives the full programmatic
// subscription-creation flow: the alias PUT under a billing scope, the SDK's
// long-running-operation poller to Succeeded, visibility of the created
// subscription through the subscriptions list, a resource group created
// inside the new subscription, and the rename/cancel/enable actions.
func TestSubscriptionAlias_CreateLifecycle(t *testing.T) {
	aliasClient, err := armsubscription.NewAliasClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)

	const aliasName = "sdk-test-sub-alias"
	poller, err := aliasClient.BeginCreate(ctx, aliasName, armsubscription.PutAliasRequest{
		Properties: &armsubscription.PutAliasRequestProperties{
			DisplayName:  to.Ptr("SDK Created Subscription"),
			Workload:     to.Ptr(armsubscription.WorkloadProduction),
			BillingScope: to.Ptr("/providers/Microsoft.Billing/billingAccounts/sim-billing-account/enrollmentAccounts/sim-enrollment"),
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.ProvisioningState)
	assert.Equal(t, armsubscription.ProvisioningStateSucceeded, *created.Properties.ProvisioningState)
	require.NotNil(t, created.Properties.SubscriptionID)
	newSubID := *created.Properties.SubscriptionID
	require.NotEmpty(t, newSubID)
	require.NotEqual(t, subscriptionID, newSubID, "a created subscription must get its own id")

	// A repeated PUT with the same intent is idempotent: same alias, same
	// subscription, no second creation.
	again, err := aliasClient.BeginCreate(ctx, aliasName, armsubscription.PutAliasRequest{
		Properties: &armsubscription.PutAliasRequestProperties{
			DisplayName:  to.Ptr("SDK Created Subscription"),
			Workload:     to.Ptr(armsubscription.WorkloadProduction),
			BillingScope: to.Ptr("/providers/Microsoft.Billing/billingAccounts/sim-billing-account/enrollmentAccounts/sim-enrollment"),
		},
	}, nil)
	require.NoError(t, err)
	againResult, err := again.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, againResult.Properties)
	assert.Equal(t, newSubID, *againResult.Properties.SubscriptionID)

	get, err := aliasClient.Get(ctx, aliasName, nil)
	require.NoError(t, err)
	assert.Equal(t, aliasName, *get.Name)
	assert.Equal(t, "Microsoft.Subscription/aliases", *get.Type)
	assert.Equal(t, newSubID, *get.Properties.SubscriptionID)

	list, err := aliasClient.List(ctx, nil)
	require.NoError(t, err)
	found := false
	for _, alias := range list.Value {
		if alias.Name != nil && *alias.Name == aliasName {
			found = true
		}
	}
	assert.True(t, found, "alias list must include %q", aliasName)

	// The created subscription is a real, Enabled subscription: readable,
	// listed, and usable as a scope for resource creation.
	subsClient, err := armsubscriptions.NewClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	sub, err := subsClient.Get(ctx, newSubID, nil)
	require.NoError(t, err)
	assert.Equal(t, "SDK Created Subscription", *sub.DisplayName)
	assert.Equal(t, armsubscriptions.SubscriptionStateEnabled, *sub.State)

	pager := subsClient.NewListPager(nil)
	listed := false
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, s := range page.Value {
			if s.SubscriptionID != nil && *s.SubscriptionID == newSubID {
				listed = true
			}
		}
	}
	assert.True(t, listed, "created subscription %q must appear in the subscriptions list", newSubID)

	rgClient, err := armresources.NewResourceGroupsClient(newSubID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	rg, err := rgClient.CreateOrUpdate(ctx, "sdk-test-sub-alias-rg", armresources.ResourceGroup{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, *rg.ID, "/subscriptions/"+newSubID+"/resourceGroups/sdk-test-sub-alias-rg")

	// Rename, cancel, and enable — the subscription-scoped actions.
	subActions, err := armsubscription.NewClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	renamed, err := subActions.Rename(ctx, newSubID, armsubscription.Name{
		SubscriptionName: to.Ptr("SDK Renamed Subscription"),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, newSubID, *renamed.SubscriptionID)
	sub, err = subsClient.Get(ctx, newSubID, nil)
	require.NoError(t, err)
	assert.Equal(t, "SDK Renamed Subscription", *sub.DisplayName)

	cancelled, err := subActions.Cancel(ctx, newSubID, nil)
	require.NoError(t, err)
	assert.Equal(t, newSubID, *cancelled.SubscriptionID)
	sub, err = subsClient.Get(ctx, newSubID, nil)
	require.NoError(t, err)
	assert.Equal(t, armsubscriptions.SubscriptionStateDisabled, *sub.State)

	enabled, err := subActions.Enable(ctx, newSubID, nil)
	require.NoError(t, err)
	assert.Equal(t, newSubID, *enabled.SubscriptionID)
	sub, err = subsClient.Get(ctx, newSubID, nil)
	require.NoError(t, err)
	assert.Equal(t, armsubscriptions.SubscriptionStateEnabled, *sub.State)

	_, err = aliasClient.Delete(ctx, aliasName, nil)
	require.NoError(t, err)
	_, err = aliasClient.Get(ctx, aliasName, nil)
	var respErr *azcore.ResponseError
	require.ErrorAs(t, err, &respErr)
	assert.Equal(t, 404, respErr.StatusCode)

	// Deleting the alias never deletes the subscription.
	sub, err = subsClient.Get(ctx, newSubID, nil)
	require.NoError(t, err)
	assert.Equal(t, armsubscriptions.SubscriptionStateEnabled, *sub.State)
}

// TestSubscriptionAlias_ProviderRefreshReadPath walks the exact two-call
// sequence the azurerm provider's `azurerm_subscription` refresh performs
// against a live alias: Alias_Get for the alias named in the resource id,
// then Subscriptions_Get for the subscription id that alias reports. The
// provider stores what those two responses carry — the alias resource path
// as the resource id, and the subscription's id, display name and tenant id
// as attributes — so a missing member in either response makes it either
// drop the resource from state (plan: create) or plan a spurious change.
//
// Refresh is repeated to assert the read is stable: the same alias must keep
// answering with the same subscription id, and the subscription must stay
// Enabled once the creation operation has settled.
func TestSubscriptionAlias_ProviderRefreshReadPath(t *testing.T) {
	aliasClient, err := armsubscription.NewAliasClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subsClient, err := armsubscriptions.NewClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)

	const aliasName = "sdk-test-sub-alias-refresh"
	const billingScope = "/providers/Microsoft.Billing/billingAccounts/sim-billing-account/enrollmentAccounts/sim-enrollment"
	poller, err := aliasClient.BeginCreate(ctx, aliasName, armsubscription.PutAliasRequest{
		Properties: &armsubscription.PutAliasRequestProperties{
			DisplayName:  to.Ptr("SDK Refresh Subscription"),
			Workload:     to.Ptr(armsubscription.WorkloadProduction),
			BillingScope: to.Ptr(billingScope),
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.SubscriptionID)
	newSubID := *created.Properties.SubscriptionID
	t.Cleanup(func() {
		_, err := aliasClient.Delete(ctx, aliasName, nil)
		require.NoError(t, err)
	})

	for attempt := 1; attempt <= 2; attempt++ {
		alias, err := aliasClient.Get(ctx, aliasName, nil)
		require.NoError(t, err, "refresh %d: Alias_Get", attempt)
		require.NotNil(t, alias.ID, "refresh %d: alias id is the terraform resource id", attempt)
		assert.Equal(t, "/providers/Microsoft.Subscription/aliases/"+aliasName, *alias.ID)
		require.NotNil(t, alias.Name)
		assert.Equal(t, aliasName, *alias.Name)
		require.NotNil(t, alias.Type)
		assert.Equal(t, "Microsoft.Subscription/aliases", *alias.Type)
		require.NotNil(t, alias.Properties, "refresh %d: alias properties", attempt)
		require.NotNil(t, alias.Properties.SubscriptionID,
			"refresh %d: the alias must report its subscription id — the provider reads the subscription through it", attempt)
		assert.Equal(t, newSubID, *alias.Properties.SubscriptionID)
		require.NotNil(t, alias.Properties.ProvisioningState)
		assert.Equal(t, armsubscription.ProvisioningStateSucceeded, *alias.Properties.ProvisioningState)
		require.NotNil(t, alias.Properties.Workload)
		assert.Equal(t, armsubscription.WorkloadProduction, *alias.Properties.Workload)
		require.NotNil(t, alias.Properties.BillingScope)
		assert.Equal(t, billingScope, *alias.Properties.BillingScope)

		sub, err := subsClient.Get(ctx, *alias.Properties.SubscriptionID, nil)
		require.NoError(t, err, "refresh %d: Subscriptions_Get", attempt)
		require.NotNil(t, sub.ID)
		assert.Equal(t, "/subscriptions/"+newSubID, *sub.ID)
		require.NotNil(t, sub.SubscriptionID)
		assert.Equal(t, newSubID, *sub.SubscriptionID)
		require.NotNil(t, sub.DisplayName,
			"refresh %d: the subscription must report its display name — the provider stores it as subscription_name", attempt)
		assert.Equal(t, "SDK Refresh Subscription", *sub.DisplayName)
		require.NotNil(t, sub.TenantID,
			"refresh %d: the subscription must report its tenant id — the provider stores it as tenant_id", attempt)
		assert.Equal(t, simTenantID, *sub.TenantID)
		require.NotNil(t, sub.State)
		assert.Equal(t, armsubscriptions.SubscriptionStateEnabled, *sub.State)
	}
}

// TestSubscriptionAlias_AdoptExistingSubscription creates an alias for an
// already-existing subscription id — the second creation mode the alias API
// supports (properties.subscriptionId instead of a billing scope).
func TestSubscriptionAlias_AdoptExistingSubscription(t *testing.T) {
	aliasClient, err := armsubscription.NewAliasClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)

	const aliasName = "sdk-test-sub-alias-adopted"
	const adoptedSubID = "00000000-0000-0000-0000-00000000ad07"
	poller, err := aliasClient.BeginCreate(ctx, aliasName, armsubscription.PutAliasRequest{
		Properties: &armsubscription.PutAliasRequestProperties{
			SubscriptionID: to.Ptr(adoptedSubID),
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	assert.Equal(t, adoptedSubID, *created.Properties.SubscriptionID)
	assert.Equal(t, armsubscription.ProvisioningStateSucceeded, *created.Properties.ProvisioningState)

	_, err = aliasClient.Delete(ctx, aliasName, nil)
	require.NoError(t, err)
}

// TestSubscriptionAlias_RequiresBillingScopeOrSubscriptionID asserts the
// documented request contract: an alias PUT must carry either a billing
// scope (creation) or a subscription id (adoption).
func TestSubscriptionAlias_RequiresBillingScopeOrSubscriptionID(t *testing.T) {
	aliasClient, err := armsubscription.NewAliasClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)

	_, err = aliasClient.BeginCreate(ctx, "sdk-test-sub-alias-invalid", armsubscription.PutAliasRequest{
		Properties: &armsubscription.PutAliasRequestProperties{
			DisplayName: to.Ptr("No Billing Scope"),
		},
	}, nil)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "alias PUT without billingScope or subscriptionId must fail, got %v", err)
	assert.Equal(t, 400, respErr.StatusCode)
}
