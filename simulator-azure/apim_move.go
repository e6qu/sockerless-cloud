package main

import (
	"strings"
)

// Cross-resource-group move for Microsoft.ApiManagement/service (dispatched
// from resource_move.go). The service and its child subtree key by resource
// ID and re-home onto the destination group; the gateway host derives from the
// globally unique name, so it is unchanged.
//
// Subscription keys derive from the subscription's resource ID, which embeds
// the resource group, yet Azure never rotates them on a move: pin the served
// material onto each moved subscription ID until the next regenerate.

var apimSubscriptionKeySlots = []string{"apim-subscription-primary", "apim-subscription-secondary"}

func moveAPIMServiceARM(oldID, newID string) {
	service, ok := apimServices.Get(oldID)
	if !ok {
		return
	}

	// Pin before anything re-keys: the pin reads the material served under the
	// source ID.
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
