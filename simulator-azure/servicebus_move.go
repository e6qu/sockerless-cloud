package main

import (
	"strings"
)

// Cross-resource-group move for Microsoft.ServiceBus/namespaces. The hook
// table in resource_move.go dispatches Resources_MoveResources here.
//
// A Service Bus namespace is addressed on two planes, and only one of them
// carries the resource group. The ARM plane keys the namespace record — and
// every queue, topic, subscription, rule, authorization rule, network rule
// set, private endpoint connection, disaster-recovery alias and migration
// configuration beneath it — by resource ID, so the whole subtree re-keys onto
// the destination group. The data plane addresses the namespace by its
// globally unique name through the host `<namespace>.servicebus.<suffix>`: the
// queue and subscription message stores key on that name, and a move changes
// neither, so a message enqueued before the move is still there after it.
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

// moveServiceBusNamespaceARM re-homes one Service Bus namespace's ARM plane
// onto a new resource ID, pinning its authorization rules' key material so the
// connection strings issued before the move keep authenticating after it.
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

// pinServiceBusAuthRuleKeys carries the current key material of every
// authorization rule under a namespace — the namespace's own rules and the
// per-queue and per-topic ones — onto the rule IDs the move is about to
// create, so listKeys serves the same keys and connection strings and the data
// plane accepts a Shared Access Signature signed before the move.
func pinServiceBusAuthRuleKeys(oldID, newID string) {
	for _, rule := range sbAuthRules.List() {
		if !strings.HasPrefix(rule.ID, oldID+"/") {
			continue
		}
		pinAzureKeySlots(rule.ID, newID+strings.TrimPrefix(rule.ID, oldID), azureKeyMaterial32, "primary", "secondary")
	}
}
