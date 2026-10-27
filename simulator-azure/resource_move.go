package main

import (
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cross-resource-group move dispatch for Resources_MoveResources and
// Resources_ValidateMoveResources. Each slice registers a hook per movable
// top-level type, so resourcesarm.go stays provider-agnostic. A type with no
// hook answers ResourceMoveNotSupported, which is also Azure's answer for the
// types it refuses to move (Azure Container Instances container groups among
// them).

// The validate spelling runs exists; the move spelling runs exists, then move.
type resourceMoveHook struct {
	exists func(resID string) bool
	// supported decides per resource when movability is not a property of the
	// type: a private endpoint moves only when its private-link resource is a type
	// Azure lists as supporting the move. The string is the
	// ResourceMoveNotSupported message's reason. Nil for most types.
	supported func(resID string) (bool, string)
	// targetRG is the destination group's name, for rows that record the group by
	// name.
	move func(oldID, newID, targetRG string)
}

// Keyed by lowercased "provider/type". Hooks over package-level stores are
// seeded here and read the stores at request time; a slice whose stores are
// locals registers through registerResourceMoveHook, as registerStorageAccounts
// does.
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

// Register functions call this before the server serves, so the map needs no
// lock. A later registration overwrites: buildSimulator can run more than once
// in one process, as the in-process test servers do.
func registerResourceMoveHook(typeKey string, hook resourceMoveHook) {
	resourceMoveHooks[strings.ToLower(typeKey)] = hook
}

// id returns a pointer to the row's ID member, so the helper filters on it and
// rewrites it in place.
func rekeyRowsByPrefix[T any](store sim.Store[T], oldPrefix, newPrefix string, id func(*T) *string) {
	rows := store.Filter(func(row T) bool { return strings.HasPrefix(*id(&row), oldPrefix) })
	for _, row := range rows {
		old := *id(&row)
		store.Delete(old)
		*id(&row) = newPrefix + strings.TrimPrefix(old, oldPrefix)
		store.Put(*id(&row), row)
	}
}

// For a store whose rows carry no ID member.
func rekeyEntry[T any](store sim.Store[T], oldKey, newKey string) {
	if row, ok := store.Get(oldKey); ok {
		store.Delete(oldKey)
		store.Put(newKey, row)
	}
}

// azureRepointMovedResource re-homes every row stored beneath the moved ID and
// rewrites every reference any resource holds to it — a private endpoint's
// privateLinkServiceId, an interface's subnet ID, a Redis linked server's
// linkedRedisCacheId, a system topic's source. It scans every store this build
// created (sim.TrackedStores) rather than a hand-listed set, so a slice added
// later cannot be missed.
func azureRepointMovedResource(oldID, newID string) {
	rekey := func(key string) string { return azureRepointStoreKey(key, oldID, newID) }
	edit := func(value string) string { return azureRepointReference(value, oldID, newID) }
	for _, store := range sim.TrackedStores() {
		store.Remap(rekey, edit)
	}
}

// Matched case-insensitively, as Azure Resource Manager compares IDs, keeping
// the rest of the key's spelling. The moved ID must be the whole key or be
// followed by `/`, so a sibling whose name extends it is left alone, as are the
// `<resourceID>|<slot>` key-generation rows, which pinAzureKeySlots carries.
func azureRepointStoreKey(key, oldID, newID string) string {
	if len(key) < len(oldID) || !strings.EqualFold(key[:len(oldID)], oldID) {
		return key
	}
	if len(key) == len(oldID) || key[len(oldID)] == '/' {
		return newID + key[len(oldID):]
	}
	return key
}

// A reference is the moved ID at a resource-ID boundary: the string's end, `/`
// for a child segment or URL path, or `?`, `#` or `&` in a URL that embeds the
// ID, so advertised data-plane URLs are rewritten too.
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

// -1 when the string holds none.
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

// A complete reference rather than the prefix of a longer name.
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
