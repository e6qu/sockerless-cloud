package main

// Microsoft.Subscription 2021-10-01 aliases create subscriptions
// programmatically: PUT /providers/Microsoft.Subscription/aliases/{aliasName}
// creates one under a billing scope or adopts an existing ID, completing
// through the alias's own provisioningState (a 201 still "Accepted" sends the
// client to GET it until "Succeeded"). Rename, cancel and enable are the
// subscription-scoped actions the azurerm provider drives on update and
// destroy.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// SubscriptionAliasResponse, spec field spelling kept.
type SubscriptionAliasRecord struct {
	Name                 string
	SubscriptionID       string
	DisplayName          string
	BillingScope         string
	Workload             string
	ProvisioningState    string
	AcceptOwnershipState string
	CreatedTime          string
	ResellerID           string
	ManagementGroupID    string
	SubscriptionOwnerID  string
	SubscriptionTenantID string
	Tags                 map[string]string
}

// For subscriptions the alias API created or the rename, cancel and enable
// actions changed. Others keep the implicit identity ("Simulator
// Subscription", Enabled, the simulator's tenant).
type AzureSubscriptionRecord struct {
	SubscriptionID string
	DisplayName    string
	State          string
	TenantID       string
}

var (
	azureSubscriptionAliases sim.Store[SubscriptionAliasRecord]
	azureSubscriptionRecords sim.Store[AzureSubscriptionRecord]
)

func registerSubscriptionAlias(srv *sim.Server) {
	azureSubscriptionAliases = sim.MakeStore[SubscriptionAliasRecord](srv.DB(), "azure_subscription_aliases")
	azureSubscriptionRecords = sim.MakeStore[AzureSubscriptionRecord](srv.DB(), "azure_subscription_records")

	// Creates under a billing scope or adopts an existing ID; completes through
	// the alias's own provisioningState.
	srv.HandleFunc("PUT /providers/Microsoft.Subscription/aliases/{aliasName}", handleSubscriptionAliasCreate)

	// Also the operation's poll target.
	srv.HandleFunc("GET /providers/Microsoft.Subscription/aliases/{aliasName}", func(w http.ResponseWriter, r *http.Request) {
		name := sim.PathParam(r, "aliasName")
		alias, ok := azureSubscriptionAliases.Get(name)
		if !ok {
			subscriptionRPError(w, "NotFound", "The alias '"+name+"' cannot be found.", http.StatusNotFound)
			return
		}
		sim.WriteJSON(w, http.StatusOK, subscriptionAliasResponse(alias))
	})

	// Removes the alias only; the subscription stays (cancellation is separate).
	srv.HandleFunc("DELETE /providers/Microsoft.Subscription/aliases/{aliasName}", func(w http.ResponseWriter, r *http.Request) {
		name := sim.PathParam(r, "aliasName")
		if !azureSubscriptionAliases.Delete(name) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("GET /providers/Microsoft.Subscription/aliases", func(w http.ResponseWriter, _ *http.Request) {
		aliases := azureSubscriptionAliases.List()
		sort.Slice(aliases, func(i, j int) bool { return aliases[i].Name < aliases[j].Name })
		out := make([]map[string]any, 0, len(aliases))
		for _, alias := range aliases {
			out = append(out, subscriptionAliasResponse(alias))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	// A cancelled subscription is Disabled, not deleted: it stays listed and can
	// be re-enabled.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.Subscription/cancel", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		setAzureSubscriptionState(sub, "Disabled")
		sim.WriteJSON(w, http.StatusOK, map[string]any{"subscriptionId": sub})
	})

	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.Subscription/enable", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		setAzureSubscriptionState(sub, "Enabled")
		sim.WriteJSON(w, http.StatusOK, map[string]any{"subscriptionId": sub})
	})

	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.Subscription/rename", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		var req struct {
			SubscriptionName string `json:"subscriptionName"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			subscriptionRPError(w, "BadRequest", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.SubscriptionName == "" {
			subscriptionRPError(w, "BadRequest", "The request body must carry a non-empty 'subscriptionName'.", http.StatusBadRequest)
			return
		}
		ensureAzureSubscriptionRecord(sub)
		azureSubscriptionRecords.Update(sub, func(rec *AzureSubscriptionRecord) {
			rec.DisplayName = req.SubscriptionName
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{"subscriptionId": sub})
	})
}

func handleSubscriptionAliasCreate(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "aliasName")
	callerTenant, callerObjectID, err := azureARMCallerPrincipal(r)
	if err != nil {
		subscriptionRPError(w, "InvalidAuthenticationToken", err.Error(), http.StatusUnauthorized)
		return
	}
	var req struct {
		Properties *struct {
			DisplayName          string `json:"displayName"`
			Workload             string `json:"workload"`
			BillingScope         string `json:"billingScope"`
			SubscriptionID       string `json:"subscriptionId"`
			ResellerID           string `json:"resellerId"`
			AdditionalProperties *struct {
				ManagementGroupID    string            `json:"managementGroupId"`
				SubscriptionTenantID string            `json:"subscriptionTenantId"`
				SubscriptionOwnerID  string            `json:"subscriptionOwnerId"`
				Tags                 map[string]string `json:"tags"`
			} `json:"additionalProperties"`
		} `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		subscriptionRPError(w, "BadRequest", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if existing, ok := azureSubscriptionAliases.Get(name); ok {
		// PUT on an existing alias is idempotent; retargeting it at a different
		// subscription is a conflict.
		if req.Properties != nil && req.Properties.SubscriptionID != "" && req.Properties.SubscriptionID != existing.SubscriptionID {
			subscriptionRPError(w, "Conflict",
				"The alias '"+name+"' already references subscription '"+existing.SubscriptionID+"'.", http.StatusConflict)
			return
		}
		sim.WriteJSON(w, http.StatusOK, subscriptionAliasResponse(existing))
		return
	}

	if req.Properties == nil || (req.Properties.BillingScope == "" && req.Properties.SubscriptionID == "") {
		subscriptionRPError(w, "BadRequest",
			"Creating a subscription requires properties.billingScope; adopting an existing subscription requires properties.subscriptionId.",
			http.StatusBadRequest)
		return
	}
	workload := req.Properties.Workload
	switch workload {
	case "":
		workload = "Production"
	case "Production", "DevTest":
	default:
		subscriptionRPError(w, "BadRequest",
			"The workload '"+workload+"' is invalid. Supported values are 'Production' and 'DevTest'.", http.StatusBadRequest)
		return
	}

	adopting := req.Properties.SubscriptionID != ""
	subscriptionID := req.Properties.SubscriptionID
	displayName := req.Properties.DisplayName
	currentTenant := callerTenant
	if adopting {
		// An adopted subscription keeps its display name; rename changes it.
		displayName, _, currentTenant = azureSubscriptionView(subscriptionID)
	}

	// additionalProperties directs the subscription at a tenant and an owner. That
	// leaves an ownership request the destination owner accepts; it does not hand
	// the subscription over.
	var managementGroupID, ownerID, destinationTenant string
	var tags map[string]string
	if extra := req.Properties.AdditionalProperties; extra != nil {
		managementGroupID = extra.ManagementGroupID
		ownerID = extra.SubscriptionOwnerID
		destinationTenant = extra.SubscriptionTenantID
		tags = extra.Tags
	}
	directed := destinationTenant != "" || ownerID != ""
	if destinationTenant == "" {
		destinationTenant = currentTenant
	}

	// A subscription directed away from its current tenant is leaving it, which
	// blockSubscriptionsLeavingTenant governs.
	if !strings.EqualFold(destinationTenant, currentTenant) &&
		azureSubscriptionPolicyDenies(currentTenant, callerObjectID, policyBlockSubscriptionsLeavingTenant) {
		subscriptionRPError(w, policyBlockSubscriptionsLeavingTenant, fmt.Sprintf(
			"The subscription policy of tenant '%s' blocks subscriptions from leaving the tenant.",
			currentTenant), http.StatusForbidden)
		return
	}

	if !adopting {
		subscriptionID = sim.NewUUID()
	}

	// The work finishes in-process, so the create answers with the alias already
	// Succeeded and the SDK's body poller reads no further.
	if adopting {
		ensureAzureSubscriptionRecord(subscriptionID)
	} else {
		azureSubscriptionRecords.Put(subscriptionID, AzureSubscriptionRecord{
			SubscriptionID: subscriptionID,
			DisplayName:    displayName,
			State:          "Enabled",
			TenantID:       currentTenant,
		})
		if callerObjectID != "" {
			rbacAssignOwner(subscriptionID, sim.NewUUID(), callerObjectID, rbacPrincipalType(callerObjectID))
		}
	}

	// A directed subscription stays Pending until Subscription_AcceptOwnership; one
	// for the caller's own use settles Completed.
	ownershipState := "Completed"
	if directed {
		ownershipState = "Pending"
	}
	alias := SubscriptionAliasRecord{
		Name:                 name,
		SubscriptionID:       subscriptionID,
		DisplayName:          displayName,
		BillingScope:         req.Properties.BillingScope,
		Workload:             workload,
		ProvisioningState:    "Succeeded",
		AcceptOwnershipState: ownershipState,
		CreatedTime:          time.Now().UTC().Format(time.RFC3339Nano),
		ResellerID:           req.Properties.ResellerID,
		ManagementGroupID:    managementGroupID,
		SubscriptionOwnerID:  ownerID,
		SubscriptionTenantID: destinationTenant,
		Tags:                 tags,
	}
	azureSubscriptionAliases.Put(name, alias)

	if directed {
		azureSubscriptionOwnerships.Put(subscriptionID, SubscriptionOwnershipRecord{
			SubscriptionID:       subscriptionID,
			AliasName:            name,
			AcceptOwnershipState: "Pending",
			ProvisioningState:    "Pending",
			BillingOwner:         azureBillingOwnerUPN(callerObjectID),
			SubscriptionTenantID: destinationTenant,
			DisplayName:          displayName,
			Tags:                 tags,
		})
	}

	sim.WriteJSON(w, http.StatusCreated, subscriptionAliasResponse(alias))
}

// Only a directory user has a user principal name; an application principal
// raises a request whose status carries none.
func azureBillingOwnerUPN(objectID string) string {
	user, err := azureGrantUser(objectID)
	if err != nil {
		return ""
	}
	return user.PreferredUsername
}

func subscriptionAliasResponse(alias SubscriptionAliasRecord) map[string]any {
	props := map[string]any{
		"subscriptionId":       alias.SubscriptionID,
		"displayName":          alias.DisplayName,
		"provisioningState":    alias.ProvisioningState,
		"acceptOwnershipState": alias.AcceptOwnershipState,
		"workload":             alias.Workload,
		"createdTime":          alias.CreatedTime,
	}
	if alias.BillingScope != "" {
		props["billingScope"] = alias.BillingScope
	}
	if alias.ResellerID != "" {
		props["resellerId"] = alias.ResellerID
	}
	if alias.ManagementGroupID != "" {
		props["managementGroupId"] = alias.ManagementGroupID
	}
	if alias.SubscriptionOwnerID != "" {
		props["subscriptionOwnerId"] = alias.SubscriptionOwnerID
	}
	if len(alias.Tags) > 0 {
		props["tags"] = alias.Tags
	}
	if alias.AcceptOwnershipState == "Pending" && alias.SubscriptionID != "" {
		// Subscription_AcceptOwnership's own route, relative to the Azure Resource
		// Manager endpoint the alias was read from.
		props["acceptOwnershipUrl"] = "/providers/Microsoft.Subscription/subscriptions/" + alias.SubscriptionID + "/acceptOwnership"
	}
	return map[string]any{
		"id":         "/providers/Microsoft.Subscription/aliases/" + alias.Name,
		"name":       alias.Name,
		"type":       "Microsoft.Subscription/aliases",
		"properties": props,
	}
}

// Preserves the identity the subscription has been served with implicitly.
func ensureAzureSubscriptionRecord(sub string) {
	if _, ok := azureSubscriptionRecords.Get(sub); ok {
		return
	}
	displayName, state, tenantID := azureSubscriptionView(sub)
	azureSubscriptionRecords.Put(sub, AzureSubscriptionRecord{
		SubscriptionID: sub,
		DisplayName:    displayName,
		State:          state,
		TenantID:       tenantID,
	})
}

func setAzureSubscriptionState(sub, state string) {
	ensureAzureSubscriptionRecord(sub)
	azureSubscriptionRecords.Update(sub, func(rec *AzureSubscriptionRecord) {
		rec.State = state
	})
}

// The nested ARM error plus the top-level code and message ErrorResponseBody
// declares.
func subscriptionRPError(w http.ResponseWriter, code, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":   map[string]string{"code": code, "message": message},
		"code":    code,
		"message": message,
	})
}
