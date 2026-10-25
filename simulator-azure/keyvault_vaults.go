package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerKeyVaultVaults(srv *sim.Server, armBase string) {
	// ARM control plane — vault CRUD.
	srv.HandleFunc("PUT "+armBase+"/vaults/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		var req KeyVault
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent",
				"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Location == "" {
			AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s",
			sub, rg, name)
		if req.Properties.Sku == nil {
			req.Properties.Sku = &KeyVaultSku{Family: "A", Name: "standard"}
		}
		if req.Properties.TenantID == "" {
			req.Properties.TenantID = "00000000-0000-0000-0000-000000000000"
		}
		// soft-delete retention echoes the requested value; Azure
		// defaults it to 90 days only when the request omits it.
		if req.Properties.SoftDeleteRetentionInDays == 0 {
			req.Properties.SoftDeleteRetentionInDays = 90
		}
		if req.Properties.PublicNetworkAccess == "" {
			req.Properties.PublicNetworkAccess = "Enabled"
		}
		req.Properties.VaultURI = azureKeyVaultEndpointURL(r, name)
		req.Properties.ProvisioningState = "Succeeded"

		vault := KeyVault{
			ID:         resourceID,
			Name:       name,
			Type:       "Microsoft.KeyVault/vaults",
			Location:   req.Location,
			Tags:       req.Tags,
			Properties: req.Properties,
		}
		keyVaults.Put(resourceID, vault)
		deleteDeletedVaultsFor(sub, name)
		sim.WriteJSON(w, http.StatusOK, vault)
	})

	srv.HandleFunc("PATCH "+armBase+"/vaults/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s",
			sub, rg, name)
		v, ok := keyVaults.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Vault %q not found in resource group %q.", name, rg)
			return
		}
		var req struct {
			Tags       *map[string]string `json:"tags,omitempty"`
			Properties struct {
				TenantID                     string                  `json:"tenantId,omitempty"`
				Sku                          *KeyVaultSku            `json:"sku,omitempty"`
				AccessPolicies               *[]KeyVaultAccessPolicy `json:"accessPolicies,omitempty"`
				EnabledForDeployment         *bool                   `json:"enabledForDeployment,omitempty"`
				EnabledForDiskEncryption     *bool                   `json:"enabledForDiskEncryption,omitempty"`
				EnabledForTemplateDeployment *bool                   `json:"enabledForTemplateDeployment,omitempty"`
				EnableSoftDelete             *bool                   `json:"enableSoftDelete,omitempty"`
				EnablePurgeProtection        *bool                   `json:"enablePurgeProtection,omitempty"`
				EnableRbacAuthorization      *bool                   `json:"enableRbacAuthorization,omitempty"`
				NetworkAcls                  *KeyVaultNetworkAcls    `json:"networkAcls,omitempty"`
				PublicNetworkAccess          *string                 `json:"publicNetworkAccess,omitempty"`
			} `json:"properties,omitempty"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent",
				"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Tags != nil {
			v.Tags = *req.Tags
		}
		if req.Properties.TenantID != "" {
			v.Properties.TenantID = req.Properties.TenantID
		}
		if req.Properties.Sku != nil {
			v.Properties.Sku = req.Properties.Sku
		}
		if req.Properties.AccessPolicies != nil {
			v.Properties.AccessPolicies = *req.Properties.AccessPolicies
		}
		if req.Properties.EnabledForDeployment != nil {
			v.Properties.EnabledForDeployment = *req.Properties.EnabledForDeployment
		}
		if req.Properties.EnabledForDiskEncryption != nil {
			v.Properties.EnabledForDiskEncryption = *req.Properties.EnabledForDiskEncryption
		}
		if req.Properties.EnabledForTemplateDeployment != nil {
			v.Properties.EnabledForTemplateDeployment = *req.Properties.EnabledForTemplateDeployment
		}
		if req.Properties.EnableSoftDelete != nil {
			v.Properties.EnableSoftDelete = *req.Properties.EnableSoftDelete
		}
		if req.Properties.EnablePurgeProtection != nil {
			v.Properties.EnablePurgeProtection = *req.Properties.EnablePurgeProtection
		}
		if req.Properties.EnableRbacAuthorization != nil {
			v.Properties.EnableRbacAuthorization = *req.Properties.EnableRbacAuthorization
		}
		if req.Properties.NetworkAcls != nil {
			v.Properties.NetworkAcls = req.Properties.NetworkAcls
		}
		if req.Properties.PublicNetworkAccess != nil {
			v.Properties.PublicNetworkAccess = *req.Properties.PublicNetworkAccess
		}
		v.Properties.VaultURI = azureKeyVaultEndpointURL(r, name)
		v.Properties.ProvisioningState = "Succeeded"
		keyVaults.Put(resourceID, v)
		sim.WriteJSON(w, http.StatusOK, v)
	})

	srv.HandleFunc("GET "+armBase+"/vaults/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s",
			sub, rg, name)
		v, ok := keyVaults.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Vault %q not found in resource group %q.", name, rg)
			return
		}
		v.Properties.VaultURI = azureKeyVaultEndpointURL(r, name)
		sim.WriteJSON(w, http.StatusOK, v)
	})

	srv.HandleFunc("DELETE "+armBase+"/vaults/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s",
			sub, rg, name)
		if v, ok := keyVaults.Get(resourceID); ok {
			now := time.Now().UTC()
			purgeAt := now.Add(90 * 24 * time.Hour)
			deletedVaults.Put(deletedVaultID(sub, v.Location, name), DeletedKeyVault{
				ID:   deletedVaultID(sub, v.Location, name),
				Name: name,
				Type: "Microsoft.KeyVault/deletedVaults",
				Properties: DeletedKeyVaultProperties{
					VaultID:            resourceID,
					Location:           v.Location,
					DeletionDate:       now.Format(time.RFC3339),
					ScheduledPurgeDate: purgeAt.Format(time.RFC3339),
				},
			})
		}
		keyVaults.Delete(resourceID)
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("GET "+armBase+"/vaults", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/",
			sub, rg)
		all := keyVaults.Filter(func(v KeyVault) bool {
			return strings.HasPrefix(v.ID, prefix)
		})
		if all == nil {
			all = []KeyVault{}
		}
		for i := range all {
			all[i].Properties.VaultURI = azureKeyVaultEndpointURL(r, all[i].Name)
		}
		page, next, pageOK := armQueryPage(w, r, all, func(x KeyVault) string { return x.ID })
		if !pageOK {
			return
		}
		out := map[string]any{"value": page}
		if next != "" {
			out["nextLink"] = armNextLink(r, next)
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})

	// Subscription-scoped vault list — terraform-provider-azurerm
	// populates a per-subscription cache by walking this list when it
	// needs to map a vault URL back to a resource ID (e.g. resolving
	// azurerm_key_vault_secret's KV URI on every plan refresh).
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/vaults", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		prefix := fmt.Sprintf("/subscriptions/%s/", sub)
		all := keyVaults.Filter(func(v KeyVault) bool {
			return strings.HasPrefix(v.ID, prefix)
		})
		if all == nil {
			all = []KeyVault{}
		}
		for i := range all {
			all[i].Properties.VaultURI = azureKeyVaultEndpointURL(r, all[i].Name)
		}
		page, next, pageOK := armQueryPage(w, r, all, func(x KeyVault) string { return x.ID })
		if !pageOK {
			return
		}
		out := map[string]any{"value": page}
		if next != "" {
			out["nextLink"] = armNextLink(r, next)
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})

	srv.HandleFunc("PUT "+armBase+"/vaults/{name}/accessPolicies/{operationKind}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		operation := sim.PathParam(r, "operationKind")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s",
			sub, rg, name)
		v, ok := keyVaults.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Vault %q not found in resource group %q.", name, rg)
			return
		}
		var req struct {
			Properties struct {
				AccessPolicies []KeyVaultAccessPolicy `json:"accessPolicies"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent",
				"Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var policies []KeyVaultAccessPolicy
		switch operation {
		case "add":
			policies = keyVaultAddAccessPolicies(v.Properties.AccessPolicies, req.Properties.AccessPolicies)
		case "replace":
			policies = keyVaultReplaceAccessPolicies(v.Properties.AccessPolicies, req.Properties.AccessPolicies)
		case "remove":
			policies = keyVaultRemoveAccessPolicies(v.Properties.AccessPolicies, req.Properties.AccessPolicies)
		default:
			AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"access policy operation %q is invalid", operation)
			return
		}
		v.Properties.AccessPolicies = policies
		keyVaults.Put(resourceID, v)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":       resourceID + "/accessPolicies/" + operation,
			"name":     operation,
			"type":     "Microsoft.KeyVault/vaults/accessPolicies",
			"location": v.Location,
			"properties": map[string]any{
				"accessPolicies": v.Properties.AccessPolicies,
			},
		})
	})

	// GET /subscriptions/{sub}/providers/Microsoft.KeyVault/locations/{location}/deletedVaults/{name}
	// terraform-provider-azurerm queries this pre-create to detect
	// recoverable soft-deleted vaults (KV soft-delete is enabled by
	// default and has a 90-day recovery window). The sim doesn't model
	// vault-level soft-delete, so the truthful response is 404 in the
	// Azure envelope (i.e. not the Go default 404 page that breaks the
	// provider's error parser). Same for the list variant.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedVaults/{name}", func(w http.ResponseWriter, r *http.Request) {
		id := deletedVaultID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "location"), sim.PathParam(r, "name"))
		v, ok := deletedVaults.Get(id)
		if !ok {
			AzureErrorf(w, "VaultNotFound", http.StatusNotFound,
				"The vault %q was not found.", sim.PathParam(r, "name"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, v)
	})

	// POST .../deletedVaults/{name}/purge accepts a purge operation. Azure
	// permits either immediate 200 completion or 202 with a Location poll URI;
	// use the latter so generated clients retain a truthful LRO lifecycle.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedVaults/{name}/purge", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		location := sim.PathParam(r, "location")
		name := sim.PathParam(r, "name")
		deletedVaults.Delete(deletedVaultID(sub, location, name))
		operationPath := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.KeyVault/locations/%s/deletedVaults/%s/purge/operation", sub, location, name)
		w.Header().Set("Location", azureRequestScheme(r)+"://"+r.Host+operationPath+"?api-version="+r.URL.Query().Get("api-version"))
		w.WriteHeader(http.StatusAccepted)
	})
	// GET .../purge/operation is the terminal Location returned by a completed
	// purge. A zero-length 200 is the Azure SDK's completion signal for this
	// documented Location-based LRO form.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedVaults/{name}/purge/operation", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/deletedVaults", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		prefix := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.KeyVault/", sub)
		all := deletedVaults.Filter(func(v DeletedKeyVault) bool {
			return strings.HasPrefix(v.ID, prefix)
		})
		if all == nil {
			all = []DeletedKeyVault{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
	})
}

func deletedVaultID(sub, location, name string) string {
	return fmt.Sprintf("/subscriptions/%s/providers/Microsoft.KeyVault/locations/%s/deletedVaults/%s", sub, location, name)
}

func deleteDeletedVaultsFor(sub, name string) {
	if deletedVaults == nil {
		return
	}
	prefix := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.KeyVault/locations/", sub)
	for _, v := range deletedVaults.List() {
		if strings.HasPrefix(v.ID, prefix) && v.Name == name {
			deletedVaults.Delete(v.ID)
		}
	}
}

func keyVaultResourceID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s", sub, rg, name)
}

// handleKeyVaultCheckNameAvailability reports whether a vault name is free.
// A name is taken when an active vault in the subscription already uses it.
func handleKeyVaultCheckNameAvailability(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		AzureError(w, "BadRequest", "name is required", http.StatusBadRequest)
		return
	}
	prefix := fmt.Sprintf("/subscriptions/%s/", sub)
	taken := false
	for _, v := range keyVaults.List() {
		if strings.HasPrefix(v.ID, prefix) && strings.EqualFold(v.Name, req.Name) {
			taken = true
			break
		}
	}
	resp := map[string]any{"nameAvailable": !taken}
	if taken {
		resp["reason"] = "AlreadyExists"
		resp["message"] = fmt.Sprintf("The vault name %q is already in use.", req.Name)
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}
