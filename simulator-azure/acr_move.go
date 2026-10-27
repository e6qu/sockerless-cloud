package main

import (
	"github.com/e6qu/sockerless-cloud/sim"
)

// Cross-resource-group move for Microsoft.ContainerRegistry/registries
// (dispatched from resource_move.go). The registry's ARM record and every
// child re-key onto the destination group. The login server derives from the
// globally unique name, so it is unchanged. The OCI content keys by resource
// ID, and the repointing pass in resource_move.go re-keys it. The admin
// passwords derive from the resource ID, so the move pins the served material
// onto the new ID until the next regenerateCredential.

// acrMovableChildStores: registerACRChild appends each generic child store, and
// registerACR resets the list so a second buildSimulator re-collects its own.
var acrMovableChildStores []sim.Store[acrSubResource]

var (
	acrCacheRules sim.Store[ACRCacheRule]
	acrWebhooks   sim.Store[acrWebhookStored]
)

// moveACRRegistryARM pins the admin credentials so the password an operator
// holds keeps authenticating.
func moveACRRegistryARM(oldID, newID string) {
	registry, ok := acrRegistries.Get(oldID)
	if !ok {
		return
	}

	pinAzureKeySlots(oldID, newID, azureKeyMaterial32, "password", "password2")

	acrRegistries.Delete(oldID)
	registry.ID = newID
	acrRegistries.Put(registry.ID, registry)

	oldSub, newSub := oldID+"/", newID+"/"
	for _, store := range acrMovableChildStores {
		rekeyRowsByPrefix(store, oldSub, newSub, func(c *acrSubResource) *string { return &c.ID })
	}
	rekeyRowsByPrefix(acrCacheRules, oldSub, newSub, func(c *ACRCacheRule) *string { return &c.ID })
	rekeyRowsByPrefix(acrWebhooks, oldSub, newSub, func(wh *acrWebhookStored) *string { return &wh.ID })

	// A task run is stored under its short run ID, so rewrite its record in place.
	// Update rather than Put: a run still executing writes its own record
	// concurrently.
	for _, run := range acrRuns.List() {
		if len(run.ID) <= len(oldSub) || run.ID[:len(oldSub)] != oldSub {
			continue
		}
		acrRuns.Update(run.Properties.RunID, func(run *acrRun) {
			if len(run.ID) > len(oldSub) && run.ID[:len(oldSub)] == oldSub {
				run.ID = newSub + run.ID[len(oldSub):]
			}
		})
	}
}
