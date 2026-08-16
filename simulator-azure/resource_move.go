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
	// supported, when set, decides per resource whether Azure Resource Manager
	// moves this particular one. Most types are movable or not as a whole, and
	// leave it nil; a Microsoft.Network private endpoint is movable only when
	// the private-link resource it connects to is one of the types Azure lists
	// as supporting the move, so it answers here instead. The string is the
	// reason ARM's ResourceMoveNotSupported message carries.
	supported func(resID string) (bool, string)
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
	"microsoft.eventgrid/systemtopics": {
		exists: func(id string) bool { _, ok := eventGridSystemTopics.Get(id); return ok },
		move:   func(oldID, newID, _ string) { moveEventGridSystemTopicARM(oldID, newID) },
	},
	"microsoft.eventgrid/partnertopics": {
		exists: func(id string) bool { _, ok := eventGridPartnerTopics.Get(id); return ok },
		move:   func(oldID, newID, _ string) { moveEventGridPartnerTopicARM(oldID, newID) },
	},
	"microsoft.eventgrid/partnernamespaces": {
		exists: func(id string) bool { _, ok := eventGridPartnerNamespaces.Get(id); return ok },
		move:   func(oldID, newID, _ string) { moveEventGridPartnerNamespaceARM(oldID, newID) },
	},
	"microsoft.apimanagement/service": {
		exists: func(id string) bool { _, ok := apimServices.Get(id); return ok },
		move:   func(oldID, newID, _ string) { moveAPIMServiceARM(oldID, newID) },
	},
	"microsoft.logic/workflows": {
		exists: func(id string) bool { _, ok := logicWorkflows.Get(id); return ok },
		move:   func(oldID, newID, _ string) { moveLogicWorkflowARM(oldID, newID) },
	},
	"microsoft.documentdb/databaseaccounts": {
		exists: func(id string) bool { _, ok := cosmosAccounts.Get(id); return ok },
		move:   func(oldID, newID, _ string) { moveCosmosAccountARM(oldID, newID) },
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

// azureRepointMovedResource re-homes everything the hooks do not address by
// name: every row any slice stores beneath the moved resource ID, and every
// reference any resource anywhere holds to it.
//
// An Azure Resource Manager resource ID is an address, and a resource that
// names another one stores that address verbatim — a private endpoint's
// privateLinkServiceId, a network interface's subnet id, an Azure Cache for
// Redis linked server's linkedRedisCacheId, an Event Grid system topic's source
// and metricResourceId, an event subscription's destination resourceId. A move
// that re-keyed only the moved records
// would leave every one of those pointing at an address nothing answers to, so
// the fabric would silently break. The pass walks the stores this build created
// (sim.TrackedStores) and rewrites both halves: a stored key that is the moved
// ID or sits beneath it, and any string that names the moved ID at a resource-ID
// boundary.
//
// Scanning every store rather than a hand-listed set is deliberate: a reference
// missed because a slice was added after the list was written is a silent
// correctness hole, and the set of stores is exactly what the simulator built.
func azureRepointMovedResource(oldID, newID string) {
	rekey := func(key string) string { return azureRepointStoreKey(key, oldID, newID) }
	edit := func(value string) string { return azureRepointReference(value, oldID, newID) }
	for _, store := range sim.TrackedStores() {
		store.Remap(rekey, edit)
	}
}

// azureRepointStoreKey re-homes a store key that addresses the moved resource
// or a row stored beneath it. Azure Resource Manager compares resource IDs
// case-insensitively, so the match is case-folded while the key keeps the rest
// of its own spelling.
//
// The match requires the moved ID to be the whole key or to be followed by the
// `/` that starts a child segment, so a key that merely begins with the same
// characters — a sibling resource whose name extends the moved one's — is left
// alone. It also leaves the `<resourceID>|<slot>` keys of the key-generation
// stores alone, which is what carries a credential across a move: those rows
// are re-homed by pinAzureKeySlots, which pins the material rather than
// re-deriving it from the new ID.
func azureRepointStoreKey(key, oldID, newID string) string {
	if len(key) < len(oldID) || !strings.EqualFold(key[:len(oldID)], oldID) {
		return key
	}
	if len(key) == len(oldID) || key[len(oldID)] == '/' {
		return newID + key[len(oldID):]
	}
	return key
}

// azureRepointReference rewrites every reference to the moved resource a stored
// string holds. A reference is the moved resource ID at a resource-ID boundary:
// the end of the string, or one of the characters that can follow a resource ID
// inside a longer value — `/` for a child segment or a URL path, and `?`, `#`
// or `&` for a URL that embeds the ID in its path. That covers both a bare
// reference member and any data-plane URL a resource advertises that is built
// from a resource ID.
func azureRepointReference(value, oldID, newID string) string {
	at := azureReferenceIndex(value, oldID, 0)
	if at < 0 {
		return value
	}
	var out strings.Builder
	out.Grow(len(value))
	from := 0
	for at >= 0 {
		out.WriteString(value[from:at])
		out.WriteString(newID)
		from = at + len(oldID)
		at = azureReferenceIndex(value, oldID, from)
	}
	out.WriteString(value[from:])
	return out.String()
}

// azureReferenceIndex returns the index of the first reference to oldID in
// value at or after from, or -1 when the string holds none.
func azureReferenceIndex(value, oldID string, from int) int {
	if oldID == "" {
		return -1
	}
	for i := from; i+len(oldID) <= len(value); i++ {
		if strings.EqualFold(value[i:i+len(oldID)], oldID) && azureReferenceEndsAt(value, i+len(oldID)) {
			return i
		}
	}
	return -1
}

// azureReferenceEndsAt reports whether a resource ID ending at index i inside
// value is a complete reference rather than the prefix of a longer name.
func azureReferenceEndsAt(value string, i int) bool {
	if i == len(value) {
		return true
	}
	switch value[i] {
	case '/', '?', '#', '&':
		return true
	}
	return false
}
