package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_backup.go implements the Microsoft.Web backup, restore and snapshot
// family: the backup configuration, on-demand backups, the backup list and
// status reads, backup deletion, backup discovery, and every restore spelling
// (from a backup item, from a backup blob, from a snapshot, from a deleted
// app) — at both the production-site and the deployment-slot scope.
//
// A backup does real work. The archive is built from the site's deployed
// content (webSiteContent) and written into the Blob data plane of the storage
// account the request's `storageAccountUrl` names, which is the same surface a
// client downloads it from; a restore reads that blob back and replaces the
// site's content with what the archive holds. Deleting the blob through the
// Blob API therefore makes the restore fail, because the blob IS the backup.
//
// Microsoft's published contract the shapes here follow
// (https://learn.microsoft.com/azure/app-service/manage-backup):
//
//   - "In the storage account, each backup consists of a ZIP file that
//     contains the backup data and an XML file that contains a manifest of the
//     ZIP file contents."
//   - "Each backup contains a .zip file with backup data and an .xml file
//     {siteName}-{dateTime}.xml, which lists the contents, including custom
//     domains. When restoring a custom backup, custom domains from the .xml
//     file will be added to the destination app if no DNS conflict exists
//     (i.e., the domain is available for binding), and if the destination app
//     has different custom domains than the .xml file's custom domain list,
//     those custom domains will be removed."
//   - "Without `_backup.filter`, restoring a backup deletes all existing files
//     in the app and replaces them with the files in the backup."
//   - "Each backup is a complete offline copy of your app, not an incremental
//     update."
//   - "Custom backups for Azure App Service require an Azure Storage account
//     that supports Shared Access Signature (SAS)–based authorization."
//   - The documented failure messages "Missing mandatory parameters for valid
//     Shared Access Signature.", "Cannot resolve {0}. {1}
//     (CannotResolveStorageAccount)" and "Storage access failed." are the ones
//     the handlers emit for those three conditions.

// --- Wire-shaped stored records ---------------------------------------------

// WebBackupSchedule mirrors the swagger BackupSchedule definition.
type WebBackupSchedule struct {
	FrequencyInterval     int32  `json:"frequencyInterval"`
	FrequencyUnit         string `json:"frequencyUnit"`
	KeepAtLeastOneBackup  bool   `json:"keepAtLeastOneBackup"`
	RetentionPeriodInDays int32  `json:"retentionPeriodInDays"`
	StartTime             string `json:"startTime,omitempty"`
	LastExecutionTime     string `json:"lastExecutionTime,omitempty"`
}

// WebDatabaseBackupSetting mirrors the swagger DatabaseBackupSetting
// definition. `databaseType` is the only required member.
type WebDatabaseBackupSetting struct {
	DatabaseType         string `json:"databaseType"`
	Name                 string `json:"name,omitempty"`
	ConnectionStringName string `json:"connectionStringName,omitempty"`
	ConnectionString     string `json:"connectionString,omitempty"`
}

// WebBackupConfigRow is a site's or slot's stored backup configuration — the
// BackupRequestProperties written by WebApps_UpdateBackupConfiguration. Keyed
// by the canonical resource ID.
type WebBackupConfigRow struct {
	ID                string                     `json:"id"`
	BackupName        string                     `json:"backupName,omitempty"`
	Enabled           bool                       `json:"enabled"`
	StorageAccountURL string                     `json:"storageAccountUrl"`
	BackupSchedule    *WebBackupSchedule         `json:"backupSchedule,omitempty"`
	Databases         []WebDatabaseBackupSetting `json:"databases,omitempty"`
}

// WebBackupItemRow is one completed (or failed) backup —
// BackupItemProperties. Keyed by "<resID>/backups/<backupId>".
type WebBackupItemRow struct {
	ID                   string                     `json:"id"`
	SiteID               string                     `json:"siteId"`
	BackupID             int32                      `json:"backupId"`
	Name                 string                     `json:"name"`
	BlobName             string                     `json:"blobName"`
	StorageAccountURL    string                     `json:"storageAccountUrl"`
	Status               string                     `json:"status"`
	SizeInBytes          int64                      `json:"sizeInBytes"`
	WebsiteSizeInBytes   int64                      `json:"websiteSizeInBytes"`
	Created              string                     `json:"created"`
	FinishedTimeStamp    string                     `json:"finishedTimeStamp,omitempty"`
	LastRestoreTimeStamp string                     `json:"lastRestoreTimeStamp,omitempty"`
	Log                  string                     `json:"log,omitempty"`
	Scheduled            bool                       `json:"scheduled"`
	CorrelationID        string                     `json:"correlationId"`
	Databases            []WebDatabaseBackupSetting `json:"databases,omitempty"`
}

// WebAppSnapshotRow is one platform snapshot of an app: the complete archive
// of its content at that instant plus the site configuration, which is what
// SnapshotRestoreRequest.recoverConfiguration reverts. Keyed by
// "<resID>/snapshots/<time>".
type WebAppSnapshotRow struct {
	ID      string      `json:"id"`
	SiteID  string      `json:"siteId"`
	Time    string      `json:"time"`
	Archive []byte      `json:"archive"`
	Config  *SiteConfig `json:"config,omitempty"`
}

// WebDeletedSiteRow is a deleted app retained for WebApps_RestoreFromDeletedApp
// and the deletedSites reads — DeletedSiteProperties plus the content archive
// and configuration the restore replays.
type WebDeletedSiteRow struct {
	ID               string      `json:"id"`
	DeletedSiteID    int32       `json:"deletedSiteId"`
	DeletedTimestamp string      `json:"deletedTimestamp"`
	Subscription     string      `json:"subscription"`
	ResourceGroup    string      `json:"resourceGroup"`
	DeletedSiteName  string      `json:"deletedSiteName"`
	Slot             string      `json:"slot,omitempty"`
	Kind             string      `json:"kind,omitempty"`
	GeoRegionName    string      `json:"geoRegionName,omitempty"`
	Archive          []byte      `json:"archive"`
	Config           *SiteConfig `json:"config,omitempty"`
	HostNames        []string    `json:"hostNames,omitempty"`
	// Snapshots is the app's platform snapshot history, retained with it:
	// Global_GetDeletedWebAppSnapshots enumerates it, and
	// DeletedAppRestoreRequest.snapshotTime selects one of its points.
	Snapshots []WebAppSnapshotRow `json:"snapshots,omitempty"`
}

var (
	webBackupConfigs sim.Store[WebBackupConfigRow]
	webBackupItems   sim.Store[WebBackupItemRow]
	// webAppSnapshots is the primary region's snapshot store;
	// webAppSnapshotsDR is the geo-redundant secondary the
	// ListSnapshotsFromDRSecondary reads and `useDRSecondary` restores from.
	// A snapshot is replicated into the secondary as it is taken, which is
	// what a healthy geo-redundant pair holds.
	webAppSnapshots   sim.Store[WebAppSnapshotRow]
	webAppSnapshotsDR sim.Store[WebAppSnapshotRow]
	webDeletedSites   sim.Store[WebDeletedSiteRow]
)

func initWebBackupStores(srv *sim.Server) {
	webBackupConfigs = sim.MakeStore[WebBackupConfigRow](srv.DB(), "web_backup_configs")
	webBackupItems = sim.MakeStore[WebBackupItemRow](srv.DB(), "web_backup_items")
	webAppSnapshots = sim.MakeStore[WebAppSnapshotRow](srv.DB(), "web_app_snapshots")
	webAppSnapshotsDR = sim.MakeStore[WebAppSnapshotRow](srv.DB(), "web_app_snapshots_dr")
	webDeletedSites = sim.MakeStore[WebDeletedSiteRow](srv.DB(), "web_deleted_sites")
}

// webCleanupBackups removes the backup configuration, backup items and
// snapshots recorded under a deleted site or slot. The archives already
// written into a customer storage account are NOT removed: they are the
// customer's blobs, and Azure keeps them after the app is gone (which is what
// makes RestoreFromBackupBlob into a new app possible).
func webCleanupBackups(resID string) {
	webBackupConfigs.Delete(resID)
	prefix := resID + "/"
	for _, row := range webBackupItems.Filter(func(row WebBackupItemRow) bool { return strings.HasPrefix(row.ID, prefix) }) {
		webBackupItems.Delete(row.ID)
	}
	for _, row := range webAppSnapshots.Filter(func(row WebAppSnapshotRow) bool { return strings.HasPrefix(row.ID, prefix) }) {
		webAppSnapshots.Delete(row.ID)
	}
	for _, row := range webAppSnapshotsDR.Filter(func(row WebAppSnapshotRow) bool { return strings.HasPrefix(row.ID, prefix) }) {
		webAppSnapshotsDR.Delete(row.ID)
	}
}

// --- Archive and manifest ----------------------------------------------------

// webBackupContentRoot is where an App Service backup archive holds the app's
// file system: the same `site/wwwroot` layout the platform's own file system
// uses, so a caller who unzips the archive browses the paths they know.
const webBackupContentRoot = "site/wwwroot"

// webBackupManifest is the XML manifest written beside the archive: the list
// of the ZIP's contents and the app's custom domains.
type webBackupManifest struct {
	XMLName   xml.Name                  `xml:"websitebackup"`
	SiteName  string                    `xml:"siteName,attr"`
	Backup    string                    `xml:"backupName,attr"`
	Created   string                    `xml:"created,attr"`
	HostNames []string                  `xml:"hostNames>hostName"`
	Files     []webBackupManifestFile   `xml:"files>file"`
	Databases []webBackupManifestDBRow  `xml:"databases>database"`
	Config    *webBackupManifestSiteCfg `xml:"siteConfig,omitempty"`
}

type webBackupManifestFile struct {
	Path string `xml:"path,attr"`
	Size int    `xml:"size,attr"`
}

type webBackupManifestDBRow struct {
	Type                 string `xml:"databaseType,attr"`
	Name                 string `xml:"name,attr,omitempty"`
	ConnectionStringName string `xml:"connectionStringName,attr,omitempty"`
}

// webBackupManifestSiteCfg records the site configuration members the manifest
// carries so a restore into a different app can report what the source ran.
type webBackupManifestSiteCfg struct {
	LinuxFxVersion string `xml:"linuxFxVersion,attr,omitempty"`
	MinTLSVersion  string `xml:"minTlsVersion,attr,omitempty"`
}

// webBackupArchive is the pair of blobs one backup writes.
type webBackupArchive struct {
	zip      []byte
	manifest []byte
	files    int
	// contentBytes is the uncompressed size of the app content, which is what
	// BackupItemProperties.websiteSizeInBytes reports.
	contentBytes int64
}

// webCustomHostNames lists the custom domains bound to a site or slot, sorted.
// These — not the platform's own `<app>.azurewebsites.net` hostnames — are what
// Microsoft documents the backup manifest listing and a restore reconciling.
func webCustomHostNames(resID string) []string {
	prefix := resID + "/hostNameBindings/"
	var out []string
	for _, b := range webHostNameBindings.Filter(func(b WebHostNameBinding) bool {
		return strings.HasPrefix(b.ID, prefix)
	}) {
		out = append(out, strings.TrimPrefix(b.ID, prefix))
	}
	sort.Strings(out)
	return out
}

// webSiteContentFiles returns a site's deployed files sorted by path, so an
// archive built twice from the same content is byte-identical.
func webSiteContentFiles(resID string) []WebSiteContentFile {
	prefix := resID + "|"
	files := webSiteContent.Filter(func(f WebSiteContentFile) bool { return strings.HasPrefix(f.ID, prefix) })
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

// webBuildBackupArchive packs a site's deployed content into the ZIP an App
// Service backup writes, and renders the XML manifest that lists it.
func webBuildBackupArchive(resID, siteName, backupName, created string, hostNames []string,
	databases []WebDatabaseBackupSetting, cfg *SiteConfig) (webBackupArchive, error) {
	files := webSiteContentFiles(resID)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	manifest := webBackupManifest{
		SiteName:  siteName,
		Backup:    backupName,
		Created:   created,
		HostNames: hostNames,
		Files:     make([]webBackupManifestFile, 0, len(files)),
		Databases: make([]webBackupManifestDBRow, 0, len(databases)),
	}
	if cfg != nil {
		manifest.Config = &webBackupManifestSiteCfg{
			LinuxFxVersion: cfg.LinuxFxVersion,
			MinTLSVersion:  cfg.MinTLSVersion,
		}
	}
	var contentBytes int64
	for _, f := range files {
		name := webBackupContentRoot + "/" + f.Path
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		hdr.SetMode(fileModeFromPerm(mode))
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return webBackupArchive{}, fmt.Errorf("add %q to the backup archive: %w", name, err)
		}
		if _, err := w.Write(f.Data); err != nil {
			return webBackupArchive{}, fmt.Errorf("write %q into the backup archive: %w", name, err)
		}
		contentBytes += int64(len(f.Data))
		manifest.Files = append(manifest.Files, webBackupManifestFile{Path: name, Size: len(f.Data)})
	}
	for _, db := range databases {
		manifest.Databases = append(manifest.Databases, webBackupManifestDBRow{
			Type: db.DatabaseType, Name: db.Name, ConnectionStringName: db.ConnectionStringName,
		})
	}
	if err := zw.Close(); err != nil {
		return webBackupArchive{}, fmt.Errorf("close the backup archive: %w", err)
	}
	xmlBody, err := xml.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return webBackupArchive{}, fmt.Errorf("render the backup manifest: %w", err)
	}
	return webBackupArchive{
		zip:          buf.Bytes(),
		manifest:     append([]byte(xml.Header), xmlBody...),
		files:        len(files),
		contentBytes: contentBytes,
	}, nil
}

// webReadBackupArchive unpacks a backup ZIP into the site-relative content it
// holds. Entries outside `site/wwwroot` are the database export the archive
// root can carry, which the content restore does not write into the file
// system.
func webReadBackupArchive(data []byte) ([]WebSiteContentFile, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("the backup archive is not a readable ZIP file: %w", err)
	}
	var out []WebSiteContentFile
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		clean := path.Clean(strings.TrimPrefix(zf.Name, "/"))
		rel, ok := strings.CutPrefix(clean, webBackupContentRoot+"/")
		if !ok {
			continue
		}
		if rel == "" || strings.HasPrefix(rel, "../") {
			return nil, fmt.Errorf("backup archive entry %q escapes the site root", zf.Name)
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, fmt.Errorf("open backup archive entry %q: %w", zf.Name, err)
		}
		content, err := io.ReadAll(io.LimitReader(rc, webDeployPackageLimit))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read backup archive entry %q: %w", zf.Name, err)
		}
		out = append(out, WebSiteContentFile{Path: rel, Mode: uint32(zf.Mode().Perm()), Data: content})
	}
	return out, nil
}

// webReplaceSiteContent makes the site's file system exactly the given set of
// files. Microsoft: "Without `_backup.filter`, restoring a backup deletes all
// existing files in the app and replaces them with the files in the backup" —
// so every file the archive does not carry is removed, which is the opposite
// of a deployment's merge.
func webReplaceSiteContent(resID string, files []WebSiteContentFile) {
	for _, existing := range webSiteContentFiles(resID) {
		webSiteContent.Delete(existing.ID)
	}
	for _, f := range files {
		id := resID + "|" + f.Path
		webSiteContent.Put(id, WebSiteContentFile{ID: id, Path: f.Path, Mode: f.Mode, Data: f.Data})
	}
	webDiscoverWebJobs(resID)
}

// --- Storage coordinates -----------------------------------------------------

// webBackupStorageTarget is the storage account and container a
// `storageAccountUrl` addresses.
type webBackupStorageTarget struct {
	account   string
	container string
}

// webSASRequiredParams are the query members Azure requires of every service
// Shared Access Signature: the signed version, the signed resource, the signed
// permissions, the expiry and the signature itself. A `storageAccountUrl`
// missing any of them draws the documented "Missing mandatory parameters for
// valid Shared Access Signature." refusal.
//
// The signature itself is not verified: the simulator's Blob data plane
// implements no SAS authorization at all, so a signature check here would
// refuse URLs the storage plane it writes through accepts.
var webSASRequiredParams = []string{"sv", "sr", "sp", "se", "sig"}

// webParseBackupStorageURL resolves a `storageAccountUrl` into the account and
// container it names and checks the SAS parameters Azure requires. It returns
// an ARM error code and message on refusal, matching the failures Microsoft
// documents for App Service backup.
func webParseBackupStorageURL(raw string) (webBackupStorageTarget, string, string) {
	if strings.TrimSpace(raw) == "" {
		return webBackupStorageTarget{}, "InvalidRequestContent",
			"The storageAccountUrl property is required."
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return webBackupStorageTarget{}, "InvalidRequestContent",
			fmt.Sprintf("The storageAccountUrl %q is not a valid URL.", raw)
	}
	q := u.Query()
	for _, key := range webSASRequiredParams {
		if q.Get(key) == "" {
			return webBackupStorageTarget{}, "InvalidRequestContent",
				"Missing mandatory parameters for valid Shared Access Signature."
		}
	}
	hostname := u.Hostname()
	account, rest, ok := strings.Cut(hostname, ".")
	if !ok || account == "" || !strings.HasPrefix(rest, "blob.") {
		return webBackupStorageTarget{}, "CannotResolveStorageAccount",
			fmt.Sprintf("Cannot resolve %s. The host does not name a Blob service endpoint. (CannotResolveStorageAccount)", hostname)
	}
	container := strings.Trim(u.Path, "/")
	if container == "" || strings.Contains(container, "/") {
		return webBackupStorageTarget{}, "InvalidRequestContent",
			fmt.Sprintf("The storageAccountUrl %q must address a single blob container.", raw)
	}
	if _, exists := blobContainersData.Get(blobContainerKey(account, container)); !exists {
		return webBackupStorageTarget{}, "StorageAccessFailed", "Storage access failed."
	}
	return webBackupStorageTarget{account: account, container: container}, "", ""
}

// webPutBackupBlob writes one backup artifact into the Blob data plane of the
// account the backup targets — the same plane the customer downloads it from.
func webPutBackupBlob(target webBackupStorageTarget, name string, data []byte, contentType string) {
	now := time.Now().UTC().Format(http.TimeFormat)
	putBlobObject(BlobObject{
		Account:      target.account,
		Container:    target.container,
		Name:         name,
		Data:         data,
		ContentType:  contentType,
		BlobType:     "BlockBlob",
		ETag:         azureNetworkEtag(),
		LastModified: now,
		CreationTime: now,
	})
}

// webGetBackupBlob reads one backup artifact back out of the Blob data plane.
func webGetBackupBlob(target webBackupStorageTarget, name string) ([]byte, bool) {
	obj, ok := blobObjects.Get(blobObjectKey(target.account, target.container, name))
	if !ok || obj.Deleted {
		return nil, false
	}
	return obj.Data, true
}

// --- Snapshots ---------------------------------------------------------------

// webCaptureAppSnapshot records a platform snapshot of the app: the complete
// archive of its content plus its configuration at this instant, replicated
// into the geo-redundant secondary as it is written.
//
// Azure's automatic backups run on a timer and capture whatever the app holds
// at each tick, so the set of app states they can ever recover is the set of
// states the app was actually in. The simulator captures exactly those states:
// one snapshot at each point the app's content or configuration really changed.
func webCaptureAppSnapshot(resID string) {
	site, ok := webSiteRecord(resID)
	if !ok {
		return
	}
	now := time.Now().UTC()
	stamp := now.Format("2006-01-02T15:04:05.0000000Z")
	archive, err := webBuildBackupArchive(resID, webSiteNameFromID(resID), "snapshot",
		now.Format(time.RFC3339), webCustomHostNames(resID), nil, site.Properties.SiteConfig)
	if err != nil {
		return
	}
	row := WebAppSnapshotRow{
		ID:      resID + "/snapshots/" + stamp,
		SiteID:  resID,
		Time:    stamp,
		Archive: archive.zip,
		Config:  site.Properties.SiteConfig,
	}
	webAppSnapshots.Put(row.ID, row)
	webAppSnapshotsDR.Put(row.ID, row)
}

// webSnapshotStore selects the endpoint a snapshot read or restore addresses:
// the geo-redundant secondary when the request sets `useDRSecondary`, the
// primary otherwise.
func webSnapshotStore(useDRSecondary bool) sim.Store[WebAppSnapshotRow] {
	if useDRSecondary {
		return webAppSnapshotsDR
	}
	return webAppSnapshots
}

func webSnapshotsFor(resID string, useDRSecondary bool) []WebAppSnapshotRow {
	prefix := resID + "/snapshots/"
	rows := webSnapshotStore(useDRSecondary).Filter(func(row WebAppSnapshotRow) bool {
		return strings.HasPrefix(row.ID, prefix)
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].Time < rows[j].Time })
	return rows
}

// --- Helpers shared with the site records ------------------------------------

// webSiteRecord reads a site or slot by resource ID: the /slots/ segment
// decides which store owns it.
func webSiteRecord(resID string) (Site, bool) {
	if strings.Contains(resID, "/slots/") {
		return webSlots.Get(resID)
	}
	return azfSites.Get(resID)
}

func webSiteStoreFor(resID string) sim.Store[Site] {
	if strings.Contains(resID, "/slots/") {
		return webSlots
	}
	return azfSites
}

// webSiteNameFromID reads the site name (the slot name for a slot) out of a
// canonical Microsoft.Web resource ID.
func webSiteNameFromID(resID string) string {
	parts := strings.Split(strings.Trim(resID, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// fileModeFromPerm converts a stored permission bitmask into the io/fs mode a
// zip header carries.
func fileModeFromPerm(perm uint32) fs.FileMode { return fs.FileMode(perm & 0o777) }

// --- Wire rendering -----------------------------------------------------------

func backupConfigWire(r *http.Request, row WebBackupConfigRow) map[string]any {
	props := map[string]any{
		"enabled":           row.Enabled,
		"storageAccountUrl": row.StorageAccountURL,
	}
	if row.BackupName != "" {
		props["backupName"] = row.BackupName
	}
	if row.BackupSchedule != nil {
		props["backupSchedule"] = row.BackupSchedule
	}
	if len(row.Databases) > 0 {
		props["databases"] = row.Databases
	}
	return map[string]any{
		"id":         row.ID + "/config/backup",
		"name":       "backup",
		"type":       webChildType(r, "config"),
		"properties": props,
	}
}

// backupItemWire renders a BackupItem. `withSecrets` decides whether the
// storage SAS URL travels: only WebApps_ListBackupStatusSecrets returns it,
// which is the distinction that operation exists for.
func backupItemWire(r *http.Request, row WebBackupItemRow, withSecrets bool) map[string]any {
	props := map[string]any{
		"id":                 row.BackupID,
		"blobName":           row.BlobName,
		"name":               row.Name,
		"status":             row.Status,
		"sizeInBytes":        row.SizeInBytes,
		"websiteSizeInBytes": row.WebsiteSizeInBytes,
		"created":            row.Created,
		"scheduled":          row.Scheduled,
		"correlationId":      row.CorrelationID,
	}
	if withSecrets {
		props["storageAccountUrl"] = row.StorageAccountURL
	}
	if row.FinishedTimeStamp != "" {
		props["finishedTimeStamp"] = row.FinishedTimeStamp
	}
	if row.LastRestoreTimeStamp != "" {
		props["lastRestoreTimeStamp"] = row.LastRestoreTimeStamp
	}
	if row.Log != "" {
		props["log"] = row.Log
	}
	if len(row.Databases) > 0 {
		props["databases"] = row.Databases
	}
	return map[string]any{
		"id":         row.ID,
		"name":       strconv.Itoa(int(row.BackupID)),
		"type":       webChildType(r, "backups"),
		"properties": props,
	}
}

func snapshotWire(r *http.Request, row WebAppSnapshotRow) map[string]any {
	return map[string]any{
		"id":         row.ID,
		"name":       row.Time,
		"type":       webChildType(r, "snapshots"),
		"properties": map[string]any{"time": row.Time},
	}
}

func deletedSiteWire(row WebDeletedSiteRow) map[string]any {
	props := map[string]any{
		"deletedSiteId":    row.DeletedSiteID,
		"deletedTimestamp": row.DeletedTimestamp,
		"subscription":     row.Subscription,
		"resourceGroup":    row.ResourceGroup,
		"deletedSiteName":  row.DeletedSiteName,
	}
	if row.Slot != "" {
		props["slot"] = row.Slot
	}
	if row.Kind != "" {
		props["kind"] = row.Kind
	}
	if row.GeoRegionName != "" {
		props["geoRegionName"] = row.GeoRegionName
	}
	out := map[string]any{
		"id":         fmt.Sprintf("/subscriptions/%s/providers/Microsoft.Web/deletedSites/%d", row.Subscription, row.DeletedSiteID),
		"name":       strconv.Itoa(int(row.DeletedSiteID)),
		"type":       "Microsoft.Web/deletedSites",
		"properties": props,
	}
	if row.Kind != "" {
		out["kind"] = row.Kind
	}
	return out
}

// --- Deleted-app retention ----------------------------------------------------

// webRecordDeletedSite retains a just-deleted app so it can be restored into
// another app (WebApps_RestoreFromDeletedApp) and enumerated by the
// deletedSites reads. Called with the site record still in hand, before its
// content is cleaned up.
func webRecordDeletedSite(resID string, site Site) {
	parts := strings.Split(strings.Trim(resID, "/"), "/")
	var sub, rg string
	for i := 0; i+1 < len(parts); i++ {
		switch strings.ToLower(parts[i]) {
		case "subscriptions":
			sub = parts[i+1]
		case "resourcegroups":
			rg = parts[i+1]
		}
	}
	name, slot := site.Name, ""
	if idx := strings.Index(resID, "/slots/"); idx >= 0 {
		slot = resID[idx+len("/slots/"):]
		name = webSiteNameFromID(resID[:idx])
	}
	next := int32(1)
	for _, row := range webDeletedSites.List() {
		if row.DeletedSiteID >= next {
			next = row.DeletedSiteID + 1
		}
	}
	now := time.Now().UTC()
	hostNames := webCustomHostNames(resID)
	archive, err := webBuildBackupArchive(resID, name, "deleted", now.Format(time.RFC3339),
		hostNames, nil, site.Properties.SiteConfig)
	if err != nil {
		return
	}
	row := WebDeletedSiteRow{
		ID:               strconv.Itoa(int(next)),
		DeletedSiteID:    next,
		DeletedTimestamp: now.Format(time.RFC3339),
		Subscription:     sub,
		ResourceGroup:    rg,
		DeletedSiteName:  name,
		Slot:             slot,
		Kind:             site.Kind,
		GeoRegionName:    site.Location,
		Archive:          archive.zip,
		Config:           site.Properties.SiteConfig,
		HostNames:        hostNames,
		Snapshots:        webSnapshotsFor(resID, false),
	}
	webDeletedSites.Put(row.ID, row)
}

// webDeletedSitesIn lists the retained deleted apps of one subscription,
// oldest first, optionally narrowed to the region they ran in.
func webDeletedSitesIn(subscription, location string) []any {
	rows := webDeletedSites.Filter(func(row WebDeletedSiteRow) bool {
		if !strings.EqualFold(row.Subscription, subscription) {
			return false
		}
		return location == "" || strings.EqualFold(
			strings.ReplaceAll(row.GeoRegionName, " ", ""), strings.ReplaceAll(location, " ", ""))
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].DeletedSiteID < rows[j].DeletedSiteID })
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, deletedSiteWire(row))
	}
	return out
}

// webDeletedSiteByRequest resolves a DeletedAppRestoreRequest's deletedSiteId,
// which the specification documents as the deleted app's ARM resource ID
// ("/subscriptions/{subId}/providers/Microsoft.Web/deletedSites/{deletedSiteId}")
// and which callers also pass as the bare numeric id.
func webDeletedSiteByRequest(deletedSiteID string) (WebDeletedSiteRow, bool) {
	id := strings.Trim(deletedSiteID, "/")
	if idx := strings.LastIndex(id, "/"); idx >= 0 {
		id = id[idx+1:]
	}
	return webDeletedSites.Get(id)
}

// --- Custom-domain reconciliation ---------------------------------------------

// webApplyManifestHostNames reconciles the destination app's custom domains
// with the manifest's list, which is what Microsoft documents a custom-backup
// restore doing: domains in the manifest are added when nothing else already
// binds them, and domains the destination holds that the manifest does not
// list are removed.
func webApplyManifestHostNames(resID string, manifestHosts []string) {
	want := map[string]bool{}
	for _, h := range manifestHosts {
		want[strings.ToLower(h)] = true
	}
	existingPrefix := resID + "/hostNameBindings/"
	for _, b := range webHostNameBindings.Filter(func(b WebHostNameBinding) bool {
		return strings.HasPrefix(b.ID, existingPrefix)
	}) {
		if !want[strings.ToLower(strings.TrimPrefix(b.ID, existingPrefix))] {
			webHostNameBindings.Delete(b.ID)
		}
	}
	siteName := webSiteNameFromID(resID)
	for _, h := range manifestHosts {
		id := existingPrefix + h
		if _, ok := webHostNameBindings.Get(id); ok {
			continue
		}
		if webHostNameBoundElsewhere(resID, h) {
			// A DNS conflict: the domain is already bound to another app, so
			// the restore leaves it alone.
			continue
		}
		webHostNameBindings.Put(id, WebHostNameBinding{
			ID:   id,
			Name: siteName + "/" + h,
			Type: "Microsoft.Web/sites/hostNameBindings",
			Properties: WebHostNameBindingProperties{
				SiteName:     siteName,
				HostNameType: "Verified",
				SSLState:     "Disabled",
			},
		})
	}
}

// webHostNameBoundElsewhere reports a custom domain already bound to a
// different app, which is the DNS conflict that stops a restore adopting it.
func webHostNameBoundElsewhere(resID, host string) bool {
	suffix := "/hostNameBindings/" + host
	for _, b := range webHostNameBindings.List() {
		if strings.HasSuffix(b.ID, suffix) && !strings.HasPrefix(b.ID, resID+"/") {
			return true
		}
	}
	return false
}

// webManifestHostNames reads the custom-domain list out of a stored manifest.
func webManifestHostNames(manifestXML []byte) []string {
	var m webBackupManifest
	if xml.Unmarshal(manifestXML, &m) != nil {
		return nil
	}
	return m.HostNames
}

// webManifestDatabases reads the database settings a manifest records, which
// is what WebApps_DiscoverBackup reports.
func webManifestDatabases(manifestXML []byte) []WebDatabaseBackupSetting {
	var m webBackupManifest
	if xml.Unmarshal(manifestXML, &m) != nil {
		return nil
	}
	out := make([]WebDatabaseBackupSetting, 0, len(m.Databases))
	for _, db := range m.Databases {
		out = append(out, WebDatabaseBackupSetting{
			DatabaseType: db.Type, Name: db.Name, ConnectionStringName: db.ConnectionStringName,
		})
	}
	return out
}

// webManifestBlobName derives the manifest blob name from the archive blob
// name: the same base with the .xml extension, which is the pairing Microsoft
// documents ("a ZIP file that contains the backup data and an XML file that
// contains a manifest of the ZIP file contents").
func webManifestBlobName(blobName string) string {
	return strings.TrimSuffix(blobName, ".zip") + ".xml"
}
