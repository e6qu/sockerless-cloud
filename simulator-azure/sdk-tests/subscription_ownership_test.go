package azure_sdk_test

// Microsoft.Subscription ownership, policy and operation-catalog surface
// (2021-10-01) through the official armsubscription SDK module. Routes
// exercised:
//
//	POST /providers/Microsoft.Subscription/subscriptions/{subscriptionId}/acceptOwnership
//	GET /providers/Microsoft.Subscription/subscriptions/{subscriptionId}/acceptOwnershipStatus
//	GET /providers/Microsoft.Subscription/subscriptionOperations/{operationId}
//	PUT /providers/Microsoft.Subscription/policies/default
//	GET /providers/Microsoft.Subscription/policies/default
//	GET /providers/Microsoft.Subscription/policies
//	GET /providers/Microsoft.Subscription/operations
//	GET /providers/Microsoft.Billing/billingAccounts/{billingAccountId}/providers/Microsoft.Subscription/policies/default
//
// The subscriptionOperations route has no client method of its own: it is the
// Location target of the ownership-acceptance long-running operation, so the
// SDK reaches it exactly the way a real client does — through the poller
// BeginAcceptOwnership returns.

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/subscription/armsubscription"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// simCallerObjectID is the object id of the principal the SDK tests
// authenticate as, read from the access token the simulator minted for them.
// The tenant policy's exemptedPrincipals names object ids, so a test that
// exempts "the caller" has to know which principal that is — the same way a
// real administrator reads it off the identity they are granting.
func simCallerObjectID(t *testing.T) string {
	t.Helper()
	token, _, err := fetchSimAccessToken("https://management.azure.com/.default")
	require.NoError(t, err)
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3, "an access token is a three-part JWT")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims struct {
		OID string `json:"oid"`
	}
	require.NoError(t, json.Unmarshal(payload, &claims))
	require.NotEmpty(t, claims.OID, "the access token must name the principal it was issued to")
	return claims.OID
}

// resetTenantPolicy restores the permissive tenant policy. Every test that
// tightens the policy registers this so the tenant it shares with the rest of
// the suite goes back to blocking nothing.
func resetTenantPolicy(t *testing.T, client *armsubscription.PolicyClient) {
	t.Helper()
	_, err := client.AddUpdatePolicyForTenant(ctx, armsubscription.PutTenantPolicyRequestProperties{
		BlockSubscriptionsLeavingTenant: to.Ptr(false),
		BlockSubscriptionsIntoTenant:    to.Ptr(false),
		ExemptedPrincipals:              []*string{},
	}, nil)
	require.NoError(t, err)
}

// createDirectedAlias creates a subscription directed at a tenant and an owner
// and waits for the alias to finish provisioning, returning the settled alias.
func createDirectedAlias(t *testing.T, client *armsubscription.AliasClient, aliasName, displayName, tenantID, billingScope string) armsubscription.AliasResponse {
	t.Helper()
	poller, err := client.BeginCreate(ctx, aliasName, armsubscription.PutAliasRequest{
		Properties: &armsubscription.PutAliasRequestProperties{
			DisplayName:  to.Ptr(displayName),
			Workload:     to.Ptr(armsubscription.WorkloadProduction),
			BillingScope: to.Ptr(billingScope),
			AdditionalProperties: &armsubscription.PutAliasRequestAdditionalProperties{
				SubscriptionTenantID: to.Ptr(tenantID),
				SubscriptionOwnerID:  to.Ptr("f09b39eb-c496-482c-9ab9-afd799572f4c"),
				Tags:                 map[string]*string{"purpose": to.Ptr("ownership")},
			},
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := client.Delete(ctx, aliasName, nil)
		require.NoError(t, err)
	})
	return created.AliasResponse
}

// TestSubscriptionOwnershipAcceptance drives the whole ownership handover the
// way a client does: a directed alias leaves the new subscription's ownership
// Pending with the URL to accept it at, BeginAcceptOwnership starts the
// long-running operation and its poller follows the Location the simulator
// returned, and the settled state is visible both on the ownership status and
// on the alias the provider reads.
func TestSubscriptionOwnershipAcceptance(t *testing.T) {
	aliasClient, err := armsubscription.NewAliasClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subClient, err := armsubscription.NewClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)

	const aliasName = "sdk-test-ownership-alias"
	const billingScope = "/providers/Microsoft.Billing/billingAccounts/sdk-ownership-billing-account/enrollmentAccounts/sim-enrollment"
	alias := createDirectedAlias(t, aliasClient, aliasName, "SDK Directed Subscription", simTenantID, billingScope)

	require.NotNil(t, alias.Properties)
	require.NotNil(t, alias.Properties.SubscriptionID)
	newSubID := *alias.Properties.SubscriptionID
	require.NotNil(t, alias.Properties.AcceptOwnershipState)
	assert.Equal(t, armsubscription.AcceptOwnershipPending, *alias.Properties.AcceptOwnershipState,
		"a subscription directed at an owner stays Pending until that owner accepts it")
	require.NotNil(t, alias.Properties.AcceptOwnershipURL)
	assert.Equal(t, "/providers/Microsoft.Subscription/subscriptions/"+newSubID+"/acceptOwnership",
		*alias.Properties.AcceptOwnershipURL)
	require.NotNil(t, alias.Properties.SubscriptionOwnerID)
	assert.Equal(t, "f09b39eb-c496-482c-9ab9-afd799572f4c", *alias.Properties.SubscriptionOwnerID)

	status, err := subClient.AcceptOwnershipStatus(ctx, newSubID, nil)
	require.NoError(t, err)
	require.NotNil(t, status.AcceptOwnershipState)
	assert.Equal(t, armsubscription.AcceptOwnershipPending, *status.AcceptOwnershipState)
	require.NotNil(t, status.SubscriptionID)
	assert.Equal(t, newSubID, *status.SubscriptionID)
	require.NotNil(t, status.SubscriptionTenantID)
	assert.Equal(t, simTenantID, *status.SubscriptionTenantID)
	require.NotNil(t, status.DisplayName)
	assert.Equal(t, "SDK Directed Subscription", *status.DisplayName)

	poller, err := subClient.BeginAcceptOwnership(ctx, newSubID, armsubscription.AcceptOwnershipRequest{
		Properties: &armsubscription.AcceptOwnershipRequestProperties{
			DisplayName: to.Ptr("SDK Accepted Subscription"),
			Tags:        map[string]*string{"owner": to.Ptr("accepted")},
		},
	}, nil)
	require.NoError(t, err)
	// PollUntilDone follows the Location the 202 carried — the
	// Microsoft.Subscription operation — until it reports the acceptance done.
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	status, err = subClient.AcceptOwnershipStatus(ctx, newSubID, nil)
	require.NoError(t, err)
	require.NotNil(t, status.AcceptOwnershipState)
	assert.Equal(t, armsubscription.AcceptOwnershipCompleted, *status.AcceptOwnershipState,
		"the poller returned, so the acceptance has settled")
	require.NotNil(t, status.DisplayName)
	assert.Equal(t, "SDK Accepted Subscription", *status.DisplayName,
		"an accepted subscription takes the name its new owner gave it")
	require.NotNil(t, status.Tags["owner"])
	assert.Equal(t, "accepted", *status.Tags["owner"])

	settled, err := aliasClient.Get(ctx, aliasName, nil)
	require.NoError(t, err)
	require.NotNil(t, settled.Properties)
	require.NotNil(t, settled.Properties.AcceptOwnershipState)
	assert.Equal(t, armsubscription.AcceptOwnershipCompleted, *settled.Properties.AcceptOwnershipState,
		"the alias the provider reads must report the completed handover")
	assert.Nil(t, settled.Properties.AcceptOwnershipURL,
		"an accepted subscription no longer advertises an acceptOwnershipUrl")

	// Accepting an ownership request that has already settled is a conflict.
	_, err = subClient.BeginAcceptOwnership(ctx, newSubID, armsubscription.AcceptOwnershipRequest{
		Properties: &armsubscription.AcceptOwnershipRequestProperties{
			DisplayName: to.Ptr("SDK Accepted Subscription"),
		},
	}, nil)
	var respErr *azcore.ResponseError
	require.ErrorAs(t, err, &respErr)
	assert.Equal(t, 409, respErr.StatusCode)
}

// TestSubscriptionAcceptOwnershipStatusUnknown asserts the ownership status of
// a subscription that never had an ownership request: there is nothing to
// report, and the service says so rather than inventing a state.
func TestSubscriptionAcceptOwnershipStatusUnknown(t *testing.T) {
	subClient, err := armsubscription.NewClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = subClient.AcceptOwnershipStatus(ctx, "00000000-0000-0000-0000-0000000d0e5f", nil)
	var respErr *azcore.ResponseError
	require.ErrorAs(t, err, &respErr)
	assert.Equal(t, 404, respErr.StatusCode)
}

// TestSubscriptionTenantPolicyLifecycle drives the tenant policy singleton
// through its client: read the default, replace it, and read it back through
// both the get and the list operation.
func TestSubscriptionTenantPolicyLifecycle(t *testing.T) {
	policyClient, err := armsubscription.NewPolicyClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	t.Cleanup(func() { resetTenantPolicy(t, policyClient) })

	initial, err := policyClient.GetPolicyForTenant(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, initial.Name)
	assert.Equal(t, "default", *initial.Name)
	require.NotNil(t, initial.Properties)
	require.NotNil(t, initial.Properties.PolicyID)
	policyID := *initial.Properties.PolicyID
	assert.NotEmpty(t, policyID)

	exempted := simCallerObjectID(t)
	updated, err := policyClient.AddUpdatePolicyForTenant(ctx, armsubscription.PutTenantPolicyRequestProperties{
		BlockSubscriptionsLeavingTenant: to.Ptr(true),
		BlockSubscriptionsIntoTenant:    to.Ptr(true),
		ExemptedPrincipals:              []*string{to.Ptr(exempted)},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.Properties)
	require.NotNil(t, updated.Properties.BlockSubscriptionsLeavingTenant)
	assert.True(t, *updated.Properties.BlockSubscriptionsLeavingTenant)
	require.NotNil(t, updated.Properties.BlockSubscriptionsIntoTenant)
	assert.True(t, *updated.Properties.BlockSubscriptionsIntoTenant)
	require.Len(t, updated.Properties.ExemptedPrincipals, 1)
	assert.Equal(t, exempted, *updated.Properties.ExemptedPrincipals[0])
	require.NotNil(t, updated.Properties.PolicyID)
	assert.Equal(t, policyID, *updated.Properties.PolicyID, "a tenant keeps one policy identifier")

	read, err := policyClient.GetPolicyForTenant(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, read.Properties)
	require.NotNil(t, read.Properties.BlockSubscriptionsLeavingTenant)
	assert.True(t, *read.Properties.BlockSubscriptionsLeavingTenant)

	pager := policyClient.NewListPolicyForTenantPager(nil)
	var listed []*armsubscription.GetTenantPolicyResponse
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		listed = append(listed, page.Value...)
	}
	require.Len(t, listed, 1, "a tenant holds exactly one subscription policy")
	require.NotNil(t, listed[0].Properties)
	require.NotNil(t, listed[0].Properties.PolicyID)
	assert.Equal(t, policyID, *listed[0].Properties.PolicyID)
}

// TestSubscriptionPolicyBlocksTenantTransfer proves the tenant policy
// constrains behaviour rather than merely being stored: with
// blockSubscriptionsLeavingTenant set, an alias that hands a subscription to
// another tenant is refused, and the same request from a principal the policy
// exempts succeeds.
func TestSubscriptionPolicyBlocksTenantTransfer(t *testing.T) {
	policyClient, err := armsubscription.NewPolicyClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	aliasClient, err := armsubscription.NewAliasClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	t.Cleanup(func() { resetTenantPolicy(t, policyClient) })

	const foreignTenantID = "22222222-2222-2222-2222-222222222222"
	const billingScope = "/providers/Microsoft.Billing/billingAccounts/sdk-transfer-billing-account/enrollmentAccounts/sim-enrollment"
	request := armsubscription.PutAliasRequest{
		Properties: &armsubscription.PutAliasRequestProperties{
			DisplayName:  to.Ptr("SDK Transferred Subscription"),
			Workload:     to.Ptr(armsubscription.WorkloadProduction),
			BillingScope: to.Ptr(billingScope),
			AdditionalProperties: &armsubscription.PutAliasRequestAdditionalProperties{
				SubscriptionTenantID: to.Ptr(foreignTenantID),
				SubscriptionOwnerID:  to.Ptr("f09b39eb-c496-482c-9ab9-afd799572f4c"),
			},
		},
	}

	_, err = policyClient.AddUpdatePolicyForTenant(ctx, armsubscription.PutTenantPolicyRequestProperties{
		BlockSubscriptionsLeavingTenant: to.Ptr(true),
	}, nil)
	require.NoError(t, err)

	_, err = aliasClient.BeginCreate(ctx, "sdk-test-transfer-blocked", request, nil)
	var respErr *azcore.ResponseError
	require.ErrorAs(t, err, &respErr, "the tenant policy must refuse the transfer")
	assert.Equal(t, 403, respErr.StatusCode)
	assert.Equal(t, "blockSubscriptionsLeavingTenant", respErr.ErrorCode,
		"the denial names the policy member that produced it")

	_, err = policyClient.AddUpdatePolicyForTenant(ctx, armsubscription.PutTenantPolicyRequestProperties{
		BlockSubscriptionsLeavingTenant: to.Ptr(true),
		ExemptedPrincipals:              []*string{to.Ptr(simCallerObjectID(t))},
	}, nil)
	require.NoError(t, err)

	const aliasName = "sdk-test-transfer-exempted"
	poller, err := aliasClient.BeginCreate(ctx, aliasName, request, nil)
	require.NoError(t, err, "an exempted principal is not blocked")
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := aliasClient.Delete(ctx, aliasName, nil)
		require.NoError(t, err)
	})
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.AcceptOwnershipState)
	assert.Equal(t, armsubscription.AcceptOwnershipPending, *created.Properties.AcceptOwnershipState)
}

// TestSubscriptionBillingAccountPolicy reads a billing account's policy and
// asserts it reports the tenants the account actually serves — the tenants
// holding the subscriptions created under it.
func TestSubscriptionBillingAccountPolicy(t *testing.T) {
	billingClient, err := armsubscription.NewBillingAccountClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	aliasClient, err := armsubscription.NewAliasClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)

	const billingAccountID = "sdk-billing-account-policy"
	const billingScope = "/providers/Microsoft.Billing/billingAccounts/" + billingAccountID + "/enrollmentAccounts/sim-enrollment"

	empty, err := billingClient.GetPolicy(ctx, billingAccountID, nil)
	require.NoError(t, err)
	require.NotNil(t, empty.Name)
	assert.Equal(t, billingAccountID, *empty.Name)
	require.NotNil(t, empty.Properties)
	require.NotNil(t, empty.Properties.AllowTransfers)
	assert.True(t, *empty.Properties.AllowTransfers)
	assert.Empty(t, empty.Properties.ServiceTenants,
		"a billing account that has funded no subscription serves no tenant")

	createDirectedAlias(t, aliasClient, "sdk-test-billing-account-alias", "SDK Billing Account Subscription", simTenantID, billingScope)

	served, err := billingClient.GetPolicy(ctx, billingAccountID, nil)
	require.NoError(t, err)
	require.NotNil(t, served.Properties)
	require.Len(t, served.Properties.ServiceTenants, 1)
	require.NotNil(t, served.Properties.ServiceTenants[0].TenantID)
	assert.Equal(t, simTenantID, *served.Properties.ServiceTenants[0].TenantID)
	require.NotNil(t, served.Properties.ServiceTenants[0].TenantName)
	assert.NotEmpty(t, *served.Properties.ServiceTenants[0].TenantName)
}

// TestSubscriptionOperationsList reads the resource provider's operation
// catalog — the actions a role assignment can name.
func TestSubscriptionOperationsList(t *testing.T) {
	operationsClient, err := armsubscription.NewOperationsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)

	pager := operationsClient.NewListPager(nil)
	var operations []*armsubscription.Operation
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		operations = append(operations, page.Value...)
	}
	require.NotEmpty(t, operations)

	names := map[string]*armsubscription.Operation{}
	for _, op := range operations {
		require.NotNil(t, op.Name)
		names[*op.Name] = op
	}
	// Every operation this simulator serves must be nameable in a role
	// assignment, so the catalog carries its action.
	for _, action := range []string{
		"Microsoft.Subscription/aliases/read",
		"Microsoft.Subscription/aliases/write",
		"Microsoft.Subscription/aliases/delete",
		"Microsoft.Subscription/cancel/action",
		"Microsoft.Subscription/rename/action",
		"Microsoft.Subscription/enable/action",
		"Microsoft.Subscription/acceptOwnership/action",
		"Microsoft.Subscription/acceptOwnershipStatus/read",
		"Microsoft.Subscription/subscriptionOperations/read",
		"Microsoft.Subscription/policies/read",
		"Microsoft.Subscription/policies/write",
		"Microsoft.Subscription/operations/read",
	} {
		op, ok := names[action]
		require.True(t, ok, "operation catalog omits %q", action)
		require.NotNil(t, op.IsDataAction)
		assert.False(t, *op.IsDataAction, "%s: Microsoft.Subscription publishes no data plane", action)
		require.NotNil(t, op.Display)
		require.NotNil(t, op.Display.Provider)
		assert.Equal(t, "Microsoft Subscription", *op.Display.Provider)
		require.NotNil(t, op.Display.Resource)
		assert.NotEmpty(t, *op.Display.Resource)
		require.NotNil(t, op.Display.Operation)
		assert.NotEmpty(t, *op.Display.Operation)
	}
}
