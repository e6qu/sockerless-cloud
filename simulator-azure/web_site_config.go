package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerSiteConfigReads(srv *sim.Server, armBase string, sites sim.Store[Site]) {
	// Store the dictionary on the site so later reads round-trip the mapping.
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

		props := site.AzureStorageAccounts
		if props == nil {
			// terraform-provider-azurerm dereferences a nil in FlattenStorageAccounts
			// when properties is absent; Azure always answers `properties: {}`.
			props = map[string]*AzureStorageInfoValue{}
		}
		sim.WriteJSON(w, http.StatusOK, AzureStoragePropertyDictionaryResource{
			ID:         resourceID + "/config/azurestorageaccounts",
			Name:       "azurestorageaccounts",
			Type:       "Microsoft.Web/sites/config",
			Properties: props,
		})
	})

	// Azure reads this through POST because the response carries storage
	// account keys.
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
			// terraform-provider-azurerm dereferences a nil in FlattenStorageAccounts
			// when properties is absent; Azure always answers `properties: {}`.
			props = map[string]*AzureStorageInfoValue{}
		}
		sim.WriteJSON(w, http.StatusOK, AzureStoragePropertyDictionaryResource{
			ID:         resourceID + "/config/azurestorageaccounts",
			Name:       "azurestorageaccounts",
			Type:       "Microsoft.Web/sites/config",
			Properties: props,
		})
	})

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

	// What PUT /config/logs stored, else every category disabled.
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

	// The password derives from the resource ID and the site's rotation counter:
	// stable across reads, distinct per site, and rotated by
	// WebApps_GenerateNewSitePublishingPassword.
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

// AzureSiteAppSettings is the StringDictionary at
// /sites/{name}/config/appsettings.
type AzureSiteAppSettings struct {
	ID         string            `json:"id,omitempty"`
	Name       string            `json:"name,omitempty"`
	Type       string            `json:"type,omitempty"`
	Properties map[string]string `json:"properties"`
}

type AzureSiteConnStringValue struct {
	Value string `json:"value"`
	Type  string `json:"type"`
}

type AzureSiteConnectionStrings struct {
	ID         string                              `json:"id,omitempty"`
	Name       string                              `json:"name,omitempty"`
	Type       string                              `json:"type,omitempty"`
	Properties map[string]AzureSiteConnStringValue `json:"properties"`
}

// siteConfigPayload is keyed by the canonical resource ID.
type siteConfigPayload struct {
	AppSettings       map[string]string                   `json:"appSettings,omitempty"`
	ConnectionStrings map[string]AzureSiteConnStringValue `json:"connectionStrings,omitempty"`
	SlotConfigNames   *SlotConfigNames                    `json:"slotConfigNames,omitempty"`
}

// SlotConfigNames are the app-setting, connection-string and azure-storage
// names a slot swap leaves with their slot.
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
		// The workload reads its settings from its environment, and a change
		// restarts it, as on App Service.
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

	// AzurePathNormalizationMiddleware lowercases any client casing before
	// dispatch.
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

	// AzurePathNormalizationMiddleware lowercases any client casing before
	// dispatch.
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
		// The workload reads its connection strings from its environment, so a
		// change restarts it.
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

	// terraform-provider-azurerm reads this on every plan refresh, even for a
	// resource with no `sticky_settings` block.
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

type AzureStoragePropertyDictionaryResource struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
	// Always emit properties: terraform-provider-azurerm dereferences a nil when
	// it is absent.
	Properties map[string]*AzureStorageInfoValue `json:"properties"`
}

// siteAzureStorageBinds binds each attached Azure Files share at its
// mountPath, so the site mounts the same share every other container mounting
// that named volume sees, as an Azure Container Apps replica's volume binds
// do.
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

type AzureStorageInfoValue struct {
	Type        string `json:"type,omitempty"`
	AccountName string `json:"accountName,omitempty"`
	ShareName   string `json:"shareName,omitempty"`
	AccessKey   string `json:"accessKey,omitempty"`
	MountPath   string `json:"mountPath,omitempty"`
}
