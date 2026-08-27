package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Additional Microsoft.DBforPostgreSQL/flexibleServers control-plane
// surface: server-instance lifecycle actions (start/stop/restart), the
// PATCH update LRO, subscription-wide listing, name availability, and the
// nested Administrator / Backup / Replica / VirtualEndpoint resources.

// PGAdministrator mirrors armpostgresqlflexibleservers.ActiveDirectoryAdministrator.
type PGAdministrator struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

// PGBackup mirrors armpostgresqlflexibleservers.ServerBackup.
type PGBackup struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

// PGVirtualEndpoint mirrors armpostgresqlflexibleservers.VirtualEndpointResource.
type PGVirtualEndpoint struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

var (
	pgAdministrators   sim.Store[PGAdministrator]
	pgBackups          sim.Store[PGBackup]
	pgVirtualEndpoints sim.Store[PGVirtualEndpoint]
)

func registerPGFlexibleServerMore(srv *sim.Server) {
	pgAdministrators = sim.MakeStore[PGAdministrator](srv.DB(), "pg_administrators")
	pgBackups = sim.MakeStore[PGBackup](srv.DB(), "pg_backups")
	pgVirtualEndpoints = sim.MakeStore[PGVirtualEndpoint](srv.DB(), "pg_virtual_endpoints")

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DBforPostgreSQL/flexibleServers"
	const subProvider = "/subscriptions/{subscriptionId}/providers/Microsoft.DBforPostgreSQL"

	// Servers_List (subscription-wide) + Servers_Update (PATCH LRO).
	srv.HandleFunc("GET "+subProvider+"/flexibleServers", handlePGListServersBySub)
	srv.HandleFunc("PATCH "+armBase+"/{name}", handlePGUpdateServer)

	// Server lifecycle actions (POST, Azure-AsyncOperation LRO, empty body).
	srv.HandleFunc("POST "+armBase+"/{name}/start", pgServerAction("Ready"))
	srv.HandleFunc("POST "+armBase+"/{name}/stop", pgServerAction("Stopped"))
	srv.HandleFunc("POST "+armBase+"/{name}/restart", pgServerAction("Ready"))

	// CheckNameAvailability (subscription scope + location scope). The SDK
	// sends `checkNameAvailability`; AzurePathNormalizationMiddleware folds it
	// to lowercase, so the routes register lowercase.
	srv.HandleFunc("POST "+subProvider+"/checknameavailability", handlePGCheckNameAvailability)
	srv.HandleFunc("POST "+subProvider+"/locations/{locationName}/checknameavailability", handlePGCheckNameAvailability)

	// Configurations_Update (PATCH LRO) — Configurations_Put (PUT) lives in
	// the base file; PATCH shares the same persisted row.
	srv.HandleFunc("PATCH "+armBase+"/{name}/configurations/{cfg}", handlePGPatchConfiguration)

	// Administrators.
	srv.HandleFunc("GET "+armBase+"/{name}/administrators", handlePGListAdministrators)
	srv.HandleFunc("GET "+armBase+"/{name}/administrators/{objectId}", handlePGGetAdministrator)
	srv.HandleFunc("PUT "+armBase+"/{name}/administrators/{objectId}", handlePGCreateAdministrator)
	srv.HandleFunc("DELETE "+armBase+"/{name}/administrators/{objectId}", handlePGDeleteAdministrator)

	// Backups.
	srv.HandleFunc("GET "+armBase+"/{name}/backups", handlePGListBackups)
	srv.HandleFunc("GET "+armBase+"/{name}/backups/{backupName}", handlePGGetBackup)
	srv.HandleFunc("PUT "+armBase+"/{name}/backups/{backupName}", handlePGCreateBackup)
	srv.HandleFunc("DELETE "+armBase+"/{name}/backups/{backupName}", handlePGDeleteBackup)

	// Replicas (read-only list of replica servers).
	srv.HandleFunc("GET "+armBase+"/{name}/replicas", handlePGListReplicas)

	// VirtualEndpoints.
	srv.HandleFunc("GET "+armBase+"/{name}/virtualendpoints", handlePGListVirtualEndpoints)
	srv.HandleFunc("GET "+armBase+"/{name}/virtualendpoints/{ve}", handlePGGetVirtualEndpoint)
	srv.HandleFunc("PUT "+armBase+"/{name}/virtualendpoints/{ve}", handlePGCreateVirtualEndpoint)
	srv.HandleFunc("PATCH "+armBase+"/{name}/virtualendpoints/{ve}", handlePGUpdateVirtualEndpoint)
	srv.HandleFunc("DELETE "+armBase+"/{name}/virtualendpoints/{ve}", handlePGDeleteVirtualEndpoint)

	registerPGFlexibleServerCompletion(srv)
}

// pgWriteActionAccepted answers a server lifecycle action (start/stop/restart)
// with the LRO contract those operations declare: 202 Accepted, no body, and an
// Azure-AsyncOperation header only. No Location header — the SDK action pollers
// (FinalStateViaLocation) would otherwise issue a stray final GET against the
// action URL, which has no resource to read back.
func pgWriteActionAccepted(w http.ResponseWriter, r *http.Request, sub, location, opID string) {
	opURL := azureAsyncOperationHeader(r, sub, "Microsoft.DBforPostgreSQL", location, "operationStatuses", opID, r.URL.Query().Get("api-version"))
	w.Header().Set("Azure-AsyncOperation", opURL)
	w.Header().Set("Retry-After", "0")
	w.WriteHeader(http.StatusAccepted)
}

func pgServerAction(finalState string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		id := pgServerID(sub, sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
		if _, ok := pgServers.Get(id); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found: %s", id)
			return
		}
		location := pgServerLocation(id)
		opID := issueAzureAsyncOperation(func() {
			pgServers.Update(id, func(stored *PGFlexibleServer) {
				if stored.Properties == nil {
					stored.Properties = map[string]any{}
				}
				stored.Properties["state"] = finalState
			})
		})
		pgWriteActionAccepted(w, r, sub, location, opID)
	}
}

func handlePGListServersBySub(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/", sub)
	out := pgServers.Filter(func(s PGFlexibleServer) bool {
		return strings.HasPrefix(s.ID, prefix)
	})
	if out == nil {
		out = []PGFlexibleServer{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handlePGUpdateServer(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	id := pgServerID(sub, rg, name)
	if _, ok := pgServers.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found: %s", id)
		return
	}
	var req struct {
		Tags       map[string]string `json:"tags,omitempty"`
		SKU        map[string]any    `json:"sku,omitempty"`
		Properties map[string]any    `json:"properties,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	// administratorLoginPassword is write-only (x-ms-secret): a PATCH that
	// carries it rotates the sealed credential and never stores it in the
	// properties a GET echoes back.
	var rotatedPassword string
	var rotate bool
	if req.Properties != nil {
		if pw, isString := req.Properties["administratorLoginPassword"].(string); isString {
			rotatedPassword = pw
			rotate = pw != ""
		}
		delete(req.Properties, "administratorLoginPassword")
	}
	if rotate {
		sealed, err := azurePGSealSecret(rotatedPassword)
		if err != nil {
			sim.AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
				"seal administrator credential: %v", err)
			return
		}
		pgServerCredentials.Put(azurePGServerKey(rg, name), pgServerCredential{Sealed: sealed})
	}
	pgServers.Update(id, func(stored *PGFlexibleServer) {
		if req.Tags != nil {
			stored.Tags = req.Tags
		}
		if req.SKU != nil {
			stored.SKU = req.SKU
		}
		if stored.Properties == nil {
			stored.Properties = map[string]any{}
		}
		for k, v := range req.Properties {
			stored.Properties[k] = v
		}
	})
	location := pgServerLocation(id)
	if rotate {
		// A server that gains its first credential through PATCH gets its
		// data plane now; a running engine applies the rotation through the
		// update's own long-running operation.
		azurePGInstallOrExplain(sub, rg, name)
		opID := issueAzureAsyncOperationOutcome(func() *AsyncOperationError {
			if err := azurePGRotateAdminPasswordIfRunning(sub, rg, name, rotatedPassword); err != nil {
				return &AsyncOperationError{Code: "PasswordRotationFailed", Message: err.Error()}
			}
			return nil
		})
		pgWriteAsyncAccepted(w, r, sub, location, opID)
		return
	}
	pgWriteAsyncAccepted(w, r, sub, location, issueAzureAsyncOperation(nil))
}

func handlePGCheckNameAvailability(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	available := true
	var reason, message string
	for _, s := range pgServers.List() {
		if s.Name == req.Name {
			available = false
			reason = "AlreadyExists"
			message = fmt.Sprintf("Server name %q is already in use.", req.Name)
			break
		}
	}
	resp := map[string]any{
		"nameAvailable": available,
		"name":          req.Name,
		"type":          req.Type,
	}
	if !available {
		resp["reason"] = reason
		resp["message"] = message
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handlePGPatchConfiguration(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	server := sim.PathParam(r, "name")
	cfgName := sim.PathParam(r, "cfg")
	serverID := pgServerID(sub, rg, server)
	if _, ok := pgServers.Get(serverID); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Flexible server %q not found", server)
		return
	}
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	c := PGConfiguration{
		ID:         serverID + "/configurations/" + cfgName,
		Name:       cfgName,
		Type:       "Microsoft.DBforPostgreSQL/flexibleServers/configurations",
		Properties: req.Properties,
	}
	pgConfigurations.Put(pgConfigKey(sub, rg, server, cfgName), c)
	pgWriteAsyncAccepted(w, r, sub, pgServerLocation(serverID), issueAzureAsyncOperation(nil))
}

func pgAdminID(serverID, objectID string) string {
	return serverID + "/administrators/" + objectID
}

func handlePGListAdministrators(w http.ResponseWriter, r *http.Request) {
	serverID := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	prefix := serverID + "/administrators/"
	out := pgAdministrators.Filter(func(a PGAdministrator) bool {
		return strings.HasPrefix(a.ID, prefix)
	})
	if out == nil {
		out = []PGAdministrator{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handlePGGetAdministrator(w http.ResponseWriter, r *http.Request) {
	serverID := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	id := pgAdminID(serverID, sim.PathParam(r, "objectId"))
	a, ok := pgAdministrators.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "administrator not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, a)
}

func handlePGCreateAdministrator(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	serverID := pgServerID(sub, sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := pgServers.Get(serverID); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found")
		return
	}
	objectID := sim.PathParam(r, "objectId")
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	props := map[string]any{"objectId": objectID}
	for k, v := range req.Properties {
		props[k] = v
	}
	a := PGAdministrator{
		ID:         pgAdminID(serverID, objectID),
		Name:       objectID,
		Type:       "Microsoft.DBforPostgreSQL/flexibleServers/administrators",
		Properties: props,
	}
	pgAdministrators.Put(a.ID, a)
	pgWriteAsyncAccepted(w, r, sub, pgServerLocation(serverID), issueAzureAsyncOperation(nil))
}

func handlePGDeleteAdministrator(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	serverID := pgServerID(sub, sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	id := pgAdminID(serverID, sim.PathParam(r, "objectId"))
	if !pgAdministrators.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "administrator not found")
		return
	}
	pgWriteAsyncAccepted(w, r, sub, pgServerLocation(serverID), issueAzureAsyncOperation(nil))
}

func pgBackupID(serverID, backupName string) string {
	return serverID + "/backups/" + backupName
}

func handlePGListBackups(w http.ResponseWriter, r *http.Request) {
	serverID := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	prefix := serverID + "/backups/"
	out := pgBackups.Filter(func(b PGBackup) bool {
		return strings.HasPrefix(b.ID, prefix)
	})
	if out == nil {
		out = []PGBackup{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handlePGGetBackup(w http.ResponseWriter, r *http.Request) {
	serverID := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	id := pgBackupID(serverID, sim.PathParam(r, "backupName"))
	b, ok := pgBackups.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "backup not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, b)
}

func handlePGCreateBackup(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	serverName := sim.PathParam(r, "name")
	serverID := pgServerID(sub, rg, serverName)
	if _, ok := pgServers.Get(serverID); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found")
		return
	}
	backupName := sim.PathParam(r, "backupName")
	b := PGBackup{
		ID:   pgBackupID(serverID, backupName),
		Name: backupName,
		Type: "Microsoft.DBforPostgreSQL/flexibleServers/backups",
		Properties: map[string]any{
			"backupType": "Customer On-Demand",
			"source":     "Full",
		},
	}
	pgBackups.Put(b.ID, b)
	// The capture runs through the backup's own long-running operation —
	// completedTime lands, and the poll succeeds, only once the server's
	// volume is actually captured. A failed capture fails the operation and
	// withdraws the backup.
	backupVolume := azurePGBackupVolume(rg, serverName, backupName)
	opID := issueAzureAsyncOperationOutcome(func() *AsyncOperationError {
		if err := azurePGCaptureVolume(rg, serverName, backupVolume); err != nil {
			pgBackups.Delete(b.ID)
			azurePGRemoveBackupVolume(backupVolume)
			return &AsyncOperationError{Code: "BackupFailed", Message: err.Error()}
		}
		if !pgBackups.Update(b.ID, func(backup *PGBackup) {
			if backup.Properties == nil {
				backup.Properties = map[string]any{}
			}
			backup.Properties["completedTime"] = time.Now().UTC().Format(time.RFC3339Nano)
		}) {
			// The backup was deleted while its capture ran; the volume, if
			// the capture made one, goes with it.
			azurePGRemoveBackupVolume(backupVolume)
		}
		return nil
	})
	pgWriteAsyncAccepted(w, r, sub, pgServerLocation(serverID), opID)
}

func handlePGDeleteBackup(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	serverName := sim.PathParam(r, "name")
	serverID := pgServerID(sub, rg, serverName)
	backupName := sim.PathParam(r, "backupName")
	id := pgBackupID(serverID, backupName)
	if !pgBackups.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "backup not found")
		return
	}
	azurePGRemoveBackupVolume(azurePGBackupVolume(rg, serverName, backupName))
	pgWriteAsyncAccepted(w, r, sub, pgServerLocation(serverID), issueAzureAsyncOperation(nil))
}

// handlePGListReplicas lists the read replicas of a server: every flexible
// server whose replicationRole marks it a replica and whose top-level
// properties.sourceServerResourceId — where ServerProperties carries it —
// points at this server (Replicas_ListByServer). The role check keeps
// restored servers out of the list: a PointInTimeRestore or GeoRestore create
// also records the sourceServerResourceId it was built from, and a restored
// server is not a replica.
func handlePGListReplicas(w http.ResponseWriter, r *http.Request) {
	serverID := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	out := pgServers.Filter(func(s PGFlexibleServer) bool {
		if s.Properties == nil {
			return false
		}
		role, _ := s.Properties["replicationRole"].(string)
		if role != "AsyncReplica" && role != "GeoAsyncReplica" {
			return false
		}
		src, _ := s.Properties["sourceServerResourceId"].(string)
		return src == serverID
	})
	if out == nil {
		out = []PGFlexibleServer{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func pgVirtualEndpointID(serverID, name string) string {
	return serverID + "/virtualendpoints/" + name
}

func handlePGListVirtualEndpoints(w http.ResponseWriter, r *http.Request) {
	serverID := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	prefix := serverID + "/virtualendpoints/"
	out := pgVirtualEndpoints.Filter(func(v PGVirtualEndpoint) bool {
		return strings.HasPrefix(v.ID, prefix)
	})
	if out == nil {
		out = []PGVirtualEndpoint{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handlePGGetVirtualEndpoint(w http.ResponseWriter, r *http.Request) {
	serverID := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	id := pgVirtualEndpointID(serverID, sim.PathParam(r, "ve"))
	v, ok := pgVirtualEndpoints.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "virtual endpoint not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, v)
}

func pgPutVirtualEndpoint(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	serverID := pgServerID(sub, sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := pgServers.Get(serverID); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found")
		return
	}
	veName := sim.PathParam(r, "ve")
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := pgVirtualEndpointID(serverID, veName)
	props := map[string]any{}
	if existing, ok := pgVirtualEndpoints.Get(id); ok && existing.Properties != nil {
		for k, v := range existing.Properties {
			props[k] = v
		}
	}
	for k, v := range req.Properties {
		props[k] = v
	}
	if _, ok := props["endpointType"]; !ok {
		props["endpointType"] = "ReadWrite"
	}
	// virtualEndpoints is a read-only list of the endpoint's hostnames.
	props["virtualEndpoints"] = []string{azureEndpointHostname(r, veName, "postgres", "database")}
	v := PGVirtualEndpoint{
		ID:         id,
		Name:       veName,
		Type:       "Microsoft.DBforPostgreSQL/flexibleServers/virtualendpoints",
		Properties: props,
	}
	pgVirtualEndpoints.Put(id, v)
	pgWriteAsyncAccepted(w, r, sub, pgServerLocation(serverID), issueAzureAsyncOperation(nil))
}

func handlePGCreateVirtualEndpoint(w http.ResponseWriter, r *http.Request) {
	pgPutVirtualEndpoint(w, r)
}
func handlePGUpdateVirtualEndpoint(w http.ResponseWriter, r *http.Request) {
	pgPutVirtualEndpoint(w, r)
}

func handlePGDeleteVirtualEndpoint(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	serverID := pgServerID(sub, sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	id := pgVirtualEndpointID(serverID, sim.PathParam(r, "ve"))
	if !pgVirtualEndpoints.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "virtual endpoint not found")
		return
	}
	pgWriteAsyncAccepted(w, r, sub, pgServerLocation(serverID), issueAzureAsyncOperation(nil))
}
