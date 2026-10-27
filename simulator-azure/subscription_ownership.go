package main

// Microsoft.Subscription 2021-10-01 ownership acceptance. An alias directing a
// subscription at a tenant and owner leaves a Pending ownership request;
// Subscription_AcceptOwnership completes it, answering 202 with a Location
// naming an operation that reports 202 while it runs and 200 with the
// subscription's link once settled. Subscription_AcceptOwnershipStatus reports
// Pending, then Completed, and the alias shows the same transition.

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// AcceptOwnershipStatusResponse, spec field spelling kept, keyed by
// subscription. AliasName links back to the alias that raised the request.
type SubscriptionOwnershipRecord struct {
	SubscriptionID string
	AliasName      string
	// Pending → Completed. The contract puts no period on the expiry that reaches
	// Expired, so nothing produces it.
	AcceptOwnershipState string
	ProvisioningState    string // Pending / Accepted / Succeeded
	BillingOwner         string
	SubscriptionTenantID string
	DisplayName          string
	Tags                 map[string]string
}

// The run state lives in the shared async-operation store under the same ID;
// this record carries the link to the subscription the operation concerns.
type SubscriptionOperationRecord struct {
	ID               string
	SubscriptionLink string
}

var (
	azureSubscriptionOwnerships sim.Store[SubscriptionOwnershipRecord]
	azureSubscriptionOperations sim.Store[SubscriptionOperationRecord]
)

func registerSubscriptionOwnership(srv *sim.Server) {
	azureSubscriptionOwnerships = sim.MakeStore[SubscriptionOwnershipRecord](srv.DB(), "azure_subscription_ownerships")
	azureSubscriptionOperations = sim.MakeStore[SubscriptionOperationRecord](srv.DB(), "azure_subscription_operations")

	srv.HandleFunc("POST /providers/Microsoft.Subscription/subscriptions/{subscriptionId}/acceptOwnership", handleSubscriptionAcceptOwnership)

	srv.HandleFunc("GET /providers/Microsoft.Subscription/subscriptions/{subscriptionId}/acceptOwnershipStatus", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		record, ok := azureSubscriptionOwnerships.Get(sub)
		if !ok {
			subscriptionRPError(w, "NotFound",
				"No ownership request exists for subscription '"+sub+"'.", http.StatusNotFound)
			return
		}
		sim.WriteJSON(w, http.StatusOK, subscriptionOwnershipStatusResponse(record))
	})

	// The Location target of every long-running Microsoft.Subscription operation.
	srv.HandleFunc("GET /providers/Microsoft.Subscription/subscriptionOperations/{operationId}", func(w http.ResponseWriter, r *http.Request) {
		opID := sim.PathParam(r, "operationId")
		record, ok := azureSubscriptionOperations.Get(opID)
		if !ok {
			subscriptionRPError(w, "NotFound",
				"The operation '"+opID+"' cannot be found.", http.StatusNotFound)
			return
		}
		op, ok := azureAsyncOps.Get(opID)
		if !ok {
			subscriptionRPError(w, "NotFound",
				"The operation '"+opID+"' cannot be found.", http.StatusNotFound)
			return
		}
		switch op.Status {
		case "Failed":
			code, message := "OperationFailed", "The operation failed."
			if op.Error != nil {
				code, message = op.Error.Code, op.Error.Message
			}
			subscriptionRPError(w, code, message, http.StatusInternalServerError)
		default:
			sim.WriteJSON(w, http.StatusOK, map[string]any{"subscriptionLink": record.SubscriptionLink})
		}
	})
}

func handleSubscriptionAcceptOwnership(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	callerTenant, callerObjectID, err := azureARMCallerPrincipal(r)
	if err != nil {
		subscriptionRPError(w, "InvalidAuthenticationToken", err.Error(), http.StatusUnauthorized)
		return
	}

	var req struct {
		Properties *struct {
			DisplayName       string            `json:"displayName"`
			ManagementGroupID string            `json:"managementGroupId"`
			Tags              map[string]string `json:"tags"`
		} `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		subscriptionRPError(w, "BadRequest", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Properties == nil || req.Properties.DisplayName == "" {
		subscriptionRPError(w, "BadRequest",
			"Accepting subscription ownership requires a non-empty 'properties.displayName'.", http.StatusBadRequest)
		return
	}

	record, ok := azureSubscriptionOwnerships.Get(sub)
	if !ok {
		subscriptionRPError(w, "NotFound",
			"No ownership request exists for subscription '"+sub+"'.", http.StatusNotFound)
		return
	}
	if record.AcceptOwnershipState != "Pending" {
		subscriptionRPError(w, "Conflict", fmt.Sprintf(
			"The ownership request for subscription '%s' is %s and can no longer be accepted.",
			sub, record.AcceptOwnershipState), http.StatusConflict)
		return
	}
	// Only a principal of the tenant the subscription was directed at can accept.
	if !equalFoldNonEmpty(callerTenant, record.SubscriptionTenantID) {
		subscriptionRPError(w, "AuthorizationFailed", fmt.Sprintf(
			"The ownership of subscription '%s' was offered to tenant '%s'; the caller belongs to tenant '%s'.",
			sub, record.SubscriptionTenantID, callerTenant), http.StatusForbidden)
		return
	}
	// Accepting brings the subscription into the caller's tenant, which
	// blockSubscriptionsIntoTenant governs.
	if azureSubscriptionPolicyDenies(callerTenant, callerObjectID, policyBlockSubscriptionsIntoTenant) {
		subscriptionRPError(w, policyBlockSubscriptionsIntoTenant, fmt.Sprintf(
			"The subscription policy of tenant '%s' blocks subscriptions from entering the tenant.",
			callerTenant), http.StatusForbidden)
		return
	}

	displayName := req.Properties.DisplayName
	tags := req.Properties.Tags
	azureSubscriptionOwnerships.Update(sub, func(rec *SubscriptionOwnershipRecord) {
		rec.DisplayName = displayName
		rec.Tags = tags
		rec.ProvisioningState = "Accepted"
	})

	opID := issueAzureAsyncOperation(func() {
		azureSubscriptionOwnerships.Update(sub, func(rec *SubscriptionOwnershipRecord) {
			rec.AcceptOwnershipState = "Completed"
			rec.ProvisioningState = "Succeeded"
		})
		// The subscription takes its new owner's name, and the alias reports the
		// completed handover.
		ensureAzureSubscriptionRecord(sub)
		azureSubscriptionRecords.Update(sub, func(rec *AzureSubscriptionRecord) {
			rec.DisplayName = displayName
			rec.TenantID = callerTenant
		})
		// A subscription that changes directory loses its role assignments;
		// the principal that accepted it owns it in the new one.
		rbacRemoveAssignmentsWithin(armSubscriptionID(sub))
		if callerObjectID != "" {
			rbacAssignOwner(sub, sim.NewUUID(), callerObjectID, rbacPrincipalType(callerObjectID))
		}
		if record.AliasName != "" {
			azureSubscriptionAliases.Update(record.AliasName, func(alias *SubscriptionAliasRecord) {
				alias.AcceptOwnershipState = "Completed"
				alias.DisplayName = displayName
				alias.Tags = tags
			})
		}
	})
	azureSubscriptionOperations.Put(opID, SubscriptionOperationRecord{
		ID:               opID,
		SubscriptionLink: armSubscriptionID(sub),
	})

	w.Header().Set("Location", subscriptionOperationURL(r, opID))
	setAzureAsyncOperationRetryAfter(w, opID)
	w.WriteHeader(http.StatusAccepted)
}

// Carries forward the caller's api-version.
func subscriptionOperationURL(r *http.Request, opID string) string {
	apiVersion := r.URL.Query().Get("api-version")
	return fmt.Sprintf("%s://%s/providers/Microsoft.Subscription/subscriptionOperations/%s?api-version=%s",
		azureRequestScheme(r), r.Host, opID, apiVersion)
}

func subscriptionOwnershipStatusResponse(record SubscriptionOwnershipRecord) map[string]any {
	out := map[string]any{
		"subscriptionId":       record.SubscriptionID,
		"acceptOwnershipState": record.AcceptOwnershipState,
		"provisioningState":    record.ProvisioningState,
		"subscriptionTenantId": record.SubscriptionTenantID,
		"displayName":          record.DisplayName,
	}
	// Present only when a directory user raised the request; an application
	// principal has no user principal name.
	if record.BillingOwner != "" {
		out["billingOwner"] = record.BillingOwner
	}
	if len(record.Tags) > 0 {
		out["tags"] = record.Tags
	}
	return out
}

// An empty identifier matches nothing.
func equalFoldNonEmpty(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(a, b)
}
