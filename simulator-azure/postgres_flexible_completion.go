package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Completion of the Microsoft.DBforPostgreSQL/flexibleServers control plane:
// provider-level operations metadata, location- and server-scoped capability
// catalogs, captured log files, quota usages, virtual-network subnet usage,
// the private-DNS-zone suffix, long-term-retention backups, server threat
// protection, performance tuning options, cross-server migrations, and the
// private endpoint connection / private link resource surfaces.

// PGMigration mirrors armpostgresqlflexibleservers.MigrationResource.
type PGMigration struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
}

// PGThreatProtection mirrors armpostgresqlflexibleservers.ServerThreatProtectionSettingsModel.
type PGThreatProtection struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

// PGPrivateEndpointConnection mirrors the common-types PrivateEndpointConnection.
type PGPrivateEndpointConnection struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

// PGLtrBackup mirrors the wire's BackupsLongTermRetentionOperation proxy
// resource: one long-term-retention backup operation, created by
// startLtrBackup and read back through ltrBackupOperations.
type PGLtrBackup struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

var (
	pgMigrations         sim.Store[PGMigration]
	pgThreatProtections  sim.Store[PGThreatProtection]
	pgPrivateEndpointCxn sim.Store[PGPrivateEndpointConnection]
	pgLtrBackups         sim.Store[PGLtrBackup]
)

func registerPGFlexibleServerCompletion(srv *sim.Server) {
	pgMigrations = sim.MakeStore[PGMigration](srv.DB(), "pg_migrations")
	pgThreatProtections = sim.MakeStore[PGThreatProtection](srv.DB(), "pg_threat_protections")
	pgPrivateEndpointCxn = sim.MakeStore[PGPrivateEndpointConnection](srv.DB(), "pg_private_endpoint_connections")
	pgLtrBackups = sim.MakeStore[PGLtrBackup](srv.DB(), "pg_ltr_backups")

	// A long-term-retention capture completes via an in-process goroutine, so
	// a persisted record still Running after a restart can never finish — its
	// goroutine died with the previous process. Mark it Failed, mirroring the
	// operation-status sweep in arm_lro.go for the poll the client holds.
	for _, b := range pgLtrBackups.List() {
		if status, _ := b.Properties["status"].(string); status != "Running" {
			continue
		}
		pgLtrBackups.Update(b.ID, func(stale *PGLtrBackup) {
			if status, _ := stale.Properties["status"].(string); status != "Running" {
				return
			}
			stale.Properties["status"] = "Failed"
			stale.Properties["endTime"] = time.Now().UTC().Format(time.RFC3339Nano)
			stale.Properties["errorCode"] = "OperationInterrupted"
			stale.Properties["errorMessage"] = "The backup was interrupted by a service restart before it completed."
		})
	}

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers"
	const subProvider = "/subscriptions/{subscriptionId}/providers/Microsoft.DBforPostgreSQL"

	// Provider / location-scoped surfaces. Action verbs not in the path
	// normalization allowlist register at the SDK's casing.
	srv.HandleFunc("GET /providers/Microsoft.DBforPostgreSQL/operations", handlePGOperationsList)
	srv.HandleFunc("POST /providers/Microsoft.DBforPostgreSQL/getPrivateDnsZoneSuffix", handlePGPrivateDNSZoneSuffix)
	srv.HandleFunc("GET "+subProvider+"/locations/{locationName}/capabilities", handlePGCapabilitiesByLocation)
	srv.HandleFunc("GET "+subProvider+"/locations/{locationName}/resourceType/flexibleServers/usages", handlePGQuotaUsages)
	srv.HandleFunc("POST "+subProvider+"/locations/{locationName}/checkVirtualNetworkSubnetUsage", handlePGVirtualNetworkSubnetUsage)

	// Server-scoped read surfaces.
	srv.HandleFunc("GET "+armBase+"/{name}/capabilities", handlePGCapabilitiesByServer)
	srv.HandleFunc("GET "+armBase+"/{name}/logFiles", handlePGLogFiles)

	// Long-term-retention backups.
	srv.HandleFunc("GET "+armBase+"/{name}/ltrBackupOperations", handlePGLtrBackupList)
	srv.HandleFunc("GET "+armBase+"/{name}/ltrBackupOperations/{backupName}", handlePGLtrBackupGet)
	srv.HandleFunc("POST "+armBase+"/{name}/startLtrBackup", handlePGStartLtrBackup)
	srv.HandleFunc("POST "+armBase+"/{name}/ltrPreBackup", handlePGLtrPreBackup)

	// Server threat protection (advancedThreatProtectionSettings).
	srv.HandleFunc("GET "+armBase+"/{name}/advancedThreatProtectionSettings", handlePGThreatProtectionList)
	srv.HandleFunc("GET "+armBase+"/{name}/advancedThreatProtectionSettings/{threatProtectionName}", handlePGThreatProtectionGet)
	srv.HandleFunc("PUT "+armBase+"/{name}/advancedThreatProtectionSettings/{threatProtectionName}", handlePGThreatProtectionPut)

	// Performance tuning options.
	srv.HandleFunc("GET "+armBase+"/{name}/tuningOptions", handlePGTuningOptionsList)
	srv.HandleFunc("GET "+armBase+"/{name}/tuningOptions/{tuningOption}", handlePGTuningOptionGet)
	srv.HandleFunc("GET "+armBase+"/{name}/tuningOptions/{tuningOption}/recommendations", handlePGTuningRecommendations)

	// Migrations.
	srv.HandleFunc("PUT "+armBase+"/{name}/migrations/{migrationName}", handlePGMigrationCreate)
	srv.HandleFunc("GET "+armBase+"/{name}/migrations/{migrationName}", handlePGMigrationGet)
	srv.HandleFunc("GET "+armBase+"/{name}/migrations", handlePGMigrationList)
	srv.HandleFunc("PATCH "+armBase+"/{name}/migrations/{migrationName}", handlePGMigrationUpdate)
	srv.HandleFunc("DELETE "+armBase+"/{name}/migrations/{migrationName}", handlePGMigrationCancel)
	srv.HandleFunc("POST "+armBase+"/{name}/checkMigrationNameAvailability", handlePGCheckMigrationNameAvailability)

	// Private endpoint connections + private link resources.
	srv.HandleFunc("GET "+armBase+"/{name}/privateEndpointConnections", handlePGPrivateEndpointConnectionList)
	srv.HandleFunc("GET "+armBase+"/{name}/privateEndpointConnections/{privateEndpointConnectionName}", handlePGPrivateEndpointConnectionGet)
	srv.HandleFunc("PUT "+armBase+"/{name}/privateEndpointConnections/{privateEndpointConnectionName}", handlePGPrivateEndpointConnectionPut)
	srv.HandleFunc("DELETE "+armBase+"/{name}/privateEndpointConnections/{privateEndpointConnectionName}", handlePGPrivateEndpointConnectionDelete)
	srv.HandleFunc("GET "+armBase+"/{name}/privateLinkResources", handlePGPrivateLinkResourceList)
	srv.HandleFunc("GET "+armBase+"/{name}/privateLinkResources/{groupName}", handlePGPrivateLinkResourceGet)
}

func pgServerFromReq(r *http.Request) (string, bool) {
	id := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	_, ok := pgServers.Get(id)
	return id, ok
}

func pgRequireServer(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := pgServerFromReq(r)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found: %s", id)
	}
	return id, ok
}

func handlePGOperationsList(w http.ResponseWriter, _ *http.Request) {
	op := func(name, resource, operation string) map[string]any {
		return map[string]any{
			"name":         name,
			"isDataAction": false,
			"origin":       "system",
			"display": map[string]any{
				"provider":    "Microsoft DB for PostgreSQL",
				"resource":    resource,
				"operation":   operation,
				"description": operation,
			},
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []map[string]any{
		op("Microsoft.DBforPostgreSQL/flexibleServers/read", "Flexible Servers", "Get Flexible Server"),
		op("Microsoft.DBforPostgreSQL/flexibleServers/write", "Flexible Servers", "Create or Update Flexible Server"),
		op("Microsoft.DBforPostgreSQL/flexibleServers/delete", "Flexible Servers", "Delete Flexible Server"),
	}})
}

func handlePGPrivateDNSZoneSuffix(w http.ResponseWriter, _ *http.Request) {
	sim.WriteJSON(w, http.StatusOK, "private.postgres.database.azure.com")
}

func pgCapabilityList(location string) map[string]any {
	return map[string]any{"value": []map[string]any{{
		"name":                       location,
		"status":                     "Available",
		"zoneRedundantHaSupported":   "Enabled",
		"geoBackupSupported":         "Enabled",
		"fastProvisioningSupported":  "Enabled",
		"onlineResizeSupported":      "Enabled",
		"storageAutoGrowthSupported": "Enabled",
	}}}
}

func handlePGCapabilitiesByLocation(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, pgCapabilityList(sim.PathParam(r, "locationName")))
}

func handlePGCapabilitiesByServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, pgCapabilityList(pgServerLocation(id)))
}

func handlePGLogFiles(w http.ResponseWriter, r *http.Request) {
	if _, ok := pgRequireServer(w, r); !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
}

func handlePGQuotaUsages(w http.ResponseWriter, r *http.Request) {
	loc := sim.PathParam(r, "locationName")
	sub := sim.PathParam(r, "subscriptionId")
	usage := func(name string, current, limit float64) map[string]any {
		return map[string]any{
			"id":           "/subscriptions/" + sub + "/providers/Microsoft.DBforPostgreSQL/locations/" + loc + "/resourceType/flexibleServers/usages/" + name,
			"name":         map[string]any{"value": name, "localizedValue": name},
			"currentValue": current,
			"limit":        limit,
			"unit":         "Count",
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []map[string]any{
		usage("ServersPerSubscription", 1, 100),
		usage("vCoresPerSubscription", 2, 1000),
	}})
}

func handlePGVirtualNetworkSubnetUsage(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"location":              sim.PathParam(r, "locationName"),
		"subscriptionId":        sim.PathParam(r, "subscriptionId"),
		"delegatedSubnetsUsage": []any{},
	})
}

// pgLtrCaptureContainers is the numberOfContainers ltrPreBackup reports: the
// capture writes the server's single data volume into a single backup volume,
// so one storage container is what the backup will use, however large the
// database.
const pgLtrCaptureContainers = 1

// pgLtrBackupRequest is the wire shape shared by startLtrBackup
// (BackupsLongTermRetentionRequest) and ltrPreBackup (LtrPreBackupRequest):
// backupSettings names the backup; targetDetails — startLtrBackup only —
// carries the SAS URIs of the storage containers the backup streams into.
type pgLtrBackupRequest struct {
	BackupSettings *struct {
		BackupName string `json:"backupName"`
	} `json:"backupSettings"`
	TargetDetails *struct {
		SasURIList []string `json:"sasUriList"`
	} `json:"targetDetails"`
}

func pgLtrBackupID(serverID, backupName string) string {
	return serverID + "/ltrBackupOperations/" + backupName
}

func handlePGLtrBackupList(w http.ResponseWriter, r *http.Request) {
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	prefix := id + "/ltrBackupOperations/"
	out := pgLtrBackups.Filter(func(b PGLtrBackup) bool { return strings.HasPrefix(b.ID, prefix) })
	if out == nil {
		out = []PGLtrBackup{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handlePGLtrBackupGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	backupName := sim.PathParam(r, "backupName")
	b, found := pgLtrBackups.Get(pgLtrBackupID(id, backupName))
	if !found {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"long-term-retention backup %q not found on server", backupName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, b)
}

func handlePGStartLtrBackup(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	serverName := sim.PathParam(r, "name")
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	var req pgLtrBackupRequest
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if req.BackupSettings == nil || req.BackupSettings.BackupName == "" {
		sim.AzureErrorf(w, "InvalidParameterValue", http.StatusBadRequest,
			"backupSettings.backupName is required")
		return
	}
	if req.TargetDetails == nil || len(req.TargetDetails.SasURIList) == 0 {
		sim.AzureErrorf(w, "InvalidParameterValue", http.StatusBadRequest,
			"targetDetails.sasUriList is required")
		return
	}
	backupName := req.BackupSettings.BackupName
	b := PGLtrBackup{
		ID:   pgLtrBackupID(id, backupName),
		Name: backupName,
		Type: "Microsoft.DBforPostgreSQL/flexibleServers/ltrBackupOperations",
		Properties: map[string]any{
			"backupName": backupName,
			"status":     "Running",
			"startTime":  time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	pgLtrBackups.Put(b.ID, b)
	// The capture runs through the operation's own long-running poll: the
	// record reaches Succeeded, and the poll completes, only once the server's
	// volume is actually captured. The operation declares its final state via
	// Location, so the completion payload is what the Location poll answers
	// with. Sizes are not reported: the capture helper does not measure the
	// volume, and a number it never read would be an invented one.
	backupVolume := azurePGLtrBackupVolume(rg, serverName, backupName)
	opID := issueAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
		if err := azurePGCaptureVolume(rg, serverName, backupVolume); err != nil {
			azurePGRemoveBackupVolume(backupVolume)
			pgLtrBackups.Update(b.ID, func(backup *PGLtrBackup) {
				backup.Properties["status"] = "Failed"
				backup.Properties["endTime"] = time.Now().UTC().Format(time.RFC3339Nano)
				backup.Properties["errorCode"] = "LtrBackupFailed"
				backup.Properties["errorMessage"] = err.Error()
			})
			return nil, &AsyncOperationError{Code: "LtrBackupFailed", Message: err.Error()}
		}
		if !pgLtrBackups.Update(b.ID, func(backup *PGLtrBackup) {
			backup.Properties["status"] = "Succeeded"
			backup.Properties["endTime"] = time.Now().UTC().Format(time.RFC3339Nano)
			backup.Properties["percentComplete"] = float64(100)
		}) {
			// The server was deleted while its capture ran, and the cascade
			// took the backup records; the volume goes with them.
			azurePGRemoveBackupVolume(backupVolume)
			return nil, &AsyncOperationError{Code: "ResourceNotFound",
				Message: "The server was deleted while the long-term-retention backup was being captured."}
		}
		settled, found := pgLtrBackups.Get(b.ID)
		if !found {
			azurePGRemoveBackupVolume(backupVolume)
			return nil, &AsyncOperationError{Code: "ResourceNotFound",
				Message: "The server was deleted while the long-term-retention backup was being captured."}
		}
		payload, err := json.Marshal(map[string]any{"properties": settled.Properties})
		if err != nil {
			return nil, &AsyncOperationError{Code: "InternalServerError",
				Message: fmt.Sprintf("render the long-term-retention backup result: %v", err)}
		}
		return payload, nil
	})
	// 202 with both LRO headers: Azure-AsyncOperation for the status poll and
	// Location at operationResults, where the final-state-via-location client
	// reads the BackupsLongTermRetentionResponse payload.
	location := pgServerLocation(id)
	apiVersion := r.URL.Query().Get("api-version")
	w.Header().Set("Azure-AsyncOperation",
		azureAsyncOperationHeader(r, sub, "Microsoft.DBforPostgreSQL", location, "operationStatuses", opID, apiVersion))
	w.Header().Set("Location",
		azureAsyncOperationHeader(r, sub, "Microsoft.DBforPostgreSQL", location, "operationResults", opID, apiVersion))
	w.Header().Set("Retry-After", "0")
	w.WriteHeader(http.StatusAccepted)
}

func handlePGLtrPreBackup(w http.ResponseWriter, r *http.Request) {
	if _, ok := pgRequireServer(w, r); !ok {
		return
	}
	var req pgLtrBackupRequest
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if req.BackupSettings == nil || req.BackupSettings.BackupName == "" {
		sim.AzureErrorf(w, "InvalidParameterValue", http.StatusBadRequest,
			"backupSettings.backupName is required")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"properties": map[string]any{"numberOfContainers": float64(pgLtrCaptureContainers)},
	})
}

func pgThreatProtectionID(serverID, name string) string {
	return serverID + "/advancedThreatProtectionSettings/" + name
}

func pgThreatProtectionDefault(serverID, name string) PGThreatProtection {
	return PGThreatProtection{
		ID:   pgThreatProtectionID(serverID, name),
		Name: name,
		Type: "Microsoft.DBforPostgreSQL/flexibleServers/advancedThreatProtectionSettings",
		Properties: map[string]any{
			"state":        "Disabled",
			"creationTime": time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
}

func handlePGThreatProtectionList(w http.ResponseWriter, r *http.Request) {
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	tp, found := pgThreatProtections.Get(pgThreatProtectionID(id, "Default"))
	if !found {
		tp = pgThreatProtectionDefault(id, "Default")
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []PGThreatProtection{tp}})
}

func handlePGThreatProtectionGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	name := sim.PathParam(r, "threatProtectionName")
	tp, found := pgThreatProtections.Get(pgThreatProtectionID(id, name))
	if !found {
		tp = pgThreatProtectionDefault(id, name)
	}
	sim.WriteJSON(w, http.StatusOK, tp)
}

func handlePGThreatProtectionPut(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	name := sim.PathParam(r, "threatProtectionName")
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	tp := pgThreatProtectionDefault(id, name)
	if state, ok := req.Properties["state"].(string); ok {
		tp.Properties["state"] = state
	}
	pgThreatProtections.Put(tp.ID, tp)
	pgWriteAsyncAccepted(w, r, sub, pgServerLocation(id), issueAzureAsyncOperation(nil))
}

var pgTuningOptionNames = []string{"index", "config"}

func pgTuningOption(serverID, opt string) map[string]any {
	return map[string]any{
		"id":         serverID + "/tuningOptions/" + opt,
		"name":       opt,
		"type":       "Microsoft.DBforPostgreSQL/flexibleServers/tuningOptions",
		"properties": map[string]any{},
	}
}

func handlePGTuningOptionsList(w http.ResponseWriter, r *http.Request) {
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	out := make([]map[string]any, 0, len(pgTuningOptionNames))
	for _, opt := range pgTuningOptionNames {
		out = append(out, pgTuningOption(id, opt))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handlePGTuningOptionGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	opt := sim.PathParam(r, "tuningOption")
	sim.WriteJSON(w, http.StatusOK, pgTuningOption(id, opt))
}

func handlePGTuningRecommendations(w http.ResponseWriter, r *http.Request) {
	if _, ok := pgRequireServer(w, r); !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
}

func pgMigrationID(serverID, name string) string {
	return serverID + "/migrations/" + name
}

func handlePGMigrationCreate(w http.ResponseWriter, r *http.Request) {
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	name := sim.PathParam(r, "migrationName")
	var req PGMigration
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	props := map[string]any{
		"migrationId":   generateUUID(),
		"migrationMode": "Offline",
	}
	for k, v := range req.Properties {
		props[k] = v
	}
	m := PGMigration{
		ID:         pgMigrationID(id, name),
		Name:       name,
		Type:       "Microsoft.DBforPostgreSQL/flexibleServers/migrations",
		Location:   req.Location,
		Tags:       req.Tags,
		Properties: props,
	}
	pgMigrations.Put(m.ID, m)
	sim.WriteJSON(w, http.StatusCreated, m)
}

func handlePGMigrationGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pgServerFromReq(r)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found")
		return
	}
	m, found := pgMigrations.Get(pgMigrationID(id, sim.PathParam(r, "migrationName")))
	if !found {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "migration not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, m)
}

func handlePGMigrationList(w http.ResponseWriter, r *http.Request) {
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	prefix := id + "/migrations/"
	out := pgMigrations.Filter(func(m PGMigration) bool { return strings.HasPrefix(m.ID, prefix) })
	if out == nil {
		out = []PGMigration{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handlePGMigrationUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pgServerFromReq(r)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found")
		return
	}
	mid := pgMigrationID(id, sim.PathParam(r, "migrationName"))
	m, found := pgMigrations.Get(mid)
	if !found {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "migration not found")
		return
	}
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if m.Properties == nil {
		m.Properties = map[string]any{}
	}
	for k, v := range req.Properties {
		m.Properties[k] = v
	}
	pgMigrations.Put(mid, m)
	sim.WriteJSON(w, http.StatusOK, m)
}

func handlePGMigrationCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := pgServerFromReq(r)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found")
		return
	}
	mid := pgMigrationID(id, sim.PathParam(r, "migrationName"))
	m, found := pgMigrations.Get(mid)
	if !found {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "migration not found")
		return
	}
	if m.Properties == nil {
		m.Properties = map[string]any{}
	}
	m.Properties["cancel"] = "True"
	pgMigrations.Put(mid, m)
	sim.WriteJSON(w, http.StatusOK, m)
}

func handlePGCheckMigrationNameAvailability(w http.ResponseWriter, r *http.Request) {
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	resp := map[string]any{"name": req.Name, "type": req.Type, "nameAvailable": true}
	if _, exists := pgMigrations.Get(pgMigrationID(id, req.Name)); exists {
		resp["nameAvailable"] = false
		resp["reason"] = "AlreadyExists"
		resp["message"] = "Migration name is already in use."
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func pgPrivateEndpointConnectionID(serverID, name string) string {
	return serverID + "/privateEndpointConnections/" + name
}

func handlePGPrivateEndpointConnectionList(w http.ResponseWriter, r *http.Request) {
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	prefix := id + "/privateEndpointConnections/"
	out := pgPrivateEndpointCxn.Filter(func(c PGPrivateEndpointConnection) bool { return strings.HasPrefix(c.ID, prefix) })
	if out == nil {
		out = []PGPrivateEndpointConnection{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handlePGPrivateEndpointConnectionGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pgServerFromReq(r)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found")
		return
	}
	c, found := pgPrivateEndpointCxn.Get(pgPrivateEndpointConnectionID(id, sim.PathParam(r, "privateEndpointConnectionName")))
	if !found {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "private endpoint connection not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, c)
}

func handlePGPrivateEndpointConnectionPut(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	name := sim.PathParam(r, "privateEndpointConnectionName")
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	connState := map[string]any{"status": "Approved", "description": "", "actionsRequired": "None"}
	if cs, ok := req.Properties["privateLinkServiceConnectionState"].(map[string]any); ok {
		for k, v := range cs {
			connState[k] = v
		}
	}
	c := PGPrivateEndpointConnection{
		ID:   pgPrivateEndpointConnectionID(id, name),
		Name: name,
		Type: "Microsoft.DBforPostgreSQL/flexibleServers/privateEndpointConnections",
		Properties: map[string]any{
			"privateEndpoint":                   map[string]any{"id": id + "/privateEndpoints/" + name},
			"privateLinkServiceConnectionState": connState,
			"groupIds":                          []string{"postgresqlServer"},
			"provisioningState":                 "Succeeded",
		},
	}
	pgPrivateEndpointCxn.Put(c.ID, c)
	pgWriteAsyncAccepted(w, r, sub, pgServerLocation(id), issueAzureAsyncOperation(nil))
}

func handlePGPrivateEndpointConnectionDelete(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	cid := pgPrivateEndpointConnectionID(id, sim.PathParam(r, "privateEndpointConnectionName"))
	if !pgPrivateEndpointCxn.Delete(cid) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "private endpoint connection not found")
		return
	}
	pgWriteActionAccepted(w, r, sub, pgServerLocation(id), issueAzureAsyncOperation(nil))
}

func pgPrivateLinkResource(serverID, groupName string) map[string]any {
	return map[string]any{
		"id":   serverID + "/privateLinkResources/" + groupName,
		"name": groupName,
		"type": "Microsoft.DBforPostgreSQL/flexibleServers/privateLinkResources",
		"properties": map[string]any{
			"groupId":           "postgresqlServer",
			"requiredMembers":   []string{"postgresqlServer"},
			"requiredZoneNames": []string{"privatelink.postgres.database.azure.com"},
		},
	}
}

func handlePGPrivateLinkResourceList(w http.ResponseWriter, r *http.Request) {
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []map[string]any{pgPrivateLinkResource(id, "postgresqlServer")}})
}

func handlePGPrivateLinkResourceGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pgRequireServer(w, r)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, pgPrivateLinkResource(id, sim.PathParam(r, "groupName")))
}
