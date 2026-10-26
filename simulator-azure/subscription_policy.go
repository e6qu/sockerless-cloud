package main

// Microsoft.Subscription 2021-10-01 policies: the tenant policy governing
// whether subscriptions cross a tenant boundary, and a billing account's
// transfer policy. The tenant policy is a "default" singleton under the
// caller's tenant: PUT replaces, GET reads, list returns it alone. Its blocks
// are enforced — blockSubscriptionsLeavingTenant denies an alias directing a
// subscription elsewhere, blockSubscriptionsIntoTenant denies an ownership
// acceptance bringing one in — and exemptedPrincipals lifts both.

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// TenantPolicy, spec field spelling kept, keyed by tenant.
type SubscriptionTenantPolicyRecord struct {
	TenantID                        string
	PolicyID                        string
	BlockSubscriptionsLeavingTenant bool
	BlockSubscriptionsIntoTenant    bool
	ExemptedPrincipals              []string
}

// The contract declares only the generic ErrorResponseBody, so a denial's error
// code is the policy member that produced it.
const (
	policyBlockSubscriptionsLeavingTenant = "blockSubscriptionsLeavingTenant"
	policyBlockSubscriptionsIntoTenant    = "blockSubscriptionsIntoTenant"
)

// allowTransfers is changed through Microsoft.Billing, outside this slice, so
// report what an unrestricted billing account reports.
const billingAccountAllowTransfers = true

var azureSubscriptionTenantPolicies sim.Store[SubscriptionTenantPolicyRecord]

func registerSubscriptionPolicy(srv *sim.Server) {
	azureSubscriptionTenantPolicies = sim.MakeStore[SubscriptionTenantPolicyRecord](srv.DB(), "azure_subscription_tenant_policies")

	// The body is the bare PutTenantPolicyRequestProperties, and it replaces the
	// policy rather than merging.
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

	srv.HandleFunc("GET /providers/Microsoft.Subscription/policies/default", func(w http.ResponseWriter, r *http.Request) {
		tenant, _, err := azureARMCallerPrincipal(r)
		if err != nil {
			subscriptionRPError(w, "InvalidAuthenticationToken", err.Error(), http.StatusUnauthorized)
			return
		}
		sim.WriteJSON(w, http.StatusOK, subscriptionTenantPolicyResponse(azureSubscriptionTenantPolicy(tenant)))
	})

	// A tenant holds exactly one policy, so the list is that policy alone.
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

	srv.HandleFunc("GET /providers/Microsoft.Billing/billingAccounts/{billingAccountId}/providers/Microsoft.Subscription/policies/default",
		func(w http.ResponseWriter, r *http.Request) {
			account := sim.PathParam(r, "billingAccountId")
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				// The policy's ID is constant; `name` carries the account.
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

// Every tenant has a policy, permissive by default, whose ID the service
// assigns once; materializing it on first read keeps the ID stable across
// reads and restarts.
func azureSubscriptionTenantPolicy(tenantID string) SubscriptionTenantPolicyRecord {
	if rec, ok := azureSubscriptionTenantPolicies.Get(tenantID); ok {
		return rec
	}
	rec := SubscriptionTenantPolicyRecord{
		TenantID:           tenantID,
		PolicyID:           sim.NewUUID(),
		ExemptedPrincipals: []string{},
	}
	azureSubscriptionTenantPolicies.Put(tenantID, rec)
	return rec
}

// exemptedPrincipals are subject to neither block.
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
		// Relative, with the type carrying the providers/ prefix, as the
		// specification spells them.
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

// The distinct tenants holding subscriptions created under the account, read
// from the aliases naming it; an account that funded none serves none.
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

// Every documented scope spelling (invoice section, customer, enrollment
// account) names the account after "billingAccounts".
func subscriptionBillingAccountID(billingScope string) string {
	segs := strings.Split(strings.Trim(billingScope, "/"), "/")
	for i, seg := range segs {
		if strings.EqualFold(seg, "billingAccounts") && i+1 < len(segs) {
			return segs[i+1]
		}
	}
	return ""
}

// The resource provider authorizes against the caller: its tenant owns the
// policy and its object ID is what exemptedPrincipals names. These are claims
// the bearer verification already validated.
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
