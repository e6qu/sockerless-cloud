package main

import (
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Cross-resource-group move dispatch for Resources_MoveResources and
// Resources_ValidateMoveResources. Each provider slice that supports moving a
// top-level resource type registers a hook here, so the Microsoft.Resources
// handler (resourcesarm.go) stays provider-agnostic and a new movable family
// is one registration plus its re-keying function. A type with no hook
// answers ARM's ResourceMoveNotSupported — which is also the truthful answer
// for the types real Azure Resource Manager refuses to move across resource
// groups (Azure Container Instances container groups among them).

// resourceMoveHook is one resource type's participation in a cross-group
// move: the validate spelling runs exists, the move spelling runs exists then
// move.
type resourceMoveHook struct {
	// exists reports whether resID names a live resource — the pre-flight
	// check both the validate and the move spellings run.
	exists func(resID string) bool
	// move re-homes the resource, and everything stored beneath it, from
	// oldID onto newID; targetRG is the destination resource-group name the
	// new ID carries, for rows that record the group by name.
	move func(oldID, newID, targetRG string)
}

// resourceMoveHooks maps the lowercased "provider/type" key of a movable
// top-level resource type to its hook.
//
// Most hooks are seeded statically: their stores are package-level variables
// and the closures read them at request time, after every register function
// has run. A slice whose stores are locals of its register function registers
// its hook there via registerResourceMoveHook, as Microsoft.Storage does in
// registerStorageAccounts.
var resourceMoveHooks = map[string]resourceMoveHook{
	"microsoft.web/sites": {
		exists: func(id string) bool { _, ok := azfSites.Get(id); return ok },
		move:   webMoveSiteTree,
	},
	"microsoft.web/serverfarms": {
		exists: func(id string) bool { _, ok := azureAppServicePlans.Get(id); return ok },
		move:   func(oldID, newID, _ string) { webMoveAppServicePlan(oldID, newID) },
	},
	"microsoft.web/certificates": {
		exists: func(id string) bool { _, ok := webCertificates.Get(id); return ok },
		move:   func(oldID, newID, _ string) { webMoveCertificate(oldID, newID) },
	},
	"microsoft.keyvault/vaults": {
		exists: func(id string) bool { _, ok := keyVaults.Get(id); return ok },
		move:   func(oldID, newID, _ string) { moveKeyVaultARM(oldID, newID) },
	},
	"microsoft.servicebus/namespaces": {
		exists: func(id string) bool { _, ok := sbNamespaces.Get(id); return ok },
		move:   func(oldID, newID, _ string) { moveServiceBusNamespaceARM(oldID, newID) },
	},
	"microsoft.eventhub/namespaces": {
		exists: func(id string) bool { _, ok := ehNamespaces.Get(id); return ok },
		move:   func(oldID, newID, _ string) { moveEventHubNamespaceARM(oldID, newID) },
	},
	"microsoft.cache/redis": {
		exists: func(id string) bool { _, ok := redisCaches.Get(id); return ok },
		move:   func(oldID, newID, _ string) { moveRedisCacheARM(oldID, newID) },
	},
	"microsoft.containerregistry/registries": {
		exists: func(id string) bool { _, ok := acrRegistries.Get(id); return ok },
		move:   func(oldID, newID, _ string) { moveACRRegistryARM(oldID, newID) },
	},
	"microsoft.eventgrid/topics": {
		exists: func(id string) bool { _, ok := eventGridTopics.Get(id); return ok },
		move:   func(oldID, newID, _ string) { moveEventGridTopicARM(oldID, newID) },
	},
	"microsoft.eventgrid/domains": {
		exists: func(id string) bool { _, ok := eventGridDomains.Get(id); return ok },
		move:   func(oldID, newID, _ string) { moveEventGridDomainARM(oldID, newID) },
	},
}

// registerResourceMoveHook adds one resource type's move hook to the dispatch
// table. Register functions call it while the server is being built, before
// it serves requests, so the map needs no locking. Registration overwrites:
// buildSimulator can run more than once in one process (the in-process test
// servers do), and each build re-registers its hooks over the previous
// build's, exactly as the package-level store variables are reassigned.
func registerResourceMoveHook(typeKey string, hook resourceMoveHook) {
	resourceMoveHooks[strings.ToLower(typeKey)] = hook
}

// rekeyRowsByPrefix re-keys every row whose ID starts with oldPrefix onto
// newPrefix. The id accessor returns a pointer to the row's ID member so the
// helper both filters on it and rewrites it in place.
func rekeyRowsByPrefix[T any](store sim.Store[T], oldPrefix, newPrefix string, id func(*T) *string) {
	rows := store.Filter(func(row T) bool { return strings.HasPrefix(*id(&row), oldPrefix) })
	for _, row := range rows {
		old := *id(&row)
		store.Delete(old)
		*id(&row) = newPrefix + strings.TrimPrefix(old, oldPrefix)
		store.Put(*id(&row), row)
	}
}

// rekeyEntry re-homes one key-addressed record (a store whose rows carry no
// ID member of their own).
func rekeyEntry[T any](store sim.Store[T], oldKey, newKey string) {
	if row, ok := store.Get(oldKey); ok {
		store.Delete(oldKey)
		store.Put(newKey, row)
	}
}
