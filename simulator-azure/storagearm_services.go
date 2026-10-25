package main

import (
	"fmt"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerStorageServiceProperties(srv *sim.Server, armBase string, storageAccounts sim.Store[StorageAccount]) {
	// Storage service properties handlers — azurerm provider polls these after creating a storage account.
	// Each service (file, blob, queue, table) has a default configuration endpoint.
	storageServiceHandler := func(serviceName, serviceType string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			name := sim.PathParam(r, "accountName")

			acctID := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", name)
			if _, ok := storageAccounts.Get(acctID); !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"The Resource 'Microsoft.Storage/storageAccounts/%s' under resource group '%s' was not found.", name, rg)
				return
			}

			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"id":   fmt.Sprintf("%s/%s/default", acctID, serviceName),
				"name": "default",
				"type": serviceType,
				"properties": map[string]any{
					"cors": map[string]any{
						"corsRules": []any{},
					},
				},
			})
		}
	}

	// fileServices/default carries the account-wide File service configuration
	// (cors, shareDeleteRetentionPolicy, protocolSettings) an administrator
	// writes through `PUT .../fileServices/default`. Reading it back here is
	// what makes that write observable, and the Files data plane's Delete Share
	// and Restore Share read the share delete-retention policy from it.
	srv.HandleFunc("GET "+armBase+"/storageAccounts/{accountName}/fileServices/default", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")
		acctID := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", account)
		if _, ok := storageAccounts.Get(acctID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Storage/storageAccounts/%s' under resource group '%s' was not found.", account, rg)
			return
		}
		props, _ := azStorageServiceProps.Get(acctID + "/fileServices")
		sim.WriteJSON(w, http.StatusOK, fileServiceResponse(acctID+"/fileServices/default", props))
	})
	srv.HandleFunc("GET "+armBase+"/storageAccounts/{accountName}/queueServices/default",
		storageServiceHandler("queueServices", "Microsoft.Storage/storageAccounts/queueServices"))
	srv.HandleFunc("GET "+armBase+"/storageAccounts/{accountName}/tableServices/default",
		storageServiceHandler("tableServices", "Microsoft.Storage/storageAccounts/tableServices"))

	// blobServices/default carries writable properties (deleteRetentionPolicy,
	// isVersioningEnabled, changeFeed, cors, restorePolicy) that
	// terraform's azurerm_storage_account.blob_properties round-trips. PUT
	// stores the supplied properties keyed by account; GET returns them.
	blobServiceProps := sim.MakeStore[map[string]any](srv.DB(), "blob_service_properties")
	// The Blob data plane reads the container delete-retention policy from here:
	// Azure configures container soft delete on this ARM resource, not on the
	// data-plane service-properties document, and Restore Container is governed
	// by it.
	blobARMServiceProps = blobServiceProps
	blobServicePath := armBase + "/storageAccounts/{accountName}/blobServices/default"
	blobServiceResourceID := func(r *http.Request) (acctID, resourceID, account, rg string) {
		sub := sim.PathParam(r, "subscriptionId")
		rg = sim.PathParam(r, "resourceGroupName")
		account = sim.PathParam(r, "accountName")
		acctID = armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", account)
		return acctID, acctID + "/blobServices/default", account, rg
	}
	srv.HandleFunc("PUT "+blobServicePath, func(w http.ResponseWriter, r *http.Request) {
		acctID, resourceID, account, rg := blobServiceResourceID(r)
		if _, ok := storageAccounts.Get(acctID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Storage/storageAccounts/%s' under resource group '%s' was not found.", account, rg)
			return
		}
		var req struct {
			Properties map[string]any `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		props := req.Properties
		if props == nil {
			props = map[string]any{}
		}
		// Blob soft delete is one configuration with two APIs onto it. It
		// lives in the data-plane service-properties document, which is what
		// a blob delete consults, so an ARM write goes there rather than into
		// a second copy that only ARM can see.
		if policy, ok := blobARMDeleteRetentionPolicy(props); ok {
			blobSetDeleteRetentionPolicy(account, policy)
		}
		delete(props, "deleteRetentionPolicy")
		// CORS rules are likewise the data-plane document's, which the
		// service evaluates every cross-origin request against.
		if rules, ok := blobARMCors(props); ok {
			blobSetCors(account, rules)
		}
		delete(props, "cors")
		blobServiceProps.Put(acctID, props)
		sim.WriteJSON(w, http.StatusOK,
			blobServiceResponse(resourceID, blobServiceARMProps(account, props)))
	})
	srv.HandleFunc("GET "+blobServicePath, func(w http.ResponseWriter, r *http.Request) {
		acctID, resourceID, account, rg := blobServiceResourceID(r)
		if _, ok := storageAccounts.Get(acctID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Storage/storageAccounts/%s' under resource group '%s' was not found.", account, rg)
			return
		}
		props, ok := blobServiceProps.Get(acctID)
		if !ok {
			props = map[string]any{}
		}
		sim.WriteJSON(w, http.StatusOK,
			blobServiceResponse(resourceID, blobServiceARMProps(account, props)))
	})
}

// fileServiceResponse builds the ARM fileServices/default resource envelope
// around the properties an administrator wrote. Real Azure always surfaces a
// cors element and a shareDeleteRetentionPolicy, reporting the policy disabled
// when nobody has enabled it.
func fileServiceResponse(resourceID string, props map[string]any) map[string]any {
	merged := map[string]any{
		"cors": map[string]any{"corsRules": []any{}},
		"shareDeleteRetentionPolicy": map[string]any{
			"enabled": false,
			"days":    0,
		},
	}
	for k, v := range props {
		merged[k] = v
	}
	return map[string]any{
		"id":         resourceID,
		"name":       "default",
		"type":       "Microsoft.Storage/storageAccounts/fileServices",
		"properties": merged,
	}
}

// blobServiceResponse builds the ARM blobServices/default resource envelope,
// merging the stored writable properties over a default cors block (real Azure
// always surfaces a cors element).
func blobServiceResponse(resourceID string, props map[string]any) map[string]any {
	merged := map[string]any{"cors": map[string]any{"corsRules": []any{}}}
	for k, v := range props {
		merged[k] = v
	}
	return map[string]any{
		"id":         resourceID,
		"name":       "default",
		"type":       "Microsoft.Storage/storageAccounts/blobServices",
		"properties": merged,
	}
}
