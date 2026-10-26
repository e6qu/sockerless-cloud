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

// Deployment beyond the /deployments log: the MSDeploy extension (site, slot
// and instance, as long-running operations), OneDeploy, deploymentStatus,
// repository sync, publishing-password rotation, and the provider-global
// publishing user and source control catalogs. A deployment fetches the
// packageUri, unpacks it into /home/site/wwwroot on the site's persistent
// /home share, and rediscovers webjobs from it (web_webjobs.go).

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

type WebMSDeployEntry struct {
	Time    string `json:"time"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

var webMSDeployOps sim.Store[WebMSDeployRecord]

// In the Kudu deployment shape the endpoint proxies.
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

// CsmDeploymentStatus, created by each MSDeploy or OneDeploy, keyed by
// "<resID>/deploymentStatus/<id>".
type WebDeploymentStatusRecord struct {
	ID           string   `json:"id"`
	SiteID       string   `json:"siteId"`
	DeploymentID string   `json:"deploymentId"`
	Status       string   `json:"status"` // DeploymentBuildStatus
	InProgress   int      `json:"numberOfInstancesInProgress"`
	Successful   int      `json:"numberOfInstancesSuccessful"`
	Failed       int      `json:"numberOfInstancesFailed"`
	Errors       []string `json:"errors,omitempty"`
	// The driving async operation, whose URL the in-progress GET advertises.
	OpID string `json:"opId,omitempty"`
}

var webDeploymentStatuses sim.Store[WebDeploymentStatusRecord]

// The files the last zip deployment wrote, which KuduSync diffs the next one
// against. Keyed by site resource ID.
type WebDeployManifest struct {
	ID    string   `json:"id"`
	Paths []string `json:"paths"`
}

var webDeployManifests sim.Store[WebDeployManifest]

// The password is stored but never echoed, as Azure's x-ms-secret member.
type WebPublishingUserRow struct {
	PublishingUserName string `json:"publishingUserName"`
	PublishingPassword string `json:"publishingPassword,omitempty"`
	ScmURI             string `json:"scmUri,omitempty"`
}

var webPublishingUser sim.Store[WebPublishingUserRow]

// Keyed by type: GitHub, Bitbucket, Dropbox, OneDrive.
type WebProviderSourceControlRow struct {
	Name           string `json:"name"`
	Token          string `json:"token,omitempty"`
	TokenSecret    string `json:"tokenSecret,omitempty"`
	RefreshToken   string `json:"refreshToken,omitempty"`
	ExpirationTime string `json:"expirationTime,omitempty"`
}

var webProviderSourceControls sim.Store[WebProviderSourceControlRow]

// Azure lists these before any token is set.
var webKnownSourceControls = []string{"Bitbucket", "Dropbox", "GitHub", "OneDrive"}

func initWebDeployStores(srv *sim.Server) {
	webMSDeployOps = sim.MakeStore[WebMSDeployRecord](srv.DB(), "web_msdeploy_ops")
	webOneDeployOps = sim.MakeStore[WebOneDeployRecord](srv.DB(), "web_onedeploy_ops")
	webDeploymentStatuses = sim.MakeStore[WebDeploymentStatusRecord](srv.DB(), "web_deployment_statuses")
	webDeployManifests = sim.MakeStore[WebDeployManifest](srv.DB(), "web_deploy_manifests")
	initWebKuduStore(srv)
	webPublishingUser = sim.MakeStore[WebPublishingUserRow](srv.DB(), "web_publishing_user")
	webProviderSourceControls = sim.MakeStore[WebProviderSourceControlRow](srv.DB(), "web_provider_sourcecontrols")
	// An MSDeploy persisted mid-flight lost its goroutine; Azure Resource Manager
	// reports such an operation failed rather than running forever.
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

// Also drops the publishing-password rotation counter, so a recreated site
// starts from fresh credentials.
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
	webSiteDockerLogs.Drop(strings.ToLower(resID))
	webForgetDockerLogUsage(resID)
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

// Stable per resource ID; WebApps_GenerateNewSitePublishingPassword rotates it
// through the shared key-rotation counter.
func webPublishingPassword(resID string) string {
	seed, _ := azureKeyGenSeed(resID, "publishingPassword")
	sum := sha256.Sum256([]byte("sim-publishing-pwd:" + resID + "|" + seed))
	return hex.EncodeToString(sum[:12])
}

const webDeployPackageLimit = 256 << 20 // 256 MiB

// The 1 GB file system quota of the Free tier, the smallest plan.
const webSiteContentLimit = 1 << 30

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

// Unpacks over wwwroot as MSDeploy syncs a package, restarting the site.
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

func webFetchAndDeployArtifact(resID, packageURI string, a webArtifact) (int, error) {
	data, err := webFetchPackage(packageURI)
	if err != nil {
		return 0, err
	}
	return webDeployArtifact(resID, data, a)
}

// An absent flag is "".
func webJSONFlag(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

const webWWWRoot = "/home/site/wwwroot"

// The OneDeploy placement shared by the Kudu publish API, its Azure Resource
// Manager twin, zip deploy and MSDeploy.
type webArtifact struct {
	// zip, war, jar, ear, lib, static or startup.
	Type string
	// An absolute path under /home: the directory a zip unpacks into, the file any
	// other artifact becomes.
	Target  string
	Clean   bool
	Restart bool
	// KuduSync semantics: files the previous zip deployment wrote that the new
	// package lacks are deleted; files no zip deployment wrote are kept.
	SyncManifest bool
}

// Worded as Kudu words it.
type oneDeployRefusal string

func (e oneDeployRefusal) Error() string { return string(e) }

func refuseOneDeploy(format string, args ...any) error {
	return oneDeployRefusal(fmt.Sprintf(format, args...))
}

// Applies each type's defaults as the platform does.
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

func siteIsLinux(site *Site) bool {
	return site.Properties.Reserved || strings.Contains(strings.ToLower(site.Kind), "linux")
}

// With WEBSITE_RUN_FROM_PACKAGE=1 every zip deployment becomes all of wwwroot,
// mounted read-only.
func webSiteRunsFromDeployedPackage(site *Site) bool {
	return strings.TrimSpace(siteAppSettings(site)["WEBSITE_RUN_FROM_PACKAGE"]) == "1"
}

// Then rediscovers webjobs, records the app-state snapshot, and restarts a site
// running its content, as a deployment restarts the app. Returns the number of
// files written.
func webDeployArtifact(resID string, data []byte, a webArtifact) (int, error) {
	site, ok := webJobSite(resID)
	if !ok {
		return 0, fmt.Errorf("site %s not found", resID)
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
	return webDeployFiles(&site, files, a)
}

// Then rediscovers webjobs, records the app-state snapshot, and restarts a site
// running its content. Returns the number of files written.
func webDeployFiles(site *Site, files []archive.File, a webArtifact) (int, error) {
	resID := site.ID
	if a.Type == "zip" && webSiteRunsFromDeployedPackage(site) {
		a.Target, a.Clean, a.SyncManifest = webWWWRoot, true, false
	}
	var written int
	var err error
	if rel, inRoot := webWWWRootRelative(a.Target); inRoot {
		written, err = webWriteSiteContent(resID, rel, files, a)
	} else {
		written, err = webWriteSiteHome(site, a.Target, files, a.Clean)
	}
	if err != nil {
		return written, err
	}
	if err := webDiscoverWebJobs(resID); err != nil {
		return written, err
	}
	// The content changed, so the automatic-backup snapshot of this state exists
	// from here on.
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

// "" for wwwroot itself.
func webWWWRootRelative(target string) (string, bool) {
	if target == webWWWRoot {
		return "", true
	}
	rel, ok := strings.CutPrefix(target, webWWWRoot+"/")
	return rel, ok
}

// dir is an absolute /home path outside wwwroot.
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

// The SCM site with the credentials embedded.
func webPublishingScmURI(site *Site, user, password string) string {
	return (&url.URL{Scheme: "https", User: url.UserPassword(user, password), Host: siteScmHost(site)}).String()
}

// A slot's is its own, beside its app's.
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
	// The Kudu deployment object OneDeploy status proxies; the swagger declares no
	// schema.
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

// MSDeploy exists on sites, slots and their instances; OneDeploy on production
// sites only, as the specification declares.
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
				// MSDeploy refuses a second deployment while one runs.
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

	both("GET", "/extensions/MSDeploy", msDeployGet(""))
	both("PUT", "/extensions/MSDeploy", msDeployPut(""))
	both("GET", "/extensions/MSDeploy/log", msDeployLog(""))
	both("GET", "/instances/{instanceId}/extensions/MSDeploy", msDeployGet("instance"))
	both("PUT", "/instances/{instanceId}/extensions/MSDeploy", msDeployPut("instance"))
	both("GET", "/instances/{instanceId}/extensions/MSDeploy/log", msDeployLog("instance"))

	// Production site only, per the specification.
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
		// Declared long-running: while building, 202 with Azure-AsyncOperation and this
		// URL as Location; terminal states answer 200.
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

	// The rotation shows through POST /config/publishingcredentials/list, which
	// reads the same counter.
	both("POST", "/newpassword", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		azureBumpKeyGen(webResourceID(r), "publishingPassword", "")
		w.WriteHeader(http.StatusOK)
	})
}

// "" for the site/slot extension, the instance path for the per-instance one.
func idSuffixExpand(r *http.Request, kind string) string {
	if kind == "instance" {
		return "/instances/" + sim.PathParam(r, "instanceId")
	}
	return ""
}

// The site's region when the record exists, Azure's default US region
// otherwise.
func webSiteOperationLocation(resID string) string {
	if site, ok := webJobSite(resID); ok && site.Location != "" {
		return strings.ToLower(strings.ReplaceAll(site.Location, " ", ""))
	}
	return "eastus"
}

func registerWebPublishingGlobals(srv *sim.Server) {
	srv.HandleFunc("GET /providers/Microsoft.Web/publishingUsers/web", func(w http.ResponseWriter, r *http.Request) {
		row, _ := webPublishingUser.Get("web")
		sim.WriteJSON(w, http.StatusOK, publishingUserWire(row))
	})
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

	// The provider catalog merged with any stored tokens.
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
	srv.HandleFunc("GET /providers/Microsoft.Web/sourcecontrols/{sourceControlType}", func(w http.ResponseWriter, r *http.Request) {
		name := sim.PathParam(r, "sourceControlType")
		row, ok := webProviderSourceControls.Get(name)
		if !ok {
			row = WebProviderSourceControlRow{Name: name}
		}
		sim.WriteJSON(w, http.StatusOK, providerSourceControlWire(row))
	})
	// The token is echoed, as Azure echoes the SourceControl written.
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
