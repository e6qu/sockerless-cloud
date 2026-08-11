package main

// Microsoft.Subscription (2021-10-01) policy surface — the tenant policy that
// governs whether subscriptions may cross a tenant boundary, and the read-only
// projection of a billing account's transfer policy.
//
// A tenant policy is a singleton named "default" under the tenant of the
// calling principal: PUT replaces it, GET reads it, and the list operation
// returns the same singleton as a one-element page. The two block members are
// enforced, not merely stored — blockSubscriptionsLeavingTenant denies an
// alias creation that directs a subscription at a different tenant, and
// blockSubscriptionsIntoTenant denies the acceptance of ownership that would
// bring one in — and exemptedPrincipals lifts both for the object ids it
// names. Wire shapes mirror the vendored Swagger
// (specs/cloud-api/azure/subscription-arm-subscriptions-2021-10-01).

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// SubscriptionTenantPolicyRecord mirrors the TenantPolicy definition in the
// Microsoft.Subscription 2021-10-01 Swagger (spec field spelling kept), keyed
// by the tenant whose policy it is.
type SubscriptionTenantPolicyRecord struct {
	TenantID                        string
	PolicyID                        string
	BlockSubscriptionsLeavingTenant bool
	BlockSubscriptionsIntoTenant    bool
	ExemptedPrincipals              []string
}

// The tenant-policy members, spelled as the Swagger spells them. A denial
// answers with the member that produced it as its error code: the 2021-10-01
// contract declares only the generic ErrorResponseBody for a failed request,
// so the policy member is the machine-readable discriminator a client has.
const (
	policyBlockSubscriptionsLeavingTenant = "blockSubscriptionsLeavingTenant"
	policyBlockSubscriptionsIntoTenant    = "blockSubscriptionsIntoTenant"
)

// billingAccountAllowTransfers is the transfer disposition the simulator
// reports for a billing account. allowTransfers is a billing-account setting:
// the Microsoft.Subscription 2021-10-01 surface only reads it, and the
// operation that changes it lives in Microsoft.Billing, which is outside this
// slice — so the value projected here is the one a billing account with no
// transfer restriction reports.
const billingAccountAllowTransfers = true

var azureSubscriptionTenantPolicies sim.Store[SubscriptionTenantPolicyRecord]

func registerSubscriptionPolicy(srv *sim.Server) {
	azureSubscriptionTenantPolicies = sim.MakeStore[SubscriptionTenantPolicyRecord](srv.DB(), "azure_subscription_tenant_policies")

	// PUT - SubscriptionPolicy_AddUpdatePolicyForTenant. The body is the bare
	// PutTenantPolicyRequestProperties, not an ARM resource envelope, and it
	// replaces the policy rather than merging into it.
	srv.HandleFunc("PUT /providers/Microsoft.Subscription/policies/default", func(w http.ResponseWriter, r *http.Request) {
		tenant, _, err := azureARMCallerPrincipal(r)
		if err != nil {
			subscriptionRPError(w, "InvalidAuthenticationToken", err.Error(), http.StatusUnauthorized)
			return
		}
		var req struct {
			BlockSubscriptionsLeavingTenant bool     `json:"blockSubscriptionsLeavingTenant"`
			BlockSubscriptionsIntoTenant    bool     `json:"blockSubscriptionsIntoTenant"`
			ExemptedPrincipals              []string `json:"exemptedPrincipals"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			subscriptionRPError(w, "BadRequest", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		policy := azureSubscriptionTenantPolicy(tenant)
		policy.BlockSubscriptionsLeavingTenant = req.BlockSubscriptionsLeavingTenant
		policy.BlockSubscriptionsIntoTenant = req.BlockSubscriptionsIntoTenant
		policy.ExemptedPrincipals = append([]string{}, req.ExemptedPrincipals...)
		azureSubscriptionTenantPolicies.Put(tenant, policy)
		sim.WriteJSON(w, http.StatusOK, subscriptionTenantPolicyResponse(policy))
	})

	// GET - SubscriptionPolicy_GetPolicyForTenant.
	srv.HandleFunc("GET /providers/Microsoft.Subscription/policies/default", func(w http.ResponseWriter, r *http.Request) {
		tenant, _, err := azureARMCallerPrincipal(r)
		if err != nil {
			subscriptionRPError(w, "InvalidAuthenticationToken", err.Error(), http.StatusUnauthorized)
			return
		}
		sim.WriteJSON(w, http.StatusOK, subscriptionTenantPolicyResponse(azureSubscriptionTenantPolicy(tenant)))
	})

	// GET - SubscriptionPolicy_ListPolicyForTenant. A tenant holds exactly one
	// subscription policy, so the pageable list is that policy alone.
	srv.HandleFunc("GET /providers/Microsoft.Subscription/policies", func(w http.ResponseWriter, r *http.Request) {
		tenant, _, err := azureARMCallerPrincipal(r)
		if err != nil {
			subscriptionRPError(w, "InvalidAuthenticationToken", err.Error(), http.StatusUnauthorized)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": []map[string]any{subscriptionTenantPolicyResponse(azureSubscriptionTenantPolicy(tenant))},
		})
	})

	// GET - BillingAccount_GetPolicy.
	srv.HandleFunc("GET /providers/Microsoft.Billing/billingAccounts/{billingAccountId}/providers/Microsoft.Subscription/policies/default",
		func(w http.ResponseWriter, r *http.Request) {
			account := sim.PathParam(r, "billingAccountId")
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				// The identifier of a billing account's policy is a constant —
				// the policy is the billing account's one policy, and the
				// account it belongs to is carried by `name`.
				"id":   "/providers/Microsoft.Subscription/Policies/policyForBillingAccount",
				"name": account,
				"type": "Microsoft.Subscription/policies",
				"properties": map[string]any{
					"serviceTenants": subscriptionBillingAccountServiceTenants(account),
					"allowTransfers": billingAccountAllowTransfers,
				},
			})
		})
}

// azureSubscriptionTenantPolicy returns a tenant's subscription policy. Every
// tenant has one: absent an explicit PUT it is the permissive default, and the
// service assigns it an identifier once and keeps it. Materializing that
// default on first read is what makes the identifier stable across reads and
// across simulator restarts.
func azureSubscriptionTenantPolicy(tenantID string) SubscriptionTenantPolicyRecord {
	if rec, ok := azureSubscriptionTenantPolicies.Get(tenantID); ok {
		return rec
	}
	rec := SubscriptionTenantPolicyRecord{
		TenantID:           tenantID,
		PolicyID:           generateUUID(),
		ExemptedPrincipals: []string{},
	}
	azureSubscriptionTenantPolicies.Put(tenantID, rec)
	return rec
}

// azureSubscriptionPolicyDenies reports whether a tenant's subscription policy
// blocks the requested tenant-boundary crossing for the calling principal.
// A principal named in exemptedPrincipals is subject to neither block.
func azureSubscriptionPolicyDenies(tenantID, callerObjectID, member string) bool {
	policy := azureSubscriptionTenantPolicy(tenantID)
	var blocked bool
	switch member {
	case policyBlockSubscriptionsLeavingTenant:
		blocked = policy.BlockSubscriptionsLeavingTenant
	case policyBlockSubscriptionsIntoTenant:
		blocked = policy.BlockSubscriptionsIntoTenant
	}
	if !blocked {
		return false
	}
	for _, principal := range policy.ExemptedPrincipals {
		if callerObjectID != "" && strings.EqualFold(principal, callerObjectID) {
			return false
		}
	}
	return true
}

func subscriptionTenantPolicyResponse(policy SubscriptionTenantPolicyRecord) map[string]any {
	exempted := policy.ExemptedPrincipals
	if exempted == nil {
		exempted = []string{}
	}
	return map[string]any{
		// The identifier and type of a tenant policy are spelled the way the
		// specification spells them — relative, and with the type carrying the
		// providers/ prefix.
		"id":   "providers/Microsoft.Subscription/policies/default",
		"name": "default",
		"type": "providers/Microsoft.Subscription/policies",
		"properties": map[string]any{
			"policyId":                        policy.PolicyID,
			"blockSubscriptionsLeavingTenant": policy.BlockSubscriptionsLeavingTenant,
			"blockSubscriptionsIntoTenant":    policy.BlockSubscriptionsIntoTenant,
			"exemptedPrincipals":              exempted,
		},
	}
}

// subscriptionBillingAccountServiceTenants reports the tenants a billing
// account serves: the distinct tenants that hold subscriptions created under
// it, read back from the alias records that named the account in their billing
// scope. A billing account that has funded no subscription yet serves no
// tenant, and says so with an empty list.
func subscriptionBillingAccountServiceTenants(billingAccountID string) []map[string]any {
	seen := map[string]bool{}
	var tenants []string
	for _, alias := range azureSubscriptionAliases.List() {
		if !strings.EqualFold(subscriptionBillingAccountID(alias.BillingScope), billingAccountID) {
			continue
		}
		if alias.SubscriptionTenantID == "" || seen[alias.SubscriptionTenantID] {
			continue
		}
		seen[alias.SubscriptionTenantID] = true
		tenants = append(tenants, alias.SubscriptionTenantID)
	}
	sort.Strings(tenants)
	out := make([]map[string]any, 0, len(tenants))
	for _, tenant := range tenants {
		entry := map[string]any{"tenantId": tenant}
		if name := azureTenantDisplayName(tenant); name != "" {
			entry["tenantName"] = name
		}
		out = append(out, entry)
	}
	return out
}

// subscriptionBillingAccountID extracts the billing account from a billing
// scope. Every documented scope spelling — invoice section, customer, and
// enrollment account — names the account in the segment after
// "billingAccounts".
func subscriptionBillingAccountID(billingScope string) string {
	segs := strings.Split(strings.Trim(billingScope, "/"), "/")
	for i, seg := range segs {
		if strings.EqualFold(seg, "billingAccounts") && i+1 < len(segs) {
			return segs[i+1]
		}
	}
	return ""
}

// azureARMCallerPrincipal returns the tenant and object id of the principal
// whose Azure Resource Manager bearer authenticates the request. The
// Microsoft.Subscription resource provider authorizes against the caller's own
// identity — its tenant owns the subscription policy, and its object id is
// what exemptedPrincipals names — so the provider reads the same claims the
// bearer verification already validated.
func azureARMCallerPrincipal(r *http.Request) (tenantID, objectID string, err error) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", "", fmt.Errorf("the request did not carry an 'Authorization: Bearer <token>' header")
	}
	claims, err := verifyAzureSimJWT(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")))
	if err != nil {
		return "", "", fmt.Errorf("the access token is not valid: %v", err)
	}
	tenantID, _ = claims["tid"].(string)
	objectID, _ = claims["oid"].(string)
	if tenantID == "" {
		return "", "", fmt.Errorf("the access token carries no 'tid' claim, so it names no tenant")
	}
	return tenantID, objectID, nil
}
