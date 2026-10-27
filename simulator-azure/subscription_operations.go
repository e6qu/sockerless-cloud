package main

// The Microsoft.Subscription 2021-10-01 operation catalog: one role-assignable
// "{provider}/{resource}/{operation}" action per distinct action the vendored
// operations require; reads collapse onto one action, so it is shorter than
// the operation list. TestSubscriptionOperationCatalogCoversSpec holds it to
// that derivation.

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Operation definition, spec field spelling kept.
type subscriptionProviderOperation struct {
	Name      string
	Resource  string
	Operation string
}

const subscriptionProviderName = "Microsoft Subscription"

var subscriptionOperationCatalog = []subscriptionProviderOperation{
	{Name: "Microsoft.Subscription/aliases/read", Resource: "Subscription Alias", Operation: "Get Subscription Alias"},
	{Name: "Microsoft.Subscription/aliases/write", Resource: "Subscription Alias", Operation: "Create Subscription Alias"},
	{Name: "Microsoft.Subscription/aliases/delete", Resource: "Subscription Alias", Operation: "Delete Subscription Alias"},
	{Name: "Microsoft.Subscription/cancel/action", Resource: "Subscription", Operation: "Cancel Subscription"},
	{Name: "Microsoft.Subscription/rename/action", Resource: "Subscription", Operation: "Rename Subscription"},
	{Name: "Microsoft.Subscription/enable/action", Resource: "Subscription", Operation: "Enable Subscription"},
	{Name: "Microsoft.Subscription/acceptOwnership/action", Resource: "Subscription", Operation: "Accept Subscription Ownership"},
	{Name: "Microsoft.Subscription/acceptOwnershipStatus/read", Resource: "Subscription", Operation: "Get Subscription Ownership Status"},
	{Name: "Microsoft.Subscription/subscriptionOperations/read", Resource: "Subscription Operation", Operation: "Get Subscription Operation"},
	{Name: "Microsoft.Subscription/policies/read", Resource: "Subscription Policy", Operation: "Get Subscription Policy"},
	{Name: "Microsoft.Subscription/policies/write", Resource: "Subscription Policy", Operation: "Create or Update Subscription Policy"},
	{Name: "Microsoft.Subscription/operations/read", Resource: "Operations", Operation: "List Subscription Operations"},
}

func registerSubscriptionOperations(srv *sim.Server) {
	srv.HandleFunc("GET /providers/Microsoft.Subscription/operations", func(w http.ResponseWriter, _ *http.Request) {
		value := make([]map[string]any, 0, len(subscriptionOperationCatalog))
		for _, op := range subscriptionOperationCatalog {
			value = append(value, map[string]any{
				"name": op.Name,
				// Microsoft.Subscription publishes no data plane.
				"isDataAction": false,
				"display": map[string]any{
					"provider":  subscriptionProviderName,
					"resource":  op.Resource,
					"operation": op.Operation,
				},
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
	})
}
