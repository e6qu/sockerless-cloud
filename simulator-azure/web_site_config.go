package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerSiteConfigReads(srv *sim.Server, armBase string, sites sim.Store[Site]) {
	// PUT - Update site's azurestorageaccounts mapping. Backend's
	// volumes.go uses WebApps.UpdateAzureStorageAccounts to bind named
	// docker volumes to Azure Files shares on the function app site.
	// Wire format: AzureStoragePropertyDictionaryResource —
	// `{ "properties": { "<volname>": { "type": "AzureFiles",
	// "accountName": "...", "shareName": "...", "accessKey": "...",
	// "mountPath": "/mnt/<vol>" }, ... } }`. The sim stores the dict
	// onto the site's AzureStorageAccounts field so subsequent GETs
	// round-trip the mapping.
	srv.HandleFunc("PUT "+armBase+"/sites/{siteName}/config/azurestorageaccounts", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")

		resourceID := armResourceID(sub, rg, "Microsoft.Web", "sites", name)
		site, ok := sites.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		var req AzureStoragePropertyDictionaryResource
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		site.AzureStorageAccounts = req.Properties
		sites.Put(resourceID, site)
		restartAzureFunctionInstance(site)

		// ARM convention: respond with the resource shape that was PUT.
		props := site.AzureStorageAccounts
		if props == nil {
			// Real Azure always returns `properties: {}` (empty object,
			// not absent). terraform-provider-azurerm panics with
			// nil-deref in FlattenStorageAccounts when properties is
			// absent. Emit empty map.
			props = map[string]*AzureStorageInfoValue{}
		}
		sim.WriteJSON(w, http.StatusOK, AzureStoragePropertyDictionaryResource{
			ID:         resourceID + "/config/azurestorageaccounts",
			Name:       "azurestorageaccounts",
			Type:       "Microsoft.Web/sites/config",
			Properties: props,
		})
	})

	// POST /list — real Azure uses POST for `/list` actions because the
	// response contains storage account keys (kept out of GET URLs).
	// terraform-provider-azurerm reads via this endpoint on every plan.
	srv.HandleFunc("POST "+armBase+"/sites/{siteName}/config/azurestorageaccounts/list", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")
		resourceID := armResourceID(sub, rg, "Microsoft.Web", "sites", name)
		site, ok := sites.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		props := site.AzureStorageAccounts
		if props == nil {
			// Real Azure always returns `properties: {}` (empty object,
			// not absent). terraform-provider-azurerm panics with
			// nil-deref in FlattenStorageAccounts when properties is
			// absent. Emit empty map.
			props = map[string]*AzureStorageInfoValue{}
		}
		sim.WriteJSON(w, http.StatusOK, AzureStoragePropertyDictionaryResource{
			ID:         resourceID + "/config/azurestorageaccounts",
			Name:       "azurestorageaccounts",
			Type:       "Microsoft.Web/sites/config",
			Properties: props,
		})
	})

	// GET /sites/{name}/basicPublishingCredentialsPolicies/{ftp|scm} —
	// the per-protocol allow flag for FTP / SCM basic auth on the
	// site's publishing endpoints. Real Azure: `properties.allow`
	// true/false. terraform-provider-azurerm reads both on every plan
	// refresh; either error blocks state convergence. Sim returns
	// true (allowed, the real Azure default for newly-created sites).
	basicPubCredsHandler := func(policyName string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			name := sim.PathParam(r, "siteName")
			resourceID := armResourceID(sub, rg, "Microsoft.Web", "sites", name)
			if _, ok := sites.Get(resourceID); !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
				return
			}
			sim.WriteJSON(w, http.StatusOK, basicPubCredsResource(resourceID, policyName))
		}
	}
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/basicpublishingcredentialspolicies/ftp", basicPubCredsHandler("ftp"))
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/basicpublishingcredentialspolicies/scm", basicPubCredsHandler("scm"))

	// GET /config/logs — App Service diagnostic logs configuration
	// (application logging, http logging, detailed errors, failed
	// request tracing): what PUT /config/logs stored, else every category
	// disabled.
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/config/logs", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")
		resourceID := armResourceID(sub, rg, "Microsoft.Web", "sites", name)
		if _, ok := sites.Get(resourceID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		props := map[string]any{
			"applicationLogs":       map[string]any{"fileSystem": map[string]any{"level": "Off"}},
			"httpLogs":              map[string]any{},
			"detailedErrorMessages": map[string]any{"enabled": false},
			"failedRequestsTracing": map[string]any{"enabled": false},
		}
		if extra, ok := webConfigExtras.Get(resourceID); ok && extra.Logs != nil {
			props = extra.Logs
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":         resourceID + "/config/logs",
			"name":       "logs",
			"type":       "Microsoft.Web/sites/config",
			"properties": props,
		})
	})

	// POST /config/authsettings/list — Easy Auth configuration. The
	// sim doesn't model App Service authentication, so the truthful
	// response is `enabled: false` + default empty fields (no auth
	// providers configured). terraform-provider-azurerm reads on
	// every plan refresh; an error here blocks state convergence.
	srv.HandleFunc("POST "+armBase+"/sites/{siteName}/config/authsettings/list", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")
		resourceID := armResourceID(sub, rg, "Microsoft.Web", "sites", name)
		if _, ok := sites.Get(resourceID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":   resourceID + "/config/authsettings",
			"name": "authsettings",
			"type": "Microsoft.Web/sites/config",
			"properties": map[string]any{
				"enabled": false,
			},
		})
	})

	// GET /config/authsettingsV2/list — Auth V2 (the newer Easy Auth
	// shape introduced in API 2020-12-01). Same truthful default:
	// authentication is not enabled on this sim site.
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/config/authsettingsv2/list", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")
		resourceID := armResourceID(sub, rg, "Microsoft.Web", "sites", name)
		if _, ok := sites.Get(resourceID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":   resourceID + "/config/authsettingsV2",
			"name": "authsettingsV2",
			"type": "Microsoft.Web/sites/config",
			"properties": map[string]any{
				"platform":          map[string]any{"enabled": false},
				"globalValidation":  map[string]any{},
				"identityProviders": map[string]any{},
				"login":             map[string]any{},
				"httpSettings":      map[string]any{},
			},
		})
	})

	// Also add to lowercase canonicalization map so /authsettingsV2 →
	// /authsettingsv2 in the middleware. Done via the package-level
	// middleware.

	// POST /config/publishingcredentials/list — real Azure returns the
	// SCM publishing user/password for App Service deployment + Kudu
	// console access. terraform-provider-azurerm reads via this endpoint
	// on every plan refresh.
	//
	// The password derives deterministically from the resource ID and the
	// site's rotation counter: stable across reads, distinct per site, and
	// rotated by `POST .../newpassword`
	// (WebApps_GenerateNewSitePublishingPassword).
	srv.HandleFunc("POST "+armBase+"/sites/{siteName}/config/publishingcredentials/list", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")
		resourceID := armResourceID(sub, rg, "Microsoft.Web", "sites", name)
		site, ok := sites.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		user := webPublishingUserName(&site)
		password := webPublishingPassword(resourceID)
		scmURI := webPublishingScmURI(&site, user, password)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":   resourceID + "/config/publishingcredentials",
			"name": "publishingcredentials",
			"type": "Microsoft.Web/sites/config",
			"properties": map[string]any{
				"publishingUserName": user,
				"publishingPassword": password,
				"scmUri":             scmURI,
			},
		})
	})
}

// AzureSiteAppSettings is the canonical StringDictionary wire shape
// real Azure emits at /sites/{name}/config/appsettings — a flat
// map of setting name → value wrapped in {properties:{...}}.
type AzureSiteAppSettings struct {
	ID         string            `json:"id,omitempty"`
	Name       string            `json:"name,omitempty"`
	Type       string            `json:"type,omitempty"`
	Properties map[string]string `json:"properties"`
}

// AzureSiteConnStringValue is the per-entry wire shape — connection
// string value plus the protocol type (MySql / SQLServer / Custom / …).
type AzureSiteConnStringValue struct {
	Value string `json:"value"`
	Type  string `json:"type"`
}

// AzureSiteConnectionStrings is the wire shape at
// /sites/{name}/config/connectionstrings.
type AzureSiteConnectionStrings struct {
	ID         string                              `json:"id,omitempty"`
	Name       string                              `json:"name,omitempty"`
	Type       string                              `json:"type,omitempty"`
	Properties map[string]AzureSiteConnStringValue `json:"properties"`
}

// siteConfigPayload persists per-section site config alongside the
// Site struct. Keyed by the canonical resource ID.
type siteConfigPayload struct {
	AppSettings       map[string]string                   `json:"appSettings,omitempty"`
	ConnectionStrings map[string]AzureSiteConnStringValue `json:"connectionStrings,omitempty"`
	SlotConfigNames   *SlotConfigNames                    `json:"slotConfigNames,omitempty"`
}

// SlotConfigNames mirrors the real Microsoft.Web/sites/config/slotconfignames
// shape: the lists of app-setting / connection-string / azure-storage
// names that are pinned to a deployment slot during slot swap.
type SlotConfigNames struct {
	AppSettingNames         []string `json:"appSettingNames,omitempty"`
	ConnectionStringNames   []string `json:"connectionStringNames,omitempty"`
	AzureStorageConfigNames []string `json:"azureStorageConfigNames,omitempty"`
}

var siteConfigStore sim.Store[siteConfigPayload]

func registerSiteConfigHandlers(srv *sim.Server, armBase string, sites sim.Store[Site]) {
	siteConfigStore = sim.MakeStore[siteConfigPayload](srv.DB(), "site_configs")

	siteResourceID := func(r *http.Request) string {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")
		return armResourceID(sub, rg, "Microsoft.Web", "sites", name)
	}
	siteExists := func(resourceID string) bool {
		_, ok := sites.Get(resourceID)
		return ok
	}

	// PUT /sites/{name}/config/appsettings
	srv.HandleFunc("PUT "+armBase+"/sites/{siteName}/config/appsettings", func(w http.ResponseWriter, r *http.Request) {
		resourceID := siteResourceID(r)
		if !siteExists(resourceID) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Site %q not found.", sim.PathParam(r, "siteName"))
			return
		}
		var req AzureSiteAppSettings
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		cfg, _ := siteConfigStore.Get(resourceID)
		cfg.AppSettings = req.Properties
		siteConfigStore.Put(resourceID, cfg)
		// The site's workload reads its settings from siteConfig.appSettings,
		// and a settings change restarts it, as it restarts an App Service app.
		if site, ok := sites.Get(resourceID); ok {
			if site.Properties.SiteConfig == nil {
				site.Properties.SiteConfig = &SiteConfig{}
			}
			site.Properties.SiteConfig.AppSettings = nameValuePairs(req.Properties)
			sites.Put(resourceID, site)
			restartAzureFunctionInstance(site)
		}
		sim.WriteJSON(w, http.StatusOK, AzureSiteAppSettings{
			ID:         resourceID + "/config/appsettings",
			Name:       "appsettings",
			Type:       "Microsoft.Web/sites/config",
			Properties: cfg.AppSettings,
		})
	})

	// POST /sites/{name}/config/appsettings/list — real Azure uses POST
	// for `/list` actions because the response contains secrets (kept
	// out of GET URLs / proxy logs). Single lowercase registration;
	// AzurePathNormalizationMiddleware canonicalizes any client casing
	// (`appSettings` / `AppSettings`) to lowercase before dispatch.
	srv.HandleFunc("POST "+armBase+"/sites/{siteName}/config/appsettings/list", func(w http.ResponseWriter, r *http.Request) {
		resourceID := siteResourceID(r)
		if !siteExists(resourceID) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Site %q not found.", sim.PathParam(r, "siteName"))
			return
		}
		cfg, _ := siteConfigStore.Get(resourceID)
		props := cfg.AppSettings
		if props == nil {
			props = map[string]string{}
		}
		sim.WriteJSON(w, http.StatusOK, AzureSiteAppSettings{
			ID:         resourceID + "/config/appsettings",
			Name:       "appsettings",
			Type:       "Microsoft.Web/sites/config",
			Properties: props,
		})
	})

	// PUT + POST /list for /config/connectionstrings — single lowercase
	// registration; AzurePathNormalizationMiddleware canonicalizes
	// camelCase variants to lowercase before dispatch.
	srv.HandleFunc("PUT "+armBase+"/sites/{siteName}/config/connectionstrings", func(w http.ResponseWriter, r *http.Request) {
		resourceID := siteResourceID(r)
		if !siteExists(resourceID) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Site %q not found.", sim.PathParam(r, "siteName"))
			return
		}
		var req AzureSiteConnectionStrings
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if err := webValidateConnStrings(req.Properties); err != nil {
			AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		cfg, _ := siteConfigStore.Get(resourceID)
		cfg.ConnectionStrings = req.Properties
		siteConfigStore.Put(resourceID, cfg)
		// The workload reads its connection strings from its environment, so
		// a change restarts it.
		if site, ok := sites.Get(resourceID); ok {
			restartAzureFunctionInstance(site)
		}
		sim.WriteJSON(w, http.StatusOK, AzureSiteConnectionStrings{
			ID:         resourceID + "/config/connectionstrings",
			Name:       "connectionstrings",
			Type:       "Microsoft.Web/sites/config",
			Properties: cfg.ConnectionStrings,
		})
	})
	srv.HandleFunc("POST "+armBase+"/sites/{siteName}/config/connectionstrings/list", func(w http.ResponseWriter, r *http.Request) {
		resourceID := siteResourceID(r)
		if !siteExists(resourceID) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Site %q not found.", sim.PathParam(r, "siteName"))
			return
		}
		cfg, _ := siteConfigStore.Get(resourceID)
		props := cfg.ConnectionStrings
		if props == nil {
			props = map[string]AzureSiteConnStringValue{}
		}
		sim.WriteJSON(w, http.StatusOK, AzureSiteConnectionStrings{
			ID:         resourceID + "/config/connectionstrings",
			Name:       "connectionstrings",
			Type:       "Microsoft.Web/sites/config",
			Properties: props,
		})
	})

	// GET /sites/{name}/config/slotconfignames — the "sticky settings": the
	// app-setting, connection-string and azure-storage names a slot swap
	// leaves with their slot. terraform-provider-azurerm reads it on every
	// plan refresh, even for a resource with no `sticky_settings` block.
	slotConfigNamesGet := func(w http.ResponseWriter, r *http.Request) {
		resourceID := siteResourceID(r)
		if !siteExists(resourceID) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Site %q not found.", sim.PathParam(r, "siteName"))
			return
		}
		cfg, _ := siteConfigStore.Get(resourceID)
		names := cfg.SlotConfigNames
		if names == nil {
			names = &SlotConfigNames{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":         resourceID + "/config/slotconfignames",
			"name":       "slotconfignames",
			"type":       "Microsoft.Web/sites/config",
			"properties": names,
		})
	}
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/config/slotconfignames", slotConfigNamesGet)
	srv.HandleFunc("PUT "+armBase+"/sites/{siteName}/config/slotconfignames", func(w http.ResponseWriter, r *http.Request) {
		resourceID := siteResourceID(r)
		if !siteExists(resourceID) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Site %q not found.", sim.PathParam(r, "siteName"))
			return
		}
		var req struct {
			Properties SlotConfigNames `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		cfg, _ := siteConfigStore.Get(resourceID)
		cfg.SlotConfigNames = &req.Properties
		siteConfigStore.Put(resourceID, cfg)
		slotConfigNamesGet(w, r)
	})

	// GET /sites/{name}/config/web — reads the full SiteConfig from
	// the site row. The siteConfig embedded in SiteProperties is the
	// canonical persistence; this endpoint just projects it out.
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/config/web", func(w http.ResponseWriter, r *http.Request) {
		resourceID := siteResourceID(r)
		site, ok := sites.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Site %q not found.", sim.PathParam(r, "siteName"))
			return
		}
		cfg := site.Properties.SiteConfig
		if cfg == nil {
			cfg = &SiteConfig{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":         resourceID + "/config/web",
			"name":       "web",
			"type":       "Microsoft.Web/sites/config",
			"properties": cfg,
		})
	})

	// PUT /sites/{name}/config/web
	srv.HandleFunc("PUT "+armBase+"/sites/{siteName}/config/web", func(w http.ResponseWriter, r *http.Request) {
		resourceID := siteResourceID(r)
		site, ok := sites.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Site %q not found.", sim.PathParam(r, "siteName"))
			return
		}
		var req struct {
			Properties SiteConfig `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.AppSettings == nil && site.Properties.SiteConfig != nil {
			req.Properties.AppSettings = site.Properties.SiteConfig.AppSettings
		}
		site.Properties.SiteConfig = &req.Properties
		sites.Put(resourceID, site)
		webRecordConfigSnapshot(resourceID, site.Properties.SiteConfig)
		restartAzureFunctionInstance(site)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":         resourceID + "/config/web",
			"name":       "web",
			"type":       "Microsoft.Web/sites/config",
			"properties": site.Properties.SiteConfig,
		})
	})
}

// AzureStoragePropertyDictionaryResource is the wire shape for
// WebApps.UpdateAzureStorageAccounts. Mirrors
// armappservice.AzureStoragePropertyDictionaryResource — a flat
// dictionary of volume-name → Azure Files mount info.
type AzureStoragePropertyDictionaryResource struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
	// Properties is always emitted (no omitempty) — real Azure returns
	// `properties: {}` even with no storage accounts, and
	// terraform-provider-azurerm nil-derefs when absent.
	Properties map[string]*AzureStorageInfoValue `json:"properties"`
}

// siteAzureStorageBinds realizes a single-container site's attached Azure Files
// shares (the site record's AzureStorageAccounts dictionary, set via
// WebApps.UpdateAzureStorageAccounts) as Docker host binds
// `<host-share-dir>:<mountPath>`, so the persistent container mounts the same
// shared share that other containers mounting the same named volume see. This
// is the App Service `azureStorageAccounts` mount contract, mirroring the ACA
// App replica's volume binds.
func siteAzureStorageBinds(site *Site) []string {
	if site == nil {
		return nil
	}
	var binds []string
	for _, v := range site.AzureStorageAccounts {
		if v == nil || v.AccountName == "" || v.ShareName == "" || v.MountPath == "" {
			continue
		}
		binds = append(binds, FileShareHostDir(v.AccountName, v.ShareName)+":"+v.MountPath)
	}
	return binds
}

// AzureStorageInfoValue mirrors armappservice.AzureStorageInfoValue.
type AzureStorageInfoValue struct {
	Type        string `json:"type,omitempty"`
	AccountName string `json:"accountName,omitempty"`
	ShareName   string `json:"shareName,omitempty"`
	AccessKey   string `json:"accessKey,omitempty"`
	MountPath   string `json:"mountPath,omitempty"`
}
