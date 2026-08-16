package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_backup_handlers.go mounts the Microsoft.Web backup, restore and snapshot
// operations described in web_backup.go, at both the production-site and the
// deployment-slot scope. Every restore spelling the specification marks
// `x-ms-long-running-operation: true` answers 202 with the Azure-AsyncOperation
// poll coordinates; the reads and the backup itself are synchronous, as the
// specification declares them.

// backupRequestBody is the swagger BackupRequest wire shape (properties
// flattened by x-ms-client-flatten on the client side, nested on the wire).
type backupRequestBody struct {
	Properties struct {
		BackupName        string                     `json:"backupName"`
		Enabled           bool                       `json:"enabled"`
		StorageAccountURL string                     `json:"storageAccountUrl"`
		BackupSchedule    *WebBackupSchedule         `json:"backupSchedule"`
		Databases         []WebDatabaseBackupSetting `json:"databases"`
	} `json:"properties"`
}

// restoreRequestBody is the swagger RestoreRequest wire shape.
type restoreRequestBody struct {
	Properties struct {
		StorageAccountURL          string                     `json:"storageAccountUrl"`
		BlobName                   string                     `json:"blobName"`
		Overwrite                  bool                       `json:"overwrite"`
		SiteName                   string                     `json:"siteName"`
		Databases                  []WebDatabaseBackupSetting `json:"databases"`
		IgnoreConflictingHostNames bool                       `json:"ignoreConflictingHostNames"`
		IgnoreDatabases            bool                       `json:"ignoreDatabases"`
		AppServicePlan             string                     `json:"appServicePlan"`
		OperationType              string                     `json:"operationType"`
		AdjustConnectionStrings    bool                       `json:"adjustConnectionStrings"`
		HostingEnvironment         string                     `json:"hostingEnvironment"`
	} `json:"properties"`
}

// snapshotRestoreRequestBody is the swagger SnapshotRestoreRequest wire shape.
type snapshotRestoreRequestBody struct {
	Properties struct {
		SnapshotTime   string `json:"snapshotTime"`
		RecoverySource *struct {
			Location string `json:"location"`
			ID       string `json:"id"`
		} `json:"recoverySource"`
		Overwrite                  bool `json:"overwrite"`
		RecoverConfiguration       bool `json:"recoverConfiguration"`
		IgnoreConflictingHostNames bool `json:"ignoreConflictingHostNames"`
		UseDRSecondary             bool `json:"useDRSecondary"`
	} `json:"properties"`
}

// deletedAppRestoreRequestBody is the swagger DeletedAppRestoreRequest wire
// shape.
type deletedAppRestoreRequestBody struct {
	Properties struct {
		DeletedSiteID        string `json:"deletedSiteId"`
		RecoverConfiguration bool   `json:"recoverConfiguration"`
		SnapshotTime         string `json:"snapshotTime"`
		UseDRSecondary       bool   `json:"useDRSecondary"`
	} `json:"properties"`
}

// webBackupID resolves the addressed backup item's store key.
func webBackupID(r *http.Request) string {
	return webResourceID(r) + "/backups/" + sim.PathParam(r, "backupId")
}

// webNextBackupID picks the next backup id for a site. BackupItemProperties.id
// is an int32 the service assigns, increasing per app.
func webNextBackupID(resID string) int32 {
	prefix := resID + "/backups/"
	next := int32(1)
	for _, row := range webBackupItems.Filter(func(row WebBackupItemRow) bool { return strings.HasPrefix(row.ID, prefix) }) {
		if row.BackupID >= next {
			next = row.BackupID + 1
		}
	}
	return next
}

// webBackupsFor lists a site's backup items, oldest first.
func webBackupsFor(resID string) []WebBackupItemRow {
	prefix := resID + "/backups/"
	rows := webBackupItems.Filter(func(row WebBackupItemRow) bool { return strings.HasPrefix(row.ID, prefix) })
	sortBackupItems(rows)
	return rows
}

func sortBackupItems(rows []WebBackupItemRow) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].BackupID < rows[j-1].BackupID; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

// registerWebBackups mounts the whole family.
func registerWebBackups(both func(string, string, http.HandlerFunc)) {
	// --- Backup configuration ------------------------------------------------

	// WebApps_UpdateBackupConfiguration[Slot] — PUT /config/backup.
	both("PUT", "/config/backup", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req backupRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if _, code, msg := webParseBackupStorageURL(req.Properties.StorageAccountURL); code != "" {
			sim.AzureError(w, code, msg, http.StatusBadRequest)
			return
		}
		row := WebBackupConfigRow{
			ID:                webResourceID(r),
			BackupName:        req.Properties.BackupName,
			Enabled:           req.Properties.Enabled,
			StorageAccountURL: req.Properties.StorageAccountURL,
			BackupSchedule:    req.Properties.BackupSchedule,
			Databases:         req.Properties.Databases,
		}
		webBackupConfigs.Put(row.ID, row)
		sim.WriteJSON(w, http.StatusOK, backupConfigWire(r, row))
	})

	// WebApps_DeleteBackupConfiguration[Slot] — DELETE /config/backup.
	both("DELETE", "/config/backup", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webBackupConfigs.Delete(webResourceID(r))
		w.WriteHeader(http.StatusOK)
	})

	// WebApps_GetBackupConfiguration[Slot] — POST /config/backup/list. An app
	// with no configured backup has no such sub-resource, which terraform's
	// FlattenBackupConfig reads as "no backup block".
	backupConfigList := func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		row, ok := webBackupConfigs.Get(webResourceID(r))
		if !ok {
			sim.AzureErrorf(w, "NotFound", http.StatusNotFound,
				"No backup configuration found for %q.", webSiteNameFromID(webResourceID(r)))
			return
		}
		sim.WriteJSON(w, http.StatusOK, backupConfigWire(r, row))
	}
	both("POST", "/config/backup/list", backupConfigList)

	// --- Backups -------------------------------------------------------------

	// WebApps_Backup[Slot] — POST /backup. The archive and its manifest are
	// written into the storage account before the response is composed, so the
	// status the caller reads is the status of work that really happened.
	both("POST", "/backup", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req backupRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		resID := webResourceID(r)
		storageURL := req.Properties.StorageAccountURL
		backupName := req.Properties.BackupName
		databases := req.Properties.Databases
		// A request that names no storage takes the app's configured backup
		// storage, which is how a scheduled backup runs.
		scheduled := false
		if storageURL == "" {
			cfg, ok := webBackupConfigs.Get(resID)
			if !ok {
				sim.AzureError(w, "InvalidRequestContent",
					"The storageAccountUrl property is required when the app has no backup configuration.",
					http.StatusBadRequest)
				return
			}
			storageURL, scheduled = cfg.StorageAccountURL, true
			if backupName == "" {
				backupName = cfg.BackupName
			}
			if databases == nil {
				databases = cfg.Databases
			}
		}
		target, code, msg := webParseBackupStorageURL(storageURL)
		if code != "" {
			sim.AzureError(w, code, msg, http.StatusBadRequest)
			return
		}
		site, _ := webSiteRecord(resID)
		siteName := webSiteNameFromID(resID)
		if backupName == "" {
			backupName = siteName
		}
		now := time.Now().UTC()
		created := now.Format(time.RFC3339)
		base := fmt.Sprintf("%s-%s", backupName, now.Format("20060102150405"))
		blobName := base + ".zip"

		archive, err := webBuildBackupArchive(resID, siteName, backupName, created,
			webCustomHostNames(resID), databases, site.Properties.SiteConfig)
		backupID := webNextBackupID(resID)
		row := WebBackupItemRow{
			ID:                resID + "/backups/" + strconv.Itoa(int(backupID)),
			SiteID:            resID,
			BackupID:          backupID,
			Name:              base,
			BlobName:          blobName,
			StorageAccountURL: storageURL,
			Created:           created,
			Scheduled:         scheduled,
			CorrelationID:     generateUUID(),
			Databases:         databases,
		}
		if err != nil {
			row.Status = "Failed"
			row.Log = err.Error()
			row.FinishedTimeStamp = time.Now().UTC().Format(time.RFC3339)
			webBackupItems.Put(row.ID, row)
			sim.WriteJSON(w, http.StatusOK, backupItemWire(r, row, false))
			return
		}
		webPutBackupBlob(target, blobName, archive.zip, "application/zip")
		webPutBackupBlob(target, webManifestBlobName(blobName), archive.manifest, "application/xml")
		row.Status = "Succeeded"
		row.SizeInBytes = int64(len(archive.zip))
		row.WebsiteSizeInBytes = archive.contentBytes
		row.FinishedTimeStamp = time.Now().UTC().Format(time.RFC3339)
		row.Log = fmt.Sprintf("Backed up %d file(s) to %s/%s.", archive.files, target.container, blobName)
		webBackupItems.Put(row.ID, row)
		if scheduled {
			webBackupConfigs.Update(resID, func(cfg *WebBackupConfigRow) {
				if cfg.BackupSchedule != nil {
					cfg.BackupSchedule.LastExecutionTime = created
				}
			})
		}
		sim.WriteJSON(w, http.StatusOK, backupItemWire(r, row, false))
	})

	// WebApps_ListBackups[Slot] — GET /backups, and
	// WebApps_ListSiteBackups[Slot] — POST /listbackups. The specification
	// gives both the same summary ("Gets existing backups of an app") and the
	// same BackupItemCollection response.
	listBackups := func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		rows := webBackupsFor(webResourceID(r))
		out := make([]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, backupItemWire(r, row, false))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	}
	both("GET", "/backups", listBackups)
	both("POST", "/listbackups", listBackups)

	// WebApps_GetBackupStatus[Slot] — GET /backups/{backupId}.
	both("GET", "/backups/{backupId}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		row, ok := webBackupItems.Get(webBackupID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Backup %q not found.", sim.PathParam(r, "backupId"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, backupItemWire(r, row, false))
	})

	// WebApps_ListBackupStatusSecrets[Slot] — POST /backups/{backupId}/list.
	// The specification: "Gets status of a web app backup that may be in
	// progress, including secrets associated with the backup, such as the
	// Azure Storage SAS URL. Also can be used to update the SAS URL for the
	// backup if a new URL is passed in the request body."
	both("POST", "/backups/{backupId}/list", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		id := webBackupID(r)
		row, ok := webBackupItems.Get(id)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Backup %q not found.", sim.PathParam(r, "backupId"))
			return
		}
		var req backupRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if url := req.Properties.StorageAccountURL; url != "" && url != row.StorageAccountURL {
			if _, code, msg := webParseBackupStorageURL(url); code != "" {
				sim.AzureError(w, code, msg, http.StatusBadRequest)
				return
			}
			webBackupItems.Update(id, func(cur *WebBackupItemRow) { cur.StorageAccountURL = url })
			row, _ = webBackupItems.Get(id)
		}
		sim.WriteJSON(w, http.StatusOK, backupItemWire(r, row, true))
	})

	// WebApps_DeleteBackup[Slot] — DELETE /backups/{backupId}. Deleting a
	// backup removes the archive and its manifest from the storage account,
	// which is the transition BackupItemStatus models as
	// DeleteInProgress → Deleted.
	both("DELETE", "/backups/{backupId}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		id := webBackupID(r)
		row, ok := webBackupItems.Get(id)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Backup %q not found.", sim.PathParam(r, "backupId"))
			return
		}
		if target, code, _ := webParseBackupStorageURL(row.StorageAccountURL); code == "" {
			deleteBlobSnapshot(target.account, target.container, row.BlobName, "")
			deleteBlobSnapshot(target.account, target.container, webManifestBlobName(row.BlobName), "")
		}
		webBackupItems.Delete(id)
		w.WriteHeader(http.StatusOK)
	})

	// WebApps_DiscoverBackup[Slot] — POST /discoverbackup. The specification:
	// "Discovers an existing app backup that can be restored from a blob in
	// Azure storage. Use this to get information about the databases stored in
	// a backup." The answer is the RestoreRequest completed from the archive's
	// own manifest.
	both("POST", "/discoverbackup", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req restoreRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		target, code, msg := webParseBackupStorageURL(req.Properties.StorageAccountURL)
		if code != "" {
			sim.AzureError(w, code, msg, http.StatusBadRequest)
			return
		}
		blobName := req.Properties.BlobName
		if blobName == "" {
			sim.AzureError(w, "InvalidRequestContent",
				"The blobName property is required to discover a backup.", http.StatusBadRequest)
			return
		}
		if _, ok := webGetBackupBlob(target, blobName); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The backup blob %q was not found in container %q.", blobName, target.container)
			return
		}
		manifest, ok := webGetBackupBlob(target, webManifestBlobName(blobName))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The backup manifest %q was not found in container %q.",
				webManifestBlobName(blobName), target.container)
			return
		}
		props := map[string]any{
			"storageAccountUrl": req.Properties.StorageAccountURL,
			"blobName":          blobName,
			"overwrite":         req.Properties.Overwrite,
			"siteName":          webSiteNameFromID(webResourceID(r)),
		}
		if dbs := webManifestDatabases(manifest); len(dbs) > 0 {
			props["databases"] = dbs
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":         webResourceID(r) + "/discoverbackup",
			"name":       "discover",
			"type":       webChildType(r, "restore"),
			"properties": props,
		})
	})

	// --- Restores -------------------------------------------------------------

	// WebApps_Restore[Slot] — POST /backups/{backupId}/restore.
	both("POST", "/backups/{backupId}/restore", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req restoreRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		item, ok := webBackupItems.Get(webBackupID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Backup %q not found.", sim.PathParam(r, "backupId"))
			return
		}
		storageURL := req.Properties.StorageAccountURL
		if storageURL == "" {
			storageURL = item.StorageAccountURL
		}
		blobName := req.Properties.BlobName
		if blobName == "" {
			blobName = item.BlobName
		}
		webRestoreFromBlob(w, r, storageURL, blobName, req, item.ID)
	})

	// WebApps_RestoreFromBackupBlob[Slot] — POST /restoreFromBackupBlob.
	both("POST", "/restoreFromBackupBlob", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req restoreRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.BlobName == "" {
			sim.AzureError(w, "InvalidRequestContent",
				"The blobName property is required to restore from a backup blob.", http.StatusBadRequest)
			return
		}
		webRestoreFromBlob(w, r, req.Properties.StorageAccountURL, req.Properties.BlobName, req, "")
	})

	// WebApps_RestoreSnapshot[Slot] — POST /restoreSnapshot.
	both("POST", "/restoreSnapshot", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req snapshotRestoreRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		resID := webResourceID(r)
		// "Optional. Specifies the web app that snapshot contents will be
		// retrieved from. If empty, the targeted web app will be used as the
		// source."
		sourceID := resID
		if src := req.Properties.RecoverySource; src != nil && src.ID != "" {
			sourceID = strings.TrimRight(src.ID, "/")
		}
		snapshots := webSnapshotsFor(sourceID, req.Properties.UseDRSecondary)
		if len(snapshots) == 0 {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"No snapshots exist for app %q.", webSiteNameFromID(sourceID))
			return
		}
		chosen := snapshots[len(snapshots)-1]
		if req.Properties.SnapshotTime != "" {
			found := false
			for _, snap := range snapshots {
				if snap.Time == req.Properties.SnapshotTime {
					chosen, found = snap, true
					break
				}
			}
			if !found {
				sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"No snapshot of app %q was taken at %q.",
					webSiteNameFromID(sourceID), req.Properties.SnapshotTime)
				return
			}
		}
		if !req.Properties.Overwrite {
			if len(webSiteContentFiles(resID)) > 0 {
				sim.AzureError(w, "Conflict",
					"The target app already has content; set overwrite to true to replace it.",
					http.StatusConflict)
				return
			}
		}
		files, err := webReadBackupArchive(chosen.Archive)
		if err != nil {
			sim.AzureError(w, "RestoreFailed", err.Error(), http.StatusBadRequest)
			return
		}
		recoverConfig := req.Properties.RecoverConfiguration
		webIssueRestore(w, r, resID, func() *AsyncOperationError {
			webReplaceSiteContent(resID, files)
			if recoverConfig && chosen.Config != nil {
				store := webSiteStoreFor(resID)
				site, ok := store.Get(resID)
				if ok {
					cfg := *chosen.Config
					site.Properties.SiteConfig = &cfg
					store.Put(resID, site)
				}
			}
			return nil
		})
	})

	// WebApps_RestoreFromDeletedApp[Slot] — POST /restoreFromDeletedApp.
	both("POST", "/restoreFromDeletedApp", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req deletedAppRestoreRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.DeletedSiteID == "" {
			sim.AzureError(w, "InvalidRequestContent",
				"The deletedSiteId property is required.", http.StatusBadRequest)
			return
		}
		deleted, ok := webDeletedSiteByRequest(req.Properties.DeletedSiteID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The deleted app with id '%s' was not found.", req.Properties.DeletedSiteID)
			return
		}
		files, err := webReadBackupArchive(deleted.Archive)
		if err != nil {
			sim.AzureError(w, "RestoreFailed", err.Error(), http.StatusBadRequest)
			return
		}
		resID := webResourceID(r)
		recoverConfig := req.Properties.RecoverConfiguration
		webIssueRestore(w, r, resID, func() *AsyncOperationError {
			webReplaceSiteContent(resID, files)
			if recoverConfig && deleted.Config != nil {
				store := webSiteStoreFor(resID)
				if site, ok := store.Get(resID); ok {
					cfg := *deleted.Config
					site.Properties.SiteConfig = &cfg
					store.Put(resID, site)
				}
			}
			return nil
		})
	})

	// --- Snapshots -------------------------------------------------------------

	// WebApps_ListSnapshots[Slot] — GET /snapshots (the primary region) and
	// WebApps_ListSnapshotsFromDRSecondary[Slot] — GET /snapshotsdr (the
	// geo-redundant secondary the `useDRSecondary` restores read).
	listSnapshots := func(useDR bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if webMissing(w, r) {
				return
			}
			rows := webSnapshotsFor(webResourceID(r), useDR)
			out := make([]any, 0, len(rows))
			for _, row := range rows {
				out = append(out, snapshotWire(r, row))
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
		}
	}
	both("GET", "/snapshots", listSnapshots(false))
	both("GET", "/snapshotsdr", listSnapshots(true))
}

// webRestoreFromBlob performs the shared work of WebApps_Restore and
// WebApps_RestoreFromBackupBlob: read the archive back out of the storage
// account, replace the app's content with it, and reconcile the custom domains
// the manifest lists.
func webRestoreFromBlob(w http.ResponseWriter, r *http.Request, storageURL, blobName string,
	req restoreRequestBody, backupItemID string) {
	target, code, msg := webParseBackupStorageURL(storageURL)
	if code != "" {
		sim.AzureError(w, code, msg, http.StatusBadRequest)
		return
	}
	data, ok := webGetBackupBlob(target, blobName)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The backup blob %q was not found in container %q.", blobName, target.container)
		return
	}
	files, err := webReadBackupArchive(data)
	if err != nil {
		sim.AzureError(w, "RestoreFailed", err.Error(), http.StatusBadRequest)
		return
	}
	resID := webResourceID(r)
	// "true is needed if trying to restore over an existing app."
	if !req.Properties.Overwrite && len(webSiteContentFiles(resID)) > 0 {
		sim.AzureError(w, "Conflict",
			"The target app already has content; set overwrite to true to restore over it.",
			http.StatusConflict)
		return
	}
	var hostNames []string
	if manifest, ok := webGetBackupBlob(target, webManifestBlobName(blobName)); ok {
		hostNames = webManifestHostNames(manifest)
	}
	ignoreHostNames := req.Properties.IgnoreConflictingHostNames
	webIssueRestore(w, r, resID, func() *AsyncOperationError {
		webReplaceSiteContent(resID, files)
		if !ignoreHostNames {
			webApplyManifestHostNames(resID, hostNames)
		}
		if backupItemID != "" {
			webBackupItems.Update(backupItemID, func(cur *WebBackupItemRow) {
				cur.LastRestoreTimeStamp = time.Now().UTC().Format(time.RFC3339)
			})
		}
		return nil
	})
}

// webIssueRestore runs the restore behind an Azure Resource Manager
// long-running operation and answers 202 with its poll coordinates, which is
// what `x-ms-long-running-operation: true` on every restore declares.
func webIssueRestore(w http.ResponseWriter, r *http.Request, resID string, run func() *AsyncOperationError) {
	opID := issueAzureAsyncOperationOutcome(run)
	opURL := azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"),
		"Microsoft.Web", webSiteOperationLocation(resID), "operationStatuses", opID,
		r.URL.Query().Get("api-version"))
	w.Header().Set("Azure-AsyncOperation", opURL)
	w.Header().Set("Location", azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"),
		"Microsoft.Web", webSiteOperationLocation(resID), "operationResults", opID,
		r.URL.Query().Get("api-version")))
	w.Header().Set("Retry-After", "0")
	w.WriteHeader(http.StatusAccepted)
}
