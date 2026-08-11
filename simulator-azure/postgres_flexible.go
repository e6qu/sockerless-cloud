package main

import (
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Microsoft.DBforPostgreSQL/flexibleServers ARM control plane.
// Surface scoped to server-instance lifecycle and its nested resources.

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

// PGConfiguration is one row in the per-server configurations
// list. Real Azure PG exposes server-wide settings (max_connections,
// log_statement, shared_buffers, etc.) as a flat key/value list. The
// shape mirrors armpostgresqlflexibleservers.Configuration:
// id + name + properties{value, defaultValue, source, ...}.
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
}

// pgConfigKey is the per-(server, configuration-name) store key.
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
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Flexible server %q not found", server)
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
		ID:         pgServerID(sub, rg, server) + "/configurations/" + cfgName,
		Name:       cfgName,
		Type:       "Microsoft.DBforPostgreSQL/flexibleServers/configurations",
		Properties: req.Properties,
	}
	pgConfigurations.Put(pgConfigKey(sub, rg, server, cfgName), c)
	sim.WriteJSON(w, http.StatusOK, c)
}

func pgServerID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DBforPostgreSQL/flexibleServers/%s", sub, rg, name)
}

func handlePGCreateServer(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	var req PGFlexibleServer
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := pgServerID(sub, rg, name)
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
	pgServers.Put(id, s)
	opID := issueAzureAsyncOperation(func() {
		pgServers.Update(id, func(stored *PGFlexibleServer) {
			if stored.Properties == nil {
				stored.Properties = map[string]any{}
			}
			stored.Properties["state"] = "Ready"
		})
	})
	// Servers_Create declares 202-only (no success body): the caller
	// polls Azure-AsyncOperation until Succeeded, then GETs the
	// resource URL (Location) for the created server.
	pgWriteAsyncAccepted(w, r, sub, s.Location, opID)
}

// pgWriteAsyncAccepted answers a PostgreSQL flexible-server mutation
// with the LRO contract the 2025-08-01 spec declares for PUT and
// DELETE: 202 Accepted, no body, Azure-AsyncOperation pointing at the
// operation-status poll URL and Location at the resource URL.
func pgWriteAsyncAccepted(w http.ResponseWriter, r *http.Request, sub, location, opID string) {
	opURL := azureAsyncOperationHeader(r, sub, "Microsoft.DBforPostgreSQL", location, "operationStatuses", opID, r.URL.Query().Get("api-version"))
	writeAzureAsyncCreateHeaders(w, opURL, azureCurrentRequestURL(r))
	w.WriteHeader(http.StatusAccepted)
}

// pgServerLocation resolves the owning server's location for the
// sub-resource LRO poll URL; the spec's operationStatuses path is
// location-scoped.
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
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found: %s", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, s)
}

func handlePGDeleteServer(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	id := pgServerID(sub, sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	location := pgServerLocation(id)
	if !pgServers.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found: %s", id)
		return
	}
	// Cascade-delete owned databases + firewall rules.
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
	pgWriteAsyncAccepted(w, r, sub, location, issueAzureAsyncOperation(nil))
}

func handlePGListServersByRG(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DBforPostgreSQL/flexibleServers/", sub, rg)
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
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found")
		return
	}
	dbName := sim.PathParam(r, "db")
	var req PGDatabase
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
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
	pgWriteAsyncAccepted(w, r, sim.PathParam(r, "subscriptionId"), pgServerLocation(parent), issueAzureAsyncOperation(nil))
}

func handlePGGetDatabase(w http.ResponseWriter, r *http.Request) {
	id := pgServerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/databases/" + sim.PathParam(r, "db")
	d, ok := pgDatabases.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "database not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, d)
}

func handlePGDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	parent := pgServerID(sub, sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	id := parent + "/databases/" + sim.PathParam(r, "db")
	if !pgDatabases.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "database not found")
		return
	}
	pgWriteAsyncAccepted(w, r, sub, pgServerLocation(parent), issueAzureAsyncOperation(nil))
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
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "server not found")
		return
	}
	ruleName := sim.PathParam(r, "rule")
	var req PGFirewallRule
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
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
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "firewall rule not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, fr)
}

func handlePGDeleteFirewallRule(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	parent := pgServerID(sub, sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	id := parent + "/firewallRules/" + sim.PathParam(r, "rule")
	if !pgFirewallRules.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "firewall rule not found")
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
