package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/archive"
)

// The Microsoft.Web backup, restore and snapshot family, at site and slot
// scope. A backup ZIPs the site's /home/site/wwwroot into the Blob data plane
// of the account `storageAccountUrl` names; a restore reads the blob back and
// replaces the site's content, so deleting the blob makes the restore fail.
//
// The shapes follow Microsoft's contract
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

type WebBackupSchedule struct {
	FrequencyInterval     int32  `json:"frequencyInterval"`
	FrequencyUnit         string `json:"frequencyUnit"`
	KeepAtLeastOneBackup  bool   `json:"keepAtLeastOneBackup"`
	RetentionPeriodInDays int32  `json:"retentionPeriodInDays"`
	StartTime             string `json:"startTime,omitempty"`
	LastExecutionTime     string `json:"lastExecutionTime,omitempty"`
}

// `databaseType` is the only required member.
type WebDatabaseBackupSetting struct {
	DatabaseType         string `json:"databaseType"`
	Name                 string `json:"name,omitempty"`
	ConnectionStringName string `json:"connectionStringName,omitempty"`
	ConnectionString     string `json:"connectionString,omitempty"`
}

// The BackupRequestProperties WebApps_UpdateBackupConfiguration writes, keyed
// by canonical resource ID.
type WebBackupConfigRow struct {
	ID                string                     `json:"id"`
	BackupName        string                     `json:"backupName,omitempty"`
	Enabled           bool                       `json:"enabled"`
	StorageAccountURL string                     `json:"storageAccountUrl"`
	BackupSchedule    *WebBackupSchedule         `json:"backupSchedule,omitempty"`
	Databases         []WebDatabaseBackupSetting `json:"databases,omitempty"`
}

// BackupItemProperties, keyed by "<resID>/backups/<backupId>".
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

// The app's content archive and site configuration at one instant, which
// recoverConfiguration reverts. Keyed by "<resID>/snapshots/<time>".
type WebAppSnapshotRow struct {
	ID      string      `json:"id"`
	SiteID  string      `json:"siteId"`
	Time    string      `json:"time"`
	Archive []byte      `json:"archive"`
	Config  *SiteConfig `json:"config,omitempty"`
}

// Retained for WebApps_RestoreFromDeletedApp and the deletedSites reads, with
// the content archive and configuration the restore replays.
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
	// Global_GetDeletedWebAppSnapshots enumerates these, and
	// DeletedAppRestoreRequest.snapshotTime selects one.
	Snapshots []WebAppSnapshotRow `json:"snapshots,omitempty"`
}

var (
	webBackupConfigs sim.Store[WebBackupConfigRow]
	webBackupItems   sim.Store[WebBackupItemRow]
	// webAppSnapshotsDR is the geo-redundant secondary ListSnapshotsFromDRSecondary
	// and `useDRSecondary` read; each snapshot replicates into it as taken, as in
	// a healthy pair.
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

// Archives already in a customer storage account stay: they are the customer's
// blobs, which is what makes RestoreFromBackupBlob into a new app possible.
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

// The platform's own `site/wwwroot` layout, so an unzipped archive shows
// familiar paths.
const webBackupContentRoot = "site/wwwroot"

// The ZIP's contents and the app's custom domains.
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

// Lets a restore into a different app report what the source ran.
type webBackupManifestSiteCfg struct {
	LinuxFxVersion string `xml:"linuxFxVersion,attr,omitempty"`
	MinTLSVersion  string `xml:"minTlsVersion,attr,omitempty"`
}

type webBackupArchive struct {
	zip      []byte
	manifest []byte
	files    int
	// The uncompressed content size, BackupItemProperties.websiteSizeInBytes.
	contentBytes int64
}

var webHostNameBindingsBySite sim.GenerationIndex[WebHostNameBinding]

// "" when the ID holds no such segment.
func webChildParent(id, segment string) string {
	if i := strings.LastIndex(id, segment); i >= 0 {
		return id[:i]
	}
	return ""
}

// Custom domains, not the platform's `<app>.azurewebsites.net`, are what the
// manifest lists and a restore reconciles.
func webCustomHostNames(resID string) []string {
	prefix := resID + "/hostNameBindings/"
	var out []string
	for _, b := range webHostNameBindingsBySite.LookupAll(webHostNameBindings, resID, func(b WebHostNameBinding) []string {
		return []string{webChildParent(b.ID, "/hostNameBindings/")}
	}) {
		out = append(out, strings.TrimPrefix(b.ID, prefix))
	}
	sort.Strings(out)
	return out
}

func webBuildBackupArchive(resID, siteName, backupName, created string, hostNames []string,
	databases []WebDatabaseBackupSetting, cfg *SiteConfig) (webBackupArchive, error) {
	files, err := webSiteContentFiles(resID)
	if err != nil {
		return webBackupArchive{}, err
	}
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

// Entries outside `site/wwwroot` are the database export, which the content
// restore does not write.
func webReadBackupArchive(data []byte) ([]WebSiteContentFile, error) {
	var out []WebSiteContentFile
	err := archive.ReadZip(data, webSiteContentLimit, func(f archive.File) error {
		if rel, ok := strings.CutPrefix(f.Name, webBackupContentRoot+"/"); ok {
			out = append(out, WebSiteContentFile{Path: rel, Mode: uint32(f.Mode), Data: f.Data})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the backup archive: %w", err)
	}
	return out, nil
}

type webBackupStorageTarget struct {
	account   string
	container string
}

// Azure requires the signed version, permissions, expiry and signature of every
// SAS; a URL missing one draws "Missing mandatory parameters for valid Shared
// Access Signature." The signature itself is verified against the account's
// keys, as the Blob data plane does (blob_authorization.go). A service SAS
// carries a signedResource; an account SAS — what
// `data.azurerm_storage_account_sas` emits for `storage_account_url` — carries
// services and resource types instead, and both are accepted.
var webSASRequiredParams = []string{"sv", "sp", "se", "sig"}

// Returns an error code and message on refusal, matching the failures
// Microsoft documents for App Service backup.
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
	if q.Get("sr") == "" && (q.Get("ss") == "" || q.Get("srt") == "") {
		return webBackupStorageTarget{}, "InvalidRequestContent",
			"Missing mandatory parameters for valid Shared Access Signature."
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
	// App Service writes through this URL, so a signature the Blob plane would
	// refuse cannot write the backup.
	if ok, _ := blobSASAuthorizes("blob", account, container, "", q, http.MethodPut); !ok {
		return webBackupStorageTarget{}, "StorageAccessFailed", "Storage access failed."
	}
	return webBackupStorageTarget{account: account, container: container}, "", ""
}

// The same plane the customer downloads it from.
func webPutBackupBlob(target webBackupStorageTarget, name string, data []byte, contentType string) error {
	now := time.Now().UTC().Format(http.TimeFormat)
	return putBlobWithContents(BlobObject{
		Account:      target.account,
		Container:    target.container,
		Name:         name,
		ContentType:  contentType,
		BlobType:     "BlockBlob",
		ETag:         azureNetworkEtag(),
		LastModified: now,
		CreationTime: now,
	}, data)
}

func webGetBackupBlob(target webBackupStorageTarget, name string) ([]byte, bool, error) {
	obj, ok := blobObjects.Get(blobObjectKey(target.account, target.container, name))
	if !ok || obj.Deleted {
		return nil, false, nil
	}
	_, data, err := blobData(obj)
	return data, true, err
}

// Azure's automatic backups capture whatever the app holds at each tick, so
// the states they can recover are states the app was in. Capture one snapshot
// at each real change of content or configuration, replicated into the
// geo-redundant secondary.
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

// The secondary when the request sets `useDRSecondary`, the primary otherwise.
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

// The /slots/ segment decides which store owns the ID.
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

// The slot name for a slot.
func webSiteNameFromID(resID string) string {
	parts := strings.Split(strings.Trim(resID, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func fileModeFromPerm(perm uint32) fs.FileMode { return fs.FileMode(perm & 0o777) }

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

// Only WebApps_ListBackupStatusSecrets returns the SAS URL, the distinction
// that operation exists for.
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
		"id":         armSubscriptionResourceID(row.Subscription, "Microsoft.Web", "deletedSites", strconv.Itoa(int(row.DeletedSiteID))),
		"name":       strconv.Itoa(int(row.DeletedSiteID)),
		"type":       "Microsoft.Web/deletedSites",
		"properties": props,
	}
	if row.Kind != "" {
		out["kind"] = row.Kind
	}
	return out
}

// Called with the site record in hand, before its content is cleaned up.
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

// Oldest first, optionally narrowed to a region.
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

// The specification documents deletedSiteId as the deleted app's resource ID
// ("/subscriptions/{subId}/providers/Microsoft.Web/deletedSites/{deletedSiteId}");
// callers also pass the bare numeric ID.
func webDeletedSiteByRequest(deletedSiteID string) (WebDeletedSiteRow, bool) {
	id := strings.Trim(deletedSiteID, "/")
	if idx := strings.LastIndex(id, "/"); idx >= 0 {
		id = id[idx+1:]
	}
	return webDeletedSites.Get(id)
}

// As Microsoft documents a custom-backup restore: manifest domains are added
// when nothing else binds them, and destination domains the manifest omits are
// removed.
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
			// A DNS conflict: another app binds the domain, so the restore leaves it.
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

func webHostNameBoundElsewhere(resID, host string) bool {
	suffix := "/hostNameBindings/" + host
	for _, b := range webHostNameBindings.List() {
		if strings.HasSuffix(b.ID, suffix) && !strings.HasPrefix(b.ID, resID+"/") {
			return true
		}
	}
	return false
}

func webManifestHostNames(manifestXML []byte) []string {
	var m webBackupManifest
	if xml.Unmarshal(manifestXML, &m) != nil {
		return nil
	}
	return m.HostNames
}

// What WebApps_DiscoverBackup reports.
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

// The archive's base name with .xml, the pairing Microsoft documents.
func webManifestBlobName(blobName string) string {
	return strings.TrimSuffix(blobName, ".zip") + ".xml"
}
