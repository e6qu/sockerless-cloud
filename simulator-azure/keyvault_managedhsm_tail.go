package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// DeletedManagedHSM is a `Microsoft.KeyVault/deletedManagedHSMs` resource: the
// record a soft-deleted pool leaves behind, which a caller reads to recover or
// purge it.
type DeletedManagedHSM struct {
	ID         string                      `json:"id"`
	Name       string                      `json:"name"`
	Type       string                      `json:"type"`
	Properties DeletedManagedHSMProperties `json:"properties"`
}

// DeletedManagedHSMProperties mirrors the DeletedManagedHsmProperties schema.
type DeletedManagedHSMProperties struct {
	MhsmID          string            `json:"mhsmId,omitempty"`
	Location        string            `json:"location,omitempty"`
	DeletionDate    string            `json:"deletionDate,omitempty"`
	ScheduledPurge  string            `json:"scheduledPurgeDate,omitempty"`
	PurgeProtection *bool             `json:"purgeProtectionEnabled,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
}

// MHSMPrivateEndpointConnection is a pool's private endpoint connection.
type MHSMPrivateEndpointConnection struct {
	ID         string                                  `json:"id"`
	Name       string                                  `json:"name"`
	Type       string                                  `json:"type"`
	Properties MHSMPrivateEndpointConnectionProperties `json:"properties"`
}

// MHSMPrivateEndpointConnectionProperties mirrors the schema of the same name.
type MHSMPrivateEndpointConnectionProperties struct {
	PrivateEndpoint                   map[string]any `json:"privateEndpoint,omitempty"`
	PrivateLinkServiceConnectionState map[string]any `json:"privateLinkServiceConnectionState,omitempty"`
	ProvisioningState                 string         `json:"provisioningState,omitempty"`
}

var (
	deletedManagedHSMs sim.Store[DeletedManagedHSM]
	mhsmPrivateLinks   sim.Store[MHSMPrivateEndpointConnection]
)

// How long a retired pool stays recoverable when its own policy names no
// retention: the service's own default.
const mhsmDefaultSoftDeleteDays = 90

func deletedManagedHSMID(sub, location, name string) string {
	return fmt.Sprintf("/subscriptions/%s/providers/Microsoft.KeyVault/locations/%s/deletedManagedHSMs/%s",
		sub, location, name)
}

// mhsmSoftDelete records the retirement of a pool that carried soft delete.
func mhsmSoftDelete(sub, rg, name string, hsm ManagedHSM) {
	if hsm.Properties.EnableSoftDelete != nil && !*hsm.Properties.EnableSoftDelete {
		return
	}
	retention := int(hsm.Properties.SoftDeleteRetentionInDays)
	if retention <= 0 {
		retention = mhsmDefaultSoftDeleteDays
	}
	now := time.Now().UTC()
	deletedManagedHSMs.Put(deletedManagedHSMID(sub, hsm.Location, name), DeletedManagedHSM{
		ID:   deletedManagedHSMID(sub, hsm.Location, name),
		Name: name,
		Type: "Microsoft.KeyVault/deletedManagedHSMs",
		Properties: DeletedManagedHSMProperties{
			MhsmID:          managedHSMID(sub, rg, name),
			Location:        hsm.Location,
			DeletionDate:    now.Format(time.RFC3339),
			ScheduledPurge:  now.AddDate(0, 0, retention).Format(time.RFC3339),
			PurgeProtection: hsm.Properties.EnablePurgeProtection,
			Tags:            hsm.Tags,
		},
	})
}

func registerManagedHSMTail(srv *sim.Server, armBase string) {
	deletedManagedHSMs = sim.MakeStore[DeletedManagedHSM](srv.DB(), "keyvault_deleted_managed_hsms")
	mhsmPrivateLinks = sim.MakeStore[MHSMPrivateEndpointConnection](srv.DB(), "keyvault_mhsm_private_links")

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/deletedManagedHSMs",
		func(w http.ResponseWriter, r *http.Request) {
			prefix := fmt.Sprintf("/subscriptions/%s/", sim.PathParam(r, "subscriptionId"))
			all := deletedManagedHSMs.Filter(func(d DeletedManagedHSM) bool {
				return strings.HasPrefix(d.ID, prefix)
			})
			if all == nil {
				all = []DeletedManagedHSM{}
			}
			sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
			sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
		})

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedManagedHSMs/{name}",
		func(w http.ResponseWriter, r *http.Request) {
			id := deletedManagedHSMID(sim.PathParam(r, "subscriptionId"),
				sim.PathParam(r, "location"), sim.PathParam(r, "name"))
			deleted, ok := deletedManagedHSMs.Get(id)
			if !ok {
				AzureError(w, "NotFound", fmt.Sprintf("Deleted managed HSM %q not found", sim.PathParam(r, "name")), http.StatusNotFound)
				return
			}
			sim.WriteJSON(w, http.StatusOK, deleted)
		})

	// Purging destroys the record, which purge protection exists to prevent.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedManagedHSMs/{name}/purge",
		func(w http.ResponseWriter, r *http.Request) {
			id := deletedManagedHSMID(sim.PathParam(r, "subscriptionId"),
				sim.PathParam(r, "location"), sim.PathParam(r, "name"))
			deleted, ok := deletedManagedHSMs.Get(id)
			if !ok {
				AzureError(w, "NotFound", fmt.Sprintf("Deleted managed HSM %q not found", sim.PathParam(r, "name")), http.StatusNotFound)
				return
			}
			if deleted.Properties.PurgeProtection != nil && *deleted.Properties.PurgeProtection {
				AzureError(w, "Conflict", fmt.Sprintf("Managed HSM %q has purge protection enabled and cannot be purged",
					sim.PathParam(r, "name")), http.StatusConflict)
				return
			}
			deletedManagedHSMs.Delete(id)
			// 202 with a Location the client polls, the same documented LRO
			// form the deleted-vault purge uses.
			operationPath := fmt.Sprintf(
				"/subscriptions/%s/providers/Microsoft.KeyVault/locations/%s/deletedManagedHSMs/%s/purge/operation",
				sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "location"), sim.PathParam(r, "name"))
			w.Header().Set("Location", azureRequestScheme(r)+"://"+r.Host+operationPath+
				"?api-version="+r.URL.Query().Get("api-version"))
			w.WriteHeader(http.StatusAccepted)
		})

	// The terminal Location a completed purge returns: a zero-length 200 is
	// the SDK's completion signal for this LRO form.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedManagedHSMs/{name}/purge/operation",
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

	// A name is available when no live pool and no retired record holds it.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/checkMhsmNameAvailability",
		func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Name string `json:"name"`
				Type string `json:"type"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				AzureError(w, "BadRequest", fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
				return
			}
			if req.Name == "" {
				AzureError(w, "BadRequest", "name is required", http.StatusBadRequest)
				return
			}
			suffix := "/managedHSMs/" + req.Name
			taken := len(managedHSMs.Filter(func(h ManagedHSM) bool {
				return strings.HasSuffix(h.ID, suffix)
			})) > 0
			if !taken {
				taken = len(deletedManagedHSMs.Filter(func(d DeletedManagedHSM) bool {
					return d.Name == req.Name
				})) > 0
			}
			if taken {
				sim.WriteJSON(w, http.StatusOK, map[string]any{
					"nameAvailable": false,
					"reason":        "AlreadyExists",
					"message":       fmt.Sprintf("The name %q is already in use.", req.Name),
				})
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"nameAvailable": true})
		})

	hsmExists := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		id := managedHSMID(sim.PathParam(r, "subscriptionId"),
			sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
		if _, ok := managedHSMs.Get(id); !ok {
			AzureError(w, "NotFound", fmt.Sprintf("Managed HSM %q not found", sim.PathParam(r, "name")), http.StatusNotFound)
			return "", false
		}
		return id, true
	}

	srv.HandleFunc("GET "+armBase+"/{name}/privateEndpointConnections",
		func(w http.ResponseWriter, r *http.Request) {
			id, ok := hsmExists(w, r)
			if !ok {
				return
			}
			all := mhsmPrivateLinks.Filter(func(c MHSMPrivateEndpointConnection) bool {
				return strings.HasPrefix(c.ID, id+"/privateEndpointConnections/")
			})
			if all == nil {
				all = []MHSMPrivateEndpointConnection{}
			}
			sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
			sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
		})

	srv.HandleFunc("GET "+armBase+"/{name}/privateEndpointConnections/{privateEndpointConnectionName}",
		func(w http.ResponseWriter, r *http.Request) {
			id, ok := hsmExists(w, r)
			if !ok {
				return
			}
			connection, found := mhsmPrivateLinks.Get(id + "/privateEndpointConnections/" +
				sim.PathParam(r, "privateEndpointConnectionName"))
			if !found {
				AzureError(w, "NotFound", fmt.Sprintf("Private endpoint connection %q not found",
					sim.PathParam(r, "privateEndpointConnectionName")), http.StatusNotFound)
				return
			}
			sim.WriteJSON(w, http.StatusOK, connection)
		})

	srv.HandleFunc("PUT "+armBase+"/{name}/privateEndpointConnections/{privateEndpointConnectionName}",
		func(w http.ResponseWriter, r *http.Request) {
			id, ok := hsmExists(w, r)
			if !ok {
				return
			}
			connectionName := sim.PathParam(r, "privateEndpointConnectionName")
			var body MHSMPrivateEndpointConnection
			if err := sim.ReadJSON(r, &body); err != nil {
				AzureError(w, "BadRequest", fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
				return
			}
			body.ID = id + "/privateEndpointConnections/" + connectionName
			body.Name = connectionName
			body.Type = "Microsoft.KeyVault/managedHSMs/privateEndpointConnections"
			body.Properties.ProvisioningState = "Succeeded"
			mhsmPrivateLinks.Put(body.ID, body)
			sim.WriteJSON(w, http.StatusOK, body)
		})

	srv.HandleFunc("DELETE "+armBase+"/{name}/privateEndpointConnections/{privateEndpointConnectionName}",
		func(w http.ResponseWriter, r *http.Request) {
			id, ok := hsmExists(w, r)
			if !ok {
				return
			}
			key := id + "/privateEndpointConnections/" + sim.PathParam(r, "privateEndpointConnectionName")
			connection, found := mhsmPrivateLinks.Get(key)
			if !found {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			mhsmPrivateLinks.Delete(key)
			connection.Properties.ProvisioningState = "Deleting"
			sim.WriteJSON(w, http.StatusOK, connection)
		})

	// A pool exposes one private-link group, "managedhsm".
	srv.HandleFunc("GET "+armBase+"/{name}/privateLinkResources",
		func(w http.ResponseWriter, r *http.Request) {
			id, ok := hsmExists(w, r)
			if !ok {
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{map[string]any{
				"id":   id + "/privateLinkResources/managedhsm",
				"name": "managedhsm",
				"type": "Microsoft.KeyVault/managedHSMs/privateLinkResources",
				"properties": map[string]any{
					"groupId":           "managedhsm",
					"requiredMembers":   []string{"managedhsm"},
					"requiredZoneNames": []string{"privatelink.managedhsm.azure.net"},
				},
			}}})
		})

	// The regions a pool's geo-replication spans. A pool that declares none
	// spans only the region it lives in.
	srv.HandleFunc("GET "+armBase+"/{name}/regions",
		func(w http.ResponseWriter, r *http.Request) {
			id, ok := hsmExists(w, r)
			if !ok {
				return
			}
			hsm, _ := managedHSMs.Get(id)
			sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{map[string]any{
				"name":              hsm.Location,
				"provisioningState": "Succeeded",
				"isPrimary":         true,
			}}})
		})
}
