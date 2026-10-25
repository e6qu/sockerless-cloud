package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
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
	return sim.ScopedDataDir("SIM_AZURE_FILES_DATA_DIR", "files", "sockerless-sim-azure-files")
}

// FileShareHostDir returns the on-disk directory backing a simulated Azure
// file share, which creating the share makes. Exported for use by the ACA
// sim's Jobs/Apps executor.
func FileShareHostDir(storageAccount, shareName string) string {
	return filepath.Join(azureFilesHostRoot(), storageAccount, shareName)
}

// resetFileShareHostDir empties the share's backing directory. A share that has
// just been created holds no files, so its directory must not carry whatever a
// share of the same name left behind — the share metadata lives in the
// simulator's store, which starts empty on every non-persistent run, while the
// directory outlives the process.
func resetFileShareHostDir(storageAccount, shareName string) error {
	dir := FileShareHostDir(storageAccount, shareName)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	// A real Azure Files SMB mount is writable by the mounting container
	// (CIFS dir_mode and file_mode 0777).
	return sim.EnsureWritableDir(dir)
}

// removeFileShareHostDir deletes the share's backing directory and everything
// under it, which is what Delete Share does to a share's contents.
func removeFileShareHostDir(storageAccount, shareName string) error {
	return os.RemoveAll(FileShareHostDir(storageAccount, shareName))
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

func registerAzureFiles(srv *sim.Server) {
	storageAccounts := sim.MakeStore[StorageAccount](srv.DB(), "storage_accounts")
	azStorageAccounts = storageAccounts
	fileShares := sim.MakeStore[FileShare](srv.DB(), "file_shares")
	azFileShares = fileShares
	storageTables = sim.MakeStore[StorageTable](srv.DB(), "storage_tables")

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage"

	registerStorageAccountCRUD(srv, armBase, storageAccounts)
	registerFileSharesARM(srv, armBase, storageAccounts, fileShares)
	registerStorageAccountKeysAndList(srv, armBase, storageAccounts)
	registerStorageServiceProperties(srv, armBase, storageAccounts)
	registerStorageTablesARM(srv, armBase, storageAccounts)
	registerBlobContainersARM(srv, armBase, storageAccounts)
}
