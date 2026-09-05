package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A storage account's migrations, and the point-in-time blob restore beside
// them. Each one changes the account or its blobs for real:
//
//   - a customer-initiated migration moves the account to the SKU it names, so
//     the account reports that SKU afterwards and the migration resource
//     records what was asked for;
//   - a hierarchical-namespace migration turns the namespace on, so the account
//     reports isHnsEnabled afterwards, and its validation request checks
//     without changing anything;
//   - a blob-range restore un-deletes the soft-deleted blobs whose names fall
//     in the ranges it was given and which were deleted after the instant it
//     restores to, which is exactly what the retention policy kept them for.
//
// None of them reports a migration in flight that is not: the simulator moves
// no bytes between replicas, so the work is finished when the account has been
// changed, and the resource says Complete because it is.

// storageAccountMigration is a migration a client asked for.
type storageAccountMigration struct {
	AccountID     string `json:"accountId"`
	Name          string `json:"name"`
	TargetSkuName string `json:"targetSkuName"`
	Status        string `json:"migrationStatus"`
	FailedReason  string `json:"migrationFailedReason,omitempty"`
	FailedDetail  string `json:"migrationFailedDetailedReason,omitempty"`
}

// storageHnsMigration records that an account's hierarchical namespace is being
// turned on, which is what an abort has to find in order to abort anything.
type storageHnsMigration struct {
	AccountID string `json:"accountId"`
	Running   bool   `json:"running"`
}

var (
	storageAccountMigrations sim.Store[storageAccountMigration]
	storageHnsMigrations     sim.Store[storageHnsMigration]
	storageBlobRestores      sim.Store[map[string]any]
)

// registerStorageMigrations mounts the five operations.
func registerStorageMigrations(srv *sim.Server, acct string) {
	storageAccountMigrations = sim.MakeStore[storageAccountMigration](srv.DB(), "storage_account_migrations")
	storageHnsMigrations = sim.MakeStore[storageHnsMigration](srv.DB(), "storage_hns_migrations")
	storageBlobRestores = sim.MakeStore[map[string]any](srv.DB(), "storage_blob_restores")

	srv.HandleFunc("POST "+acct+"/startAccountMigration", handleStorageStartAccountMigration)
	srv.HandleFunc("GET "+acct+"/accountMigrations/{migrationName}", handleStorageGetAccountMigration)
	srv.HandleFunc("POST "+acct+"/hnsonmigration", handleStorageHnsOnMigration)
	srv.HandleFunc("POST "+acct+"/aborthnsonmigration", handleStorageAbortHnsOnMigration)
	srv.HandleFunc("POST "+acct+"/restoreBlobRanges", handleStorageRestoreBlobRanges)
}

// handleStorageStartAccountMigration — StorageAccounts_CustomerInitiatedMigration.
// The account is moved to the SKU the request names, because that is the whole
// of what the migration does to an account this simulator holds.
func handleStorageStartAccountMigration(w http.ResponseWriter, r *http.Request) {
	acctID, name, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	var req struct {
		Name       string `json:"name"`
		Properties struct {
			TargetSkuName string `json:"targetSkuName"`
		} `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "InvalidRequest", http.StatusBadRequest,
			"The migration request could not be read: %v", err)
		return
	}
	if req.Properties.TargetSkuName == "" {
		AzureErrorf(w, "InvalidRequest", http.StatusBadRequest,
			"A customer-initiated migration must name the target SKU to move the account to.")
		return
	}
	account, _ := azStorageAccounts.Get(acctID)
	if account.Sku != nil && strings.EqualFold(account.Sku.Name, req.Properties.TargetSkuName) {
		AzureErrorf(w, "InvalidRequest", http.StatusBadRequest,
			"The account '%s' is already on SKU '%s'.", name, req.Properties.TargetSkuName)
		return
	}

	migrationName := req.Name
	if migrationName == "" {
		migrationName = "default"
	}
	if account.Sku == nil {
		account.Sku = &StorageSku{}
	}
	account.Sku.Name = req.Properties.TargetSkuName
	azStorageAccounts.Put(acctID, account)

	storageAccountMigrations.Put(acctID, storageAccountMigration{
		AccountID:     acctID,
		Name:          migrationName,
		TargetSkuName: req.Properties.TargetSkuName,
		// The account is on the target SKU already: there are no bytes to move
		// between replicas here, so the migration is over when the account has
		// changed, and reporting InProgress would describe work nobody is doing.
		Status: "Complete",
	})
	writeStorageAsyncAccepted(w, r, account.Location)
}

// handleStorageGetAccountMigration — StorageAccounts_GetCustomerInitiatedMigration.
func handleStorageGetAccountMigration(w http.ResponseWriter, r *http.Request) {
	acctID, name, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	migrationName := sim.PathParam(r, "migrationName")
	held, found := storageAccountMigrations.Get(acctID)
	if !found || !strings.EqualFold(held.Name, migrationName) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"No customer-initiated migration named '%s' was started on account '%s'.",
			migrationName, name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"id":   acctID + "/accountMigrations/" + held.Name,
		"name": held.Name,
		"type": "Microsoft.Storage/storageAccounts/accountMigrations",
		"properties": map[string]any{
			"targetSkuName":   held.TargetSkuName,
			"migrationStatus": held.Status,
		},
	})
}

// handleStorageHnsOnMigration — StorageAccounts_HierarchicalNamespaceMigration.
// The request type decides whether the namespace is turned on or only checked,
// which is the difference the operation exists to draw.
func handleStorageHnsOnMigration(w http.ResponseWriter, r *http.Request) {
	acctID, name, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	requestType := r.URL.Query().Get("requestType")
	switch {
	case strings.EqualFold(requestType, "HnsOnValidationRequest"):
	case strings.EqualFold(requestType, "HnsOnHydrationRequest"):
	default:
		AzureErrorf(w, "InvalidRequest", http.StatusBadRequest,
			"requestType must be HnsOnValidationRequest or HnsOnHydrationRequest, not %q.", requestType)
		return
	}
	account, _ := azStorageAccounts.Get(acctID)
	if account.Properties.IsHnsEnabled != nil && *account.Properties.IsHnsEnabled {
		AzureErrorf(w, "Conflict", http.StatusConflict,
			"The account '%s' already has a hierarchical namespace.", name)
		return
	}
	if strings.EqualFold(requestType, "HnsOnValidationRequest") {
		// A validation asks whether the account could be migrated. It changes
		// nothing, which is what makes it a validation.
		writeStorageAsyncAccepted(w, r, account.Location)
		return
	}
	enabled := true
	account.Properties.IsHnsEnabled = &enabled
	azStorageAccounts.Put(acctID, account)
	storageHnsMigrations.Put(acctID, storageHnsMigration{AccountID: acctID, Running: false})
	writeStorageAsyncAccepted(w, r, account.Location)
}

// handleStorageAbortHnsOnMigration — StorageAccounts_AbortHierarchicalNamespaceMigration.
// There is nothing to abort unless a migration is running, and the simulator
// finishes one inside the request that starts it, so this reports the conflict
// the service reports rather than accepting an abort of nothing.
func handleStorageAbortHnsOnMigration(w http.ResponseWriter, r *http.Request) {
	acctID, name, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	held, found := storageHnsMigrations.Get(acctID)
	if !found || !held.Running {
		AzureErrorf(w, "Conflict", http.StatusConflict,
			"No hierarchical namespace migration is running on account '%s' to abort.", name)
		return
	}
	held.Running = false
	storageHnsMigrations.Put(acctID, held)
	account, _ := azStorageAccounts.Get(acctID)
	writeStorageAsyncAccepted(w, r, account.Location)
}

// handleStorageRestoreBlobRanges — StorageAccounts_RestoreBlobRanges. The
// restore un-deletes the soft-deleted blobs the retention policy kept: those
// whose names fall in the ranges the request names and which were deleted after
// the instant it restores to. A blob deleted before that instant was already
// gone then, so restoring to that moment does not bring it back.
func handleStorageRestoreBlobRanges(w http.ResponseWriter, r *http.Request) {
	acctID, name, ok := requireStorageAccount(w, r)
	if !ok {
		return
	}
	var req struct {
		TimeToRestore string `json:"timeToRestore"`
		BlobRanges    []struct {
			StartRange string `json:"startRange"`
			EndRange   string `json:"endRange"`
		} `json:"blobRanges"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "InvalidRequest", http.StatusBadRequest,
			"The restore request could not be read: %v", err)
		return
	}
	if !storageRestorePolicyEnabled(acctID) {
		AzureErrorf(w, "InvalidRequest", http.StatusBadRequest,
			"Point-in-time restore is not enabled on account '%s'. Enable the blob service restore policy first.",
			name)
		return
	}
	restoreTo, err := time.Parse(time.RFC3339, req.TimeToRestore)
	if err != nil {
		AzureErrorf(w, "InvalidRequest", http.StatusBadRequest,
			"timeToRestore must be an RFC 3339 instant: %v", err)
		return
	}
	if len(req.BlobRanges) == 0 {
		AzureErrorf(w, "InvalidRequest", http.StatusBadRequest,
			"A restore must name at least one blob range to restore.")
		return
	}

	account, _ := azStorageAccounts.Get(acctID)
	restoredCount := storageRestoreBlobs(account.Name, restoreTo, req.BlobRanges)

	restoreID := "restore-" + generateUUID()
	ranges := []any{}
	for _, rng := range req.BlobRanges {
		ranges = append(ranges, map[string]any{
			"startRange": rng.StartRange, "endRange": rng.EndRange,
		})
	}
	status := map[string]any{
		"restoreId": restoreID,
		// The blobs are back already: the retention policy kept them in place
		// and the restore un-deleted them, so there is no later moment for the
		// caller to wait for.
		"status": "Complete",
		"parameters": map[string]any{
			"timeToRestore": req.TimeToRestore,
			"blobRanges":    ranges,
		},
	}
	storageBlobRestores.Put(restoreID, status)
	_ = restoredCount
	sim.WriteJSON(w, http.StatusOK, status)
}

// storageRestorePolicyEnabled reports whether the account's blob service
// declares point-in-time restore, which is what makes a restore possible.
func storageRestorePolicyEnabled(acctID string) bool {
	if blobARMServiceProps == nil {
		return false
	}
	// The store is keyed by the account's own resource ID and holds the
	// blobServices/default property bag directly, which is what the control
	// plane wrote when the client set it.
	props, ok := blobARMServiceProps.Get(acctID)
	if !ok {
		return false
	}
	policy, _ := props["restorePolicy"].(map[string]any)
	if policy == nil {
		return false
	}
	enabled, _ := policy["enabled"].(bool)
	return enabled
}

// storageRestoreBlobs takes the blobs a restore covers back to how they stood
// at the instant it names: one deleted after that instant comes back, and one
// written after it goes away, because neither had happened yet. It reports how
// many blobs it moved.
func storageRestoreBlobs(account string, restoreTo time.Time, ranges []struct {
	StartRange string `json:"startRange"`
	EndRange   string `json:"endRange"`
},
) int {
	if blobObjects == nil {
		return 0
	}
	restored := 0
	for _, blob := range blobObjects.List() {
		if blob.Account != account {
			continue
		}
		path := blob.Container + "/" + blob.Name
		if !storageRangeCovers(ranges, path) {
			continue
		}
		if !blob.Deleted {
			// A blob written after the instant being restored to did not exist
			// then, so restoring to it removes the blob. Reporting a restore
			// that left later writes in place would describe a state the
			// account was never in.
			created, err := time.Parse(time.RFC1123, blob.LastModified)
			if err != nil || created.Before(restoreTo.Truncate(time.Second).Add(time.Second)) {
				continue
			}
			blobObjects.Delete(blobSnapshotKey(blob.Account, blob.Container, blob.Name, blob.Snapshot))
			restored++
			continue
		}
		deletedAt, err := time.Parse(time.RFC1123, blob.DeletedTime)
		if err != nil {
			// A blob whose deletion time cannot be read cannot be placed
			// relative to the instant being restored to, and restoring it
			// anyway would undo a deletion the caller did not ask to undo.
			continue
		}
		// A blob records its deletion time to the second, because that is the
		// precision the header carries it in, so the instant restored to is
		// compared at the same precision. Comparing a whole-second stamp
		// against a nanosecond one would place every deletion in the current
		// second before the restore point and restore nothing.
		if deletedAt.Before(restoreTo.Truncate(time.Second)) {
			continue
		}
		blob.Deleted = false
		blob.DeletedTime = ""
		blob.RemainingRetentionDays = 0
		blobObjects.Put(blobSnapshotKey(blob.Account, blob.Container, blob.Name, blob.Snapshot), blob)
		restored++
	}
	return restored
}

// storageRangeCovers reports whether a blob path falls in one of the lexical
// ranges the restore names. An empty end range is open-ended, which is how
// Azure spells "everything from here on".
func storageRangeCovers(ranges []struct {
	StartRange string `json:"startRange"`
	EndRange   string `json:"endRange"`
}, path string,
) bool {
	for _, rng := range ranges {
		if path < rng.StartRange {
			continue
		}
		if rng.EndRange != "" && path >= rng.EndRange {
			continue
		}
		return true
	}
	return false
}
