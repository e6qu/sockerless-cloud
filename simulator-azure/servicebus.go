package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Microsoft.ServiceBus: namespaces, queues, topics, subscriptions, rules and
// authorization rules. The REST and AMQP data planes live in
// servicebus_dataplane.go and servicebus_amqp.go.

type SBNamespace struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location,omitempty"`
	Sku        map[string]any    `json:"sku,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
}

type SBQueue struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type SBTopic struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type SBSubscription struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type SBRule struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type SBNetworkRuleSet struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type SBPrivateEndpointConnection struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type SBDisasterRecovery struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type SBMigrationConfig struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

// Azure provisions `RootManageSharedAccessKey` on every namespace; operators
// add named rules with scoped rights.
type SBAuthorizationRule struct {
	ID         string                  `json:"id"`
	Name       string                  `json:"name"`
	Type       string                  `json:"type"`
	Properties SBAuthorizationRuleSpec `json:"properties"`
}

type SBAuthorizationRuleSpec struct {
	Rights []string `json:"rights"`
}

var (
	sbNamespaces    sim.Store[SBNamespace]
	sbQueues        sim.Store[SBQueue]
	sbTopics        sim.Store[SBTopic]
	sbSubscriptions sim.Store[SBSubscription]
	sbRules         sim.Store[SBRule]
	sbAuthRules     sim.Store[SBAuthorizationRule]
	sbNetworkRules  sim.Store[SBNetworkRuleSet]
	sbPrivateConns  sim.Store[SBPrivateEndpointConnection]
	sbDRConfigs     sim.Store[SBDisasterRecovery]
	sbMigrations    sim.Store[SBMigrationConfig]
)

// Every child collection is addressed by the parent's ID plus a collection
// segment, so these indexes list a parent's children without decoding the
// whole store.
var (
	sbQueuesByParent        sim.GenerationIndex[SBQueue]
	sbTopicsByParent        sim.GenerationIndex[SBTopic]
	sbSubscriptionsByParent sim.GenerationIndex[SBSubscription]
	sbRulesByParent         sim.GenerationIndex[SBRule]
	sbAuthRulesByParent     sim.GenerationIndex[SBAuthorizationRule]
)

func sbQueuesUnder(prefix string) []SBQueue {
	return sbQueuesByParent.LookupAll(sbQueues, prefix,
		func(q SBQueue) []string { return sim.PathPrefixes(q.ID) })
}

func sbTopicsUnder(prefix string) []SBTopic {
	return sbTopicsByParent.LookupAll(sbTopics, prefix,
		func(t SBTopic) []string { return sim.PathPrefixes(t.ID) })
}

func sbSubscriptionsUnder(prefix string) []SBSubscription {
	return sbSubscriptionsByParent.LookupAll(sbSubscriptions, prefix,
		func(s SBSubscription) []string { return sim.PathPrefixes(s.ID) })
}

func sbRulesUnder(prefix string) []SBRule {
	return sbRulesByParent.LookupAll(sbRules, prefix,
		func(r SBRule) []string { return sim.PathPrefixes(r.ID) })
}

func sbAuthRulesUnder(prefix string) []SBAuthorizationRule {
	return sbAuthRulesByParent.LookupAll(sbAuthRules, prefix,
		func(r SBAuthorizationRule) []string { return sim.PathPrefixes(r.ID) })
}

func registerServiceBus(srv *sim.Server) {
	makeAzureKeyGens(srv)
	sbNamespaces = sim.MakeStore[SBNamespace](srv.DB(), "sb_namespaces")
	sbQueues = sim.MakeStore[SBQueue](srv.DB(), "sb_queues")
	sbTopics = sim.MakeStore[SBTopic](srv.DB(), "sb_topics")
	sbSubscriptions = sim.MakeStore[SBSubscription](srv.DB(), "sb_subscriptions")
	sbRules = sim.MakeStore[SBRule](srv.DB(), "sb_rules")
	sbAuthRules = sim.MakeStore[SBAuthorizationRule](srv.DB(), "sb_auth_rules")
	sbNetworkRules = sim.MakeStore[SBNetworkRuleSet](srv.DB(), "sb_network_rule_sets")
	sbPrivateConns = sim.MakeStore[SBPrivateEndpointConnection](srv.DB(), "sb_private_endpoint_connections")
	sbDRConfigs = sim.MakeStore[SBDisasterRecovery](srv.DB(), "sb_disaster_recovery_configs")
	sbMigrations = sim.MakeStore[SBMigrationConfig](srv.DB(), "sb_migration_configs")

	const ns = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ServiceBus/namespaces"
	const nsBySub = "/subscriptions/{subscriptionId}/providers/Microsoft.ServiceBus/namespaces"

	srv.HandleFunc("PUT "+ns+"/{name}", handleSBCreateNamespace)
	srv.HandleFunc("GET "+ns+"/{name}", handleSBGetNamespace)
	srv.HandleFunc("PATCH "+ns+"/{name}", handleSBUpdateNamespace)
	srv.HandleFunc("DELETE "+ns+"/{name}", handleSBDeleteNamespace)
	srv.HandleFunc("GET "+ns, handleSBListNamespacesByRG)
	srv.HandleFunc("GET "+nsBySub, handleSBListNamespacesBySub)
	srv.HandleFunc("GET "+ns+"/{name}/networkRuleSets/default", handleSBGetNamespaceNetworkRuleSet)
	srv.HandleFunc("PUT "+ns+"/{name}/networkRuleSets/default", handleSBPutNamespaceNetworkRuleSet)
	srv.HandleFunc("GET "+ns+"/{name}/networkRuleSets", handleSBListNamespaceNetworkRuleSets)
	srv.HandleFunc("GET "+ns+"/{name}/privateLinkResources", handleSBListPrivateLinkResources)
	srv.HandleFunc("GET "+ns+"/{name}/privateEndpointConnections", handleSBListPrivateEndpointConnections)
	srv.HandleFunc("PUT "+ns+"/{name}/privateEndpointConnections/{pec}", handleSBPutPrivateEndpointConnection)
	srv.HandleFunc("GET "+ns+"/{name}/privateEndpointConnections/{pec}", handleSBGetPrivateEndpointConnection)
	srv.HandleFunc("DELETE "+ns+"/{name}/privateEndpointConnections/{pec}", handleSBDeletePrivateEndpointConnection)

	srv.HandleFunc("GET "+ns+"/{name}/disasterRecoveryConfigs/{alias}", handleSBGetDisasterRecoveryConfig)
	srv.HandleFunc("PUT "+ns+"/{name}/disasterRecoveryConfigs/{alias}", handleSBPutDisasterRecoveryConfig)
	srv.HandleFunc("DELETE "+ns+"/{name}/disasterRecoveryConfigs/{alias}", handleSBDeleteDisasterRecoveryConfig)
	srv.HandleFunc("GET "+ns+"/{name}/disasterRecoveryConfigs", handleSBListDisasterRecoveryConfigs)
	srv.HandleFunc("POST "+ns+"/{name}/disasterRecoveryConfigs/{alias}/breakPairing", handleSBDisasterRecoveryBreakPairing)
	srv.HandleFunc("POST "+ns+"/{name}/disasterRecoveryConfigs/{alias}/failover", handleSBDisasterRecoveryFailover)
	srv.HandleFunc("GET "+ns+"/{name}/disasterRecoveryConfigs/{alias}/authorizationRules", handleSBListDRAuthorizationRules)
	srv.HandleFunc("GET "+ns+"/{name}/disasterRecoveryConfigs/{alias}/authorizationRules/{rule}", handleSBGetDRAuthorizationRule)
	srv.HandleFunc("POST "+ns+"/{name}/disasterRecoveryConfigs/{alias}/authorizationRules/{rule}/listKeys", handleSBListDRAuthorizationRuleKeys)

	srv.HandleFunc("GET "+ns+"/{name}/migrationConfigurations/{config}", handleSBGetMigrationConfiguration)
	srv.HandleFunc("PUT "+ns+"/{name}/migrationConfigurations/{config}", handleSBPutMigrationConfiguration)
	srv.HandleFunc("DELETE "+ns+"/{name}/migrationConfigurations/{config}", handleSBDeleteMigrationConfiguration)
	srv.HandleFunc("GET "+ns+"/{name}/migrationConfigurations", handleSBListMigrationConfigurations)
	srv.HandleFunc("POST "+ns+"/{name}/migrationConfigurations/{config}/upgrade", handleSBCompleteMigration)
	srv.HandleFunc("POST "+ns+"/{name}/migrationConfigurations/{config}/revert", handleSBRevertMigration)

	// Namespace PUT provisions RootManageSharedAccessKey; listKeys and
	// regenerateKeys return SAS keys derived from the rule's resource ID.
	srv.HandleFunc("PUT "+ns+"/{name}/authorizationRules/{rule}", sbAuthRuleCreate("Microsoft.ServiceBus/namespaces/authorizationRules", "namespaces"))
	srv.HandleFunc("GET "+ns+"/{name}/authorizationRules/{rule}", sbAuthRuleGet("namespaces"))
	srv.HandleFunc("DELETE "+ns+"/{name}/authorizationRules/{rule}", sbAuthRuleDelete("namespaces"))
	srv.HandleFunc("GET "+ns+"/{name}/authorizationRules", sbAuthRuleList("namespaces"))
	srv.HandleFunc("POST "+ns+"/{name}/authorizationRules/{rule}/listKeys", sbAuthRuleListKeys("namespaces"))
	srv.HandleFunc("POST "+ns+"/{name}/authorizationRules/{rule}/regenerateKeys", sbAuthRuleRegenerateKeys("namespaces"))

	// The Azure SDK sends the swagger's PascalCase `AuthorizationRules`; terraform
	// and raw HTTP clients send `authorizationRules`. Both dispatch here, and
	// storage keys on the lowercase ID.
	srv.HandleFunc("PUT "+ns+"/{name}/AuthorizationRules/{rule}", sbAuthRuleCreate("Microsoft.ServiceBus/namespaces/authorizationRules", "namespaces"))
	srv.HandleFunc("GET "+ns+"/{name}/AuthorizationRules/{rule}", sbAuthRuleGet("namespaces"))
	srv.HandleFunc("DELETE "+ns+"/{name}/AuthorizationRules/{rule}", sbAuthRuleDelete("namespaces"))
	srv.HandleFunc("GET "+ns+"/{name}/AuthorizationRules", sbAuthRuleList("namespaces"))
	srv.HandleFunc("POST "+ns+"/{name}/AuthorizationRules/{rule}/listKeys", sbAuthRuleListKeys("namespaces"))
	srv.HandleFunc("POST "+ns+"/{name}/AuthorizationRules/{rule}/regenerateKeys", sbAuthRuleRegenerateKeys("namespaces"))

	srv.HandleFunc("PUT "+ns+"/{name}/queues/{queue}/authorizationRules/{rule}", sbAuthRuleCreate("Microsoft.ServiceBus/namespaces/queues/authorizationRules", "queues"))
	srv.HandleFunc("GET "+ns+"/{name}/queues/{queue}/authorizationRules/{rule}", sbAuthRuleGet("queues"))
	srv.HandleFunc("DELETE "+ns+"/{name}/queues/{queue}/authorizationRules/{rule}", sbAuthRuleDelete("queues"))
	srv.HandleFunc("GET "+ns+"/{name}/queues/{queue}/authorizationRules", sbAuthRuleList("queues"))
	srv.HandleFunc("POST "+ns+"/{name}/queues/{queue}/authorizationRules/{rule}/listKeys", sbAuthRuleListKeys("queues"))
	// The SDK swagger spells queue and topic list-keys `ListKeys`, unlike the
	// namespace's `listKeys`.
	srv.HandleFunc("POST "+ns+"/{name}/queues/{queue}/authorizationRules/{rule}/ListKeys", sbAuthRuleListKeys("queues"))
	srv.HandleFunc("POST "+ns+"/{name}/queues/{queue}/authorizationRules/{rule}/regenerateKeys", sbAuthRuleRegenerateKeys("queues"))

	srv.HandleFunc("PUT "+ns+"/{name}/topics/{topic}/authorizationRules/{rule}", sbAuthRuleCreate("Microsoft.ServiceBus/namespaces/topics/authorizationRules", "topics"))
	srv.HandleFunc("GET "+ns+"/{name}/topics/{topic}/authorizationRules/{rule}", sbAuthRuleGet("topics"))
	srv.HandleFunc("DELETE "+ns+"/{name}/topics/{topic}/authorizationRules/{rule}", sbAuthRuleDelete("topics"))
	srv.HandleFunc("GET "+ns+"/{name}/topics/{topic}/authorizationRules", sbAuthRuleList("topics"))
	srv.HandleFunc("POST "+ns+"/{name}/topics/{topic}/authorizationRules/{rule}/listKeys", sbAuthRuleListKeys("topics"))
	srv.HandleFunc("POST "+ns+"/{name}/topics/{topic}/authorizationRules/{rule}/ListKeys", sbAuthRuleListKeys("topics"))
	srv.HandleFunc("POST "+ns+"/{name}/topics/{topic}/authorizationRules/{rule}/regenerateKeys", sbAuthRuleRegenerateKeys("topics"))

	srv.HandleFunc("PUT "+ns+"/{name}/queues/{queue}", handleSBCreateQueue)
	srv.HandleFunc("GET "+ns+"/{name}/queues/{queue}", handleSBGetQueue)
	srv.HandleFunc("DELETE "+ns+"/{name}/queues/{queue}", handleSBDeleteQueue)
	srv.HandleFunc("GET "+ns+"/{name}/queues", handleSBListQueues)

	srv.HandleFunc("PUT "+ns+"/{name}/topics/{topic}", handleSBCreateTopic)
	srv.HandleFunc("GET "+ns+"/{name}/topics/{topic}", handleSBGetTopic)
	srv.HandleFunc("DELETE "+ns+"/{name}/topics/{topic}", handleSBDeleteTopic)
	srv.HandleFunc("GET "+ns+"/{name}/topics", handleSBListTopics)

	srv.HandleFunc("PUT "+ns+"/{name}/topics/{topic}/subscriptions/{sub}", handleSBCreateSubscription)
	srv.HandleFunc("GET "+ns+"/{name}/topics/{topic}/subscriptions/{sub}", handleSBGetSubscription)
	srv.HandleFunc("DELETE "+ns+"/{name}/topics/{topic}/subscriptions/{sub}", handleSBDeleteSubscription)
	srv.HandleFunc("GET "+ns+"/{name}/topics/{topic}/subscriptions", handleSBListSubscriptions)
}

func sbNamespaceID(sub, rg, name string) string {
	return armResourceID(sub, rg, "Microsoft.ServiceBus", "namespaces", name)
}

func sbNetworkRuleSetID(sub, rg, name string) string {
	return armChildID(sbNamespaceID(sub, rg, name), "networkRuleSets", "default")
}

func handleSBCreateNamespace(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	var req SBNamespace
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := sbNamespaceID(sub, rg, name)
	n := SBNamespace{
		ID:       id,
		Name:     name,
		Type:     "Microsoft.ServiceBus/namespaces",
		Location: req.Location,
		Sku:      req.Sku,
		Tags:     req.Tags,
		Properties: map[string]any{
			"provisioningState":  "Succeeded",
			"serviceBusEndpoint": azureServiceBusEndpointURL(r, name),
		},
	}
	if req.Properties != nil {
		for k, v := range req.Properties {
			n.Properties[k] = v
		}
		n.Properties["provisioningState"] = "Succeeded"
	}
	if n.Sku == nil {
		n.Sku = map[string]any{"name": "Standard", "tier": "Standard"}
	}
	sbNamespaces.Put(id, n)

	// Azure provisions `RootManageSharedAccessKey` (Listen, Send, Manage) on a new
	// namespace; later PUTs keep operator edits.
	rootID := id + "/authorizationRules/RootManageSharedAccessKey"
	if _, ok := sbAuthRules.Get(rootID); !ok {
		sbAuthRules.Put(rootID, SBAuthorizationRule{
			ID:   rootID,
			Name: "RootManageSharedAccessKey",
			Type: "Microsoft.ServiceBus/namespaces/authorizationRules",
			Properties: SBAuthorizationRuleSpec{
				Rights: []string{"Listen", "Send", "Manage"},
			},
		})
	}

	sim.WriteJSON(w, http.StatusOK, n)
}

func handleSBGetNamespace(w http.ResponseWriter, r *http.Request) {
	id := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	n, ok := sbNamespaces.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "namespace not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, n)
}

func handleSBDeleteNamespace(w http.ResponseWriter, r *http.Request) {
	id := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if !sbNamespaces.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "namespace not found")
		return
	}
	prefix := id + "/"
	for _, q := range sbQueues.List() {
		if strings.HasPrefix(q.ID, prefix) {
			sbQueues.Delete(q.ID)
		}
	}
	for _, t := range sbTopics.List() {
		if strings.HasPrefix(t.ID, prefix) {
			sbTopics.Delete(t.ID)
		}
	}
	for _, s := range sbSubscriptions.List() {
		if strings.HasPrefix(s.ID, prefix) {
			sbSubscriptions.Delete(s.ID)
		}
	}
	for _, rule := range sbRules.List() {
		if strings.HasPrefix(rule.ID, prefix) {
			sbRules.Delete(rule.ID)
		}
	}
	for _, rule := range sbAuthRules.List() {
		if strings.HasPrefix(rule.ID, prefix) {
			sbAuthRules.Delete(rule.ID)
			sbDropAuthRuleKeyGens(rule.ID)
		}
	}
	for _, ruleSet := range sbNetworkRules.List() {
		if strings.HasPrefix(ruleSet.ID, prefix) {
			sbNetworkRules.Delete(ruleSet.ID)
		}
	}
	for _, pec := range sbPrivateConns.List() {
		if strings.HasPrefix(pec.ID, prefix) {
			sbPrivateConns.Delete(pec.ID)
		}
	}
	for _, dr := range sbDRConfigs.List() {
		if strings.HasPrefix(dr.ID, prefix) {
			sbDRConfigs.Delete(dr.ID)
		}
	}
	for _, mc := range sbMigrations.List() {
		if strings.HasPrefix(mc.ID, prefix) {
			sbMigrations.Delete(mc.ID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleSBUpdateNamespace(w http.ResponseWriter, r *http.Request) {
	id := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	n, ok := sbNamespaces.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "namespace not found")
		return
	}
	var req SBNamespace
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if req.Location != "" {
		n.Location = req.Location
	}
	if req.Sku != nil {
		n.Sku = req.Sku
	}
	if req.Tags != nil {
		n.Tags = req.Tags
	}
	if n.Properties == nil {
		n.Properties = map[string]any{}
	}
	for k, v := range req.Properties {
		n.Properties[k] = v
	}
	n.Properties["provisioningState"] = "Succeeded"
	sbNamespaces.Put(id, n)
	sim.WriteJSON(w, http.StatusOK, n)
}

func handleSBListNamespacesBySub(w http.ResponseWriter, r *http.Request) {
	prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
	all := sbNamespaces.List()
	out := make([]SBNamespace, 0, len(all))
	for _, n := range all {
		if strings.HasPrefix(n.ID, prefix) {
			out = append(out, n)
		}
	}
	writeSBPagedList(w, r, out, func(x SBNamespace) string { return x.ID })
}

func handleSBListNamespacesByRG(w http.ResponseWriter, r *http.Request) {
	prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.ServiceBus", "namespaces") + "/"
	all := sbNamespaces.List()
	out := make([]SBNamespace, 0, len(all))
	for _, n := range all {
		if strings.HasPrefix(n.ID, prefix) {
			out = append(out, n)
		}
	}
	writeSBPagedList(w, r, out, func(x SBNamespace) string { return x.ID })
}

// Ordered by key.
func writeSBPagedList[T any](w http.ResponseWriter, r *http.Request, items []T, key func(T) string) {
	page, next, pageOK := armPage(w, r, items, key)
	if !pageOK {
		return
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = armNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleSBGetNamespaceNetworkRuleSet(w http.ResponseWriter, r *http.Request) {
	sub, rg, name := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")
	id := sbNetworkRuleSetID(sub, rg, name)
	if _, ok := sbNamespaces.Get(sbNamespaceID(sub, rg, name)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "namespace not found")
		return
	}
	ruleSet, ok := sbNetworkRules.Get(id)
	if !ok {
		ruleSet = sbDefaultNetworkRuleSet(id)
	}
	sim.WriteJSON(w, http.StatusOK, ruleSet)
}

func handleSBPutNamespaceNetworkRuleSet(w http.ResponseWriter, r *http.Request) {
	sub, rg, name := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")
	if _, ok := sbNamespaces.Get(sbNamespaceID(sub, rg, name)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "namespace not found")
		return
	}
	var req SBNetworkRuleSet
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := sbNetworkRuleSetID(sub, rg, name)
	ruleSet := sbDefaultNetworkRuleSet(id)
	for k, v := range req.Properties {
		ruleSet.Properties[k] = v
	}
	applySBNetworkRuleSetDefaults(ruleSet.Properties)
	sbNetworkRules.Put(id, ruleSet)
	sim.WriteJSON(w, http.StatusOK, ruleSet)
}

func handleSBListNamespaceNetworkRuleSets(w http.ResponseWriter, r *http.Request) {
	sub, rg, name := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")
	id := sbNetworkRuleSetID(sub, rg, name)
	if _, ok := sbNamespaces.Get(sbNamespaceID(sub, rg, name)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "namespace not found")
		return
	}
	ruleSet, ok := sbNetworkRules.Get(id)
	if !ok {
		ruleSet = sbDefaultNetworkRuleSet(id)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []SBNetworkRuleSet{ruleSet}})
}

func sbDisasterRecoveryID(sub, rg, name, alias string) string {
	return armChildID(sbNamespaceID(sub, rg, name), "disasterRecoveryConfigs", alias)
}

func sbMigrationConfigID(sub, rg, name, config string) string {
	return armChildID(sbNamespaceID(sub, rg, name), "migrationConfigurations", config)
}

func handleSBListDisasterRecoveryConfigs(w http.ResponseWriter, r *http.Request) {
	sbListNamespaceChildren(w, r, sbDRConfigs, "disasterRecoveryConfigs")
}

func handleSBGetDisasterRecoveryConfig(w http.ResponseWriter, r *http.Request) {
	id := sbDisasterRecoveryID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "alias"))
	dr, ok := sbDRConfigs.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "disaster recovery config not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, dr)
}

func handleSBPutDisasterRecoveryConfig(w http.ResponseWriter, r *http.Request) {
	sbPutNamespaceChild(w, r, sbDRConfigs, "disasterRecoveryConfigs", "alias", "Microsoft.ServiceBus/namespaces/disasterRecoveryConfigs",
		func(requested map[string]any) map[string]any {
			return sbCopyKeys(map[string]any{
				"provisioningState":                 "Succeeded",
				"role":                              "Primary",
				"pendingReplicationOperationsCount": 0,
			}, requested, "partnerNamespace", "alternateName")
		})
}

func handleSBDeleteDisasterRecoveryConfig(w http.ResponseWriter, r *http.Request) {
	id := sbDisasterRecoveryID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "alias"))
	if !sbDRConfigs.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "disaster recovery config not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The alias's role becomes PrimaryNotReplicating.
func handleSBDisasterRecoveryBreakPairing(w http.ResponseWriter, r *http.Request) {
	id := sbDisasterRecoveryID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "alias"))
	if _, ok := sbDRConfigs.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "disaster recovery config not found")
		return
	}
	sbDRConfigs.Update(id, func(dr *SBDisasterRecovery) {
		if dr.Properties == nil {
			dr.Properties = map[string]any{}
		}
		dr.Properties["role"] = "PrimaryNotReplicating"
	})
	w.WriteHeader(http.StatusOK)
}

func handleSBDisasterRecoveryFailover(w http.ResponseWriter, r *http.Request) {
	id := sbDisasterRecoveryID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "alias"))
	if _, ok := sbDRConfigs.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "disaster recovery config not found")
		return
	}
	sbDRConfigs.Update(id, func(dr *SBDisasterRecovery) {
		if dr.Properties == nil {
			dr.Properties = map[string]any{}
		}
		dr.Properties["role"] = "Primary"
	})
	w.WriteHeader(http.StatusOK)
}

// A Geo-DR alias surfaces the primary namespace's SAS rules, so read them
// through the namespace store.
func sbDRAuthRuleParent(r *http.Request) (string, bool) {
	sub, rg, name, alias := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "alias")
	if _, ok := sbDRConfigs.Get(sbDisasterRecoveryID(sub, rg, name, alias)); !ok {
		return "", false
	}
	return sbNamespaceID(sub, rg, name), true
}

func handleSBListDRAuthorizationRules(w http.ResponseWriter, r *http.Request) {
	parent, ok := sbDRAuthRuleParent(r)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "disaster recovery config not found")
		return
	}
	prefix := parent + "/authorizationRules/"
	var out []SBAuthorizationRule
	for _, rule := range sbAuthRules.List() {
		if strings.HasPrefix(rule.ID, prefix) {
			out = append(out, rule)
		}
	}
	if out == nil {
		out = []SBAuthorizationRule{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleSBGetDRAuthorizationRule(w http.ResponseWriter, r *http.Request) {
	parent, ok := sbDRAuthRuleParent(r)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "disaster recovery config not found")
		return
	}
	rule, ok := sbAuthRules.Get(parent + "/authorizationRules/" + sim.PathParam(r, "rule"))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "authorization rule not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, rule)
}

func handleSBListDRAuthorizationRuleKeys(w http.ResponseWriter, r *http.Request) {
	parent, ok := sbDRAuthRuleParent(r)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "disaster recovery config not found")
		return
	}
	ruleName := sim.PathParam(r, "rule")
	id := parent + "/authorizationRules/" + ruleName
	if _, ok := sbAuthRules.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "authorization rule not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, sbAuthRuleListKeysBody(r, id, sim.PathParam(r, "name"), ruleName))
}

func handleSBListMigrationConfigurations(w http.ResponseWriter, r *http.Request) {
	sbListNamespaceChildren(w, r, sbMigrations, "migrationConfigurations")
}

// A namespace that does not exist is refused.
func sbListNamespaceChildren[T sbEntityShape](w http.ResponseWriter, r *http.Request, store sim.Store[T], collection string) {
	parent := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := sbNamespaces.Get(parent); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "namespace not found")
		return
	}
	prefix := armChildID(parent, collection) + "/"
	all := store.List()
	out := make([]T, 0, len(all))
	for _, child := range all {
		if strings.HasPrefix(sbEntity(child).ID, prefix) {
			out = append(out, child)
		}
	}
	writeSBPagedList(w, r, out, func(x T) string { return sbEntity(x).ID })
}

func handleSBGetMigrationConfiguration(w http.ResponseWriter, r *http.Request) {
	id := sbMigrationConfigID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "config"))
	mc, ok := sbMigrations.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "migration configuration not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, mc)
}

func handleSBPutMigrationConfiguration(w http.ResponseWriter, r *http.Request) {
	sbPutNamespaceChild(w, r, sbMigrations, "migrationConfigurations", "config", "Microsoft.ServiceBus/namespaces/migrationConfigurations",
		func(requested map[string]any) map[string]any {
			return sbCopyKeys(map[string]any{
				"provisioningState":                 "Succeeded",
				"migrationState":                    "Active",
				"pendingReplicationOperationsCount": 0,
			}, requested, "targetNamespace", "postMigrationName")
		})
}

// The wire shape of queues, topics, disaster recovery configurations and
// migration configurations.
type sbEntity struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type sbEntityShape interface {
	~struct {
		ID         string         `json:"id"`
		Name       string         `json:"name"`
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties,omitempty"`
	}
}

func sbPutNamespaceChild[T sbEntityShape](w http.ResponseWriter, r *http.Request, store sim.Store[T], collection, nameParam, resourceType string, properties func(requested map[string]any) map[string]any) {
	parent := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := sbNamespaces.Get(parent); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "namespace not found")
		return
	}
	var req T
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	name := sim.PathParam(r, nameParam)
	id := armChildID(parent, collection, name)
	entity := T(sbEntity{ID: id, Name: name, Type: resourceType, Properties: properties(sbEntity(req).Properties)})
	store.Put(id, entity)
	sim.WriteJSON(w, http.StatusOK, entity)
}

func sbOverlay(defaults, requested map[string]any) map[string]any {
	for k, v := range requested {
		defaults[k] = v
	}
	return defaults
}

func sbCopyKeys(props, requested map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		if v, ok := requested[k]; ok {
			props[k] = v
		}
	}
	return props
}

func handleSBDeleteMigrationConfiguration(w http.ResponseWriter, r *http.Request) {
	id := sbMigrationConfigID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "config"))
	if !sbMigrations.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "migration configuration not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The connection strings point to the premium namespace.
func handleSBCompleteMigration(w http.ResponseWriter, r *http.Request) {
	id := sbMigrationConfigID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "config"))
	if _, ok := sbMigrations.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "migration configuration not found")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleSBRevertMigration(w http.ResponseWriter, r *http.Request) {
	id := sbMigrationConfigID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "config"))
	if _, ok := sbMigrations.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "migration configuration not found")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func sbPrivateEndpointConnectionID(sub, rg, name, pec string) string {
	return armChildID(sbNamespaceID(sub, rg, name), "privateEndpointConnections", pec)
}

func handleSBListPrivateEndpointConnections(w http.ResponseWriter, r *http.Request) {
	sub, rg, name := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")
	if _, ok := sbNamespaces.Get(sbNamespaceID(sub, rg, name)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "namespace not found")
		return
	}
	prefix := sbNamespaceID(sub, rg, name) + "/privateEndpointConnections/"
	all := sbPrivateConns.List()
	out := make([]SBPrivateEndpointConnection, 0, len(all))
	for _, pec := range all {
		if strings.HasPrefix(pec.ID, prefix) {
			out = append(out, pec)
		}
	}
	writeSBPagedList(w, r, out, func(x SBPrivateEndpointConnection) string { return x.ID })
}

func handleSBGetPrivateEndpointConnection(w http.ResponseWriter, r *http.Request) {
	id := sbPrivateEndpointConnectionID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "pec"))
	pec, ok := sbPrivateConns.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "private endpoint connection not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, pec)
}

func handleSBPutPrivateEndpointConnection(w http.ResponseWriter, r *http.Request) {
	sub, rg, name, pecName := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "pec")
	if _, ok := sbNamespaces.Get(sbNamespaceID(sub, rg, name)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "namespace not found")
		return
	}
	var req SBPrivateEndpointConnection
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := sbPrivateEndpointConnectionID(sub, rg, name, pecName)
	props := map[string]any{}
	for k, v := range req.Properties {
		props[k] = v
	}
	props["provisioningState"] = "Succeeded"
	pec := SBPrivateEndpointConnection{
		ID:         id,
		Name:       pecName,
		Type:       "Microsoft.ServiceBus/namespaces/privateEndpointConnections",
		Properties: props,
	}
	sbPrivateConns.Put(id, pec)
	sim.WriteJSON(w, http.StatusOK, pec)
}

func handleSBDeletePrivateEndpointConnection(w http.ResponseWriter, r *http.Request) {
	id := sbPrivateEndpointConnectionID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "pec"))
	if !sbPrivateConns.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "private endpoint connection not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// A namespace exposes the single "namespace" group.
func handleSBListPrivateLinkResources(w http.ResponseWriter, r *http.Request) {
	sub, rg, name := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")
	id := sbNamespaceID(sub, rg, name)
	if _, ok := sbNamespaces.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "namespace not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []map[string]any{{
			"id":   id + "/privateLinkResources/namespace",
			"name": "namespace",
			"type": "Microsoft.ServiceBus/namespaces/privateLinkResources",
			"properties": map[string]any{
				"groupId":           "namespace",
				"requiredMembers":   []string{"namespace"},
				"requiredZoneNames": []string{"privatelink.servicebus.windows.net"},
			},
		}},
	})
}

func sbDefaultNetworkRuleSet(id string) SBNetworkRuleSet {
	props := map[string]any{}
	applySBNetworkRuleSetDefaults(props)
	return SBNetworkRuleSet{
		ID:         id,
		Name:       "default",
		Type:       "Microsoft.ServiceBus/namespaces/networkRuleSets",
		Properties: props,
	}
}

func applySBNetworkRuleSetDefaults(props map[string]any) {
	if _, ok := props["defaultAction"]; !ok {
		props["defaultAction"] = "Allow"
	}
	if _, ok := props["publicNetworkAccess"]; !ok {
		props["publicNetworkAccess"] = "Enabled"
	}
	if _, ok := props["trustedServiceAccessEnabled"]; !ok {
		props["trustedServiceAccessEnabled"] = false
	}
	if _, ok := props["virtualNetworkRules"]; !ok {
		props["virtualNetworkRules"] = []any{}
	}
	if _, ok := props["ipRules"]; !ok {
		props["ipRules"] = []any{}
	}
}

func handleSBCreateQueue(w http.ResponseWriter, r *http.Request) {
	sbPutNamespaceChild(w, r, sbQueues, "queues", "queue", "Microsoft.ServiceBus/namespaces/queues",
		func(requested map[string]any) map[string]any { return sbOverlay(sbDefaultQueueProperties(), requested) })
}

func handleSBGetQueue(w http.ResponseWriter, r *http.Request) {
	id := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/queues/" + sim.PathParam(r, "queue")
	q, ok := sbQueues.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "queue not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, q)
}

func handleSBDeleteQueue(w http.ResponseWriter, r *http.Request) {
	id := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/queues/" + sim.PathParam(r, "queue")
	if !sbQueues.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "queue not found")
		return
	}
	// Service Bus deletes the queue's scoped authorization rules with it.
	sbDropAuthRulesUnder(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleSBListQueues(w http.ResponseWriter, r *http.Request) {
	prefix := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/queues/"
	all := sbQueues.List()
	filtered := make([]SBQueue, 0, len(all))
	for _, q := range all {
		if strings.HasPrefix(q.ID, prefix) {
			filtered = append(filtered, q)
		}
	}
	writeSBPagedList(w, r, filtered, func(x SBQueue) string { return x.Name })
}

func handleSBCreateTopic(w http.ResponseWriter, r *http.Request) {
	sbPutNamespaceChild(w, r, sbTopics, "topics", "topic", "Microsoft.ServiceBus/namespaces/topics",
		func(requested map[string]any) map[string]any { return sbOverlay(sbDefaultTopicProperties(), requested) })
}

func handleSBGetTopic(w http.ResponseWriter, r *http.Request) {
	id := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/topics/" + sim.PathParam(r, "topic")
	t, ok := sbTopics.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "topic not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, t)
}

func handleSBDeleteTopic(w http.ResponseWriter, r *http.Request) {
	id := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/topics/" + sim.PathParam(r, "topic")
	if !sbTopics.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "topic not found")
		return
	}
	prefix := id + "/subscriptions/"
	for _, s := range sbSubscriptions.List() {
		if strings.HasPrefix(s.ID, prefix) {
			sbSubscriptions.Delete(s.ID)
		}
	}
	for _, rule := range sbRules.List() {
		if strings.HasPrefix(rule.ID, prefix) {
			sbRules.Delete(rule.ID)
		}
	}
	// Service Bus deletes the topic's scoped authorization rules with it.
	sbDropAuthRulesUnder(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleSBListTopics(w http.ResponseWriter, r *http.Request) {
	prefix := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/topics/"
	all := sbTopics.List()
	out := make([]SBTopic, 0, len(all))
	for _, t := range all {
		if strings.HasPrefix(t.ID, prefix) && !strings.Contains(strings.TrimPrefix(t.ID, prefix), "/") {
			out = append(out, t)
		}
	}
	writeSBPagedList(w, r, out, func(x SBTopic) string { return x.ID })
}

func handleSBCreateSubscription(w http.ResponseWriter, r *http.Request) {
	parent := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/topics/" + sim.PathParam(r, "topic")
	if _, ok := sbTopics.Get(parent); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "topic not found")
		return
	}
	sName := sim.PathParam(r, "sub")
	var req SBSubscription
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := parent + "/subscriptions/" + sName
	s := SBSubscription{
		ID: id, Name: sName, Type: "Microsoft.ServiceBus/namespaces/topics/subscriptions",
		Properties: sbDefaultSubscriptionProperties(),
	}
	if req.Properties != nil {
		for k, v := range req.Properties {
			s.Properties[k] = v
		}
	}
	sbSubscriptions.Put(id, s)
	sim.WriteJSON(w, http.StatusOK, s)
}

func handleSBGetSubscription(w http.ResponseWriter, r *http.Request) {
	id := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/topics/" + sim.PathParam(r, "topic") + "/subscriptions/" + sim.PathParam(r, "sub")
	s, ok := sbSubscriptions.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "subscription not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, s)
}

func handleSBDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	id := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/topics/" + sim.PathParam(r, "topic") + "/subscriptions/" + sim.PathParam(r, "sub")
	if !sbSubscriptions.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "subscription not found")
		return
	}
	for _, rule := range sbRules.List() {
		if strings.HasPrefix(rule.ID, id+"/rules/") {
			sbRules.Delete(rule.ID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func sbAuthRuleParentID(r *http.Request, scope string) string {
	base := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	switch scope {
	case "queues":
		return base + "/queues/" + sim.PathParam(r, "queue")
	case "topics":
		return base + "/topics/" + sim.PathParam(r, "topic")
	default:
		return base
	}
}

func sbAuthRuleParentExists(parent, scope string) bool {
	switch scope {
	case "queues":
		_, ok := sbQueues.Get(parent)
		return ok
	case "topics":
		_, ok := sbTopics.Get(parent)
		return ok
	default:
		_, ok := sbNamespaces.Get(parent)
		return ok
	}
}

func sbAuthRuleCreate(armType, scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parent := sbAuthRuleParentID(r, scope)
		if !sbAuthRuleParentExists(parent, scope) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "parent resource not found: %s", parent)
			return
		}
		rule := sim.PathParam(r, "rule")
		var req SBAuthorizationRule
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		rights := req.Properties.Rights
		if len(rights) == 0 {
			rights = []string{"Listen"}
		}
		id := parent + "/authorizationRules/" + rule
		stored := SBAuthorizationRule{
			ID:   id,
			Name: rule,
			Type: armType,
			Properties: SBAuthorizationRuleSpec{
				Rights: rights,
			},
		}
		sbAuthRules.Put(id, stored)
		sim.WriteJSON(w, http.StatusOK, stored)
	}
}

func sbAuthRuleGet(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := sbAuthRuleParentID(r, scope) + "/authorizationRules/" + sim.PathParam(r, "rule")
		rule, ok := sbAuthRules.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "authorization rule not found: %s", id)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rule)
	}
}

// A later rule with the same ID starts from fresh keys.
func sbDropAuthRuleKeyGens(ruleID string) {
	azureDropKeyGens(ruleID, "primary", "secondary")
}

func sbDropAuthRulesUnder(parentID string) {
	for _, rule := range sbAuthRulesUnder(parentID + "/authorizationRules/") {
		sbAuthRules.Delete(rule.ID)
		sbDropAuthRuleKeyGens(rule.ID)
	}
}

func sbAuthRuleDelete(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := sbAuthRuleParentID(r, scope) + "/authorizationRules/" + sim.PathParam(r, "rule")
		if !sbAuthRules.Delete(id) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "authorization rule not found: %s", id)
			return
		}
		sbDropAuthRuleKeyGens(id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func sbAuthRuleList(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parent := sbAuthRuleParentID(r, scope)
		prefix := parent + "/authorizationRules/"
		var out []SBAuthorizationRule
		for _, rule := range sbAuthRules.List() {
			if strings.HasPrefix(rule.ID, prefix) {
				out = append(out, rule)
			}
		}
		if out == nil {
			out = []SBAuthorizationRule{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	}
}

// Each 44-character base64 key derives from the rule ID and its rotation
// generation: stable across reads, distinct per slot, new after each
// regenerateKeys. The connection strings embed the current keys.
func sbAuthRuleListKeysBody(r *http.Request, ruleID, namespace, ruleName string) map[string]any {
	primary := azureKeyMaterial32(ruleID, "primary")
	secondary := azureKeyMaterial32(ruleID, "secondary")
	// Endpoint, then the SAS rule name and key, as Azure builds them.
	endpoint := "Endpoint=" + azureServiceBusConnectionEndpoint(r, namespace)
	return map[string]any{
		"primaryKey":                primary,
		"secondaryKey":              secondary,
		"primaryConnectionString":   endpoint + ";SharedAccessKeyName=" + ruleName + ";SharedAccessKey=" + primary,
		"secondaryConnectionString": endpoint + ";SharedAccessKeyName=" + ruleName + ";SharedAccessKey=" + secondary,
		"keyName":                   ruleName,
	}
}

func sbAuthRuleListKeys(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ruleName := sim.PathParam(r, "rule")
		id := sbAuthRuleParentID(r, scope) + "/authorizationRules/" + ruleName
		if _, ok := sbAuthRules.Get(id); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "authorization rule not found: %s", id)
			return
		}
		ns := sim.PathParam(r, "name")
		sim.WriteJSON(w, http.StatusOK, sbAuthRuleListKeysBody(r, id, ns, ruleName))
	}
}

func sbAuthRuleRegenerateKeys(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ruleName := sim.PathParam(r, "rule")
		id := sbAuthRuleParentID(r, scope) + "/authorizationRules/" + ruleName
		if _, ok := sbAuthRules.Get(id); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "authorization rule not found: %s", id)
			return
		}
		// keyType selects the slot; the optional key pins it to caller-supplied
		// material, as Service Bus does.
		var req struct {
			KeyType string `json:"keyType"`
			Key     string `json:"key"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		switch req.KeyType {
		case "PrimaryKey":
			azureBumpKeyGen(id, "primary", req.Key)
		case "SecondaryKey":
			azureBumpKeyGen(id, "secondary", req.Key)
		default:
			AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"keyType must be 'PrimaryKey' or 'SecondaryKey', got %q", req.KeyType)
			return
		}
		ns := sim.PathParam(r, "name")
		sim.WriteJSON(w, http.StatusOK, sbAuthRuleListKeysBody(r, id, ns, ruleName))
	}
}

func handleSBListSubscriptions(w http.ResponseWriter, r *http.Request) {
	prefix := sbNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/topics/" + sim.PathParam(r, "topic") + "/subscriptions/"
	all := sbSubscriptions.List()
	out := make([]SBSubscription, 0, len(all))
	for _, s := range all {
		if strings.HasPrefix(s.ID, prefix) {
			out = append(out, s)
		}
	}
	writeSBPagedList(w, r, out, func(x SBSubscription) string { return x.ID })
}
