package main

import (
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Microsoft.DocumentDB surfaces beyond the SQL family in cosmos.go: the
// MongoDB, Cassandra and Gremlin families, SQL script sub-resources and
// client-encryption keys, throughput migration, account update, failover, key
// regeneration and name existence, and the provider operations and locations
// lists. Every child shares the {resource, options} GetResults envelope, so
// one generic store serves them all.

const cosmosTypeBase = "Microsoft.DocumentDB/databaseAccounts"

type CosmosResource struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
}

// cosmosKeyGenRow: Key holds material pinned onto the role by a
// cross-resource-group move; the next RegenerateKey clears it.
type cosmosKeyGenRow struct {
	N   int    `json:"n"`
	Key string `json:"key,omitempty"`
}

var (
	cosmosResources sim.Store[CosmosResource]
	cosmosKeyGens   sim.Store[cosmosKeyGenRow]
)

func registerCosmosAPIs(srv *sim.Server) {
	cosmosResources = sim.MakeStore[CosmosResource](srv.DB(), "cosmos_api_resources")
	cosmosKeyGens = sim.MakeStore[cosmosKeyGenRow](srv.DB(), "cosmos_key_generations")

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts"

	srv.HandleFunc("PATCH "+armBase+"/{account}", handleCosmosUpdateAccount)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.DocumentDB/databaseAccounts", handleCosmosListAccountsBySubscription)
	srv.HandleFunc("POST "+armBase+"/{account}/failoverPriorityChange", handleCosmosFailoverPriorityChange)
	srv.HandleFunc("POST "+armBase+"/{account}/regenerateKey", handleCosmosRegenerateKey)
	srv.HandleFunc("GET "+armBase+"/{account}/readonlykeys", handleCosmosListReadOnlyKeys)
	srv.HandleFunc("HEAD /providers/Microsoft.DocumentDB/databaseAccountNames/{account}", handleCosmosCheckNameExists)

	srv.HandleFunc("GET /providers/Microsoft.DocumentDB/operations", handleCosmosOperations)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.DocumentDB/locations", handleCosmosLocations)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.DocumentDB/locations/{location}", handleCosmosLocationGet)

	cosmosRegisterFamily(srv, armBase+"/{account}/mongodbDatabases", cosmosTypeBase+"/mongodbDatabases", true)
	cosmosRegisterFamily(srv, armBase+"/{account}/mongodbDatabases/{database}/collections", cosmosTypeBase+"/mongodbDatabases/collections", true)
	cosmosRegisterFamily(srv, armBase+"/{account}/cassandraKeyspaces", cosmosTypeBase+"/cassandraKeyspaces", true)
	cosmosRegisterFamily(srv, armBase+"/{account}/cassandraKeyspaces/{keyspace}/tables", cosmosTypeBase+"/cassandraKeyspaces/tables", true)
	cosmosRegisterFamily(srv, armBase+"/{account}/gremlinDatabases", cosmosTypeBase+"/gremlinDatabases", true)
	cosmosRegisterFamily(srv, armBase+"/{account}/gremlinDatabases/{database}/graphs", cosmosTypeBase+"/gremlinDatabases/graphs", true)

	// SQL script sub-resources carry no dedicated throughput.
	cosmosRegisterFamily(srv, armBase+"/{account}/sqlDatabases/{database}/containers/{container}/storedProcedures", cosmosTypeBase+"/sqlDatabases/containers/storedProcedures", false)
	cosmosRegisterFamily(srv, armBase+"/{account}/sqlDatabases/{database}/containers/{container}/userDefinedFunctions", cosmosTypeBase+"/sqlDatabases/containers/userDefinedFunctions", false)
	cosmosRegisterFamily(srv, armBase+"/{account}/sqlDatabases/{database}/containers/{container}/triggers", cosmosTypeBase+"/sqlDatabases/containers/triggers", false)

	// The API exposes no delete for client-encryption keys.
	cekType := cosmosTypeBase + "/sqlDatabases/clientEncryptionKeys"
	srv.HandleFunc("GET "+armBase+"/{account}/sqlDatabases/{database}/clientEncryptionKeys", cosmosListHandler(cekType))
	srv.HandleFunc("GET "+armBase+"/{account}/sqlDatabases/{database}/clientEncryptionKeys/{name}", handleCosmosGetResource)
	srv.HandleFunc("PUT "+armBase+"/{account}/sqlDatabases/{database}/clientEncryptionKeys/{name}", cosmosPutHandler(cekType))

	cosmosRegisterMigrate(srv, armBase+"/{account}/sqlDatabases/{database}/throughputSettings/default")
	cosmosRegisterMigrate(srv, armBase+"/{account}/sqlDatabases/{database}/containers/{container}/throughputSettings/default")
	cosmosRegisterMigrate(srv, armBase+"/{account}/tables/{table}/throughputSettings/default")
}

func cosmosRegisterFamily(srv *sim.Server, base, resType string, throughput bool) {
	srv.HandleFunc("PUT "+base+"/{name}", cosmosPutHandler(resType))
	srv.HandleFunc("GET "+base+"/{name}", handleCosmosGetResource)
	srv.HandleFunc("DELETE "+base+"/{name}", handleCosmosDeleteResource)
	srv.HandleFunc("GET "+base, cosmosListHandler(resType))
	if throughput {
		srv.HandleFunc("GET "+base+"/{name}/throughputSettings/default", handleCosmosGetThroughput)
		srv.HandleFunc("PUT "+base+"/{name}/throughputSettings/default", handleCosmosPutThroughput)
		cosmosRegisterMigrate(srv, base+"/{name}/throughputSettings/default")
	}
}

func cosmosRegisterMigrate(srv *sim.Server, throughputBase string) {
	srv.HandleFunc("POST "+throughputBase+"/migrateToAutoscale", handleCosmosMigrateToAutoscale)
	srv.HandleFunc("POST "+throughputBase+"/migrateToManualThroughput", handleCosmosMigrateToManual)
}

func cosmosParentExists(id string) bool {
	if _, ok := cosmosAccounts.Get(id); ok {
		return true
	}
	if _, ok := cosmosResources.Get(id); ok {
		return true
	}
	if _, ok := cosmosDatabases.Get(id); ok {
		return true
	}
	if _, ok := cosmosContainers.Get(id); ok {
		return true
	}
	if _, ok := cosmosTables.Get(id); ok {
		return true
	}
	return false
}

func cosmosPutHandler(resType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The parent ID is the path with the family segment and name stripped.
		parent := path.Dir(path.Dir(r.URL.Path))
		if !cosmosParentExists(parent) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "parent resource not found: %s", parent)
			return
		}
		var req CosmosResource
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid %s body: %v", cosmosFriendly(resType), err)
			return
		}
		id := r.URL.Path
		name := path.Base(id)
		props := ensureResourceProperty(req.Properties, name)
		cosmosStoreThroughputFromProps(props, id, resType)
		res := CosmosResource{
			ID:         id,
			Name:       name,
			Type:       resType,
			Location:   req.Location,
			Tags:       req.Tags,
			Properties: props,
		}
		cosmosResources.Put(id, res)
		sim.WriteJSON(w, http.StatusOK, res)
	}
}

func handleCosmosGetResource(w http.ResponseWriter, r *http.Request) {
	res, ok := cosmosResources.Get(r.URL.Path)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos resource not found: %s", r.URL.Path)
		return
	}
	sim.WriteJSON(w, http.StatusOK, res)
}

func handleCosmosDeleteResource(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path
	if !cosmosResources.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos resource not found: %s", id)
		return
	}
	for _, c := range cosmosResources.List() {
		if strings.HasPrefix(c.ID, id+"/") {
			cosmosResources.Delete(c.ID)
		}
	}
	for _, t := range cosmosThroughputs.List() {
		if strings.HasPrefix(t.ID, id+"/") {
			cosmosThroughputs.Delete(t.ID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func cosmosListHandler(resType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prefix := r.URL.Path + "/"
		all := cosmosResources.Filter(func(c CosmosResource) bool {
			return c.Type == resType && strings.HasPrefix(c.ID, prefix)
		})
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		if all == nil {
			all = []CosmosResource{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
	}
}

// A member created without a throughput option shares its database's or
// account's and has no dedicated offer.
func cosmosStoreThroughputFromProps(props map[string]any, resID, resType string) {
	opts, _ := props["options"].(map[string]any)
	if opts == nil {
		return
	}
	resource := map[string]any{}
	if tp := opts["throughput"]; tp != nil {
		resource["throughput"] = tp
	}
	if as := opts["autoscaleSettings"]; as != nil {
		resource["autoscaleSettings"] = as
	}
	if len(resource) == 0 {
		return
	}
	tid := resID + "/throughputSettings/default"
	cosmosStampThroughput(tid, resource)
	cosmosThroughputs.Put(tid, CosmosThroughput{
		ID:         tid,
		Name:       "default",
		Type:       resType + "/throughputSettings",
		Properties: map[string]any{"resource": resource},
	})
}

func cosmosFriendly(resType string) string {
	return path.Base(resType)
}

// Cassandra-table and Gremlin-graph paths go before the bare tables and graphs
// cases, whose segments they contain.
func cosmosThroughputTypeFromPath(p string) string {
	switch {
	case strings.Contains(p, "/cassandraKeyspaces/") && strings.Contains(p, "/tables/"):
		return cosmosTypeBase + "/cassandraKeyspaces/tables/throughputSettings"
	case strings.Contains(p, "/cassandraKeyspaces/"):
		return cosmosTypeBase + "/cassandraKeyspaces/throughputSettings"
	case strings.Contains(p, "/gremlinDatabases/") && strings.Contains(p, "/graphs/"):
		return cosmosTypeBase + "/gremlinDatabases/graphs/throughputSettings"
	case strings.Contains(p, "/gremlinDatabases/"):
		return cosmosTypeBase + "/gremlinDatabases/throughputSettings"
	case strings.Contains(p, "/mongodbDatabases/") && strings.Contains(p, "/collections/"):
		return cosmosTypeBase + "/mongodbDatabases/collections/throughputSettings"
	case strings.Contains(p, "/mongodbDatabases/"):
		return cosmosTypeBase + "/mongodbDatabases/throughputSettings"
	case strings.Contains(p, "/sqlDatabases/") && strings.Contains(p, "/containers/"):
		return cosmosTypeBase + "/sqlDatabases/containers/throughputSettings"
	case strings.Contains(p, "/sqlDatabases/"):
		return cosmosTypeBase + "/sqlDatabases/throughputSettings"
	case strings.Contains(p, "/tables/"):
		return cosmosTypeBase + "/tables/throughputSettings"
	}
	return cosmosTypeBase + "/throughputSettings"
}

func handleCosmosMigrateToAutoscale(w http.ResponseWriter, r *http.Request) {
	cosmosMigrateThroughput(w, r, true)
}

func handleCosmosMigrateToManual(w http.ResponseWriter, r *http.Request) {
	cosmosMigrateThroughput(w, r, false)
}

// cosmosMigrateThroughput converts a throughput resource between manual RU/s and
// autoscale (max RU/s), the real BeginMigrateXToAutoscale /
// BeginMigrateXToManualThroughput behavior.
func cosmosMigrateThroughput(w http.ResponseWriter, r *http.Request, toAutoscale bool) {
	id := strings.TrimSuffix(r.URL.Path, "/migrateToAutoscale")
	id = strings.TrimSuffix(id, "/migrateToManualThroughput")
	t, ok := cosmosThroughputs.Get(id)
	if !ok {
		cosmosThroughputNotFound(w, id)
		return
	}
	if t.Properties == nil {
		t.Properties = map[string]any{}
	}
	resource, _ := t.Properties["resource"].(map[string]any)
	if resource == nil {
		resource = map[string]any{}
	}
	if toAutoscale {
		maxRU := cosmosToFloat(resource["throughput"])
		if maxRU < 1000 {
			maxRU = 4000
		}
		resource["autoscaleSettings"] = map[string]any{"maxThroughput": maxRU}
		delete(resource, "throughput")
	} else {
		manual := float64(400)
		if as, ok := resource["autoscaleSettings"].(map[string]any); ok {
			if m := cosmosToFloat(as["maxThroughput"]); m > 0 {
				manual = m
			}
		}
		resource["throughput"] = manual
		delete(resource, "autoscaleSettings")
	}
	cosmosStampThroughput(id, resource)
	t.Properties["resource"] = resource
	cosmosThroughputs.Put(id, t)
	sim.WriteJSON(w, http.StatusOK, t)
}

func cosmosToFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func handleCosmosUpdateAccount(w http.ResponseWriter, r *http.Request) {
	id := cosmosAccountID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account"))
	a, ok := cosmosAccounts.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB account not found: %s", id)
		return
	}
	var req CosmosAccount
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid Cosmos account update body: %v", err)
		return
	}
	if req.Tags != nil {
		a.Tags = req.Tags
	}
	if req.Location != "" {
		a.Location = req.Location
	}
	if a.Properties == nil {
		a.Properties = map[string]any{}
	}
	for k, v := range req.Properties {
		a.Properties[k] = v
	}
	if _, ok := req.Properties["locations"]; ok {
		cosmosNormalizeLocations(a.Properties, a.Name, a.Location)
	}
	cosmosAccounts.Put(id, a)
	sim.WriteJSON(w, http.StatusOK, a)
}

func handleCosmosListAccountsBySubscription(w http.ResponseWriter, r *http.Request) {
	prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
	all := cosmosAccounts.Filter(func(a CosmosAccount) bool { return strings.HasPrefix(a.ID, prefix) })
	page, next, pageOK := armQueryPage(w, r, all, func(x CosmosAccount) string { return x.ID })
	if !pageOK {
		return
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = armNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// The new priority-0 region becomes the write region.
func handleCosmosFailoverPriorityChange(w http.ResponseWriter, r *http.Request) {
	id := cosmosAccountID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account"))
	a, ok := cosmosAccounts.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB account not found: %s", id)
		return
	}
	var req struct {
		FailoverPolicies []map[string]any `json:"failoverPolicies"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid failover policy body: %v", err)
		return
	}
	if len(req.FailoverPolicies) > 0 {
		priorityByLocation := map[string]int{}
		policies := make([]map[string]any, 0, len(req.FailoverPolicies))
		for _, p := range req.FailoverPolicies {
			name, _ := p["locationName"].(string)
			prio := cosmosToInt(p["failoverPriority"])
			priorityByLocation[name] = prio
			policies = append(policies, map[string]any{
				"id":               a.Name + "-" + strings.ToLower(strings.ReplaceAll(name, " ", "")),
				"locationName":     name,
				"failoverPriority": prio,
			})
		}
		a.Properties["failoverPolicies"] = policies
		cosmosApplyFailoverPriorities(a.Properties, priorityByLocation)
		cosmosAccounts.Put(id, a)
	}
	w.WriteHeader(http.StatusOK)
}

func cosmosApplyFailoverPriorities(props map[string]any, priorityByLocation map[string]int) {
	locations := cosmosLocationMaps(props["locations"])
	writeLocations := make([]map[string]any, 0, 1)
	for _, loc := range locations {
		name, _ := loc["locationName"].(string)
		if prio, ok := priorityByLocation[name]; ok {
			loc["failoverPriority"] = prio
		}
		if cosmosToInt(loc["failoverPriority"]) == 0 {
			writeLocations = append(writeLocations, loc)
		}
	}
	props["locations"] = locations
	props["readLocations"] = locations
	if len(writeLocations) > 0 {
		props["writeLocations"] = writeLocations
	}
}

// A stored locations array is []map[string]any fresh from
// cosmosNormalizeLocations, or []any after a store round-trip.
func cosmosLocationMaps(v any) []map[string]any {
	switch arr := v.(type) {
	case []map[string]any:
		return arr
	case []any:
		out := make([]map[string]any, 0, len(arr))
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

func handleCosmosRegenerateKey(w http.ResponseWriter, r *http.Request) {
	id := cosmosAccountID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account"))
	if _, ok := cosmosAccounts.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB account not found: %s", id)
		return
	}
	var req struct {
		KeyKind string `json:"keyKind"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid regenerate key body: %v", err)
		return
	}
	role := cosmosKeyRole(req.KeyKind)
	if role == "" {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "unknown keyKind: %q", req.KeyKind)
		return
	}
	cosmosBumpKeyGen(id, role)
	w.WriteHeader(http.StatusOK)
}

var cosmosKeyRoles = []string{"primary", "secondary", "primary-readonly", "secondary-readonly"}

func cosmosKeyRole(kind string) string {
	switch strings.ToLower(kind) {
	case "primary":
		return "primary"
	case "secondary":
		return "secondary"
	case "primaryreadonly":
		return "primary-readonly"
	case "secondaryreadonly":
		return "secondary-readonly"
	}
	return ""
}

func cosmosBumpKeyGen(accountID, role string) {
	key := accountID + "|" + role
	g, _ := cosmosKeyGens.Get(key)
	g.N++
	// A regenerate clears any pin a move left, returning to derived material.
	g.Key = ""
	cosmosKeyGens.Put(key, g)
}

// cosmosKeyMaterial returns the current key for an account role, advancing the
// seed each time the role has been regenerated.
func cosmosKeyMaterial(accountID, role string) string {
	g, _ := cosmosKeyGens.Get(accountID + "|" + role)
	if g.Key != "" {
		return g.Key
	}
	if g.N == 0 {
		return simListKey32(accountID, role)
	}
	return simListKey32(accountID, fmt.Sprintf("%s-gen%d", role, g.N))
}

// The material derives from the resource ID, which embeds the resource group;
// Azure Cosmos DB never rotates keys on a move.
func pinCosmosKeys(oldID, newID string) {
	for _, role := range cosmosKeyRoles {
		pinned := cosmosKeyMaterial(oldID, role)
		row, _ := cosmosKeyGens.Get(oldID + "|" + role)
		cosmosKeyGens.Delete(oldID + "|" + role)
		row.Key = pinned
		cosmosKeyGens.Put(newID+"|"+role, row)
	}
}

// 200 if the name is taken, 404 if available.
func handleCosmosCheckNameExists(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "account")
	for _, a := range cosmosAccounts.List() {
		if a.Name == name {
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
}

func handleCosmosOperations(w http.ResponseWriter, _ *http.Request) {
	// The Cosmos DB OperationDisplay schema capitalizes its keys
	// (Provider/Resource/Operation/Description), unlike most providers.
	op := func(name, op, desc string) map[string]any {
		return map[string]any{
			"name": name,
			"display": map[string]any{
				"Provider":    "Microsoft DocumentDB",
				"Resource":    "databaseAccounts",
				"Operation":   op,
				"Description": desc,
			},
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []map[string]any{
		op("Microsoft.DocumentDB/databaseAccounts/read", "Read database accounts", "Read a database account."),
		op("Microsoft.DocumentDB/databaseAccounts/write", "Create or update database accounts", "Create or update a database account."),
		op("Microsoft.DocumentDB/databaseAccounts/delete", "Delete database accounts", "Delete a database account."),
		op("Microsoft.DocumentDB/databaseAccounts/listKeys/action", "List keys", "List the access keys for a database account."),
	}})
}

func handleCosmosLocations(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	names := []string{"East US", "West US", "North Europe", "West Europe"}
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, cosmosLocationBody(sub, n))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleCosmosLocationGet(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	location := sim.PathParam(r, "location")
	sim.WriteJSON(w, http.StatusOK, cosmosLocationBody(sub, location))
}

func cosmosLocationBody(sub, name string) map[string]any {
	normalized := strings.ToLower(strings.ReplaceAll(name, " ", ""))
	return map[string]any{
		"id":   armSubscriptionResourceID(sub, "Microsoft.DocumentDB", "locations", normalized),
		"name": normalized,
		"type": "Microsoft.DocumentDB/locations",
		"properties": map[string]any{
			"status":                    "Online",
			"supportsAvailabilityZone":  true,
			"isResidencyRestricted":     false,
			"backupStorageRedundancies": []string{"Geo", "Local", "Zone"},
		},
	}
}
