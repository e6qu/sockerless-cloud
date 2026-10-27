package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Microsoft.Web backup, restore and snapshot operations at site and slot
// scope. Every restore the specification marks
// `x-ms-long-running-operation: true` answers 202 with Azure-AsyncOperation;
// the reads and the backup itself are synchronous, as declared.

// Properties are flattened client-side by x-ms-client-flatten and nested on the
// wire.
type backupRequestBody struct {
	Properties struct {
		BackupName        string                     `json:"backupName"`
		Enabled           bool                       `json:"enabled"`
		StorageAccountURL string                     `json:"storageAccountUrl"`
		BackupSchedule    *WebBackupSchedule         `json:"backupSchedule"`
		Databases         []WebDatabaseBackupSetting `json:"databases"`
	} `json:"properties"`
}

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

type deletedAppRestoreRequestBody struct {
	Properties struct {
		DeletedSiteID        string `json:"deletedSiteId"`
		RecoverConfiguration bool   `json:"recoverConfiguration"`
		SnapshotTime         string `json:"snapshotTime"`
		UseDRSecondary       bool   `json:"useDRSecondary"`
	} `json:"properties"`
}

func webBackupID(r *http.Request) string {
	return armChildID(webResourceID(r), "backups", sim.PathParam(r, "backupId"))
}

// BackupItemProperties.id is an int32 the service assigns, increasing per app.
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

// Oldest first.
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

func registerWebBackups(both func(string, string, http.HandlerFunc)) {

	both("PUT", "/config/backup", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req backupRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if _, code, msg := webParseBackupStorageURL(req.Properties.StorageAccountURL); code != "" {
			AzureError(w, code, msg, http.StatusBadRequest)
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
		// Azure defaults a schedule's start time to the save time and always returns
		// one; terraform-provider-azurerm 5.1.0 dereferences it before its nil check
		// (FlattenBackupConfig, common_web_app_schema.go:1158).
		if row.BackupSchedule != nil && row.BackupSchedule.StartTime == "" {
			row.BackupSchedule.StartTime = time.Now().UTC().Format("2006-01-02T15:04:05.9999999")
		}
		webBackupConfigs.Put(row.ID, row)
		sim.WriteJSON(w, http.StatusOK, backupConfigWire(r, row))
	})

	both("DELETE", "/config/backup", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webBackupConfigs.Delete(webResourceID(r))
		w.WriteHeader(http.StatusOK)
	})

	// An app with no backup configuration has no such sub-resource, which
	// terraform's FlattenBackupConfig reads as "no backup block".
	backupConfigList := func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		row, ok := webBackupConfigs.Get(webResourceID(r))
		if !ok {
			AzureErrorf(w, "NotFound", http.StatusNotFound,
				"No backup configuration found for %q.", webSiteNameFromID(webResourceID(r)))
			return
		}
		sim.WriteJSON(w, http.StatusOK, backupConfigWire(r, row))
	}
	both("POST", "/config/backup/list", backupConfigList)

	// The archive and manifest are written before the response is composed, so
	// the status is that of work that happened.
	both("POST", "/backup", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req backupRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		resID := webResourceID(r)
		storageURL := req.Properties.StorageAccountURL
		backupName := req.Properties.BackupName
		databases := req.Properties.Databases
		// A request naming no storage uses the configured backup storage, as a
		// scheduled backup does.
		scheduled := false
		if storageURL == "" {
			cfg, ok := webBackupConfigs.Get(resID)
			if !ok {
				AzureError(w, "InvalidRequestContent",
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
			AzureError(w, code, msg, http.StatusBadRequest)
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
			CorrelationID:     sim.NewUUID(),
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
		stored := webPutBackupBlob(target, blobName, archive.zip, "application/zip")
		if stored == nil {
			stored = webPutBackupBlob(target, webManifestBlobName(blobName), archive.manifest, "application/xml")
		}
		if stored != nil {
			row.Status = "Failed"
			row.Log = stored.Error()
			row.FinishedTimeStamp = time.Now().UTC().Format(time.RFC3339)
			webBackupItems.Put(row.ID, row)
			sim.WriteJSON(w, http.StatusOK, backupItemWire(r, row, false))
			return
		}
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

	// WebApps_ListBackups (GET /backups) and WebApps_ListSiteBackups (POST
	// /listbackups) share a summary and the BackupItemCollection response.
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

	both("GET", "/backups/{backupId}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		row, ok := webBackupItems.Get(webBackupID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Backup %q not found.", sim.PathParam(r, "backupId"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, backupItemWire(r, row, false))
	})

	// The specification: "Gets status of a web app backup that may be in progress,
	// including secrets associated with the backup, such as the Azure Storage SAS
	// URL. Also can be used to update the SAS URL for the backup if a new URL is
	// passed in the request body."
	both("POST", "/backups/{backupId}/list", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		id := webBackupID(r)
		row, ok := webBackupItems.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Backup %q not found.", sim.PathParam(r, "backupId"))
			return
		}
		var req backupRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if url := req.Properties.StorageAccountURL; url != "" && url != row.StorageAccountURL {
			if _, code, msg := webParseBackupStorageURL(url); code != "" {
				AzureError(w, code, msg, http.StatusBadRequest)
				return
			}
			webBackupItems.Update(id, func(cur *WebBackupItemRow) { cur.StorageAccountURL = url })
			row, _ = webBackupItems.Get(id)
		}
		sim.WriteJSON(w, http.StatusOK, backupItemWire(r, row, true))
	})

	// Deleting a backup removes its archive and manifest, the DeleteInProgress →
	// Deleted transition BackupItemStatus models.
	both("DELETE", "/backups/{backupId}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		id := webBackupID(r)
		row, ok := webBackupItems.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

	// The specification: "Discovers an existing app backup that can be restored
	// from a blob in Azure storage. Use this to get information about the
	// databases stored in a backup." Answers the RestoreRequest completed from the
	// archive's manifest.
	both("POST", "/discoverbackup", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req restoreRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		target, code, msg := webParseBackupStorageURL(req.Properties.StorageAccountURL)
		if code != "" {
			AzureError(w, code, msg, http.StatusBadRequest)
			return
		}
		blobName := req.Properties.BlobName
		if blobName == "" {
			AzureError(w, "InvalidRequestContent",
				"The blobName property is required to discover a backup.", http.StatusBadRequest)
			return
		}
		if _, ok, err := webGetBackupBlob(target, blobName); err != nil {
			AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
			return
		} else if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The backup blob %q was not found in container %q.", blobName, target.container)
			return
		}
		manifest, ok, err := webGetBackupBlob(target, webManifestBlobName(blobName))
		if err != nil {
			AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

	both("POST", "/backups/{backupId}/restore", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req restoreRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		item, ok := webBackupItems.Get(webBackupID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

	both("POST", "/restoreFromBackupBlob", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req restoreRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.BlobName == "" {
			AzureError(w, "InvalidRequestContent",
				"The blobName property is required to restore from a backup blob.", http.StatusBadRequest)
			return
		}
		webRestoreFromBlob(w, r, req.Properties.StorageAccountURL, req.Properties.BlobName, req, "")
	})

	both("POST", "/restoreSnapshot", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req snapshotRestoreRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		resID := webResourceID(r)
		// "Optional. Specifies the web app that snapshot contents will be retrieved
		// from. If empty, the targeted web app will be used as the source."
		sourceID := resID
		if src := req.Properties.RecoverySource; src != nil && src.ID != "" {
			sourceID = strings.TrimRight(src.ID, "/")
		}
		snapshots := webSnapshotsFor(sourceID, req.Properties.UseDRSecondary)
		if len(snapshots) == 0 {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"No snapshot of app %q was taken at %q.",
					webSiteNameFromID(sourceID), req.Properties.SnapshotTime)
				return
			}
		}
		if !req.Properties.Overwrite {
			hasContent, err := webSiteHasContent(resID)
			if err != nil {
				AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
				return
			}
			if hasContent {
				AzureError(w, "Conflict",
					"The target app already has content; set overwrite to true to replace it.",
					http.StatusConflict)
				return
			}
		}
		files, err := webReadBackupArchive(chosen.Archive)
		if err != nil {
			AzureError(w, "RestoreFailed", err.Error(), http.StatusBadRequest)
			return
		}
		recoverConfig := req.Properties.RecoverConfiguration
		webIssueRestore(w, r, resID, func() *AsyncOperationError {
			if err := webReplaceSiteContent(resID, files); err != nil {
				return &AsyncOperationError{Code: "InternalServerError", Message: err.Error()}
			}
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

	both("POST", "/restoreFromDeletedApp", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req deletedAppRestoreRequestBody
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.DeletedSiteID == "" {
			AzureError(w, "InvalidRequestContent",
				"The deletedSiteId property is required.", http.StatusBadRequest)
			return
		}
		deleted, ok := webDeletedSiteByRequest(req.Properties.DeletedSiteID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The deleted app with id '%s' was not found.", req.Properties.DeletedSiteID)
			return
		}
		files, err := webReadBackupArchive(deleted.Archive)
		if err != nil {
			AzureError(w, "RestoreFailed", err.Error(), http.StatusBadRequest)
			return
		}
		resID := webResourceID(r)
		recoverConfig := req.Properties.RecoverConfiguration
		webIssueRestore(w, r, resID, func() *AsyncOperationError {
			if err := webReplaceSiteContent(resID, files); err != nil {
				return &AsyncOperationError{Code: "InternalServerError", Message: err.Error()}
			}
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

	// GET /snapshots reads the primary region, GET /snapshotsdr the geo-redundant
	// secondary that `useDRSecondary` restores read.
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

// Shared by WebApps_Restore and WebApps_RestoreFromBackupBlob: read the
// archive back, replace the app's content, and reconcile the manifest's
// custom domains.
func webRestoreFromBlob(w http.ResponseWriter, r *http.Request, storageURL, blobName string,
	req restoreRequestBody, backupItemID string) {
	target, code, msg := webParseBackupStorageURL(storageURL)
	if code != "" {
		AzureError(w, code, msg, http.StatusBadRequest)
		return
	}
	data, ok, err := webGetBackupBlob(target, blobName)
	if err != nil {
		AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The backup blob %q was not found in container %q.", blobName, target.container)
		return
	}
	files, err := webReadBackupArchive(data)
	if err != nil {
		AzureError(w, "RestoreFailed", err.Error(), http.StatusBadRequest)
		return
	}
	resID := webResourceID(r)
	// "true is needed if trying to restore over an existing app."
	hasContent, err := webSiteHasContent(resID)
	if err != nil {
		AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	if !req.Properties.Overwrite && hasContent {
		AzureError(w, "Conflict",
			"The target app already has content; set overwrite to true to restore over it.",
			http.StatusConflict)
		return
	}
	var hostNames []string
	manifest, ok, err := webGetBackupBlob(target, webManifestBlobName(blobName))
	if err != nil {
		AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	if ok {
		hostNames = webManifestHostNames(manifest)
	}
	ignoreHostNames := req.Properties.IgnoreConflictingHostNames
	webIssueRestore(w, r, resID, func() *AsyncOperationError {
		if err := webReplaceSiteContent(resID, files); err != nil {
			return &AsyncOperationError{Code: "InternalServerError", Message: err.Error()}
		}
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

// 202 with the poll coordinates, as `x-ms-long-running-operation: true` on
// every restore declares.
func webIssueRestore(w http.ResponseWriter, r *http.Request, resID string, run func() *AsyncOperationError) {
	opID := startAzureAsyncOperationOutcome(run)
	opURL := azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"),
		"Microsoft.Web", webSiteOperationLocation(resID), "operationStatuses", opID,
		r.URL.Query().Get("api-version"))
	w.Header().Set("Azure-AsyncOperation", opURL)
	w.Header().Set("Location", azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"),
		"Microsoft.Web", webSiteOperationLocation(resID), "operationResults", opID,
		r.URL.Query().Get("api-version")))
	setAzureAsyncOperationRetryAfter(w, opID)
	w.WriteHeader(http.StatusAccepted)
}
