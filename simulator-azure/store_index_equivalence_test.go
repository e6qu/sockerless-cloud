package main

import (
	"sort"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// The generation-keyed indexes replaced scans that decoded a whole store on
// every request. An index is only allowed to be faster — it must answer
// exactly what the scan answered — so each case here computes the answer both
// ways over the same rows and requires them to agree, rather than asserting a
// hand-written expectation the index and the scan could drift away from
// together.

// seedMessagingRuleStores wires the two authorization-rule stores the
// messaging host authenticates against, and seeds rules whose identifiers
// include the shapes that make a "find by suffix" lookup subtle: a namespace
// and an entity rule of the same name, a same-named rule in a second
// namespace, and a resource group literally called "namespaces".
func seedMessagingRuleStores(t *testing.T) {
	t.Helper()
	sbAuthRules = sim.MakeStore[SBAuthorizationRule](nil, "test_index_sb_auth_rules")
	ehAuthRules = sim.MakeStore[EHAuthorizationRule](nil, "test_index_eh_auth_rules")
	t.Cleanup(func() { sbAuthRules, ehAuthRules = nil, nil })

	const base = "/subscriptions/sub-1/resourceGroups/rg-1/providers/Microsoft.ServiceBus"
	// A resource group of this name puts a second "/namespaces/" earlier in
	// the identifier than the one that scopes the rule.
	const tricky = "/subscriptions/sub-1/resourceGroups/namespaces/providers/Microsoft.ServiceBus"
	for _, id := range []string{
		base + "/namespaces/ns-1/authorizationRules/send",
		base + "/namespaces/ns-1/queues/orders/authorizationRules/send",
		base + "/namespaces/ns-1/topics/events/authorizationRules/send",
		base + "/namespaces/ns-2/authorizationRules/send",
		base + "/namespaces/ns-1/queues/orders/authorizationRules/listen",
		tricky + "/namespaces/ns-3/authorizationRules/send",
		tricky + "/namespaces/ns-3/queues/orders/authorizationRules/send",
		// A queue of this name puts a "/namespaces/" occurrence *after* the
		// one that scopes the rule, so resolving the segment by its last
		// occurrence is wrong here just as resolving it by its first is wrong
		// for the resource group above.
		base + "/namespaces/ns-4/queues/namespaces/authorizationRules/send",
		base + "/namespaces/ns-4/authorizationRules/send",
	} {
		sbAuthRules.Put(id, SBAuthorizationRule{ID: id, Name: id[strings.LastIndex(id, "/")+1:]})
	}
	const ehBase = "/subscriptions/sub-1/resourceGroups/rg-1/providers/Microsoft.EventHub"
	for _, id := range []string{
		ehBase + "/namespaces/ns-1/authorizationRules/send",
		ehBase + "/namespaces/ns-1/eventhubs/telemetry/authorizationRules/send",
	} {
		ehAuthRules.Put(id, EHAuthorizationRule{ID: id, Name: id[strings.LastIndex(id, "/")+1:]})
	}
}

// scanRuleCandidates is the full-store scan sasRuleCandidates performed before
// it was indexed, kept here as the oracle the index is checked against.
func scanRuleCandidates(namespace, entityPath, keyName string) []string {
	nsSuffix := "/namespaces/" + namespace + "/authorizationRules/" + keyName
	entity := entityPath
	if i := strings.Index(entity, "/"); i >= 0 {
		entity = entity[:i]
	}
	var entitySuffixes []string
	if entity != "" {
		for _, kind := range []string{"queues", "topics", "eventhubs"} {
			entitySuffixes = append(entitySuffixes,
				"/namespaces/"+namespace+"/"+kind+"/"+entity+"/authorizationRules/"+keyName)
		}
	}
	matches := func(id string) bool {
		if strings.HasSuffix(id, nsSuffix) {
			return true
		}
		for _, suffix := range entitySuffixes {
			if strings.HasSuffix(id, suffix) {
				return true
			}
		}
		return false
	}
	var out []string
	for _, rule := range sbAuthRules.List() {
		if matches(rule.ID) {
			out = append(out, rule.ID)
		}
	}
	for _, rule := range ehAuthRules.List() {
		if matches(rule.ID) {
			out = append(out, rule.ID)
		}
	}
	return out
}

func TestSASRuleCandidatesAnswerExactlyWhatTheScanAnswered(t *testing.T) {
	seedMessagingRuleStores(t)

	cases := []struct{ namespace, entityPath, keyName string }{
		{"ns-1", "", "send"},                       // the namespace rule alone
		{"ns-1", "orders", "send"},                 // namespace and queue rules
		{"ns-1", "events/subscriptions/a", "send"}, // a subscription is authorized by its topic
		{"ns-1", "telemetry", "send"},              // an Event Hubs entity on the shared host
		{"ns-1", "orders", "listen"},               // a second key name on the same entity
		{"ns-2", "orders", "send"},                 // a namespace with no entity rule
		{"ns-3", "orders", "send"},                 // resource group called "namespaces"
		{"ns-4", "namespaces", "send"},             // a queue called "namespaces"
		{"ns-1", "orders", "absent"},               // no rule of that name
		{"absent", "orders", "send"},               // no namespace of that name
	}
	for _, c := range cases {
		want := scanRuleCandidates(c.namespace, c.entityPath, c.keyName)
		got := sasRuleCandidates(c.namespace, c.entityPath, c.keyName)
		sort.Strings(want)
		sort.Strings(got)
		if strings.Join(want, "\n") != strings.Join(got, "\n") {
			t.Errorf("namespace %q entity %q key %q:\nscan  %v\nindex %v",
				c.namespace, c.entityPath, c.keyName, want, got)
		}
	}

	// The rule scoped to ns-3 lives under a resource group called
	// "namespaces", so an index that resolved the segment by position rather
	// than by trying every occurrence would answer nothing here.
	if got := sasRuleCandidates("ns-3", "orders", "send"); len(got) != 2 {
		t.Errorf("a namespace under a resource group called \"namespaces\" must still resolve: %v", got)
	}
	// And the mirror image: the entity rule here is only found if the segment
	// is not resolved by its last occurrence either.
	if got := sasRuleCandidates("ns-4", "namespaces", "send"); len(got) != 2 {
		t.Errorf("a queue called \"namespaces\" must still resolve its own rule: %v", got)
	}
}

// requireSameIDs reports the difference between what the scan returned and
// what the index returned, order-insensitively — the stores hand rows back in
// their own order and neither answer promises one.
func requireSameIDs(t *testing.T, what string, want, got []string) {
	t.Helper()
	if len(want) == 0 {
		t.Errorf("%s: the oracle scan matched nothing, so the case proves nothing", what)
		return
	}
	sorted := func(in []string) string {
		out := append([]string(nil), in...)
		sort.Strings(out)
		return strings.Join(out, "\n")
	}
	if sorted(want) != sorted(got) {
		t.Errorf("%s:\nscan  %v\nindex %v", what, want, got)
	}
}

// seedServiceBusChildStores builds two namespaces holding same-named children,
// so a lookup that ignored the parent would return the sibling's rows.
func seedServiceBusChildStores(t *testing.T) (nsA, nsB string) {
	t.Helper()
	sbNamespaces = sim.MakeStore[SBNamespace](nil, "test_index_sb_namespaces")
	sbQueues = sim.MakeStore[SBQueue](nil, "test_index_sb_queues")
	sbTopics = sim.MakeStore[SBTopic](nil, "test_index_sb_topics")
	sbSubscriptions = sim.MakeStore[SBSubscription](nil, "test_index_sb_subs")
	sbRules = sim.MakeStore[SBRule](nil, "test_index_sb_rules")
	t.Cleanup(func() {
		sbNamespaces, sbQueues, sbTopics, sbSubscriptions, sbRules = nil, nil, nil, nil, nil
	})

	// The admin identifiers resolve through the namespace store, so the rows
	// carry the resource identifiers a provisioned namespace has.
	for _, ns := range []string{"ns-a", "ns-b"} {
		id := sbNamespaceID("sub-1", "rg-1", ns)
		sbNamespaces.Put(id, SBNamespace{ID: id, Name: ns})
	}

	for _, ns := range []string{"ns-a", "ns-b"} {
		for _, q := range []string{"orders", "invoices"} {
			id := sbAdminQueueID(ns, q)
			sbQueues.Put(id, SBQueue{ID: id, Name: q})
		}
		for _, topic := range []string{"events", "audit"} {
			id := sbAdminTopicID(ns, topic)
			sbTopics.Put(id, SBTopic{ID: id, Name: topic})
			for _, sub := range []string{"all", "errors"} {
				subID := sbAdminSubscriptionID(ns, topic, sub)
				sbSubscriptions.Put(subID, SBSubscription{ID: subID, Name: sub})
				for _, rule := range []string{"$Default", "high"} {
					ruleID := sbAdminRuleID(ns, topic, sub, rule)
					sbRules.Put(ruleID, SBRule{ID: ruleID, Name: rule})
				}
			}
		}
	}
	return "ns-a", "ns-b"
}

func TestServiceBusChildLookupsMatchTheirPrefixScan(t *testing.T) {
	nsA, nsB := seedServiceBusChildStores(t)

	// Each helper is checked against the HasPrefix scan it replaced.
	queuePrefix := sbAdminNamespaceID(nsA) + "/queues/"
	var wantQueues []string
	for _, q := range sbQueues.List() {
		if strings.HasPrefix(q.ID, queuePrefix) {
			wantQueues = append(wantQueues, q.ID)
		}
	}
	var gotQueues []string
	for _, q := range sbQueuesUnder(queuePrefix) {
		gotQueues = append(gotQueues, q.ID)
	}
	requireSameIDs(t, "queues of "+nsA, wantQueues, gotQueues)

	subPrefix := sbAdminTopicID(nsA, "events") + "/subscriptions/"
	var wantSubs []string
	for _, s := range sbSubscriptions.List() {
		if strings.HasPrefix(s.ID, subPrefix) {
			wantSubs = append(wantSubs, s.ID)
		}
	}
	var gotSubs []string
	for _, s := range sbSubscriptionsUnder(subPrefix) {
		gotSubs = append(gotSubs, s.ID)
	}
	requireSameIDs(t, "subscriptions of "+nsA+"/events", wantSubs, gotSubs)

	// A rule is indexed under every prefix of its identifier, so the same
	// index answers both the subscription's own rules and the topic-wide
	// cascade a topic delete performs.
	rulePrefix := sbAdminSubscriptionID(nsA, "events", "all") + "/rules/"
	if got := len(sbRulesUnder(rulePrefix)); got != 2 {
		t.Errorf("a subscription's rules: want 2, got %d", got)
	}
	topicWide := sbAdminTopicID(nsA, "events") + "/subscriptions/"
	if got := len(sbRulesUnder(topicWide)); got != 4 {
		t.Errorf("every rule under a topic's subscriptions: want 4, got %d", got)
	}

	// Nothing belonging to the sibling namespace is ever returned.
	for _, id := range gotQueues {
		if strings.Contains(id, "/namespaces/"+nsB+"/") {
			t.Errorf("a lookup scoped to %s returned %s", nsA, id)
		}
	}

	// A topic delete cascades over its own children only.
	topicID := sbAdminTopicID(nsA, "events")
	for _, sub := range sbSubscriptionsUnder(topicID + "/subscriptions/") {
		sbSubscriptions.Delete(sub.ID)
	}
	for _, rule := range sbRulesUnder(topicID + "/subscriptions/") {
		sbRules.Delete(rule.ID)
	}
	if got := len(sbSubscriptionsUnder(sbAdminTopicID(nsA, "audit") + "/subscriptions/")); got != 2 {
		t.Errorf("deleting one topic's subscriptions took another topic's: %d left", got)
	}
	if got := len(sbSubscriptionsUnder(sbAdminTopicID(nsB, "events") + "/subscriptions/")); got != 2 {
		t.Errorf("deleting one namespace's subscriptions took the other namespace's: %d left", got)
	}
	if got := len(sbRulesUnder(topicID + "/subscriptions/")); got != 0 {
		t.Errorf("the cascade left %d rules behind", got)
	}
}

func TestKeyVaultListingsAreScopedToTheirVault(t *testing.T) {
	keyVaultKeys = sim.MakeStore[kvKeyStored](nil, "test_index_kv_keys")
	keyVaultCertificates = sim.MakeStore[kvCertStored](nil, "test_index_kv_certs")
	t.Cleanup(func() { keyVaultKeys, keyVaultCertificates = nil, nil })

	// Both vaults hold a key of the same name, which is what a scan filtered
	// on and an unscoped index would confuse.
	for _, vault := range []string{"vault-a", "vault-b"} {
		for _, name := range []string{"signing", "encryption"} {
			key := keyVaultKeyKey(vault, name)
			keyVaultKeys.Put(key, kvKeyStored{Vault: vault, Name: name})
			certKey := keyVaultCertKey(vault, name)
			keyVaultCertificates.Put(certKey, kvCertStored{Vault: vault, Name: name})
		}
	}

	keys := keyVaultKeysIn("vault-a")
	if len(keys) != 2 {
		t.Fatalf("vault-a holds 2 keys, got %d", len(keys))
	}
	for _, k := range keys {
		if k.Vault != "vault-a" {
			t.Errorf("a listing of vault-a returned a key of %s", k.Vault)
		}
	}
	// The handlers relied on the scan's sort, so the helper owns it now.
	if keys[0].Name != "encryption" || keys[1].Name != "signing" {
		t.Errorf("keys must be sorted by name, got %s then %s", keys[0].Name, keys[1].Name)
	}
	certs := keyVaultCertificatesIn("vault-b")
	if len(certs) != 2 || certs[0].Name != "encryption" {
		t.Errorf("vault-b's certificates must be its own, sorted: %+v", certs)
	}

	// Sorting the returned rows must not disturb the index, which hands out
	// its own slice: a second call sees the same order.
	again := keyVaultKeysIn("vault-a")
	if again[0].Name != "encryption" || again[1].Name != "signing" {
		t.Errorf("a second listing changed order: %s then %s", again[0].Name, again[1].Name)
	}
}
