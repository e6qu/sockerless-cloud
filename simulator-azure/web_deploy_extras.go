package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_deploy_extras.go implements the Microsoft.Web deployment surface beyond
// the /deployments publishing log: the MSDeploy site extension (site, slot and
// per-instance variants, as Azure-AsyncOperation long-running operations),
// OneDeploy, the deploymentStatus read model, repository sync, publishing
// password rotation, and the provider-global publishing user and source
// control token catalogs.
//
// Deployments do real work: the package the request's packageUri names is
// fetched over HTTP, unpacked, and persisted durably as the site's deployed
// content (webSiteContent). Webjobs are discovered from that content
// (web_webjobs.go), so the deploy → discover → run chain is the same one the
// real platform executes.

// WebSiteContentFile is one durably persisted deployed file of a site or
// slot. ID is "<resID>|<path>"; Path is wwwroot-relative with forward
// slashes.
type WebSiteContentFile struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
	Data []byte `json:"data"`
}

var webSiteContent sim.Store[WebSiteContentFile]

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
	webSiteContent = sim.MakeStore[WebSiteContentFile](srv.DB(), "web_site_content")
	webMSDeployOps = sim.MakeStore[WebMSDeployRecord](srv.DB(), "web_msdeploy_ops")
	webOneDeployOps = sim.MakeStore[WebOneDeployRecord](srv.DB(), "web_onedeploy_ops")
	webDeploymentStatuses = sim.MakeStore[WebDeploymentStatusRecord](srv.DB(), "web_deployment_statuses")
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
	filePrefix := resID + "|"
	for _, f := range webSiteContent.Filter(func(f WebSiteContentFile) bool { return strings.HasPrefix(f.ID, filePrefix) }) {
		webSiteContent.Delete(f.ID)
	}
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

// --- Package application -------------------------------------------------

// webDeployPackageLimit bounds a fetched deployment package.
const webDeployPackageLimit = 256 << 20 // 256 MiB

// webApplyDeploymentPackage fetches the package at packageURI, unpacks the
// zip, and persists every file as the site's deployed content, then
// rediscovers the site's webjobs. Returns the number of files written.
func webApplyDeploymentPackage(resID, packageURI string) (int, error) {
	if packageURI == "" {
		return 0, fmt.Errorf("packageUri is required")
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(packageURI)
	if err != nil {
		return 0, fmt.Errorf("fetch package: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("fetch package: %s returned %d", packageURI, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, webDeployPackageLimit+1))
	if err != nil {
		return 0, fmt.Errorf("read package: %w", err)
	}
	if len(data) > webDeployPackageLimit {
		return 0, fmt.Errorf("package exceeds %d bytes", webDeployPackageLimit)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0, fmt.Errorf("open package zip: %w", err)
	}
	written := 0
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		clean := path.Clean(strings.TrimPrefix(zf.Name, "/"))
		if clean == "." || strings.HasPrefix(clean, "../") {
			return written, fmt.Errorf("package entry %q escapes the site root", zf.Name)
		}
		rc, err := zf.Open()
		if err != nil {
			return written, fmt.Errorf("open package entry %q: %w", zf.Name, err)
		}
		content, err := io.ReadAll(io.LimitReader(rc, webDeployPackageLimit))
		_ = rc.Close()
		if err != nil {
			return written, fmt.Errorf("read package entry %q: %w", zf.Name, err)
		}
		webSiteContent.Put(resID+"|"+clean, WebSiteContentFile{
			ID:   resID + "|" + clean,
			Path: clean,
			Mode: uint32(zf.Mode().Perm()),
			Data: content,
		})
		written++
	}
	webDiscoverWebJobs(resID)
	// The app's content just changed, so the platform's automatic-backup
	// snapshot of this app state exists from here on.
	webCaptureAppSnapshot(resID)
	return written, nil
}

// --- Wire shapes -----------------------------------------------------------

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

// --- Handlers ----------------------------------------------------------------

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
				sim.AzureErrorf(w, "NotFound", http.StatusNotFound,
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
				sim.AzureErrorf(w, "NotFound", http.StatusNotFound,
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
				sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
				return
			}
			if req.Properties.PackageURI == "" {
				sim.AzureError(w, "InvalidRequestContent", "The MSDeploy request must name a packageUri.", http.StatusBadRequest)
				return
			}
			resID := webResourceID(r)
			recID := resID + idSuffixExpand(r, idSuffix) + "/extensions/MSDeploy"
			if existing, ok := webMSDeployOps.Get(recID); ok &&
				(existing.ProvisioningState == "accepted" || existing.ProvisioningState == "running") {
				// Real MSDeploy refuses a second deployment while one runs.
				sim.AzureError(w, "Conflict", "Another MSDeploy operation is in progress.", http.StatusConflict)
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

			deployStatusID := generateUUID()
			statusRecID := resID + "/deploymentStatus/" + deployStatusID
			webDeploymentStatuses.Put(statusRecID, WebDeploymentStatusRecord{
				ID:           statusRecID,
				SiteID:       resID,
				DeploymentID: deployStatusID,
				Status:       "BuildRequestReceived",
				InProgress:   1,
			})

			opID := issueAzureAsyncOperationOutcome(func() *AsyncOperationError {
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
						row.Status = "RuntimeFailed"
						row.InProgress = 0
						row.Failed = 1
						row.Errors = append(row.Errors, err.Error())
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
				webDeploymentStatuses.Update(statusRecID, func(row *WebDeploymentStatusRecord) {
					row.Status = "RuntimeSuccessful"
					row.InProgress = 0
					row.Successful = 1
				})
				return nil
			})
			webDeploymentStatuses.Update(statusRecID, func(row *WebDeploymentStatusRecord) { row.OpID = opID })

			opURL := azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"),
				"Microsoft.Web", webSiteOperationLocation(resID), "operationStatuses", opID, r.URL.Query().Get("api-version"))
			writeAzureAsyncCreateHeaders(w, opURL, azureCurrentRequestURL(r))
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
			sim.AzureErrorf(w, "NotFound", http.StatusNotFound,
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
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.PackageURI == "" {
			sim.AzureError(w, "InvalidRequestContent", "The OneDeploy request must name a packageUri.", http.StatusBadRequest)
			return
		}
		resID := webResourceID(r)
		recID := resID + "/extensions/onedeploy"
		start := time.Now().UTC()
		deploymentID := generateUUID()
		written, err := webApplyDeploymentPackage(resID, req.Properties.PackageURI)
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
			status.Status = "RuntimeFailed"
			status.Failed = 1
			status.Errors = []string{err.Error()}
			webOneDeployOps.Put(recID, rec)
			webDeploymentStatuses.Put(statusRecID, status)
			sim.AzureError(w, "DeploymentFailed", err.Error(), http.StatusBadRequest)
			return
		}
		rec.Status = 4
		rec.StatusText = "Success"
		rec.Message = fmt.Sprintf("OneDeploy succeeded: %d file(s) deployed.", written)
		status.Status = "RuntimeSuccessful"
		status.Successful = 1
		webOneDeployOps.Put(recID, rec)
		webDeploymentStatuses.Put(statusRecID, status)
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
			sim.AzureErrorf(w, "NotFound", http.StatusNotFound,
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
			w.Header().Set("Retry-After", "1")
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
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.PublishingUserName == "" {
			sim.AzureError(w, "InvalidRequestContent", "The publishingUserName property is required.", http.StatusBadRequest)
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
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		row := req.Properties
		row.Name = sim.PathParam(r, "sourceControlType")
		webProviderSourceControls.Put(row.Name, row)
		sim.WriteJSON(w, http.StatusOK, providerSourceControlWire(row))
	})
}
