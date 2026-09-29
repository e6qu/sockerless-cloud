package main

import (
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	amqp "github.com/Azure/go-amqp"
	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/streamlog"
)

type EHNamespace struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location,omitempty"`
	SKU        map[string]any    `json:"sku,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
}

type EHEventHub struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
	CreatedAt  time.Time      `json:"-"`
}

type EHConsumerGroup struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type EHAuthorizationRule struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

// EHPrivateEndpointConnection is a private endpoint connection on an
// Event Hubs namespace.
type EHPrivateEndpointConnection struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

// ehEvent is one event as its publisher sent it; the partition's log assigns
// its sequence number and enqueued time.
type ehEvent struct {
	AMQP []byte `json:"amqp"`
}

var (
	ehNamespaces     sim.Store[EHNamespace]
	ehEventHubs      sim.Store[EHEventHub]
	ehConsumerGroups sim.Store[EHConsumerGroup]
	ehAuthRules      sim.Store[EHAuthorizationRule]
	ehLog            *streamlog.Log[ehEvent]
	ehPrivateConns   sim.Store[EHPrivateEndpointConnection]
	ehNetworkRules   sim.Store[EHNetworkRuleSet]
)

// EHNetworkRuleSet is the single 'default' network rule set on an
// Event Hubs namespace.
type EHNetworkRuleSet struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

func registerEventHubs(srv *sim.Server) {
	makeAzureKeyGens(srv)
	ehNamespaces = sim.MakeStore[EHNamespace](srv.DB(), "eventhub_namespaces")
	ehEventHubs = sim.MakeStore[EHEventHub](srv.DB(), "eventhub_eventhubs")
	ehConsumerGroups = sim.MakeStore[EHConsumerGroup](srv.DB(), "eventhub_consumer_groups")
	ehAuthRules = sim.MakeStore[EHAuthorizationRule](srv.DB(), "eventhub_auth_rules")
	ehLog = streamlog.New(
		sim.MakeStore[streamlog.Record[ehEvent]](srv.DB(), "eventhub_partition_records"),
		sim.MakeStore[streamlog.Head](srv.DB(), "eventhub_partition_heads"))
	if err := ehMigratePartitions(srv.DB()); err != nil {
		log.Fatalf("event hubs: %v", err)
	}
	ehPrivateConns = sim.MakeStore[EHPrivateEndpointConnection](srv.DB(), "eventhub_private_endpoint_connections")
	ehNetworkRules = sim.MakeStore[EHNetworkRuleSet](srv.DB(), "eventhub_network_rule_sets")

	const ns = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventHub/namespaces"
	const nsBySub = "/subscriptions/{subscriptionId}/providers/Microsoft.EventHub/namespaces"

	srv.HandleFunc("PUT "+ns+"/{name}", handleEHCreateNamespace)
	srv.HandleFunc("GET "+ns+"/{name}", handleEHGetNamespace)
	srv.HandleFunc("PATCH "+ns+"/{name}", handleEHUpdateNamespace)
	srv.HandleFunc("DELETE "+ns+"/{name}", handleEHDeleteNamespace)
	srv.HandleFunc("GET "+ns, handleEHListNamespacesByRG)
	srv.HandleFunc("GET "+nsBySub, handleEHListNamespacesBySub)
	srv.HandleFunc("GET "+ns+"/{name}/networkRuleSets/default", handleEHGetNamespaceNetworkRuleSet)
	srv.HandleFunc("PUT "+ns+"/{name}/networkRuleSets/default", handleEHPutNamespaceNetworkRuleSet)
	srv.HandleFunc("GET "+ns+"/{name}/networkRuleSets", handleEHListNamespaceNetworkRuleSets)
	srv.HandleFunc("GET "+ns+"/{name}/privateLinkResources", handleEHListPrivateLinkResources)
	srv.HandleFunc("GET "+ns+"/{name}/privateEndpointConnections", handleEHListPrivateEndpointConnections)
	srv.HandleFunc("PUT "+ns+"/{name}/privateEndpointConnections/{pec}", handleEHPutPrivateEndpointConnection)
	srv.HandleFunc("GET "+ns+"/{name}/privateEndpointConnections/{pec}", handleEHGetPrivateEndpointConnection)
	srv.HandleFunc("DELETE "+ns+"/{name}/privateEndpointConnections/{pec}", handleEHDeletePrivateEndpointConnection)
	srv.HandleFunc("GET "+ns+"/{name}/networkSecurityPerimeterConfigurations", handleEHListNetworkSecurityPerimeterConfigs)
	srv.HandleFunc("GET "+ns+"/{name}/networkSecurityPerimeterConfigurations/{assoc}", handleEHGetNetworkSecurityPerimeterConfig)
	srv.HandleFunc("POST "+ns+"/{name}/networkSecurityPerimeterConfigurations/{assoc}/reconcile", handleEHReconcileNetworkSecurityPerimeterConfig)

	srv.HandleFunc("GET "+ns+"/{name}/disasterRecoveryConfigs/{alias}/authorizationRules", handleEHListDRAuthorizationRules)
	srv.HandleFunc("GET "+ns+"/{name}/disasterRecoveryConfigs/{alias}/authorizationRules/{rule}", handleEHGetDRAuthorizationRule)
	srv.HandleFunc("POST "+ns+"/{name}/disasterRecoveryConfigs/{alias}/authorizationRules/{rule}/listKeys", handleEHListDRAuthorizationRuleKeys)

	srv.HandleFunc("PUT "+ns+"/{name}/authorizationRules/{rule}", ehAuthRuleCreate("Microsoft.EventHub/namespaces/authorizationRules", "namespaces"))
	srv.HandleFunc("GET "+ns+"/{name}/authorizationRules/{rule}", ehAuthRuleGet("namespaces"))
	srv.HandleFunc("DELETE "+ns+"/{name}/authorizationRules/{rule}", ehAuthRuleDelete("namespaces"))
	srv.HandleFunc("GET "+ns+"/{name}/authorizationRules", ehAuthRuleList("namespaces"))
	srv.HandleFunc("POST "+ns+"/{name}/authorizationRules/{rule}/listKeys", ehAuthRuleListKeys("namespaces"))
	srv.HandleFunc("POST "+ns+"/{name}/authorizationRules/{rule}/regenerateKeys", ehAuthRuleRegenerateKeys("namespaces"))

	srv.HandleFunc("PUT "+ns+"/{name}/eventhubs/{eventhub}", handleEHCreateEventHub)
	srv.HandleFunc("GET "+ns+"/{name}/eventhubs/{eventhub}", handleEHGetEventHub)
	srv.HandleFunc("DELETE "+ns+"/{name}/eventhubs/{eventhub}", handleEHDeleteEventHub)
	srv.HandleFunc("GET "+ns+"/{name}/eventhubs", handleEHListEventHubs)

	srv.HandleFunc("PUT "+ns+"/{name}/eventhubs/{eventhub}/authorizationRules/{rule}", ehAuthRuleCreate("Microsoft.EventHub/namespaces/eventhubs/authorizationRules", "eventhubs"))
	srv.HandleFunc("GET "+ns+"/{name}/eventhubs/{eventhub}/authorizationRules/{rule}", ehAuthRuleGet("eventhubs"))
	srv.HandleFunc("DELETE "+ns+"/{name}/eventhubs/{eventhub}/authorizationRules/{rule}", ehAuthRuleDelete("eventhubs"))
	srv.HandleFunc("GET "+ns+"/{name}/eventhubs/{eventhub}/authorizationRules", ehAuthRuleList("eventhubs"))
	srv.HandleFunc("POST "+ns+"/{name}/eventhubs/{eventhub}/authorizationRules/{rule}/listKeys", ehAuthRuleListKeys("eventhubs"))
	srv.HandleFunc("POST "+ns+"/{name}/eventhubs/{eventhub}/authorizationRules/{rule}/regenerateKeys", ehAuthRuleRegenerateKeys("eventhubs"))

	srv.HandleFunc("PUT "+ns+"/{name}/eventhubs/{eventhub}/consumerGroups/{consumerGroup}", handleEHCreateConsumerGroup)
	srv.HandleFunc("GET "+ns+"/{name}/eventhubs/{eventhub}/consumerGroups/{consumerGroup}", handleEHGetConsumerGroup)
	srv.HandleFunc("DELETE "+ns+"/{name}/eventhubs/{eventhub}/consumerGroups/{consumerGroup}", handleEHDeleteConsumerGroup)
	srv.HandleFunc("GET "+ns+"/{name}/eventhubs/{eventhub}/consumerGroups", handleEHListConsumerGroups)
	srv.HandleFunc("PUT "+ns+"/{name}/eventhubs/{eventhub}/consumergroups/{consumerGroup}", handleEHCreateConsumerGroup)
	srv.HandleFunc("GET "+ns+"/{name}/eventhubs/{eventhub}/consumergroups/{consumerGroup}", handleEHGetConsumerGroup)
	srv.HandleFunc("DELETE "+ns+"/{name}/eventhubs/{eventhub}/consumergroups/{consumerGroup}", handleEHDeleteConsumerGroup)
	srv.HandleFunc("GET "+ns+"/{name}/eventhubs/{eventhub}/consumergroups", handleEHListConsumerGroups)
}

func ehNamespaceID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventHub/namespaces/%s", sub, rg, name)
}

func ehEventHubID(sub, rg, ns, hub string) string {
	return ehNamespaceID(sub, rg, ns) + "/eventhubs/" + hub
}

func ehConsumerGroupID(sub, rg, ns, hub, group string) string {
	return ehEventHubID(sub, rg, ns, hub) + "/consumerGroups/" + group
}

func handleEHCreateNamespace(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	var req EHNamespace
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	now := time.Now().UTC()
	id := ehNamespaceID(sub, rg, name)
	n := EHNamespace{
		ID:       id,
		Name:     name,
		Type:     "Microsoft.EventHub/namespaces",
		Location: req.Location,
		SKU:      req.SKU,
		Tags:     req.Tags,
		Properties: map[string]any{
			"provisioningState":  "Creating",
			"status":             "Activating",
			"createdAt":          now.Format(time.RFC3339Nano),
			"updatedAt":          now.Format(time.RFC3339Nano),
			"serviceBusEndpoint": azureServiceBusEndpointURL(r, name),
		},
	}
	if n.SKU == nil {
		n.SKU = map[string]any{}
	}
	ehApplyNamespaceDefaults(&n, r)
	for k, v := range req.Properties {
		n.Properties[k] = v
	}
	ehApplyNamespaceDefaults(&n, r)
	n.Properties["provisioningState"] = "Creating"
	n.Properties["status"] = "Activating"
	ehNamespaces.Put(id, n)
	rootID := id + "/authorizationRules/RootManageSharedAccessKey"
	if _, ok := ehAuthRules.Get(rootID); !ok {
		ehAuthRules.Put(rootID, EHAuthorizationRule{
			ID:   rootID,
			Name: "RootManageSharedAccessKey",
			Type: "Microsoft.EventHub/namespaces/authorizationRules",
			Properties: map[string]any{
				"rights": []string{"Listen", "Send", "Manage"},
			},
		})
	}
	opID := issueAzureAsyncOperation(func() {
		ehNamespaces.Update(id, func(stored *EHNamespace) {
			if stored.Properties == nil {
				stored.Properties = map[string]any{}
			}
			stored.Properties["provisioningState"] = "Succeeded"
			stored.Properties["status"] = "Active"
			stored.Properties["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		})
	})
	opURL := azureAsyncOperationHeader(r, sub, "Microsoft.EventHub", n.Location, "operationStatuses", opID, r.URL.Query().Get("api-version"))
	writeAzureAsyncCreateHeaders(w, opURL, azureCurrentRequestURL(r))
	sim.WriteJSON(w, http.StatusCreated, n)
}

func handleEHGetNamespace(w http.ResponseWriter, r *http.Request) {
	id := ehNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	n, ok := ehNamespaces.Get(id)
	if !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	ehApplyNamespaceDefaults(&n, r)
	sim.WriteJSON(w, http.StatusOK, n)
}

func handleEHDeleteNamespace(w http.ResponseWriter, r *http.Request) {
	id := ehNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if !ehNamespaces.Delete(id) {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	prefix := id + "/"
	for _, hub := range ehEventHubs.List() {
		if strings.HasPrefix(hub.ID, prefix) {
			ehEventHubs.Delete(hub.ID)
			ehDropPartitionLogs(sim.PathParam(r, "name"), hub)
		}
	}
	for _, group := range ehConsumerGroups.List() {
		if strings.HasPrefix(group.ID, prefix) {
			ehConsumerGroups.Delete(group.ID)
		}
	}
	for _, rule := range ehAuthRules.List() {
		if strings.HasPrefix(rule.ID, prefix) {
			ehDropAuthRule(rule.ID)
		}
	}
	for _, pec := range ehPrivateConns.List() {
		if strings.HasPrefix(pec.ID, prefix) {
			ehPrivateConns.Delete(pec.ID)
		}
	}
	ehNetworkRules.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleEHListNamespacesByRG(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventHub/namespaces/",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"))
	var out []EHNamespace
	for _, n := range ehNamespaces.List() {
		if strings.HasPrefix(n.ID, prefix) {
			ehApplyNamespaceDefaults(&n, r)
			out = append(out, n)
		}
	}
	if out == nil {
		out = []EHNamespace{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func ehDefaultNetworkRuleSet(id string) EHNetworkRuleSet {
	return EHNetworkRuleSet{
		ID:   id + "/networkRuleSets/default",
		Name: "default",
		Type: "Microsoft.EventHub/namespaces/networkRuleSets",
		Properties: map[string]any{
			"defaultAction":               "Allow",
			"publicNetworkAccess":         "Enabled",
			"trustedServiceAccessEnabled": false,
			"virtualNetworkRules":         []any{},
			"ipRules":                     []any{},
		},
	}
}

func handleEHGetNamespaceNetworkRuleSet(w http.ResponseWriter, r *http.Request) {
	id := ehNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := ehNamespaces.Get(id); !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	ruleSet, ok := ehNetworkRules.Get(id)
	if !ok {
		ruleSet = ehDefaultNetworkRuleSet(id)
	}
	sim.WriteJSON(w, http.StatusOK, ruleSet)
}

func handleEHPutNamespaceNetworkRuleSet(w http.ResponseWriter, r *http.Request) {
	id := ehNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := ehNamespaces.Get(id); !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	var req EHNetworkRuleSet
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	ruleSet := ehDefaultNetworkRuleSet(id)
	for k, v := range req.Properties {
		ruleSet.Properties[k] = v
	}
	ehNetworkRules.Put(id, ruleSet)
	sim.WriteJSON(w, http.StatusOK, ruleSet)
}

func handleEHListNamespaceNetworkRuleSets(w http.ResponseWriter, r *http.Request) {
	id := ehNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := ehNamespaces.Get(id); !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	ruleSet, ok := ehNetworkRules.Get(id)
	if !ok {
		ruleSet = ehDefaultNetworkRuleSet(id)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []EHNetworkRuleSet{ruleSet}})
}

// handleEHUpdateNamespace applies a PATCH to an Event Hubs namespace.
func handleEHUpdateNamespace(w http.ResponseWriter, r *http.Request) {
	id := ehNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	n, ok := ehNamespaces.Get(id)
	if !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	var req EHNamespace
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if req.Location != "" {
		n.Location = req.Location
	}
	if req.SKU != nil {
		n.SKU = req.SKU
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
	ehApplyNamespaceDefaults(&n, r)
	n.Properties["provisioningState"] = "Succeeded"
	n.Properties["status"] = "Active"
	ehNamespaces.Put(id, n)
	sim.WriteJSON(w, http.StatusOK, n)
}

// handleEHListNamespacesBySub lists every Event Hubs namespace in the
// subscription.
func handleEHListNamespacesBySub(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("/subscriptions/%s/", sim.PathParam(r, "subscriptionId"))
	var out []EHNamespace
	for _, n := range ehNamespaces.List() {
		if strings.HasPrefix(n.ID, prefix) {
			ehApplyNamespaceDefaults(&n, r)
			out = append(out, n)
		}
	}
	if out == nil {
		out = []EHNamespace{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func ehPrivateEndpointConnectionID(sub, rg, name, pec string) string {
	return ehNamespaceID(sub, rg, name) + "/privateEndpointConnections/" + pec
}

func handleEHListPrivateEndpointConnections(w http.ResponseWriter, r *http.Request) {
	id := ehNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := ehNamespaces.Get(id); !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	prefix := id + "/privateEndpointConnections/"
	var out []EHPrivateEndpointConnection
	for _, pec := range ehPrivateConns.List() {
		if strings.HasPrefix(pec.ID, prefix) {
			out = append(out, pec)
		}
	}
	if out == nil {
		out = []EHPrivateEndpointConnection{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleEHGetPrivateEndpointConnection(w http.ResponseWriter, r *http.Request) {
	id := ehPrivateEndpointConnectionID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "pec"))
	pec, ok := ehPrivateConns.Get(id)
	if !ok {
		AzureError(w, "ResourceNotFound", "private endpoint connection not found", http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, pec)
}

func handleEHPutPrivateEndpointConnection(w http.ResponseWriter, r *http.Request) {
	sub, rg, name, pecName := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "pec")
	if _, ok := ehNamespaces.Get(ehNamespaceID(sub, rg, name)); !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	var req EHPrivateEndpointConnection
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := ehPrivateEndpointConnectionID(sub, rg, name, pecName)
	props := map[string]any{}
	for k, v := range req.Properties {
		props[k] = v
	}
	props["provisioningState"] = "Succeeded"
	pec := EHPrivateEndpointConnection{
		ID:         id,
		Name:       pecName,
		Type:       "Microsoft.EventHub/namespaces/privateEndpointConnections",
		Properties: props,
	}
	ehPrivateConns.Put(id, pec)
	sim.WriteJSON(w, http.StatusOK, pec)
}

func handleEHDeletePrivateEndpointConnection(w http.ResponseWriter, r *http.Request) {
	id := ehPrivateEndpointConnectionID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "pec"))
	if !ehPrivateConns.Delete(id) {
		AzureError(w, "ResourceNotFound", "private endpoint connection not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleEHListPrivateLinkResources returns the namespace's private link
// resource groups (the single "namespace" group).
func handleEHListPrivateLinkResources(w http.ResponseWriter, r *http.Request) {
	id := ehNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := ehNamespaces.Get(id); !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []map[string]any{{
			"id":   id + "/privateLinkResources/namespace",
			"name": "namespace",
			"type": "Microsoft.EventHub/namespaces/privateLinkResources",
			"properties": map[string]any{
				"groupId":           "namespace",
				"requiredMembers":   []string{"namespace"},
				"requiredZoneNames": []string{"privatelink.servicebus.windows.net"},
			},
		}},
	})
}

// handleEHListNetworkSecurityPerimeterConfigs returns the namespace's
// network security perimeter configurations (none until a perimeter is
// associated, as real Azure returns).
func handleEHListNetworkSecurityPerimeterConfigs(w http.ResponseWriter, r *http.Request) {
	id := ehNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := ehNamespaces.Get(id); !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
}

func handleEHGetNetworkSecurityPerimeterConfig(w http.ResponseWriter, r *http.Request) {
	id := ehNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := ehNamespaces.Get(id); !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	AzureError(w, "ResourceNotFound", "network security perimeter configuration not found", http.StatusNotFound)
}

func handleEHReconcileNetworkSecurityPerimeterConfig(w http.ResponseWriter, r *http.Request) {
	id := ehNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := ehNamespaces.Get(id); !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ehDRAuthRuleParent validates the namespace exists and returns its
// resource ID. A GEO-DR alias surfaces the primary namespace's SAS
// authorization rules, so they are read through the namespace store.
func ehDRAuthRuleParent(r *http.Request) (string, bool) {
	id := ehNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	_, ok := ehNamespaces.Get(id)
	return id, ok
}

func handleEHListDRAuthorizationRules(w http.ResponseWriter, r *http.Request) {
	parent, ok := ehDRAuthRuleParent(r)
	if !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	prefix := parent + "/authorizationRules/"
	var out []EHAuthorizationRule
	for _, rule := range ehAuthRules.List() {
		if strings.HasPrefix(rule.ID, prefix) {
			out = append(out, rule)
		}
	}
	if out == nil {
		out = []EHAuthorizationRule{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleEHGetDRAuthorizationRule(w http.ResponseWriter, r *http.Request) {
	parent, ok := ehDRAuthRuleParent(r)
	if !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	rule, ok := ehAuthRules.Get(parent + "/authorizationRules/" + sim.PathParam(r, "rule"))
	if !ok {
		AzureError(w, "ResourceNotFound", "authorization rule not found", http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, rule)
}

func handleEHListDRAuthorizationRuleKeys(w http.ResponseWriter, r *http.Request) {
	parent, ok := ehDRAuthRuleParent(r)
	if !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	ruleName := sim.PathParam(r, "rule")
	ruleID := parent + "/authorizationRules/" + ruleName
	if _, ok := ehAuthRules.Get(ruleID); !ok {
		AzureError(w, "ResourceNotFound", "authorization rule not found", http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, ehAuthKeysBody(r, ruleID, sim.PathParam(r, "name"), ruleName, "namespaces"))
}

func ehApplyNamespaceDefaults(n *EHNamespace, r *http.Request) {
	if n.SKU == nil {
		n.SKU = map[string]any{}
	}
	if _, ok := n.SKU["name"]; !ok {
		n.SKU["name"] = "Standard"
	}
	if _, ok := n.SKU["tier"]; !ok {
		n.SKU["tier"] = n.SKU["name"]
	}
	if _, ok := n.SKU["capacity"]; !ok {
		n.SKU["capacity"] = 1
	}
	if n.Properties == nil {
		n.Properties = map[string]any{}
	}
	if _, ok := n.Properties["provisioningState"]; !ok {
		n.Properties["provisioningState"] = "Succeeded"
	}
	if _, ok := n.Properties["status"]; !ok {
		n.Properties["status"] = "Active"
	}
	if _, ok := n.Properties["createdAt"]; !ok {
		n.Properties["createdAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if _, ok := n.Properties["updatedAt"]; !ok {
		n.Properties["updatedAt"] = n.Properties["createdAt"]
	}
	if _, ok := n.Properties["serviceBusEndpoint"]; !ok {
		n.Properties["serviceBusEndpoint"] = azureServiceBusEndpointURL(r, n.Name)
	}
	if _, ok := n.Properties["isAutoInflateEnabled"]; !ok {
		n.Properties["isAutoInflateEnabled"] = false
	}
	if _, ok := n.Properties["maximumThroughputUnits"]; !ok {
		n.Properties["maximumThroughputUnits"] = 0
	}
	if _, ok := n.Properties["publicNetworkAccess"]; !ok {
		n.Properties["publicNetworkAccess"] = "Enabled"
	}
	if _, ok := n.Properties["minimumTlsVersion"]; !ok {
		n.Properties["minimumTlsVersion"] = "1.2"
	}
	if _, ok := n.Properties["disableLocalAuth"]; !ok {
		n.Properties["disableLocalAuth"] = false
	}
}

func handleEHCreateEventHub(w http.ResponseWriter, r *http.Request) {
	sub, rg, nsName, hubName := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "eventhub")
	if _, ok := ehNamespaces.Get(ehNamespaceID(sub, rg, nsName)); !ok {
		AzureError(w, "ResourceNotFound", "namespace not found", http.StatusNotFound)
		return
	}
	var req EHEventHub
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	now := time.Now().UTC()
	partitionCount := ehPartitionCount(req.Properties)
	props := map[string]any{
		"createdAt":              now.Format(time.RFC3339Nano),
		"updatedAt":              now.Format(time.RFC3339Nano),
		"messageRetentionInDays": 7,
		"partitionCount":         partitionCount,
		"partitionIds":           ehPartitionIDs(partitionCount),
		"status":                 "Active",
	}
	for k, v := range req.Properties {
		props[k] = v
	}
	props["partitionCount"] = partitionCount
	props["partitionIds"] = ehPartitionIDs(partitionCount)
	hub := EHEventHub{
		ID:         ehEventHubID(sub, rg, nsName, hubName),
		Name:       hubName,
		Type:       "Microsoft.EventHub/namespaces/eventhubs",
		Properties: props,
		CreatedAt:  now,
	}
	ehEventHubs.Put(hub.ID, hub)
	defaultGroupID := hub.ID + "/consumerGroups/$Default"
	if _, ok := ehConsumerGroups.Get(defaultGroupID); !ok {
		ehConsumerGroups.Put(defaultGroupID, EHConsumerGroup{
			ID:   defaultGroupID,
			Name: "$Default",
			Type: "Microsoft.EventHub/namespaces/eventhubs/consumergroups",
			Properties: map[string]any{
				"createdAt": now.Format(time.RFC3339Nano),
				"updatedAt": now.Format(time.RFC3339Nano),
			},
		})
	}
	sim.WriteJSON(w, http.StatusOK, hub)
}

func handleEHGetEventHub(w http.ResponseWriter, r *http.Request) {
	id := ehEventHubID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "eventhub"))
	hub, ok := ehEventHubs.Get(id)
	if !ok {
		AzureError(w, "ResourceNotFound", "event hub not found", http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, hub)
}

func handleEHDeleteEventHub(w http.ResponseWriter, r *http.Request) {
	id := ehEventHubID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "eventhub"))
	hub, ok := ehEventHubs.Get(id)
	if !ok || !ehEventHubs.Delete(id) {
		AzureError(w, "ResourceNotFound", "event hub not found", http.StatusNotFound)
		return
	}
	ehDropPartitionLogs(sim.PathParam(r, "name"), hub)
	prefix := id + "/"
	for _, group := range ehConsumerGroups.List() {
		if strings.HasPrefix(group.ID, prefix) {
			ehConsumerGroups.Delete(group.ID)
		}
	}
	for _, rule := range ehAuthRules.List() {
		if strings.HasPrefix(rule.ID, prefix) {
			ehDropAuthRule(rule.ID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func ehDropPartitionLogs(namespace string, hub EHEventHub) {
	for _, partition := range ehPartitionIDs(ehPartitionCount(hub.Properties)) {
		ehLog.Drop(ehPartitionKey(namespace, hub.Name, partition))
	}
}

func handleEHListEventHubs(w http.ResponseWriter, r *http.Request) {
	prefix := ehNamespaceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/eventhubs/"
	var out []EHEventHub
	for _, hub := range ehEventHubs.List() {
		if strings.HasPrefix(hub.ID, prefix) {
			out = append(out, hub)
		}
	}
	if out == nil {
		out = []EHEventHub{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleEHCreateConsumerGroup(w http.ResponseWriter, r *http.Request) {
	sub, rg, nsName, hubName, groupName := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "eventhub"), sim.PathParam(r, "consumerGroup")
	if _, ok := ehEventHubs.Get(ehEventHubID(sub, rg, nsName, hubName)); !ok {
		AzureError(w, "ResourceNotFound", "event hub not found", http.StatusNotFound)
		return
	}
	var req EHConsumerGroup
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	now := time.Now().UTC()
	props := map[string]any{
		"createdAt": now.Format(time.RFC3339Nano),
		"updatedAt": now.Format(time.RFC3339Nano),
	}
	for k, v := range req.Properties {
		props[k] = v
	}
	group := EHConsumerGroup{
		ID:         ehConsumerGroupID(sub, rg, nsName, hubName, groupName),
		Name:       groupName,
		Type:       "Microsoft.EventHub/namespaces/eventhubs/consumergroups",
		Properties: props,
	}
	ehConsumerGroups.Put(group.ID, group)
	sim.WriteJSON(w, http.StatusOK, group)
}

func handleEHGetConsumerGroup(w http.ResponseWriter, r *http.Request) {
	id := ehConsumerGroupID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "eventhub"), sim.PathParam(r, "consumerGroup"))
	group, ok := ehConsumerGroups.Get(id)
	if !ok {
		AzureError(w, "ResourceNotFound", "consumer group not found", http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, group)
}

func handleEHDeleteConsumerGroup(w http.ResponseWriter, r *http.Request) {
	id := ehConsumerGroupID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "eventhub"), sim.PathParam(r, "consumerGroup"))
	if !ehConsumerGroups.Delete(id) {
		AzureError(w, "ResourceNotFound", "consumer group not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleEHListConsumerGroups(w http.ResponseWriter, r *http.Request) {
	prefix := ehEventHubID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "eventhub")) + "/consumerGroups/"
	var out []EHConsumerGroup
	for _, group := range ehConsumerGroups.List() {
		if strings.HasPrefix(group.ID, prefix) {
			out = append(out, group)
		}
	}
	if out == nil {
		out = []EHConsumerGroup{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func ehAuthRuleCreate(resourceType, scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parent, ok := ehAuthRuleParentID(r, scope)
		if !ok {
			AzureError(w, "ResourceNotFound", "parent not found", http.StatusNotFound)
			return
		}
		ruleName := sim.PathParam(r, "rule")
		var req EHAuthorizationRule
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		rights := []string{"Listen", "Send"}
		if raw, ok := req.Properties["rights"].([]any); ok && len(raw) > 0 {
			rights = nil
			for _, v := range raw {
				rights = append(rights, fmt.Sprint(v))
			}
		}
		rule := EHAuthorizationRule{
			ID:   parent + "/authorizationRules/" + ruleName,
			Name: ruleName,
			Type: resourceType,
			Properties: map[string]any{
				"rights": rights,
			},
		}
		ehAuthRules.Put(rule.ID, rule)
		sim.WriteJSON(w, http.StatusOK, rule)
	}
}

func ehAuthRuleGet(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parent, ok := ehAuthRuleParentID(r, scope)
		if !ok {
			AzureError(w, "ResourceNotFound", "parent not found", http.StatusNotFound)
			return
		}
		rule, ok := ehAuthRules.Get(parent + "/authorizationRules/" + sim.PathParam(r, "rule"))
		if !ok {
			AzureError(w, "ResourceNotFound", "authorization rule not found", http.StatusNotFound)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rule)
	}
}

// ehDropAuthRule removes an authorization rule together with its
// key-rotation state, so a later rule created under the same ID starts from
// fresh key material.
func ehDropAuthRule(ruleID string) {
	ehAuthRules.Delete(ruleID)
	azureDropKeyGens(ruleID, "primary", "secondary")
}

func ehAuthRuleDelete(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parent, ok := ehAuthRuleParentID(r, scope)
		if !ok {
			AzureError(w, "ResourceNotFound", "parent not found", http.StatusNotFound)
			return
		}
		ehDropAuthRule(parent + "/authorizationRules/" + sim.PathParam(r, "rule"))
		w.WriteHeader(http.StatusNoContent)
	}
}

func ehAuthRuleList(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parent, ok := ehAuthRuleParentID(r, scope)
		if !ok {
			AzureError(w, "ResourceNotFound", "parent not found", http.StatusNotFound)
			return
		}
		prefix := parent + "/authorizationRules/"
		var out []EHAuthorizationRule
		for _, rule := range ehAuthRules.List() {
			if strings.HasPrefix(rule.ID, prefix) {
				out = append(out, rule)
			}
		}
		if out == nil {
			out = []EHAuthorizationRule{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	}
}

func ehAuthRuleListKeys(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ruleID, ok := ehResolveAuthRule(w, r, scope)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, ehAuthKeysBody(r, ruleID, sim.PathParam(r, "name"), sim.PathParam(r, "rule"), scope))
	}
}

func ehAuthRuleRegenerateKeys(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ruleID, ok := ehResolveAuthRule(w, r, scope)
		if !ok {
			return
		}
		// RegenerateAccessKeyParameters: keyType selects the slot; the optional
		// key field pins the slot to caller-supplied material instead of an
		// auto-generated value — both exactly as real Event Hubs behaves.
		var req struct {
			KeyType string `json:"keyType"`
			Key     string `json:"key"`
		}
		_ = sim.ReadJSON(r, &req)
		switch req.KeyType {
		case "PrimaryKey":
			azureBumpKeyGen(ruleID, "primary", req.Key)
		case "SecondaryKey":
			azureBumpKeyGen(ruleID, "secondary", req.Key)
		default:
			AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"keyType must be 'PrimaryKey' or 'SecondaryKey', got %q", req.KeyType)
			return
		}
		sim.WriteJSON(w, http.StatusOK, ehAuthKeysBody(r, ruleID, sim.PathParam(r, "name"), sim.PathParam(r, "rule"), scope))
	}
}

// ehResolveAuthRule validates the parent + rule from the request path and
// returns the rule's full resource ID.
func ehResolveAuthRule(w http.ResponseWriter, r *http.Request, scope string) (parent, ruleID string, ok bool) {
	parent, ok = ehAuthRuleParentID(r, scope)
	if !ok {
		AzureError(w, "ResourceNotFound", "parent not found", http.StatusNotFound)
		return "", "", false
	}
	ruleID = parent + "/authorizationRules/" + sim.PathParam(r, "rule")
	if _, ok := ehAuthRules.Get(ruleID); !ok {
		AzureError(w, "ResourceNotFound", "authorization rule not found", http.StatusNotFound)
		return "", "", false
	}
	return parent, ruleID, true
}

// ehAuthKeysBody is the canonical Event Hubs AccessKeys shape listKeys and
// regenerateKeys both return. Keys are deterministic 44-char base64 strings
// derived from the rule resource ID + rotation generation (mirroring the
// real-Azure SAS-key shape); the connection strings embed the current key
// material, so both reflect every rotation performed so far.
func ehAuthKeysBody(r *http.Request, ruleID, namespace, ruleName, scope string) map[string]any {
	primary := azureKeyMaterial32(ruleID, "primary")
	secondary := azureKeyMaterial32(ruleID, "secondary")
	endpoint := "Endpoint=" + azureServiceBusConnectionEndpoint(r, namespace)
	entityPath := ""
	if scope == "eventhubs" {
		entityPath = ";EntityPath=" + sim.PathParam(r, "eventhub")
	}
	return map[string]any{
		"keyName":                   ruleName,
		"primaryKey":                primary,
		"secondaryKey":              secondary,
		"primaryConnectionString":   endpoint + ";SharedAccessKeyName=" + ruleName + ";SharedAccessKey=" + primary + entityPath,
		"secondaryConnectionString": endpoint + ";SharedAccessKeyName=" + ruleName + ";SharedAccessKey=" + secondary + entityPath,
	}
}

func ehAuthRuleParentID(r *http.Request, scope string) (string, bool) {
	sub, rg, nsName := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")
	switch scope {
	case "namespaces":
		id := ehNamespaceID(sub, rg, nsName)
		_, ok := ehNamespaces.Get(id)
		return id, ok
	case "eventhubs":
		id := ehEventHubID(sub, rg, nsName, sim.PathParam(r, "eventhub"))
		_, ok := ehEventHubs.Get(id)
		return id, ok
	default:
		return "", false
	}
}

func ehPartitionCount(props map[string]any) int {
	count := 1
	switch v := props["partitionCount"].(type) {
	case float64:
		count = int(v)
	case int:
		count = v
	}
	if count < 1 {
		count = 1
	}
	return count
}

func ehPartitionIDs(count int) []string {
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, strconv.Itoa(i))
	}
	return out
}

func ehAMQPHandleRPC(namespace string, req *amqp.Message) (*amqp.Message, bool) {
	if req.ApplicationProperties == nil {
		return nil, false
	}
	if fmt.Sprint(req.ApplicationProperties["operation"]) != "READ" {
		return nil, false
	}
	entityType := fmt.Sprint(req.ApplicationProperties["type"])
	hubName := fmt.Sprint(req.ApplicationProperties["name"])
	switch entityType {
	case "com.microsoft:eventhub":
		hub, ok := ehAMQPFindHub(namespace, hubName)
		if !ok {
			return ehAMQPError(req, 404, "Event Hub not found"), true
		}
		partitions := ehPartitionIDs(ehPartitionCount(hub.Properties))
		return ehAMQPValue(req, map[string]any{
			"name":                  hub.Name,
			"created_at":            hub.CreatedAt,
			"partition_ids":         partitions,
			"georeplication_factor": int64(0),
		}), true
	case "com.microsoft:partition":
		partition := fmt.Sprint(req.ApplicationProperties["partition"])
		hub, ok := ehAMQPFindHub(namespace, hubName)
		if !ok {
			return ehAMQPError(req, 404, "Event Hub not found"), true
		}
		key := ehPartitionKey(namespace, hub.Name, partition)
		ehTrim(hub, key, time.Now())
		head := ehLog.Head(key)
		lastSeq := int64(-1)
		lastOffset := ""
		lastTime := time.Time{}
		if last, ok := ehLog.Last(key); ok {
			lastSeq = last.Seq
			lastOffset = strconv.FormatInt(last.Seq, 10)
			lastTime = time.UnixMilli(last.Time).UTC()
		}
		// begin_sequence_number is the oldest sequence a consumer can still
		// read; once events age out of retention it advances past them.
		beginSeq := head.First
		return ehAMQPValue(req, map[string]any{
			"name":                          hub.Name,
			"partition":                     partition,
			"begin_sequence_number":         beginSeq,
			"last_enqueued_sequence_number": lastSeq,
			"last_enqueued_offset":          lastOffset,
			"last_enqueued_time_utc":        lastTime,
			"is_partition_empty":            head.First == head.Next,
		}), true
	default:
		return nil, false
	}
}

func ehAMQPValue(req *amqp.Message, value map[string]any) *amqp.Message {
	return &amqp.Message{
		Properties:            &amqp.MessageProperties{CorrelationID: req.Properties.MessageID},
		ApplicationProperties: map[string]any{"status-code": int32(200), "status-description": "OK"},
		Value:                 value,
	}
}

func ehAMQPError(req *amqp.Message, code int32, description string) *amqp.Message {
	return &amqp.Message{
		Properties:            &amqp.MessageProperties{CorrelationID: req.Properties.MessageID},
		ApplicationProperties: map[string]any{"status-code": code, "status-description": description},
	}
}

func ehAMQPFindHub(namespace, hubName string) (EHEventHub, bool) {
	suffix := "/namespaces/" + namespace + "/eventhubs/" + hubName
	for _, hub := range ehEventHubs.List() {
		if strings.HasSuffix(hub.ID, suffix) {
			return hub, true
		}
	}
	return EHEventHub{}, false
}

func ehAMQPIsSenderAddress(namespace, address string) bool {
	hub, _, ok := ehAMQPParseEventHubAddress(address)
	if !ok {
		return false
	}
	_, exists := ehAMQPFindHub(namespace, hub)
	return exists
}

func ehAMQPIsReceiverAddress(namespace, address string) bool {
	hub, partition, ok := ehAMQPParseConsumerAddress(address)
	if !ok || partition == "" {
		return false
	}
	_, exists := ehAMQPFindHub(namespace, hub)
	return exists
}

func ehAMQPEnqueue(namespace, address string, msg *amqp.Message) {
	hubName, partitionID, ok := ehAMQPParseEventHubAddress(address)
	if !ok {
		return
	}
	hub, ok := ehAMQPFindHub(namespace, hubName)
	if !ok {
		return
	}
	if partitionID == "" {
		partitionID = ehSelectPartition(hub, msg)
	}
	var events []ehEvent
	for _, event := range ehExpandAMQPEvents(msg) {
		raw, err := event.MarshalBinary()
		if err != nil {
			continue
		}
		events = append(events, ehEvent{AMQP: raw})
	}
	key := ehPartitionKey(namespace, hub.Name, partitionID)
	now := time.Now()
	ehTrim(hub, key, now)
	ehLog.Append(key, now, events...)
}

// ehRetention is how long the hub keeps an event: retentionDescription's
// retentionTimeInHours when set, else messageRetentionInDays.
func ehRetention(hub EHEventHub) time.Duration {
	if desc, ok := hub.Properties["retentionDescription"].(map[string]any); ok {
		if hours, ok := desc["retentionTimeInHours"].(float64); ok && hours > 0 {
			return time.Duration(hours * float64(time.Hour))
		}
	}
	switch days := hub.Properties["messageRetentionInDays"].(type) {
	case float64:
		if days > 0 {
			return time.Duration(days * 24 * float64(time.Hour))
		}
	case int:
		if days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	return ehDefaultRetention
}

const ehDefaultRetention = 7 * 24 * time.Hour

// ehTrim ages out the partition's events past the hub's retention.
func ehTrim(hub EHEventHub, key string, now time.Time) {
	ehLog.Trim(key, now.Add(-ehRetention(hub)))
}

func ehExpandAMQPEvents(msg *amqp.Message) []*amqp.Message {
	events := make([]*amqp.Message, 0, len(msg.Data))
	for _, data := range msg.Data {
		var event amqp.Message
		if err := event.UnmarshalBinary(data); err != nil {
			continue
		}
		events = append(events, &event)
	}
	if len(events) == 0 {
		return []*amqp.Message{msg}
	}
	return events
}

// ehAMQPNextEvent renders the event at sequence number seq — or, when that
// aged out, the oldest retained one — and returns the sequence number the
// consumer reads next.
func ehAMQPNextEvent(namespace, address string, seq int64) ([]byte, int64, bool) {
	hubName, partitionID, ok := ehAMQPParseConsumerAddress(address)
	if !ok {
		return nil, seq, false
	}
	key := ehPartitionKey(namespace, hubName, partitionID)
	if hub, ok := ehAMQPFindHub(namespace, hubName); ok {
		ehTrim(hub, key, time.Now())
	}
	recs := ehLog.Read(key, seq, 1)
	if len(recs) == 0 {
		return nil, seq, false
	}
	rec := recs[0]
	out := &amqp.Message{}
	if err := out.UnmarshalBinary(rec.Value.AMQP); err != nil {
		return nil, seq, false
	}
	out.DeliveryTag = []byte(sim.NewUUID())
	if out.Annotations == nil {
		out.Annotations = amqp.Annotations{}
	}
	out.Annotations["x-opt-sequence-number"] = rec.Seq
	out.Annotations["x-opt-enqueued-time"] = time.UnixMilli(rec.Time).UTC()
	out.Annotations["x-opt-offset"] = strconv.FormatInt(rec.Seq, 10)
	body, err := out.MarshalBinary()
	if err != nil {
		return nil, seq, false
	}
	return body, rec.Seq + 1, true
}

var ehStartFilter = regexp.MustCompile(`^amqp\.annotation\.x-opt-(offset|sequence-number|enqueued-time)\s*(>=|>)\s*'([^']*)'$`)

// ehStartPosition resolves a consumer link's selector filter — the start
// position an Event Hubs client asks for — to the sequence number it reads
// first. A link without a filter starts at the oldest retained event.
func ehStartPosition(namespace, address, filter string) int64 {
	hubName, partitionID, ok := ehAMQPParseConsumerAddress(address)
	if !ok {
		return 0
	}
	key := ehPartitionKey(namespace, hubName, partitionID)
	if hub, ok := ehAMQPFindHub(namespace, hubName); ok {
		ehTrim(hub, key, time.Now())
	}
	head := ehLog.Head(key)
	m := ehStartFilter.FindStringSubmatch(strings.TrimSpace(filter))
	if m == nil {
		return head.First
	}
	inclusive := m[2] == ">="
	if m[1] == "offset" && m[3] == "@latest" {
		return head.Next
	}
	n, err := strconv.ParseInt(m[3], 10, 64)
	if err != nil {
		return head.First
	}
	if m[1] == "enqueued-time" {
		if !inclusive {
			n++
		}
		return ehLog.SeekTime(key, time.UnixMilli(n))
	}
	if !inclusive {
		n++
	}
	return max(n, head.First)
}

func ehAMQPParseEventHubAddress(address string) (hubName, partitionID string, ok bool) {
	segs := strings.Split(strings.Trim(address, "/"), "/")
	if len(segs) == 1 && segs[0] != "" {
		return segs[0], "", true
	}
	if len(segs) == 3 && strings.EqualFold(segs[1], "Partitions") {
		return segs[0], segs[2], true
	}
	return "", "", false
}

func ehAMQPParseConsumerAddress(address string) (hubName, partitionID string, ok bool) {
	segs := strings.Split(strings.Trim(address, "/"), "/")
	if len(segs) == 5 && strings.EqualFold(segs[1], "ConsumerGroups") && strings.EqualFold(segs[3], "Partitions") {
		return segs[0], segs[4], true
	}
	return "", "", false
}

func ehSelectPartition(hub EHEventHub, msg *amqp.Message) string {
	ids := streamlog.Modulo(ehPartitionIDs(ehPartitionCount(hub.Properties)))
	if len(ids) == 1 {
		return ids[0]
	}
	key := ""
	if msg.Annotations != nil {
		key = fmt.Sprint(msg.Annotations["x-opt-partition-key"])
	}
	if key == "" && msg.Properties != nil && msg.Properties.MessageID != nil {
		key = fmt.Sprint(msg.Properties.MessageID)
	}
	if key == "" {
		return ids[0]
	}
	return ids.Partition(key)
}

func ehPartitionKey(namespace, hub, partition string) string {
	return namespace + "/" + hub + "/" + partition
}
