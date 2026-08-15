package main

import (
	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Cross-resource-group move for Microsoft.ContainerRegistry/registries. The
// hook table in resource_move.go dispatches Resources_MoveResources here.
//
// A registry keys its ARM record — and its replications, scope maps, tokens,
// credential sets, connected registries, private endpoint connections, cache
// rules, webhooks, tasks, task runs and agent pools — by resource ID, so the
// whole subtree re-keys onto the destination group. The registry's login
// server is derived from its globally unique name rather than its resource
// group, so the host a Docker client pushes to is unchanged by the move, and
// the repositories, manifests and blobs of the OCI data plane are addressed
// under that name and need no re-keying.
//
// The registry's two admin passwords are derived from the resource ID, which
// embeds the resource group, so the material listCredentials serves is pinned
// onto the moved ID; the next regenerateCredential clears the pin and derives
// fresh material.

// acrMovableChildStores are the generic child stores a registry's move
// re-keys. registerACRChild appends each one as it mounts it, and registerACR
// resets the list so a second buildSimulator in the same process re-collects
// its own stores rather than the previous build's.
var acrMovableChildStores []sim.Store[acrSubResource]

// acrCacheRules, acrWebhooks are the two registry children with bespoke row
// types; the move re-keys them alongside the generic ones.
var (
	acrCacheRules sim.Store[ACRCacheRule]
	acrWebhooks   sim.Store[acrWebhookStored]
)

// moveACRRegistryARM re-homes one container registry's ARM plane onto a new
// resource ID, pinning its admin credentials so the password an operator holds
// keeps authenticating.
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

	// A task run is stored under its own short run identifier rather than its
	// resource ID, so its record is rewritten in place: the ID it reports
	// names the registry, and that reference moves with the registry.
	for _, run := range acrRuns.List() {
		if len(run.ID) <= len(oldSub) || run.ID[:len(oldSub)] != oldSub {
			continue
		}
		run.ID = newSub + run.ID[len(oldSub):]
		acrRuns.Put(run.Properties.RunID, run)
	}
}
