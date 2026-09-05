package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
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

// A workload joins a load balancer's or an application gateway's backend
// through its own network interface, so both pools are answered by one index
// over the interfaces. The lookup runs in a handler wrapper for the gateway,
// so every request into the simulator paid the scan it replaces.
func TestBackendPoolMembersMatchTheInterfaceScan(t *testing.T) {
	azureNICs = sim.MakeStore[NetworkInterface](nil, "test_index_nics")
	t.Cleanup(func() { azureNICs = nil })

	const lbPool = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Network/loadBalancers/lb/backendAddressPools/web"
	const gwPool = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Network/applicationGateways/gw/backendAddressPools/api"
	const otherPool = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Network/loadBalancers/lb/backendAddressPools/idle"

	nic := func(name, address string, lbPools, gwPools []string) {
		var lbRefs []LoadBalancerChild
		for _, id := range lbPools {
			lbRefs = append(lbRefs, LoadBalancerChild{ID: id})
		}
		var gwRefs []SubResource
		for _, id := range gwPools {
			gwRefs = append(gwRefs, SubResource{ID: id})
		}
		azureNICs.Put(name, NetworkInterface{
			ID:   "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Network/networkInterfaces/" + name,
			Name: name,
			Properties: NetworkInterfaceProperties{
				IPConfigurations: []NetworkInterfaceIPConfiguration{{
					Name: "ipconfig1",
					Properties: NetworkInterfaceIPConfigurationProperties{
						PrivateIPAddress:                      address,
						LoadBalancerBackendAddressPools:       lbRefs,
						ApplicationGatewayBackendAddressPools: gwRefs,
					},
				}},
			},
		})
	}
	nic("nic-1", "10.0.0.4", []string{lbPool}, nil)
	nic("nic-2", "10.0.0.5", []string{lbPool}, []string{gwPool})
	nic("nic-3", "10.0.0.6", []string{otherPool}, nil)
	nic("nic-4", "10.0.0.7", nil, nil)

	// The oracle: the scan each caller performed over every interface.
	scan := func(poolID string) []string {
		var names []string
		for _, n := range azureNICs.List() {
			for _, ipcfg := range n.Properties.IPConfigurations {
				inPool := false
				for _, ref := range ipcfg.Properties.LoadBalancerBackendAddressPools {
					if strings.EqualFold(ref.ID, poolID) {
						inPool = true
					}
				}
				for _, ref := range ipcfg.Properties.ApplicationGatewayBackendAddressPools {
					if strings.EqualFold(ref.ID, poolID) {
						inPool = true
					}
				}
				if inPool {
					names = append(names, n.Name)
				}
			}
		}
		return names
	}

	// ARM compares resource identifiers case-insensitively, and a caller may
	// hold the pool identifier in either case.
	for _, poolID := range []string{lbPool, gwPool, otherPool, strings.ToUpper(lbPool), "absent"} {
		var got []string
		for _, n := range azureNICsInBackendPool(poolID) {
			got = append(got, n.Name)
		}
		requireSameOrEmpty(t, "members of "+poolID, scan(poolID), got)
	}

	// The gateway's own accessor returns the IP configurations, not the
	// interfaces, and only those that named this pool.
	members := applicationGatewayPoolMemberIPConfigurations(gwPool)
	if len(members) != 1 || members[0].Properties.PrivateIPAddress != "10.0.0.5" {
		t.Errorf("the gateway pool holds exactly nic-2's configuration, got %+v", members)
	}
	if got := applicationGatewayPoolMemberIPConfigurations(""); got != nil {
		t.Errorf("an empty pool identifier matches nothing, got %+v", got)
	}
}

// requireSameOrEmpty compares a scan and an index answer, allowing the empty
// case the previous helper rejects — here a pool with no members is a case
// worth proving rather than a sign the fixture missed.
func requireSameOrEmpty(t *testing.T, what string, want, got []string) {
	t.Helper()
	sorted := func(in []string) string {
		out := append([]string(nil), in...)
		sort.Strings(out)
		return strings.Join(out, "\n")
	}
	if sorted(want) != sorted(got) {
		t.Errorf("%s:\nscan  %v\nindex %v", what, want, got)
	}
}

// A publish delivers to the subscriptions of one scope, and a subscription
// belongs to its scope in either of two ways: its resource identifier hangs
// off that scope, or its properties name the topic. The delivery index is
// keyed on both, from the same function the predicate answers with — and the
// identifier-derived half had no test of its own, so a subscription that
// carries no `topic` property is the case proved here.
func TestEventGridDeliversToASubscriptionKnownOnlyByItsIdentifier(t *testing.T) {
	eventGridSubscriptions = sim.MakeStore[EventGridEventSubscription](nil, "test_index_eg_subs")
	t.Cleanup(func() { eventGridSubscriptions = nil })

	delivered := make(chan string, 4)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		delivered <- string(body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hook.Close)

	const topicID = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/topics/eg-index"
	const otherID = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/topics/eg-other"
	destination := map[string]any{
		"endpointType": "WebHook",
		"properties":   map[string]any{"endpointUrl": hook.URL},
	}
	// Known only by its identifier: no "topic" property at all.
	byIdentifier := topicID + "/providers/Microsoft.EventGrid/eventSubscriptions/by-id"
	eventGridSubscriptions.Put(byIdentifier, EventGridEventSubscription{
		ID: byIdentifier, Name: "by-id",
		Properties: map[string]any{"destination": destination},
	})
	// A sibling topic's subscription, which this publish must not reach.
	otherSub := otherID + "/providers/Microsoft.EventGrid/eventSubscriptions/other"
	eventGridSubscriptions.Put(otherSub, EventGridEventSubscription{
		ID: otherSub, Name: "other",
		Properties: map[string]any{"destination": destination},
	})

	if !eventGridSubscriptionBelongsToTopic(eventGridSubscriptionAt(t, byIdentifier), topicID) {
		t.Fatal("a subscription whose identifier hangs off the topic belongs to it")
	}
	if eventGridSubscriptionBelongsToTopic(eventGridSubscriptionAt(t, otherSub), topicID) {
		t.Fatal("a sibling topic's subscription does not belong to this topic")
	}

	deliverEventGridBatch(topicID, []byte(`[{"id":"evt-1"}]`))
	select {
	case body := <-delivered:
		if !strings.Contains(body, "evt-1") {
			t.Errorf("the webhook received %q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the subscription known only by its identifier received no delivery")
	}
	// Exactly one subscription matched, so nothing else arrives.
	select {
	case extra := <-delivered:
		t.Errorf("a sibling topic's subscription was also delivered to: %q", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

func eventGridSubscriptionAt(t *testing.T, id string) EventGridEventSubscription {
	t.Helper()
	es, ok := eventGridSubscriptions.Get(id)
	if !ok {
		t.Fatalf("the fixture must hold %s", id)
	}
	return es
}
