package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Microsoft.KeyVault/managedHSMs — the Managed HSM pool ARM resource, the
// second resource the Key Vault provider tracks alongside vaults. A client
// enumerating a subscription's or a resource group's pools reaches
// ManagedHsms_ListBySubscription / ManagedHsms_ListByResourceGroup, which
// answer the collection — empty for a subscription that has provisioned none,
// rather than the 404 an unrouted path returns.
//
// Real API:
//
//	https://learn.microsoft.com/en-us/rest/api/keyvault/keyvault/managed-hsms

// ManagedHSM is a `Microsoft.KeyVault/managedHSMs/{name}` ARM resource.
type ManagedHSM struct {
	ID         string               `json:"id"`
	Name       string               `json:"name"`
	Type       string               `json:"type"`
	Location   string               `json:"location"`
	Sku        *ManagedHSMSku       `json:"sku,omitempty"`
	Tags       map[string]string    `json:"tags,omitempty"`
	Properties ManagedHSMProperties `json:"properties"`
}

// ManagedHSMSku mirrors the ManagedHsmSku schema.
type ManagedHSMSku struct {
	Family string `json:"family"`
	Name   string `json:"name"`
}

// ManagedHSMProperties mirrors the ManagedHsmProperties schema.
type ManagedHSMProperties struct {
	TenantID                  string   `json:"tenantId,omitempty"`
	InitialAdminObjectIds     []string `json:"initialAdminObjectIds,omitempty"`
	HsmURI                    string   `json:"hsmUri,omitempty"`
	EnableSoftDelete          *bool    `json:"enableSoftDelete,omitempty"`
	SoftDeleteRetentionInDays int32    `json:"softDeleteRetentionInDays,omitempty"`
	EnablePurgeProtection     *bool    `json:"enablePurgeProtection,omitempty"`
	CreateMode                string   `json:"createMode,omitempty"`
	PublicNetworkAccess       string   `json:"publicNetworkAccess,omitempty"`
	ProvisioningState         string   `json:"provisioningState,omitempty"`
	StatusMessage             string   `json:"statusMessage,omitempty"`
}

// managedHSMs is the Managed HSM pool store, read by the resource registry so
// a pool appears in the generic resource lists like any other tracked
// Microsoft.KeyVault resource.
var managedHSMs sim.Store[ManagedHSM]

func managedHSMID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/managedHSMs/%s",
		sub, rg, name)
}

func registerKeyVaultManagedHSM(srv *sim.Server) {
	managedHSMs = sim.MakeStore[ManagedHSM](srv.DB(), "keyvault_managed_hsms")

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.KeyVault/managedHSMs"

	// ManagedHsms_CreateOrUpdate / ManagedHsms_Update. Both spellings settle
	// the pool synchronously: the simulator has no provisioning work to wait
	// on, so it reports the state it actually reached.
	upsert := func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		id := managedHSMID(sub, rg, name)

		var req ManagedHSM
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent",
				"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		existing, exists := managedHSMs.Get(id)
		if r.Method == http.MethodPatch && !exists {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Managed HSM %q not found in resource group %q.", name, rg)
			return
		}
		if r.Method == http.MethodPut && req.Location == "" {
			AzureError(w, "InvalidRequestContent",
				"The 'location' property is required.", http.StatusBadRequest)
			return
		}

		hsm := ManagedHSM{
			ID:         id,
			Name:       name,
			Type:       "Microsoft.KeyVault/managedHSMs",
			Location:   req.Location,
			Sku:        req.Sku,
			Tags:       req.Tags,
			Properties: req.Properties,
		}
		if r.Method == http.MethodPatch {
			// PATCH carries only what changes; everything else stays as stored.
			hsm.Location = existing.Location
			if req.Sku == nil {
				hsm.Sku = existing.Sku
			}
			if req.Tags == nil {
				hsm.Tags = existing.Tags
			}
			hsm.Properties = existing.Properties
			if req.Properties.PublicNetworkAccess != "" {
				hsm.Properties.PublicNetworkAccess = req.Properties.PublicNetworkAccess
			}
			if req.Properties.EnablePurgeProtection != nil {
				hsm.Properties.EnablePurgeProtection = req.Properties.EnablePurgeProtection
			}
		}
		if hsm.Sku == nil {
			hsm.Sku = &ManagedHSMSku{Family: "B", Name: "Standard_B1"}
		}
		if hsm.Properties.TenantID == "" {
			hsm.Properties.TenantID = "00000000-0000-0000-0000-000000000000"
		}
		if hsm.Properties.SoftDeleteRetentionInDays == 0 {
			hsm.Properties.SoftDeleteRetentionInDays = 90
		}
		if hsm.Properties.CreateMode == "" {
			hsm.Properties.CreateMode = "default"
		}
		hsm.Properties.HsmURI = fmt.Sprintf("https://%s.managedhsm.azure.net/", name)
		hsm.Properties.ProvisioningState = "Succeeded"
		managedHSMs.Put(id, hsm)

		// ManagedHsms_CreateOrUpdate and ManagedHsms_Update declare 200 and
		// 202 only: the pool settles here with no provisioning work to wait on,
		// so the synchronous 200 carrying the settled resource is the terminal
		// answer the client's long-running-operation poller reads.
		sim.WriteJSON(w, http.StatusOK, hsm)
	}
	srv.HandleFunc("PUT "+armBase+"/{name}", upsert)
	srv.HandleFunc("PATCH "+armBase+"/{name}", upsert)

	// ManagedHsms_Get
	srv.HandleFunc("GET "+armBase+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		hsm, ok := managedHSMs.Get(managedHSMID(sub, rg, name))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Managed HSM %q not found in resource group %q.", name, rg)
			return
		}
		sim.WriteJSON(w, http.StatusOK, hsm)
	})

	// ManagedHsms_Delete
	srv.HandleFunc("DELETE "+armBase+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		id := managedHSMID(sub, rg, name)
		hsm, ok := managedHSMs.Get(id)
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		managedHSMs.Delete(id)
		// A pool with soft delete on is retired rather than destroyed, and the
		// deleted collection is what a caller reads to find it and purge it.
		// Read the pool before the delete: its location addresses the record.
		mhsmSoftDelete(sub, rg, name, hsm)
		w.WriteHeader(http.StatusOK)
	})

	// ManagedHsms_ListByResourceGroup and ManagedHsms_ListBySubscription. A
	// scope holding no pool answers an empty collection, which is what real
	// Azure answers and what a client enumerating the provider's resources
	// needs in order to tell "none" from "no such route".
	list := func(w http.ResponseWriter, r *http.Request) {
		prefix := fmt.Sprintf("/subscriptions/%s/", sim.PathParam(r, "subscriptionId"))
		if rg := sim.PathParam(r, "resourceGroupName"); rg != "" {
			prefix = managedHSMID(sim.PathParam(r, "subscriptionId"), rg, "")
		}
		all := managedHSMs.Filter(func(h ManagedHSM) bool {
			return strings.HasPrefix(h.ID, prefix)
		})
		if all == nil {
			all = []ManagedHSM{}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		page, next := armPage(r, all)
		if page == nil {
			page = []ManagedHSM{}
		}
		out := map[string]any{"value": page}
		if next != "" {
			out["nextLink"] = armNextLink(r, next)
		}
		sim.WriteJSON(w, http.StatusOK, out)
	}
	srv.HandleFunc("GET "+armBase, list)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/managedHSMs", list)
	registerManagedHSMTail(srv, armBase)
}
