package main

import (
	"net/http"
	"testing"
)

// These set a policy and drive the operations it governs, so a policy that
// stopped constraining fails.

const subscriptionForeignTenantID = "22222222-2222-2222-2222-222222222222"

func (c *subscriptionTestClient) putTenantPolicy(blockLeaving, blockInto bool, exempted []string) map[string]any {
	c.t.Helper()
	if exempted == nil {
		exempted = []string{}
	}
	return c.doJSON(http.MethodPut, "/providers/Microsoft.Subscription/policies/default", map[string]any{
		"blockSubscriptionsLeavingTenant": blockLeaving,
		"blockSubscriptionsIntoTenant":    blockInto,
		"exemptedPrincipals":              exempted,
	}, http.StatusOK)
}

// Hands a new subscription to a tenant and owner, the shape both blocks govern.
func directedAliasRequest(displayName, tenantID string) map[string]any {
	return map[string]any{
		"properties": map[string]any{
			"displayName":  displayName,
			"workload":     "Production",
			"billingScope": "/providers/Microsoft.Billing/billingAccounts/sim-billing-account/enrollmentAccounts/sim-enrollment",
			"additionalProperties": map[string]any{
				"subscriptionTenantId": tenantID,
				"subscriptionOwnerId":  entraDefaultUser.OID,
			},
		},
	}
}

// The policy exists with a stable ID before any write, a write replaces it,
// and list and get agree.
func TestSubscriptionTenantPolicyRoundTrip(t *testing.T) {
	c := newSubscriptionTestClient(t)

	initial := c.doJSON(http.MethodGet, "/providers/Microsoft.Subscription/policies/default", nil, http.StatusOK)
	if initial["name"] != "default" {
		t.Fatalf("tenant policy name = %v, want %q", initial["name"], "default")
	}
	props := subscriptionProps(t, initial)
	policyID, _ := props["policyId"].(string)
	if policyID == "" {
		t.Fatal("a tenant policy must carry a policyId")
	}
	if props["blockSubscriptionsLeavingTenant"] != false || props["blockSubscriptionsIntoTenant"] != false {
		t.Fatalf("an unwritten tenant policy blocks nothing, got %#v", props)
	}

	written := c.putTenantPolicy(true, true, []string{entraDefaultUser.OID})
	props = subscriptionProps(t, written)
	if props["policyId"] != policyID {
		t.Errorf("the policy identifier must be stable across writes: %v then %v", policyID, props["policyId"])
	}
	if props["blockSubscriptionsLeavingTenant"] != true || props["blockSubscriptionsIntoTenant"] != true {
		t.Fatalf("the written policy was not read back: %#v", props)
	}

	read := c.doJSON(http.MethodGet, "/providers/Microsoft.Subscription/policies/default", nil, http.StatusOK)
	if subscriptionProps(t, read)["policyId"] != policyID {
		t.Errorf("get reported a different policy than put returned")
	}

	list := c.doJSON(http.MethodGet, "/providers/Microsoft.Subscription/policies", nil, http.StatusOK)
	value, ok := list["value"].([]any)
	if !ok || len(value) != 1 {
		t.Fatalf("a tenant holds exactly one subscription policy, got %#v", list["value"])
	}
	listed, _ := value[0].(map[string]any)
	if subscriptionProps(t, listed)["policyId"] != policyID {
		t.Errorf("the listed policy is not the tenant's policy")
	}

	// A write replaces rather than merges: an exemption a later write omits is
	// gone.
	cleared := c.putTenantPolicy(false, false, nil)
	props = subscriptionProps(t, cleared)
	exempted, ok := props["exemptedPrincipals"].([]any)
	if !ok || len(exempted) != 0 {
		t.Fatalf("exemptedPrincipals after a write that omitted them = %#v", props["exemptedPrincipals"])
	}
}

// Denied while the block is set; allowed for an exempted principal.
func TestSubscriptionPolicyBlocksSubscriptionsLeavingTenant(t *testing.T) {
	c := newSubscriptionTestClient(t)
	c.putTenantPolicy(true, false, nil)

	blocked := c.do(http.MethodPut, "/providers/Microsoft.Subscription/aliases/leaving-blocked",
		directedAliasRequest("Leaving Subscription", subscriptionForeignTenantID))
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("directing a subscription out of the tenant while the block is set: status %d, want 403: %s",
			blocked.Code, blocked.Body.String())
	}

	// Directed at the caller's own tenant, the subscription is not leaving.
	staying := c.do(http.MethodPut, "/providers/Microsoft.Subscription/aliases/leaving-same-tenant",
		directedAliasRequest("Same Tenant Subscription", simTenantID))
	if staying.Code != http.StatusCreated {
		t.Fatalf("directing a subscription within the tenant: status %d, want 201: %s", staying.Code, staying.Body.String())
	}

	c.putTenantPolicy(true, false, []string{entraDefaultUser.OID})
	exempted := c.do(http.MethodPut, "/providers/Microsoft.Subscription/aliases/leaving-exempted",
		directedAliasRequest("Leaving Subscription", subscriptionForeignTenantID))
	if exempted.Code != http.StatusCreated {
		t.Fatalf("an exempted principal is not blocked: status %d, want 201: %s", exempted.Code, exempted.Body.String())
	}
}

// Accepting ownership brings the subscription into the accepting principal's
// tenant.
func TestSubscriptionPolicyBlocksSubscriptionsIntoTenant(t *testing.T) {
	c := newSubscriptionTestClient(t)
	subscriptionID, _ := c.createDirectedAlias("into-tenant-alias", "Directed Subscription", simTenantID)
	acceptPath := "/providers/Microsoft.Subscription/subscriptions/" + subscriptionID + "/acceptOwnership"
	accept := map[string]any{"properties": map[string]any{"displayName": "Accepted Subscription"}}

	c.putTenantPolicy(false, true, nil)
	blocked := c.do(http.MethodPost, acceptPath, accept)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("accepting ownership while the block is set: status %d, want 403: %s", blocked.Code, blocked.Body.String())
	}

	c.putTenantPolicy(false, true, []string{entraDefaultUser.OID})
	exempted := c.do(http.MethodPost, acceptPath, accept)
	if exempted.Code != http.StatusAccepted {
		t.Fatalf("an exempted principal is not blocked: status %d, want 202: %s", exempted.Code, exempted.Body.String())
	}
}

// The tenants holding subscriptions created under the account, not a fixed
// list.
func TestSubscriptionBillingAccountPolicyServiceTenants(t *testing.T) {
	c := newSubscriptionTestClient(t)
	const policyPath = "/providers/Microsoft.Billing/billingAccounts/sim-billing-account/providers/Microsoft.Subscription/policies/default"

	empty := c.doJSON(http.MethodGet, policyPath, nil, http.StatusOK)
	if empty["name"] != "sim-billing-account" {
		t.Fatalf("billing account policy name = %v, want the billing account id", empty["name"])
	}
	props := subscriptionProps(t, empty)
	if tenants, ok := props["serviceTenants"].([]any); !ok || len(tenants) != 0 {
		t.Fatalf("a billing account that has funded no subscription serves no tenant, got %#v", props["serviceTenants"])
	}
	if props["allowTransfers"] != true {
		t.Fatalf("allowTransfers = %v, want true", props["allowTransfers"])
	}

	c.createDirectedAlias("billing-account-alias", "Directed Subscription", simTenantID)

	served := c.doJSON(http.MethodGet, policyPath, nil, http.StatusOK)
	props = subscriptionProps(t, served)
	tenants, ok := props["serviceTenants"].([]any)
	if !ok || len(tenants) != 1 {
		t.Fatalf("serviceTenants = %#v, want the one tenant the billing account now serves", props["serviceTenants"])
	}
	tenant, _ := tenants[0].(map[string]any)
	if tenant["tenantId"] != simTenantID {
		t.Errorf("serviceTenants tenantId = %v, want %q", tenant["tenantId"], simTenantID)
	}
	if tenant["tenantName"] != azureTenantDisplayName(simTenantID) {
		t.Errorf("serviceTenants tenantName = %v, want %q", tenant["tenantName"], azureTenantDisplayName(simTenantID))
	}

	// A billing account no subscription named still answers, with no tenants.
	other := c.doJSON(http.MethodGet,
		"/providers/Microsoft.Billing/billingAccounts/other-billing-account/providers/Microsoft.Subscription/policies/default",
		nil, http.StatusOK)
	if tenants, ok := subscriptionProps(t, other)["serviceTenants"].([]any); !ok || len(tenants) != 0 {
		t.Fatalf("an unrelated billing account serves no tenant, got %#v", tenants)
	}
}

func TestSubscriptionOperationsListCatalog(t *testing.T) {
	c := newSubscriptionTestClient(t)
	list := c.doJSON(http.MethodGet, "/providers/Microsoft.Subscription/operations", nil, http.StatusOK)
	value, ok := list["value"].([]any)
	if !ok || len(value) != len(subscriptionOperationCatalog) {
		t.Fatalf("operations list carried %v entries, want %d", list["value"], len(subscriptionOperationCatalog))
	}
	seen := map[string]bool{}
	for _, raw := range value {
		entry, _ := raw.(map[string]any)
		name, _ := entry["name"].(string)
		if name == "" {
			t.Fatalf("operation entry without a name: %#v", entry)
		}
		seen[name] = true
		if entry["isDataAction"] != false {
			t.Errorf("%s: isDataAction = %v, want false — Microsoft.Subscription publishes no data plane", name, entry["isDataAction"])
		}
		display, ok := entry["display"].(map[string]any)
		if !ok {
			t.Fatalf("%s: operation entry without a display object", name)
		}
		if display["provider"] != subscriptionProviderName {
			t.Errorf("%s: display.provider = %v, want %q", name, display["provider"], subscriptionProviderName)
		}
		if display["resource"] == "" || display["operation"] == "" {
			t.Errorf("%s: display = %#v", name, display)
		}
	}
	for _, op := range subscriptionOperationCatalog {
		if !seen[op.Name] {
			t.Errorf("operations list omitted %q", op.Name)
		}
	}
}
