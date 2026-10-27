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
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// cosmosETagSeq makes ETags unique within one wall-clock second, so If-Match
// tells two quick writes apart. It also orders the change feed, so startup
// raises it above every persisted sequence (cosmosRaiseETagFloor): a
// continuation issued before a SIM_PERSIST restart must never see the clock
// run backwards.
var cosmosETagSeq atomic.Uint64

func cosmosRaiseETagFloor(seq uint64) {
	for {
		cur := cosmosETagSeq.Load()
		if seq <= cur || cosmosETagSeq.CompareAndSwap(cur, seq) {
			return
		}
	}
}

// Azure Cosmos DB for NoSQL: the Microsoft.DocumentDB control plane and the
// SQL data plane.

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
	registerCosmosRIDs(srv)

	cosmosInitSessionState(srv)
	for _, d := range cosmosDocs.List() {
		cosmosRaiseETagFloor(cosmosETagSeqOf(d.ETag))
	}
	registerCosmosItems(srv)

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

	// The azcosmos SDK's global endpoint manager GETs the account root on its first
	// request and fails if it errors.
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
	srv.HandleFunc("PUT /dbs/{database}/colls/{container}", handleCosmosDataReplaceColl)
	srv.HandleFunc("DELETE /dbs/{database}/colls/{container}", handleCosmosDataDeleteColl)
	srv.HandleFunc("POST /dbs/{database}/colls/{container}/docs", cosmosMetered(handleCosmosDataCreateOrQueryDoc))
	srv.HandleFunc("GET /dbs/{database}/colls/{container}/docs", cosmosMetered(handleCosmosDataListDocs))
	srv.HandleFunc("GET /dbs/{database}/colls/{container}/docs/{doc}", cosmosMetered(handleCosmosDataGetDoc))
	srv.HandleFunc("PUT /dbs/{database}/colls/{container}/docs/{doc}", cosmosMetered(handleCosmosDataReplaceDoc))
	srv.HandleFunc("PATCH /dbs/{database}/colls/{container}/docs/{doc}", cosmosMetered(handleCosmosDataPatchDoc))
	srv.HandleFunc("DELETE /dbs/{database}/colls/{container}/docs/{doc}", cosmosMetered(handleCosmosDataDeleteDoc))

	registerCosmosThroughput(srv)
}

func cosmosAccountID(sub, rg, account string) string {
	return armResourceID(sub, rg, "Microsoft.DocumentDB", "databaseAccounts", account)
}

func cosmosSQLDatabaseID(sub, rg, account, database string) string {
	return armChildID(cosmosAccountID(sub, rg, account), "sqlDatabases", database)
}

func cosmosSQLContainerID(sub, rg, account, database, container string) string {
	return armChildID(cosmosSQLDatabaseID(sub, rg, account, database), "containers", container)
}

func cosmosTableID(sub, rg, account, table string) string {
	return armChildID(cosmosAccountID(sub, rg, account), "tables", table)
}

func cosmosDocKey(account, database, container, doc string) string {
	return account + "/" + database + "/" + container + "/" + doc
}

// cosmosDataAccount reads the account from the host the client dialled. Each
// account has a hostname of its own — the documentEndpoint — and no other way
// to be named, which is why account names are globally unique. A client
// dialling by address names the account in its Host header, as through a
// proxy; a host naming no account resolves to none, and the data plane's
// authorization refuses it.
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
	// An account name is a hostname (`<name>.documents.azure.com`), so it is
	// global, as DatabaseAccounts_CheckNameExists reports. A PUT to the same
	// resource ID is an update; a different ID claiming a taken name is refused.
	// The vendored swagger declares no error response here, so the 409's code
	// follows the nearest captured refusal of a global name, Azure Cache for
	// Redis's `NameNotAvailable`.
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

// Azure returns the default Session policy when the create body omits it.
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

// cosmosNormalizeLocations rebuilds the geo-replication arrays the
// databaseAccounts GET returns from properties.locations:
//   - terraform-provider-azurerm builds `geo_location` from failoverPolicies,
//     looking zone_redundant up in locations by a shared id; without
//     failoverPolicies it plans to add the block on every refresh.
//   - its create poll dereferences provisioningState on every
//     read/writeLocations entry, so each must carry one.
//
// Each region's id, `{account}-{location-normalized}`, is shared between
// failoverPolicies and locations.
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
	prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.DocumentDB", "databaseAccounts") + "/"
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

// DatabaseAccountListReadOnlyKeysResult carries only the two read-only keys.
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
	cosmosStoreThroughputFromProps(props, cosmosTableID(sub, rg, account, table), "Microsoft.DocumentDB/databaseAccounts/tables")
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
	cosmosStoreThroughputFromProps(db.Properties, id, db.Type)
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
			cosmosThroughputs.Delete(c.ID + "/throughputSettings/default")
		}
	}
	cosmosThroughputs.Delete(id + "/throughputSettings/default")
	cosmosDropItems(cosmosDataDBKey(account, database) + "/")
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
	// SqlContainerGetProperties nests partitionKey under properties.resource.
	if res, ok := c.Properties["resource"].(map[string]any); ok && res["partitionKey"] == nil {
		res["partitionKey"] = map[string]any{"paths": []string{"/id"}, "kind": "Hash"}
	}
	cosmosStoreThroughputFromProps(c.Properties, id, c.Type)
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
	id := cosmosSQLContainerID(sub, rg, account, database, container)
	if !cosmosContainers.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos SQL container not found: %s", container)
		return
	}
	cosmosThroughputs.Delete(id + "/throughputSettings/default")
	release := cosmosLockColl(account, database, container)
	defer release()
	cosmosForgetContainer(account, database, container)
	cosmosDropItems(cosmosDataCollKey(account, database, container) + "/")
	w.WriteHeader(http.StatusNoContent)
}

func handleCosmosGetThroughput(w http.ResponseWriter, r *http.Request) {
	t, ok := cosmosThroughputs.Get(cosmosThroughputID(r))
	if !ok {
		cosmosThroughputNotFound(w, r.URL.Path)
		return
	}
	sim.WriteJSON(w, http.StatusOK, t)
}

// A resource without dedicated throughput has no offer, and the resource
// provider answers 404, which the azurerm provider reads as "no throughput".
func cosmosThroughputNotFound(w http.ResponseWriter, id string) {
	AzureErrorf(w, "NotFound", http.StatusNotFound, "Throughput settings %s do not exist; the resource has no dedicated throughput.", id)
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
	resource, ok := req.Properties["resource"].(map[string]any)
	if !ok {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid throughput body: properties.resource is required")
		return
	}
	// A resource sharing its database's or account's throughput has no offer to
	// update; the azurerm provider reports that throughput cannot be configured
	// after creation.
	if _, provisioned := cosmosThroughputs.Get(id); !provisioned {
		cosmosThroughputNotFound(w, id)
		return
	}
	for _, readOnly := range []string{"_rid", "_ts", "_etag", "minimumThroughput", "offerReplacePending"} {
		delete(resource, readOnly)
	}
	cosmosStampThroughput(id, resource)
	cosmosThroughputs.Put(id, req)
	sim.WriteJSON(w, http.StatusOK, req)
}

func cosmosARMParts(r *http.Request) (string, string, string, string) {
	return sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account"), sim.PathParam(r, "database")
}

// The throughputSettings/default ID is the request path, so one pair of
// handlers serves every family.
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
	// Record the database, so the read that follows finds it.
	if err := cosmosProvisionThroughputFromHeaders(r, account, id, ""); err != nil {
		cosmosDataError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	cosmosDataDBs.Put(cosmosDataDBKey(account, id), CosmosDataDB{Account: account, DB: id})
	cosmosWriteData(w, http.StatusCreated, cosmosDataDB(account, id))
}

func handleCosmosDataListDBs(w http.ResponseWriter, r *http.Request) {
	account := cosmosDataAccount(r)
	dbs := map[string]map[string]any{}
	for _, d := range cosmosDataDBs.ListPrefix(account + "/") {
		dbs[d.Item.DB] = cosmosDataDB(account, d.Item.DB)
	}
	for _, d := range cosmosDatabases.List() {
		if acc, db, _, ok := cosmosARMIDNames(d.ID); ok && acc == account && db != "" {
			dbs[db] = cosmosDataDB(account, db)
		}
	}
	for _, c := range cosmosContainers.List() {
		if acc, db, _, ok := cosmosARMIDNames(c.ID); ok && acc == account {
			dbs[db] = cosmosDataDB(account, db)
		}
	}
	for _, c := range cosmosDataColls.ListPrefix(account + "/") {
		dbs[c.Item.DB] = cosmosDataDB(account, c.Item.DB)
	}
	for _, d := range cosmosDocs.ListPrefix(account + "/") {
		dbs[d.Item.DB] = cosmosDataDB(account, d.Item.DB)
	}
	items := make([]map[string]any, 0, len(dbs))
	for _, db := range dbs {
		items = append(items, db)
	}
	cosmosWriteData(w, http.StatusOK, map[string]any{"Databases": items, "_count": len(items)})
}

// cosmosDataDBExists uses the listing's derivation — a database exists once a
// container or document is under it — so a read and a list agree.
func cosmosDataDBExists(account, db string) bool {
	if _, created := cosmosDataDBs.Get(cosmosDataDBKey(account, db)); created {
		return true
	}
	under := cosmosDataDBKey(account, db) + "/"
	return cosmosARMDatabaseExists(account, db) ||
		cosmosARMDatabaseHasContainers(account, db) ||
		len(cosmosDataColls.ListPrefix(under)) > 0 ||
		len(cosmosDocs.ListPrefix(under)) > 0
}

// cosmosARMDatabaseExists reports a NoSQL database Azure Resource Manager
// created, which the data plane serves at once.
func cosmosARMDatabaseExists(account, db string) bool {
	for _, d := range cosmosDatabases.List() {
		if acc, name, coll, ok := cosmosARMIDNames(d.ID); ok && acc == account && name == db && coll == "" {
			return true
		}
	}
	return false
}

func handleCosmosDataGetDB(w http.ResponseWriter, r *http.Request) {
	account, db := cosmosDataAccount(r), sim.PathParam(r, "database")
	// A database nobody created does not exist.
	if !cosmosDataDBExists(account, db) {
		cosmosDataError(w, "NotFound", "Owner resource does not exist", http.StatusNotFound)
		return
	}
	cosmosWriteData(w, http.StatusOK, cosmosDataDB(account, db))
}

func handleCosmosDataDeleteDB(w http.ResponseWriter, r *http.Request) {
	account, db := cosmosDataAccount(r), sim.PathParam(r, "database")
	cosmosDataDBs.Delete(cosmosDataDBKey(account, db))
	under := cosmosDataDBKey(account, db) + "/"
	for _, c := range cosmosDataColls.ListPrefix(under) {
		cosmosForgetContainer(account, db, c.Item.Coll)
	}
	cosmosForgetSQLThroughput(account, db, "")
	cosmosDropItems(under)
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
	// The SDK declares the partition-key path here, not through Azure Resource
	// Manager; the data plane scopes items by (partition key, id).
	pkPath := cosmosCollPKPath(body)
	defaultTTL, ok := cosmosCollDefaultTTL(body)
	if !ok {
		cosmosDataError(w, "BadRequest", "The value of defaultTtl must be -1 or a positive integer.", http.StatusBadRequest)
		return
	}
	if err := cosmosProvisionThroughputFromHeaders(r, account, db, id); err != nil {
		cosmosDataError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	collection := CosmosDataColl{Account: account, DB: db, Coll: id, PKPath: pkPath, DefaultTTL: defaultTTL}
	cosmosDataColls.Put(cosmosDataCollKey(account, db, id), collection)
	cosmosWriteData(w, http.StatusCreated, cosmosDataColl(account, db, id))
}

func handleCosmosDataListColls(w http.ResponseWriter, r *http.Request) {
	account, db := cosmosDataAccount(r), sim.PathParam(r, "database")
	colls := map[string]map[string]any{}
	under := cosmosDataDBKey(account, db) + "/"
	for _, c := range cosmosDataColls.ListPrefix(under) {
		colls[c.Item.Coll] = cosmosDataColl(account, db, c.Item.Coll)
	}
	for _, d := range cosmosDocs.ListPrefix(under) {
		colls[d.Item.Coll] = cosmosDataColl(account, db, d.Item.Coll)
	}
	items := make([]map[string]any, 0, len(colls))
	for _, c := range colls {
		items = append(items, c)
	}
	cosmosWriteData(w, http.StatusOK, map[string]any{"DocumentCollections": items, "_count": len(items)})
}

func handleCosmosDataGetColl(w http.ResponseWriter, r *http.Request) {
	account, db, coll := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container")
	// A container created here and one created through the management plane are
	// the same container to a data-plane client.
	_, created := cosmosDataColls.Get(cosmosDataCollKey(account, db, coll))
	if !created {
		_, created = cosmosARMContainer(account, db, coll)
	}
	if !created {
		cosmosDataError(w, "NotFound", "Owner resource does not exist", http.StatusNotFound)
		return
	}
	cosmosWriteData(w, http.StatusOK, cosmosDataColl(account, db, coll))
}

func cosmosCollPKPath(body map[string]any) string {
	pk, _ := body["partitionKey"].(map[string]any)
	paths, _ := pk["paths"].([]any)
	if len(paths) == 0 {
		return ""
	}
	path, _ := paths[0].(string)
	return path
}

// cosmosCollDefaultTTL returns nil when time to live is off, and false when the
// value is neither -1 nor a positive integer.
func cosmosCollDefaultTTL(body map[string]any) (*int64, bool) {
	raw, present := body["defaultTtl"]
	if !present || raw == nil {
		return nil, true
	}
	ttl, ok := cosmosNumberOf(raw)
	if !ok || ttl == 0 || ttl < -1 || ttl != float64(int64(ttl)) {
		return nil, false
	}
	seconds := int64(ttl)
	return &seconds, true
}

// handleCosmosDataReplaceColl serves ReplaceContainer: the body replaces the
// settings, so an omitted defaultTtl turns time to live off; the partition key
// is fixed at create.
func handleCosmosDataReplaceColl(w http.ResponseWriter, r *http.Request) {
	account, db, coll := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container")
	var body map[string]any
	if err := sim.ReadJSON(r, &body); err != nil {
		cosmosDataError(w, "BadRequest", "invalid collection body", http.StatusBadRequest)
		return
	}
	defaultTTL, ok := cosmosCollDefaultTTL(body)
	if !ok {
		cosmosDataError(w, "BadRequest", "The value of defaultTtl must be -1 or a positive integer.", http.StatusBadRequest)
		return
	}
	release := cosmosLockColl(account, db, coll)
	defer release()
	key := cosmosDataCollKey(account, db, coll)
	dataColl, fromDataPlane := cosmosDataColls.Get(key)
	armColl, fromARM := cosmosARMContainer(account, db, coll)
	if !fromDataPlane && !fromARM {
		cosmosDataError(w, "NotFound", "Owner resource does not exist", http.StatusNotFound)
		return
	}
	current, _ := cosmosContainerPKPath(account, db, coll)
	if requested := cosmosCollPKPath(body); requested != "" && requested != current {
		cosmosDataError(w, "BadRequest",
			fmt.Sprintf("The partition key of a container cannot be changed: it is %q, and the request declares %q.", current, requested),
			http.StatusBadRequest)
		return
	}
	if fromDataPlane {
		dataColl.DefaultTTL = defaultTTL
		cosmosDataColls.Put(key, dataColl)
	}
	if fromARM {
		if armColl.Properties == nil {
			armColl.Properties = map[string]any{}
		}
		resource, _ := armColl.Properties["resource"].(map[string]any)
		if resource == nil {
			resource = map[string]any{}
			armColl.Properties["resource"] = resource
		}
		if defaultTTL == nil {
			delete(resource, "defaultTtl")
		} else {
			resource["defaultTtl"] = float64(*defaultTTL)
		}
		cosmosContainers.Put(armColl.ID, armColl)
	}
	cosmosWriteData(w, http.StatusOK, cosmosDataColl(account, db, coll))
}

func handleCosmosDataDeleteColl(w http.ResponseWriter, r *http.Request) {
	account, db, coll := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container")
	release := cosmosLockColl(account, db, coll)
	defer release()
	cosmosForgetContainer(account, db, coll)
	cosmosDropItems(cosmosDataCollKey(account, db, coll) + "/")
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
		id = sim.NewUUID()
		body["id"] = id
	}
	pkComponent, werr := cosmosResolvePKForWrite(r, account, db, coll, body)
	if werr != nil {
		cosmosDataError(w, werr.code, werr.msg, werr.status)
		return
	}
	upsert := strings.EqualFold(r.Header.Get("x-ms-documentdb-is-upsert"), "true")
	key := cosmosDocKeyPK(account, db, coll, pkComponent, id)
	release := cosmosLockColl(account, db, coll)
	defer release()
	existing, exists := cosmosLiveDoc(key)
	if exists && !upsert {
		// A plain create of an existing id is a 409 Conflict.
		cosmosDataError(w, "Conflict",
			"Resource with specified id or name already exists.", http.StatusConflict)
		return
	}
	if exists && upsert {
		// Upsert of an existing id honors an If-Match precondition like Replace.
		if !cosmosIfMatch(r, existing.ETag) {
			cosmosPreconditionFailed(w)
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
	// An `A-IM: Incremental feed` read switches the route into change-feed mode.
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

// cosmosResolvePointDoc reads by partition-scoped store key when the SDK sends
// the partition-key header, and scans by id within the collection without it.
func cosmosResolvePointDoc(r *http.Request, account, db, coll, docID string) (CosmosDocument, string, bool, *cosmosWriteError) {
	pkComponent, hasHeader, werr := cosmosResolvePKForPoint(r, account, db, coll)
	if werr != nil {
		return CosmosDocument{}, "", false, werr
	}
	if hasHeader {
		key := cosmosDocKeyPK(account, db, coll, pkComponent, docID)
		doc, ok := cosmosLiveDoc(key)
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
	release := cosmosLockColl(account, db, coll)
	defer release()
	existing, ok := cosmosLiveDoc(key)
	if !ok {
		cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
		return
	}
	if !cosmosIfMatch(r, existing.ETag) {
		cosmosPreconditionFailed(w)
		return
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
	release := cosmosLockColl(account, db, coll)
	defer release()
	existing, key, ok, werr := cosmosResolvePointDoc(r, account, db, coll, docID)
	if werr != nil {
		cosmosDataError(w, werr.code, werr.msg, werr.status)
		return
	}
	if !ok {
		cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
		return
	}
	if !cosmosIfMatch(r, existing.ETag) {
		cosmosPreconditionFailed(w)
		return
	}
	cosmosDeleteDoc(existing, key)
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
	if err := azureReadRequiredJSON(r, &req); err != nil {
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

	// A pk header scopes the query to one partition even when the SDK sets
	// enablecrosspartition=true, as it does by default.
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

	// A COUNT aggregate is a single row and never paged.
	start, end, continuation, err := listq.Window(listq.Base64Decimal, len(out), r.Header.Get("x-ms-continuation"), cosmosMaxItemCount(r), 0, 0)
	if err != nil {
		cosmosDataError(w, "BadRequest", "invalid continuation token", http.StatusBadRequest)
		return
	}
	page := out
	if plan.countAll {
		continuation = ""
	} else {
		page = out[start:end]
	}
	if continuation != "" {
		w.Header().Set("x-ms-continuation", continuation)
	}
	w.Header().Set("x-ms-item-count", fmt.Sprintf("%d", len(page)))
	cosmosWriteDataCharge(w, http.StatusOK, map[string]any{"Documents": page, "_count": len(page)}, cosmosQueryCharge(len(page)))
}

// handleCosmosDataPatchDoc applies a `{operations:[{op,path,value}]}` partial
// document patch: op is set, add, replace, remove or incr; path is `/a/b`.
func handleCosmosDataPatchDoc(w http.ResponseWriter, r *http.Request) {
	account, db, coll, docID := cosmosDataAccount(r), sim.PathParam(r, "database"), sim.PathParam(r, "container"), sim.PathParam(r, "doc")
	if cosmosGuardConsistency(w, r, account) {
		return
	}
	release := cosmosLockColl(account, db, coll)
	defer release()
	existing, key, ok, werr := cosmosResolvePointDoc(r, account, db, coll, docID)
	if werr != nil {
		cosmosDataError(w, werr.code, werr.msg, werr.status)
		return
	}
	if !ok {
		cosmosDataError(w, "NotFound", "Entity with the specified id does not exist", http.StatusNotFound)
		return
	}
	if !cosmosIfMatch(r, existing.ETag) {
		cosmosPreconditionFailed(w)
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
	// Patch a deep copy so a mid-list failure leaves no partial write.
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

func cosmosApplyPatchOp(body map[string]any, op, path string, value any) error {
	segs := cosmosSplitPatchPath(path)
	if len(segs) == 0 {
		return fmt.Errorf("patch op %q has an empty path", op)
	}
	parent := body
	for _, seg := range segs[:len(segs)-1] {
		next, ok := parent[seg].(map[string]any)
		if !ok {
			// set and add create intermediate objects; the others fail on a missing path.
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

func cosmosDataDB(account, id string) map[string]any {
	rid := cosmosDatabaseRID(account, id)
	return map[string]any{"id": id, "_rid": rid, "_self": "dbs/" + rid + "/", "_etag": `"db"`, "_ts": time.Now().UTC().Unix()}
}

func cosmosDataColl(account, db, id string) map[string]any {
	dbRID, collRID := cosmosDatabaseRID(account, db), cosmosCollectionRID(account, db, id)
	coll := map[string]any{"id": id, "_rid": collRID, "_self": "dbs/" + dbRID + "/colls/" + collRID + "/", "_etag": `"coll"`, "_ts": time.Now().UTC().Unix()}
	if path, declared := cosmosContainerPKPath(account, db, id); declared {
		coll["partitionKey"] = map[string]any{"paths": []string{path}, "kind": "Hash"}
	}
	if ttl := cosmosContainerTTLOf(account, db, id); ttl.enabled {
		coll["defaultTtl"] = ttl.seconds
	}
	return coll
}

// cosmosIsDataPlaneRequest uses Cosmos-specific signals — the master-key
// Authorization (`type=master`, URL-encoded by azcosmos), a documentdb header,
// or a host naming an account — never the bare `x-ms-version`, which storage
// requests also send.
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

// The single read/write region echoes the client's own endpoint, so the SDK
// keeps routing to the simulator.
func handleCosmosAccountProperties(srv *sim.Server, w http.ResponseWriter, r *http.Request) {
	if !cosmosIsDataPlaneRequest(r) {
		// Cosmos owns the API root; hand an unrecognized request back to the server,
		// which sends a browser to the console.
		if srv.ServeUIRoot(w, r) {
			return
		}
		http.NotFound(w, r)
		return
	}
	// Reading the account carries the same shared-key authorization as any other
	// data-plane read, over an empty resource type and link. It is checked here,
	// not in the middleware, because only the branch above can tell a console
	// visitor from a Cosmos client.
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

// cosmosWriteDataCharge stamps `x-ms-request-charge`: about 1 RU for a
// metadata read, and the RU model's charge (cosmos_throughput.go) for items
// and queries.
func cosmosWriteDataCharge(w http.ResponseWriter, status int, v any, charge float64) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-ms-activity-id", sim.NewUUID())
	w.Header().Set("x-ms-request-charge", cosmosFormatCharge(charge))
	// azcosmos reads the ETag from the header, not the body.
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
	w.Header().Set("x-ms-activity-id", sim.NewUUID())
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
