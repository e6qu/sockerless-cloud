package main

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Microsoft.DBforPostgreSQL/flexibleServers: server lifecycle and nested
// resources.

type PGFlexibleServer struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location,omitempty"`
	SKU        map[string]any    `json:"sku,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
}

type PGDatabase struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type PGFirewallRule struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

// Server-wide settings as a flat key/value list:
// id, name, properties{value, defaultValue, source, ...}.
type PGConfiguration struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
}

var (
	pgServers        sim.Store[PGFlexibleServer]
	pgDatabases      sim.Store[PGDatabase]
	pgFirewallRules  sim.Store[PGFirewallRule]
	pgConfigurations sim.Store[PGConfiguration]
)

func registerPGFlexibleServer(srv *sim.Server) {
	pgServers = sim.MakeStore[PGFlexibleServer](srv.DB(), "pg_servers")
	pgDatabases = sim.MakeStore[PGDatabase](srv.DB(), "pg_databases")
	pgFirewallRules = sim.MakeStore[PGFirewallRule](srv.DB(), "pg_firewall_rules")
	pgConfigurations = sim.MakeStore[PGConfiguration](srv.DB(), "pg_configurations")
	pgDataPlaneKeys = sim.MakeStore[pgDataPlaneKeyRecord](srv.DB(), "pg_dataplane_key")
	pgServerCredentials = sim.MakeStore[pgServerCredential](srv.DB(), "pg_server_credentials")

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers"

	srv.HandleFunc("PUT "+armBase+"/{name}", handlePGCreateServer)
	srv.HandleFunc("GET "+armBase+"/{name}", handlePGGetServer)
	srv.HandleFunc("DELETE "+armBase+"/{name}", handlePGDeleteServer)
	srv.HandleFunc("GET "+armBase, handlePGListServersByRG)

	srv.HandleFunc("PUT "+armBase+"/{name}/databases/{db}", handlePGCreateDatabase)
	srv.HandleFunc("GET "+armBase+"/{name}/databases/{db}", handlePGGetDatabase)
	srv.HandleFunc("DELETE "+armBase+"/{name}/databases/{db}", handlePGDeleteDatabase)
	srv.HandleFunc("GET "+armBase+"/{name}/databases", handlePGListDatabases)

	srv.HandleFunc("PUT "+armBase+"/{name}/firewallRules/{rule}", handlePGCreateFirewallRule)
	srv.HandleFunc("GET "+armBase+"/{name}/firewallRules/{rule}", handlePGGetFirewallRule)
	srv.HandleFunc("DELETE "+armBase+"/{name}/firewallRules/{rule}", handlePGDeleteFirewallRule)
	srv.HandleFunc("GET "+armBase+"/{name}/firewallRules", handlePGListFirewallRules)

	srv.HandleFunc("GET "+armBase+"/{name}/configurations", handlePGListConfigurations)
	srv.HandleFunc("GET "+armBase+"/{name}/configurations/{cfg}", handlePGGetConfiguration)
	srv.HandleFunc("PUT "+armBase+"/{name}/configurations/{cfg}", handlePGUpdateConfiguration)

	registerPGFlexibleServerMore(srv)

	// A persistent restart rebinds every credentialed server's listener,
	// re-registers its DNS name and re-adopts its engine. The API-only tier holds
	// no engines.
	if sim.RequireContainerRuntime("the Azure Database for PostgreSQL data plane") == nil {
		if err := azurePGRecoverDataPlanes(); err != nil {
			log.Fatalf("recover Azure Database for PostgreSQL data planes: %v", err)
		}
	}
}

func pgConfigKey(sub, rg, server, cfg string) string {
	return sub + "/" + rg + "/" + server + "/" + cfg
}

func handlePGListConfigurations(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	server := sim.PathParam(r, "name")
	all := pgConfigurations.Filter(func(c PGConfiguration) bool {
		return strings.HasPrefix(c.ID, pgServerID(sub, rg, server)+"/configurations/")
	})
	if all == nil {
		all = []PGConfiguration{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
}

func handlePGGetConfiguration(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	server := sim.PathParam(r, "name")
	cfgName := sim.PathParam(r, "cfg")
	c, ok := pgConfigurations.Get(pgConfigKey(sub, rg, server, cfgName))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Configuration %q not found on server %q", cfgName, server)
		return
	}
	sim.WriteJSON(w, http.StatusOK, c)
}

func handlePGUpdateConfiguration(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	server := sim.PathParam(r, "name")
	cfgName := sim.PathParam(r, "cfg")
	if _, ok := pgServers.Get(pgServerID(sub, rg, server)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Flexible server %q not found", server)
		return
	}
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	c := PGConfiguration{
		ID:         pgServerID(sub, rg, server) + "/configurations/" + cfgName,
		Name:       cfgName,
		Type:       "Microsoft.DBforPostgreSQL/flexibleServers/configurations",
		Properties: req.Properties,
	}
	pgConfigurations.Put(pgConfigKey(sub, rg, server, cfgName), c)
	sim.WriteJSON(w, http.StatusOK, c)
}

func pgServerID(sub, rg, name string) string {
	return armResourceID(sub, rg, "Microsoft.DBforPostgreSQL", "flexibleServers", name)
}

func handlePGCreateServer(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	var req PGFlexibleServer
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := pgServerID(sub, rg, name)
	// administratorLoginPassword is x-ms-secret: it seals into the credential
	// store and never lands in the stored properties.
	var adminPassword string
	if req.Properties != nil {
		adminPassword, _ = req.Properties["administratorLoginPassword"].(string)
		delete(req.Properties, "administratorLoginPassword")
	}
	s := PGFlexibleServer{
		ID:       id,
		Name:     name,
		Type:     "Microsoft.DBforPostgreSQL/flexibleServers",
		Location: req.Location,
		SKU:      req.SKU,
		Tags:     req.Tags,
		Properties: map[string]any{
			"state":                    "Starting",
			"version":                  "15",
			"fullyQualifiedDomainName": azureEndpointHostname(r, name, "postgres", "database"),
			"administratorLogin":       "psqladmin",
			"storage": map[string]any{
				"storageSizeGB": 32,
			},
			"backup": map[string]any{
				"backupRetentionDays": 7,
				"geoRedundantBackup":  "Disabled",
			},
		},
	}
	if req.Properties != nil {
		for k, v := range req.Properties {
			s.Properties[k] = v
		}
		s.Properties["state"] = "Starting"
	}

	// The source-based modes (PointInTimeRestore, GeoRestore, Replica) build the
	// server from another's data: it keeps the source's administrator login and
	// credential, and the clone runs through the create's own operation.
	createMode, _ := s.Properties["createMode"].(string)
	var restore *azurePGRestoreSource
	var cloneFailCode string
	var settleClone func()
	switch {
	case createMode == "" || strings.EqualFold(createMode, "Default") || strings.EqualFold(createMode, "Create"):
		// On an existing name Default is an update, which overwriting the stored record
		// already is.
	case strings.EqualFold(createMode, "Update"):
		// Azure Resource Manager refuses an Update of a server that does not exist.
		if _, exists := pgServers.Get(id); !exists {
			AzureErrorf(w, "InvalidParameterValue", http.StatusBadRequest,
				"createMode \"Update\" requires an existing server, and server %q does not exist", name)
			return
		}
	case strings.EqualFold(createMode, "PointInTimeRestore"):
		sourceSub, sourceRG, sourceName, ok := pgCreateSourceServer(w, s.Properties, rg, name)
		if !ok {
			return
		}
		pointInTimeRaw, _ := s.Properties["pointInTimeUTC"].(string)
		pointInTime, err := time.Parse(time.RFC3339Nano, pointInTimeRaw)
		if err != nil {
			AzureErrorf(w, "InvalidParameterValue", http.StatusBadRequest,
				"pointInTimeUTC %q is not an ISO8601 timestamp", pointInTimeRaw)
			return
		}
		picked := azurePGPickRestoreSource(sourceSub, sourceRG, sourceName, pointInTime)
		restore = &picked
		cloneFailCode = "RestoreFailed"
	case strings.EqualFold(createMode, "GeoRestore"):
		// Geo-restore takes the source's newest settled backup, or its live volume
		// when none has settled.
		sourceSub, sourceRG, sourceName, ok := pgCreateSourceServer(w, s.Properties, rg, name)
		if !ok {
			return
		}
		picked := azurePGPickRestoreSource(sourceSub, sourceRG, sourceName, time.Now().UTC())
		restore = &picked
		cloneFailCode = "GeoRestoreFailed"
	case strings.EqualFold(createMode, "Replica"):
		// A replica clones the primary's live volume and serves it under the source's
		// credential: Provisioning while the clone runs, then Active; the source
		// becomes the primary.
		sourceSub, sourceRG, sourceName, ok := pgCreateSourceServer(w, s.Properties, rg, name)
		if !ok {
			return
		}
		s.Properties["replicationRole"] = "AsyncReplica"
		s.Properties["replica"] = map[string]any{"role": "AsyncReplica", "replicationState": "Provisioning"}
		restore = &azurePGRestoreSource{sourceRG: sourceRG, sourceName: sourceName}
		cloneFailCode = "ReplicaProvisioningFailed"
		sourceServerID := pgServerID(sourceSub, sourceRG, sourceName)
		settleClone = func() {
			pgServers.Update(id, func(stored *PGFlexibleServer) {
				if stored.Properties == nil {
					stored.Properties = map[string]any{}
				}
				stored.Properties["replica"] = map[string]any{"role": "AsyncReplica", "replicationState": "Active"}
			})
			pgServers.Update(sourceServerID, func(stored *PGFlexibleServer) {
				if stored.Properties == nil {
					stored.Properties = map[string]any{}
				}
				stored.Properties["replicationRole"] = "Primary"
				replica, _ := stored.Properties["replica"].(map[string]any)
				if replica == nil {
					replica = map[string]any{}
				}
				replica["role"] = "Primary"
				stored.Properties["replica"] = replica
			})
		}
	case strings.EqualFold(createMode, "ReviveDropped"):
		// Deleting a server removes its volumes and backups, so nothing is left to
		// revive; a plain create would serve an empty impostor.
		AzureErrorf(w, "InvalidParameterValue", http.StatusBadRequest,
			"createMode \"ReviveDropped\" cannot be served: the simulator retains no dropped server to revive; deleted servers' volumes are removed on delete")
		return
	default:
		AzureErrorf(w, "InvalidParameterValue", http.StatusBadRequest,
			"createMode %q is not a creation mode the service defines", createMode)
		return
	}

	if restore == nil && adminPassword != "" {
		sealed, err := azurePGSealSecret(adminPassword)
		if err != nil {
			AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
				"seal administrator credential: %v", err)
			return
		}
		pgServerCredentials.Put(azurePGServerKey(rg, name), pgServerCredential{Sealed: sealed})
	}

	pgServers.Put(id, s)

	var opID string
	if restore != nil {
		// The create's operation completes once the clone is in place and the data
		// plane installs on it.
		source := *restore
		failCode := cloneFailCode
		settle := settleClone
		opID = startAzureAsyncOperationOutcome(func() *AsyncOperationError {
			if err := azurePGCloneForRestore(rg, name, source); err != nil {
				return &AsyncOperationError{Code: failCode, Message: err.Error()}
			}
			azurePGInstallOrExplain(sub, rg, name)
			pgServers.Update(id, func(stored *PGFlexibleServer) {
				if stored.Properties == nil {
					stored.Properties = map[string]any{}
				}
				stored.Properties["state"] = "Ready"
			})
			if settle != nil {
				settle()
			}
			return nil
		})
	} else {
		azurePGInstallOrExplain(sub, rg, name)
		opID = issueAzureAsyncOperation(func() {
			pgServers.Update(id, func(stored *PGFlexibleServer) {
				if stored.Properties == nil {
					stored.Properties = map[string]any{}
				}
				stored.Properties["state"] = "Ready"
			})
		})
	}
	// Servers_Create declares 202 only: the caller polls Azure-AsyncOperation, then
	// GETs the Location.
	pgWriteAsyncAccepted(w, r, sub, s.Location, opID)
}

// A restored server or replica takes the source's administrator login and
// sealed credential. It writes the error and reports ok=false for an
// unparseable ID or a missing source.
func pgCreateSourceServer(w http.ResponseWriter, props map[string]any, rg, name string) (sourceSub, sourceRG, sourceName string, ok bool) {
	sourceID, _ := props["sourceServerResourceId"].(string)
	sourceSub, sourceRG, sourceName, parsed := pgParseServerResourceID(sourceID)
	if !parsed {
		AzureErrorf(w, "InvalidParameterValue", http.StatusBadRequest,
			"sourceServerResourceId %q is not a flexible-server resource ID", sourceID)
		return "", "", "", false
	}
	source, found := pgServers.Get(pgServerID(sourceSub, sourceRG, sourceName))
	if !found {
		AzureErrorf(w, "InvalidParameterValue", http.StatusBadRequest,
			"source server %q not found", sourceID)
		return "", "", "", false
	}
	if sourceLogin, isString := source.Properties["administratorLogin"].(string); isString && sourceLogin != "" {
		props["administratorLogin"] = sourceLogin
	}
	if credential, found := pgServerCredentials.Get(azurePGServerKey(sourceRG, sourceName)); found {
		pgServerCredentials.Put(azurePGServerKey(rg, name), credential)
	}
	return sourceSub, sourceRG, sourceName, true
}

// The 2025-08-01 PUT and DELETE contract: 202, no body, Azure-AsyncOperation at
// the status poll and Location at the resource.
func pgWriteAsyncAccepted(w http.ResponseWriter, r *http.Request, sub, location, opID string) {
	opURL := azureAsyncOperationHeader(r, sub, "Microsoft.DBforPostgreSQL", location, "operationStatuses", opID, r.URL.Query().Get("api-version"))
	writeAzureAsyncCreateHeaders(w, opID, opURL, azureCurrentRequestURL(r))
	w.WriteHeader(http.StatusAccepted)
}

// The operationStatuses path is location-scoped.
func pgServerLocation(serverID string) string {
	if s, ok := pgServers.Get(serverID); ok && s.Location != "" {
		return s.Location
	}
	return "global"
}

func handlePGGetServer(w http.ResponseWriter, r *http.Request) {
	id := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	s, ok := pgServers.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found: %s", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, s)
}

func handlePGDeleteServer(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	id := pgServerID(sub, rg, name)
	location := pgServerLocation(id)
	s, existed := pgServers.Get(id)
	if !existed || !pgServers.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found: %s", id)
		return
	}
	// Stop the engine, close the listener, remove the volume, drop the credential
	// and unregister the DNS name.
	azurePGStopDataPlane(rg, name, true)
	pgServerCredentials.Delete(azurePGServerKey(rg, name))
	if fqdn, isString := s.Properties["fullyQualifiedDomainName"].(string); isString && fqdn != "" {
		UnregisterAzureDNSName(fqdn)
	}
	// Cascade-delete databases, firewall rules and backups, on-demand and
	// long-term, volumes included.
	prefix := id + "/"
	for _, d := range pgDatabases.List() {
		if strings.HasPrefix(d.ID, prefix) {
			pgDatabases.Delete(d.ID)
		}
	}
	for _, fr := range pgFirewallRules.List() {
		if strings.HasPrefix(fr.ID, prefix) {
			pgFirewallRules.Delete(fr.ID)
		}
	}
	for _, b := range pgBackups.List() {
		if strings.HasPrefix(b.ID, id+"/backups/") {
			pgBackups.Delete(b.ID)
			azurePGRemoveBackupVolume(azurePGBackupVolume(rg, name, b.Name))
		}
	}
	for _, b := range pgLtrBackups.List() {
		if strings.HasPrefix(b.ID, id+"/ltrBackupOperations/") {
			pgLtrBackups.Delete(b.ID)
			azurePGRemoveBackupVolume(azurePGLtrBackupVolume(rg, name, b.Name))
		}
	}
	pgWriteAsyncAccepted(w, r, sub, location, issueAzureAsyncOperation(nil))
}

func handlePGListServersByRG(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	prefix := armResourceID(sub, rg, "Microsoft.DBforPostgreSQL", "flexibleServers") + "/"
	var out []PGFlexibleServer
	for _, s := range pgServers.List() {
		if strings.HasPrefix(s.ID, prefix) {
			out = append(out, s)
		}
	}
	if out == nil {
		out = []PGFlexibleServer{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handlePGCreateDatabase(w http.ResponseWriter, r *http.Request) {
	parent := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := pgServers.Get(parent); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found")
		return
	}
	dbName := sim.PathParam(r, "db")
	var req PGDatabase
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := parent + "/databases/" + dbName
	d := PGDatabase{
		ID:   id,
		Name: dbName,
		Type: "Microsoft.DBforPostgreSQL/flexibleServers/databases",
		Properties: map[string]any{
			"charset":   "UTF8",
			"collation": "en_US.utf8",
		},
	}
	if req.Properties != nil {
		for k, v := range req.Properties {
			d.Properties[k] = v
		}
	}
	pgDatabases.Put(id, d)
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	serverName := sim.PathParam(r, "name")
	// A running engine gets the database now; a cold one at readiness.
	opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
		if err := azurePGEnsureDatabaseIfRunning(sub, rg, serverName, dbName); err != nil {
			return &AsyncOperationError{Code: "DatabaseCreateFailed", Message: err.Error()}
		}
		return nil
	})
	pgWriteAsyncAccepted(w, r, sub, pgServerLocation(parent), opID)
}

func handlePGGetDatabase(w http.ResponseWriter, r *http.Request) {
	id := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/databases/" + sim.PathParam(r, "db")
	d, ok := pgDatabases.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "database not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, d)
}

func handlePGDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	serverName := sim.PathParam(r, "name")
	parent := pgServerID(sub, rg, serverName)
	dbName := sim.PathParam(r, "db")
	id := parent + "/databases/" + dbName
	if !pgDatabases.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "database not found")
		return
	}
	// A running engine drops the database now; a cold one keeps the bytes, but
	// readiness reconciles only declared databases.
	opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
		if err := azurePGDropDatabaseIfRunning(sub, rg, serverName, dbName); err != nil {
			return &AsyncOperationError{Code: "DatabaseDeleteFailed", Message: err.Error()}
		}
		return nil
	})
	pgWriteAsyncAccepted(w, r, sub, pgServerLocation(parent), opID)
}

func handlePGListDatabases(w http.ResponseWriter, r *http.Request) {
	prefix := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/databases/"
	var out []PGDatabase
	for _, d := range pgDatabases.List() {
		if strings.HasPrefix(d.ID, prefix) {
			out = append(out, d)
		}
	}
	if out == nil {
		out = []PGDatabase{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handlePGCreateFirewallRule(w http.ResponseWriter, r *http.Request) {
	parent := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := pgServers.Get(parent); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found")
		return
	}
	ruleName := sim.PathParam(r, "rule")
	var req PGFirewallRule
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := parent + "/firewallRules/" + ruleName
	fr := PGFirewallRule{
		ID:         id,
		Name:       ruleName,
		Type:       "Microsoft.DBforPostgreSQL/flexibleServers/firewallRules",
		Properties: req.Properties,
	}
	pgFirewallRules.Put(id, fr)
	pgWriteAsyncAccepted(w, r, sim.PathParam(r, "subscriptionId"), pgServerLocation(parent), issueAzureAsyncOperation(nil))
}

func handlePGGetFirewallRule(w http.ResponseWriter, r *http.Request) {
	id := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/firewallRules/" + sim.PathParam(r, "rule")
	fr, ok := pgFirewallRules.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "firewall rule not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, fr)
}

func handlePGDeleteFirewallRule(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	parent := pgServerID(sub, sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	id := parent + "/firewallRules/" + sim.PathParam(r, "rule")
	if !pgFirewallRules.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "firewall rule not found")
		return
	}
	pgWriteAsyncAccepted(w, r, sub, pgServerLocation(parent), issueAzureAsyncOperation(nil))
}

func handlePGListFirewallRules(w http.ResponseWriter, r *http.Request) {
	prefix := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/firewallRules/"
	var out []PGFirewallRule
	for _, fr := range pgFirewallRules.List() {
		if strings.HasPrefix(fr.ID, prefix) {
			out = append(out, fr)
		}
	}
	if out == nil {
		out = []PGFirewallRule{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}
