package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_more.go broadens the Microsoft.Web (App Service / Web Apps) ARM slice
// beyond the function-app core in functions.go: deployment slots, deployments,
// host-name bindings, source control, site extensions, site config sections
// (metadata / logs / auth settings), lifecycle actions (start / stop /
// restart / slot swap), App Service plan list/patch/usages, the
// subscription-global list + check-name + geo/sku catalogs, and Static Web
// Apps. Slots are real Azure resources (a Microsoft.Web/sites child); they are
// stored in their own store and every slot sub-resource operation shares the
// same handler as its production-site counterpart, keyed by a resource ID that
// carries the /slots/{slot} segment.

// Deployment slots reuse the Site shape (a slot is a Microsoft.Web/sites
// child with the same schema). They live in a separate store so the
// production-site list (WebApps_List / ListByResourceGroup) never enumerates
// them.
var webSlots sim.Store[Site]

// WebDeployment mirrors armappservice.Deployment — a publishing-activity
// record under a site/slot.
type WebDeployment struct {
	ID         string                  `json:"id,omitempty"`
	Name       string                  `json:"name,omitempty"`
	Type       string                  `json:"type,omitempty"`
	Properties WebDeploymentProperties `json:"properties"`
}

// WebDeploymentProperties mirrors armappservice.DeploymentProperties. Field
// names are the wire spelling (snake_case) the spec defines.
type WebDeploymentProperties struct {
	ID          string `json:"id,omitempty"`
	Status      int    `json:"status,omitempty"`
	Active      bool   `json:"active,omitempty"`
	Author      string `json:"author,omitempty"`
	AuthorEmail string `json:"author_email,omitempty"`
	Deployer    string `json:"deployer,omitempty"`
	Details     string `json:"details,omitempty"`
	Message     string `json:"message,omitempty"`
	StartTime   string `json:"start_time,omitempty"`
	EndTime     string `json:"end_time,omitempty"`
}

var webDeployments sim.Store[WebDeployment]

// WebHostNameBinding mirrors armappservice.HostNameBinding.
type WebHostNameBinding struct {
	ID         string                       `json:"id,omitempty"`
	Name       string                       `json:"name,omitempty"`
	Type       string                       `json:"type,omitempty"`
	Properties WebHostNameBindingProperties `json:"properties"`
}

// WebHostNameBindingProperties mirrors armappservice.HostNameBindingProperties.
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

// WebSourceControl mirrors armappservice.SiteSourceControl.
type WebSourceControl struct {
	ID         string                     `json:"id,omitempty"`
	Name       string                     `json:"name,omitempty"`
	Type       string                     `json:"type,omitempty"`
	Properties WebSourceControlProperties `json:"properties"`
}

// WebSourceControlProperties mirrors armappservice.SiteSourceControlProperties.
type WebSourceControlProperties struct {
	RepoURL                   string `json:"repoUrl,omitempty"`
	Branch                    string `json:"branch,omitempty"`
	IsManualIntegration       *bool  `json:"isManualIntegration,omitempty"`
	IsGitHubAction            *bool  `json:"isGitHubAction,omitempty"`
	IsMercurial               *bool  `json:"isMercurial,omitempty"`
	DeploymentRollbackEnabled *bool  `json:"deploymentRollbackEnabled,omitempty"`
}

var webSourceControls sim.Store[WebSourceControl]

// WebSiteExtension mirrors armappservice.SiteExtensionInfo.
type WebSiteExtension struct {
	ID         string                     `json:"id,omitempty"`
	Name       string                     `json:"name,omitempty"`
	Type       string                     `json:"type,omitempty"`
	Properties WebSiteExtensionProperties `json:"properties"`
}

// WebSiteExtensionProperties mirrors armappservice.SiteExtensionInfoProperties
// (the subset the simulator round-trips).
type WebSiteExtensionProperties struct {
	ExtensionID   string `json:"extension_id,omitempty"`
	Title         string `json:"title,omitempty"`
	ExtensionType string `json:"extension_type,omitempty"`
	Version       string `json:"version,omitempty"`
	Description   string `json:"description,omitempty"`
}

var webSiteExtensions sim.Store[WebSiteExtension]

// webConfigExtra holds the site/slot config sections that aren't part of the
// SiteConfig (web) block: metadata, diagnostic logs, and the two Easy Auth
// shapes. Keyed by the canonical resource ID (production site or slot). The
// secret-bearing app-settings / connection-strings stay in siteConfigStore,
// never here.
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
	initWebDeployStores(srv)
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

// webCleanupSiteResources removes every child record stored under a deleted
// site or slot: deployment slots (a production-site delete removes its whole
// subtree, as real Azure does), deployments, host-name bindings, source
// controls, site extensions, sitecontainers, config sections, public
// certificates, domain ownership identifiers, premier add-ons, push settings,
// Functions host and function keys, webjobs (their containers stopped) and
// run history, deployed site content and deployment operation records,
// deployed workflow artifacts and the workflows they materialized, VNet
// connections and their gateways, hybrid connections (both spellings),
// private endpoint connections, and the private access record. Without
// this, a site recreated under the same name would inherit the deleted
// site's children.
func webCleanupSiteResources(resID string) {
	ids := []string{resID}
	slotPrefix := resID + "/slots/"
	for _, s := range webSlots.Filter(func(row Site) bool { return strings.HasPrefix(row.ID, slotPrefix) }) {
		ids = append(ids, s.ID)
		// A slot deleted with its parent app is retained the same way a
		// directly deleted slot is — DeletedSiteProperties carries the slot
		// name for exactly this case.
		webRecordDeletedSite(s.ID, s)
		webSlots.Delete(s.ID)
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

// webResourceID builds the canonical resource ID for the addressed site or
// slot (the /slots/{slot} segment is appended when present).
func webResourceID(r *http.Request) string {
	id := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "siteName"))
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

// webMissing writes the canonical ARM 404 when the addressed site/slot does
// not exist; returns true when it wrote a response.
func webMissing(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := webResource(r); ok {
		return false
	}
	name := sim.PathParam(r, "siteName")
	if slot := sim.PathParam(r, "slot"); slot != "" {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Web/sites/%s/slots/%s' was not found.", name, slot)
		return true
	}
	sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource 'Microsoft.Web/sites/%s' was not found.", name)
	return true
}

// registerWebSiteAndSlotHandlers wires every operation that exists identically
// on a production site and on a deployment slot. `both` registers a handler at
// both levels; `slot` only at the slot level (the production-site spelling is
// already served by functions.go); `site` only at the production-site level.
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
	registerWebDiagnostics(both)
	registerWebBackups(both)
	registerWebProviderGlobal(srv, site)

	// PATCH a production site — merge tags/properties. (Slot PATCH lives in
	// registerWebSlotCRUD.)
	site("PATCH", "", func(w http.ResponseWriter, r *http.Request) {
		patchWebSite(w, r, azfSites)
	})
}

// configResource is the standard {id,name,type,properties} envelope every
// Microsoft.Web/sites/config sub-resource shares.
func configResource(resID, name string, props any) map[string]any {
	return map[string]any{
		"id":         resID + "/config/" + name,
		"name":       name,
		"type":       "Microsoft.Web/sites/config",
		"properties": props,
	}
}

func registerWebConfigSections(both, slot func(string, string, http.HandlerFunc)) {
	// GET /config — list every config sub-resource (the SiteConfig "web"
	// block is the one the simulator persists).
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

	// /config/metadata — a StringDictionary. PUT stores, POST /list returns.
	both("PUT", "/config/metadata", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req struct {
			Properties map[string]string `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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

	// /config/logs — diagnostic logs config. GET (slot only; the production
	// site GET is in functions.go), PUT (both).
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
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		extra, _ := webConfigExtras.Get(webResourceID(r))
		extra.Logs = req.Properties
		webConfigExtras.Put(webResourceID(r), extra)
		props := req.Properties
		if props == nil {
			props = logsDefault()
		}
		sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "logs", props))
	})

	// /config/authsettings — Easy Auth (v1). PUT (both), POST /list (slot only).
	both("PUT", "/config/authsettings", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req struct {
			Properties map[string]any `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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

	// /config/authsettingsV2 — Easy Auth (v2). GET + PUT (both), GET /list
	// (slot only; the production-site /list is in functions.go).
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
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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

	// PATCH /config/web — partial SiteConfig update (both levels). GET/PUT
	// for the production site are in functions.go.
	both("PATCH", "/config/web", webConfigWebPut)
	slot("GET", "/config/web", webConfigWebGet)
	slot("PUT", "/config/web", webConfigWebPut)

	// Slot-level mirrors of the production-site config sections functions.go
	// already serves: app settings, connection strings, azure storage
	// accounts, publishing credentials, backup config.
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
	if webMissing(w, r) {
		return
	}
	var req struct {
		Properties SiteConfig `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	store := webResourceStore(r)
	row, _ := store.Get(webResourceID(r))
	row.Properties.SiteConfig = &req.Properties
	store.Put(webResourceID(r), row)
	webRecordConfigSnapshot(webResourceID(r), row.Properties.SiteConfig)
	sim.WriteJSON(w, http.StatusOK, configResource(webResourceID(r), "web", row.Properties.SiteConfig))
}

func registerWebLifecycle(both func(string, string, http.HandlerFunc)) {
	// Each lifecycle operation records what it did in the site's event
	// journal, which is what the restart detectors report from.
	setState := func(state, operation string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if webMissing(w, r) {
				return
			}
			if state != "" {
				store := webResourceStore(r)
				row, _ := store.Get(webResourceID(r))
				row.Properties.State = state
				store.Put(webResourceID(r), row)
			}
			recordWebSiteEvent(webResourceID(r), operation, webEventCauseUser)
			w.WriteHeader(http.StatusOK)
		}
	}
	both("POST", "/start", setState("Running", "Start"))
	both("POST", "/stop", setState("Stopped", "Stop"))
	// A restart restarts: the site's workload container is torn down, and the
	// next request to the site brings a new one up. Without that the operation
	// would report success while the same process kept running.
	both("POST", "/restart", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		stopAzureFunctionInstance(site.Name)
		recordWebSiteEvent(webResourceID(r), "Restart", webEventCauseUser)
		w.WriteHeader(http.StatusOK)
	})

	// Slot-swap family — production⇄slot config exchange. The simulator
	// records the call and returns success (the swap itself is an
	// orchestration the sim does not model deeply).
	both("POST", "/slotsswap", okIfExists)
	both("POST", "/applySlotConfig", okIfExists)
	both("POST", "/resetSlotConfig", okIfExists)
	both("POST", "/slotsdiffs", emptyValueIfExists)

	// /usages — resource usage quotas (empty for a sim site).
	both("GET", "/usages", emptyValueIfExists)

	// /publishxml — publishing profile (FileZilla/WebDeploy/FTP XML). The
	// response is a raw XML document, not a JSON resource.
	both("POST", "/publishxml", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		name := sim.PathParam(r, "siteName")
		host := name + ".azurewebsites.net"
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `<publishData><publishProfile profileName="%s - Web Deploy" publishMethod="MSDeploy" publishUrl="%s:443" userName="$%s" destinationAppUrl="https://%s"></publishProfile></publishData>`,
			name, name+".scm.azurewebsites.net", name, host)
	})
}

func okIfExists(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	w.WriteHeader(http.StatusOK)
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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
		w.WriteHeader(http.StatusOK)
	})
	both("GET", "/deployments/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		d, ok := webDeployments.Get(deployID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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

func registerWebSourceControl(both func(string, string, http.HandlerFunc)) {
	scID := func(r *http.Request) string { return webResourceID(r) + "/sourcecontrols/web" }
	get := func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		sc, ok := webSourceControls.Get(scID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"No source control configured for %q.", sim.PathParam(r, "siteName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, sc)
	}
	put := func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req WebSourceControl
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		req.ID = scID(r)
		req.Name = "web"
		req.Type = "Microsoft.Web/sites/sourcecontrols"
		webSourceControls.Put(req.ID, req)
		sim.WriteJSON(w, http.StatusOK, req)
	}
	both("GET", "/sourcecontrols/web", get)
	both("PUT", "/sourcecontrols/web", put)
	both("PATCH", "/sourcecontrols/web", put)
	both("DELETE", "/sourcecontrols/web", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		webSourceControls.Delete(scID(r))
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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

// registerWebFunctionsRW adds the write half of the functions sub-resource
// (functions list/get are served by functions.go for the production site).
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
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		name := sim.PathParam(r, "functionName")
		req.ID = funcID(r)
		req.Name = sim.PathParam(r, "siteName") + "/" + name
		req.Type = "Microsoft.Web/sites/functions"
		req.FunctionName = name
		azfFunctionConfigs.Put(req.ID, req)
		// Real Azure provisions a "default" function key with the function.
		ensureWebFunctionKeys(req.ID)
		sim.WriteJSON(w, http.StatusCreated, req)
	})
	both("DELETE", "/functions/{functionName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		azfFunctionConfigs.Delete(funcID(r))
		webFunctionKeys.Delete(funcID(r))
		w.WriteHeader(http.StatusNoContent)
	})

	// Slot mirror of the read half (production-site list/get is in functions.go).
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Function %q not found.", sim.PathParam(r, "functionName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, f)
	})
}

func basicPubCredsResource(resID, name string) map[string]any {
	return map[string]any{
		"id":         resID + "/basicPublishingCredentialsPolicies/" + name,
		"name":       name,
		"type":       "Microsoft.Web/sites/basicPublishingCredentialsPolicies",
		"properties": map[string]any{"allow": true},
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
	// GET list (both); GET ftp/scm for the production site are in functions.go,
	// so add those only for slots. PUT ftp/scm on both.
	both("GET", "/basicpublishingcredentialspolicies", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": []any{basicPubCredsResource(webResourceID(r), "ftp"), basicPubCredsResource(webResourceID(r), "scm")},
		})
	})
	slot("GET", "/basicpublishingcredentialspolicies/ftp", policy("ftp"))
	slot("GET", "/basicpublishingcredentialspolicies/scm", policy("scm"))
	both("PUT", "/basicpublishingcredentialspolicies/ftp", policy("ftp"))
	both("PUT", "/basicpublishingcredentialspolicies/scm", policy("scm"))
}

// patchWebSite merges a PATCH body's tags/properties into the stored row.
//
// ARM PATCH is a merge, not a replace: a field the client omits keeps its
// stored value. Presence is therefore decided by whether the JSON key is
// present in the request body, not by whether the decoded Go value is
// non-zero — the two disagree for every field whose zero value is meaningful
// (`httpsOnly: false`, `enabled: false`), and a struct-only decode would let a
// tags-only PATCH silently clear them.
func patchWebSite(w http.ResponseWriter, r *http.Request, store sim.Store[Site]) {
	id := webResourceID(r)
	row, ok := store.Get(id)
	if !ok {
		webMissing(w, r)
		return
	}
	var raw map[string]json.RawMessage
	if err := sim.ReadJSON(r, &raw); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}

	props := map[string]json.RawMessage{}
	if rawProps, present := raw["properties"]; present {
		if err := json.Unmarshal(rawProps, &props); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
	}

	// applyIfPresent decodes one key into dst only when the client sent it, so
	// an absent key leaves the stored value untouched and a present key is
	// applied even when its value is the type's zero.
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
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			bad = true
		}
	}

	applyIfPresent(raw, "tags", &row.Tags)
	applyIfPresent(raw, "kind", &row.Kind)
	applyIfPresent(props, "serverFarmId", &row.Properties.ServerFarmID)
	applyIfPresent(props, "siteConfig", &row.Properties.SiteConfig)
	applyIfPresent(props, "httpsOnly", &row.Properties.HTTPSOnly)
	applyIfPresent(props, "enabled", &row.Properties.Enabled)
	applyIfPresent(props, "clientCertMode", &row.Properties.ClientCertMode)
	applyIfPresent(props, "virtualNetworkSubnetId", &row.Properties.VirtualNetworkSubnetID)
	if bad {
		return
	}

	// The sku member derives from the associated App Service plan, so a
	// patched serverFarmId re-derives it.
	if _, present := props["serverFarmId"]; present {
		row.Properties.SKU = webPlanSKUFor(row.Properties.ServerFarmID)
	}

	// An explicit JSON null detaches the VNet integration (json.Unmarshal
	// leaves the destination untouched on null, so it is handled here).
	vnsRaw, vnsPresent := props["virtualNetworkSubnetId"]
	if vnsPresent && string(vnsRaw) == "null" {
		row.Properties.VirtualNetworkSubnetID = ""
	}

	store.Put(id, row)

	// virtualNetworkSubnetId is the modern spelling of regional VNet
	// integration: a patched value joins (or detaches) the site's containers
	// for real, and the stored row ends up reflecting the resulting state.
	if vnsPresent {
		if err := applySiteVirtualNetworkSubnetID(r, row, row.Properties.VirtualNetworkSubnetID); err != nil {
			sim.AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
				"failed to integrate site %q into VNet: %v", row.Name, err)
			return
		}
		row, _ = store.Get(id)
	}
	sim.WriteJSON(w, http.StatusOK, row)
}
