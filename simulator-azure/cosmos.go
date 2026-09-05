package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// cosmosETagSeq makes document ETags unique even within the same wall-clock
// second, so optimistic-concurrency (If-Match) can distinguish two quick
// writes. The sequence also orders the change feed, so each register function
// whose store carries ETags raises the floor above every persisted sequence at
// startup (cosmosRaiseETagFloor) — a change-feed continuation token issued
// before a SIM_PERSIST restart must never observe the clock running backwards.
var cosmosETagSeq atomic.Uint64

// cosmosRaiseETagFloor lifts cosmosETagSeq to at least seq.
func cosmosRaiseETagFloor(seq uint64) {
	for {
		cur := cosmosETagSeq.Load()
		if seq <= cur || cosmosETagSeq.CompareAndSwap(cur, seq) {
			return
		}
	}
}

// cosmosIfMatchOK reports whether the request's If-Match precondition is
// satisfied against the current ETag (an absent header always passes).
func cosmosIfMatchOK(r *http.Request, currentETag string) bool {
	im := r.Header.Get("If-Match")
	if im == "" {
		return true
	}
	return strings.Trim(im, `"`) == strings.Trim(currentETag, `"`)
}

// Azure Cosmos DB for NoSQL. The simulator exposes both the
// Microsoft.DocumentDB ARM control plane used by Terraform/az and the
// SQL data plane used by Cosmos clients for database, container, item,
// and query operations.

type CosmosAccount struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Kind       string            `json:"kind,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
}

type CosmosSQLDatabase struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type CosmosSQLContainer struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type CosmosTable struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
}

type CosmosThroughput struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type CosmosDocument struct {
	ID      string         `json:"id"`
	Account string         `json:"-"`
	DB      string         `json:"-"`
	Coll    string         `json:"-"`
	Body    map[string]any `json:"body"`
	ETag    string         `json:"_etag,omitempty"`
	RID     string         `json:"_rid,omitempty"`
	Self    string         `json:"_self,omitempty"`
	TS      int64          `json:"_ts,omitempty"`
}

var (
	cosmosAccounts    sim.Store[CosmosAccount]
	cosmosDatabases   sim.Store[CosmosSQLDatabase]
	cosmosContainers  sim.Store[CosmosSQLContainer]
	cosmosTables      sim.Store[CosmosTable]
	cosmosThroughputs sim.Store[CosmosThroughput]
	cosmosDocs        sim.Store[CosmosDocument]
	cosmosDataColls   sim.Store[CosmosDataColl]
	cosmosDataDBs     sim.Store[CosmosDataDB]
)

func registerCosmosDB(srv *sim.Server) {
	cosmosAccounts = sim.MakeStore[CosmosAccount](srv.DB(), "cosmos_accounts")
	cosmosDatabases = sim.MakeStore[CosmosSQLDatabase](srv.DB(), "cosmos_sql_databases")
	cosmosContainers = sim.MakeStore[CosmosSQLContainer](srv.DB(), "cosmos_sql_containers")
	cosmosTables = sim.MakeStore[CosmosTable](srv.DB(), "cosmos_tables")
	cosmosThroughputs = sim.MakeStore[CosmosThroughput](srv.DB(), "cosmos_throughputs")
	cosmosDocs = sim.MakeStore[CosmosDocument](srv.DB(), "cosmos_documents")
	cosmosDataColls = sim.MakeStore[CosmosDataColl](srv.DB(), "cosmos_data_collections")
	cosmosDataDBs = sim.MakeStore[CosmosDataDB](srv.DB(), "cosmos_data_databases")

	cosmosInitSessionState(srv)
	for _, d := range cosmosDocs.List() {
		cosmosRaiseETagFloor(cosmosETagSeqOf(d.ETag))
	}

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts"
	srv.HandleFunc("PUT "+armBase+"/{account}", handleCosmosCreateAccount)
	srv.HandleFunc("GET "+armBase+"/{account}", handleCosmosGetAccount)
	srv.HandleFunc("DELETE "+armBase+"/{account}", handleCosmosDeleteAccount)
	srv.HandleFunc("GET "+armBase, handleCosmosListAccounts)
	srv.HandleFunc("POST "+armBase+"/{account}/listKeys", handleCosmosListKeys)
	srv.HandleFunc("POST "+armBase+"/{account}/listConnectionStrings", handleCosmosListConnectionStrings)
	srv.HandleFunc("POST "+armBase+"/{account}/readonlykeys", handleCosmosListReadOnlyKeys)

	srv.HandleFunc("PUT "+armBase+"/{account}/tables/{table}", handleCosmosCreateTable)
	srv.HandleFunc("GET "+armBase+"/{account}/tables/{table}", handleCosmosGetTable)
	srv.HandleFunc("DELETE "+armBase+"/{account}/tables/{table}", handleCosmosDeleteTable)
	srv.HandleFunc("GET "+armBase+"/{account}/tables", handleCosmosListTables)
	srv.HandleFunc("GET "+armBase+"/{account}/tables/{table}/throughputSettings/default", handleCosmosGetThroughput)
	srv.HandleFunc("PUT "+armBase+"/{account}/tables/{table}/throughputSettings/default", handleCosmosPutThroughput)

	srv.HandleFunc("PUT "+armBase+"/{account}/sqlDatabases/{database}", handleCosmosCreateSQLDatabase)
	srv.HandleFunc("GET "+armBase+"/{account}/sqlDatabases/{database}", handleCosmosGetSQLDatabase)
	srv.HandleFunc("DELETE "+armBase+"/{account}/sqlDatabases/{database}", handleCosmosDeleteSQLDatabase)
	srv.HandleFunc("GET "+armBase+"/{account}/sqlDatabases", handleCosmosListSQLDatabases)
	srv.HandleFunc("GET "+armBase+"/{account}/sqlDatabases/{database}/throughputSettings/default", handleCosmosGetThroughput)
	srv.HandleFunc("PUT "+armBase+"/{account}/sqlDatabases/{database}/throughputSettings/default", handleCosmosPutThroughput)

	srv.HandleFunc("PUT "+armBase+"/{account}/sqlDatabases/{database}/containers/{container}", handleCosmosCreateSQLContainer)
	srv.HandleFunc("GET "+armBase+"/{account}/sqlDatabases/{database}/containers/{container}", handleCosmosGetSQLContainer)
	srv.HandleFunc("DELETE "+armBase+"/{account}/sqlDatabases/{database}/containers/{container}", handleCosmosDeleteSQLContainer)
	srv.HandleFunc("GET "+armBase+"/{account}/sqlDatabases/{database}/containers", handleCosmosListSQLContainers)
	srv.HandleFunc("GET "+armBase+"/{account}/sqlDatabases/{database}/containers/{container}/throughputSettings/default", handleCosmosGetThroughput)
	srv.HandleFunc("PUT "+armBase+"/{account}/sqlDatabases/{database}/containers/{container}/throughputSettings/default", handleCosmosPutThroughput)

	// Account discovery: the azcosmos SDK's global-endpoint-manager GETs the
	// account root on its first request and fails if it errors, so this is
	// required for the real SDK to talk to the sim at all.
	srv.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		handleCosmosAccountProperties(srv, w, r)
	})

	srv.HandleFunc("POST /dbs", handleCosmosDataCreateDB)
	srv.HandleFunc("GET /dbs", handleCosmosDataListDBs)
	srv.HandleFunc("GET /dbs/{database}", handleCosmosDataGetDB)
	srv.HandleFunc("DELETE /dbs/{database}", handleCosmosDataDeleteDB)
	srv.HandleFunc("POST /dbs/{database}/colls", handleCosmosDataCreateColl)
	srv.HandleFunc("GET /dbs/{database}/colls", handleCosmosDataListColls)
	srv.HandleFunc("GET /dbs/{database}/colls/{container}", handleCosmosDataGetColl)
	srv.HandleFunc("DELETE /dbs/{database}/colls/{container}", handleCosmosDataDeleteColl)
	srv.HandleFunc("POST /dbs/{database}/colls/{container}/docs", handleCosmosDataCreateOrQueryDoc)
	srv.HandleFunc("GET /dbs/{database}/colls/{container}/docs", handleCosmosDataListDocs)
	srv.HandleFunc("GET /dbs/{database}/colls/{container}/docs/{doc}", handleCosmosDataGetDoc)
	srv.HandleFunc("PUT /dbs/{database}/colls/{container}/docs/{doc}", handleCosmosDataReplaceDoc)
	srv.HandleFunc("PATCH /dbs/{database}/colls/{container}/docs/{doc}", handleCosmosDataPatchDoc)
	srv.HandleFunc("DELETE /dbs/{database}/colls/{container}/docs/{doc}", handleCosmosDataDeleteDoc)

	registerCosmosThroughput(srv)
}

func cosmosAccountID(sub, rg, account string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DocumentDB/databaseAccounts/%s", sub, rg, account)
}

func cosmosSQLDatabaseID(sub, rg, account, database string) string {
	return cosmosAccountID(sub, rg, account) + "/sqlDatabases/" + database
}

func cosmosSQLContainerID(sub, rg, account, database, container string) string {
	return cosmosSQLDatabaseID(sub, rg, account, database) + "/containers/" + container
}

func cosmosTableID(sub, rg, account, table string) string {
	return cosmosAccountID(sub, rg, account) + "/tables/" + table
}

func cosmosDocKey(account, database, container, doc string) string {
	return account + "/" + database + "/" + container + "/" + doc
}

// cosmosDataAccount is the account a data-plane request addressed, read from
// the host the client dialled.
//
// That host is the whole coordinate. Azure Cosmos DB gives each account a
// hostname of its own — the `documentEndpoint` the control plane advertises —
// and a client reaches an account by resolving it; there is no other way to
// name one, which is why the account name must be globally unique and why
// creating a second account under an existing name is refused.
//
// A client that dials the simulator by address rather than by name says so in
// its `Host` header, exactly as it would against the service through a proxy.
// A request whose host names no account resolves to none, and the data plane's
// own authorization refuses it, because there is no account whose keys could
// have signed it.
func cosmosDataAccount(r *http.Request) string {
	host := strings.Split(r.Host, ":")[0]
	if i := strings.Index(host, ".documents."); i > 0 {
		return host[:i]
	}
	return ""
}

func handleCosmosCreateAccount(w http.ResponseWriter, r *http.Request) {
	sub, rg, name := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account")
	var req CosmosAccount
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid Cosmos account body: %v", err)
		return
	}
	id := cosmosAccountID(sub, rg, name)
	// An account name is a hostname: the control plane advertises
	// `<name>.documents.azure.com` as the account's documentEndpoint, and a
	// client has no other way to address one. The name is therefore global, and
	// the service publishes an operation whose only purpose is to say so —
	// `DatabaseAccounts_CheckNameExists`, which this simulator serves, answering
	// 200 for a name in use and 404 for one that is free.
	//
	// Creating a second account under a name that operation reports as taken
	// would contradict it, and it did: two accounts could exist under one name
	// in different resource groups, which is a state Azure cannot hold. A PUT
	// to the same resource identifier is still the update it is defined to be;
	// only a different identifier claiming a name already in use is refused.
	//
	// The refusal is a 409 naming what is unavailable. The vendored swagger
	// declares no error response for this operation at all — only the 200 — so
	// the code string here follows the nearest captured Azure refusal of a
	// globally-unique name, Azure Cache for Redis's `NameNotAvailable`. Serving
	// the name to a second account is the one thing that is certainly wrong.
	for _, existing := range cosmosAccounts.List() {
		if existing.Name == name && existing.ID != id {
			AzureErrorf(w, "NameNotAvailable", http.StatusConflict,
				"The account name %s is not available.", name)
			return
		}
	}
	props := map[string]any{
		"provisioningState":        "Succeeded",
		"documentEndpoint":         azureCosmosEndpointURL(r, name),
		"databaseAccountOfferType": "Standard",
	}
	for k, v := range req.Properties {
		props[k] = v
	}
	cosmosNormalizeLocations(props, name, req.Location)
	cosmosEnsureBackupPolicy(props)
	cosmosEnsureConsistencyPolicy(props)
	a := CosmosAccount{
		ID:         id,
		Name:       name,
		Type:       "Microsoft.DocumentDB/databaseAccounts",
		Location:   req.Location,
		Tags:       req.Tags,
		Kind:       defaultString(req.Kind, "GlobalDocumentDB"),
		Properties: props,
	}
	cosmosAccounts.Put(id, a)
	sim.WriteJSON(w, http.StatusOK, a)
}

// cosmosEnsureConsistencyPolicy injects the default Session consistency
// policy real Azure GET always returns when the create body omits it.
func cosmosEnsureConsistencyPolicy(props map[string]any) {
	if cp, ok := props["consistencyPolicy"].(map[string]any); ok && cp["defaultConsistencyLevel"] != nil {
		return
	}
	props["consistencyPolicy"] = map[string]any{
		"defaultConsistencyLevel": "Session",
		"maxIntervalInSeconds":    5,
		"maxStalenessPrefix":      100,
	}
}

func cosmosEnsureBackupPolicy(props map[string]any) {
	backupPolicy, _ := props["backupPolicy"].(map[string]any)
	if backupPolicy == nil {
		backupPolicy = map[string]any{}
	}
	if backupPolicy["type"] == nil || fmt.Sprint(backupPolicy["type"]) == "" {
		backupPolicy["type"] = "Periodic"
	}
	if backupPolicy["type"] == "Periodic" && backupPolicy["periodicModeProperties"] == nil {
		backupPolicy["periodicModeProperties"] = map[string]any{
			"backupIntervalInMinutes":        240,
			"backupRetentionIntervalInHours": 8,
		}
	}
	props["backupPolicy"] = backupPolicy
}

// cosmosNormalizeLocations rebuilds the geo-replication arrays the real ARM
// Microsoft.DocumentDB/databaseAccounts GET returns from the create request's
// properties.locations (Terraform's geo_location blocks). It is load-bearing
// for both correctness and provider stability:
//   - terraform-provider-azurerm builds `geo_location` from
//     properties.failoverPolicies (NOT locations), looking up zone_redundant
//     in properties.locations by a shared id. Without failoverPolicies the
//     provider sees zero geo_locations and plans to add the block every refresh.
//   - the create poll (cosmosdb_account_resource.go:1595) dereferences
//     *location.ProvisioningState over read/writeLocations — every entry MUST
//     carry a non-nil provisioningState or the provider panics.
//
// Each region gets a stable id `{account}-{location-normalized}` shared between
// failoverPolicies and locations so findZoneRedundant resolves.
func cosmosNormalizeLocations(props map[string]any, accountName, accountLocation string) {
	type region struct {
		name string
		prio int
		zone bool
	}
	var regions []region
	if raw, ok := props["locations"].([]any); ok {
		for _, item := range raw {
			loc, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, _ := loc["locationName"].(string)
			if name == "" {
				continue
			}
			zone, _ := loc["isZoneRedundant"].(bool)
			regions = append(regions, region{name: name, prio: cosmosToInt(loc["failoverPriority"]), zone: zone})
		}
	}
	if len(regions) == 0 {
		regions = []region{{name: accountLocation, prio: 0, zone: false}}
	}

	docEndpoint, _ := props["documentEndpoint"].(string)
	locID := func(name string) string {
		return accountName + "-" + strings.ToLower(strings.ReplaceAll(name, " ", ""))
	}

	locations := make([]map[string]any, 0, len(regions))
	writeLocations := make([]map[string]any, 0, 1)
	readLocations := make([]map[string]any, 0, len(regions))
	failoverPolicies := make([]map[string]any, 0, len(regions))
	for _, reg := range regions {
		id := locID(reg.name)
		loc := map[string]any{
			"id":                id,
			"locationName":      reg.name,
			"failoverPriority":  reg.prio,
			"isZoneRedundant":   reg.zone,
			"provisioningState": "Succeeded",
			"documentEndpoint":  docEndpoint,
		}
		locations = append(locations, loc)
		readLocations = append(readLocations, loc)
		if reg.prio == 0 {
			writeLocations = append(writeLocations, loc)
		}
		failoverPolicies = append(failoverPolicies, map[string]any{
			"id":               id,
			"locationName":     reg.name,
			"failoverPriority": reg.prio,
		})
	}
	if len(writeLocations) == 0 {
		writeLocations = append(writeLocations, locations[0])
	}
	props["locations"] = locations
	props["writeLocations"] = writeLocations
	props["readLocations"] = readLocations
	props["failoverPolicies"] = failoverPolicies
}

// cosmosToInt coerces a JSON-decoded number (float64) or int to int.
func cosmosToInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func handleCosmosGetAccount(w http.ResponseWriter, r *http.Request) {
	id := cosmosAccountID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account"))
	a, ok := cosmosAccounts.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB account not found: %s", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, a)
}

func handleCosmosListAccounts(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DocumentDB/databaseAccounts/",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"))
	all := cosmosAccounts.Filter(func(a CosmosAccount) bool { return strings.HasPrefix(a.ID, prefix) })
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	filtered, err := azureApplyListQuery(all, r)
	if err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	all = filtered
	page, next := armPage(r, all)
	if page == nil {
		page = []CosmosAccount{}
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = armNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCosmosDeleteAccount(w http.ResponseWriter, r *http.Request) {
	account := sim.PathParam(r, "account")
	id := cosmosAccountID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), account)
	if !cosmosAccounts.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB account not found: %s", id)
		return
	}
	for _, db := range cosmosDatabases.List() {
		if strings.HasPrefix(db.ID, id+"/") {
			cosmosDatabases.Delete(db.ID)
		}
	}
	for _, c := range cosmosContainers.List() {
		if strings.HasPrefix(c.ID, id+"/") {
			cosmosContainers.Delete(c.ID)
		}
	}
	for _, t := range cosmosTables.List() {
		if strings.HasPrefix(t.ID, id+"/") {
			cosmosTables.Delete(t.ID)
			deleteTableDataPlaneProjection(account, t.Name)
		}
	}
	for _, t := range cosmosThroughputs.List() {
		if strings.HasPrefix(t.ID, id+"/") {
			cosmosThroughputs.Delete(t.ID)
		}
	}
	for _, c := range cosmosResources.List() {
		if strings.HasPrefix(c.ID, id+"/") {
			cosmosResources.Delete(c.ID)
		}
	}
	for _, role := range cosmosKeyRoles {
		cosmosKeyGens.Delete(id + "|" + role)
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleCosmosListKeys(w http.ResponseWriter, r *http.Request) {
	id := cosmosAccountID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account"))
	if _, ok := cosmosAccounts.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB account not found: %s", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"primaryMasterKey":           cosmosKeyMaterial(id, "primary"),
		"secondaryMasterKey":         cosmosKeyMaterial(id, "secondary"),
		"primaryReadonlyMasterKey":   cosmosKeyMaterial(id, "primary-readonly"),
		"secondaryReadonlyMasterKey": cosmosKeyMaterial(id, "secondary-readonly"),
	})
}

// handleCosmosListReadOnlyKeys serves POST .../readonlykeys. The
// DatabaseAccountListReadOnlyKeysResult shape carries ONLY the two
// readonly keys — the writable master keys never appear here.
func handleCosmosListReadOnlyKeys(w http.ResponseWriter, r *http.Request) {
	id := cosmosAccountID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account"))
	if _, ok := cosmosAccounts.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB account not found: %s", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"primaryReadonlyMasterKey":   cosmosKeyMaterial(id, "primary-readonly"),
		"secondaryReadonlyMasterKey": cosmosKeyMaterial(id, "secondary-readonly"),
	})
}

func handleCosmosListConnectionStrings(w http.ResponseWriter, r *http.Request) {
	id := cosmosAccountID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account"))
	a, ok := cosmosAccounts.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB account not found: %s", id)
		return
	}
	key := cosmosKeyMaterial(id, "primary")
	endpoint, _ := a.Properties["documentEndpoint"].(string)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"connectionStrings": []map[string]any{{
			"description":      "Primary SQL Connection String",
			"connectionString": "AccountEndpoint=" + endpoint + ";AccountKey=" + key + ";",
			"keyKind":          "Primary",
			"type":             "Sql",
		}},
	})
}

func handleCosmosCreateTable(w http.ResponseWriter, r *http.Request) {
	sub, rg, account := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account")
	table := sim.PathParam(r, "table")
	if _, ok := cosmosAccounts.Get(cosmosAccountID(sub, rg, account)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB account not found: %s", account)
		return
	}
	var req CosmosTable
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid table body: %v", err)
		return
	}
	props := ensureResourceProperty(req.Properties, table)
	if options, ok := props["options"].(map[string]any); ok {
		if throughput := options["throughput"]; throughput != nil {
			cosmosThroughputs.Put(cosmosTableID(sub, rg, account, table)+"/throughputSettings/default", CosmosThroughput{
				ID:   cosmosTableID(sub, rg, account, table) + "/throughputSettings/default",
				Name: "default",
				Type: "Microsoft.DocumentDB/databaseAccounts/tables/throughputSettings",
				Properties: map[string]any{
					"resource": map[string]any{"throughput": throughput},
				},
			})
		}
	}
	c := CosmosTable{
		ID:         cosmosTableID(sub, rg, account, table),
		Name:       table,
		Type:       "Microsoft.DocumentDB/databaseAccounts/tables",
		Location:   req.Location,
		Tags:       req.Tags,
		Properties: props,
	}
	cosmosTables.Put(c.ID, c)
	upsertTableDataPlaneProjection(account, table)
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleCosmosGetTable(w http.ResponseWriter, r *http.Request) {
	sub, rg, account := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account")
	table := sim.PathParam(r, "table")
	t, ok := cosmosTables.Get(cosmosTableID(sub, rg, account, table))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB table not found: %s", table)
		return
	}
	sim.WriteJSON(w, http.StatusOK, t)
}

func handleCosmosListTables(w http.ResponseWriter, r *http.Request) {
	sub, rg, account := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account")
	prefix := cosmosAccountID(sub, rg, account) + "/tables/"
	all := cosmosTables.Filter(func(t CosmosTable) bool { return strings.HasPrefix(t.ID, prefix) })
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	if all == nil {
		all = []CosmosTable{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
}

func handleCosmosDeleteTable(w http.ResponseWriter, r *http.Request) {
	sub, rg, account := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account")
	table := sim.PathParam(r, "table")
	id := cosmosTableID(sub, rg, account, table)
	if !cosmosTables.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB table not found: %s", table)
		return
	}
	cosmosThroughputs.Delete(id + "/throughputSettings/default")
	deleteTableDataPlaneProjection(account, table)
	w.WriteHeader(http.StatusNoContent)
}

func handleCosmosCreateSQLDatabase(w http.ResponseWriter, r *http.Request) {
	sub, rg, account, database := cosmosARMParts(r)
	if _, ok := cosmosAccounts.Get(cosmosAccountID(sub, rg, account)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB account not found: %s", account)
		return
	}
	var req CosmosSQLDatabase
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid SQL database body: %v", err)
		return
	}
	id := cosmosSQLDatabaseID(sub, rg, account, database)
	db := CosmosSQLDatabase{
		ID:         id,
		Name:       database,
		Type:       "Microsoft.DocumentDB/databaseAccounts/sqlDatabases",
		Properties: ensureResourceProperty(req.Properties, database),
	}
	cosmosDatabases.Put(id, db)
	sim.WriteJSON(w, http.StatusOK, db)
}

func handleCosmosGetSQLDatabase(w http.ResponseWriter, r *http.Request) {
	sub, rg, account, database := cosmosARMParts(r)
	db, ok := cosmosDatabases.Get(cosmosSQLDatabaseID(sub, rg, account, database))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos SQL database not found: %s", database)
		return
	}
	sim.WriteJSON(w, http.StatusOK, db)
}

func handleCosmosListSQLDatabases(w http.ResponseWriter, r *http.Request) {
	sub, rg, account := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account")
	prefix := cosmosAccountID(sub, rg, account) + "/sqlDatabases/"
	all := cosmosDatabases.Filter(func(db CosmosSQLDatabase) bool { return strings.HasPrefix(db.ID, prefix) })
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
}

func handleCosmosDeleteSQLDatabase(w http.ResponseWriter, r *http.Request) {
	sub, rg, account, database := cosmosARMParts(r)
	id := cosmosSQLDatabaseID(sub, rg, account, database)
	if !cosmosDatabases.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos SQL database not found: %s", database)
		return
	}
	for _, c := range cosmosContainers.List() {
		if strings.HasPrefix(c.ID, id+"/") {
			cosmosContainers.Delete(c.ID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleCosmosCreateSQLContainer(w http.ResponseWriter, r *http.Request) {
	sub, rg, account, database := cosmosARMParts(r)
	container := sim.PathParam(r, "container")
	if _, ok := cosmosDatabases.Get(cosmosSQLDatabaseID(sub, rg, account, database)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos SQL database not found: %s", database)
		return
	}
	var req CosmosSQLContainer
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid SQL container body: %v", err)
		return
	}
	id := cosmosSQLContainerID(sub, rg, account, database, container)
	c := CosmosSQLContainer{
		ID:         id,
		Name:       container,
		Type:       "Microsoft.DocumentDB/databaseAccounts/sqlDatabases/containers",
		Properties: ensureResourceProperty(req.Properties, container),
	}
	// SqlContainerGetProperties nests partitionKey under
	// properties.resource, never directly under properties.
	if res, ok := c.Properties["resource"].(map[string]any); ok && res["partitionKey"] == nil {
		res["partitionKey"] = map[string]any{"paths": []string{"/id"}, "kind": "Hash"}
	}
	cosmosContainers.Put(id, c)
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleCosmosGetSQLContainer(w http.ResponseWriter, r *http.Request) {
	sub, rg, account, database := cosmosARMParts(r)
	container := sim.PathParam(r, "container")
	c, ok := cosmosContainers.Get(cosmosSQLContainerID(sub, rg, account, database, container))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos SQL container not found: %s", container)
		return
	}
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleCosmosListSQLContainers(w http.ResponseWriter, r *http.Request) {
	sub, rg, account, database := cosmosARMParts(r)
	prefix := cosmosSQLDatabaseID(sub, rg, account, database) + "/containers/"
	all := cosmosContainers.Filter(func(c CosmosSQLContainer) bool { return strings.HasPrefix(c.ID, prefix) })
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
}

func handleCosmosDeleteSQLContainer(w http.ResponseWriter, r *http.Request) {
	sub, rg, account, database := cosmosARMParts(r)
	container := sim.PathParam(r, "container")
	if !cosmosContainers.Delete(cosmosSQLContainerID(sub, rg, account, database, container)) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos SQL container not found: %s", container)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleCosmosGetThroughput(w http.ResponseWriter, r *http.Request) {
	id := cosmosThroughputID(r)
	t, ok := cosmosThroughputs.Get(id)
	if !ok {
		t = CosmosThroughput{
			ID:   id,
			Name: "default",
			Type: cosmosThroughputType(r),
			Properties: map[string]any{
				"resource": map[string]any{"throughput": float64(400)},
			},
		}
	}
	sim.WriteJSON(w, http.StatusOK, t)
}

func handleCosmosPutThroughput(w http.ResponseWriter, r *http.Request) {
	var req CosmosThroughput
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid throughput body: %v", err)
		return
	}
	id := cosmosThroughputID(r)
	req.ID = id
	req.Name = "default"
	if req.Type == "" {
		req.Type = cosmosThroughputType(r)
	}
	if req.Properties == nil {
		req.Properties = map[string]any{"resource": map[string]any{"throughput": float64(400)}}
	}
	cosmosThroughputs.Put(id, req)
	sim.WriteJSON(w, http.StatusOK, req)
}

func cosmosARMParts(r *http.Request) (string, string, string, string) {
	return sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account"), sim.PathParam(r, "database")
}

// cosmosThroughputID is the ARM id of the throughputSettings/default resource
// being addressed — which is exactly the request path. Keying by the path lets
// one pair of throughput handlers serve every family (SQL, Table, MongoDB,
// Cassandra, Gremlin) without per-family path-param plumbing.
func cosmosThroughputID(r *http.Request) string {
	return r.URL.Path
}

func cosmosThroughputType(r *http.Request) string {
	return cosmosThroughputTypeFromPath(r.URL.Path)
}

func ensureResourceProperty(props map[string]any, name string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	resource, _ := props["resource"].(map[string]any)
	if resource == nil {
		resource = map[string]any{}
	}
	if resource["id"] == nil {
		resource["id"] = name
	}
	props["resource"] = resource
	return props
}

func azureCosmosEndpointURL(r *http.Request, account string) string {
	return fmt.Sprintf("%s://%s/", azureRequestScheme(r), azureEndpointHost(r, account, "documents"))
}

func defaultString(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func handleCosmosDataCreateDB(w http.ResponseWriter, r *http.Request) {
	account := cosmosDataAccount(r)
	var body map[string]any
	if err := sim.ReadJSON(r, &body); err != nil {
		cosmosDataError(w, "BadRequest", "invalid database body", http.StatusBadRequest)
		return
	}
	id, _ := body["id"].(string)
	if id == "" {
		cosmosDataError(w, "BadRequest", "id is required", http.StatusBadRequest)
		return
	}
	// The database is recorded, not just described back. A create whose only
	// trace is its own response leaves the read that follows with nothing to
	// find, and leaves existence to be guessed from whatever containers happen
	// to be under it.
	cosmosDataDBs.Put(cosmosDataDBKey(account, id), CosmosDataDB{Account: account, DB: id})
	db := cosmosDataDB(account, id)
	if rid, ok := db["_rid"].(string); ok {
		cosmosProvisionOfferFromHeaders(r, account, rid)
	}
	cosmosWriteData(w, http.StatusCreated, db)
}

func handleCosmosDataListDBs(w http.ResponseWriter, r *http.Request) {
	account := cosmosDataAccount(r)
	dbs := map[string]map[string]any{}
	for _, d := range cosmosDataDBs.List() {
		if d.Account == account {
			dbs[d.DB] = cosmosDataDB(account, d.DB)
		}
	}
	for _, c := range cosmosContainers.List() {
		if acc, db, _, ok := cosmosARMIDNames(c.ID); ok && acc == account {
			dbs[db] = cosmosDataDB(account, db)
		}
	}
	for _, d := range cosmosDocs.List() {
		if d.Account == account {
			dbs[d.DB] = cosmosDataDB(account, d.DB)
		}
	}
	items := make([]map[string]any, 0, len(dbs))
	for _, db := range dbs {
		items = append(items, db)
	}
	cosmosWriteData(w, http.StatusOK, map[string]any{"Databases": items, "_count": len(items)})
}

// cosmosDataDBExists reports whether an account holds a database on the data
// plane. It is the same derivation the listing uses — a database exists once a
// container or a document is under it, however that container was created —
// so a read and a list cannot disagree about what is there.
func cosmosDataDBExists(account, db string) bool {
	if _, created := cosmosDataDBs.Get(cosmosDataDBKey(account, db)); created {
		return true
	}
	for _, c := range cosmosContainers.List() {
		if acc, name, _, ok := cosmosARMIDNames(c.ID); ok && acc == account && name == db {
			return true
		}
	}
	for _, c := range cosmosDataColls.List() {
		if c.Account == account && c.DB == db {
			return true
		}
	}
	for _, d := range cosmosDocs.List() {
		if d.Account == account && d.DB == db {
			return true
		}
	}
	return false
}

func handleCosmosDataGetDB(w http.ResponseWriter, r *http.Request) {
	account, db := cosmosDataAccount(r), sim.PathParam(r, "database")
	// A read of a database nobody created is not a database. Answering 200
	// told a caller that every name it asked about was already there, which
	// the listing beside it contradicts.
	if !cosmosDataDBExists(account, db) {
		cosmosDataError(w, "NotFound", "Owner resource does not exist", http.StatusNotFound)
		return
	}
	cosmosWriteData(w, http.StatusOK, cosmosDataDB(account, db))
}

func handleCosmosDataDeleteDB(w http.ResponseWriter, r *http.Request) {
	account, db := cosmosDataAccount(r), sim.PathParam(r, "database")
	cosmosDataDBs.Delete(cosmosDataDBKey(account, db))
	for _, d := range cosmosDocs.List() {
		if d.Account == account && d.DB == db {
			cosmosDocs.Delete(cosmosStoredDocKey(d))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleCosmosDataCreateColl(w http.ResponseWriter, r *http.Request) {
	account, db := cosmosDataAccount(r), sim.PathParam(r, "database")
	var body map[string]any
	if err := sim.ReadJSON(r, &body); err != nil {
		cosmosDataError(w, "BadRequest", "invalid collection body", http.StatusBadRequest)
		return
	}
	id, _ := body["id"].(string)
	if id == "" {
		cosmosDataError(w, "BadRequest", "id is required", http.StatusBadRequest)
		return
	}
	// Persist the declared partition-key path so the data plane can scope items
	// by (partition key, id) — the SDK declares it here, not via ARM.
	pkPath := ""
	if pk, ok := body["partitionKey"].(map[string]any); ok {
		if paths, ok := pk["paths"].([]any); ok && len(paths) > 0 {
			pkPath, _ = paths[0].(string)
		}
	}
	cosmosDataColls.Put(cosmosDataCollKey(account, db, id), CosmosDataColl{Account: account, DB: db, Coll: id, PKPath: pkPath})
	coll := cosmosDataColl(account, db, id)
	if rid, ok := coll["_rid"].(string); ok {
		cosmosProvisionOfferFromHeaders(r, account, rid)
	}
	cosmosWriteData(w, http.StatusCreated, coll)
}

func handleCosmosDataListColls(w http.ResponseWriter, r *http.Request) {
	account, db := cosmosDataAccount(r), sim.PathParam(r, "database")
	colls := map[string]map[string]any{}
	for _, d := range cosmosDocs.List() {
		if d.Account == account && d.DB == db {
			colls[d.Coll] = cosmosDataColl(account, db, d.Coll)
		}
	}
	items := make([]map[string]any, 0, len(colls))
	for _, c := range colls {
		items = append(items, c)
	}
	cosmosWriteData(w, http.StatusOK, map[string]any{"DocumentCollections": items, "_count": len(items)})
}

func handleCosmosDataGetColl(w http.ResponseWriter, r *http.Request) {
	account, db, coll := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container")
	// A container created here, or one created through the management plane —
	// both are the same container to a data-plane client, and neither is a
	// name nobody used.
	_, created := cosmosDataColls.Get(cosmosDataCollKey(account, db, coll))
	if !created {
		for _, c := range cosmosContainers.List() {
			if acc, name, container, ok := cosmosARMIDNames(c.ID); ok &&
				acc == account && name == db && container == coll {
				created = true
				break
			}
		}
	}
	if !created {
		cosmosDataError(w, "NotFound", "Owner resource does not exist", http.StatusNotFound)
		return
	}
	cosmosWriteData(w, http.StatusOK, cosmosDataColl(account, db, coll))
}

func handleCosmosDataDeleteColl(w http.ResponseWriter, r *http.Request) {
	account, db, coll := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container")
	for _, d := range cosmosDocs.List() {
		if d.Account == account && d.DB == db && d.Coll == coll {
			cosmosDocs.Delete(cosmosStoredDocKey(d))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleCosmosDataCreateOrQueryDoc(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Header.Get("x-ms-documentdb-isquery"), "true") ||
		strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/query+json") {
		handleCosmosDataQueryDocs(w, r)
		return
	}
	account, db, coll := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container")
	if cosmosGuardConsistency(w, r, account) {
		return
	}
	var body map[string]any
	if err := sim.ReadJSON(r, &body); err != nil {
		cosmosDataError(w, "BadRequest", "invalid document body", http.StatusBadRequest)
		return
	}
	id, _ := body["id"].(string)
	if id == "" {
		id = generateUUID()
		body["id"] = id
	}
	pkComponent, werr := cosmosResolvePKForWrite(r, account, db, coll, body)
	if werr != nil {
		cosmosDataError(w, werr.code, werr.msg, werr.status)
		return
	}
	upsert := strings.EqualFold(r.Header.Get("x-ms-documentdb-is-upsert"), "true")
	key := cosmosDocKeyPK(account, db, coll, pkComponent, id)
	existing, exists := cosmosDocs.Get(key)
	if exists && !upsert {
		// Real Cosmos: a plain create (no upsert header) of an existing id is
		// a 409 Conflict.
		cosmosDataError(w, "Conflict",
			"Resource with specified id or name already exists.", http.StatusConflict)
		return
	}
	if exists && upsert {
		// Upsert of an existing id honors an If-Match precondition like Replace.
		if !cosmosIfMatchOK(r, existing.ETag) {
			cosmosDataError(w, "PreconditionFailed",
				"Operation cannot be performed because one of the specified precondition is not met.",
				http.StatusPreconditionFailed)
			return
		}
	}
	doc := cosmosStoreDocKey(key, account, db, coll, id, body)
	status := http.StatusCreated
	if exists && upsert {
		// An upsert that replaced an existing document returns 200, not 201.
		status = http.StatusOK
	}
	out := cosmosDocBody(doc)
	cosmosSetWriteSession(w, account, db, coll, pkComponent)
	cosmosWriteDataCharge(w, status, out, cosmosWriteCharge(out))
}

func handleCosmosDataListDocs(w http.ResponseWriter, r *http.Request) {
	// A `A-IM: Incremental feed` request switches the documents read into
	// change-feed mode (same route, as in real Cosmos).
	if cosmosIsChangeFeedRequest(r) {
		handleCosmosChangeFeed(w, r)
		return
	}
	account, db, coll := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container")
	items := cosmosDocsFor(account, db, coll)
	docs := make([]map[string]any, 0, len(items))
	for _, d := range items {
		docs = append(docs, cosmosDocBody(d))
	}
	cosmosWriteData(w, http.StatusOK, map[string]any{"Documents": docs, "_count": len(docs)})
}

func handleCosmosDataGetDoc(w http.ResponseWriter, r *http.Request) {
	account, db, coll, docID := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container"), sim.PathParam(r, "doc")
	if cosmosGuardConsistency(w, r, account) {
		return
	}
	doc, _, ok, werr := cosmosResolvePointDoc(r, account, db, coll, docID)
	if werr != nil {
		cosmosDataError(w, werr.code, werr.msg, werr.status)
		return
	}
	if !ok {
		cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
		return
	}
	out := cosmosDocBody(doc)
	cosmosEchoReadSession(w, r, account, db, coll, cosmosDocPKComponent(account, db, coll, doc))
	cosmosWriteDataCharge(w, http.StatusOK, out, cosmosReadCharge(out))
}

// cosmosResolvePointDoc looks up a single document addressed by (partition key,
// id). When the SDK sends the partition-key header the lookup is an exact,
// partition-scoped store-key read; without the header (legacy raw-HTTP) it falls
// back to an id scan within the collection. It returns the document, its store
// key, whether it was found, and any header-parse error.
func cosmosResolvePointDoc(r *http.Request, account, db, coll, docID string) (CosmosDocument, string, bool, *cosmosWriteError) {
	pkComponent, hasHeader, werr := cosmosResolvePKForPoint(r, account, db, coll)
	if werr != nil {
		return CosmosDocument{}, "", false, werr
	}
	if hasHeader {
		key := cosmosDocKeyPK(account, db, coll, pkComponent, docID)
		doc, ok := cosmosDocs.Get(key)
		return doc, key, ok, nil
	}
	doc, ok := cosmosFindDocByID(account, db, coll, docID)
	if !ok {
		return CosmosDocument{}, "", false, nil
	}
	return doc, cosmosDocKeyPK(account, db, coll, cosmosDocPKComponent(account, db, coll, doc), docID), true, nil
}

func handleCosmosDataReplaceDoc(w http.ResponseWriter, r *http.Request) {
	account, db, coll, docID := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container"), sim.PathParam(r, "doc")
	if cosmosGuardConsistency(w, r, account) {
		return
	}
	var body map[string]any
	if err := sim.ReadJSON(r, &body); err != nil {
		cosmosDataError(w, "BadRequest", "invalid document body", http.StatusBadRequest)
		return
	}
	body["id"] = docID
	pkComponent, werr := cosmosResolvePKForWrite(r, account, db, coll, body)
	if werr != nil {
		cosmosDataError(w, werr.code, werr.msg, werr.status)
		return
	}
	key := cosmosDocKeyPK(account, db, coll, pkComponent, docID)
	if existing, ok := cosmosDocs.Get(key); ok {
		if !cosmosIfMatchOK(r, existing.ETag) {
			cosmosDataError(w, "PreconditionFailed",
				"Operation cannot be performed because one of the specified precondition is not met.",
				http.StatusPreconditionFailed)
			return
		}
	}
	doc := cosmosStoreDocKey(key, account, db, coll, docID, body)
	out := cosmosDocBody(doc)
	cosmosSetWriteSession(w, account, db, coll, pkComponent)
	cosmosWriteDataCharge(w, http.StatusOK, out, cosmosWriteCharge(out))
}

func handleCosmosDataDeleteDoc(w http.ResponseWriter, r *http.Request) {
	account, db, coll, docID := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container"), sim.PathParam(r, "doc")
	if cosmosGuardConsistency(w, r, account) {
		return
	}
	existing, key, ok, werr := cosmosResolvePointDoc(r, account, db, coll, docID)
	if werr != nil {
		cosmosDataError(w, werr.code, werr.msg, werr.status)
		return
	}
	if !ok {
		cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
		return
	}
	if !cosmosIfMatchOK(r, existing.ETag) {
		cosmosDataError(w, "PreconditionFailed",
			"Operation cannot be performed because one of the specified precondition is not met.",
			http.StatusPreconditionFailed)
		return
	}
	cosmosDocs.Delete(key)
	w.Header().Set("x-ms-request-charge", cosmosFormatCharge(cosmosDeleteCharge()))
	cosmosSetWriteSession(w, account, db, coll, cosmosDocPKComponent(account, db, coll, existing))
	w.WriteHeader(http.StatusNoContent)
}

func handleCosmosDataQueryDocs(w http.ResponseWriter, r *http.Request) {
	account, db, coll := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container")
	if cosmosGuardConsistency(w, r, account) {
		return
	}
	var req struct {
		Query      string           `json:"query"`
		Parameters []map[string]any `json:"parameters,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		cosmosDataError(w, "BadRequest", "invalid query body", http.StatusBadRequest)
		return
	}
	plan, err := cosmosParseQuery(req.Query)
	if err != nil {
		cosmosDataError(w, "BadRequest",
			"Syntax error, message: "+err.Error(), http.StatusBadRequest)
		return
	}
	docs := cosmosDocsFor(account, db, coll)

	// Partition scoping: a pk header scopes the query to that single partition
	// even when the SDK also sets enablecrosspartition=true (which it does by
	// default), matching real Cosmos's single-partition routing.
	pkComponent, scoped, werr := cosmosResolvePKForPoint(r, account, db, coll)
	if werr != nil {
		cosmosDataError(w, werr.code, werr.msg, werr.status)
		return
	}
	if scoped {
		filtered := docs[:0:0]
		for _, d := range docs {
			if cosmosDocPKComponent(account, db, coll, d) == pkComponent {
				filtered = append(filtered, d)
			}
		}
		docs = filtered
	}

	out := cosmosRunQuery(plan, docs, cosmosBindParams(req.Parameters))

	// Pagination: honor x-ms-max-item-count + x-ms-continuation. A COUNT
	// aggregate is a single scalar row and is never paged.
	offset, oerr := cosmosContinuationOffset(r)
	if oerr != nil {
		cosmosDataError(w, "BadRequest", oerr.Error(), http.StatusBadRequest)
		return
	}
	maxItems := cosmosMaxItemCount(r)
	page := out
	var continuation string
	if !plan.countAll {
		if offset > len(out) {
			offset = len(out)
		}
		page = out[offset:]
		if maxItems >= 0 && maxItems < len(page) {
			page = page[:maxItems]
			continuation = cosmosEncodeContinuation(offset + maxItems)
		}
	}
	if continuation != "" {
		w.Header().Set("x-ms-continuation", continuation)
	}
	w.Header().Set("x-ms-item-count", fmt.Sprintf("%d", len(page)))
	cosmosWriteDataCharge(w, http.StatusOK, map[string]any{"Documents": page, "_count": len(page)}, cosmosQueryCharge(len(page)))
}

// handleCosmosDataPatchDoc applies a Cosmos partial-document patch (the
// `{operations:[{op,path,value}]}` body) to a stored document. op ∈
// set/add/replace/remove/incr; path is a JSON-pointer-ish `/a/b`.
func handleCosmosDataPatchDoc(w http.ResponseWriter, r *http.Request) {
	account, db, coll, docID := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container"), sim.PathParam(r, "doc")
	if cosmosGuardConsistency(w, r, account) {
		return
	}
	existing, key, ok, werr := cosmosResolvePointDoc(r, account, db, coll, docID)
	if werr != nil {
		cosmosDataError(w, werr.code, werr.msg, werr.status)
		return
	}
	if !ok {
		cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
		return
	}
	if !cosmosIfMatchOK(r, existing.ETag) {
		cosmosDataError(w, "PreconditionFailed",
			"Operation cannot be performed because one of the specified precondition is not met.",
			http.StatusPreconditionFailed)
		return
	}
	var req struct {
		Operations []struct {
			Op    string `json:"op"`
			Path  string `json:"path"`
			Value any    `json:"value"`
		} `json:"operations"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		cosmosDataError(w, "BadRequest", "invalid patch body", http.StatusBadRequest)
		return
	}
	// Operate on a deep copy so a mid-list failure doesn't leave a partial write.
	body := cosmosCloneBody(existing.Body)
	for _, op := range req.Operations {
		if err := cosmosApplyPatchOp(body, op.Op, op.Path, op.Value); err != nil {
			cosmosDataError(w, "BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
	}
	body["id"] = docID
	doc := cosmosStoreDocKey(key, account, db, coll, docID, body)
	out := cosmosDocBody(doc)
	cosmosSetWriteSession(w, account, db, coll, cosmosDocPKComponent(account, db, coll, doc))
	cosmosWriteDataCharge(w, http.StatusOK, out, cosmosWriteCharge(out))
}

// cosmosApplyPatchOp mutates body per a single patch operation. The path is the
// Cosmos JSON-pointer form (`/a/b`); the final segment is the property to act
// on within its parent object.
func cosmosApplyPatchOp(body map[string]any, op, path string, value any) error {
	segs := cosmosSplitPatchPath(path)
	if len(segs) == 0 {
		return fmt.Errorf("patch op %q has an empty path", op)
	}
	parent := body
	for _, seg := range segs[:len(segs)-1] {
		next, ok := parent[seg].(map[string]any)
		if !ok {
			// set/add create intermediate objects; others fail on a missing path.
			switch strings.ToLower(op) {
			case "set", "add":
				next = map[string]any{}
				parent[seg] = next
			default:
				return fmt.Errorf("patch op %q: path %q does not exist", op, path)
			}
		}
		parent = next
	}
	last := segs[len(segs)-1]
	switch strings.ToLower(op) {
	case "set", "add", "replace":
		if strings.ToLower(op) == "replace" {
			if _, ok := parent[last]; !ok {
				return fmt.Errorf("patch replace: path %q does not exist", path)
			}
		}
		parent[last] = value
	case "remove":
		if _, ok := parent[last]; !ok {
			return fmt.Errorf("patch remove: path %q does not exist", path)
		}
		delete(parent, last)
	case "incr", "increment":
		cur, _ := cosmosNumberOf(parent[last])
		delta, ok := cosmosNumberOf(value)
		if !ok {
			return fmt.Errorf("patch incr: value is not numeric")
		}
		parent[last] = cur + delta
	default:
		return fmt.Errorf("unsupported patch op %q", op)
	}
	return nil
}

func cosmosSplitPatchPath(path string) []string {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func cosmosNumberOf(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return 0, false
}

func cosmosCloneBody(in map[string]any) map[string]any {
	b, err := json.Marshal(in)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// cosmosStoreDocKey stores a document under an explicit, partition-scoped store
// key (built by cosmosDocKeyPK) so two docs with the same id in different
// partitions remain distinct items.
func cosmosStoreDocKey(key, account, db, coll, id string, body map[string]any) CosmosDocument {
	now := time.Now().UTC().Unix()
	doc := CosmosDocument{
		ID:      id,
		Account: account,
		DB:      db,
		Coll:    coll,
		Body:    body,
		ETag:    fmt.Sprintf(`"%x-%x"`, now, cosmosETagSeq.Add(1)),
		RID:     account + "-" + db + "-" + coll + "-" + id,
		Self:    "dbs/" + db + "/colls/" + coll + "/docs/" + id + "/",
		TS:      now,
	}
	cosmosDocs.Put(key, doc)
	return doc
}

func cosmosDocBody(doc CosmosDocument) map[string]any {
	body := make(map[string]any, len(doc.Body)+4)
	for k, v := range doc.Body {
		body[k] = v
	}
	body["_rid"] = doc.RID
	body["_self"] = doc.Self
	body["_etag"] = doc.ETag
	body["_ts"] = doc.TS
	body["_attachments"] = "attachments/"
	return body
}

func cosmosDocsFor(account, db, coll string) []CosmosDocument {
	docs := cosmosDocs.Filter(func(d CosmosDocument) bool {
		return d.Account == account && d.DB == db && d.Coll == coll
	})
	sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
	return docs
}

func cosmosDataDB(account, id string) map[string]any {
	return map[string]any{"id": id, "_rid": account + "-" + id, "_self": "dbs/" + id + "/", "_etag": `"db"`, "_ts": time.Now().UTC().Unix()}
}

func cosmosDataColl(account, db, id string) map[string]any {
	return map[string]any{"id": id, "_rid": account + "-" + db + "-" + id, "_self": "dbs/" + db + "/colls/" + id + "/", "_etag": `"coll"`, "_ts": time.Now().UTC().Unix()}
}

// cosmosIsDataPlaneRequest reports whether a request is a Cosmos data-plane call.
// It uses Cosmos-SPECIFIC signals — the master-key Authorization (`type=master`,
// which the azcosmos SDK URL-encodes), a documentdb header, or a host naming a
// Cosmos account — and deliberately NOT the bare `x-ms-version` header, which
// storage requests also send. Both the account-discovery root GET and the
// path-style storage fallback use this to avoid misrouting Cosmos traffic.
func cosmosIsDataPlaneRequest(r *http.Request) bool {
	if cosmosDataAccount(r) != "" {
		return true
	}
	auth := strings.ToLower(r.Header.Get("Authorization"))
	if strings.Contains(auth, "type=master") || strings.Contains(auth, "type%3dmaster") {
		return true
	}
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-ms-documentdb") {
			return true
		}
	}
	return false
}

// handleCosmosAccountProperties serves the Cosmos account root the SDK's global
// endpoint manager reads. The single read/write region echoes back the client's
// own endpoint so the SDK keeps routing every request to the sim.
func handleCosmosAccountProperties(srv *sim.Server, w http.ResponseWriter, r *http.Request) {
	if !cosmosIsDataPlaneRequest(r) {
		// Cosmos owns the API root, so a request the account-discovery route
		// does not recognise lands here rather than on the console redirect.
		// Hand it back to the server, which sends a browser at the bare origin
		// to the console; only when no console is registered is the root
		// genuinely nothing.
		if srv.ServeUIRoot(w, r) {
			return
		}
		http.NotFound(w, r)
		return
	}
	// Reading the account is a data-plane operation and carries the same
	// shared-key authorization as any other, over an empty resource type and an
	// empty resource link. It is authorized here rather than in the data-plane
	// middleware because the operator console shares this path, and only the
	// branch above can tell a console visitor from a Cosmos client.
	if !cosmosAuthorizeDataPlane(w, r) {
		return
	}
	endpoint := azureRequestScheme(r) + "://" + r.Host + "/"
	region := []map[string]any{{"name": "South Central US", "databaseAccountEndpoint": endpoint}}
	cosmosWriteData(w, http.StatusOK, map[string]any{
		"id":                           cosmosDataAccount(r),
		"writableLocations":            region,
		"readableLocations":            region,
		"enableMultipleWriteLocations": false,
		"userConsistencyPolicy":        map[string]any{"defaultConsistencyLevel": "Session"},
	})
}

func cosmosWriteData(w http.ResponseWriter, status int, v any) {
	cosmosWriteDataCharge(w, status, v, cosmosMetadataCharge)
}

// cosmosWriteDataCharge is the shared Cosmos data-plane response writer. Every
// data-plane response carries a realistic per-operation `x-ms-request-charge`
// (the RU cost): metadata reads default to a flat ~1 RU, while item and query
// handlers pass the size/result-scaled charge from the RU model in
// cosmos_throughput.go.
func cosmosWriteDataCharge(w http.ResponseWriter, status int, v any, charge float64) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-ms-activity-id", generateUUID())
	w.Header().Set("x-ms-request-charge", cosmosFormatCharge(charge))
	// Real Cosmos returns the resource ETag in the HTTP ETag header (the azcosmos
	// SDK reads it from there, not the body); surface it for any single-resource
	// response that carries an `_etag`.
	if m, ok := v.(map[string]any); ok {
		if et, ok := m["_etag"].(string); ok && et != "" {
			w.Header().Set("Etag", et)
		}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func cosmosDataError(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-ms-activity-id", generateUUID())
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message})
}

func cosmosARMIDNames(id string) (account, db, coll string, ok bool) {
	parts := strings.Split(id, "/")
	for i := 0; i < len(parts); i++ {
		if parts[i] == "databaseAccounts" && i+1 < len(parts) {
			account = parts[i+1]
		}
		if parts[i] == "sqlDatabases" && i+1 < len(parts) {
			db = parts[i+1]
		}
		if parts[i] == "containers" && i+1 < len(parts) {
			coll = parts[i+1]
		}
	}
	return account, db, coll, account != ""
}
