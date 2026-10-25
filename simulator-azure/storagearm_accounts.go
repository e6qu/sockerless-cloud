package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerStorageAccountKeysAndList(srv *sim.Server, armBase string, storageAccounts sim.Store[StorageAccount]) {
	// POST - List storage account keys (azurerm provider calls this after creating a storage account)
	srv.HandleFunc("POST "+armBase+"/storageAccounts/{accountName}/listKeys", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "accountName")

		acctID := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", name)
		if _, ok := storageAccounts.Get(acctID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Storage/storageAccounts/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		sim.WriteJSON(w, http.StatusOK, storageAccountKeysBody(acctID))
	})

	// GET - List storage accounts at subscription level (azurerm provider checks name uniqueness)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/storageAccounts", func(w http.ResponseWriter, r *http.Request) {
		all := storageAccounts.Filter(func(sa StorageAccount) bool { return true })
		for i := range all {
			applyStorageAccountEndpoints(r, &all[i])
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": all,
		})
	})
}

func registerStorageAccountCRUD(srv *sim.Server, armBase string, storageAccounts sim.Store[StorageAccount]) {
	// PUT - Create or update storage account
	srv.HandleFunc("PUT "+armBase+"/storageAccounts/{accountName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "accountName")

		var req StorageAccount
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Location == "" {
			AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}

		resourceID := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", name)

		kind := req.Kind
		if kind == "" {
			kind = "StorageV2"
		}

		sku := req.Sku
		if sku == nil {
			sku = &StorageSku{Name: "Standard_LRS"}
		}
		if sku.Tier == "" {
			sku.Tier = storageSkuTierFromName(sku.Name)
		}

		// Azure defaults supportsHttpsTrafficOnly to true when the
		// request omits it; the provider reads it back as
		// enable_https_traffic_only.
		httpsOnly := req.Properties.SupportsHttpsTrafficOnly
		if httpsOnly == nil {
			v := true
			httpsOnly = &v
		}
		// Azure defaults minimumTlsVersion to TLS1_2 for new accounts; the
		// provider reads it back as min_tls_version.
		minTLS := req.Properties.MinimumTLSVersion
		if minTLS == "" {
			minTLS = "TLS1_2"
		}

		acct := StorageAccount{
			ID:       resourceID,
			Name:     name,
			Type:     "Microsoft.Storage/storageAccounts",
			Location: req.Location,
			Kind:     kind,
			Sku:      sku,
			Tags:     req.Tags,
			Properties: StorageAccountProperties{
				ProvisioningState:        "Succeeded",
				PrimaryLocation:          req.Location,
				SupportsHttpsTrafficOnly: httpsOnly,
				MinimumTLSVersion:        minTLS,
				AccessTier:               req.Properties.AccessTier,
				AllowBlobPublicAccess:    req.Properties.AllowBlobPublicAccess,
				AllowSharedKeyAccess:     req.Properties.AllowSharedKeyAccess,
				PublicNetworkAccess:      req.Properties.PublicNetworkAccess,
				IsHnsEnabled:             req.Properties.IsHnsEnabled,
				LargeFileSharesState:     req.Properties.LargeFileSharesState,
				Encryption:               req.Properties.Encryption,
				NetworkAcls:              req.Properties.NetworkAcls,
				CreationTime:             time.Now().UTC().Format(time.RFC3339),
			},
		}
		applyStorageAccountEndpoints(r, &acct)

		storageAccounts.Put(resourceID, acct)

		// go-azure-sdk expects 200 for create (it treats this as a sync LRO)
		sim.WriteJSON(w, http.StatusOK, acct)
	})

	// PATCH - Update storage account
	srv.HandleFunc("PATCH "+armBase+"/storageAccounts/{accountName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "accountName")
		resourceID := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", name)

		var req struct {
			Sku        *StorageSku        `json:"sku,omitempty"`
			Kind       string             `json:"kind,omitempty"`
			Tags       *map[string]string `json:"tags,omitempty"`
			Properties map[string]any     `json:"properties,omitempty"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Sku != nil && req.Sku.Tier == "" {
			req.Sku.Tier = storageSkuTierFromName(req.Sku.Name)
		}
		ok := storageAccounts.Update(resourceID, func(acct *StorageAccount) {
			if req.Sku != nil {
				acct.Sku = req.Sku
			}
			if req.Kind != "" {
				acct.Kind = req.Kind
			}
			if req.Tags != nil {
				acct.Tags = *req.Tags
			}
			// Merge the updatable nested properties a PATCH carries (accessTier,
			// public-access / TLS knobs, encryption, networkAcls, …) into the
			// stored account — only the keys actually present, preserving the
			// rest. Without this, real-azurerm property updates were silently
			// dropped and the next plan showed drift.
			if req.Properties != nil {
				pb, _ := json.Marshal(req.Properties)
				var patch StorageAccountProperties
				_ = json.Unmarshal(pb, &patch)
				p := &acct.Properties
				if _, ok := req.Properties["accessTier"]; ok {
					p.AccessTier = patch.AccessTier
				}
				if _, ok := req.Properties["minimumTlsVersion"]; ok {
					p.MinimumTLSVersion = patch.MinimumTLSVersion
				}
				if _, ok := req.Properties["publicNetworkAccess"]; ok {
					p.PublicNetworkAccess = patch.PublicNetworkAccess
				}
				if _, ok := req.Properties["largeFileSharesState"]; ok {
					p.LargeFileSharesState = patch.LargeFileSharesState
				}
				if _, ok := req.Properties["supportsHttpsTrafficOnly"]; ok {
					p.SupportsHttpsTrafficOnly = patch.SupportsHttpsTrafficOnly
				}
				if _, ok := req.Properties["allowBlobPublicAccess"]; ok {
					p.AllowBlobPublicAccess = patch.AllowBlobPublicAccess
				}
				if _, ok := req.Properties["allowSharedKeyAccess"]; ok {
					p.AllowSharedKeyAccess = patch.AllowSharedKeyAccess
				}
				if _, ok := req.Properties["isHnsEnabled"]; ok {
					p.IsHnsEnabled = patch.IsHnsEnabled
				}
				if _, ok := req.Properties["encryption"]; ok {
					p.Encryption = patch.Encryption
				}
				if _, ok := req.Properties["networkAcls"]; ok {
					p.NetworkAcls = patch.NetworkAcls
				}
			}
			acct.Properties.ProvisioningState = "Succeeded"
			applyStorageAccountEndpoints(r, acct)
		})
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Storage/storageAccounts/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		acct, _ := storageAccounts.Get(resourceID)
		sim.WriteJSON(w, http.StatusOK, acct)
	})

	// GET - Get storage account
	srv.HandleFunc("GET "+armBase+"/storageAccounts/{accountName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "accountName")

		resourceID := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", name)

		acct, ok := storageAccounts.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Storage/storageAccounts/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		sim.WriteJSON(w, http.StatusOK, acct)
	})

	// DELETE - Delete storage account
	srv.HandleFunc("DELETE "+armBase+"/storageAccounts/{accountName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "accountName")

		resourceID := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", name)

		if storageAccounts.Delete(resourceID) {
			storageDropAccountKeyGens(resourceID)
			tablePrefix := resourceID + "/tableServices/default/tables/"
			for _, t := range storageTables.List() {
				if strings.HasPrefix(t.ID, tablePrefix) {
					storageTables.Delete(t.ID)
					deleteTableDataPlaneProjection(name, t.Name)
				}
			}
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

type StorageAccount struct {
	ID         string                   `json:"id"`
	Name       string                   `json:"name"`
	Type       string                   `json:"type"`
	Location   string                   `json:"location"`
	Kind       string                   `json:"kind,omitempty"`
	Sku        *StorageSku              `json:"sku,omitempty"`
	Tags       map[string]string        `json:"tags,omitempty"`
	Properties StorageAccountProperties `json:"properties"`
}

type StorageSku struct {
	Name string `json:"name"`
	Tier string `json:"tier,omitempty"`
}

// storageSkuTierFromName derives the ARM `sku.tier` value from a SKU
// name like "Standard_LRS" or "Premium_LRS". Azure populates `tier`
// from the leading segment of the SKU name; the azurerm/azurestack
// providers read it back to set `account_tier`.
func storageSkuTierFromName(name string) string {
	if i := strings.Index(name, "_"); i > 0 {
		return name[:i]
	}
	switch name {
	case "":
		return "Standard"
	default:
		return name
	}
}

type StorageAccountProperties struct {
	ProvisioningState        string                   `json:"provisioningState"`
	PrimaryLocation          string                   `json:"primaryLocation,omitempty"`
	SupportsHttpsTrafficOnly *bool                    `json:"supportsHttpsTrafficOnly,omitempty"`
	MinimumTLSVersion        string                   `json:"minimumTlsVersion,omitempty"`
	AccessTier               string                   `json:"accessTier,omitempty"`
	AllowBlobPublicAccess    *bool                    `json:"allowBlobPublicAccess,omitempty"`
	AllowSharedKeyAccess     *bool                    `json:"allowSharedKeyAccess,omitempty"`
	PublicNetworkAccess      string                   `json:"publicNetworkAccess,omitempty"`
	IsHnsEnabled             *bool                    `json:"isHnsEnabled,omitempty"`
	LargeFileSharesState     string                   `json:"largeFileSharesState,omitempty"`
	Encryption               json.RawMessage          `json:"encryption,omitempty"`
	NetworkAcls              json.RawMessage          `json:"networkAcls,omitempty"`
	PrimaryEndpoints         *StoragePrimaryEndpoints `json:"primaryEndpoints,omitempty"`
	CreationTime             string                   `json:"creationTime,omitempty"`
}

type StoragePrimaryEndpoints struct {
	File  string `json:"file,omitempty"`
	Blob  string `json:"blob,omitempty"`
	Table string `json:"table,omitempty"`
	Queue string `json:"queue,omitempty"`
	Web   string `json:"web,omitempty"`
	Dfs   string `json:"dfs,omitempty"`
}

func applyStorageAccountEndpoints(r *http.Request, acct *StorageAccount) {
	acct.Properties.PrimaryEndpoints = &StoragePrimaryEndpoints{
		File:  azureStorageEndpointURL(r, acct.Name, "file"),
		Blob:  azureStorageEndpointURL(r, acct.Name, "blob"),
		Table: azureStorageEndpointURL(r, acct.Name, "table"),
		Queue: azureStorageEndpointURL(r, acct.Name, "queue"),
		Web:   azureStorageEndpointURL(r, acct.Name, "web"),
		Dfs:   azureStorageEndpointURL(r, acct.Name, "dfs"),
	}
}
