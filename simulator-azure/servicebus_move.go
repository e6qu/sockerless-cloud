package main

import (
	"strings"
)

// Cross-resource-group move for Microsoft.ServiceBus/namespaces (dispatched
// from resource_move.go). The ARM subtree keys by resource ID and re-keys onto
// the destination group; the data plane addresses the namespace by its
// globally unique name (`<namespace>.servicebus.<suffix>`), and the message
// stores key on that name, so messages survive the move.
//
// Each rule's SAS keys derive from the rule's resource ID, yet Azure never
// rotates keys on a move: pin the served material onto the moved rule IDs
// first. The pin is the one an explicit RegenerateKeys value uses, so the next
// rotation clears it.

func moveServiceBusNamespaceARM(oldID, newID string) {
	ns, ok := sbNamespaces.Get(oldID)
	if !ok {
		return
	}

	pinServiceBusAuthRuleKeys(oldID, newID)

	sbNamespaces.Delete(oldID)
	ns.ID = newID
	sbNamespaces.Put(ns.ID, ns)

	oldSub, newSub := oldID+"/", newID+"/"
	rekeyRowsByPrefix(sbQueues, oldSub, newSub, func(q *SBQueue) *string { return &q.ID })
	rekeyRowsByPrefix(sbTopics, oldSub, newSub, func(t *SBTopic) *string { return &t.ID })
	rekeyRowsByPrefix(sbSubscriptions, oldSub, newSub, func(s *SBSubscription) *string { return &s.ID })
	rekeyRowsByPrefix(sbRules, oldSub, newSub, func(r *SBRule) *string { return &r.ID })
	rekeyRowsByPrefix(sbAuthRules, oldSub, newSub, func(a *SBAuthorizationRule) *string { return &a.ID })
	rekeyRowsByPrefix(sbNetworkRules, oldSub, newSub, func(n *SBNetworkRuleSet) *string { return &n.ID })
	rekeyRowsByPrefix(sbPrivateConns, oldSub, newSub, func(p *SBPrivateEndpointConnection) *string { return &p.ID })
	rekeyRowsByPrefix(sbDRConfigs, oldSub, newSub, func(d *SBDisasterRecovery) *string { return &d.ID })
	rekeyRowsByPrefix(sbMigrations, oldSub, newSub, func(m *SBMigrationConfig) *string { return &m.ID })
}

// The namespace's rules and each queue's and topic's, so listKeys and SAS
// tokens signed before the move keep working.
func pinServiceBusAuthRuleKeys(oldID, newID string) {
	for _, rule := range sbAuthRules.List() {
		if !strings.HasPrefix(rule.ID, oldID+"/") {
			continue
		}
		pinAzureKeySlots(rule.ID, newID+strings.TrimPrefix(rule.ID, oldID), azureKeyMaterial32, "primary", "secondary")
	}
}
