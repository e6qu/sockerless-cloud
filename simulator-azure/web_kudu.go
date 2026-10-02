package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// web_kudu.go serves the deployment API of a web app's SCM site — Kudu, on
// the Repository hostname the site reports in hostNameSslStates: zip deploy
// (/api/zipdeploy), OneDeploy (/api/publish) and the deployment records both
// write (/api/deployments, and the legacy /deployments the warmup probe
// reads). Requests authenticate with the site's publishing credentials (or
// the subscription's deployment user) as HTTP basic auth while the scm basic
// publishing credentials policy allows it, or with a Microsoft Entra bearer
// token. A deployment lands its artifact through the same placement the Azure
// Resource Manager OneDeploy and MSDeploy operations use, records a Kudu
// deployment (which Azure Resource Manager's /deployments proxies) and a
// deploymentStatus, and the site restarts on the new content.

// Kudu's DeployStatus values.
const (
	kuduStatusPending  = 0
	kuduStatusBuilding = 1
	kuduStatusFailed   = 3
	kuduStatusSuccess  = 4
)

// Kudu log entry types.
const (
	kuduLogMessage = 0
	kuduLogError   = 2
)

// WebKuduDeployment is one deployment a site's Kudu performed. ID is
// "<resID>/deployments/<deploymentId>", the key Azure Resource Manager's
// deployment record shares.
type WebKuduDeployment struct {
	ID           string            `json:"id"`
	SiteID       string            `json:"siteId"`
	DeploymentID string            `json:"deploymentId"`
	Status       int               `json:"status"`
	StatusText   string            `json:"statusText"`
	Author       string            `json:"author"`
	AuthorEmail  string            `json:"authorEmail"`
	Deployer     string            `json:"deployer"`
	Message      string            `json:"message"`
	Progress     string            `json:"progress"`
	ReceivedTime string            `json:"receivedTime"`
	StartTime    string            `json:"startTime"`
	EndTime      string            `json:"endTime,omitempty"`
	Complete     bool              `json:"complete"`
	Active       bool              `json:"active"`
	Log          []WebKuduLogEntry `json:"log,omitempty"`
}

// WebKuduLogEntry is one line of a Kudu deployment's log.
type WebKuduLogEntry struct {
	ID      string `json:"id"`
	LogTime string `json:"logTime"`
	Message string `json:"message"`
	Type    int    `json:"type"`
}

var webKuduDeployments sim.Store[WebKuduDeployment]

// kuduDeployLocks holds the site-wide deployment lock Kudu takes for the
// fetch-and-deploy phase of a deployment; a second deployment arriving
// meanwhile is refused with 409.
var kuduDeployLocks = struct {
	sync.Mutex
	held map[string]bool
}{held: map[string]bool{}}

func kuduTryLock(resID string) bool {
	kuduDeployLocks.Lock()
	defer kuduDeployLocks.Unlock()
	if kuduDeployLocks.held[resID] {
		return false
	}
	kuduDeployLocks.held[resID] = true
	return true
}

func kuduUnlock(resID string) {
	kuduDeployLocks.Lock()
	defer kuduDeployLocks.Unlock()
	delete(kuduDeployLocks.held, resID)
}

// kuduTime is the timestamp format Kudu writes.
func kuduTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.0000000Z")
}

// initWebKuduStore wires the Kudu deployment store. A deployment persisted
// mid-flight lost its goroutine with the previous process, so it reads back
// failed, as Kudu reports a deployment its worker died during.
func initWebKuduStore(srv *sim.Server) {
	webKuduDeployments = sim.MakeStore[WebKuduDeployment](srv.DB(), "web_kudu_deployments")
	for _, rec := range webKuduDeployments.List() {
		if rec.Complete {
			continue
		}
		rec.Status = kuduStatusFailed
		rec.StatusText = "Failed"
		rec.Complete = true
		rec.EndTime = kuduTime(time.Now())
		rec.Log = append(rec.Log, WebKuduLogEntry{
			ID: sim.NewUUID(), LogTime: rec.EndTime, Type: kuduLogError,
			Message: "The deployment was interrupted by a service restart before it completed.",
		})
		kuduSaveDeployment(rec)
	}
}

// kuduSaveDeployment stores a Kudu deployment and the Azure Resource Manager
// deployment record that proxies it.
func kuduSaveDeployment(rec WebKuduDeployment) {
	webKuduDeployments.Put(rec.ID, rec)
	typ := "Microsoft.Web/sites/deployments"
	if strings.Contains(rec.SiteID, "/slots/") {
		typ = "Microsoft.Web/sites/slots/deployments"
	}
	webDeployments.Put(rec.ID, WebDeployment{
		ID:   rec.ID,
		Name: rec.DeploymentID,
		Type: typ,
		Properties: WebDeploymentProperties{
			Status:      rec.Status,
			Active:      rec.Active,
			Author:      rec.Author,
			AuthorEmail: rec.AuthorEmail,
			Deployer:    rec.Deployer,
			Message:     rec.Message,
			StartTime:   rec.StartTime,
			EndTime:     rec.EndTime,
		},
	})
}

// kuduUpdateDeployment applies fn to a stored deployment and saves it.
func kuduUpdateDeployment(id string, fn func(*WebKuduDeployment)) {
	rec, ok := webKuduDeployments.Get(id)
	if !ok {
		return
	}
	fn(&rec)
	kuduSaveDeployment(rec)
}

func (rec *WebKuduDeployment) logf(typ int, format string, args ...any) {
	rec.Log = append(rec.Log, WebKuduLogEntry{
		ID: sim.NewUUID(), LogTime: kuduTime(time.Now()), Type: typ, Message: fmt.Sprintf(format, args...),
	})
}

// webCleanupKudu removes the Kudu deployments of a deleted site or slot.
func webCleanupKudu(resID string) {
	prefix := resID + "/deployments/"
	for _, rec := range webKuduDeployments.Filter(func(rec WebKuduDeployment) bool { return strings.HasPrefix(rec.ID, prefix) }) {
		webKuduDeployments.Delete(rec.ID)
	}
}

// siteScmHost is the SCM hostname a site reports: its Repository
// hostNameSslStates entry.
func siteScmHost(site *Site) string {
	for _, s := range site.Properties.HostNameSslStates {
		if s.HostType == "Repository" {
			return s.Name
		}
	}
	return ""
}

// siteScmHostKeys are the hostnames a site's SCM site answers on: the one it
// advertises, and the platform's own <label>.scm.azurewebsites.net.
func siteScmHostKeys(s Site) []string {
	var keys []string
	add := func(h string) {
		if hn, _, err := net.SplitHostPort(h); err == nil {
			h = hn
		}
		if h = strings.ToLower(strings.TrimSuffix(h, ".")); h != "" {
			keys = append(keys, h)
		}
	}
	add(siteScmHost(&s))
	if label, ok := strings.CutSuffix(strings.ToLower(s.Properties.DefaultHostName), ".azurewebsites.net"); ok {
		add(label + ".scm.azurewebsites.net")
	}
	return keys
}

var (
	appServiceSitesByScmHost sim.GenerationIndex[Site]
	appServiceSlotsByScmHost sim.GenerationIndex[Site]
)

// appServiceSiteByScmHost returns the site or slot whose SCM site a request's
// Host header addresses.
func appServiceSiteByScmHost(host string) (Site, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || !strings.Contains(host, ".scm.") {
		return Site{}, false
	}
	if azfSites != nil {
		if site, ok := appServiceSitesByScmHost.Lookup(azfSites, host, siteScmHostKeys); ok {
			return site, true
		}
	}
	if webSlots != nil {
		return appServiceSlotsByScmHost.Lookup(webSlots, host, siteScmHostKeys)
	}
	return Site{}, false
}

// registerAppServiceKudu mounts the SCM sites: a request whose Host is a
// site's SCM hostname goes to that site's Kudu.
func registerAppServiceKudu(srv *sim.Server) {
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			site, ok := appServiceSiteByScmHost(r.Host)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			serveKudu(w, r, &site)
		})
	})
}

func kuduError(w http.ResponseWriter, status int, format string, args ...any) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, format, args...)
}

func serveKudu(w http.ResponseWriter, r *http.Request, site *Site) {
	if !kuduAuthorize(w, r, site) {
		return
	}
	p := strings.TrimSuffix(strings.ToLower(r.URL.Path), "/")
	if rest, ok := strings.CutPrefix(p, "/api"); ok && strings.HasPrefix(rest, "/deployments") {
		p = rest
	}
	switch {
	case p == "/deployments":
		if !kuduMethod(w, r, http.MethodGet) {
			return
		}
		kuduListDeployments(w, r, site)
	case strings.HasPrefix(p, "/deployments/"):
		if !kuduMethod(w, r, http.MethodGet) {
			return
		}
		kuduGetDeployment(w, r, site, strings.TrimPrefix(p, "/deployments/"))
	case p == "/api/zipdeploy":
		if !kuduMethod(w, r, http.MethodPost, http.MethodPut) {
			return
		}
		kuduZipDeploy(w, r, site)
	case p == "/api/publish":
		if !kuduMethod(w, r, http.MethodPost, http.MethodPut) {
			return
		}
		kuduOneDeploy(w, r, site)
	default:
		kuduError(w, http.StatusNotImplemented,
			"App Service SCM site: %s %s is not implemented by the simulator, which serves the Kudu deployment API (/api/zipdeploy, /api/publish, /api/deployments)",
			r.Method, r.URL.Path)
	}
}

func kuduMethod(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, m := range methods {
		if r.Method == m {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	kuduError(w, http.StatusMethodNotAllowed, "The requested resource does not support http method '%s'.", r.Method)
	return false
}

// kuduAppServiceAudiences are the Microsoft Entra audiences of the App Service
// resource the Azure CLI requests a Kudu token for.
var kuduAppServiceAudiences = map[string]bool{
	"https://appservice.azure.com":  true,
	"https://appservice.azure.com/": true,
}

// kuduAuthorize admits a request carrying the site's publishing credentials
// or the deployment user's as basic auth — while the site's scm basic
// publishing credentials policy allows basic auth — or a Microsoft Entra
// bearer token for Azure Resource Manager or App Service.
func kuduAuthorize(w http.ResponseWriter, r *http.Request, site *Site) bool {
	auth := r.Header.Get("Authorization")
	if token, ok := strings.CutPrefix(auth, "Bearer "); ok {
		claims, err := verifyAzureSimJWT(strings.TrimSpace(token))
		if err == nil {
			aud := azureTokenAudience(claims)
			if armTokenAudiences[aud] || kuduAppServiceAudiences[aud] {
				return true
			}
		}
	} else if user, password, ok := r.BasicAuth(); ok && webBasicPublishingAllowed(site.ID, "scm") {
		if kuduCredentialsMatch(site, user, password) {
			return true
		}
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="site"`)
	kuduError(w, http.StatusUnauthorized, "401 - Unauthorized: Access is denied due to invalid credentials.")
	return false
}

func kuduCredentialsMatch(site *Site, user, password string) bool {
	equal := func(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
	siteUser := webPublishingUserName(site)
	label := strings.TrimPrefix(siteUser, "$")
	if (user == siteUser || user == label+`\`+siteUser) && equal(password, webPublishingPassword(site.ID)) {
		return true
	}
	if row, ok := webPublishingUser.Get("web"); ok && row.PublishingUserName != "" && row.PublishingPassword != "" {
		if (user == row.PublishingUserName || user == label+`\`+row.PublishingUserName) &&
			equal(password, row.PublishingPassword) {
			return true
		}
	}
	return false
}

// webPublishingUserName is a site's publishing user: "$<app>" for an app and
// "$<app>__<slot>" for a slot.
func webPublishingUserName(site *Site) string {
	return "$" + strings.Replace(site.Name, "/", "__", 1)
}

func kuduDeploymentURL(r *http.Request, id string) string {
	return azureRequestScheme(r) + "://" + r.Host + "/api/deployments/" + id
}

func kuduDeploymentWire(r *http.Request, site *Site, rec WebKuduDeployment) map[string]any {
	out := map[string]any{
		"id":                    rec.DeploymentID,
		"status":                rec.Status,
		"status_text":           rec.StatusText,
		"author_email":          rec.AuthorEmail,
		"author":                rec.Author,
		"deployer":              rec.Deployer,
		"message":               rec.Message,
		"progress":              rec.Progress,
		"received_time":         rec.ReceivedTime,
		"start_time":            rec.StartTime,
		"end_time":              nil,
		"last_success_end_time": nil,
		"complete":              rec.Complete,
		"active":                rec.Active,
		"is_temp":               false,
		"is_readonly":           true,
		"url":                   kuduDeploymentURL(r, rec.DeploymentID),
		"log_url":               kuduDeploymentURL(r, rec.DeploymentID) + "/log",
		"site_name":             strings.SplitN(site.Name, "/", 2)[0],
	}
	if rec.EndTime != "" {
		out["end_time"] = rec.EndTime
		if rec.Status == kuduStatusSuccess {
			out["last_success_end_time"] = rec.EndTime
		}
	}
	return out
}

var webKuduDeploymentsBySite sim.GenerationIndex[WebKuduDeployment]

// kuduSiteDeployments returns a site's deployments, newest first.
func kuduSiteDeployments(siteID string) []WebKuduDeployment {
	recs := slices.Clone(webKuduDeploymentsBySite.LookupAll(webKuduDeployments, siteID,
		func(rec WebKuduDeployment) []string { return []string{rec.SiteID} }))
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].ReceivedTime > recs[j].ReceivedTime })
	return recs
}

func kuduListDeployments(w http.ResponseWriter, r *http.Request, site *Site) {
	out := []any{}
	for _, rec := range kuduSiteDeployments(site.ID) {
		out = append(out, kuduDeploymentWire(r, site, rec))
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func kuduGetDeployment(w http.ResponseWriter, r *http.Request, site *Site, rest string) {
	id, sub, _ := strings.Cut(rest, "/")
	var rec WebKuduDeployment
	found := false
	if id == "latest" {
		if recs := kuduSiteDeployments(site.ID); len(recs) > 0 {
			rec, found = recs[0], true
		}
	} else {
		rec, found = webKuduDeployments.Get(site.ID + "/deployments/" + id)
	}
	if !found {
		kuduError(w, http.StatusNotFound, "Deployment '%s' not found.", id)
		return
	}
	switch sub {
	case "":
		sim.WriteJSON(w, http.StatusOK, kuduDeploymentWire(r, site, rec))
	case "log":
		out := make([]any, 0, len(rec.Log))
		for _, e := range rec.Log {
			out = append(out, map[string]any{
				"log_time":    e.LogTime,
				"id":          e.ID,
				"message":     e.Message,
				"type":        e.Type,
				"details_url": nil,
			})
		}
		sim.WriteJSON(w, http.StatusOK, out)
	default:
		kuduError(w, http.StatusNotFound, "No HTTP resource was found that matches the request URI '%s'.", r.URL.Path)
	}
}

// kuduQuery reads a query parameter case-insensitively, as Kudu's ASP.NET
// binding does.
func kuduQuery(r *http.Request, name string) string {
	for k, v := range r.URL.Query() {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// kuduArtifactSource is the deployment's artifact: the request body, or the
// package a JSON body names by packageUri, which the deployment fetches.
type kuduArtifactSource struct {
	data       []byte
	packageURI string
}

func kuduReadArtifact(w http.ResponseWriter, r *http.Request) (kuduArtifactSource, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, webDeployPackageLimit+1))
	if err != nil {
		kuduError(w, http.StatusBadRequest, "Failed to read the request body: %v", err)
		return kuduArtifactSource{}, false
	}
	if len(body) > webDeployPackageLimit {
		kuduError(w, http.StatusRequestEntityTooLarge, "The artifact exceeds %d bytes.", webDeployPackageLimit)
		return kuduArtifactSource{}, false
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && mt == "application/json" {
		var req struct {
			PackageURI string `json:"packageUri"`
		}
		if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.PackageURI) == "" {
			kuduError(w, http.StatusBadRequest, "A JSON deployment request must name a packageUri.")
			return kuduArtifactSource{}, false
		}
		return kuduArtifactSource{packageURI: req.PackageURI}, true
	}
	if len(body) == 0 {
		kuduError(w, http.StatusBadRequest, "The request carries no artifact.")
		return kuduArtifactSource{}, false
	}
	return kuduArtifactSource{data: body}, true
}

func kuduZipDeploy(w http.ResponseWriter, r *http.Request, site *Site) {
	src, ok := kuduReadArtifact(w, r)
	if !ok {
		return
	}
	deployer := kuduQuery(r, "deployer")
	if deployer == "" {
		deployer = "ZipDeploy"
	}
	kuduDeploy(w, r, site, src, webArtifact{
		Type: "zip", Target: webWWWRoot, Restart: true, SyncManifest: true,
	}, deployer, strings.EqualFold(kuduQuery(r, "isAsync"), "true"))
}

func kuduOneDeploy(w http.ResponseWriter, r *http.Request, site *Site) {
	artifact, err := webOneDeployArtifact(site, kuduQuery(r, "type"), kuduQuery(r, "path"),
		kuduQuery(r, "clean"), kuduQuery(r, "restart"))
	if err != nil {
		kuduError(w, http.StatusBadRequest, "%v", err)
		return
	}
	src, ok := kuduReadArtifact(w, r)
	if !ok {
		return
	}
	kuduDeploy(w, r, site, src, artifact, "OneDeploy", strings.EqualFold(kuduQuery(r, "async"), "true"))
}

// kuduDeploy records a deployment and runs it: synchronously, answering 200
// once the artifact is deployed, or in the background, answering 202 with the
// deployment's status URL as Location.
func kuduDeploy(w http.ResponseWriter, r *http.Request, site *Site, src kuduArtifactSource, artifact webArtifact, deployer string, async bool) {
	if !kuduTryLock(site.ID) {
		kuduError(w, http.StatusConflict, "There is a deployment currently in progress. Please try again when it completes.")
		return
	}
	now := time.Now()
	deploymentID := sim.NewUUID()
	author := kuduQuery(r, "author")
	if author == "" {
		author = "N/A"
	}
	message := kuduQuery(r, "message")
	if message == "" {
		message = "Created via a push deployment"
	}
	rec := WebKuduDeployment{
		ID:           site.ID + "/deployments/" + deploymentID,
		SiteID:       site.ID,
		DeploymentID: deploymentID,
		Status:       kuduStatusPending,
		StatusText:   "Receiving changes.",
		Author:       author,
		AuthorEmail:  "N/A",
		Deployer:     deployer,
		Message:      message,
		ReceivedTime: kuduTime(now),
		StartTime:    kuduTime(now),
	}
	rec.logf(kuduLogMessage, "Received %s deployment request.", deployer)
	kuduSaveDeployment(rec)
	statusID := site.ID + "/deploymentStatus/" + deploymentID
	webDeploymentStatuses.Put(statusID, WebDeploymentStatusRecord{
		ID:           statusID,
		SiteID:       site.ID,
		DeploymentID: deploymentID,
		Status:       "BuildRequestReceived",
		InProgress:   1,
	})

	run := func() error {
		defer kuduUnlock(site.ID)
		return kuduRunDeployment(rec.ID, statusID, site.ID, src, artifact)
	}
	if async {
		go func() { _ = run() }()
		w.Header().Set("Location", fmt.Sprintf("%s://%s/api/deployments/latest?deployer=%s&time=%s",
			azureRequestScheme(r), r.Host, url.QueryEscape(deployer), url.QueryEscape(now.UTC().Format("2006-01-02_15-04-05Z"))))
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if err := run(); err != nil {
		kuduError(w, http.StatusBadRequest, "%v", err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// kuduRunDeployment fetches the artifact when it is named by URL, lands it,
// and settles the Kudu deployment. A deployment that landed then tracks the
// site's restart in the deploymentStatus record: RuntimeSuccessful once the
// site answers the platform's warmup request, RuntimeFailed when it does not
// start.
func kuduRunDeployment(recID, statusID, resID string, src kuduArtifactSource, artifact webArtifact) error {
	kuduUpdateDeployment(recID, func(rec *WebKuduDeployment) {
		rec.Status = kuduStatusBuilding
		rec.StatusText = "Building and Deploying"
		rec.Progress = "Deploying the artifact."
	})
	webDeploymentStatuses.Update(statusID, func(row *WebDeploymentStatusRecord) { row.Status = "BuildInProgress" })

	data := src.data
	var err error
	if src.packageURI != "" {
		kuduUpdateDeployment(recID, func(rec *WebKuduDeployment) {
			rec.logf(kuduLogMessage, "Fetching the package from %s.", src.packageURI)
		})
		data, err = webFetchPackage(src.packageURI)
	}
	written := 0
	if err == nil {
		written, err = webDeployArtifact(resID, data, artifact)
	}
	end := kuduTime(time.Now())
	if err != nil {
		kuduUpdateDeployment(recID, func(rec *WebKuduDeployment) {
			rec.Status = kuduStatusFailed
			rec.StatusText = "Failed"
			rec.Progress = ""
			rec.Complete = true
			rec.EndTime = end
			rec.logf(kuduLogError, "Deployment failed: %v", err)
		})
		webDeploymentStatuses.Update(statusID, func(row *WebDeploymentStatusRecord) {
			row.Status = "BuildFailed"
			row.InProgress = 0
			row.Failed = 1
			row.Errors = append(row.Errors, err.Error())
		})
		return err
	}
	for _, other := range kuduSiteDeployments(resID) {
		if other.Active && other.ID != recID {
			kuduUpdateDeployment(other.ID, func(rec *WebKuduDeployment) { rec.Active = false })
		}
	}
	kuduUpdateDeployment(recID, func(rec *WebKuduDeployment) {
		rec.Status = kuduStatusSuccess
		rec.StatusText = ""
		rec.Progress = ""
		rec.Complete = true
		rec.Active = true
		rec.EndTime = end
		rec.logf(kuduLogMessage, "Deployed %d file(s) of the %s artifact to %s.", written, artifact.Type, artifact.Target)
		rec.logf(kuduLogMessage, "Deployment successful.")
	})
	webDeploymentStatuses.Update(statusID, func(row *WebDeploymentStatusRecord) { row.Status = "RuntimeStarting" })
	go kuduTrackRuntime(statusID, resID)
	return nil
}

// kuduTrackRuntime settles a deployment's runtime status on the site's start.
func kuduTrackRuntime(statusID, resID string) {
	fail := func(msg string) {
		webDeploymentStatuses.Update(statusID, func(row *WebDeploymentStatusRecord) {
			row.Status = "RuntimeFailed"
			row.InProgress = 0
			row.Failed = 1
			row.Errors = append(row.Errors, msg)
		})
	}
	site, ok := azfSites.Get(resID)
	if !ok {
		fail("The simulator runs no instance of a deployment slot.")
		return
	}
	if !siteRunsContainer(&site) {
		fail(siteImageMissing(&site).Error())
		return
	}
	if _, err := siteContainerAddress(context.Background(), &site); err != nil {
		fail(err.Error())
		return
	}
	webDeploymentStatuses.Update(statusID, func(row *WebDeploymentStatusRecord) {
		row.Status = "RuntimeSuccessful"
		row.InProgress = 0
		row.Successful = 1
	})
}
