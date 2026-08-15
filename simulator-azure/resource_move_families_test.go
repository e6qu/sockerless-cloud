package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// The families that joined the cross-resource-group move dispatch alongside
// Microsoft.ServiceBus all share one hazard: their credential material is
// derived from the resource ID, which embeds the resource group. Each test
// below captures the credential an operator holds before the move and proves
// it still works after it — through the data plane where the family has one
// that authenticates, and through the control-plane surface that serves the
// credential where it does not.

func moveTestResourceID(rg, provider, typeName, name string) string {
	return "/subscriptions/sub-move/resourceGroups/" + rg + "/providers/" + provider + "/" + typeName + "/" + name
}

// ---- Microsoft.EventHub/namespaces ----

func newEventHubMoveTestStores(t *testing.T) {
	t.Helper()
	ehNamespaces = sim.MakeStore[EHNamespace](nil, "test_eh_namespaces")
	ehEventHubs = sim.MakeStore[EHEventHub](nil, "test_eh_hubs")
	ehConsumerGroups = sim.MakeStore[EHConsumerGroup](nil, "test_eh_groups")
	ehAuthRules = sim.MakeStore[EHAuthorizationRule](nil, "test_eh_auth_rules")
	ehNetworkRules = sim.MakeStore[EHNetworkRuleSet](nil, "test_eh_network_rules")
	ehPrivateConns = sim.MakeStore[EHPrivateEndpointConnection](nil, "test_eh_private_conns")
	sbAuthRules = sim.MakeStore[SBAuthorizationRule](nil, "test_sb_auth_rules_for_eh")
	azureKeyGens = sim.MakeStore[azureKeyGenRow](nil, "test_key_gens")
	t.Cleanup(func() {
		ehNamespaces, ehEventHubs, ehConsumerGroups, ehAuthRules = nil, nil, nil, nil
		ehNetworkRules, ehPrivateConns, sbAuthRules, azureKeyGens = nil, nil, nil, nil
	})
}

// TestEventHubMovePinsAuthorizationRuleKeys drives the Microsoft.EventHub hook
// against the data plane's own Shared Access Signature verification: a token
// signed before the move with the namespace's root key still authenticates
// after it, and the whole ARM subtree answers under the destination group.
func TestEventHubMovePinsAuthorizationRuleKeys(t *testing.T) {
	newEventHubMoveTestStores(t)

	const namespace = "eh-move-ns"
	oldID := moveTestResourceID("eh-src", "Microsoft.EventHub", "namespaces", namespace)
	newID := moveTestResourceID("eh-dst", "Microsoft.EventHub", "namespaces", namespace)

	ehNamespaces.Put(oldID, EHNamespace{ID: oldID, Name: namespace, Type: "Microsoft.EventHub/namespaces", Location: "eastus"})
	rootRuleID := oldID + "/authorizationRules/RootManageSharedAccessKey"
	ehAuthRules.Put(rootRuleID, EHAuthorizationRule{
		ID: rootRuleID, Name: "RootManageSharedAccessKey",
		Type:       "Microsoft.EventHub/namespaces/authorizationRules",
		Properties: map[string]any{"rights": []any{"Listen", "Send", "Manage"}},
	})
	hubID := oldID + "/eventhubs/telemetry"
	ehEventHubs.Put(hubID, EHEventHub{ID: hubID, Name: "telemetry", Type: "Microsoft.EventHub/namespaces/eventhubs"})
	groupID := hubID + "/consumergroups/readers"
	ehConsumerGroups.Put(groupID, EHConsumerGroup{ID: groupID, Name: "readers", Type: "Microsoft.EventHub/namespaces/eventhubs/consumergroups"})
	hubRuleID := hubID + "/authorizationRules/sender"
	ehAuthRules.Put(hubRuleID, EHAuthorizationRule{
		ID: hubRuleID, Name: "sender", Type: "Microsoft.EventHub/namespaces/eventhubs/authorizationRules",
		Properties: map[string]any{"rights": []any{"Send"}},
	})

	// The credential an operator holds, captured before the move.
	rootKeyBefore := azureKeyMaterial32(rootRuleID, "primary")
	hubKeyBefore := azureKeyMaterial32(hubRuleID, "primary")
	audience := "amqps://" + namespace + ".servicebus.localhost/telemetry"
	token := serviceBusMoveTestToken("RootManageSharedAccessKey", rootKeyBefore, audience)

	moveEventHubNamespaceARM(oldID, newID)

	if _, ok := ehNamespaces.Get(oldID); ok {
		t.Fatal("the namespace still answers under the source group")
	}
	for _, moved := range []string{newID, newID + "/eventhubs/telemetry", newID + "/eventhubs/telemetry/consumergroups/readers"} {
		if !eventHubMoveRecordExists(moved) {
			t.Fatalf("record %q did not move onto the destination group", moved)
		}
	}

	movedRootRule := newID + "/authorizationRules/RootManageSharedAccessKey"
	if got := azureKeyMaterial32(movedRootRule, "primary"); got != rootKeyBefore {
		t.Fatalf("the move rotated the namespace root key: %q != %q", got, rootKeyBefore)
	}
	movedHubRule := newID + "/eventhubs/telemetry/authorizationRules/sender"
	if got := azureKeyMaterial32(movedHubRule, "primary"); got != hubKeyBefore {
		t.Fatalf("the move rotated an event hub rule key: %q != %q", got, hubKeyBefore)
	}

	// The proof that matters: the token signed before the move still verifies
	// against the moved rule's material through the data plane's own path.
	granted, err := verifyMessagingSAS(namespace, token)
	if err != nil {
		t.Fatalf("a Shared Access Signature issued before the move must still authenticate: %v", err)
	}
	if !sasAudienceCoversEntity(granted, "telemetry") {
		t.Fatalf("granted audience %q does not cover the event hub it was signed for", granted)
	}

	// The rotation contract is unchanged: regenerating clears the pin.
	azureBumpKeyGen(movedRootRule, "primary", "")
	if got := azureKeyMaterial32(movedRootRule, "primary"); got == rootKeyBefore {
		t.Fatal("regenerating the moved rule's key returned the pinned material")
	}
	if _, err := verifyMessagingSAS(namespace, token); err == nil {
		t.Fatal("a token signed with the rotated key must stop authenticating")
	}
}

func eventHubMoveRecordExists(id string) bool {
	if _, ok := ehNamespaces.Get(id); ok {
		return true
	}
	if _, ok := ehEventHubs.Get(id); ok {
		return true
	}
	_, ok := ehConsumerGroups.Get(id)
	return ok
}

// ---- Microsoft.Web/sites ----

// TestWebSiteMovePinsSiteDerivedCredentials covers the two credentials a
// Microsoft.Web site derives from its own resource ID: the publishing password
// the deployment surfaces serve, and the access key a hosted workflow signs its
// callback URLs with. Both are pinned across the move, so the publish profile
// and the callback URL an operator already holds keep working. The full
// simulator is built here because the site hook re-keys the whole
// Microsoft.Web child subtree, which is more stores than a seeded subset.
func TestWebSiteMovePinsSiteDerivedCredentials(t *testing.T) {
	if _, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"}); err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}

	const siteName = "web-move-site"
	oldID := moveTestResourceID("web-src", "Microsoft.Web", "sites", siteName)
	newID := moveTestResourceID("web-dst", "Microsoft.Web", "sites", siteName)
	azfSites.Put(oldID, Site{ID: oldID, Name: siteName, Type: "Microsoft.Web/sites", Location: "eastus"})

	workflowID := oldID + "/workflows/orders"
	logicWorkflows.Put(workflowID, LogicWorkflow{ID: workflowID, Name: "orders", Type: "Microsoft.Logic/workflows"})

	const callbackPath = "/triggers/manual/run"
	publishingBefore := azureKeyMaterial32(oldID, "publishingPassword")
	signatureBefore := logicCallbackSignature(workflowID, callbackPath)

	webMoveSiteTree(oldID, newID, "web-dst")

	if _, ok := azfSites.Get(newID); !ok {
		t.Fatal("the site did not move onto the destination group")
	}
	if got := azureKeyMaterial32(newID, "publishingPassword"); got != publishingBefore {
		t.Fatalf("the move rotated the site's publishing password: %q != %q", got, publishingBefore)
	}
	movedWorkflowID := newID + strings.TrimPrefix(workflowID, oldID)
	if _, ok := logicWorkflows.Get(movedWorkflowID); !ok {
		t.Fatal("the hosted workflow did not move with the site")
	}
	if got := logicCallbackSignature(movedWorkflowID, callbackPath); got != signatureBefore {
		t.Fatal("the move invalidated the callback URL signature the workflow had already issued")
	}

	azureBumpKeyGen(newID, "publishingPassword", "")
	if got := azureKeyMaterial32(newID, "publishingPassword"); got == publishingBefore {
		t.Fatal("regenerating the moved site's publishing password returned the pinned material")
	}
}

// ---- Microsoft.Cache/redis ----

func newRedisMoveTestStores(t *testing.T) {
	t.Helper()
	redisCaches = sim.MakeStore[RedisCache](nil, "test_redis_caches")
	redisFirewallRules = sim.MakeStore[RedisFirewallRule](nil, "test_redis_firewall_rules")
	redisAccessPolicies = sim.MakeStore[RedisSubResource](nil, "test_redis_access_policies")
	redisAccessPolicyAssignments = sim.MakeStore[RedisSubResource](nil, "test_redis_assignments")
	redisLinkedServers = sim.MakeStore[RedisSubResource](nil, "test_redis_linked_servers")
	redisPatchSchedules = sim.MakeStore[RedisSubResource](nil, "test_redis_patch_schedules")
	redisPrivateConns = sim.MakeStore[RedisSubResource](nil, "test_redis_private_conns")
	azureKeyGens = sim.MakeStore[azureKeyGenRow](nil, "test_key_gens")
	t.Cleanup(func() {
		redisCaches, redisFirewallRules, redisAccessPolicies = nil, nil, nil
		redisAccessPolicyAssignments, redisLinkedServers = nil, nil
		redisPatchSchedules, redisPrivateConns, azureKeyGens = nil, nil, nil
	})
}

// TestRedisMovePinsAccessKeys drives the Microsoft.Cache hook. Azure Cache for
// Redis has no data plane in this simulator — the slice serves the ARM
// lifecycle only — so the credential proof is the strongest one the surface
// supports: the access keys listKeys serves, which are the password a Redis
// client authenticates with, are identical before and after the move.
func TestRedisMovePinsAccessKeys(t *testing.T) {
	newRedisMoveTestStores(t)

	const cache = "redis-move-cache"
	oldID := moveTestResourceID("redis-src", "Microsoft.Cache", "Redis", cache)
	newID := moveTestResourceID("redis-dst", "Microsoft.Cache", "Redis", cache)

	redisCaches.Put(oldID, RedisCache{ID: oldID, Name: cache, Type: "Microsoft.Cache/Redis", Location: "eastus"})
	ruleID := oldID + "/firewallRules/office"
	redisFirewallRules.Put(ruleID, RedisFirewallRule{ID: ruleID, Name: "office", Type: "Microsoft.Cache/Redis/firewallRules"})
	policyID := oldID + "/accessPolicies/reader"
	redisAccessPolicies.Put(policyID, RedisSubResource{ID: policyID, Name: "reader", Type: "Microsoft.Cache/Redis/accessPolicies"})
	scheduleID := oldID + "/patchSchedules/default"
	redisPatchSchedules.Put(scheduleID, RedisSubResource{ID: scheduleID, Name: "default", Type: "Microsoft.Cache/Redis/patchSchedules"})

	primaryBefore := azureKeyMaterial32(oldID, "primary")
	secondaryBefore := azureKeyMaterial32(oldID, "secondary")

	moveRedisCacheARM(oldID, newID)

	if _, ok := redisCaches.Get(oldID); ok {
		t.Fatal("the cache still answers under the source group")
	}
	if _, ok := redisCaches.Get(newID); !ok {
		t.Fatal("the cache did not move onto the destination group")
	}
	if _, ok := redisFirewallRules.Get(newID + "/firewallRules/office"); !ok {
		t.Fatal("the firewall rule did not move with the cache")
	}
	if _, ok := redisAccessPolicies.Get(newID + "/accessPolicies/reader"); !ok {
		t.Fatal("the access policy did not move with the cache")
	}
	if _, ok := redisPatchSchedules.Get(newID + "/patchSchedules/default"); !ok {
		t.Fatal("the patch schedule did not move with the cache")
	}
	if got := azureKeyMaterial32(newID, "primary"); got != primaryBefore {
		t.Fatalf("the move rotated the primary access key: %q != %q", got, primaryBefore)
	}
	if got := azureKeyMaterial32(newID, "secondary"); got != secondaryBefore {
		t.Fatalf("the move rotated the secondary access key: %q != %q", got, secondaryBefore)
	}

	azureBumpKeyGen(newID, "primary", "")
	if got := azureKeyMaterial32(newID, "primary"); got == primaryBefore {
		t.Fatal("regenerating the moved cache's key returned the pinned material")
	}
}

// ---- Microsoft.ContainerRegistry/registries ----

func newACRMoveTestStores(t *testing.T) {
	t.Helper()
	acrRegistries = sim.MakeStore[Registry](nil, "test_acr_registries")
	acrCacheRules = sim.MakeStore[ACRCacheRule](nil, "test_acr_cache_rules")
	acrWebhooks = sim.MakeStore[acrWebhookStored](nil, "test_acr_webhooks")
	acrRuns = sim.MakeStore[acrRun](nil, "test_acr_runs")
	replications := sim.MakeStore[acrSubResource](nil, "test_acr_replications")
	acrMovableChildStores = []sim.Store[acrSubResource]{replications}
	azureKeyGens = sim.MakeStore[azureKeyGenRow](nil, "test_key_gens")
	t.Cleanup(func() {
		acrRegistries, acrCacheRules, acrWebhooks, acrRuns = nil, nil, nil, nil
		acrMovableChildStores, azureKeyGens = nil, nil
	})
}

// TestACRMovePinsAdminCredentials drives the Microsoft.ContainerRegistry hook.
// The registry's admin passwords are derived from the resource ID, so the move
// pins them: listCredentials serves the same username and passwords, and the
// whole child subtree — including a task run, which is stored under its own
// run identifier but reports a resource ID naming the registry — re-homes.
func TestACRMovePinsAdminCredentials(t *testing.T) {
	newACRMoveTestStores(t)

	const registry = "acrmovereg"
	oldID := moveTestResourceID("acr-src", "Microsoft.ContainerRegistry", "registries", registry)
	newID := moveTestResourceID("acr-dst", "Microsoft.ContainerRegistry", "registries", registry)

	acrRegistries.Put(oldID, Registry{ID: oldID, Name: registry, Type: "Microsoft.ContainerRegistry/registries", Location: "eastus"})
	replicationID := oldID + "/replications/westus"
	acrMovableChildStores[0].Put(replicationID, acrSubResource{ID: replicationID, Name: "westus", Type: "Microsoft.ContainerRegistry/registries/replications"})
	cacheRuleID := oldID + "/cacheRules/alpine"
	acrCacheRules.Put(cacheRuleID, ACRCacheRule{ID: cacheRuleID, Name: "alpine", Type: "Microsoft.ContainerRegistry/registries/cacheRules"})
	webhookID := oldID + "/webhooks/pushes"
	acrWebhooks.Put(webhookID, acrWebhookStored{ID: webhookID, Name: "pushes", Type: "Microsoft.ContainerRegistry/registries/webhooks"})
	acrRuns.Put("cb0001", acrRun{
		ID: oldID + "/runs/cb0001", Name: "cb0001", Type: "Microsoft.ContainerRegistry/registries/runs",
		Properties: acrRunProperties{RunID: "cb0001", Status: "Succeeded"},
	})

	credentialsBefore := acrAdminCredentialsBody(oldID, registry)

	moveACRRegistryARM(oldID, newID)

	if _, ok := acrRegistries.Get(oldID); ok {
		t.Fatal("the registry still answers under the source group")
	}
	if _, ok := acrRegistries.Get(newID); !ok {
		t.Fatal("the registry did not move onto the destination group")
	}
	if _, ok := acrMovableChildStores[0].Get(newID + "/replications/westus"); !ok {
		t.Fatal("the replication did not move with the registry")
	}
	if _, ok := acrCacheRules.Get(newID + "/cacheRules/alpine"); !ok {
		t.Fatal("the cache rule did not move with the registry")
	}
	if _, ok := acrWebhooks.Get(newID + "/webhooks/pushes"); !ok {
		t.Fatal("the webhook did not move with the registry")
	}
	run, ok := acrRuns.Get("cb0001")
	if !ok || run.ID != newID+"/runs/cb0001" {
		t.Fatalf("the task run still reports %q, want it under the destination group", run.ID)
	}

	credentialsAfter := acrAdminCredentialsBody(newID, registry)
	if credentialsAfter["username"] != credentialsBefore["username"] {
		t.Fatal("the move changed the registry's admin username")
	}
	before := credentialsBefore["passwords"].([]map[string]string)
	after := credentialsAfter["passwords"].([]map[string]string)
	for i := range before {
		if before[i]["value"] != after[i]["value"] {
			t.Fatalf("the move rotated admin %s", before[i]["name"])
		}
	}

	azureBumpKeyGen(newID, "password", "")
	rotated := acrAdminCredentialsBody(newID, registry)["passwords"].([]map[string]string)
	if rotated[0]["value"] == before[0]["value"] {
		t.Fatal("regenerating the moved registry's password returned the pinned material")
	}
}

// ---- Microsoft.EventGrid/topics and /domains ----

func newEventGridMoveTestStores(t *testing.T) {
	t.Helper()
	eventGridTopics = sim.MakeStore[EventGridTopic](nil, "test_eg_topics")
	eventGridDomains = sim.MakeStore[EventGridTopic](nil, "test_eg_domains")
	eventGridDomainTopics = sim.MakeStore[EventGridTopic](nil, "test_eg_domain_topics")
	eventGridSubscriptions = sim.MakeStore[EventGridEventSubscription](nil, "test_eg_subscriptions")
	azureKeyGens = sim.MakeStore[azureKeyGenRow](nil, "test_key_gens")
	t.Cleanup(func() {
		eventGridTopics, eventGridDomains, eventGridDomainTopics = nil, nil, nil
		eventGridSubscriptions, azureKeyGens = nil, nil
	})
}

// TestEventGridTopicMovePinsKeysAndRewritesSubscriptionTopic drives the
// Microsoft.EventGrid hook against the publish data plane's own credential
// check. A topic's access keys are derived from its resource ID, and its event
// subscriptions carry that same ID in their `topic` property, so the move has
// to pin the one and rewrite the other.
func TestEventGridTopicMovePinsKeysAndRewritesSubscriptionTopic(t *testing.T) {
	newEventGridMoveTestStores(t)

	const topic = "eg-move-topic"
	oldID := moveTestResourceID("eg-src", "Microsoft.EventGrid", "topics", topic)
	newID := moveTestResourceID("eg-dst", "Microsoft.EventGrid", "topics", topic)
	endpoint := "http://" + topic + ".eventgrid.localhost:4568/api/events"

	eventGridTopics.Put(oldID, EventGridTopic{
		ID: oldID, Name: topic, Type: "Microsoft.EventGrid/topics", Location: "eastus",
		Properties: map[string]any{"provisioningState": "Succeeded", "endpoint": endpoint},
	})
	subscriptionID := oldID + "/providers/Microsoft.EventGrid/eventSubscriptions/orders"
	eventGridSubscriptions.Put(subscriptionID, EventGridEventSubscription{
		ID: subscriptionID, Name: "orders", Type: "Microsoft.EventGrid/eventSubscriptions",
		Properties: map[string]any{"provisioningState": "Succeeded", "topic": oldID},
	})

	keyBefore := eventGridKeyMaterial(oldID, "key1")
	tokenBefore := eventGridSASFor(t, endpoint, time.Now().Add(time.Hour), keyBefore)
	publish := httptest.NewRequest("POST", endpoint+"?api-version=2018-01-01", nil)
	publish.Host = topic + ".eventgrid.localhost:4568"

	moveEventGridTopicARM(oldID, newID)

	if _, ok := eventGridTopics.Get(oldID); ok {
		t.Fatal("the topic still answers under the source group")
	}
	if _, ok := eventGridTopics.Get(newID); !ok {
		t.Fatal("the topic did not move onto the destination group")
	}

	movedSubscriptionID := newID + "/providers/Microsoft.EventGrid/eventSubscriptions/orders"
	moved, ok := eventGridSubscriptions.Get(movedSubscriptionID)
	if !ok {
		t.Fatal("the event subscription did not move with the topic")
	}
	if moved.Properties["topic"] != newID {
		t.Fatalf("the subscription still names %v as its topic, want the destination ID", moved.Properties["topic"])
	}
	if !eventGridSubscriptionBelongsToTopic(moved, newID) {
		t.Fatal("the publish fan-out no longer matches the moved subscription to its topic")
	}

	if got := eventGridKeyMaterial(newID, "key1"); got != keyBefore {
		t.Fatalf("the move rotated the topic's access key: %q != %q", got, keyBefore)
	}
	if !eventGridKeyAuthorizes(newID, keyBefore) {
		t.Fatal("the access key captured before the move no longer authorizes a publish")
	}
	if !eventGridSASAuthorizes(newID, tokenBefore, publish) {
		t.Fatal("the Shared Access Signature signed before the move no longer authorizes a publish")
	}

	azureBumpKeyGen(newID, "key1", "")
	if eventGridKeyAuthorizes(newID, keyBefore) {
		t.Fatal("regenerating the moved topic's key left the pinned material authorizing")
	}
}

// TestEventGridTopicMoveWithoutKeyPinningBreaksTheCredential is the negative
// control for the pin. It re-keys the same topic the way a move that only
// re-homed the ARM record would — the record lands at the destination, but
// nothing carries the derived key material — and shows that the key and the
// signature an operator captured before the move stop authorizing a publish.
// That failure is what the pin in moveEventGridTopicARM exists to prevent.
func TestEventGridTopicMoveWithoutKeyPinningBreaksTheCredential(t *testing.T) {
	newEventGridMoveTestStores(t)

	const topic = "eg-nopin-topic"
	oldID := moveTestResourceID("eg-nopin-src", "Microsoft.EventGrid", "topics", topic)
	newID := moveTestResourceID("eg-nopin-dst", "Microsoft.EventGrid", "topics", topic)
	endpoint := "http://" + topic + ".eventgrid.localhost:4568/api/events"

	eventGridTopics.Put(oldID, EventGridTopic{
		ID: oldID, Name: topic, Type: "Microsoft.EventGrid/topics", Location: "eastus",
		Properties: map[string]any{"endpoint": endpoint},
	})
	keyBefore := eventGridKeyMaterial(oldID, "key1")
	tokenBefore := eventGridSASFor(t, endpoint, time.Now().Add(time.Hour), keyBefore)
	publish := httptest.NewRequest("POST", endpoint+"?api-version=2018-01-01", nil)
	publish.Host = topic + ".eventgrid.localhost:4568"
	if !eventGridKeyAuthorizes(oldID, keyBefore) || !eventGridSASAuthorizes(oldID, tokenBefore, publish) {
		t.Fatal("the credential must authorize before the move")
	}

	// The move, minus the pin: the record re-keys and nothing else happens.
	record, ok := eventGridTopics.Get(oldID)
	if !ok {
		t.Fatal("seeded topic missing")
	}
	eventGridTopics.Delete(oldID)
	record.ID = newID
	eventGridTopics.Put(record.ID, record)

	if eventGridKeyMaterial(newID, "key1") == keyBefore {
		t.Fatal("the unpinned control did not actually change the derived key material, so it proves nothing")
	}
	if eventGridKeyAuthorizes(newID, keyBefore) {
		t.Error("without the pin the pre-move key still authorized; the control cannot detect a rotation")
	}
	if eventGridSASAuthorizes(newID, tokenBefore, publish) {
		t.Error("without the pin the pre-move signature still authorized; the control cannot detect a rotation")
	}
}

// TestEventGridDomainMoveCarriesItsTopicsAndSubscriptions covers the domain
// spelling of the same hook: the domain, its domain topics, and the event
// subscriptions hanging off both re-key, and the domain's publish credential
// is pinned.
func TestEventGridDomainMoveCarriesItsTopicsAndSubscriptions(t *testing.T) {
	newEventGridMoveTestStores(t)

	const domain = "eg-move-domain"
	oldID := moveTestResourceID("eg-src", "Microsoft.EventGrid", "domains", domain)
	newID := moveTestResourceID("eg-dst", "Microsoft.EventGrid", "domains", domain)

	eventGridDomains.Put(oldID, EventGridTopic{
		ID: oldID, Name: domain, Type: "Microsoft.EventGrid/domains", Location: "eastus",
		Properties: map[string]any{"provisioningState": "Succeeded"},
	})
	domainTopicID := oldID + "/topics/orders"
	eventGridDomainTopics.Put(domainTopicID, EventGridTopic{
		ID: domainTopicID, Name: "orders", Type: "Microsoft.EventGrid/domains/topics",
	})
	subscriptionID := domainTopicID + "/providers/Microsoft.EventGrid/eventSubscriptions/all"
	eventGridSubscriptions.Put(subscriptionID, EventGridEventSubscription{
		ID: subscriptionID, Name: "all", Type: "Microsoft.EventGrid/eventSubscriptions",
		Properties: map[string]any{"topic": domainTopicID},
	})

	keyBefore := eventGridKeyMaterial(oldID, "key2")

	moveEventGridDomainARM(oldID, newID)

	if _, ok := eventGridDomains.Get(newID); !ok {
		t.Fatal("the domain did not move onto the destination group")
	}
	if _, ok := eventGridDomainTopics.Get(newID + "/topics/orders"); !ok {
		t.Fatal("the domain topic did not move with the domain")
	}
	moved, ok := eventGridSubscriptions.Get(newID + "/topics/orders/providers/Microsoft.EventGrid/eventSubscriptions/all")
	if !ok {
		t.Fatal("the domain topic's event subscription did not move")
	}
	if moved.Properties["topic"] != newID+"/topics/orders" {
		t.Fatalf("the subscription still names %v as its topic", moved.Properties["topic"])
	}
	if got := eventGridKeyMaterial(newID, "key2"); got != keyBefore {
		t.Fatalf("the move rotated the domain's access key: %q != %q", got, keyBefore)
	}
}
