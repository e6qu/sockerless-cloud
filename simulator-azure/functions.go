package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
	dockerclient "github.com/moby/moby/client"
)

// Site represents an Azure Function App (Web App).
//
// AzureStorageAccounts is the site's Azure Files / Blob mount dictionary, set
// via the azurestorageaccounts config sub-resource and never part of the
// Microsoft.Web site wire shape (its own PUT/list routes serve it). It lives
// at the top level of the stored record — not nested in SiteProperties —
// because the persistence sidecar covers exported top-level json:"-" fields,
// keeping the mounts durable across a SIM_PERSIST restart.
type Site struct {
	ID                   string                            `json:"id"`
	Name                 string                            `json:"name"`
	Type                 string                            `json:"type"`
	Kind                 string                            `json:"kind,omitempty"`
	Location             string                            `json:"location"`
	Tags                 map[string]string                 `json:"tags,omitempty"`
	Properties           SiteProperties                    `json:"properties"`
	AzureStorageAccounts map[string]*AzureStorageInfoValue `json:"-"`
}

// SiteProperties holds the properties of a function app.
type SiteProperties struct {
	State            string      `json:"state,omitempty"`
	DefaultHostName  string      `json:"defaultHostName,omitempty"`
	HostNames        []string    `json:"hostNames,omitempty"`
	Enabled          bool        `json:"enabled"`
	EnabledHostNames []string    `json:"enabledHostNames,omitempty"`
	ServerFarmID     string      `json:"serverFarmId,omitempty"`
	SKU              string      `json:"sku,omitempty"`
	Reserved         bool        `json:"reserved,omitempty"`
	SiteConfig       *SiteConfig `json:"siteConfig,omitempty"`
	ResourceGroup    string      `json:"resourceGroup,omitempty"`
	LastModifiedTime string      `json:"lastModifiedTimeUtc,omitempty"`
	HTTPSOnly        bool        `json:"httpsOnly,omitempty"`
	ClientCertMode   string      `json:"clientCertMode,omitempty"`
}

// SiteConfig holds the site configuration for a function app.
type SiteConfig struct {
	AppSettings                            []NameValuePair `json:"appSettings,omitempty"`
	LinuxFxVersion                         string          `json:"linuxFxVersion,omitempty"`
	FunctionAppScaleLimit                  int             `json:"functionAppScaleLimit,omitempty"`
	FtpsState                              string          `json:"ftpsState,omitempty"`
	LoadBalancing                          string          `json:"loadBalancing,omitempty"`
	ManagedPipelineMode                    string          `json:"managedPipelineMode,omitempty"`
	IPSecurityRestrictionsDefaultAction    string          `json:"ipSecurityRestrictionsDefaultAction,omitempty"`
	MinTLSVersion                          string          `json:"minTlsVersion,omitempty"`
	ScmMinTLSVersion                       string          `json:"scmMinTlsVersion,omitempty"`
	ScmIPSecurityRestrictionsDefaultAction string          `json:"scmIpSecurityRestrictionsDefaultAction,omitempty"`
}

// NameValuePair holds a name-value pair for app settings.
type NameValuePair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// FunctionEnvelope represents a function within a function app.
//
// FunctionName is the function's short name, tracked internally; the real
// Microsoft.Web wire shape carries only the ProxyResource `name`
// ("<site>/<function>"), so the short name is never emitted. It sits at the
// top level of the stored record so the persistence sidecar keeps it durable.
type FunctionEnvelope struct {
	ID           string                     `json:"id"`
	Name         string                     `json:"name"`
	Type         string                     `json:"type"`
	Properties   FunctionEnvelopeProperties `json:"properties"`
	FunctionName string                     `json:"-"`
}

// FunctionEnvelopeProperties holds the properties of a function.
//
// ScriptHref / ConfigHref / Href / InvokeURLTemplate are EXTERNAL
// URLs pointing at the operator's deployed Function App
// (`https://<app>.azurewebsites.net/admin/host/...`). The sim emits
// them on every function describe response so SDK consumers parsing
// the envelope see canonical-shape strings, but the sim does not
// service those URLs — they live on a different Azure surface
// (Kudu admin endpoints) the simulator does not implement. Marked
// as external per the `sim-emitted-url-roundtrip` skill's
// "document external" branch.
type FunctionEnvelopeProperties struct {
	FunctionAppID     string         `json:"function_app_id,omitempty"`
	ScriptHref        string         `json:"script_href,omitempty"` // external: Kudu admin URL on the deployed Function App
	ConfigHref        string         `json:"config_href,omitempty"` // external: Kudu admin URL on the deployed Function App
	Href              string         `json:"href,omitempty"`        // external: Kudu admin URL on the deployed Function App
	Config            map[string]any `json:"config,omitempty"`
	InvokeURLTemplate string         `json:"invoke_url_template,omitempty"` // external: HTTP-trigger URL the user's app exposes
	Language          string         `json:"language,omitempty"`
	IsDisabled        bool           `json:"isDisabled"`
}

// Package-level stores for dashboard access and the web_more.go slice (slot
// functions reuse the same function-config store).
var azfSites sim.Store[Site]
var azfFunctionConfigs sim.Store[FunctionEnvelope]

func registerAzureFunctions(srv *sim.Server) {
	sites := sim.MakeStore[Site](srv.DB(), "azf_sites")
	azfSites = sites
	functionConfigs := sim.MakeStore[FunctionEnvelope](srv.DB(), "azf_function_configs")
	azfFunctionConfigs = functionConfigs

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web"

	// Subscription-scoped check for site-name availability. Real Azure
	// validates `<name>.azurewebsites.net` against the global namespace;
	// terraform-provider-azurerm calls this before site creation so
	// conflicts surface as `nameAvailable: false` instead of a 409 on
	// PUT. The sim has no real cross-subscription namespace so we check
	// the local sites store — any existing site name reads as taken.
	checkNameAvailabilityHandler := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		suffix := "/providers/Microsoft.Web/sites/" + req.Name
		taken := len(sites.Filter(func(s Site) bool {
			return strings.HasSuffix(s.ID, suffix)
		})) > 0
		resp := map[string]any{
			"nameAvailable": !taken,
			"message":       "",
		}
		if taken {
			resp["reason"] = "AlreadyExists"
			resp["message"] = fmt.Sprintf("Hostname '%s' already exists. Please select a different name.", req.Name)
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	}
	// Single lowercase registration; AzurePathNormalizationMiddleware
	// canonicalizes any client casing (`checkNameAvailability` /
	// `CheckNameAvailability`) down to lowercase before dispatch.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.Web/checknameavailability", checkNameAvailabilityHandler)

	// PUT - Create or update function app
	srv.HandleFunc("PUT "+armBase+"/sites/{siteName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")

		var req Site
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Location == "" {
			sim.AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)

		kind := req.Kind
		if kind == "" {
			kind = "functionapp"
		}

		// Real Azure assigns a per-site hostname `<site>.azurewebsites.net`
		// — invocations route to the right function app by HTTP Host header.
		// The sim hosts every site on a single port, so callers connect to
		// the sim's TCP address but set Host = `<name>.azurewebsites.net`;
		// the invoke handler matches that against DefaultHostName.
		defaultHostName := name + ".azurewebsites.net"

		// Default the ARM-computed site properties the provider reads back
		// when the request omits them, so a post-apply GET echoes the same
		// values terraform expects (no idempotency drift). These mirror the
		// real Microsoft.Web/sites defaults.
		clientCertMode := req.Properties.ClientCertMode
		if clientCertMode == "" {
			clientCertMode = "Optional"
		}

		siteConfig := req.Properties.SiteConfig
		if siteConfig == nil {
			siteConfig = &SiteConfig{}
		}
		if siteConfig.LoadBalancing == "" {
			siteConfig.LoadBalancing = "LeastRequests"
		}
		if siteConfig.ManagedPipelineMode == "" {
			siteConfig.ManagedPipelineMode = "Integrated"
		}
		if siteConfig.IPSecurityRestrictionsDefaultAction == "" {
			siteConfig.IPSecurityRestrictionsDefaultAction = "Allow"
		}
		if siteConfig.MinTLSVersion == "" {
			siteConfig.MinTLSVersion = "1.2"
		}
		if siteConfig.ScmMinTLSVersion == "" {
			siteConfig.ScmMinTLSVersion = "1.2"
		}
		if siteConfig.ScmIPSecurityRestrictionsDefaultAction == "" {
			siteConfig.ScmIPSecurityRestrictionsDefaultAction = "Allow"
		}

		site := Site{
			ID:       resourceID,
			Name:     name,
			Type:     "Microsoft.Web/sites",
			Kind:     kind,
			Location: req.Location,
			Tags:     req.Tags,
			Properties: SiteProperties{
				State:            "Running",
				DefaultHostName:  defaultHostName,
				HostNames:        []string{defaultHostName},
				Enabled:          true,
				EnabledHostNames: []string{defaultHostName, name + ".scm.azurewebsites.net"},
				ServerFarmID:     req.Properties.ServerFarmID,
				SKU:              webPlanSKUFor(req.Properties.ServerFarmID),
				Reserved:         req.Properties.Reserved,
				SiteConfig:       siteConfig,
				ResourceGroup:    rg,
				LastModifiedTime: time.Now().UTC().Format(time.RFC3339),
				HTTPSOnly:        req.Properties.HTTPSOnly,
				ClientCertMode:   clientCertMode,
			},
		}

		sites.Put(resourceID, site)
		// Real Azure provisions the Functions host key set (master key +
		// "default" host function key) with the new site.
		ensureWebHostKeys(resourceID)

		// terraform-provider-azurerm sends app settings inside the site PUT's
		// siteConfig.appSettings, then reads them back via POST
		// /config/appsettings/list (a separate store). Mirror them into that
		// store so the read recovers FUNCTIONS_EXTENSION_VERSION /
		// AzureWebJobsStorage / AzureWebJobsDashboard — the provider derives
		// functions_extension_version / storage_account_name /
		// builtin_logging_enabled from those, and drops them on drift otherwise.
		if len(siteConfig.AppSettings) > 0 {
			cfg, _ := siteConfigStore.Get(resourceID)
			settings := make(map[string]string, len(siteConfig.AppSettings))
			for _, kv := range siteConfig.AppSettings {
				settings[kv.Name] = kv.Value
			}
			cfg.AppSettings = settings
			siteConfigStore.Put(resourceID, cfg)
		}

		// Always return 200 OK so the ARM SDK's BeginCreateOrUpdate poller
		// treats this as an immediately completed operation.
		sim.WriteJSON(w, http.StatusOK, site)
	})

	// GET - Get function app
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)

		site, ok := sites.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		sim.WriteJSON(w, http.StatusOK, site)
	})

	// GET - List function apps by resource group
	srv.HandleFunc("GET "+armBase+"/sites", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/", sub, rg)

		filtered := sites.Filter(func(s Site) bool {
			return strings.HasPrefix(s.ID, prefix)
		})
		out := make([]Site, 0, len(filtered))
		out = append(out, filtered...)

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": out,
		})
	})

	// DELETE - Delete function app
	srv.HandleFunc("DELETE "+armBase+"/sites/{siteName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)

		if sites.Delete(resourceID) {
			stopAzureFunctionInstance(name)
			cleanupSiteContainers(resourceID, name)
			webCleanupSiteResources(resourceID)
			// Clean up associated functions
			funcs := functionConfigs.Filter(func(f FunctionEnvelope) bool {
				return strings.HasPrefix(f.ID, resourceID+"/functions/")
			})
			for _, f := range funcs {
				functionConfigs.Delete(f.ID)
			}

			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// GET - List functions
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/functions", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)

		// Verify site exists
		if _, ok := sites.Get(resourceID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		filtered := functionConfigs.Filter(func(f FunctionEnvelope) bool {
			return strings.HasPrefix(f.ID, resourceID+"/functions/")
		})

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": filtered,
		})
	})

	// GET - Get function
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/functions/{functionName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		siteName := sim.PathParam(r, "siteName")
		funcName := sim.PathParam(r, "functionName")

		funcID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s/functions/%s",
			sub, rg, siteName, funcName)

		fn, ok := functionConfigs.Get(funcID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The function '%s' in site '%s' was not found.", funcName, siteName)
			return
		}

		sim.WriteJSON(w, http.StatusOK, fn)
	})

	// POST /api/function — invoke a function app, identified by HTTP Host
	// header matching the site's DefaultHostName (real Azure routing).
	// Each site has a unique `<name>.azurewebsites.net` hostname; the
	// azure-functions backend builds invoke URLs from DefaultHostName, and
	// SDK tests set the Host header explicitly when connecting to the sim's
	// TCP port.
	srv.HandleFunc("POST /api/function", func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		var matchedSite *Site
		for _, s := range sites.List() {
			if s.Properties.DefaultHostName == host {
				s := s
				matchedSite = &s
				break
			}
		}
		if matchedSite == nil {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"no function app with DefaultHostName=%q (set Host header to <site>.azurewebsites.net)", host)
			return
		}

		// The real Azure Functions authLevel contract: when the site declares
		// the addressed function's config, the host demands the key its
		// httpTrigger binding's authLevel requires (x-functions-key header or
		// ?code= query) and answers 401 on a wrong or missing key. See
		// azureFunctionInvokeAuthorized.
		if !azureFunctionInvokeAuthorized(matchedSite, r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		responseBody := []byte("{}")
		hasCmd := false
		if matchedSite.Properties.SiteConfig != nil {
			if hasAzureFunctionHTTPBootstrap(matchedSite) {
				body, exitCode, err := invokeAzureFunctionHTTP(matchedSite, r.Body, r.Header.Get("Content-Type"))
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = fmt.Fprintf(w, `{"error":"%s"}`, err.Error())
					return
				}
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("X-Sockerless-Exit-Code", strconv.Itoa(exitCode))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
				return
			}
			for _, setting := range matchedSite.Properties.SiteConfig.AppSettings {
				if setting.Name == "SOCKERLESS_CMD" || setting.Name == "SOCKERLESS_ENTRYPOINT" {
					hasCmd = true
					break
				}
			}
		}
		if hasCmd {
			var exitCode int
			responseBody, exitCode = invokeAzureFunctionProcess(matchedSite)
			if exitCode != 0 {
				// Real Azure Functions returns HTTP error when function crashes
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write(responseBody)
				return
			}
		} else {
			injectAppTrace(matchedSite.Name, "Function invoked")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(responseBody)
	})

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

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)
		site, ok := sites.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		var req AzureStoragePropertyDictionaryResource
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		site.AzureStorageAccounts = req.Properties
		sites.Put(resourceID, site)

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
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)
		site, ok := sites.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

	// POST /config/backup/list — "Get Backup Configuration" (POST because the
	// response carries the storage-account SAS secret). The sim doesn't model
	// backup schedules; real Azure returns 404 when none is configured. Earlier
	// the sim returned 200 with an empty `properties: {}` bag — but
	// terraform-provider-azurerm's FlattenBackupConfig only short-circuits to
	// [] when Properties is nil, so a non-nil empty bag materialised a phantom
	// `backup { enabled = false }` block that drifted every plan. 404 makes the
	// provider treat it as NotFound → no backup block.
	backupNotFound := func(w http.ResponseWriter, r *http.Request) {
		name := sim.PathParam(r, "siteName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), name)
		if _, ok := sites.Get(resourceID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' was not found.", name)
			return
		}
		sim.AzureErrorf(w, "NotFound", http.StatusNotFound,
			"No backup configuration found for site %q.", name)
	}
	srv.HandleFunc("POST "+armBase+"/sites/{siteName}/config/backup/list", backupNotFound)

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
			resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)
			if _, ok := sites.Get(resourceID); !ok {
				sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"id":         resourceID + "/basicPublishingCredentialsPolicies/" + policyName,
				"name":       policyName,
				"type":       "Microsoft.Web/sites/basicPublishingCredentialsPolicies",
				"properties": map[string]any{"allow": true},
			})
		}
	}
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/basicpublishingcredentialspolicies/ftp", basicPubCredsHandler("ftp"))
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/basicpublishingcredentialspolicies/scm", basicPubCredsHandler("scm"))

	// GET /config/logs — App Service diagnostic logs configuration
	// (application logging, http logging, detailed errors, failed
	// request tracing). The sim doesn't model log retention; truthful
	// default response is every category disabled.
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/config/logs", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)
		if _, ok := sites.Get(resourceID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":   resourceID + "/config/logs",
			"name": "logs",
			"type": "Microsoft.Web/sites/config",
			"properties": map[string]any{
				"applicationLogs":       map[string]any{"fileSystem": map[string]any{"level": "Off"}},
				"httpLogs":              map[string]any{},
				"detailedErrorMessages": map[string]any{"enabled": false},
				"failedRequestsTracing": map[string]any{"enabled": false},
			},
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
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)
		if _, ok := sites.Get(resourceID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)
		if _, ok := sites.Get(resourceID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)
		if _, ok := sites.Get(resourceID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		user := "$" + name
		password := webPublishingPassword(resourceID)
		scmURI := fmt.Sprintf("https://%s:%s@%s.scm.azurewebsites.net", user, password, name)
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

	registerSiteConfigHandlers(srv, armBase, sites)
	registerSiteContainerHandlers(srv, armBase)
	registerSiteVNetIntegration(srv, armBase, sites)
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
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)
	}
	siteExists := func(resourceID string) bool {
		_, ok := sites.Get(resourceID)
		return ok
	}

	// PUT /sites/{name}/config/appsettings
	srv.HandleFunc("PUT "+armBase+"/sites/{siteName}/config/appsettings", func(w http.ResponseWriter, r *http.Request) {
		resourceID := siteResourceID(r)
		if !siteExists(resourceID) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Site %q not found.", sim.PathParam(r, "siteName"))
			return
		}
		var req AzureSiteAppSettings
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		cfg, _ := siteConfigStore.Get(resourceID)
		cfg.AppSettings = req.Properties
		siteConfigStore.Put(resourceID, cfg)
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Site %q not found.", sim.PathParam(r, "siteName"))
			return
		}
		var req AzureSiteConnectionStrings
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		cfg, _ := siteConfigStore.Get(resourceID)
		cfg.ConnectionStrings = req.Properties
		siteConfigStore.Put(resourceID, cfg)
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

	// GET /sites/{name}/config/slotconfignames — the "sticky settings"
	// list (which app-setting / connection-string / azure-storage names
	// should be preserved during slot swap). terraform-provider-azurerm
	// reads this on every plan refresh even when the resource has no
	// `sticky_settings` block. The sim doesn't model slot swaps, so
	// the truthful response is empty arrays for every category. PUT is
	// also supported so a future `sticky_settings` block round-trips.
	slotConfigNamesGet := func(w http.ResponseWriter, r *http.Request) {
		resourceID := siteResourceID(r)
		if !siteExists(resourceID) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Site %q not found.", sim.PathParam(r, "siteName"))
			return
		}
		var req struct {
			Properties SlotConfigNames `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Site %q not found.", sim.PathParam(r, "siteName"))
			return
		}
		var req struct {
			Properties SiteConfig `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		site.Properties.SiteConfig = &req.Properties
		sites.Put(resourceID, site)
		webRecordConfigSnapshot(resourceID, site.Properties.SiteConfig)
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

// azureFunctionInstance tracks the persistent App Service site container for a
// function app. On an always-on plan (the only plan the sim models for a
// container-image site) the platform keeps one container running for the life
// of the site; invokes route to it and it is torn down only when the site is
// deleted. The per-instance mutex serializes the lazy start so concurrent
// invokes to the same site share one container.
type azureFunctionInstance struct {
	mu             sync.Mutex
	containerID    string
	cancelLogs     context.CancelFunc
	sidecarHandles []*sim.ContainerHandle
	rawHandle      *sim.ContainerHandle // non-HTTP service container (e.g. a redis `services:` site)
	bootstrapURL   string               // "" for a raw service (no HTTP invoke)
	// dockerNetwork is the App Service VNet-integration network (sim-vnet-<vnet>)
	// the site joined, set by the swift virtualNetwork connection handler. The
	// site container attaches to it with its identity aliases so peers resolve it.
	dockerNetwork string
}

var azureFunctionInstances = struct {
	sync.Mutex
	bySite map[string]*azureFunctionInstance
}{bySite: map[string]*azureFunctionInstance{}}

// azfInstanceFor returns the (lazily created) instance holder for a site,
// creating an empty one on first reference. The returned holder's own mutex
// guards its container lifecycle.
func azfInstanceFor(siteName string) *azureFunctionInstance {
	azureFunctionInstances.Lock()
	defer azureFunctionInstances.Unlock()
	inst := azureFunctionInstances.bySite[siteName]
	if inst == nil {
		inst = &azureFunctionInstance{}
		azureFunctionInstances.bySite[siteName] = inst
	}
	return inst
}

func hasAzureFunctionHTTPBootstrap(site *Site) bool {
	if site == nil || site.Properties.SiteConfig == nil {
		return false
	}
	// A multi-container (sitecontainers) site always runs as a long-lived
	// main container with its sidecars sharing one network namespace.
	if mainSiteContainer(site.ID) != nil {
		return true
	}
	imageRef := site.Properties.SiteConfig.LinuxFxVersion
	if strings.Contains(imageRef, "/sockerless-overlay/") || strings.Contains(imageRef, "|sockerless-overlay/") {
		return true
	}
	for _, setting := range site.Properties.SiteConfig.AppSettings {
		switch setting.Name {
		case "SOCKERLESS_USER_ENTRYPOINT", "SOCKERLESS_USER_CMD":
			return true
		}
	}
	return false
}

func invokeAzureFunctionHTTP(site *Site, body io.Reader, contentType string) ([]byte, int, error) {
	if site == nil || site.Properties.SiteConfig == nil {
		return nil, -1, fmt.Errorf("site config is required")
	}

	// App Service runs the site's container persistently on an always-on plan:
	// start it on first invoke and keep it for the life of the site. Subsequent
	// invokes (gitlab-runner cycles create/start/exec/wait per stage against the
	// same site) reuse the one long-lived container — its in-container bootstrap
	// HTTP server handles repeated buffered invokes, and its reverse-agent stays
	// registered for docker exec. The container is torn down only when the site
	// is deleted (stopAzureFunctionInstance, from the DELETE handler).
	inst := azfInstanceFor(site.Name)
	inst.mu.Lock()
	if err := inst.ensureStarted(site); err != nil {
		inst.mu.Unlock()
		return nil, -1, err
	}
	bootstrapURL := inst.bootstrapURL
	inst.mu.Unlock()

	if bootstrapURL == "" {
		return nil, -1, fmt.Errorf("site %q runs a non-HTTP service container; it has no function bootstrap to invoke", site.Name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 230*time.Second)
	defer cancel()
	return postBootstrapWithRetry(ctx, bootstrapURL, body, contentType, 230*time.Second)
}

// ensureStarted starts the site's persistent container if it isn't already
// running, and (for a VNet-integrated site) ensures it's attached to its App
// Service network. Caller holds inst.mu. Used by both the HTTP invoke path and
// the swift VNet-integration handler (which starts a non-HTTP service such as a
// `services:` redis that is never invoked).
func (inst *azureFunctionInstance) ensureStarted(site *Site) error {
	if inst.containerID != "" && sim.ContainerRunning(inst.containerID) {
		if inst.dockerNetwork != "" {
			// Idempotent: a re-connect of an already-attached endpoint is a
			// harmless no-op error we ignore.
			_ = sim.ConnectContainerToNetwork(inst.containerID, inst.dockerNetwork, siteNetAliases(site))
		}
		return nil
	}
	if inst.containerID != "" {
		// A previously-started container died; reap before restarting.
		inst.teardownLocked()
	}
	return inst.startLocked(site)
}

// siteNetAliases are the identity names an App Service site is reachable by on
// its VNet-integration network: the site name and its default hostname. Service
// aliases (a `--network-alias`) are added on top via the Private DNS record the
// backend registers (realizeCNAMEAsSiteDockerAlias).
func siteNetAliases(site *Site) []string {
	var out []string
	if site.Name != "" {
		out = append(out, site.Name)
	}
	if site.Properties.DefaultHostName != "" {
		out = append(out, site.Properties.DefaultHostName)
	}
	return out
}

// startRawServiceLocked runs a non-bootstrap site image (a `services:` container
// such as redis) directly on its App Service VNet network, with the site's
// identity aliases, so peers reach it by name over that network. It serves its
// own port (e.g. redis 6379) container-to-container — there is no HTTP function
// bootstrap, so bootstrapURL stays "". Caller holds inst.mu.
func (inst *azureFunctionInstance) startRawServiceLocked(site *Site) error {
	image := siteContainerImage(site)
	if image == "" {
		return fmt.Errorf("site %q has no container image", site.Name)
	}
	localImage := sim.ResolveLocalImage(image)
	ctx, cancel := context.WithTimeout(context.Background(), 230*time.Second)
	defer cancel()
	platform, err := localImagePlatform(ctx, localImage)
	if err != nil {
		return err
	}
	sink := &funcLogSink{appName: site.Name}
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:          localImage,
		Architecture:   platform,
		Env:            mergeEnv(siteAppSettings(site), hostMetadataEnv()),
		Binds:          siteAzureStorageBinds(site),
		Name:           fmt.Sprintf("sockerless-sim-azure-svc-%s-%s", site.Name, randomSuffix(6)),
		Labels:         map[string]string{"sockerless-sim-type": "azure-service", "sockerless-site": site.Name},
		Network:        inst.dockerNetwork,
		NetworkAliases: siteNetAliases(site),
		Sandbox:        sim.SandboxAZF,
	}, sink)
	if err != nil {
		return fmt.Errorf("start service container: %w", err)
	}
	inst.containerID = handle.ContainerID
	inst.rawHandle = handle
	inst.bootstrapURL = ""
	return nil
}

// startLocked launches the persistent site container (plus any sidecar
// sitecontainers) and resolves the reachable in-container bootstrap URL,
// recording everything on the instance. Caller holds inst.mu.
func (inst *azureFunctionInstance) startLocked(site *Site) error {
	// A site whose image carries no sockerless function bootstrap (a `services:`
	// container such as redis) runs its own process directly on the VNet network
	// and is reached by peers over it — not via an HTTP invoke.
	if !hasAzureFunctionHTTPBootstrap(site) {
		return inst.startRawServiceLocked(site)
	}

	// Multi-container (sitecontainers) sites run the IsMain member as the
	// long-lived HTTP container; the LinuxFxVersion-derived image is the
	// single-container fallback.
	main := mainSiteContainer(site.ID)
	var (
		containerImage string
		mainCmd        []string
		mainEnv        map[string]string
		mainBinds      []string
	)
	if main != nil {
		containerImage = main.Properties.Image
		mainCmd = splitStartUpCommand(main.Properties.StartUpCommand)
		mainEnv = envVarsMap(main.Properties.EnvironmentVariables)
		mainBinds = siteContainerVolumeBinds(site.Name, main.Properties.VolumeMounts)
	} else {
		containerImage = siteContainerImage(site)
		// Single-container site: mount the site's Azure Files shares (attached
		// via WebApps.UpdateAzureStorageAccounts). A shared named volume like
		// gitlab-runner's /builds dir maps to one share, so every container that
		// mounts that volume sees the same workspace — the build container must
		// see what the helper container cloned into /builds.
		mainBinds = siteAzureStorageBinds(site)
	}
	if containerImage == "" {
		return fmt.Errorf("site %q has no container image", site.Name)
	}

	localImage := sim.ResolveLocalImage(containerImage)
	ctx, cancel := context.WithTimeout(context.Background(), 230*time.Second)
	defer cancel()

	platform, err := localImagePlatform(ctx, localImage)
	if err != nil {
		return err
	}
	hostPort, err := pickFreeTCPPort()
	if err != nil {
		return fmt.Errorf("pick free port: %w", err)
	}

	env := mergeEnv(map[string]string{
		"PORT":          "8080",
		"WEBSITES_PORT": "8080",
	}, siteAppSettings(site))
	env = mergeEnv(env, hostMetadataEnv())
	env = mergeEnv(env, mainEnv)
	sink := &funcLogSink{appName: site.Name}

	containerID, err := sim.StartHTTPContainer(ctx, sim.HTTPContainerConfig{
		Image:        localImage,
		Architecture: platform,
		HostPort:     hostPort,
		Env:          env,
		Cmd:          mainCmd,
		Binds:        mainBinds,
		Name:         fmt.Sprintf("sockerless-sim-azure-func-http-%s-%d", site.Name, hostPort),
		Labels: map[string]string{
			"sockerless-sim-type": "azure-function-http",
			"sockerless-site":     site.Name,
		},
		ExtraHosts: hostMetadataExtraHosts(),
		Sandbox:    sim.SandboxAZF,
	})
	if err != nil {
		return fmt.Errorf("start function http container: %w", err)
	}
	logCtx, cancelLogs := context.WithCancel(context.Background())

	// Sidecar sitecontainers share the main's network namespace, so a
	// sidecar that binds a port is reachable from the main on
	// localhost:<port> — the App Service multi-container loopback contract.
	sidecarHandles := startSidecarContainers(site, containerID, sink)
	go sim.StreamContainerLogs(logCtx, containerID, sink)

	// Reach the bootstrap by whichever address connects:
	//   - <containerIP>:8080 — works when the sim runs INSIDE a harness
	//     container (the host-published port binds the host's loopback, not the
	//     sim container's, so 127.0.0.1:hostPort is unreachable there);
	//   - 127.0.0.1:<hostPort> — works when the sim runs directly on the host.
	// Same reach the gcp sim's Cloud Run/Functions invoke and the ACA App
	// invoke use.
	var cands []string
	if ip := sim.ContainerIPv4(containerID); ip != "" {
		cands = append(cands, fmt.Sprintf("http://%s:8080/api/function", ip))
	}
	cands = append(cands, fmt.Sprintf("http://127.0.0.1:%d/api/function", hostPort))
	bootstrapURL, err := firstReachableHTTP(ctx, cands, 30*time.Second)
	if err != nil {
		// Failed to come up — reap the partial container set.
		cancelLogs()
		for _, h := range sidecarHandles {
			h.Cancel()
		}
		sim.StopAndRemoveContainer(containerID)
		return fmt.Errorf("bootstrap not ready (tried %d address(es)): %w", len(cands), err)
	}

	// VNet-integrated site: attach the (running) HTTP container to its App
	// Service network so peers resolve it by name. The container's :8080 is for
	// the function bootstrap; cross-site reachability is over this network.
	if inst.dockerNetwork != "" {
		_ = sim.ConnectContainerToNetwork(containerID, inst.dockerNetwork, siteNetAliases(site))
	}

	inst.containerID = containerID
	inst.cancelLogs = cancelLogs
	inst.sidecarHandles = sidecarHandles
	inst.bootstrapURL = bootstrapURL
	return nil
}

// teardownLocked stops the instance's main container, its sidecars, and its log
// stream, clearing the recorded handles. Caller holds inst.mu.
func (inst *azureFunctionInstance) teardownLocked() {
	for _, h := range inst.sidecarHandles {
		h.Cancel()
	}
	if inst.rawHandle != nil {
		inst.rawHandle.Cancel()
	}
	if inst.cancelLogs != nil {
		inst.cancelLogs()
	}
	if inst.containerID != "" {
		sim.StopAndRemoveContainer(inst.containerID)
	}
	inst.containerID = ""
	inst.cancelLogs = nil
	inst.sidecarHandles = nil
	inst.rawHandle = nil
	inst.bootstrapURL = ""
}

// firstReachableHTTP polls the candidate URLs (each round, in order) and
// returns the first whose host:port accepts a TCP connection within timeout.
func firstReachableHTTP(ctx context.Context, cands []string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		for _, cand := range cands {
			parsed, perr := url.Parse(cand)
			if perr != nil {
				lastErr = perr
				continue
			}
			host := parsed.Host
			if _, _, splitErr := net.SplitHostPort(host); splitErr != nil {
				switch parsed.Scheme {
				case "https":
					host = net.JoinHostPort(host, "443")
				default:
					host = net.JoinHostPort(host, "80")
				}
			}
			conn, derr := net.DialTimeout("tcp", host, time.Second)
			if derr == nil {
				_ = conn.Close()
				return cand, nil
			}
			lastErr = derr
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timeout after %s", timeout)
	}
	return "", lastErr
}

func stopAzureFunctionInstance(siteName string) {
	azureFunctionInstances.Lock()
	inst := azureFunctionInstances.bySite[siteName]
	delete(azureFunctionInstances.bySite, siteName)
	azureFunctionInstances.Unlock()
	if inst == nil {
		return
	}
	inst.mu.Lock()
	inst.teardownLocked()
	inst.mu.Unlock()
}

func siteContainerImage(site *Site) string {
	if site == nil || site.Properties.SiteConfig == nil {
		return ""
	}
	parts := strings.SplitN(site.Properties.SiteConfig.LinuxFxVersion, "|", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return ""
}

func siteAppSettings(site *Site) map[string]string {
	out := map[string]string{}
	if site == nil || site.Properties.SiteConfig == nil {
		return out
	}
	for _, s := range site.Properties.SiteConfig.AppSettings {
		out[s.Name] = s.Value
	}
	return out
}

func localImagePlatform(ctx context.Context, imageRef string) (string, error) {
	cli := sim.DockerClient()
	if cli == nil {
		return "", fmt.Errorf("docker client not initialized")
	}
	inspect, err := cli.ImageInspect(ctx, imageRef)
	if err != nil {
		rc, pullErr := cli.ImagePull(ctx, imageRef, dockerclient.ImagePullOptions{})
		if pullErr != nil {
			return "", fmt.Errorf("inspect image %q platform: %w; pull image: %w", imageRef, err, pullErr)
		}
		if _, copyErr := io.Copy(io.Discard, rc); copyErr != nil {
			_ = rc.Close()
			return "", fmt.Errorf("pull image %q: %w", imageRef, copyErr)
		}
		if closeErr := rc.Close(); closeErr != nil {
			return "", fmt.Errorf("close image pull stream %q: %w", imageRef, closeErr)
		}
		inspect, err = cli.ImageInspect(ctx, imageRef)
		if err != nil {
			return "", fmt.Errorf("inspect pulled image %q platform: %w", imageRef, err)
		}
	}
	if inspect.Os == "" || inspect.Architecture == "" {
		return "", fmt.Errorf("inspect image %q platform: missing os/architecture", imageRef)
	}
	return inspect.Os + "/" + inspect.Architecture, nil
}

func pickFreeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		_ = l.Close()
		return 0, fmt.Errorf("listener address is not a *net.TCPAddr: %T", l.Addr())
	}
	port := addr.Port
	_ = l.Close()
	return port, nil
}

func postBootstrapWithRetry(ctx context.Context, bootstrapURL string, body io.Reader, contentType string, timeout time.Duration) ([]byte, int, error) {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, -1, fmt.Errorf("read invoke body: %w", err)
		}
	}
	if contentType == "" {
		contentType = "application/json"
	}
	httpClient := &http.Client{Timeout: timeout}
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, bootstrapURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, -1, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", contentType)
		resp, err := httpClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			respBytes, _ := io.ReadAll(resp.Body)
			return respBytes, bootstrapExitCode(resp), nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, -1, fmt.Errorf("invoke bootstrap: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, -1, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func bootstrapExitCode(resp *http.Response) int {
	if hdr := resp.Header.Get("X-Sockerless-Exit-Code"); hdr != "" {
		if n, err := strconv.Atoi(hdr); err == nil {
			return n
		}
	}
	if resp.StatusCode >= 400 {
		return 1
	}
	return 0
}

// invokeAzureFunctionProcess executes a function app's container via sim.StartContainerSync
// and returns the stdout output as the response body plus the process exit code.
func invokeAzureFunctionProcess(site *Site) ([]byte, int) {
	var entrypoint, cmd []string
	if site.Properties.SiteConfig != nil {
		// Cloud-native: read SOCKERLESS_ENTRYPOINT + SOCKERLESS_CMD
		// separately so docker's ENTRYPOINT vs CMD semantics are preserved.
		for _, s := range site.Properties.SiteConfig.AppSettings {
			switch s.Name {
			case "SOCKERLESS_ENTRYPOINT":
				decoded, err := base64.StdEncoding.DecodeString(s.Value)
				if err != nil {
					msg := fmt.Sprintf("invalid SOCKERLESS_ENTRYPOINT base64: %v", err)
					return []byte(msg), 1
				}
				if err := json.Unmarshal(decoded, &entrypoint); err != nil {
					msg := fmt.Sprintf("invalid SOCKERLESS_ENTRYPOINT JSON: %v", err)
					return []byte(msg), 1
				}
			case "SOCKERLESS_CMD":
				decoded, err := base64.StdEncoding.DecodeString(s.Value)
				if err != nil {
					msg := fmt.Sprintf("invalid SOCKERLESS_CMD base64: %v", err)
					return []byte(msg), 1
				}
				if err := json.Unmarshal(decoded, &cmd); err != nil {
					msg := fmt.Sprintf("invalid SOCKERLESS_CMD JSON: %v", err)
					return []byte(msg), 1
				}
			}
		}
	}
	if len(entrypoint) == 0 && len(cmd) == 0 {
		return []byte("{}"), 0
	}

	// Derive container image from LinuxFxVersion (e.g., "DOCKER|myimage:latest")
	var containerImage string
	if site.Properties.SiteConfig != nil && site.Properties.SiteConfig.LinuxFxVersion != "" {
		parts := strings.SplitN(site.Properties.SiteConfig.LinuxFxVersion, "|", 2)
		if len(parts) == 2 {
			containerImage = parts[1]
		}
	}
	if containerImage == "" {
		// No container image configured — cannot run
		return []byte("{}"), 0
	}

	// Extract environment from app settings
	var cmdEnv map[string]string
	if site.Properties.SiteConfig != nil && len(site.Properties.SiteConfig.AppSettings) > 0 {
		cmdEnv = make(map[string]string, len(site.Properties.SiteConfig.AppSettings))
		for _, s := range site.Properties.SiteConfig.AppSettings {
			cmdEnv[s.Name] = s.Value
		}
	}

	timeout := 230 * time.Second // Azure Functions default timeout
	sink := &funcLogSink{appName: site.Name}
	var stdout bytes.Buffer
	collectSink := sim.FuncSink(func(line sim.LogLine) {
		sink.WriteLog(line)
		if line.Stream == "stdout" {
			stdout.WriteString(line.Text)
			stdout.WriteByte('\n')
		}
	})

	containerName := fmt.Sprintf("sockerless-sim-azure-func-%s-%d", site.Name, time.Now().UnixNano())
	localImage := sim.ResolveLocalImage(containerImage)
	platform, err := localImagePlatform(context.Background(), localImage)
	if err != nil {
		injectAppTrace(site.Name,
			fmt.Sprintf("Function execution error: resolve image platform failed: %v", err))
		return []byte("{}"), -1
	}

	// Host metadata: route IMDS + identity reads via env.
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        localImage,
		Architecture: platform,
		Command:      entrypoint,
		Args:         cmd,
		Env:          mergeEnv(cmdEnv, hostMetadataEnv()),
		Timeout:      timeout,
		Name:         containerName,
		Labels: map[string]string{
			"sockerless-sim-type": "azure-function-invocation",
			"sockerless-site":     site.Name,
		},
		ExtraHosts: hostMetadataExtraHosts(),
		Sandbox:    sim.SandboxAZF,
	}, collectSink)
	if err != nil {
		injectAppTrace(site.Name,
			fmt.Sprintf("Function execution error: container start failed: %v", err))
		return []byte("{}"), -1
	}
	result := handle.Wait()

	if result.ExitCode != 0 {
		injectAppTrace(site.Name,
			fmt.Sprintf("Function execution error: process exited with code %d", result.ExitCode))
	}

	output := strings.TrimRight(stdout.String(), "\n")
	if output == "" {
		return []byte("{}"), result.ExitCode
	}
	return []byte(output), result.ExitCode
}

// funcLogSink implements sim.LogSink and writes log lines to AppTraces
// for Azure Function invocations.
type funcLogSink struct {
	appName string
}

func (s *funcLogSink) WriteLog(line sim.LogLine) {
	injectAppTrace(s.appName, line.Text)
}
