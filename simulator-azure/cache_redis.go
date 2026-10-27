package main

import (
	"log"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Microsoft.Cache/Redis: the Azure Resource Manager lifecycle of a cache and
// its sub-resources, and a real Redis engine as its data plane
// (cache_redis_engine.go).

// sku lives under properties (RedisProperties allOf RedisCreateProperties),
// never at the top level.
type RedisCache struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
}

type RedisFirewallRule struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
}

// RedisSubResource is the shape of a Redis child resource with a free-form
// properties bag: access policies and their assignments, linked servers, patch
// schedules, private endpoint connections.
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
	redisEngineRecords = sim.MakeStore[redisEngineRecord](srv.DB(), "redis_engines")

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

	srv.HandleFunc("GET /providers/Microsoft.Cache/operations", handleRedisOperationsList)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.Cache/checknameavailability", handleRedisCheckNameAvailability)

	// Microsoft Entra ID data-access RBAC.
	srv.HandleFunc("PUT "+armBase+"/{name}/accessPolicies/{policy}", handleRedisAccessPolicyPut)
	srv.HandleFunc("GET "+armBase+"/{name}/accessPolicies/{policy}", handleRedisAccessPolicyGet)
	srv.HandleFunc("DELETE "+armBase+"/{name}/accessPolicies/{policy}", handleRedisAccessPolicyDelete)
	srv.HandleFunc("GET "+armBase+"/{name}/accessPolicies", handleRedisAccessPolicyList)
	srv.HandleFunc("PUT "+armBase+"/{name}/accessPolicyAssignments/{assignment}", handleRedisAccessPolicyAssignmentPut)
	srv.HandleFunc("GET "+armBase+"/{name}/accessPolicyAssignments/{assignment}", handleRedisAccessPolicyAssignmentGet)
	srv.HandleFunc("DELETE "+armBase+"/{name}/accessPolicyAssignments/{assignment}", handleRedisAccessPolicyAssignmentDelete)
	srv.HandleFunc("GET "+armBase+"/{name}/accessPolicyAssignments", handleRedisAccessPolicyAssignmentList)

	srv.HandleFunc("PUT "+armBase+"/{name}/linkedServers/{linkedServer}", handleRedisLinkedServerPut)
	srv.HandleFunc("GET "+armBase+"/{name}/linkedServers/{linkedServer}", handleRedisLinkedServerGet)
	srv.HandleFunc("DELETE "+armBase+"/{name}/linkedServers/{linkedServer}", handleRedisLinkedServerDelete)
	srv.HandleFunc("GET "+armBase+"/{name}/linkedServers", handleRedisLinkedServerList)

	srv.HandleFunc("PUT "+armBase+"/{name}/patchSchedules/{schedule}", handleRedisPatchSchedulePut)
	srv.HandleFunc("GET "+armBase+"/{name}/patchSchedules/{schedule}", handleRedisPatchScheduleGet)
	srv.HandleFunc("DELETE "+armBase+"/{name}/patchSchedules/{schedule}", handleRedisPatchScheduleDelete)
	srv.HandleFunc("GET "+armBase+"/{name}/patchSchedules", handleRedisPatchScheduleList)

	srv.HandleFunc("PUT "+armBase+"/{name}/privateEndpointConnections/{pec}", handleRedisPECPut)
	srv.HandleFunc("GET "+armBase+"/{name}/privateEndpointConnections/{pec}", handleRedisPECGet)
	srv.HandleFunc("DELETE "+armBase+"/{name}/privateEndpointConnections/{pec}", handleRedisPECDelete)
	srv.HandleFunc("GET "+armBase+"/{name}/privateEndpointConnections", handleRedisPECList)
	srv.HandleFunc("GET "+armBase+"/{name}/privateLinkResources", handleRedisPrivateLinkResources)

	// A persistent restart rebinds every cache's ports and re-adopts the engine
	// workloads the earlier process left. The API-only tier runs no engines.
	if redisRunsEngines() {
		if err := redisRecoverEngines(); err != nil {
			log.Fatalf("recover Azure Cache for Redis data planes: %v", err)
		}
	}
}

func redisCacheID(sub, rg, name string) string {
	return armResourceID(sub, rg, "Microsoft.Cache", "Redis", name)
}

// The rule's resource ID keys its store, like every Redis child store, so a
// cross-resource-group move re-keys them alike.
func redisFirewallRuleID(sub, rg, cache, rule string) string {
	return armChildID(redisCacheID(sub, rg, cache), "firewallRules", rule)
}

func handleRedisCacheCreateFirewallRule(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	cache := sim.PathParam(r, "name")
	rule := sim.PathParam(r, "rule")
	if _, ok := redisCaches.Get(redisCacheID(sub, rg, cache)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Redis cache %q not found", cache)
		return
	}
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	fr := RedisFirewallRule{
		ID:         redisFirewallRuleID(sub, rg, cache, rule),
		Name:       rule,
		Type:       "Microsoft.Cache/Redis/firewallRules",
		Properties: req.Properties,
	}
	redisFirewallRules.Put(fr.ID, fr)
	sim.WriteJSON(w, http.StatusOK, fr)
}

func handleRedisCacheGetFirewallRule(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	cache := sim.PathParam(r, "name")
	rule := sim.PathParam(r, "rule")
	fr, ok := redisFirewallRules.Get(redisFirewallRuleID(sub, rg, cache, rule))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
	if !redisFirewallRules.Delete(redisFirewallRuleID(sub, rg, cache, rule)) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

// redisAccessKeysBody derives each 44-character base64 key from the resource
// ID, the slot and its rotation generation: stable across reads, distinct per
// slot and per cache, new after each regenerate of that slot.
func redisAccessKeysBody(id string) map[string]any {
	return map[string]any{
		"primaryKey":   azureKeyMaterial32(id, "primary"),
		"secondaryKey": azureKeyMaterial32(id, "secondary"),
	}
}

func handleRedisCacheListKeys(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	id := redisCacheID(sub, rg, name)
	if _, ok := redisCaches.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := armResourceID(sub, rg, "Microsoft.Cache", "Redis", name)
	if _, exists := redisCaches.Get(id); exists {
		// Azure Resource Manager's PUT is create-or-update.
		redisCacheUpdate(w, r, id, req)
		return
	}
	requested, _ := req.Properties["redisVersion"].(string)
	version, image, known := redisEngineVersion(requested)
	if !known {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest,
			"redisVersion %q is not a supported Redis version; supported versions are 4.0 and 6.0 (latest).", requested)
		return
	}
	if shards, _ := req.Properties["shardCount"].(float64); shards > 0 {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest,
			"shardCount %v: this simulator serves no clustered cache; the specification states no port a shard's instances answer on.", shards)
		return
	}
	cache := RedisCache{
		ID:       id,
		Name:     name,
		Type:     "Microsoft.Cache/Redis",
		Location: req.Location,
		Tags:     req.Tags,
		Properties: map[string]any{
			"enableNonSslPort": false,
			"sslPort":          redisSSLPort,
			"port":             redisNonSSLPort,
			"hostName":         azureEndpointHostname(r, name, "redis", "cache"),
		},
	}
	for k, v := range req.Properties {
		cache.Properties[k] = v
	}
	cache.Properties["redisVersion"] = version
	cache.Properties["provisioningState"] = "Creating"
	plane, err := redisInstallEngine(cache, image)
	if err != nil {
		AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "start the cache's engine: %v", err)
		return
	}
	redisCaches.Put(id, cache)
	opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
		state, failure := "Succeeded", (*AsyncOperationError)(nil)
		if plane != nil {
			if err := redisStartEngine(plane); err != nil {
				redisLogEngineFailure(id, err)
				state, failure = "Failed", &AsyncOperationError{Code: "InternalServerError", Message: "The cache's engine did not start: " + err.Error()}
			} else {
				redisSaveEngine(id, plane)
			}
		}
		redisCaches.Update(id, func(stored *RedisCache) { stored.Properties["provisioningState"] = state })
		return failure
	})
	opURL := azureAsyncOperationHeader(r, sub, "Microsoft.Cache", cache.Location, "asyncOperations", opID, r.URL.Query().Get("api-version"))
	writeAzureAsyncCreateHeaders(w, opID, opURL, azureCurrentRequestURL(r))
	sim.WriteJSON(w, http.StatusCreated, cache)
}

func handleRedisCachePatch(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	id := redisCacheID(sub, rg, name)
	if _, ok := redisCaches.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Cache/Redis/%s' under resource group '%s' was not found.", name, rg)
		return
	}
	var req RedisCache
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	redisCacheUpdate(w, r, id, req)
}

// redisCacheUpdate applies live: the non-SSL port opens or closes, the replica
// count follows the SKU, and redisConfiguration's directives apply.
func redisCacheUpdate(w http.ResponseWriter, r *http.Request, id string, req RedisCache) {
	current, _ := redisCaches.Get(id)
	if state, _ := current.Properties["provisioningState"].(string); state != "Succeeded" && state != "" {
		AzureErrorf(w, "Conflict", http.StatusConflict,
			"The cache %s is %s; it takes an update once it has Succeeded.", current.Name, state)
		return
	}
	if shards, _ := req.Properties["shardCount"].(float64); shards > 0 {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest,
			"shardCount %v: this simulator serves no clustered cache; the specification states no port a shard's instances answer on.", shards)
		return
	}
	version, _ := current.Properties["redisVersion"].(string)
	upgrade := ""
	if requested, set := req.Properties["redisVersion"].(string); set {
		resolved, image, known := redisEngineVersion(requested)
		switch {
		case !known:
			AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"redisVersion %q is not a supported Redis version; supported versions are 4.0 and 6.0 (latest).", requested)
			return
		case resolved < version:
			AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"redisVersion %q: a cache's Redis version cannot be downgraded from %s.", requested, version)
			return
		case resolved != version:
			version, upgrade = resolved, image
		}
	}
	next := map[string]any{}
	for k, v := range current.Properties {
		next[k] = v
	}
	for k, v := range req.Properties {
		next[k] = v
	}
	next["redisVersion"] = version
	if plane, ok := redisLoadEngine(id); ok {
		var err error
		if upgrade != "" {
			if err = plane.Restart(nil, upgrade); err == nil {
				redisEngineRecords.Update(id, func(record *redisEngineRecord) { record.Image = upgrade })
			}
		}
		if err == nil {
			err = redisSetNonSSLPort(id, plane, redisNonSSLEnabled(next))
		}
		if err == nil {
			err = plane.Configure(redisEngineDirectives(next))
		}
		if err == nil && redisReplicaCount(next) != len(plane.Replicas()) {
			err = plane.ResizeInstance(redisReplicaCount(next))
			redisSaveEngine(id, plane)
		}
		if err != nil {
			AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "update the cache's engine: %v", err)
			return
		}
	}
	redisCaches.Update(id, func(cache *RedisCache) {
		if req.Location != "" {
			cache.Location = req.Location
		}
		if req.Tags != nil {
			cache.Tags = req.Tags
		}
		cache.Properties = next
		cache.Properties["provisioningState"] = "Succeeded"
	})
	cache, _ := redisCaches.Get(id)
	sim.WriteJSON(w, http.StatusOK, cache)
}

func handleRedisCacheGet(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	id := armResourceID(sub, rg, "Microsoft.Cache", "Redis", name)
	cache, ok := redisCaches.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Cache/Redis/%s' under resource group '%s' was not found.", name, rg)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cache)
}

func handleRedisCacheDelete(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	id := armResourceID(sub, rg, "Microsoft.Cache", "Redis", name)
	if _, ok := redisCaches.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Cache/Redis/%s' under resource group '%s' was not found.", name, rg)
		return
	}
	redisRemoveEngine(id)
	redisCaches.Delete(id)
	azureDropKeyGens(id, "primary", "secondary")
	w.WriteHeader(http.StatusNoContent)
}

func handleRedisCacheListByRG(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	prefix := armResourceID(sub, rg, "Microsoft.Cache", "Redis") + "/"
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
	prefix := armSubscriptionID(sub) + "/"
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

// redisCacheExists writes ResourceNotFound when the cache is absent.
func redisCacheExists(w http.ResponseWriter, sub, rg, name string) bool {
	if _, ok := redisCaches.Get(redisCacheID(sub, rg, name)); ok {
		return true
	}
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource 'Microsoft.Cache/Redis/%s' under resource group '%s' was not found.", name, rg)
	return false
}

// handleRedisCacheRegenerateKey records the rotation per slot, so listKeys
// observes the new value and the other slot keeps its own.
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
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := redisCacheID(sub, rg, name)
	switch req.KeyType {
	case "Primary":
		azureBumpKeyGen(id, "primary", "")
	case "Secondary":
		azureBumpKeyGen(id, "secondary", "")
	default:
		AzureErrorf(w, "BadRequest", http.StatusBadRequest,
			"keyType must be 'Primary' or 'Secondary', got %q", req.KeyType)
		return
	}
	if plane, ok := redisLoadEngine(id); ok && plane.Running() {
		err := plane.SetPassword(azureKeyMaterial32(id, "primary"))
		if err == nil {
			err = plane.ApplyUsers(plane.Nodes())
		}
		if err != nil {
			AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "rotate the cache's access key: %v", err)
			return
		}
	}
	sim.WriteJSON(w, http.StatusOK, redisAccessKeysBody(id))
}

// Azure answers the reboot with a status message synchronously.
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
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if plane, ok := redisLoadEngine(redisCacheID(sub, rg, name)); ok {
		var nodes []int
		switch req.RebootType {
		case "PrimaryNode":
			nodes = []int{plane.Primary()}
		case "SecondaryNode":
			nodes = plane.Replicas()
		case "AllNodes":
			nodes = plane.Nodes()
		default:
			AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"rebootType must be 'PrimaryNode', 'SecondaryNode' or 'AllNodes', got %q", req.RebootType)
			return
		}
		if len(nodes) > 0 {
			if err := plane.Reboot(nodes); err != nil {
				AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "reboot the cache's nodes: %v", err)
				return
			}
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"message": "Reboot operation has been initiated on the requested Redis node(s).",
	})
}

// Answers 200 without importing anything.
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
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if len(req.Files) == 0 {
		AzureError(w, "BadRequest", "at least one file is required to import", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Answers 200 without exporting anything.
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
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if req.Container == "" || req.Prefix == "" {
		AzureError(w, "BadRequest", "container and prefix are required to export", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleRedisFlushCache deletes every key within the request, so the operation
// completes as it answers.
func handleRedisFlushCache(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	if !redisCacheExists(w, sub, rg, name) {
		return
	}
	if plane, ok := redisLoadEngine(redisCacheID(sub, rg, name)); ok {
		if err := plane.Flush(); err != nil {
			AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "flush the cache: %v", err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// A healthy cache has no upgrade notifications.
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

// Redis_CheckNameAvailability returns no body: 200 when the name is free, an
// error envelope when a cache of that name exists in any resource group of the
// subscription.
func handleRedisCheckNameAvailability(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if req.Name == "" {
		AzureError(w, "BadRequest", "name is required", http.StatusBadRequest)
		return
	}
	prefix := armSubscriptionID(sub) + "/"
	for _, c := range redisCaches.List() {
		if strings.HasPrefix(c.ID, prefix) && strings.EqualFold(c.Name, req.Name) {
			AzureErrorf(w, "NameNotAvailable", http.StatusConflict,
				"The name %q is already in use.", req.Name)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// redisChildPut checks the parent cache exists and echoes the properties.
// stampProvisioningState is false only for the patch schedule, whose
// ScheduleEntries shape has no provisioningState.
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
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
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
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "%s %q not found.", childSeg, childName)
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

// A cache exposes a single "redisCache" private-link group.
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
