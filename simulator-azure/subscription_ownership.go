package main

// Microsoft.Subscription (2021-10-01) ownership acceptance — the second half
// of the programmatic subscription-creation flow.
//
// An alias creation that directs a subscription at a tenant and an owner
// (properties.additionalProperties.subscriptionTenantId /
// subscriptionOwnerId) does not hand the subscription over by itself: it
// leaves an ownership request in the Pending state, and the destination owner
// completes the handover by calling Subscription_AcceptOwnership. That call is
// long-running in the Azure Resource Manager sense — it answers 202 with a
// Location naming a Microsoft.Subscription operation, which reports 202 while
// the acceptance runs and 200 with the new subscription's link once it has
// settled. Subscription_AcceptOwnershipStatus reports the ownership state
// itself (Pending, then Completed), and the same transition is visible on the
// alias the provider reads.
//
// Wire shapes, status codes and the Location target mirror the vendored
// Swagger and its examples
// (specs/cloud-api/azure/subscription-arm-subscriptions-2021-10-01).

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// SubscriptionOwnershipRecord mirrors AcceptOwnershipStatusResponse in the
// Microsoft.Subscription 2021-10-01 Swagger (spec field spelling kept), keyed
// by the subscription whose ownership it tracks. AliasName links it back to
// the alias creation that raised the request, so accepting ownership is
// visible on the alias too.
type SubscriptionOwnershipRecord struct {
	SubscriptionID string
	AliasName      string
	// AcceptOwnershipState carries the Swagger's AcceptOwnershipState enum.
	// An offer runs Pending → Completed; the enum's third member, Expired, is
	// reached by an expiry the 2021-10-01 contract puts no period on, so
	// nothing here produces it.
	AcceptOwnershipState string
	ProvisioningState    string // Pending / Accepted / Succeeded
	BillingOwner         string
	SubscriptionTenantID string
	DisplayName          string
	Tags                 map[string]string
}

// SubscriptionOperationRecord is one entry of the Microsoft.Subscription
// operation registry that SubscriptionOperation_Get reads. The operation's
// run state lives in the shared Azure Resource Manager async-operation store
// under the same id; this record carries the provider-specific result, the
// link to the subscription the operation concerns.
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

	// POST - Subscription_AcceptOwnership.
	srv.HandleFunc("POST /providers/Microsoft.Subscription/subscriptions/{subscriptionId}/acceptOwnership", handleSubscriptionAcceptOwnership)

	// GET - Subscription_AcceptOwnershipStatus.
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

	// GET - SubscriptionOperation_Get: the Location target of every
	// long-running Microsoft.Subscription operation.
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
		case "InProgress":
			// The Location of an in-progress operation is the operation
			// itself; Retry-After is short so a poller re-polls promptly
			// rather than falling back to its default frequency.
			w.Header().Set("Location", azureCurrentRequestURL(r))
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusAccepted)
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
	// Ownership is accepted by a principal of the tenant the subscription was
	// directed at; a principal of any other tenant has nothing to accept.
	if !equalFoldNonEmpty(callerTenant, record.SubscriptionTenantID) {
		subscriptionRPError(w, "AuthorizationFailed", fmt.Sprintf(
			"The ownership of subscription '%s' was offered to tenant '%s'; the caller belongs to tenant '%s'.",
			sub, record.SubscriptionTenantID, callerTenant), http.StatusForbidden)
		return
	}
	// Accepting ownership brings the subscription into the caller's tenant,
	// which is exactly what blockSubscriptionsIntoTenant governs.
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
		// The accepted subscription takes the name its new owner gave it, and
		// the alias that raised the request reports the completed handover.
		ensureAzureSubscriptionRecord(sub)
		azureSubscriptionRecords.Update(sub, func(rec *AzureSubscriptionRecord) {
			rec.DisplayName = displayName
			rec.TenantID = callerTenant
		})
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
		SubscriptionLink: "/subscriptions/" + sub,
	})

	w.Header().Set("Location", subscriptionOperationURL(r, opID))
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusAccepted)
}

// subscriptionOperationURL is the absolute Location a long-running
// Microsoft.Subscription operation advertises, carrying forward the
// api-version the caller used.
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
	// billingOwner is the user principal name of the billing owner. It is
	// present only when the principal that raised the ownership request is a
	// directory user; an application principal has no user principal name.
	if record.BillingOwner != "" {
		out["billingOwner"] = record.BillingOwner
	}
	if len(record.Tags) > 0 {
		out["tags"] = record.Tags
	}
	return out
}

// equalFoldNonEmpty reports whether two identifiers are the same, treating an
// empty identifier as matching nothing.
func equalFoldNonEmpty(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(a, b)
}
