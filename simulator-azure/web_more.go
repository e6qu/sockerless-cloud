package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Microsoft.Web slice beyond functions.go's core: deployment slots,
// deployments, host-name bindings, source control, site extensions, config
// sections, lifecycle actions, App Service plan list, patch and usages, the
// subscription-global catalogs, and Static Web Apps. Slots live in their own
// store, and each slot operation shares its site counterpart's handler, keyed
// by a resource ID carrying /slots/{slot}.

// A slot has the Site schema but its own store, so WebApps_List and
// ListByResourceGroup never enumerate slots.
var webSlots sim.Store[Site]

// A publishing-activity record under a site or slot.
type WebDeployment struct {
	ID         string                  `json:"id,omitempty"`
	Name       string                  `json:"name,omitempty"`
	Type       string                  `json:"type,omitempty"`
	Properties WebDeploymentProperties `json:"properties"`
}

// Field names are the spec's snake_case wire spelling.
type WebDeploymentProperties struct {
	Status      int    `json:"status"`
	Active      bool   `json:"active"`
	Author      string `json:"author,omitempty"`
	AuthorEmail string `json:"author_email,omitempty"`
	Deployer    string `json:"deployer,omitempty"`
	Details     string `json:"details,omitempty"`
	Message     string `json:"message,omitempty"`
	StartTime   string `json:"start_time,omitempty"`
	EndTime     string `json:"end_time,omitempty"`
}

var webDeployments sim.Store[WebDeployment]

type WebHostNameBinding struct {
	ID         string                       `json:"id,omitempty"`
	Name       string                       `json:"name,omitempty"`
	Type       string                       `json:"type,omitempty"`
	Properties WebHostNameBindingProperties `json:"properties"`
}

type WebHostNameBindingProperties struct {
	SiteName                    string `json:"siteName,omitempty"`
	HostNameType                string `json:"hostNameType,omitempty"`
	SSLState                    string `json:"sslState,omitempty"`
	AzureResourceName           string `json:"azureResourceName,omitempty"`
	AzureResourceType           string `json:"azureResourceType,omitempty"`
	CustomHostNameDNSRecordType string `json:"customHostNameDnsRecordType,omitempty"`
	DomainID                    string `json:"domainId,omitempty"`
	Thumbprint                  string `json:"thumbprint,omitempty"`
	VirtualIP                   string `json:"virtualIP,omitempty"`
}

var webHostNameBindings sim.Store[WebHostNameBinding]

type WebSourceControl struct {
	ID         string                     `json:"id,omitempty"`
	Name       string                     `json:"name,omitempty"`
	Type       string                     `json:"type,omitempty"`
	Properties WebSourceControlProperties `json:"properties"`
}

type WebSourceControlProperties struct {
	RepoURL                   string `json:"repoUrl,omitempty"`
	Branch                    string `json:"branch,omitempty"`
	IsManualIntegration       *bool  `json:"isManualIntegration,omitempty"`
	IsGitHubAction            *bool  `json:"isGitHubAction,omitempty"`
	IsMercurial               *bool  `json:"isMercurial,omitempty"`
	DeploymentRollbackEnabled *bool  `json:"deploymentRollbackEnabled,omitempty"`
}

var webSourceControls sim.Store[WebSourceControl]

type WebSiteExtension struct {
	ID         string                     `json:"id,omitempty"`
	Name       string                     `json:"name,omitempty"`
	Type       string                     `json:"type,omitempty"`
	Properties WebSiteExtensionProperties `json:"properties"`
}

type WebSiteExtensionProperties struct {
	ExtensionID   string `json:"extension_id,omitempty"`
	Title         string `json:"title,omitempty"`
	ExtensionType string `json:"extension_type,omitempty"`
	Version       string `json:"version,omitempty"`
	Description   string `json:"description,omitempty"`
}

var webSiteExtensions sim.Store[WebSiteExtension]

// The config sections outside SiteConfig: metadata, diagnostic logs, and both
// Easy Auth shapes. Keyed by canonical site or slot ID. App settings and
// connection strings stay in siteConfigStore.
type webConfigExtra struct {
	Metadata       map[string]string `json:"metadata,omitempty"`
	Logs           map[string]any    `json:"logs,omitempty"`
	AuthSettings   map[string]any    `json:"authSettings,omitempty"`
	AuthSettingsV2 map[string]any    `json:"authSettingsV2,omitempty"`
}

var webConfigExtras sim.Store[webConfigExtra]

const webProvider = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web"

func registerWebMore(srv *sim.Server) {
	webSlots = sim.MakeStore[Site](srv.DB(), "web_slots")
	webDeployments = sim.MakeStore[WebDeployment](srv.DB(), "web_deployments")
	webHostNameBindings = sim.MakeStore[WebHostNameBinding](srv.DB(), "web_hostname_bindings")
	webSourceControls = sim.MakeStore[WebSourceControl](srv.DB(), "web_source_controls")
	webSiteExtensions = sim.MakeStore[WebSiteExtension](srv.DB(), "web_site_extensions")
	webConfigExtras = sim.MakeStore[webConfigExtra](srv.DB(), "web_config_extras")
	webSiteEvents = sim.MakeStore[WebSiteEvent](srv.DB(), "web_site_events")
	webHostKeys = sim.MakeStore[WebHostKeysRow](srv.DB(), "web_host_keys")
	webFunctionKeys = sim.MakeStore[WebFunctionKeysRow](srv.DB(), "web_function_keys")
	webBasicPublishingPolicies = sim.MakeStore[WebBasicPublishingPolicy](srv.DB(), "web_basic_publishing_policies")
	initWebDeployStores(srv)
	initWebSlotPreviewStore(srv)
	initWebJobStores(srv)
	initWebBackupStores(srv)

	registerWebCertificates(srv)
	registerWebSiteAndSlotHandlers(srv)
	registerWebSlotCRUD(srv)
	registerAppServicePlanMore(srv)
	registerWebGlobal(srv)
	registerWebPublishingGlobals(srv)
	registerWebStaticSites(srv)
	registerWebChildResources(srv)
	registerWebConfigReferences(srv)
	registerWebWorkflows(srv)
}

// Without this a site recreated under the same name would inherit the deleted
// site's children. A production-site delete removes its slots' subtrees too,
// as Azure does.
func webCleanupSiteResources(resID string) {
	ids := []string{resID}
	slotPrefix := resID + "/slots/"
	for _, s := range webSlots.Filter(func(row Site) bool { return strings.HasPrefix(row.ID, slotPrefix) }) {
		ids = append(ids, s.ID)
		// A slot deleted with its app is retained like a directly deleted one;
		// DeletedSiteProperties carries the slot name for this case.
		webRecordDeletedSite(s.ID, s)
		webSyncSiteIdentityPrincipal(&s, nil)
		webSlots.Delete(s.ID)
		stopAzureFunctionInstance(s.Name)
		cleanupSiteContainers(s.ID, s.Name)
	}
	for _, id := range ids {
		sub := id + "/"
		for _, d := range webDeployments.Filter(func(d WebDeployment) bool { return strings.HasPrefix(d.ID, sub) }) {
			webDeployments.Delete(d.ID)
		}
		for _, b := range webHostNameBindings.Filter(func(b WebHostNameBinding) bool { return strings.HasPrefix(b.ID, sub) }) {
			webHostNameBindings.Delete(b.ID)
		}
		for _, sc := range webSourceControls.Filter(func(sc WebSourceControl) bool { return strings.HasPrefix(sc.ID, sub) }) {
			webSourceControls.Delete(sc.ID)
		}
		for _, e := range webSiteExtensions.Filter(func(e WebSiteExtension) bool { return strings.HasPrefix(e.ID, sub) }) {
			webSiteExtensions.Delete(e.ID)
		}
		for _, c := range azfSiteContainers.Filter(func(c SiteContainer) bool { return strings.HasPrefix(c.ID, sub) }) {
			azfSiteContainers.Delete(c.ID)
		}
		for _, c := range webPublicCertificates.Filter(func(c WebPublicCertificate) bool { return strings.HasPrefix(c.ID, sub) }) {
			webPublicCertificates.Delete(c.ID)
		}
		for _, c := range webSiteCertificates.Filter(func(c WebCertificate) bool { return strings.HasPrefix(c.ID, sub) }) {
			webSiteCertificates.Delete(c.ID)
		}
		for _, s := range webConfigSnapshots.Filter(func(s webConfigSnapshotRow) bool { return strings.HasPrefix(s.ID, sub) }) {
			webConfigSnapshots.Delete(s.ID)
		}
		for _, d := range webDomainOwnershipIdentifiers.Filter(func(d WebDomainOwnershipIdentifier) bool { return strings.HasPrefix(d.ID, sub) }) {
			webDomainOwnershipIdentifiers.Delete(d.ID)
		}
		for _, a := range webPremierAddOns.Filter(func(a WebPremierAddOn) bool { return strings.HasPrefix(a.ID, sub) }) {
			webPremierAddOns.Delete(a.ID)
		}
		for _, p := range webPushSettings.Filter(func(p WebPushSettings) bool { return strings.HasPrefix(p.ID, sub) }) {
			webPushSettings.Delete(p.ID)
		}
		for _, c := range webVnetConnections.Filter(func(c WebVnetConnection) bool { return strings.HasPrefix(c.ID, sub) }) {
			webVnetConnections.Delete(c.ID)
		}
		for _, g := range webVnetGateways.Filter(func(g WebVnetGateway) bool { return strings.HasPrefix(g.ID, sub) }) {
			webVnetGateways.Delete(g.ID)
		}
		for _, hc := range webHybridConnections.Filter(func(hc WebHybridConnection) bool { return strings.HasPrefix(hc.ID, sub) }) {
			webHybridConnections.Delete(hc.ID)
		}
		for _, rc := range webRelayServiceConns.Filter(func(rc WebRelayServiceConnection) bool { return strings.HasPrefix(rc.ID, sub) }) {
			webRelayServiceConns.Delete(rc.ID)
		}
		for _, pec := range webSitePECs.Filter(func(pec WebSitePrivateEndpointConnection) bool { return strings.HasPrefix(pec.ID, sub) }) {
			webSitePECs.Delete(pec.ID)
		}
		webPrivateAccess.Delete(id)
		webCleanupFunctionKeys(id)
		webCleanupWebJobs(id)
		webCleanupDeployments(id)
		webConfigExtras.Delete(id)
		siteConfigStore.Delete(id)
		webWorkflowFiles.Delete(id)
		wfPrefix := id + webHostruntimeWorkflows + "/"
		for _, wf := range logicWorkflows.Filter(func(wf LogicWorkflow) bool { return strings.HasPrefix(wf.ID, wfPrefix) }) {
			webDeleteSiteWorkflow(wf.ID)
		}
	}
}

// Appends /slots/{slot} when present.
func webResourceID(r *http.Request) string {
	id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Web", "sites", sim.PathParam(r, "siteName"))
	if slot := sim.PathParam(r, "slot"); slot != "" {
		id += "/slots/" + slot
	}
	return id
}

func webResourceStore(r *http.Request) sim.Store[Site] {
	if sim.PathParam(r, "slot") != "" {
		return webSlots
	}
	return azfSites
}

func webResource(r *http.Request) (Site, bool) {
	return webResourceStore(r).Get(webResourceID(r))
}

// Returns true when it wrote the 404.
func webMissing(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := webResource(r); ok {
		return false
	}
	name := sim.PathParam(r, "siteName")
	if slot := sim.PathParam(r, "slot"); slot != "" {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Web/sites/%s/slots/%s' was not found.", name, slot)
		return true
	}
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource 'Microsoft.Web/sites/%s' was not found.", name)
	return true
}

// `both` registers at site and slot level, `slot` at slot level only
// (functions.go serves the site spelling), `site` at site level only.
func registerWebSiteAndSlotHandlers(srv *sim.Server) {
	both := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}"+suffix, h)
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}/slots/{slot}"+suffix, h)
	}
	slot := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}/slots/{slot}"+suffix, h)
	}
	site := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}"+suffix, h)
	}

	registerWebLifecycle(both)
	registerWebConfigSections(both, slot)
	registerWebDeployments(both)
	registerWebHostNameBindings(both)
	registerWebSourceControl(both)
	registerWebSiteExtensions(both)
	registerWebFunctionsRW(both, slot)
	registerWebBasicPubCreds(both, slot)
	registerWebFunctionKeyHandlers(both)
	registerWebJobHandlers(both)
	registerWebDeploymentExtras(both, site)
	registerWebSiteCertificates(both)
	registerWebHostnameTruth(srv, both)
	registerWebConfigSnapshots(srv, both)
	registerWebContainerLogs(both)
	registerWebProcesses(both)
	registerWebNetworkTraces(srv, both)
	registerWebPerfCounters(both, site)
	registerWebResourceHealth(srv, both)
	registerWebMigrateMySQL(srv, both, site)
	registerWebDiagnostics(both)
	registerWebBackups(both)
	registerWebProviderGlobal(srv, site)

	// Slot PATCH lives in registerWebSlotCRUD.
	site("PATCH", "", func(w http.ResponseWriter, r *http.Request) {
		patchWebSite(w, r, azfSites)
	})
}

// The {id,name,type,properties} envelope every config sub-resource shares.
func configResource(resID, name string, props any) map[string]any {
	return map[string]any{
		"id":         resID + "/config/" + name,
		"name":       name,
		"type":       "Microsoft.Web/sites/config",
		"properties": props,
	}
}

func registerWebConfigSections(both, slot func(string, string, http.HandlerFunc)) {
	// Only the SiteConfig "web" block persists.
	both("GET", "/config", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		row, _ := webResource(r)
		cfg := row.Properties.SiteConfig
		if cfg == nil {
			cfg = &SiteConfig{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": []any{configResource(webResourceID(r), "web", cfg)},
		})
	})

	// A StringDictionary: PUT stores, POST /list returns.
	both("PUT", "/config/metadata", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req struct {
			Properties map[string]string `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		extra, _ := webConfigExtras.Get(webResourceID(r))
		extra.Metadata = req.Properties
		webConfigExtras.Put(webResourceID(r), extra)
		sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "metadata", nonNilStrMap(req.Properties)))
	})
	both("POST", "/config/metadata/list", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		extra, _ := webConfigExtras.Get(webResourceID(r))
		sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "metadata", nonNilStrMap(extra.Metadata)))
	})

	// GET at slot level (functions.go serves the site's), PUT at both.
	logsDefault := func() map[string]any {
		return map[string]any{
			"applicationLogs":       map[string]any{"fileSystem": map[string]any{"level": "Off"}},
			"httpLogs":              map[string]any{},
			"detailedErrorMessages": map[string]any{"enabled": false},
			"failedRequestsTracing": map[string]any{"enabled": false},
		}
	}
	slot("GET", "/config/logs", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		extra, _ := webConfigExtras.Get(webResourceID(r))
		props := extra.Logs
		if props == nil {
			props = logsDefault()
		}
		sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "logs", props))
	})
	both("PUT", "/config/logs", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req struct {
			Properties map[string]any `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		extra, _ := webConfigExtras.Get(webResourceID(r))
		extra.Logs = req.Properties
		webConfigExtras.Put(webResourceID(r), extra)
		webForgetDockerLogUsage(webResourceID(r))
		webRotateSiteDockerLog(webResourceID(r), strings.ToLower(webResourceID(r)), 0, time.Now())
		props := req.Properties
		if props == nil {
			props = logsDefault()
		}
		sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "logs", props))
	})

	// Easy Auth v1: PUT at both, POST /list at slot level.
	both("PUT", "/config/authsettings", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req struct {
			Properties map[string]any `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		extra, _ := webConfigExtras.Get(webResourceID(r))
		extra.AuthSettings = req.Properties
		webConfigExtras.Put(webResourceID(r), extra)
		sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "authsettings", authSettingsOrDefault(req.Properties)))
	})
	slot("POST", "/config/authsettings/list", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		extra, _ := webConfigExtras.Get(webResourceID(r))
		sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "authsettings", authSettingsOrDefault(extra.AuthSettings)))
	})

	// Easy Auth v2: GET and PUT at both, GET /list at slot level (functions.go
	// serves the site's).
	authV2Default := func() map[string]any {
		return map[string]any{
			"platform":          map[string]any{"enabled": false},
			"globalValidation":  map[string]any{},
			"identityProviders": map[string]any{},
			"login":             map[string]any{},
			"httpSettings":      map[string]any{},
		}
	}
	authV2Get := func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		extra, _ := webConfigExtras.Get(webResourceID(r))
		props := extra.AuthSettingsV2
		if props == nil {
			props = authV2Default()
		}
		sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "authsettingsV2", props))
	}
	both("GET", "/config/authsettingsv2", authV2Get)
	slot("GET", "/config/authsettingsv2/list", authV2Get)
	both("PUT", "/config/authsettingsv2", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req struct {
			Properties map[string]any `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		extra, _ := webConfigExtras.Get(webResourceID(r))
		extra.AuthSettingsV2 = req.Properties
		webConfigExtras.Put(webResourceID(r), extra)
		props := req.Properties
		if props == nil {
			props = authV2Default()
		}
		sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "authsettingsV2", props))
	})

	// The request's fields replace the stored ones. functions.go serves the site's
	// GET and PUT.
	both("PATCH", "/config/web", webConfigWebPatch)
	slot("GET", "/config/web", webConfigWebGet)
	slot("PUT", "/config/web", webConfigWebPut)

	// Slot mirrors of the config sections functions.go serves for sites.
	slot("PUT", "/config/appsettings", webSlotStringDictPut)
	slot("POST", "/config/appsettings/list", webSlotStringDictList)
	slot("PUT", "/config/connectionstrings", webSlotConnStringsPut)
	slot("POST", "/config/connectionstrings/list", webSlotConnStringsList)
	slot("PUT", "/config/azurestorageaccounts", webSlotAzureStoragePut)
	slot("POST", "/config/azurestorageaccounts/list", webSlotAzureStorageList)
	slot("POST", "/config/publishingcredentials/list", webPublishingCredentials)
}

func nonNilStrMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func authSettingsOrDefault(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{"enabled": false}
	}
	return m
}

func webConfigWebGet(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	row, _ := webResource(r)
	cfg := row.Properties.SiteConfig
	if cfg == nil {
		cfg = &SiteConfig{}
	}
	sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "web", cfg))
}

func webConfigWebPut(w http.ResponseWriter, r *http.Request) {
	webConfigWebWrite(w, r, false)
}

func webConfigWebPatch(w http.ResponseWriter, r *http.Request) {
	webConfigWebWrite(w, r, true)
}

// A PUT replaces the configuration and a PATCH decodes onto it; both keep the
// app settings when the request carries none, as they are their own resource.
// The workload restarts, as App Service restarts an app whose configuration
// changed.
func webConfigWebWrite(w http.ResponseWriter, r *http.Request, merge bool) {
	if webMissing(w, r) {
		return
	}
	store := webResourceStore(r)
	row, _ := store.Get(webResourceID(r))
	var req struct {
		Properties SiteConfig `json:"properties"`
	}
	if merge && row.Properties.SiteConfig != nil {
		req.Properties = *row.Properties.SiteConfig
		req.Properties.AppSettings = nil
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	if req.Properties.AppSettings == nil && row.Properties.SiteConfig != nil {
		req.Properties.AppSettings = row.Properties.SiteConfig.AppSettings
	}
	webKeepScmType(&req.Properties, &row)
	row.Properties.SiteConfig = &req.Properties
	store.Put(webResourceID(r), row)
	webRecordConfigSnapshot(webResourceID(r), row.Properties.SiteConfig)
	restartAzureFunctionInstance(row)
	sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "web", row.Properties.SiteConfig))
}

func registerWebLifecycle(both func(string, string, http.HandlerFunc)) {
	// Each lifecycle operation records itself in the journal the restart
	// detectors read.
	setState := func(state, operation string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if webMissing(w, r) {
				return
			}
			store := webResourceStore(r)
			row, _ := store.Get(webResourceID(r))
			row.Properties.State = state
			store.Put(webResourceID(r), row)
			if err := webApplySiteState(row); err != nil {
				AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
				return
			}
			recordWebSiteEvent(webResourceID(r), operation, webEventCauseUser)
			w.WriteHeader(http.StatusOK)
		}
	}
	both("POST", "/start", setState("Running", "Start"))
	both("POST", "/stop", setState("Stopped", "Stop"))
	// Tear the workload container down; the next request brings a new one up.
	both("POST", "/restart", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		restartAzureFunctionInstance(site)
		recordWebSiteEvent(webResourceID(r), "Restart", webEventCauseUser)
		w.WriteHeader(http.StatusOK)
	})

	registerWebSlotSwap(both)
	both("POST", "/applySlotConfig", webApplySlotConfig)
	both("POST", "/resetSlotConfig", webResetSlotConfig)
	both("POST", "/slotsdiffs", webListSlotDifferences)

	// Answers an empty collection.
	both("GET", "/usages", emptyValueIfExists)

	// The response is a raw XML publishing profile, not a JSON resource.
	both("POST", "/publishxml", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		scm := siteScmHost(&site)
		if _, _, err := net.SplitHostPort(scm); err != nil {
			scm += ":443"
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `<publishData><publishProfile profileName="%s - Web Deploy" publishMethod="MSDeploy" publishUrl="%s" userName="%s" destinationAppUrl="https://%s"></publishProfile></publishData>`,
			strings.Replace(site.Name, "/", "-", 1), scm, webPublishingUserName(&site), site.Properties.DefaultHostName)
	})
}

func emptyValueIfExists(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
}

func registerWebDeployments(both func(string, string, http.HandlerFunc)) {
	deployID := func(r *http.Request) string {
		return webResourceID(r) + "/deployments/" + sim.PathParam(r, "id")
	}
	both("GET", "/deployments", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		prefix := webResourceID(r) + "/deployments/"
		out := webDeployments.Filter(func(d WebDeployment) bool { return strings.HasPrefix(d.ID, prefix) })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	both("GET", "/deployments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		d, ok := webDeployments.Get(deployID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Deployment %q not found.", sim.PathParam(r, "id"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, d)
	})
	both("PUT", "/deployments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req WebDeployment
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		id := sim.PathParam(r, "id")
		req.ID = deployID(r)
		req.Name = id
		req.Type = "Microsoft.Web/sites/deployments"
		webDeployments.Put(req.ID, req)
		sim.WriteJSON(w, http.StatusOK, req)
	})
	both("DELETE", "/deployments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webDeployments.Delete(deployID(r))
		webKuduDeployments.Delete(deployID(r))
		w.WriteHeader(http.StatusOK)
	})
	both("GET", "/deployments/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		d, ok := webDeployments.Get(deployID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Deployment %q not found.", sim.PathParam(r, "id"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, d)
	})
}

func registerWebHostNameBindings(both func(string, string, http.HandlerFunc)) {
	bindID := func(r *http.Request) string {
		return webResourceID(r) + "/hostNameBindings/" + sim.PathParam(r, "hostName")
	}
	both("GET", "/hostNameBindings", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		prefix := webResourceID(r) + "/hostNameBindings/"
		out := webHostNameBindings.Filter(func(b WebHostNameBinding) bool { return strings.HasPrefix(b.ID, prefix) })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	both("GET", "/hostNameBindings/{hostName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		b, ok := webHostNameBindings.Get(bindID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Hostname binding %q not found.", sim.PathParam(r, "hostName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, b)
	})
	both("PUT", "/hostNameBindings/{hostName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req WebHostNameBinding
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		host := sim.PathParam(r, "hostName")
		req.ID = bindID(r)
		req.Name = sim.PathParam(r, "siteName") + "/" + host
		req.Type = "Microsoft.Web/sites/hostNameBindings"
		if req.Properties.SiteName == "" {
			req.Properties.SiteName = sim.PathParam(r, "siteName")
		}
		webHostNameBindings.Put(req.ID, req)
		sim.WriteJSON(w, http.StatusOK, req)
	})
	both("DELETE", "/hostNameBindings/{hostName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webHostNameBindings.Delete(bindID(r))
		w.WriteHeader(http.StatusOK)
	})
}

func registerWebSiteExtensions(both func(string, string, http.HandlerFunc)) {
	extID := func(r *http.Request) string {
		return webResourceID(r) + "/siteextensions/" + sim.PathParam(r, "siteExtensionId")
	}
	both("GET", "/siteextensions", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		prefix := webResourceID(r) + "/siteextensions/"
		out := webSiteExtensions.Filter(func(e WebSiteExtension) bool { return strings.HasPrefix(e.ID, prefix) })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	both("GET", "/siteextensions/{siteExtensionId}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		e, ok := webSiteExtensions.Get(extID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Site extension %q not found.", sim.PathParam(r, "siteExtensionId"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, e)
	})
	both("PUT", "/siteextensions/{siteExtensionId}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req WebSiteExtension
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		id := sim.PathParam(r, "siteExtensionId")
		req.ID = extID(r)
		req.Name = id
		req.Type = "Microsoft.Web/sites/siteextensions"
		if req.Properties.ExtensionID == "" {
			req.Properties.ExtensionID = id
		}
		webSiteExtensions.Put(req.ID, req)
		sim.WriteJSON(w, http.StatusOK, req)
	})
	both("DELETE", "/siteextensions/{siteExtensionId}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webSiteExtensions.Delete(extID(r))
		w.WriteHeader(http.StatusNoContent)
	})
}

// functions.go serves the site's list and get.
func registerWebFunctionsRW(both, slot func(string, string, http.HandlerFunc)) {
	funcID := func(r *http.Request) string {
		return webResourceID(r) + "/functions/" + sim.PathParam(r, "functionName")
	}
	both("PUT", "/functions/{functionName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req FunctionEnvelope
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		name := sim.PathParam(r, "functionName")
		req.ID = funcID(r)
		req.Name = sim.PathParam(r, "siteName") + "/" + name
		req.Type = "Microsoft.Web/sites/functions"
		req.FunctionName = name
		azfFunctionConfigs.Put(req.ID, req)
		// Azure provisions a "default" function key with the function.
		ensureWebFunctionKeys(req.ID)
		if !functionsHostSecretsOut(w, r) {
			return
		}
		sim.WriteJSON(w, http.StatusCreated, req)
	})
	both("DELETE", "/functions/{functionName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		azfFunctionConfigs.Delete(funcID(r))
		webFunctionKeys.Delete(funcID(r))
		if site, ok := hostRunFunctionApp(webResourceID(r)); ok {
			if err := removeFunctionSecrets(site, sim.PathParam(r, "functionName")); err != nil {
				AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "%v", err)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Slot mirror of the read half.
	slot("GET", "/functions", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		prefix := webResourceID(r) + "/functions/"
		out := azfFunctionConfigs.Filter(func(f FunctionEnvelope) bool { return strings.HasPrefix(f.ID, prefix) })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	slot("GET", "/functions/{functionName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		f, ok := azfFunctionConfigs.Get(funcID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Function %q not found.", sim.PathParam(r, "functionName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, f)
	})
}

// A site with no row allows basic auth, as a new site does.
type WebBasicPublishingPolicy struct {
	ID    string `json:"id"`
	Allow bool   `json:"allow"`
}

var webBasicPublishingPolicies sim.Store[WebBasicPublishingPolicy]

func webBasicPublishingPolicyID(resID, name string) string {
	return armChildID(resID, "basicPublishingCredentialsPolicies", name)
}

func webBasicPublishingAllowed(resID, name string) bool {
	if webBasicPublishingPolicies == nil {
		return true
	}
	p, ok := webBasicPublishingPolicies.Get(webBasicPublishingPolicyID(resID, name))
	return !ok || p.Allow
}

func basicPubCredsResource(resID, name string) map[string]any {
	typ := "Microsoft.Web/sites/basicPublishingCredentialsPolicies"
	if strings.Contains(resID, "/slots/") {
		typ = "Microsoft.Web/sites/slots/basicPublishingCredentialsPolicies"
	}
	return map[string]any{
		"id":         webBasicPublishingPolicyID(resID, name),
		"name":       name,
		"type":       typ,
		"properties": map[string]any{"allow": webBasicPublishingAllowed(resID, name)},
	}
}

func registerWebBasicPubCreds(both, slot func(string, string, http.HandlerFunc)) {
	policy := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if webMissing(w, r) {
				return
			}
			sim.WriteJSON(w, http.StatusOK, basicPubCredsResource(webResourceID(r), name))
		}
	}
	// functions.go serves the site's ftp and scm GET, so those mount for slots
	// only.
	both("GET", "/basicpublishingcredentialspolicies", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": []any{basicPubCredsResource(webResourceID(r), "ftp"), basicPubCredsResource(webResourceID(r), "scm")},
		})
	})
	put := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if webMissing(w, r) {
				return
			}
			var req struct {
				Properties struct {
					Allow *bool `json:"allow"`
				} `json:"properties"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
				return
			}
			if req.Properties.Allow == nil {
				AzureError(w, "InvalidRequestContent", "The 'properties.allow' property is required.", http.StatusBadRequest)
				return
			}
			id := webBasicPublishingPolicyID(webResourceID(r), name)
			webBasicPublishingPolicies.Put(id, WebBasicPublishingPolicy{ID: id, Allow: *req.Properties.Allow})
			sim.WriteJSON(w, http.StatusOK, basicPubCredsResource(webResourceID(r), name))
		}
	}
	slot("GET", "/basicpublishingcredentialspolicies/ftp", policy("ftp"))
	slot("GET", "/basicpublishingcredentialspolicies/scm", policy("scm"))
	both("PUT", "/basicpublishingcredentialspolicies/ftp", put("ftp"))
	both("PUT", "/basicpublishingcredentialspolicies/scm", put("scm"))
}

// Azure Resource Manager PATCH merges: presence is decided by whether the JSON
// key is in the body, not by a non-zero decoded value, or a tags-only PATCH
// would clear `httpsOnly: false` and `enabled: false`.
func patchWebSite(w http.ResponseWriter, r *http.Request, store sim.Store[Site]) {
	id := webResourceID(r)
	row, ok := store.Get(id)
	if !ok {
		webMissing(w, r)
		return
	}
	var raw map[string]json.RawMessage
	if err := sim.ReadJSON(r, &raw); err != nil {
		AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}

	props := map[string]json.RawMessage{}
	if rawProps, present := raw["properties"]; present {
		if err := json.Unmarshal(rawProps, &props); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
	}

	// Decode a key only when sent, applying it even when its value is zero.
	bad := false
	applyIfPresent := func(body map[string]json.RawMessage, key string, dst any) {
		if bad {
			return
		}
		v, present := body[key]
		if !present {
			return
		}
		if err := json.Unmarshal(v, dst); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			bad = true
		}
	}

	applyIfPresent(raw, "tags", &row.Tags)
	applyIfPresent(raw, "kind", &row.Kind)
	applyIfPresent(props, "serverFarmId", &row.Properties.ServerFarmID)
	applyIfPresent(props, "siteConfig", &row.Properties.SiteConfig)
	applyIfPresent(props, "httpsOnly", &row.Properties.HTTPSOnly)
	applyIfPresent(props, "enabled", &row.Properties.Enabled)
	applyIfPresent(props, "clientCertEnabled", &row.Properties.ClientCertEnabled)
	applyIfPresent(props, "clientCertMode", &row.Properties.ClientCertMode)
	applyIfPresent(props, "clientCertExclusionPaths", &row.Properties.ClientCertExclusionPaths)
	applyIfPresent(props, "virtualNetworkSubnetId", &row.Properties.VirtualNetworkSubnetID)
	applyIfPresent(props, "keyVaultReferenceIdentity", &row.Properties.KeyVaultReferenceIdentity)
	var identity *SiteIdentity
	applyIfPresent(raw, "identity", &identity)
	if bad {
		return
	}
	prev := row
	if identity != nil {
		if err := webApplySiteIdentity(&row, identity, &prev); err != nil {
			AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		webSyncSiteIdentityPrincipal(&prev, &row)
	}

	// sku derives from the plan, so a patched serverFarmId re-derives it.
	if _, present := props["serverFarmId"]; present {
		row.Properties.SKU = webPlanSKUFor(row.Properties.ServerFarmID)
	}

	// An explicit null detaches the VNet integration; json.Unmarshal leaves the
	// destination untouched on null.
	vnsRaw, vnsPresent := props["virtualNetworkSubnetId"]
	if vnsPresent && string(vnsRaw) == "null" {
		row.Properties.VirtualNetworkSubnetID = ""
	}

	store.Put(id, row)
	if _, present := props["siteConfig"]; present {
		restartAzureFunctionInstance(row)
	}

	// A patched value joins or detaches the site's containers, and the stored row
	// reflects the result.
	if vnsPresent {
		if err := applySiteVirtualNetworkSubnetID(r, row, row.Properties.VirtualNetworkSubnetID); err != nil {
			writeVnetIntegrationError(w, row.Name, err)
			return
		}
		row, _ = store.Get(id)
	}
	sim.WriteJSON(w, http.StatusOK, row)
}
