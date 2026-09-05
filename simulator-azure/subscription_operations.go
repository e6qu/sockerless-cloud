package main

// Microsoft.Subscription (2021-10-01) operation catalog — the list of Azure
// Resource Manager actions the resource provider exposes, which every provider
// serves at /providers/{namespace}/operations.
//
// The catalog is the provider's own surface expressed as role-assignable
// actions: one entry per distinct "{provider}/{resource}/{operation}" action
// that the operations in the vendored Swagger require. Reads collapse onto one
// action (Alias_Get and Alias_List both need Microsoft.Subscription/aliases/
// read), which is why the catalog is shorter than the operation list.
// TestSubscriptionOperationCatalogCoversSpec holds it to that derivation: it
// reads the vendored Swagger and fails if an operationId maps to an action the
// catalog is missing, or if the catalog carries an action no operation needs.

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// subscriptionProviderOperation mirrors the Operation definition in the
// Microsoft.Subscription 2021-10-01 Swagger (spec field spelling kept).
type subscriptionProviderOperation struct {
	Name      string
	Resource  string
	Operation string
}

// subscriptionProviderName is the display spelling of the resource provider,
// as the operations catalog reports it.
const subscriptionProviderName = "Microsoft Subscription"

// subscriptionOperationCatalog is the Microsoft.Subscription action catalog.
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
	// GET - Operations_List.
	srv.HandleFunc("GET /providers/Microsoft.Subscription/operations", func(w http.ResponseWriter, _ *http.Request) {
		value := make([]map[string]any, 0, len(subscriptionOperationCatalog))
		for _, op := range subscriptionOperationCatalog {
			value = append(value, map[string]any{
				"name": op.Name,
				// Every action of this provider is a control-plane action;
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
