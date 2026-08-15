package main

import (
	"strings"
)

// Cross-resource-group move for Microsoft.EventGrid/topics and
// Microsoft.EventGrid/domains. The hook table in resource_move.go dispatches
// Resources_MoveResources here.
//
// A custom topic or a domain keys its ARM record — and its event
// subscriptions, and a domain's domain topics with their own subscriptions —
// by resource ID, so the whole subtree re-keys onto the destination group.
// The publish endpoint is derived from the resource's globally unique name
// rather than its resource group, so the URL a publisher posts to is unchanged.
//
// Two things a naive re-key would break:
//
//   - The credential. A topic's and a domain's two access keys are derived
//     from the resource ID, which embeds the resource group, so the material
//     listKeys serves is pinned onto the moved ID; the data plane accepts the
//     key and every Shared Access Signature signed with it exactly as before.
//     The next regenerateKey clears the pin and derives fresh material.
//   - An inbound reference. An event subscription records the resource ID of
//     the scope it belongs to in its own `topic` property, which the publish
//     fan-out matches on, so every moved subscription's property is rewritten
//     onto the destination ID alongside its key.

// moveEventGridTopicARM re-homes one Event Grid custom topic.
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

// moveEventGridDomainARM re-homes one Event Grid domain, its domain topics,
// and the event subscriptions of both.
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

// pinEventGridKeys carries a publishing resource's current access keys onto
// the resource ID the move is about to create, so listKeys serves the same
// material and the publish data plane keeps accepting the credentials an
// operator already holds.
func pinEventGridKeys(oldID, newID string) {
	pinAzureKeySlots(oldID, newID, azureKeyMaterial32, eventGridKeySlots...)
}

// moveEventGridSubscriptions re-keys every event subscription stored beneath a
// moved scope and rewrites the scope resource ID each one carries in its
// `topic` property, which is how the publish fan-out and the scoped list views
// find it.
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
