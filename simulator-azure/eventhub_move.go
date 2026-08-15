package main

import (
	"strings"
)

// Cross-resource-group move for Microsoft.EventHub/namespaces. The hook table
// in resource_move.go dispatches Resources_MoveResources here.
//
// An Event Hubs namespace is addressed on two planes, and only one of them
// carries the resource group. The ARM plane keys the namespace record — and
// every event hub, consumer group, authorization rule, network rule set and
// private endpoint connection beneath it — by resource ID, so the whole
// subtree re-keys onto the destination group. The data plane addresses the
// namespace by its globally unique name through the host
// `<namespace>.servicebus.<suffix>`: the per-partition event logs key on that
// name, and a move changes neither, so an event published before the move is
// still readable after it.
//
// What a naive re-key would break is the credential. An authorization rule's
// two Shared Access Signature keys are derived from the rule's own resource
// ID, which embeds the resource group, so moving the rule would silently
// re-derive both keys and invalidate every connection string and SAS token an
// operator already holds. Real Azure never rotates a namespace's keys on a
// move, so the material each rule currently serves is pinned onto the moved
// rule ID before the subtree re-keys. The pin is the same one an explicit
// RegenerateKeys value uses, so the next rotation clears it and returns the
// slot to derived material — the rotation contract is unchanged by the move.

// moveEventHubNamespaceARM re-homes one Event Hubs namespace's ARM plane onto
// a new resource ID, pinning its authorization rules' key material so the
// connection strings issued before the move keep authenticating after it.
func moveEventHubNamespaceARM(oldID, newID string) {
	ns, ok := ehNamespaces.Get(oldID)
	if !ok {
		return
	}

	pinEventHubAuthRuleKeys(oldID, newID)

	ehNamespaces.Delete(oldID)
	ns.ID = newID
	ehNamespaces.Put(ns.ID, ns)

	oldSub, newSub := oldID+"/", newID+"/"
	rekeyRowsByPrefix(ehEventHubs, oldSub, newSub, func(h *EHEventHub) *string { return &h.ID })
	rekeyRowsByPrefix(ehConsumerGroups, oldSub, newSub, func(g *EHConsumerGroup) *string { return &g.ID })
	rekeyRowsByPrefix(ehAuthRules, oldSub, newSub, func(a *EHAuthorizationRule) *string { return &a.ID })
	rekeyRowsByPrefix(ehNetworkRules, oldSub, newSub, func(n *EHNetworkRuleSet) *string { return &n.ID })
	rekeyRowsByPrefix(ehPrivateConns, oldSub, newSub, func(p *EHPrivateEndpointConnection) *string { return &p.ID })
}

// pinEventHubAuthRuleKeys carries the current key material of every
// authorization rule under a namespace — the namespace's own rules and the
// per-event-hub ones — onto the rule IDs the move is about to create, so
// listKeys serves the same keys and connection strings and the data plane
// accepts a Shared Access Signature signed before the move.
func pinEventHubAuthRuleKeys(oldID, newID string) {
	for _, rule := range ehAuthRules.List() {
		if !strings.HasPrefix(rule.ID, oldID+"/") {
			continue
		}
		pinAzureKeySlots(rule.ID, newID+strings.TrimPrefix(rule.ID, oldID), azureKeyMaterial32, "primary", "secondary")
	}
}
