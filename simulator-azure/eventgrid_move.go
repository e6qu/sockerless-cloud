package main

import (
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cross-resource-group move for topics, domains, systemTopics, partnerTopics
// and partnerNamespaces (dispatched from resource_move.go). Azure refuses
// partnerRegistrations and partnerConfigurations, so they have no hook and
// answer ResourceMoveNotSupported.
//
// The subtree keys by resource ID and re-keys onto the destination group; the
// publish endpoint derives from the globally unique name, so it is unchanged.
// The move also pins the access keys, which derive from the resource ID, so
// listKeys and every SAS signed with them keep working until the next
// regenerateKey, and rewrites each moved event subscription's `topic`
// property, which the publish fan-out matches on.

func moveEventGridTopicARM(oldID, newID string) {
	topic, ok := eventGridTopics.Get(oldID)
	if !ok {
		return
	}
	pinEventGridKeys(oldID, newID)
	eventGridTopics.Delete(oldID)
	topic.ID = newID
	eventGridTopics.Put(topic.ID, topic)
	moveEventGridSubscriptions(oldID, newID)
}

func moveEventGridDomainARM(oldID, newID string) {
	domain, ok := eventGridDomains.Get(oldID)
	if !ok {
		return
	}
	pinEventGridKeys(oldID, newID)
	eventGridDomains.Delete(oldID)
	domain.ID = newID
	eventGridDomains.Put(domain.ID, domain)
	rekeyRowsByPrefix(eventGridDomainTopics, oldID+"/", newID+"/", func(t *EventGridTopic) *string { return &t.ID })
	moveEventGridSubscriptions(oldID, newID)
}

// pinEventGridKeys carries the current keys onto the new resource ID, so the
// credentials an operator holds keep working.
func pinEventGridKeys(oldID, newID string) {
	pinAzureKeySlots(oldID, newID, azureKeyMaterial32, eventGridKeySlots...)
}

// A system topic has no access key — its `source` resource publishes to it —
// so there is nothing to pin. Its `source` and `metricResourceId` follow the
// source through the repointing pass when the source moves.
func moveEventGridSystemTopicARM(oldID, newID string) {
	moveEventGridScope(eventGridSystemTopics, oldID, newID, false)
}

// A partner publishes through the partner namespace's channel, with the
// namespace's key, so the topic has no key to pin.
func moveEventGridPartnerTopicARM(oldID, newID string) {
	moveEventGridScope(eventGridPartnerTopics, oldID, newID, false)
}

func moveEventGridPartnerNamespaceARM(oldID, newID string) {
	moveEventGridScope(eventGridPartnerNamespaces, oldID, newID, true)
	rekeyRowsByPrefix(eventGridPartnerChannels, oldID+"/", newID+"/",
		func(c *EventGridTopic) *string { return &c.ID })
}

// keyBearing pins the scope's access keys.
func moveEventGridScope(store sim.Store[EventGridTopic], oldID, newID string, keyBearing bool) {
	scope, ok := store.Get(oldID)
	if !ok {
		return
	}
	if keyBearing {
		pinEventGridKeys(oldID, newID)
	}
	store.Delete(oldID)
	scope.ID = newID
	store.Put(scope.ID, scope)
	moveEventGridSubscriptions(oldID, newID)
}

// Rewrite each subscription's `topic` property, which the publish fan-out and
// the scoped list views find it by.
func moveEventGridSubscriptions(oldID, newID string) {
	for _, es := range eventGridSubscriptions.List() {
		if !strings.HasPrefix(es.ID, oldID+"/") {
			continue
		}
		eventGridSubscriptions.Delete(es.ID)
		es.ID = newID + strings.TrimPrefix(es.ID, oldID)
		if topic, ok := es.Properties["topic"].(string); ok && strings.HasPrefix(topic, oldID) {
			es.Properties["topic"] = newID + strings.TrimPrefix(topic, oldID)
		}
		eventGridSubscriptions.Put(es.ID, es)
	}
}
