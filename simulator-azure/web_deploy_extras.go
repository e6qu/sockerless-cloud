package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/archive"
)

// web_deploy_extras.go implements the Microsoft.Web deployment surface beyond
// the /deployments publishing log: the MSDeploy site extension (site, slot and
// per-instance variants, as Azure-AsyncOperation long-running operations),
// OneDeploy, the deploymentStatus read model, repository sync, publishing
// password rotation, and the provider-global publishing user and source
// control token catalogs.
//
// Deployments do real work: the package the request's packageUri names is
// fetched over HTTP, unpacked, and written into the site's /home/site/wwwroot
// on its persistent /home share. Webjobs are discovered from that content
// (web_webjobs.go), so the deploy → discover → run chain is the same one the
// real platform executes.

// WebMSDeployRecord is the durable state of a site's (or instance's) latest
// MSDeploy operation, served by WebApps_GetMSDeployStatus / GetMSDeployLog.
// Keyed by "<resID>[/instances/<id>]/extensions/MSDeploy".
type WebMSDeployRecord struct {
	ID                string             `json:"id"`
	SiteID            string             `json:"siteId"`
	Deployer          string             `json:"deployer"`
	ProvisioningState string             `json:"provisioningState"` // accepted/running/succeeded/failed/canceled
	StartTime         string             `json:"startTime"`
	EndTime           string             `json:"endTime,omitempty"`
	Complete          bool               `json:"complete"`
	Entries           []WebMSDeployEntry `json:"entries,omitempty"`
	PackageURI        string             `json:"packageUri,omitempty"`
}

// WebMSDeployEntry is one MSDeployLog entry (type: Message/Warning/Error).
type WebMSDeployEntry struct {
	Time    string `json:"time"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

var webMSDeployOps sim.Store[WebMSDeployRecord]

// WebOneDeployRecord is the durable state of a site's latest OneDeploy
// operation (WebApps_GetOneDeployStatus), in the Kudu deployment shape the
// real endpoint proxies.
type WebOneDeployRecord struct {
	ID           string `json:"id"`
	SiteID       string `json:"siteId"`
	DeploymentID string `json:"deploymentId"`
	Status       int    `json:"status"` // Kudu DeployStatus: 4 = Success, 3 = Failed
	StatusText   string `json:"statusText"`
	Message      string `json:"message"`
	StartTime    string `json:"startTime"`
	EndTime      string `json:"endTime,omitempty"`
	Complete     bool   `json:"complete"`
}

var webOneDeployOps sim.Store[WebOneDeployRecord]

// WebDeploymentStatusRecord is one CsmDeploymentStatus row —
// WebApps_{Get,List}ProductionSiteDeploymentStatus — created by each MSDeploy
// / OneDeploy operation. Keyed by "<resID>/deploymentStatus/<id>".
type WebDeploymentStatusRecord struct {
	ID           string   `json:"id"`
	SiteID       string   `json:"siteId"`
	DeploymentID string   `json:"deploymentId"`
	Status       string   `json:"status"` // DeploymentBuildStatus
	InProgress   int      `json:"numberOfInstancesInProgress"`
	Successful   int      `json:"numberOfInstancesSuccessful"`
	Failed       int      `json:"numberOfInstancesFailed"`
	Errors       []string `json:"errors,omitempty"`
	// OpID names the Azure Resource Manager async operation driving this
	// deployment, so the in-progress GET can advertise its Azure-AsyncOperation
	// poll URL.
	OpID string `json:"opId,omitempty"`
}

var webDeploymentStatuses sim.Store[WebDeploymentStatusRecord]

// WebDeployManifest lists the wwwroot files a site's last zip deployment
// wrote, the manifest KuduSync diffs the next zip deployment against. Keyed
// by the site's resource ID.
type WebDeployManifest struct {
	ID    string   `json:"id"`
	Paths []string `json:"paths"`
}

var webDeployManifests sim.Store[WebDeployManifest]

// WebPublishingUserRow is the provider-global publishing user
// (GetPublishingUser / UpdatePublishingUser). The password is stored but,
// like real Azure's x-ms-secret member, never echoed on reads.
type WebPublishingUserRow struct {
	PublishingUserName string `json:"publishingUserName"`
	PublishingPassword string `json:"publishingPassword,omitempty"`
	ScmURI             string `json:"scmUri,omitempty"`
}

var webPublishingUser sim.Store[WebPublishingUserRow]

// WebProviderSourceControlRow is one provider-global source control token
// (GitHub / Bitbucket / Dropbox / OneDrive), keyed by type.
type WebProviderSourceControlRow struct {
	Name           string `json:"name"`
	Token          string `json:"token,omitempty"`
	TokenSecret    string `json:"tokenSecret,omitempty"`
	RefreshToken   string `json:"refreshToken,omitempty"`
	ExpirationTime string `json:"expirationTime,omitempty"`
}

var webProviderSourceControls sim.Store[WebProviderSourceControlRow]

// webKnownSourceControls are the provider-global source control rows real
// Azure enumerates even before any token is set.
var webKnownSourceControls = []string{"Bitbucket", "Dropbox", "GitHub", "OneDrive"}

// initWebDeployStores wires the stores of this slice.
func initWebDeployStores(srv *sim.Server) {
	webMSDeployOps = sim.MakeStore[WebMSDeployRecord](srv.DB(), "web_msdeploy_ops")
	webOneDeployOps = sim.MakeStore[WebOneDeployRecord](srv.DB(), "web_onedeploy_ops")
	webDeploymentStatuses = sim.MakeStore[WebDeploymentStatusRecord](srv.DB(), "web_deployment_statuses")
	webDeployManifests = sim.MakeStore[WebDeployManifest](srv.DB(), "web_deploy_manifests")
	initWebKuduStore(srv)
	webPublishingUser = sim.MakeStore[WebPublishingUserRow](srv.DB(), "web_publishing_user")
	webProviderSourceControls = sim.MakeStore[WebProviderSourceControlRow](srv.DB(), "web_provider_sourcecontrols")
	// An MSDeploy operation persisted mid-flight lost its goroutine with the
	// previous process; real ARM reports such an operation failed rather than
	// running forever.
	for _, rec := range webMSDeployOps.List() {
		if rec.ProvisioningState == "accepted" || rec.ProvisioningState == "running" {
			webMSDeployOps.Update(rec.ID, func(row *WebMSDeployRecord) {
				row.ProvisioningState = "failed"
				row.EndTime = time.Now().UTC().Format(time.RFC3339)
				row.Complete = true
				row.Entries = append(row.Entries, WebMSDeployEntry{
					Time: row.EndTime, Type: "Error",
					Message: "The deployment was interrupted by a service restart before it completed.",
				})
			})
		}
	}
	for _, rec := range webDeploymentStatuses.List() {
		if !webDeploymentStatusTerminal(rec.Status) {
			webDeploymentStatuses.Update(rec.ID, func(row *WebDeploymentStatusRecord) {
				row.Status = "RuntimeFailed"
				row.InProgress = 0
				row.Failed = 1
				row.Errors = append(row.Errors, "The deployment was interrupted by a service restart before it completed.")
			})
		}
	}
}

// webCleanupDeployments removes every deployment artifact and record stored
// under a deleted site or slot, and drops its publishing-password rotation
// counter so a recreated site starts from fresh credentials.
func webCleanupDeployments(resID string) {
	subPrefix := resID + "/"
	for _, rec := range webMSDeployOps.Filter(func(rec WebMSDeployRecord) bool { return strings.HasPrefix(rec.ID, subPrefix) }) {
		webMSDeployOps.Delete(rec.ID)
	}
	for _, rec := range webOneDeployOps.Filter(func(rec WebOneDeployRecord) bool { return strings.HasPrefix(rec.ID, subPrefix) }) {
		webOneDeployOps.Delete(rec.ID)
	}
	for _, rec := range webDeploymentStatuses.Filter(func(rec WebDeploymentStatusRecord) bool { return strings.HasPrefix(rec.ID, subPrefix) }) {
		webDeploymentStatuses.Delete(rec.ID)
	}
	webDeployManifests.Delete(resID)
	webCleanupSlotPreview(resID)
	webCleanupKudu(resID)
	webSiteDockerLogs.Delete(strings.ToLower(resID))
	for _, protocol := range []string{"ftp", "scm"} {
		webBasicPublishingPolicies.Delete(webBasicPublishingPolicyID(resID, protocol))
	}
	if i := strings.LastIndex(resID, "/sites/"); i >= 0 {
		if site, slot, ok := strings.Cut(resID[i+len("/sites/"):], "/slots/"); ok {
			_ = os.RemoveAll(webSiteHomeDir(&Site{Name: site + "/" + slot}))
		}
	}
	azureDropKeyGens(resID, "publishingPassword")
	webCleanupBackups(resID)
}

// webPublishingPassword derives the site's current SCM publishing password:
// stable per resource ID across reads, rotated by
// WebApps_GenerateNewSitePublishingPassword through the shared key-rotation
// counter.
func webPublishingPassword(resID string) string {
	seed, _ := azureKeyGenSeed(resID, "publishingPassword")
	sum := sha256.Sum256([]byte("sim-publishing-pwd:" + resID + "|" + seed))
	return hex.EncodeToString(sum[:12])
}

// webDeployPackageLimit bounds a fetched deployment package.
const webDeployPackageLimit = 256 << 20 // 256 MiB

// webSiteContentLimit bounds the files a package or backup unpacks to: the
// 1 GB file system quota of the Free tier, the smallest App Service plan.
const webSiteContentLimit = 1 << 30

// webFetchPackage fetches the deployment package at packageURI.
func webFetchPackage(packageURI string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(packageURI)
	if err != nil {
		return nil, fmt.Errorf("fetch package: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch package: %s returned %d", packageURI, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, webDeployPackageLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read package: %w", err)
	}
	if len(data) > webDeployPackageLimit {
		return nil, fmt.Errorf("package exceeds %d bytes", webDeployPackageLimit)
	}
	return data, nil
}

// webApplyDeploymentPackage fetches the package at packageURI and unpacks it
// over the site's wwwroot, as MSDeploy syncs a package, restarting the site.
// Returns the number of files written.
func webApplyDeploymentPackage(resID, packageURI string) (int, error) {
	if packageURI == "" {
		return 0, fmt.Errorf("packageUri is required")
	}
	data, err := webFetchPackage(packageURI)
	if err != nil {
		return 0, err
	}
	return webDeployArtifact(resID, data, webArtifact{Type: "zip", Target: webWWWRoot, Restart: true})
}

// webFetchAndDeployArtifact fetches the package at packageURI and lands it as
// the artifact a describes.
func webFetchAndDeployArtifact(resID, packageURI string, a webArtifact) (int, error) {
	data, err := webFetchPackage(packageURI)
	if err != nil {
		return 0, err
	}
	return webDeployArtifact(resID, data, a)
}

// webJSONFlag renders a JSON boolean or string flag the way a query string
// carries it; an absent flag is "".
func webJSONFlag(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// webWWWRoot is the directory a site serves its content from.
const webWWWRoot = "/home/site/wwwroot"

// webArtifact is where and how one deployment artifact lands: the OneDeploy
// placement the Kudu publish API, its Azure Resource Manager twin, zip deploy
// and MSDeploy share.
type webArtifact struct {
	// Type is the OneDeploy artifact type: zip, war, jar, ear, lib, static
	// or startup.
	Type string
	// Target is an absolute path under /home: the directory a zip unpacks
	// into, the file any other artifact becomes.
	Target string
	// Clean empties the target directory before the artifact lands.
	Clean bool
	// Restart restarts the site once the artifact landed.
	Restart bool
	// SyncManifest gives zip deploy's KuduSync semantics: files the
	// previous zip deployment wrote that the new package lacks are deleted,
	// and files no zip deployment wrote are kept.
	SyncManifest bool
}

// oneDeployRefusal is a OneDeploy request Kudu refuses, worded as Kudu
// words it.
type oneDeployRefusal string

func (e oneDeployRefusal) Error() string { return string(e) }

func refuseOneDeploy(format string, args ...any) error {
	return oneDeployRefusal(fmt.Sprintf(format, args...))
}

// webOneDeployArtifact resolves the OneDeploy query a client sent — type,
// path, clean, restart — into the artifact's placement, applying each type's
// defaults the way the platform does.
func webOneDeployArtifact(site *Site, artifactType, targetPath, clean, restart string) (webArtifact, error) {
	a := webArtifact{Type: strings.ToLower(strings.TrimSpace(artifactType)), Restart: true}
	base := webWWWRoot
	switch a.Type {
	case "zip":
		a.Clean = true
		a.Target = webWWWRoot
	case "war", "jar", "ear":
		a.Clean = true
		a.Target = webWWWRoot + "/app." + a.Type
	case "static":
		if strings.TrimSpace(targetPath) == "" {
			return a, refuseOneDeploy("Path must be defined for static file deployments")
		}
	case "startup":
		if !siteIsLinux(site) {
			base = "/home/site/scripts"
			a.Target = base + "/startup.cmd"
		} else {
			a.Target = webWWWRoot + "/startup.sh"
		}
	case "lib":
		if strings.TrimSpace(targetPath) == "" {
			return a, refuseOneDeploy("Path must be defined for library deployments")
		}
		base = "/home/site/libs"
	case "":
		return a, refuseOneDeploy("Artifact type is required: one of zip, war, jar, ear, lib, static or startup")
	default:
		return a, refuseOneDeploy("Artifact type '%s' not supported: use one of zip, war, jar, ear, lib, static or startup", artifactType)
	}
	if p := strings.TrimSpace(targetPath); p != "" {
		if !strings.HasPrefix(p, "/") {
			p = base + "/" + p
		}
		cleaned := path.Clean(p)
		if cleaned != "/home" && !strings.HasPrefix(cleaned, "/home/") {
			return a, refuseOneDeploy("Path '%s' is outside /home", targetPath)
		}
		a.Target = cleaned
	}
	for _, flag := range []struct {
		raw string
		dst *bool
	}{{clean, &a.Clean}, {restart, &a.Restart}} {
		if strings.TrimSpace(flag.raw) == "" {
			continue
		}
		v, err := strconv.ParseBool(strings.TrimSpace(flag.raw))
		if err != nil {
			return a, fmt.Errorf("invalid boolean %q", flag.raw)
		}
		*flag.dst = v
	}
	return a, nil
}

// siteIsLinux reports whether the site runs on Linux.
func siteIsLinux(site *Site) bool {
	return site.Properties.Reserved || strings.Contains(strings.ToLower(site.Kind), "linux")
}

// webSiteRunsFromDeployedPackage reports whether the site has
// WEBSITE_RUN_FROM_PACKAGE=1: every zip deployment then becomes the whole of
// wwwroot, which the site mounts read-only.
func webSiteRunsFromDeployedPackage(site *Site) bool {
	return strings.TrimSpace(siteAppSettings(site)["WEBSITE_RUN_FROM_PACKAGE"]) == "1"
}

// webDeployArtifact lands one artifact under /home in the site's persistent
// storage, then rediscovers the site's webjobs, records the app-state
// snapshot, and restarts a site that runs its content, as a deployment
// restarts the app.
// Returns the number of files written.
func webDeployArtifact(resID string, data []byte, a webArtifact) (int, error) {
	site, ok := webJobSite(resID)
	if !ok {
		return 0, fmt.Errorf("site %s not found", resID)
	}
	if a.Type == "zip" && webSiteRunsFromDeployedPackage(&site) {
		a.Target, a.Clean, a.SyncManifest = webWWWRoot, true, false
	}
	var files []archive.File
	if a.Type == "zip" {
		if err := archive.ReadZip(data, webSiteContentLimit, func(f archive.File) error {
			files = append(files, f)
			return nil
		}); err != nil {
			return 0, fmt.Errorf("unpack package: %w", err)
		}
	} else {
		mode := fs.FileMode(0o644)
		if a.Type == "startup" {
			mode = 0o755
		}
		files = []archive.File{{Name: path.Base(a.Target), Mode: mode, Data: data}}
		a.Target = path.Dir(a.Target)
	}
	var written int
	var err error
	if rel, inRoot := webWWWRootRelative(a.Target); inRoot {
		written, err = webWriteSiteContent(resID, rel, files, a)
	} else {
		written, err = webWriteSiteHome(&site, a.Target, files, a.Clean)
	}
	if err != nil {
		return written, err
	}
	if err := webDiscoverWebJobs(resID); err != nil {
		return written, err
	}
	// The app's content just changed, so the platform's automatic-backup
	// snapshot of this app state exists from here on.
	webCaptureAppSnapshot(resID)
	if a.Restart {
		if site, ok := webJobSite(resID); ok {
			if _, runsStack := sitePlatformImage(&site); runsStack {
				restartAzureFunctionInstance(site)
			}
		}
	}
	return written, nil
}

// webWWWRootRelative returns target relative to wwwroot ("" for wwwroot
// itself) when it lies inside it.
func webWWWRootRelative(target string) (string, bool) {
	if target == webWWWRoot {
		return "", true
	}
	rel, ok := strings.CutPrefix(target, webWWWRoot+"/")
	return rel, ok
}

// webWriteSiteHome writes files under dir, an absolute /home path outside
// wwwroot, into the site's persistent /home storage.
func webWriteSiteHome(site *Site, dir string, files []archive.File, clean bool) (int, error) {
	home := webSiteHomeDir(site)
	rel := strings.TrimPrefix(strings.TrimPrefix(dir, "/home"), "/")
	if err := sim.EnsureWritableDir(home); err != nil {
		return 0, fmt.Errorf("create site storage: %w", err)
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return 0, fmt.Errorf("open site storage: %w", err)
	}
	defer func() { _ = root.Close() }()
	if rel == "" {
		rel = "."
	}
	if clean && rel != "." {
		if err := root.RemoveAll(rel); err != nil {
			return 0, fmt.Errorf("clean %s: %w", dir, err)
		}
	}
	for _, f := range files {
		name := path.Join(rel, f.Name)
		if err := root.MkdirAll(path.Dir(name), 0o777); err != nil {
			return 0, fmt.Errorf("write %s: %w", "/home/"+name, err)
		}
		mode := f.Mode & fs.ModePerm
		if mode == 0 {
			mode = 0o644
		}
		if err := root.WriteFile(name, f.Data, mode); err != nil {
			return 0, fmt.Errorf("write %s: %w", "/home/"+name, err)
		}
	}
	return len(files), nil
}

// webPublishingScmURI is the scmUri a site's publishing credentials carry:
// its SCM site with the credentials embedded.
func webPublishingScmURI(site *Site, user, password string) string {
	return (&url.URL{Scheme: "https", User: url.UserPassword(user, password), Host: siteScmHost(site)}).String()
}

// webSiteHomeDir is the persistent /home storage of a site or slot. A slot's
// is its own, beside its app's.
func webSiteHomeDir(site *Site) string {
	return siteHomeDir(site.Name)
}

func msDeployStatusWire(rec WebMSDeployRecord) map[string]any {
	props := map[string]any{
		"deployer":          rec.Deployer,
		"provisioningState": rec.ProvisioningState,
		"startTime":         rec.StartTime,
		"complete":          rec.Complete,
	}
	if rec.EndTime != "" {
		props["endTime"] = rec.EndTime
	}
	return map[string]any{
		"id":         rec.ID,
		"name":       "MSDeploy",
		"type":       "Microsoft.Web/sites/extensions",
		"properties": props,
	}
}

func deploymentStatusWire(rec WebDeploymentStatusRecord) map[string]any {
	props := map[string]any{
		"deploymentId":                rec.DeploymentID,
		"status":                      rec.Status,
		"numberOfInstancesInProgress": rec.InProgress,
		"numberOfInstancesSuccessful": rec.Successful,
		"numberOfInstancesFailed":     rec.Failed,
	}
	if len(rec.Errors) > 0 {
		errs := make([]any, 0, len(rec.Errors))
		for _, msg := range rec.Errors {
			errs = append(errs, map[string]any{"code": "DeploymentFailed", "message": msg})
		}
		props["errors"] = errs
	}
	return map[string]any{
		"id":         rec.ID,
		"name":       rec.DeploymentID,
		"type":       "Microsoft.Web/sites/deploymentStatus",
		"properties": props,
	}
}

func webDeploymentStatusTerminal(status string) bool {
	switch status {
	case "RuntimeSuccessful", "RuntimeFailed", "BuildFailed", "BuildAborted", "TimedOut":
		return true
	}
	return false
}

func oneDeployWire(rec WebOneDeployRecord) map[string]any {
	// The Kudu deployment object OneDeploy status reads proxy (the swagger
	// declares no schema for this operation).
	out := map[string]any{
		"id":          rec.DeploymentID,
		"status":      rec.Status,
		"status_text": rec.StatusText,
		"message":     rec.Message,
		"deployer":    "OneDeploy",
		"start_time":  rec.StartTime,
		"complete":    rec.Complete,
		"active":      rec.Status == 4,
	}
	if rec.EndTime != "" {
		out["end_time"] = rec.EndTime
	}
	return out
}

func publishingUserWire(row WebPublishingUserRow) map[string]any {
	props := map[string]any{"publishingUserName": row.PublishingUserName}
	if row.ScmURI != "" {
		props["scmUri"] = row.ScmURI
	}
	return map[string]any{
		"id":         "/providers/Microsoft.Web/publishingUsers/web",
		"name":       "web",
		"type":       "Microsoft.Web/publishingUsers",
		"properties": props,
	}
}

func providerSourceControlWire(row WebProviderSourceControlRow) map[string]any {
	props := map[string]any{}
	if row.Token != "" {
		props["token"] = row.Token
	}
	if row.TokenSecret != "" {
		props["tokenSecret"] = row.TokenSecret
	}
	if row.RefreshToken != "" {
		props["refreshToken"] = row.RefreshToken
	}
	if row.ExpirationTime != "" {
		props["expirationTime"] = row.ExpirationTime
	}
	return map[string]any{
		"id":         "/providers/Microsoft.Web/sourcecontrols/" + row.Name,
		"name":       row.Name,
		"type":       "Microsoft.Web/sourcecontrols",
		"properties": props,
	}
}

// registerWebDeploymentExtras mounts the site/slot-level deployment routes.
// The MSDeploy extension exists on sites, slots and their instances; OneDeploy
// exists on production sites only, exactly as the specification declares.
func registerWebDeploymentExtras(both, site func(string, string, http.HandlerFunc)) {
	msDeployGet := func(idSuffix string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if webMissing(w, r) {
				return
			}
			rec, ok := webMSDeployOps.Get(webResourceID(r) + idSuffixExpand(r, idSuffix) + "/extensions/MSDeploy")
			if !ok {
				AzureErrorf(w, "NotFound", http.StatusNotFound,
					"No MSDeploy deployment found for %q.", sim.PathParam(r, "siteName"))
				return
			}
			sim.WriteJSON(w, http.StatusOK, msDeployStatusWire(rec))
		}
	}
	msDeployLog := func(idSuffix string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if webMissing(w, r) {
				return
			}
			rec, ok := webMSDeployOps.Get(webResourceID(r) + idSuffixExpand(r, idSuffix) + "/extensions/MSDeploy")
			if !ok {
				AzureErrorf(w, "NotFound", http.StatusNotFound,
					"No MSDeploy deployment found for %q.", sim.PathParam(r, "siteName"))
				return
			}
			entries := make([]any, 0, len(rec.Entries))
			for _, e := range rec.Entries {
				entries = append(entries, map[string]any{"time": e.Time, "type": e.Type, "message": e.Message})
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"id":         rec.ID + "/log",
				"name":       "log",
				"type":       "Microsoft.Web/sites/extensions/log",
				"properties": map[string]any{"entries": entries},
			})
		}
	}
	msDeployPut := func(idSuffix string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if webMissing(w, r) {
				return
			}
			var req struct {
				Properties struct {
					PackageURI string `json:"packageUri"`
				} `json:"properties"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
				return
			}
			if req.Properties.PackageURI == "" {
				AzureError(w, "InvalidRequestContent", "The MSDeploy request must name a packageUri.", http.StatusBadRequest)
				return
			}
			resID := webResourceID(r)
			recID := resID + idSuffixExpand(r, idSuffix) + "/extensions/MSDeploy"
			if existing, ok := webMSDeployOps.Get(recID); ok &&
				(existing.ProvisioningState == "accepted" || existing.ProvisioningState == "running") {
				// Real MSDeploy refuses a second deployment while one runs.
				AzureError(w, "Conflict", "Another MSDeploy operation is in progress.", http.StatusConflict)
				return
			}
			now := time.Now().UTC().Format(time.RFC3339)
			rec := WebMSDeployRecord{
				ID:                recID,
				SiteID:            resID,
				Deployer:          "ARM",
				ProvisioningState: "running",
				StartTime:         now,
				PackageURI:        req.Properties.PackageURI,
				Entries: []WebMSDeployEntry{{
					Time: now, Type: "Message",
					Message: "Deployment started: fetching package " + req.Properties.PackageURI,
				}},
			}
			webMSDeployOps.Put(recID, rec)

			deployStatusID := sim.NewUUID()
			statusRecID := resID + "/deploymentStatus/" + deployStatusID
			webDeploymentStatuses.Put(statusRecID, WebDeploymentStatusRecord{
				ID:           statusRecID,
				SiteID:       resID,
				DeploymentID: deployStatusID,
				Status:       "BuildRequestReceived",
				InProgress:   1,
			})

			opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
				written, err := webApplyDeploymentPackage(resID, req.Properties.PackageURI)
				end := time.Now().UTC().Format(time.RFC3339)
				if err != nil {
					webMSDeployOps.Update(recID, func(row *WebMSDeployRecord) {
						row.ProvisioningState = "failed"
						row.EndTime = end
						row.Complete = true
						row.Entries = append(row.Entries, WebMSDeployEntry{Time: end, Type: "Error", Message: err.Error()})
					})
					webDeploymentStatuses.Update(statusRecID, func(row *WebDeploymentStatusRecord) {
						row.Status = "BuildFailed"
						row.InProgress = 0
						row.Failed = 1
						row.Errors = append(row.Errors, err.Error())
						row.OpID = ""
					})
					return &AsyncOperationError{Code: "DeploymentFailed", Message: err.Error()}
				}
				webMSDeployOps.Update(recID, func(row *WebMSDeployRecord) {
					row.ProvisioningState = "succeeded"
					row.EndTime = end
					row.Complete = true
					row.Entries = append(row.Entries, WebMSDeployEntry{
						Time: end, Type: "Message",
						Message: fmt.Sprintf("Deployment succeeded: %d file(s) deployed.", written),
					})
				})
				webStartDeploymentRuntime(statusRecID, resID)
				return nil
			})
			webDeploymentStatuses.Update(statusRecID, func(row *WebDeploymentStatusRecord) {
				if !webDeploymentStatusTerminal(row.Status) && row.Status != "RuntimeStarting" {
					row.OpID = opID
				}
			})

			opURL := azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"),
				"Microsoft.Web", webSiteOperationLocation(resID), "operationStatuses", opID, r.URL.Query().Get("api-version"))
			writeAzureAsyncCreateHeaders(w, opID, opURL, azureCurrentRequestURL(r))
			sim.WriteJSON(w, http.StatusCreated, msDeployStatusWire(rec))
		}
	}

	// Site/slot-level MSDeploy extension.
	both("GET", "/extensions/MSDeploy", msDeployGet(""))
	both("PUT", "/extensions/MSDeploy", msDeployPut(""))
	both("GET", "/extensions/MSDeploy/log", msDeployLog(""))
	// Per-instance MSDeploy extension.
	both("GET", "/instances/{instanceId}/extensions/MSDeploy", msDeployGet("instance"))
	both("PUT", "/instances/{instanceId}/extensions/MSDeploy", msDeployPut("instance"))
	both("GET", "/instances/{instanceId}/extensions/MSDeploy/log", msDeployLog("instance"))

	// OneDeploy — production site only, per the specification.
	site("GET", "/extensions/onedeploy", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		rec, ok := webOneDeployOps.Get(webResourceID(r) + "/extensions/onedeploy")
		if !ok {
			AzureErrorf(w, "NotFound", http.StatusNotFound,
				"No OneDeploy deployment found for %q.", sim.PathParam(r, "siteName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, oneDeployWire(rec))
	})
	site("PUT", "/extensions/onedeploy", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req struct {
			Properties struct {
				PackageURI string `json:"packageUri"`
				Type       string `json:"type"`
				Path       string `json:"path"`
				Clean      any    `json:"clean"`
				Restart    any    `json:"restart"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.PackageURI == "" {
			AzureError(w, "InvalidRequestContent", "The OneDeploy request must name a packageUri.", http.StatusBadRequest)
			return
		}
		site, _ := webResource(r)
		artifact, err := webOneDeployArtifact(&site, req.Properties.Type, req.Properties.Path,
			webJSONFlag(req.Properties.Clean), webJSONFlag(req.Properties.Restart))
		if err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		resID := webResourceID(r)
		recID := resID + "/extensions/onedeploy"
		start := time.Now().UTC()
		deploymentID := sim.NewUUID()
		written, err := webFetchAndDeployArtifact(resID, req.Properties.PackageURI, artifact)
		end := time.Now().UTC().Format(time.RFC3339)
		rec := WebOneDeployRecord{
			ID:           recID,
			SiteID:       resID,
			DeploymentID: deploymentID,
			StartTime:    start.Format(time.RFC3339),
			EndTime:      end,
			Complete:     true,
		}
		statusRecID := resID + "/deploymentStatus/" + deploymentID
		status := WebDeploymentStatusRecord{
			ID:           statusRecID,
			SiteID:       resID,
			DeploymentID: deploymentID,
		}
		if err != nil {
			rec.Status = 3
			rec.StatusText = "Failed"
			rec.Message = err.Error()
			status.Status = "BuildFailed"
			status.Failed = 1
			status.Errors = []string{err.Error()}
			webOneDeployOps.Put(recID, rec)
			webDeploymentStatuses.Put(statusRecID, status)
			AzureError(w, "DeploymentFailed", err.Error(), http.StatusBadRequest)
			return
		}
		rec.Status = 4
		rec.StatusText = "Success"
		rec.Message = fmt.Sprintf("OneDeploy succeeded: %d file(s) deployed.", written)
		status.InProgress = 1
		webOneDeployOps.Put(recID, rec)
		webDeploymentStatuses.Put(statusRecID, status)
		webStartDeploymentRuntime(statusRecID, resID)
		sim.WriteJSON(w, http.StatusOK, oneDeployWire(rec))
	})

	// deploymentStatus — the CsmDeploymentStatus read model.
	both("GET", "/deploymentStatus", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		prefix := webResourceID(r) + "/deploymentStatus/"
		recs := webDeploymentStatuses.Filter(func(rec WebDeploymentStatusRecord) bool { return strings.HasPrefix(rec.ID, prefix) })
		sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
		out := make([]any, 0, len(recs))
		for _, rec := range recs {
			out = append(out, deploymentStatusWire(rec))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	both("GET", "/deploymentStatus/{deploymentStatusId}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		rec, ok := webDeploymentStatuses.Get(webResourceID(r) + "/deploymentStatus/" + sim.PathParam(r, "deploymentStatusId"))
		if !ok {
			AzureErrorf(w, "NotFound", http.StatusNotFound,
				"Deployment status %q not found.", sim.PathParam(r, "deploymentStatusId"))
			return
		}
		// The operation is declared long-running: while the deployment is
		// still building, answer 202 with the poll coordinates (the driving
		// operation's Azure-AsyncOperation URL and this URL as Location);
		// terminal states answer 200.
		if !webDeploymentStatusTerminal(rec.Status) {
			if rec.OpID != "" {
				w.Header().Set("Azure-AsyncOperation", azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"),
					"Microsoft.Web", webSiteOperationLocation(rec.SiteID), "operationStatuses", rec.OpID,
					r.URL.Query().Get("api-version")))
			}
			w.Header().Set("Location", azureCurrentRequestURL(r))
			w.Header().Set("Retry-After", azureAsyncOperationRetryAfter)
			sim.WriteJSON(w, http.StatusAccepted, deploymentStatusWire(rec))
			return
		}
		sim.WriteJSON(w, http.StatusOK, deploymentStatusWire(rec))
	})

	// POST /sync — WebApps_SyncRepository: re-sync the configured source
	// control. The sim's source-control record has no remote content to pull,
	// so the sync finds nothing to reconcile and reports success.
	both("POST", "/sync", okIfExists)

	// POST /newpassword — WebApps_GenerateNewSitePublishingPassword: rotate
	// the site's SCM publishing password. The rotation is observable through
	// POST /config/publishingcredentials/list, which derives from the same
	// rotation counter.
	both("POST", "/newpassword", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		azureBumpKeyGen(webResourceID(r), "publishingPassword", "")
		w.WriteHeader(http.StatusOK)
	})
}

// idSuffixExpand resolves the store-key suffix for the MSDeploy handler
// variants: "" for the site/slot extension, the instance path for the
// per-instance extension.
func idSuffixExpand(r *http.Request, kind string) string {
	if kind == "instance" {
		return "/instances/" + sim.PathParam(r, "instanceId")
	}
	return ""
}

// webSiteOperationLocation names the location segment of an async operation
// URL for a site-scoped operation: the site's own region when the record
// exists, Azure's default US region otherwise.
func webSiteOperationLocation(resID string) string {
	if site, ok := webJobSite(resID); ok && site.Location != "" {
		return strings.ToLower(strings.ReplaceAll(site.Location, " ", ""))
	}
	return "eastus"
}

// registerWebPublishingGlobals mounts the provider-global (subscription-free)
// Microsoft.Web routes: the publishing user and the source control token
// catalog.
func registerWebPublishingGlobals(srv *sim.Server) {
	// GET /providers/Microsoft.Web/publishingUsers/web — GetPublishingUser.
	srv.HandleFunc("GET /providers/Microsoft.Web/publishingUsers/web", func(w http.ResponseWriter, r *http.Request) {
		row, _ := webPublishingUser.Get("web")
		sim.WriteJSON(w, http.StatusOK, publishingUserWire(row))
	})
	// PUT /providers/Microsoft.Web/publishingUsers/web — UpdatePublishingUser.
	srv.HandleFunc("PUT /providers/Microsoft.Web/publishingUsers/web", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Properties WebPublishingUserRow `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.PublishingUserName == "" {
			AzureError(w, "InvalidRequestContent", "The publishingUserName property is required.", http.StatusBadRequest)
			return
		}
		webPublishingUser.Put("web", req.Properties)
		sim.WriteJSON(w, http.StatusOK, publishingUserWire(req.Properties))
	})

	// GET /providers/Microsoft.Web/sourcecontrols — ListSourceControls: the
	// provider catalog, merged with any stored tokens.
	srv.HandleFunc("GET /providers/Microsoft.Web/sourcecontrols", func(w http.ResponseWriter, r *http.Request) {
		out := make([]any, 0, len(webKnownSourceControls))
		for _, name := range webKnownSourceControls {
			row, ok := webProviderSourceControls.Get(name)
			if !ok {
				row = WebProviderSourceControlRow{Name: name}
			}
			out = append(out, providerSourceControlWire(row))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	// GET /providers/Microsoft.Web/sourcecontrols/{sourceControlType} —
	// GetSourceControl.
	srv.HandleFunc("GET /providers/Microsoft.Web/sourcecontrols/{sourceControlType}", func(w http.ResponseWriter, r *http.Request) {
		name := sim.PathParam(r, "sourceControlType")
		row, ok := webProviderSourceControls.Get(name)
		if !ok {
			row = WebProviderSourceControlRow{Name: name}
		}
		sim.WriteJSON(w, http.StatusOK, providerSourceControlWire(row))
	})
	// PUT /providers/Microsoft.Web/sourcecontrols/{sourceControlType} —
	// UpdateSourceControl: store the OAuth token set. The token is echoed, as
	// real Azure echoes the SourceControl resource written.
	srv.HandleFunc("PUT /providers/Microsoft.Web/sourcecontrols/{sourceControlType}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Properties WebProviderSourceControlRow `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		row := req.Properties
		row.Name = sim.PathParam(r, "sourceControlType")
		webProviderSourceControls.Put(row.Name, row)
		sim.WriteJSON(w, http.StatusOK, providerSourceControlWire(row))
	})
}
