package main

import (
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Microsoft.Cache/Redis ARM control plane. Real Azure exposes a
// full instance lifecycle (create / get / list / patch / delete)
// plus FirewallRule + LinkedServer + Access Policy sub-resources;
// the sim implements the load-bearing CRUD slice. Data plane
// (the actual Redis protocol) is out of scope — terraform's
// `azurerm_redis_cache` resource only needs the ARM lifecycle.

// RedisCache mirrors RedisResource: sku lives under properties
// (RedisProperties allOf RedisCreateProperties), never at the top level.
type RedisCache struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
}

// RedisFirewallRule is one per-cache firewall rule (start..end IP).
// Real Azure stores them as sub-resources at
// /Redis/{cache}/firewallRules/{name}; terraform-provider-azurerm
// uses azurerm_redis_firewall_rule.
type RedisFirewallRule struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
}

// RedisSubResource is the generic shape for a Redis child resource
// (access policy, access-policy assignment, linked server, patch schedule,
// private endpoint connection): an ARM resource with a free-form properties
// bag. The handlers echo the operator-supplied properties; resources whose
// schema includes provisioningState additionally get it stamped to Succeeded.
type RedisSubResource struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

var (
	redisCaches                  sim.Store[RedisCache]
	redisFirewallRules           sim.Store[RedisFirewallRule]
	redisAccessPolicies          sim.Store[RedisSubResource]
	redisAccessPolicyAssignments sim.Store[RedisSubResource]
	redisLinkedServers           sim.Store[RedisSubResource]
	redisPatchSchedules          sim.Store[RedisSubResource]
	redisPrivateConns            sim.Store[RedisSubResource]
)

func registerCacheRedis(srv *sim.Server) {
	makeAzureKeyGens(srv)
	redisCaches = sim.MakeStore[RedisCache](srv.DB(), "redis_caches")
	redisFirewallRules = sim.MakeStore[RedisFirewallRule](srv.DB(), "redis_firewall_rules")
	redisAccessPolicies = sim.MakeStore[RedisSubResource](srv.DB(), "redis_access_policies")
	redisAccessPolicyAssignments = sim.MakeStore[RedisSubResource](srv.DB(), "redis_access_policy_assignments")
	redisLinkedServers = sim.MakeStore[RedisSubResource](srv.DB(), "redis_linked_servers")
	redisPatchSchedules = sim.MakeStore[RedisSubResource](srv.DB(), "redis_patch_schedules")
	redisPrivateConns = sim.MakeStore[RedisSubResource](srv.DB(), "redis_private_endpoint_connections")

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Cache/Redis"

	srv.HandleFunc("PUT "+armBase+"/{name}", handleRedisCacheCreate)
	srv.HandleFunc("PATCH "+armBase+"/{name}", handleRedisCachePatch)
	srv.HandleFunc("GET "+armBase+"/{name}", handleRedisCacheGet)
	srv.HandleFunc("DELETE "+armBase+"/{name}", handleRedisCacheDelete)
	srv.HandleFunc("GET "+armBase, handleRedisCacheListByRG)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Cache/Redis", handleRedisCacheListBySubscription)

	srv.HandleFunc("PUT "+armBase+"/{name}/firewallRules/{rule}", handleRedisCacheCreateFirewallRule)
	srv.HandleFunc("GET "+armBase+"/{name}/firewallRules/{rule}", handleRedisCacheGetFirewallRule)
	srv.HandleFunc("DELETE "+armBase+"/{name}/firewallRules/{rule}", handleRedisCacheDeleteFirewallRule)
	srv.HandleFunc("GET "+armBase+"/{name}/firewallRules", handleRedisCacheListFirewallRules)

	srv.HandleFunc("POST "+armBase+"/{name}/listKeys", handleRedisCacheListKeys)
	srv.HandleFunc("POST "+armBase+"/{name}/regenerateKey", handleRedisCacheRegenerateKey)
	srv.HandleFunc("POST "+armBase+"/{name}/forceReboot", handleRedisForceReboot)
	srv.HandleFunc("POST "+armBase+"/{name}/import", handleRedisImportData)
	srv.HandleFunc("POST "+armBase+"/{name}/export", handleRedisExportData)
	srv.HandleFunc("POST "+armBase+"/{name}/flush", handleRedisFlushCache)
	srv.HandleFunc("GET "+armBase+"/{name}/listUpgradeNotifications", handleRedisListUpgradeNotifications)

	// Provider-level operations metadata, name-availability check, and the
	// async-operation status endpoint Redis_Create's poller follows.
	srv.HandleFunc("GET /providers/Microsoft.Cache/operations", handleRedisOperationsList)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.Cache/checknameavailability", handleRedisCheckNameAvailability)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Cache/locations/{location}/asyncOperations/{operationId}", handleRedisAsyncOperationStatus)

	// Access policies + assignments (Microsoft Entra ID data-access RBAC).
	srv.HandleFunc("PUT "+armBase+"/{name}/accessPolicies/{policy}", handleRedisAccessPolicyPut)
	srv.HandleFunc("GET "+armBase+"/{name}/accessPolicies/{policy}", handleRedisAccessPolicyGet)
	srv.HandleFunc("DELETE "+armBase+"/{name}/accessPolicies/{policy}", handleRedisAccessPolicyDelete)
	srv.HandleFunc("GET "+armBase+"/{name}/accessPolicies", handleRedisAccessPolicyList)
	srv.HandleFunc("PUT "+armBase+"/{name}/accessPolicyAssignments/{assignment}", handleRedisAccessPolicyAssignmentPut)
	srv.HandleFunc("GET "+armBase+"/{name}/accessPolicyAssignments/{assignment}", handleRedisAccessPolicyAssignmentGet)
	srv.HandleFunc("DELETE "+armBase+"/{name}/accessPolicyAssignments/{assignment}", handleRedisAccessPolicyAssignmentDelete)
	srv.HandleFunc("GET "+armBase+"/{name}/accessPolicyAssignments", handleRedisAccessPolicyAssignmentList)

	// Linked servers (geo-replication).
	srv.HandleFunc("PUT "+armBase+"/{name}/linkedServers/{linkedServer}", handleRedisLinkedServerPut)
	srv.HandleFunc("GET "+armBase+"/{name}/linkedServers/{linkedServer}", handleRedisLinkedServerGet)
	srv.HandleFunc("DELETE "+armBase+"/{name}/linkedServers/{linkedServer}", handleRedisLinkedServerDelete)
	srv.HandleFunc("GET "+armBase+"/{name}/linkedServers", handleRedisLinkedServerList)

	// Patch schedules (maintenance windows).
	srv.HandleFunc("PUT "+armBase+"/{name}/patchSchedules/{schedule}", handleRedisPatchSchedulePut)
	srv.HandleFunc("GET "+armBase+"/{name}/patchSchedules/{schedule}", handleRedisPatchScheduleGet)
	srv.HandleFunc("DELETE "+armBase+"/{name}/patchSchedules/{schedule}", handleRedisPatchScheduleDelete)
	srv.HandleFunc("GET "+armBase+"/{name}/patchSchedules", handleRedisPatchScheduleList)

	// Private endpoint connections + private link resources.
	srv.HandleFunc("PUT "+armBase+"/{name}/privateEndpointConnections/{pec}", handleRedisPECPut)
	srv.HandleFunc("GET "+armBase+"/{name}/privateEndpointConnections/{pec}", handleRedisPECGet)
	srv.HandleFunc("DELETE "+armBase+"/{name}/privateEndpointConnections/{pec}", handleRedisPECDelete)
	srv.HandleFunc("GET "+armBase+"/{name}/privateEndpointConnections", handleRedisPECList)
	srv.HandleFunc("GET "+armBase+"/{name}/privateLinkResources", handleRedisPrivateLinkResources)
}

func redisCacheID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Cache/Redis/%s", sub, rg, name)
}

func redisFirewallRuleKey(sub, rg, cache, rule string) string {
	return sub + "/" + rg + "/" + cache + "/" + rule
}

func handleRedisCacheCreateFirewallRule(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	cache := sim.PathParam(r, "name")
	rule := sim.PathParam(r, "rule")
	if _, ok := redisCaches.Get(redisCacheID(sub, rg, cache)); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Redis cache %q not found", cache)
		return
	}
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	fr := RedisFirewallRule{
		ID:         redisCacheID(sub, rg, cache) + "/firewallRules/" + rule,
		Name:       rule,
		Type:       "Microsoft.Cache/Redis/firewallRules",
		Properties: req.Properties,
	}
	redisFirewallRules.Put(redisFirewallRuleKey(sub, rg, cache, rule), fr)
	sim.WriteJSON(w, http.StatusOK, fr)
}

func handleRedisCacheGetFirewallRule(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	cache := sim.PathParam(r, "name")
	rule := sim.PathParam(r, "rule")
	fr, ok := redisFirewallRules.Get(redisFirewallRuleKey(sub, rg, cache, rule))
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Firewall rule %q not found on cache %q", rule, cache)
		return
	}
	sim.WriteJSON(w, http.StatusOK, fr)
}

func handleRedisCacheDeleteFirewallRule(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	cache := sim.PathParam(r, "name")
	rule := sim.PathParam(r, "rule")
	if !redisFirewallRules.Delete(redisFirewallRuleKey(sub, rg, cache, rule)) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Firewall rule %q not found on cache %q", rule, cache)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleRedisCacheListFirewallRules(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	cache := sim.PathParam(r, "name")
	all := redisFirewallRules.Filter(func(fr RedisFirewallRule) bool {
		return strings.HasPrefix(fr.ID, redisCacheID(sub, rg, cache)+"/firewallRules/")
	})
	if all == nil {
		all = []RedisFirewallRule{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
}

// redisAccessKeysBody is the RedisAccessKeys shape listKeys and regenerateKey
// both return, reflecting every rotation performed so far. Real Azure
// generates 32-byte random keys per cache; the sim derives deterministic
// 44-char base64 strings from the resource ID + key slot + rotation
// generation so the wire shape matches what clients expect (Redis AUTH
// tokens, downstream connection strings that reference the
// `primary_access_key` attribute in terraform-provider-azurerm). Same key
// across reads; distinct between primary / secondary; distinct between
// caches; a new value after every regenerate of that slot.
func redisAccessKeysBody(id string) map[string]any {
	return map[string]any{
		"primaryKey":   azureKeyMaterial32(id, "primary"),
		"secondaryKey": azureKeyMaterial32(id, "secondary"),
	}
}

// handleRedisCacheListKeys returns the primary + secondary access keys.
func handleRedisCacheListKeys(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	id := redisCacheID(sub, rg, name)
	if _, ok := redisCaches.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Redis cache %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, redisAccessKeysBody(id))
}

func handleRedisCacheCreate(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	var req RedisCache
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Cache/Redis/%s", sub, rg, name)
	cache := RedisCache{
		ID:       id,
		Name:     name,
		Type:     "Microsoft.Cache/Redis",
		Location: req.Location,
		Tags:     req.Tags,
		Properties: map[string]any{
			"provisioningState": "Creating",
			"redisVersion":      "6.0",
			"sslPort":           6380,
			"port":              6379,
			"hostName":          azureEndpointHostname(r, name, "redis", "cache"),
		},
	}
	// Merge operator-supplied properties (e.g. sku, redisConfiguration).
	if req.Properties != nil {
		for k, v := range req.Properties {
			cache.Properties[k] = v
		}
		cache.Properties["provisioningState"] = "Creating"
	}
	redisCaches.Put(id, cache)
	opID := issueAzureAsyncOperation(func() {
		redisCaches.Update(id, func(stored *RedisCache) {
			if stored.Properties == nil {
				stored.Properties = map[string]any{}
			}
			stored.Properties["provisioningState"] = "Succeeded"
		})
	})
	opURL := azureAsyncOperationHeader(r, sub, "Microsoft.Cache", cache.Location, "asyncOperations", opID, r.URL.Query().Get("api-version"))
	writeAzureAsyncCreateHeaders(w, opURL, azureCurrentRequestURL(r))
	sim.WriteJSON(w, http.StatusCreated, cache)
}

func handleRedisCachePatch(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	id := redisCacheID(sub, rg, name)
	if _, ok := redisCaches.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Cache/Redis/%s' under resource group '%s' was not found.", name, rg)
		return
	}
	var req RedisCache
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	redisCaches.Update(id, func(cache *RedisCache) {
		if req.Location != "" {
			cache.Location = req.Location
		}
		if req.Tags != nil {
			cache.Tags = req.Tags
		}
		if req.Properties != nil {
			if cache.Properties == nil {
				cache.Properties = map[string]any{}
			}
			for k, v := range req.Properties {
				cache.Properties[k] = v
			}
			cache.Properties["provisioningState"] = "Succeeded"
		}
	})
	cache, _ := redisCaches.Get(id)
	sim.WriteJSON(w, http.StatusOK, cache)
}

func handleRedisCacheGet(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	id := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Cache/Redis/%s", sub, rg, name)
	cache, ok := redisCaches.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Cache/Redis/%s' under resource group '%s' was not found.", name, rg)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cache)
}

func handleRedisCacheDelete(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	id := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Cache/Redis/%s", sub, rg, name)
	if !redisCaches.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Cache/Redis/%s' under resource group '%s' was not found.", name, rg)
		return
	}
	azureDropKeyGens(id, "primary", "secondary")
	w.WriteHeader(http.StatusNoContent)
}

func handleRedisCacheListByRG(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Cache/Redis/", sub, rg)
	var out []RedisCache
	for _, c := range redisCaches.List() {
		if strings.HasPrefix(c.ID, prefix) {
			out = append(out, c)
		}
	}
	if out == nil {
		out = []RedisCache{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleRedisCacheListBySubscription(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	prefix := fmt.Sprintf("/subscriptions/%s/", sub)
	var out []RedisCache
	for _, c := range redisCaches.List() {
		if strings.HasPrefix(c.ID, prefix) {
			out = append(out, c)
		}
	}
	if out == nil {
		out = []RedisCache{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

// redisCacheExists reports whether the named cache is provisioned, writing a
// ResourceNotFound error when it is not.
func redisCacheExists(w http.ResponseWriter, sub, rg, name string) bool {
	if _, ok := redisCaches.Get(redisCacheID(sub, rg, name)); ok {
		return true
	}
	sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource 'Microsoft.Cache/Redis/%s' under resource group '%s' was not found.", name, rg)
	return false
}

// handleRedisCacheRegenerateKey regenerates the primary or secondary access
// key and returns the full RedisAccessKeys pair. The rotation is recorded per
// key slot, so a subsequent listKeys observes the new value, a second
// regenerate of the same slot yields yet another value, and the untouched
// slot keeps its listKeys value.
func handleRedisCacheRegenerateKey(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	if !redisCacheExists(w, sub, rg, name) {
		return
	}
	var req struct {
		KeyType string `json:"keyType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := redisCacheID(sub, rg, name)
	switch req.KeyType {
	case "Primary":
		azureBumpKeyGen(id, "primary", "")
	case "Secondary":
		azureBumpKeyGen(id, "secondary", "")
	default:
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest,
			"keyType must be 'Primary' or 'Secondary', got %q", req.KeyType)
		return
	}
	sim.WriteJSON(w, http.StatusOK, redisAccessKeysBody(id))
}

// handleRedisForceReboot reboots Redis node(s). Real Azure returns a status
// message synchronously.
func handleRedisForceReboot(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	if !redisCacheExists(w, sub, rg, name) {
		return
	}
	var req struct {
		RebootType string  `json:"rebootType"`
		ShardID    *int32  `json:"shardId,omitempty"`
		Ports      []int32 `json:"ports,omitempty"`
	}
	_ = sim.ReadJSON(r, &req)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"message": "Reboot operation has been initiated on the requested Redis node(s).",
	})
}

// handleRedisImportData imports RDB files into the cache (long-running). The
// simulator completes the import synchronously (no headers → the SDK poller
// resolves immediately).
func handleRedisImportData(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	if !redisCacheExists(w, sub, rg, name) {
		return
	}
	var req struct {
		Files  []string `json:"files"`
		Format string   `json:"format,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if len(req.Files) == 0 {
		sim.AzureError(w, "BadRequest", "at least one file is required to import", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleRedisExportData exports the cache contents to a storage container
// (long-running, completed synchronously).
func handleRedisExportData(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	if !redisCacheExists(w, sub, rg, name) {
		return
	}
	var req struct {
		Container string `json:"container"`
		Prefix    string `json:"prefix"`
		Format    string `json:"format,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if req.Container == "" || req.Prefix == "" {
		sim.AzureError(w, "BadRequest", "container and prefix are required to export", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleRedisFlushCache deletes all keys in the cache (long-running, completed
// synchronously).
func handleRedisFlushCache(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	if !redisCacheExists(w, sub, rg, name) {
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleRedisListUpgradeNotifications returns pending Redis-version upgrade
// notifications. A healthy cache has none.
func handleRedisListUpgradeNotifications(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	if !redisCacheExists(w, sub, rg, name) {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
}

func handleRedisOperationsList(w http.ResponseWriter, _ *http.Request) {
	op := func(name, resource, operation, desc string) map[string]any {
		return map[string]any{
			"name": name,
			"display": map[string]any{
				"provider":    "Microsoft Cache",
				"resource":    resource,
				"operation":   operation,
				"description": desc,
			},
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []map[string]any{
			op("Microsoft.Cache/redis/read", "Redis Cache", "Get Redis Cache", "View the settings and configuration of a Redis cache."),
			op("Microsoft.Cache/redis/write", "Redis Cache", "Set Redis Cache", "Modify a Redis cache's settings and configuration."),
			op("Microsoft.Cache/redis/delete", "Redis Cache", "Delete Redis Cache", "Remove a Redis cache."),
			op("Microsoft.Cache/redis/listKeys/action", "Redis Cache", "List Redis Cache Keys", "View the value of Redis cache access keys."),
		},
	})
}

// handleRedisCheckNameAvailability reports whether a cache name is free. Unlike
// Key Vault, Redis_CheckNameAvailability returns no body: 200 (empty) when the
// name is available, and an error envelope when it is already taken (a cache
// with that name exists in any resource group of the subscription).
func handleRedisCheckNameAvailability(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if req.Name == "" {
		sim.AzureError(w, "BadRequest", "name is required", http.StatusBadRequest)
		return
	}
	prefix := fmt.Sprintf("/subscriptions/%s/", sub)
	for _, c := range redisCaches.List() {
		if strings.HasPrefix(c.ID, prefix) && strings.EqualFold(c.Name, req.Name) {
			sim.AzureErrorf(w, "NameNotAvailable", http.StatusConflict,
				"The name %q is already in use.", req.Name)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func handleRedisAsyncOperationStatus(w http.ResponseWriter, r *http.Request) {
	op, ok := azureAsyncOps.Get(sim.PathParam(r, "operationId"))
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Operation %q not found.", sim.PathParam(r, "operationId"))
		return
	}
	// Real Azure returns Retry-After on async-operation polls; advertising a
	// zero delay lets the SDK poller re-poll immediately instead of falling
	// back to its 30s default frequency.
	if op.Status == "InProgress" {
		w.Header().Set("Retry-After", "1")
	}
	sim.WriteJSON(w, http.StatusOK, op)
}

// redisChildPut is the shared create-or-update body for Redis child resources
// that carry a properties bag. It validates the parent cache exists and echoes
// the operator properties. Resources whose schema includes a provisioningState
// (access policies/assignments, linked servers, private endpoint connections)
// pass stampProvisioningState=true; the patch schedule's ScheduleEntries shape
// has no provisioningState, so it passes false.
func redisChildPut(w http.ResponseWriter, r *http.Request, store sim.Store[RedisSubResource], childSeg, childName, typ string, stampProvisioningState bool) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	cache := sim.PathParam(r, "name")
	if !redisCacheExists(w, sub, rg, cache) {
		return
	}
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	props := map[string]any{}
	for k, v := range req.Properties {
		props[k] = v
	}
	if stampProvisioningState {
		props["provisioningState"] = "Succeeded"
	}
	id := redisCacheID(sub, rg, cache) + "/" + childSeg + "/" + childName
	res := RedisSubResource{ID: id, Name: childName, Type: typ, Properties: props}
	store.Put(id, res)
	sim.WriteJSON(w, http.StatusOK, res)
}

func redisChildGet(w http.ResponseWriter, r *http.Request, store sim.Store[RedisSubResource], childSeg, childName string) {
	id := redisCacheID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/" + childSeg + "/" + childName
	res, ok := store.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "%s %q not found.", childSeg, childName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, res)
}

func redisChildDelete(w http.ResponseWriter, r *http.Request, store sim.Store[RedisSubResource], childSeg, childName string) {
	id := redisCacheID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/" + childSeg + "/" + childName
	if !store.Delete(id) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func redisChildList(w http.ResponseWriter, r *http.Request, store sim.Store[RedisSubResource], childSeg string) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	cache := sim.PathParam(r, "name")
	if !redisCacheExists(w, sub, rg, cache) {
		return
	}
	prefix := redisCacheID(sub, rg, cache) + "/" + childSeg + "/"
	out := store.Filter(func(res RedisSubResource) bool {
		return strings.HasPrefix(res.ID, prefix)
	})
	if out == nil {
		out = []RedisSubResource{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleRedisAccessPolicyPut(w http.ResponseWriter, r *http.Request) {
	redisChildPut(w, r, redisAccessPolicies, "accessPolicies", sim.PathParam(r, "policy"), "Microsoft.Cache/Redis/accessPolicies", true)
}
func handleRedisAccessPolicyGet(w http.ResponseWriter, r *http.Request) {
	redisChildGet(w, r, redisAccessPolicies, "accessPolicies", sim.PathParam(r, "policy"))
}
func handleRedisAccessPolicyDelete(w http.ResponseWriter, r *http.Request) {
	redisChildDelete(w, r, redisAccessPolicies, "accessPolicies", sim.PathParam(r, "policy"))
}
func handleRedisAccessPolicyList(w http.ResponseWriter, r *http.Request) {
	redisChildList(w, r, redisAccessPolicies, "accessPolicies")
}

func handleRedisAccessPolicyAssignmentPut(w http.ResponseWriter, r *http.Request) {
	redisChildPut(w, r, redisAccessPolicyAssignments, "accessPolicyAssignments", sim.PathParam(r, "assignment"), "Microsoft.Cache/Redis/accessPolicyAssignments", true)
}
func handleRedisAccessPolicyAssignmentGet(w http.ResponseWriter, r *http.Request) {
	redisChildGet(w, r, redisAccessPolicyAssignments, "accessPolicyAssignments", sim.PathParam(r, "assignment"))
}
func handleRedisAccessPolicyAssignmentDelete(w http.ResponseWriter, r *http.Request) {
	redisChildDelete(w, r, redisAccessPolicyAssignments, "accessPolicyAssignments", sim.PathParam(r, "assignment"))
}
func handleRedisAccessPolicyAssignmentList(w http.ResponseWriter, r *http.Request) {
	redisChildList(w, r, redisAccessPolicyAssignments, "accessPolicyAssignments")
}

func handleRedisLinkedServerPut(w http.ResponseWriter, r *http.Request) {
	redisChildPut(w, r, redisLinkedServers, "linkedServers", sim.PathParam(r, "linkedServer"), "Microsoft.Cache/Redis/linkedServers", true)
}
func handleRedisLinkedServerGet(w http.ResponseWriter, r *http.Request) {
	redisChildGet(w, r, redisLinkedServers, "linkedServers", sim.PathParam(r, "linkedServer"))
}
func handleRedisLinkedServerDelete(w http.ResponseWriter, r *http.Request) {
	redisChildDelete(w, r, redisLinkedServers, "linkedServers", sim.PathParam(r, "linkedServer"))
}
func handleRedisLinkedServerList(w http.ResponseWriter, r *http.Request) {
	redisChildList(w, r, redisLinkedServers, "linkedServers")
}

func handleRedisPatchSchedulePut(w http.ResponseWriter, r *http.Request) {
	redisChildPut(w, r, redisPatchSchedules, "patchSchedules", sim.PathParam(r, "schedule"), "Microsoft.Cache/Redis/patchSchedules", false)
}
func handleRedisPatchScheduleGet(w http.ResponseWriter, r *http.Request) {
	redisChildGet(w, r, redisPatchSchedules, "patchSchedules", sim.PathParam(r, "schedule"))
}
func handleRedisPatchScheduleDelete(w http.ResponseWriter, r *http.Request) {
	redisChildDelete(w, r, redisPatchSchedules, "patchSchedules", sim.PathParam(r, "schedule"))
}
func handleRedisPatchScheduleList(w http.ResponseWriter, r *http.Request) {
	redisChildList(w, r, redisPatchSchedules, "patchSchedules")
}

func handleRedisPECPut(w http.ResponseWriter, r *http.Request) {
	redisChildPut(w, r, redisPrivateConns, "privateEndpointConnections", sim.PathParam(r, "pec"), "Microsoft.Cache/Redis/privateEndpointConnections", true)
}
func handleRedisPECGet(w http.ResponseWriter, r *http.Request) {
	redisChildGet(w, r, redisPrivateConns, "privateEndpointConnections", sim.PathParam(r, "pec"))
}
func handleRedisPECDelete(w http.ResponseWriter, r *http.Request) {
	redisChildDelete(w, r, redisPrivateConns, "privateEndpointConnections", sim.PathParam(r, "pec"))
}
func handleRedisPECList(w http.ResponseWriter, r *http.Request) {
	redisChildList(w, r, redisPrivateConns, "privateEndpointConnections")
}

// handleRedisPrivateLinkResources returns the cache's single "redisCache"
// private-link group, matching real Azure.
func handleRedisPrivateLinkResources(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	cache := sim.PathParam(r, "name")
	if !redisCacheExists(w, sub, rg, cache) {
		return
	}
	id := redisCacheID(sub, rg, cache)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []map[string]any{{
			"id":   id + "/privateLinkResources/redisCache",
			"name": "redisCache",
			"type": "Microsoft.Cache/Redis/privateLinkResources",
			"properties": map[string]any{
				"groupId":           "redisCache",
				"requiredMembers":   []string{"redisCache"},
				"requiredZoneNames": []string{"privatelink.redis.cache.windows.net"},
			},
		}},
	})
}
