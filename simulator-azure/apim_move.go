package main

import (
	"strings"
)

// Cross-resource-group move for Microsoft.ApiManagement/service. The hook
// table in resource_move.go dispatches Resources_MoveResources here.
//
// Azure Resource Manager moves an API Management service between resource
// groups ("Azure resource types for move operations", Microsoft.ApiManagement
// / service: Resource group = Yes).
//
// A service keys its ARM record — and its APIs, products, subscriptions,
// operations, backends, named values and the rest of its child subtree — by
// resource ID, so the whole subtree re-homes onto the destination group; the
// repointing pass in resource_move.go carries the children and every reference
// held to them. The service's gateway host is derived from its globally unique
// name rather than its resource group, so the URL a client calls is unchanged.
//
// The credential a move must not rotate is the subscription key: each
// subscription's primary and secondary keys are derived from the subscription's
// own resource ID, which embeds the service's resource group, so a naive
// re-key would silently invalidate every `Ocp-Apim-Subscription-Key` an API
// consumer holds. Real Azure never rotates a subscription's keys on a move, so
// the material listSecrets serves is pinned onto each moved subscription ID;
// the next regenerate clears the pin and derives fresh material.

// apimSubscriptionKeySlots are the two key slots an API Management
// subscription serves through listSecrets and rotates individually.
var apimSubscriptionKeySlots = []string{"apim-subscription-primary", "apim-subscription-secondary"}

// moveAPIMServiceARM re-homes one API Management service's ARM record onto a
// new resource ID, pinning every subscription key the service issues so the
// credential an API consumer already sends keeps authorizing.
func moveAPIMServiceARM(oldID, newID string) {
	service, ok := apimServices.Get(oldID)
	if !ok {
		return
	}

	// Pin before anything re-keys: the pin reads the material the slot serves
	// under the source ID.
	for _, subscription := range apimSubscriptions.List() {
		if !strings.HasPrefix(subscription.ID, oldID+"/") {
			continue
		}
		pinAzureKeySlots(subscription.ID, newID+strings.TrimPrefix(subscription.ID, oldID),
			azureKeyMaterial32, apimSubscriptionKeySlots...)
	}

	apimServices.Delete(oldID)
	service.ID = newID
	apimServices.Put(service.ID, service)
}
