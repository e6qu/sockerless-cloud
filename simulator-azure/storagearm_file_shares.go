package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerFileSharesARM(srv *sim.Server, armBase string, storageAccounts sim.Store[StorageAccount], fileShares sim.Store[FileShare]) {
	// PUT - Create or update file share
	srv.HandleFunc("PUT "+armBase+"/storageAccounts/{accountName}/fileServices/default/shares/{shareName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")
		shareName := sim.PathParam(r, "shareName")

		var req FileShare
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Verify storage account exists
		acctID := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", account)
		if _, ok := storageAccounts.Get(acctID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Storage/storageAccounts/%s' under resource group '%s' was not found.", account, rg)
			return
		}

		shareID := fmt.Sprintf("%s/fileServices/default/shares/%s", acctID, shareName)

		quota := req.Properties.ShareQuota
		if quota == 0 {
			quota = 5120
		}

		accessTier := req.Properties.AccessTier
		if accessTier == "" {
			accessTier = "TransactionOptimized"
		}

		protocols := req.Properties.EnabledProtocols
		if protocols == "" {
			protocols = "SMB"
		}

		signedIdentifiers := req.Properties.SignedIdentifiers
		if existing, ok := fileShares.Get(shareID); ok && signedIdentifiers == nil {
			signedIdentifiers = existing.Properties.SignedIdentifiers
		}

		share := FileShare{
			ID:   shareID,
			Name: shareName,
			Type: "Microsoft.Storage/storageAccounts/fileServices/shares",
			Etag: fmt.Sprintf("\"0x%s\"", randomSuffix(16)),
			Properties: FileShareProperties{
				ShareQuota:        quota,
				AccessTier:        accessTier,
				EnabledProtocols:  protocols,
				LastModifiedTime:  time.Now().UTC().Format(time.RFC3339),
				LeaseStatus:       "Unlocked",
				LeaseState:        "Available",
				Metadata:          req.Properties.Metadata,
				SignedIdentifiers: signedIdentifiers,
			},
		}

		fileShares.Put(shareID, share)
		// A share is one resource with two faces: the ARM entity created here
		// and the Files data-plane share a client reads and writes. Project the
		// ARM create onto the data plane so a file written to it lands in the
		// share's backing directory — the directory a Container Apps workload
		// mounting this share reads.
		if err := upsertFileShareDataPlaneProjection(account, shareName, quota, req.Properties.Metadata); err != nil {
			AzureErrorf(w, "InternalError", http.StatusInternalServerError,
				"Failed to materialize file share %q: %v", shareName, err)
			return
		}
		data, _ := fileShareData.Get(fileShareKey(account, shareName))
		data.ACLs = signedIdentifiers
		fileShareData.Put(fileShareKey(account, shareName), data)

		// go-azure-sdk expects 200 for sync creates
		sim.WriteJSON(w, http.StatusOK, share)
	})

	// GET - Get file share
	srv.HandleFunc("GET "+armBase+"/storageAccounts/{accountName}/fileServices/default/shares/{shareName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")
		shareName := sim.PathParam(r, "shareName")

		shareID := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", account, "fileServices", "default", "shares", shareName)

		share, ok := fileShares.Get(shareID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The file share '%s' was not found.", shareName)
			return
		}

		sim.WriteJSON(w, http.StatusOK, share)
	})

	// DELETE - Delete file share
	srv.HandleFunc("DELETE "+armBase+"/storageAccounts/{accountName}/fileServices/default/shares/{shareName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")
		shareName := sim.PathParam(r, "shareName")

		shareID := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", account, "fileServices", "default", "shares", shareName)

		if fileShares.Delete(shareID) {
			if err := deleteFileShareDataPlaneProjection(account, shareName); err != nil {
				AzureErrorf(w, "InternalError", http.StatusInternalServerError,
					"Failed to delete file share %q contents: %v", shareName, err)
				return
			}
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// GET - List file shares
	srv.HandleFunc("GET "+armBase+"/storageAccounts/{accountName}/fileServices/default/shares", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")

		prefix := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", account, "fileServices", "default", "shares") + "/"

		filtered := fileShares.Filter(func(s FileShare) bool {
			return strings.HasPrefix(s.ID, prefix)
		})

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": filtered,
		})
	})
}

type FileShare struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Type       string              `json:"type"`
	Etag       string              `json:"etag,omitempty"`
	Properties FileShareProperties `json:"properties"`
}

type FileShareProperties struct {
	ShareQuota        int                     `json:"shareQuota,omitempty"`
	AccessTier        string                  `json:"accessTier,omitempty"`
	EnabledProtocols  string                  `json:"enabledProtocols,omitempty"`
	LastModifiedTime  string                  `json:"lastModifiedTime,omitempty"`
	LeaseStatus       string                  `json:"leaseStatus,omitempty"`
	LeaseState        string                  `json:"leaseState,omitempty"`
	Metadata          map[string]string       `json:"metadata,omitempty"`
	SignedIdentifiers []TableSignedIdentifier `json:"signedIdentifiers,omitempty"`
}

// upsertFileShareDataPlaneProjection makes an ARM-created share readable and
// writable on the Files data plane: the share row the data plane resolves, and
// the backing directory its files live in.
func upsertFileShareDataPlaneProjection(account, share string, quota int, metadata map[string]string) error {
	if !isStoragePathSegment(account) || !isStoragePathSegment(share) {
		return fmt.Errorf("invalid storage account or share name")
	}
	key := fileShareKey(account, share)
	if existing, ok := fileShareData.Get(key); ok {
		existing.Quota = quota
		existing.Metadata = metadata
		fileShareData.Put(key, existing)
		return nil
	}
	if err := resetFileShareHostDir(account, share); err != nil {
		return err
	}
	fileShareData.Put(key, FileShareData{
		Account:  account,
		Name:     share,
		Quota:    quota,
		Metadata: metadata,
		Created:  time.Now().UTC().Format(http.TimeFormat),
		ETag:     `"` + sim.NewUUID() + `"`,
	})
	return nil
}

// deleteFileShareDataPlaneProjection removes the data-plane share an ARM delete
// destroyed, along with its files.
func deleteFileShareDataPlaneProjection(account, share string) error {
	if !isStoragePathSegment(account) || !isStoragePathSegment(share) {
		return fmt.Errorf("invalid storage account or share name")
	}
	fileShareData.Delete(fileShareKey(account, share))
	deleteFileObjectProperties(account, share)
	return removeFileShareHostDir(account, share)
}

// fileShareResourceIDForAccount builds the ARM resource ID of a share on an
// ARM-known storage account. A share created on the data plane of an account
// that ARM has never seen has no ARM identity to project onto.
func fileShareResourceIDForAccount(account, share string) (string, bool) {
	acct, ok := azureStorageAccountByName(account)
	if !ok {
		return "", false
	}
	return acct.ID + "/fileServices/default/shares/" + share, true
}

// upsertFileShareARMProjection mirrors a data-plane share onto ARM, so a share
// created with `az storage share create` is the same resource
// `armstorage.NewFileSharesClient` reads back.
func upsertFileShareARMProjection(account, share string, quota int, metadata map[string]string) {
	if azFileShares == nil {
		return
	}
	resourceID, ok := fileShareResourceIDForAccount(account, share)
	if !ok {
		return
	}
	azFileShares.Put(resourceID, FileShare{
		ID:   resourceID,
		Name: share,
		Type: "Microsoft.Storage/storageAccounts/fileServices/shares",
		Etag: fmt.Sprintf("\"0x%s\"", randomSuffix(16)),
		Properties: FileShareProperties{
			ShareQuota:       quota,
			AccessTier:       "TransactionOptimized",
			EnabledProtocols: "SMB",
			LastModifiedTime: time.Now().UTC().Format(time.RFC3339),
			LeaseStatus:      "Unlocked",
			LeaseState:       "Available",
			Metadata:         metadata,
		},
	})
}

// deleteFileShareARMProjection removes the ARM share a data-plane delete
// destroyed.
func deleteFileShareARMProjection(account, share string) {
	if azFileShares == nil {
		return
	}
	if resourceID, ok := fileShareResourceIDForAccount(account, share); ok {
		azFileShares.Delete(resourceID)
	}
}

// updateFileShareARMAccessPolicies keeps the ARM and Azure Files data-plane
// representations of one share consistent. Azure exposes the same stored
// access policies through both File Shares APIs.
func updateFileShareARMAccessPolicies(account, share string, policies []TableSignedIdentifier) {
	if azFileShares == nil {
		return
	}
	resourceID, ok := fileShareResourceIDForAccount(account, share)
	if !ok {
		return
	}
	resource, ok := azFileShares.Get(resourceID)
	if !ok {
		return
	}
	resource.Properties.SignedIdentifiers = policies
	azFileShares.Put(resourceID, resource)
}
