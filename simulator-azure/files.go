package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// azureFilesHostRoot returns the on-disk backing directory for every
// simulated Azure Files share. Each (storage account, share) pair
// becomes a nested subdirectory so the ACA Jobs / Apps executor can
// bind-mount a real host path when it resolves a
// Volume{StorageType: AzureFile}.
//
// Resolution order:
//  1. SIM_AZURE_FILES_DATA_DIR — explicit operator override.
//  2. <SIM_DATA_DIR>/files — when a data directory is configured, share
//     contents live beside the SQLite database so a SIM_PERSIST restart
//     that reattaches the same SIM_DATA_DIR sees the same bytes the
//     persisted share metadata describes.
//  3. A fixed path under os.TempDir() — no configured data directory.
func azureFilesHostRoot() string {
	if dir := os.Getenv("SIM_AZURE_FILES_DATA_DIR"); dir != "" {
		return dir
	}
	if dataDir := os.Getenv("SIM_DATA_DIR"); dataDir != "" {
		return filepath.Join(dataDir, "files")
	}
	return filepath.Join(os.TempDir(), "sockerless-sim-azure-files")
}

// FileShareHostDir returns the on-disk directory backing a simulated
// Azure file share. Created lazily; safe for concurrent callers.
// Exported for use by the ACA sim's Jobs/Apps executor.
func FileShareHostDir(storageAccount, shareName string) string {
	dir := filepath.Join(azureFilesHostRoot(), storageAccount, shareName)
	_ = os.MkdirAll(dir, 0o777)
	// MkdirAll honors the process umask, so the share dir lands at 0755 and a
	// non-root workload (e.g. a gitlab-runner helper writing the build
	// workspace) can't create files in it. A real Azure Files SMB mount is
	// writable by the mounting container (CIFS default dir_mode/file_mode
	// 0777); chmod past the umask so the materialized share matches.
	_ = os.Chmod(dir, 0o777)
	return dir
}

// resetFileShareHostDir empties the share's backing directory. A share that has
// just been created holds no files, so its directory must not carry whatever a
// share of the same name left behind — the share metadata lives in the
// simulator's store, which starts empty on every non-persistent run, while the
// directory outlives the process.
func resetFileShareHostDir(storageAccount, shareName string) error {
	if err := os.RemoveAll(filepath.Join(azureFilesHostRoot(), storageAccount, shareName)); err != nil {
		return err
	}
	FileShareHostDir(storageAccount, shareName)
	return nil
}

// removeFileShareHostDir deletes the share's backing directory and everything
// under it, which is what Delete Share does to a share's contents.
func removeFileShareHostDir(storageAccount, shareName string) error {
	return os.RemoveAll(filepath.Join(azureFilesHostRoot(), storageAccount, shareName))
}

// fileShareHostPath resolves `relPath` inside the share's backing directory —
// the very directory the Container Apps Jobs / Apps executor bind-mounts for a
// Volume{StorageType: AzureFile}, so bytes written through the Files data plane
// are the bytes a mounting container reads.
//
// It reports false for any name that would resolve outside that directory. The
// account comes from the request's Host header and the share and path from its
// URL, so a name carrying a separator or a parent-directory component must
// never reach the filesystem.
func fileShareHostPath(storageAccount, shareName, relPath string) (string, bool) {
	if !isStoragePathSegment(storageAccount) || !isStoragePathSegment(shareName) {
		return "", false
	}
	segments := strings.Split(relPath, "/")
	for _, segment := range segments {
		if !isStoragePathSegment(segment) {
			return "", false
		}
	}
	return filepath.Join(FileShareHostDir(storageAccount, shareName), filepath.Join(segments...)), true
}

// isStoragePathSegment reports whether a single account / share / path segment
// names something inside the share directory rather than a way out of it.
func isStoragePathSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	return !strings.ContainsAny(segment, `/\`+"\x00")
}

// StorageAccount represents an Azure Storage Account.
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

// BlobContainer is a `Microsoft.Storage/storageAccounts/blobServices/
// containers/{name}` resource — the ARM control-plane entity that
// terraform's `azurerm_storage_container` and the SDK's
// `armstorage.NewBlobContainersClient` create. Sockerless runner
// artifact / cache flows store blobs in containers; without ARM
// container CRUD, every cache step 404s.
type BlobContainer struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Type       string             `json:"type"`
	Etag       string             `json:"etag,omitempty"`
	Properties BlobContainerProps `json:"properties"`
}

// StorageTable is a Microsoft.Storage ARM table resource. It is the
// control-plane projection of an Azure Tables data-plane table.
type StorageTable struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type"`
	Properties StorageTableProperties `json:"properties"`
}

type StorageTableProperties struct {
	TableName         string           `json:"tableName,omitempty"`
	SignedIdentifiers []map[string]any `json:"signedIdentifiers,omitempty"`
}

// BlobContainerProps mirrors the ARM `BlobContainerProperties` shape
// the SDK round-trips on Get/List.
type BlobContainerProps struct {
	PublicAccess     string            `json:"publicAccess,omitempty"`
	LeaseStatus      string            `json:"leaseStatus,omitempty"`
	LeaseState       string            `json:"leaseState,omitempty"`
	HasImmutability  bool              `json:"hasImmutabilityPolicy,omitempty"`
	HasLegalHold     bool              `json:"hasLegalHold,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	LastModifiedTime string            `json:"lastModifiedTime,omitempty"`
}

// StorageSku holds the SKU for a storage account.
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

// StorageAccountProperties holds the properties of a storage account.
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

// StoragePrimaryEndpoints holds the primary endpoints for a storage account.
type StoragePrimaryEndpoints struct {
	File  string `json:"file,omitempty"`
	Blob  string `json:"blob,omitempty"`
	Table string `json:"table,omitempty"`
	Queue string `json:"queue,omitempty"`
	Web   string `json:"web,omitempty"`
	Dfs   string `json:"dfs,omitempty"`
}

// FileShare represents an Azure File Share.
type FileShare struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Type       string              `json:"type"`
	Etag       string              `json:"etag,omitempty"`
	Properties FileShareProperties `json:"properties"`
}

// FileShareProperties holds the properties of a file share.
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

// Package-level stores for cross-plane projections and dashboard access.
var (
	azStorageAccounts sim.Store[StorageAccount]
	storageTables     sim.Store[StorageTable]
	azBlobContainers  sim.Store[BlobContainer]
	azFileShares      sim.Store[FileShare]
	// azStorageServiceProps holds the `Microsoft.Storage/storageAccounts/
	// {blob,file,queue,table}Services/default` resources, keyed
	// `<accountId>/<service>`. It is the account-wide service configuration an
	// administrator writes; the Files data plane reads the share
	// delete-retention policy from the fileServices entry, which is what
	// decides whether Delete Share destroys a share or keeps it for Restore
	// Share.
	azStorageServiceProps sim.Store[map[string]any]
)

func storageTableResourceID(sub, rg, account, table string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s/tableServices/default/tables/%s",
		sub, rg, account, table)
}

func storageTableResourceIDForAccount(account, table string) (string, bool) {
	acct, ok := azureStorageAccountByName(account)
	if !ok {
		return "", false
	}
	return acct.ID + "/tableServices/default/tables/" + table, true
}

func upsertStorageTableARMProjection(account, table string) {
	if storageTables == nil {
		return
	}
	resourceID, ok := storageTableResourceIDForAccount(account, table)
	if !ok {
		return
	}
	storageTables.Put(resourceID, StorageTable{
		ID:   resourceID,
		Name: table,
		Type: "Microsoft.Storage/storageAccounts/tableServices/tables",
		Properties: StorageTableProperties{
			TableName: table,
		},
	})
}

func deleteStorageTableARMProjection(account, table string) {
	if storageTables == nil {
		return
	}
	if resourceID, ok := storageTableResourceIDForAccount(account, table); ok {
		storageTables.Delete(resourceID)
	}
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
		ETag:     `"` + generateUUID() + `"`,
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

func registerAzureFiles(srv *sim.Server) {
	storageAccounts := sim.MakeStore[StorageAccount](srv.DB(), "storage_accounts")
	azStorageAccounts = storageAccounts
	fileShares := sim.MakeStore[FileShare](srv.DB(), "file_shares")
	azFileShares = fileShares
	storageTables = sim.MakeStore[StorageTable](srv.DB(), "storage_tables")

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage"

	// PUT - Create or update storage account
	srv.HandleFunc("PUT "+armBase+"/storageAccounts/{accountName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "accountName")

		var req StorageAccount
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Location == "" {
			sim.AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", sub, rg, name)

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
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", sub, rg, name)

		var req struct {
			Sku        *StorageSku        `json:"sku,omitempty"`
			Kind       string             `json:"kind,omitempty"`
			Tags       *map[string]string `json:"tags,omitempty"`
			Properties map[string]any     `json:"properties,omitempty"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", sub, rg, name)

		acct, ok := storageAccounts.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", sub, rg, name)

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

	// PUT - Create or update file share
	srv.HandleFunc("PUT "+armBase+"/storageAccounts/{accountName}/fileServices/default/shares/{shareName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")
		shareName := sim.PathParam(r, "shareName")

		var req FileShare
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Verify storage account exists
		acctID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", sub, rg, account)
		if _, ok := storageAccounts.Get(acctID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			sim.AzureErrorf(w, "InternalError", http.StatusInternalServerError,
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

		shareID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s/fileServices/default/shares/%s",
			sub, rg, account, shareName)

		share, ok := fileShares.Get(shareID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

		shareID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s/fileServices/default/shares/%s",
			sub, rg, account, shareName)

		if fileShares.Delete(shareID) {
			if err := deleteFileShareDataPlaneProjection(account, shareName); err != nil {
				sim.AzureErrorf(w, "InternalError", http.StatusInternalServerError,
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

		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s/fileServices/default/shares/",
			sub, rg, account)

		filtered := fileShares.Filter(func(s FileShare) bool {
			return strings.HasPrefix(s.ID, prefix)
		})

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": filtered,
		})
	})

	// POST - List storage account keys (azurerm provider calls this after creating a storage account)
	srv.HandleFunc("POST "+armBase+"/storageAccounts/{accountName}/listKeys", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "accountName")

		acctID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", sub, rg, name)
		if _, ok := storageAccounts.Get(acctID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

	// Storage service properties handlers — azurerm provider polls these after creating a storage account.
	// Each service (file, blob, queue, table) has a default configuration endpoint.
	storageServiceHandler := func(serviceName, serviceType string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			name := sim.PathParam(r, "accountName")

			acctID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", sub, rg, name)
			if _, ok := storageAccounts.Get(acctID); !ok {
				sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
		acctID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", sub, rg, account)
		if _, ok := storageAccounts.Get(acctID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
		acctID = fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", sub, rg, account)
		return acctID, acctID + "/blobServices/default", account, rg
	}
	srv.HandleFunc("PUT "+blobServicePath, func(w http.ResponseWriter, r *http.Request) {
		acctID, resourceID, account, rg := blobServiceResourceID(r)
		if _, ok := storageAccounts.Get(acctID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Storage/storageAccounts/%s' under resource group '%s' was not found.", account, rg)
			return
		}
		var req struct {
			Properties map[string]any `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		props := req.Properties
		if props == nil {
			props = map[string]any{}
		}
		blobServiceProps.Put(acctID, props)
		sim.WriteJSON(w, http.StatusOK, blobServiceResponse(resourceID, props))
	})
	srv.HandleFunc("GET "+blobServicePath, func(w http.ResponseWriter, r *http.Request) {
		acctID, resourceID, account, rg := blobServiceResourceID(r)
		if _, ok := storageAccounts.Get(acctID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Storage/storageAccounts/%s' under resource group '%s' was not found.", account, rg)
			return
		}
		props, ok := blobServiceProps.Get(acctID)
		if !ok {
			props = map[string]any{}
		}
		sim.WriteJSON(w, http.StatusOK, blobServiceResponse(resourceID, props))
	})

	// --- Tables (ARM control plane) ---
	tableBasePath := armBase + "/storageAccounts/{accountName}/tableServices/default/tables"

	writeStorageTableNotFound := func(w http.ResponseWriter, table, account string) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Storage table %q not found in storage account %q", table, account)
	}
	readStorageTableBody := func(r *http.Request) (StorageTable, error) {
		var req StorageTable
		if r.Body == nil {
			return req, nil
		}
		err := sim.ReadJSON(r, &req)
		if err != nil && err != io.EOF {
			return req, err
		}
		return req, nil
	}
	upsertStorageTable := func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")
		table := sim.PathParam(r, "tableName")
		acctID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", sub, rg, account)
		if _, ok := storageAccounts.Get(acctID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Storage/storageAccounts/%s' under resource group '%s' was not found.", account, rg)
			return
		}
		req, err := readStorageTableBody(r)
		if err != nil {
			sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		resourceID := storageTableResourceID(sub, rg, account, table)
		props := req.Properties
		props.TableName = table
		out := StorageTable{
			ID:         resourceID,
			Name:       table,
			Type:       "Microsoft.Storage/storageAccounts/tableServices/tables",
			Properties: props,
		}
		storageTables.Put(resourceID, out)
		upsertTableDataPlaneProjection(account, table)
		sim.WriteJSON(w, http.StatusOK, out)
	}
	srv.HandleFunc("PUT "+tableBasePath+"/{tableName}", upsertStorageTable)
	srv.HandleFunc("PATCH "+tableBasePath+"/{tableName}", upsertStorageTable)

	srv.HandleFunc("GET "+tableBasePath+"/{tableName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")
		table := sim.PathParam(r, "tableName")
		resourceID := storageTableResourceID(sub, rg, account, table)
		t, ok := storageTables.Get(resourceID)
		if !ok {
			writeStorageTableNotFound(w, table, account)
			return
		}
		sim.WriteJSON(w, http.StatusOK, t)
	})

	srv.HandleFunc("DELETE "+tableBasePath+"/{tableName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")
		table := sim.PathParam(r, "tableName")
		resourceID := storageTableResourceID(sub, rg, account, table)
		if !storageTables.Delete(resourceID) {
			writeStorageTableNotFound(w, table, account)
			return
		}
		deleteTableDataPlaneProjection(account, table)
		w.WriteHeader(http.StatusNoContent)
	})

	srv.HandleFunc("GET "+tableBasePath, func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		account := sim.PathParam(r, "accountName")
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s/tableServices/default/tables/",
			sub, rg, account)
		all := storageTables.Filter(func(t StorageTable) bool {
			return strings.HasPrefix(t.ID, prefix)
		})
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		if all == nil {
			all = []StorageTable{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
	})

	// --- Blob Containers (ARM control plane) ---
	// Sockerless runner artifact / cache flows store blobs in
	// `Microsoft.Storage/storageAccounts/{a}/blobServices/default/containers/{c}`.
	// The ARM control plane creates the container entity; the data plane
	// (subdomain-routed below) handles blob upload/download. Without this
	// surface, terraform's `azurerm_storage_container` and the SDK's
	// `armstorage.NewBlobContainersClient` 404.
	blobContainers := sim.MakeStore[BlobContainer](srv.DB(), "blob_containers")
	azBlobContainers = blobContainers
	containerBasePath := armBase + "/storageAccounts/{accountName}/blobServices/default/containers"

	srv.HandleFunc("PUT "+containerBasePath+"/{containerName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		acct := sim.PathParam(r, "accountName")
		name := sim.PathParam(r, "containerName")

		var req BlobContainer
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}

		resourceID := fmt.Sprintf(
			"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s/blobServices/default/containers/%s",
			sub, rg, acct, name)

		container := BlobContainer{
			ID:   resourceID,
			Name: name,
			Type: "Microsoft.Storage/storageAccounts/blobServices/containers",
			Properties: BlobContainerProps{
				PublicAccess:     req.Properties.PublicAccess,
				LeaseStatus:      "Unlocked",
				LeaseState:       "Available",
				HasImmutability:  false,
				HasLegalHold:     false,
				Metadata:         req.Properties.Metadata,
				LastModifiedTime: time.Now().UTC().Format(time.RFC3339),
			},
		}
		if container.Properties.PublicAccess == "" {
			container.Properties.PublicAccess = "None"
		}
		blobContainers.Put(resourceID, container)
		// A blob container is ONE object with two management surfaces: the
		// container the Azure Resource Manager plane creates here is the same
		// container the Blob data plane serves, so it comes into being on both.
		// Without this a container created through Microsoft.Storage — which is
		// how terraform-provider-azurerm creates every container — is invisible
		// to every client that addresses it at the account's blob endpoint.
		mirrorARMContainerToBlobPlane(acct, name, container.Properties.PublicAccess, container.Properties.Metadata)
		sim.WriteJSON(w, http.StatusOK, container)
	})

	srv.HandleFunc("GET "+containerBasePath+"/{containerName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		acct := sim.PathParam(r, "accountName")
		name := sim.PathParam(r, "containerName")
		resourceID := fmt.Sprintf(
			"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s/blobServices/default/containers/%s",
			sub, rg, acct, name)
		container, ok := blobContainers.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Blob container %q not found in storage account %q", name, acct)
			return
		}
		sim.WriteJSON(w, http.StatusOK, container)
	})

	srv.HandleFunc("DELETE "+containerBasePath+"/{containerName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		acct := sim.PathParam(r, "accountName")
		name := sim.PathParam(r, "containerName")
		resourceID := fmt.Sprintf(
			"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s/blobServices/default/containers/%s",
			sub, rg, acct, name)
		blobContainers.Delete(resourceID)
		// The same one object goes on both surfaces.
		removeARMContainerFromBlobPlane(acct, name)
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("GET "+containerBasePath, func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		acct := sim.PathParam(r, "accountName")
		prefix := fmt.Sprintf(
			"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s/blobServices/default/containers/",
			sub, rg, acct)
		all := blobContainers.Filter(func(c BlobContainer) bool {
			return strings.HasPrefix(c.ID, prefix)
		})
		if all == nil {
			all = []BlobContainer{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
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
