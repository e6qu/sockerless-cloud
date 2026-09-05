package main

import (
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Private endpoint connections on a Cosmos DB account
// (Microsoft.DocumentDB/databaseAccounts/privateEndpointConnections). A private
// endpoint connection is created when an Azure Private Endpoint targets the
// account; the account owner then approves or rejects it by PUTting a new
// privateLinkServiceConnectionState. The simulator stores each connection by
// its ARM id and serves the list/get/create-or-update/delete surface the
// armcosmos PrivateEndpointConnectionsClient calls.

const cosmosPECType = "Microsoft.DocumentDB/databaseAccounts/privateEndpointConnections"

// CosmosPrivateEndpointConnection is the stored ARM representation of a private
// endpoint connection, matching the swagger PrivateEndpointConnection shape
// (ProxyResource id/name/type + flattened properties).
type CosmosPrivateEndpointConnection struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

var cosmosPECs sim.Store[CosmosPrivateEndpointConnection]

func registerCosmosPEC(srv *sim.Server) {
	cosmosPECs = sim.MakeStore[CosmosPrivateEndpointConnection](srv.DB(), "cosmos_private_endpoint_connections")

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts"
	base := armBase + "/{account}/privateEndpointConnections"
	srv.HandleFunc("GET "+base, handleCosmosListPECs)
	srv.HandleFunc("GET "+base+"/{name}", handleCosmosGetPEC)
	srv.HandleFunc("PUT "+base+"/{name}", handleCosmosPutPEC)
	srv.HandleFunc("DELETE "+base+"/{name}", handleCosmosDeletePEC)
}

// cosmosPECAccountID is the ARM id of the account that owns the connection
// addressed by the request path (the path with /privateEndpointConnections/...
// stripped).
func cosmosPECAccountID(r *http.Request) string {
	return cosmosAccountID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account"))
}

func handleCosmosListPECs(w http.ResponseWriter, r *http.Request) {
	if _, ok := cosmosAccounts.Get(cosmosPECAccountID(r)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB account not found: %s", cosmosPECAccountID(r))
		return
	}
	prefix := r.URL.Path + "/"
	all := cosmosPECs.Filter(func(c CosmosPrivateEndpointConnection) bool {
		return strings.HasPrefix(c.ID, prefix)
	})
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	if all == nil {
		all = []CosmosPrivateEndpointConnection{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
}

func handleCosmosGetPEC(w http.ResponseWriter, r *http.Request) {
	pec, ok := cosmosPECs.Get(r.URL.Path)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "private endpoint connection not found: %s", r.URL.Path)
		return
	}
	sim.WriteJSON(w, http.StatusOK, pec)
}

func handleCosmosPutPEC(w http.ResponseWriter, r *http.Request) {
	if _, ok := cosmosAccounts.Get(cosmosPECAccountID(r)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB account not found: %s", cosmosPECAccountID(r))
		return
	}
	var req CosmosPrivateEndpointConnection
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid private endpoint connection body: %v", err)
		return
	}
	id := r.URL.Path
	props := req.Properties
	if props == nil {
		props = map[string]any{}
	}
	// groupId defaults to the SQL (NoSQL) API sub-resource when the caller
	// only sets the connection state to approve/reject the endpoint.
	if existing, ok := cosmosPECs.Get(id); ok && props["groupId"] == nil {
		if g, ok := existing.Properties["groupId"]; ok {
			props["groupId"] = g
		}
	}
	if props["groupId"] == nil {
		props["groupId"] = "Sql"
	}
	props["provisioningState"] = "Succeeded"
	pec := CosmosPrivateEndpointConnection{
		ID:         id,
		Name:       path.Base(id),
		Type:       cosmosPECType,
		Properties: props,
	}
	cosmosPECs.Put(id, pec)
	sim.WriteJSON(w, http.StatusOK, pec)
}

func handleCosmosDeletePEC(w http.ResponseWriter, r *http.Request) {
	if !cosmosPECs.Delete(r.URL.Path) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "private endpoint connection not found: %s", r.URL.Path)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
