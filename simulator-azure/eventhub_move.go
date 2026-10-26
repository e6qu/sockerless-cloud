package main

import (
	"strings"
)

// Cross-resource-group move for Microsoft.EventHub/namespaces (dispatched from
// resource_move.go). The ARM subtree keys by resource ID and re-keys onto the
// destination group; the data plane addresses the namespace by its globally
// unique name (`<namespace>.servicebus.<suffix>`), and the partition logs key
// on that name, so events survive the move.
//
// Each authorization rule's keys derive from the rule's resource ID, yet Azure
// never rotates keys on a move: pin the served material onto the moved rule
// IDs first. The pin is the one an explicit RegenerateKeys value uses, so the
// next rotation clears it.

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

// Pin the namespace's rules and each event hub's onto the new rule IDs, so
// listKeys and SAS tokens signed before the move keep working.
func pinEventHubAuthRuleKeys(oldID, newID string) {
	for _, rule := range ehAuthRules.List() {
		if !strings.HasPrefix(rule.ID, oldID+"/") {
			continue
		}
		pinAzureKeySlots(rule.ID, newID+strings.TrimPrefix(rule.ID, oldID), azureKeyMaterial32, "primary", "secondary")
	}
}
