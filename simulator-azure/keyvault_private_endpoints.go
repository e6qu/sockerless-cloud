package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// KeyVaultPrivateEndpointConnection is a private endpoint connection on a
// vault (Microsoft.KeyVault/vaults/privateEndpointConnections).
type KeyVaultPrivateEndpointConnection struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Etag       string         `json:"etag,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

func keyVaultPECID(sub, rg, vault, pec string) string {
	return keyVaultResourceID(sub, rg, vault) + "/privateEndpointConnections/" + pec
}

func handleKeyVaultPECPut(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	vault := sim.PathParam(r, "name")
	pecName := sim.PathParam(r, "pec")
	if _, ok := keyVaults.Get(keyVaultResourceID(sub, rg, vault)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Vault %q not found in resource group %q.", vault, rg)
		return
	}
	var req KeyVaultPrivateEndpointConnection
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	props := map[string]any{}
	for k, v := range req.Properties {
		props[k] = v
	}
	props["provisioningState"] = "Succeeded"
	id := keyVaultPECID(sub, rg, vault, pecName)
	pec := KeyVaultPrivateEndpointConnection{
		ID:         id,
		Name:       pecName,
		Type:       "Microsoft.KeyVault/vaults/privateEndpointConnections",
		Properties: props,
	}
	keyVaultPrivConn.Put(id, pec)
	sim.WriteJSON(w, http.StatusOK, keyVaultPECWire(pec))
}

func handleKeyVaultPECGet(w http.ResponseWriter, r *http.Request) {
	id := keyVaultPECID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
		sim.PathParam(r, "name"), sim.PathParam(r, "pec"))
	pec, ok := keyVaultPrivConn.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Private endpoint connection %q not found.", sim.PathParam(r, "pec"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, keyVaultPECWire(pec))
}

// keyVaultPECWire projects a stored private endpoint connection onto the
// members Microsoft.KeyVault declares. The connection object is shared with
// the Microsoft.Network private-endpoint surface, which carries additional
// members of its own (group ids, the link identifier, the endpoint's location
// and address); emitting those here would answer a Key Vault client with
// fields its contract does not define.
func keyVaultPECWire(pec KeyVaultPrivateEndpointConnection) KeyVaultPrivateEndpointConnection {
	if pec.Properties == nil {
		return pec
	}
	declared := make(map[string]any, 3)
	for _, member := range []string{"privateEndpoint", "privateLinkServiceConnectionState", "provisioningState"} {
		if v, ok := pec.Properties[member]; ok {
			declared[member] = v
		}
	}
	pec.Properties = declared
	return pec
}

func handleKeyVaultPECDelete(w http.ResponseWriter, r *http.Request) {
	id := keyVaultPECID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
		sim.PathParam(r, "name"), sim.PathParam(r, "pec"))
	if !keyVaultPrivConn.Delete(id) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleKeyVaultPECList(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	vault := sim.PathParam(r, "name")
	if _, ok := keyVaults.Get(keyVaultResourceID(sub, rg, vault)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Vault %q not found in resource group %q.", vault, rg)
		return
	}
	prefix := keyVaultResourceID(sub, rg, vault) + "/privateEndpointConnections/"
	out := keyVaultPrivConn.Filter(func(p KeyVaultPrivateEndpointConnection) bool {
		return strings.HasPrefix(p.ID, prefix)
	})
	wire := make([]KeyVaultPrivateEndpointConnection, 0, len(out))
	for _, pec := range out {
		wire = append(wire, keyVaultPECWire(pec))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": wire})
}

// handleKeyVaultPrivateLinkResources returns the vault's single "vault"
// private-link group, matching real Azure.
func handleKeyVaultPrivateLinkResources(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	vault := sim.PathParam(r, "name")
	id := keyVaultResourceID(sub, rg, vault)
	if _, ok := keyVaults.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Vault %q not found in resource group %q.", vault, rg)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []map[string]any{{
			"id":   id + "/privateLinkResources/vault",
			"name": "vault",
			"type": "Microsoft.KeyVault/vaults/privateLinkResources",
			"properties": map[string]any{
				"groupId":           "vault",
				"requiredMembers":   []string{"default"},
				"requiredZoneNames": []string{"privatelink.vaultcore.azure.net"},
			},
		}},
	})
}
