package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
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
	State            string   `json:"state,omitempty"`
	DefaultHostName  string   `json:"defaultHostName,omitempty"`
	HostNames        []string `json:"hostNames,omitempty"`
	Enabled          bool     `json:"enabled"`
	EnabledHostNames []string `json:"enabledHostNames,omitempty"`
	// HostNameSslStates names the site's hostnames by role: the Standard host
	// requests reach and the Repository (SCM) host deployments go to.
	HostNameSslStates []HostNameSslState `json:"hostNameSslStates,omitempty"`
	ServerFarmID      string             `json:"serverFarmId,omitempty"`
	SKU               string             `json:"sku,omitempty"`
	Reserved          bool               `json:"reserved,omitempty"`
	SiteConfig        *SiteConfig        `json:"siteConfig,omitempty"`
	ResourceGroup     string             `json:"resourceGroup,omitempty"`
	LastModifiedTime  string             `json:"lastModifiedTimeUtc,omitempty"`
	HTTPSOnly         bool               `json:"httpsOnly,omitempty"`
	ClientCertMode    string             `json:"clientCertMode,omitempty"`
	// VirtualNetworkSubnetID is the modern spelling of regional VNet
	// integration (`az webapp vnet-integration add`, terraform's
	// virtual_network_subnet_id): writing it joins the site to the subnet's
	// VNet exactly as a swift networkConfig/virtualNetwork PUT does, and it
	// always reflects the site's current regional integration.
	VirtualNetworkSubnetID string `json:"virtualNetworkSubnetId,omitempty"`
	// HostingEnvironmentProfile places the site directly in an App Service
	// Environment. A site normally inherits the environment from its App
	// Service plan; either way the environment's own app list reads it back.
	HostingEnvironmentProfile *HostingEnvironmentProfile `json:"hostingEnvironmentProfile,omitempty"`
}

// SiteConfig holds the site configuration for a function app.
type SiteConfig struct {
	AppSettings    []NameValuePair `json:"appSettings,omitempty"`
	LinuxFxVersion string          `json:"linuxFxVersion,omitempty"`
	// AppCommandLine is the site's startup command (`az functionapp config set
	// --startup-file`, terraform's app_command_line). The platform runs it as
	// the container's command, after the image's own entrypoint.
	AppCommandLine string `json:"appCommandLine,omitempty"`
	AlwaysOn       bool   `json:"alwaysOn,omitempty"`
	// AcrUseManagedIdentityCreds asks the platform to pull the site's image
	// from Azure Container Registry with a managed identity: the
	// user-assigned identity AcrUserManagedIdentityID names by client id,
	// else the site's system-assigned identity.
	AcrUseManagedIdentityCreds             bool   `json:"acrUseManagedIdentityCreds,omitempty"`
	AcrUserManagedIdentityID               string `json:"acrUserManagedIdentityID,omitempty"`
	FunctionAppScaleLimit                  int    `json:"functionAppScaleLimit,omitempty"`
	FtpsState                              string `json:"ftpsState,omitempty"`
	LoadBalancing                          string `json:"loadBalancing,omitempty"`
	ManagedPipelineMode                    string `json:"managedPipelineMode,omitempty"`
	IPSecurityRestrictionsDefaultAction    string `json:"ipSecurityRestrictionsDefaultAction,omitempty"`
	MinTLSVersion                          string `json:"minTlsVersion,omitempty"`
	ScmMinTLSVersion                       string `json:"scmMinTlsVersion,omitempty"`
	ScmIPSecurityRestrictionsDefaultAction string `json:"scmIpSecurityRestrictionsDefaultAction,omitempty"`
	// Extra holds the siteConfig properties the simulator does not act on,
	// exactly as the client sent them, so a read returns what was written.
	Extra map[string]json.RawMessage `json:"-"`
}

// siteConfigFields names the properties SiteConfig decodes into fields.
var siteConfigFields = func() map[string]bool {
	names := map[string]bool{}
	t := reflect.TypeOf(SiteConfig{})
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names[name] = true
		}
	}
	return names
}()

// UnmarshalJSON decodes onto the receiver, so a PATCH decoded onto the stored
// configuration keeps every property the request leaves out.
func (c *SiteConfig) UnmarshalJSON(data []byte) error {
	type fields SiteConfig
	if err := json.Unmarshal(data, (*fields)(c)); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	// A PATCH decodes onto a copy of the stored row, which shares this map.
	c.Extra = maps.Clone(c.Extra)
	for name, value := range all {
		if siteConfigFields[name] {
			continue
		}
		if c.Extra == nil {
			c.Extra = map[string]json.RawMessage{}
		}
		c.Extra[name] = value
	}
	return nil
}

func (c SiteConfig) MarshalJSON() ([]byte, error) {
	type fields SiteConfig
	known, err := json.Marshal(fields(c))
	if err != nil || len(c.Extra) == 0 {
		return known, err
	}
	all := map[string]json.RawMessage{}
	if err := json.Unmarshal(known, &all); err != nil {
		return nil, err
	}
	for name, value := range c.Extra {
		all[name] = value
	}
	return json.Marshal(all)
}

// HostNameSslState mirrors armappservice.HostNameSSLState.
type HostNameSslState struct {
	Name     string `json:"name"`
	SslState string `json:"sslState"`
	HostType string `json:"hostType"`
}

// siteHostNameSslStates lists a site's default and SCM hostnames, neither
// bound to a certificate.
func siteHostNameSslStates(defaultHost, scmHost string) []HostNameSslState {
	return []HostNameSslState{
		{Name: defaultHost, SslState: "Disabled", HostType: "Standard"},
		{Name: scmHost, SslState: "Disabled", HostType: "Repository"},
	}
}

// NameValuePair holds a name-value pair for app settings.
type NameValuePair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// nameValuePairs renders a settings dictionary as the siteConfig list, ordered
// by name.
func nameValuePairs(settings map[string]string) []NameValuePair {
	names := make([]string, 0, len(settings))
	for name := range settings {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]NameValuePair, 0, len(names))
	for _, name := range names {
		out = append(out, NameValuePair{Name: name, Value: settings[name]})
	}
	return out
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
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Location == "" {
			AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)

		// The resource provider takes a plan in the site's own resource group
		// by name, as az functionapp create names it, and records its ID.
		if plan := req.Properties.ServerFarmID; plan != "" && !strings.Contains(plan, "/") {
			req.Properties.ServerFarmID = fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/%s", sub, rg, plan)
		}

		// A site placed in an App Service Environment is placed in one that
		// exists; the resource provider refuses an unresolvable reference.
		hostingEnvironment, err := webResolveHostingEnvironmentProfile(req.Properties.HostingEnvironmentProfile)
		if err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}

		kind := req.Kind
		if kind == "" {
			kind = webDefaultSiteKind(req.Properties.Reserved, req.Properties.ServerFarmID)
		}

		// Real Azure assigns a per-site hostname `<site>.azurewebsites.net`
		// — invocations route to the right function app by HTTP Host header.
		// The sim hosts every site on a single port, so callers connect to
		// the sim's TCP address but set Host = `<name>.azurewebsites.net`;
		// the invoke handler matches that against DefaultHostName.
		defaultHostName := name + ".azurewebsites.net"
		scmHostName := azureAppServiceScmHost(r, name)

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
				State:                     "Running",
				DefaultHostName:           defaultHostName,
				HostNames:                 []string{defaultHostName},
				Enabled:                   true,
				EnabledHostNames:          []string{defaultHostName, scmHostName},
				HostNameSslStates:         siteHostNameSslStates(defaultHostName, scmHostName),
				ServerFarmID:              req.Properties.ServerFarmID,
				SKU:                       webPlanSKUFor(req.Properties.ServerFarmID),
				Reserved:                  req.Properties.Reserved,
				SiteConfig:                siteConfig,
				ResourceGroup:             rg,
				LastModifiedTime:          time.Now().UTC().Format(time.RFC3339),
				HTTPSOnly:                 req.Properties.HTTPSOnly,
				ClientCertMode:            clientCertMode,
				HostingEnvironmentProfile: hostingEnvironment,
			},
		}

		_, existed := sites.Get(resourceID)
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

		// virtualNetworkSubnetId on the envelope is regional VNet integration:
		// join the subnet's VNet exactly as a swift PUT would. A PUT that
		// omits it keeps an existing integration (the swift-connection flow
		// re-PUTs the site without it); the stored row still reflects the
		// current integration either way.
		if req.Properties.VirtualNetworkSubnetID != "" {
			if err := applySiteVirtualNetworkSubnetID(r, site, req.Properties.VirtualNetworkSubnetID); err != nil {
				AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
					"failed to integrate site %q into VNet: %v", name, err)
				return
			}
		} else {
			syncSiteVnetSubnetProperty(r)
		}
		site, _ = sites.Get(resourceID)
		if existed {
			restartAzureFunctionInstance(site)
		} else {
			startAlwaysOnSite(site)
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

		deleted, existed := sites.Get(resourceID)
		if sites.Delete(resourceID) {
			stopAzureFunctionInstance(name)
			cleanupSiteContainers(resourceID, name)
			if existed {
				// Retain the app so RestoreFromDeletedApp and the deletedSites
				// reads can reach it, before its content is cleaned up.
				webRecordDeletedSite(resourceID, deleted)
			}
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

		site, ok := sites.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": siteFunctions(&site),
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

		siteID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, siteName)
		fn, ok := functionConfigs.Get(funcID)
		if site, isSite := sites.Get(siteID); isSite && !ok {
			fn, ok = siteFunction(&site, funcName)
		}
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The function '%s' in site '%s' was not found.", funcName, siteName)
			return
		}

		sim.WriteJSON(w, http.StatusOK, fn)
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
			resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)
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
	// request tracing). The sim doesn't model log retention; truthful
	// default response is every category disabled.
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/config/logs", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)
		if _, ok := sites.Get(resourceID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)
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
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s", sub, rg, name)
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

	registerSiteConfigHandlers(srv, armBase, sites)
	registerAppServiceFrontEnd(srv)
	registerAppServiceKudu(srv)
	registerSiteContainerHandlers(srv, armBase)
	registerSiteVNetIntegration(srv)
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

// azureFunctionInstance tracks the container App Service runs for a site. The
// platform keeps one container per site instance: a request to the site starts
// it when none is running, Always On starts it without one, and a restart, a
// configuration change or the site's deletion tears it down. The per-instance
// mutex serializes the start so concurrent requests share one container.
type azureFunctionInstance struct {
	mu             sync.Mutex
	containerID    string
	cancelLogs     context.CancelFunc
	sidecarHandles []*sim.ContainerHandle
	// exited closes when the main container stops running; logsDone closes
	// once its output has been read to the end.
	exited   <-chan struct{}
	logsDone <-chan struct{}
	stopWait context.CancelFunc
	// port is the container port the front end forwards the site's requests
	// to; candidates are the addresses that port may be reached at, and
	// address is the one that answered.
	port       int
	candidates []string
	address    string
	// stopGrace is the WEBSITES_CONTAINER_STOP_TIME_LIMIT the containers were
	// started under, applied when they are torn down.
	stopGrace time.Duration
	// dockerNetworks are the App Service VNet-integration networks
	// (sim-vnet-<vnet>) the site joined, set by the virtualNetwork connection
	// handlers (both the swift and the classic spelling). The site's containers
	// attach to every one with the site's identity aliases so peers resolve it.
	// They outlive a restart, as the integration does.
	dockerNetworks []string
}

// addNetworkLocked records a VNet-integration network on the instance.
// Caller holds inst.mu.
func (inst *azureFunctionInstance) addNetworkLocked(network string) {
	for _, n := range inst.dockerNetworks {
		if n == network {
			return
		}
	}
	inst.dockerNetworks = append(inst.dockerNetworks, network)
}

// removeNetworkLocked forgets a VNet-integration network. Caller holds inst.mu.
func (inst *azureFunctionInstance) removeNetworkLocked(network string) {
	kept := inst.dockerNetworks[:0]
	for _, n := range inst.dockerNetworks {
		if n != network {
			kept = append(kept, n)
		}
	}
	inst.dockerNetworks = kept
}

// connectNetworksLocked attaches the instance's live container to every
// recorded VNet-integration network with the site's identity aliases.
// Re-connecting an already-attached endpoint is a harmless no-op error.
// Caller holds inst.mu.
func (inst *azureFunctionInstance) connectNetworksLocked(containerID string, site *Site) {
	for _, network := range inst.dockerNetworks {
		_ = sim.ConnectContainerToNetwork(containerID, network, siteNetAliases(site))
	}
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

// siteRunsContainer reports whether App Service runs the site as a container
// this simulator can start: a sitecontainers site, a linuxFxVersion naming an
// image, or a site on a built-in stack this App Service runs.
func siteRunsContainer(site *Site) bool {
	if mainSiteContainer(site.ID) != nil || siteContainerImage(site) != "" {
		return true
	}
	_, ok := sitePlatformImage(site)
	return ok
}

// siteAlwaysOn reports the site's Always On setting: the platform keeps such a
// site's container running without waiting for a request to start it.
func siteAlwaysOn(site *Site) bool {
	return site != nil && site.Properties.SiteConfig != nil && site.Properties.SiteConfig.AlwaysOn
}

// azureFunctionsHTTPRequestLimit is how long an HTTP-triggered function may
// take to answer: "Regardless of the function app timeout setting, 230 seconds
// is the maximum amount of time that an HTTP triggered function can take to
// respond to a request."
const azureFunctionsHTTPRequestLimit = 230 * time.Second

// siteContainerPort is the port App Service forwards a single-container site's
// requests to: the WEBSITES_PORT app setting, else 80.
func siteContainerPort(site *Site) int {
	if port, err := strconv.Atoi(strings.TrimSpace(siteAppSettings(site)["WEBSITES_PORT"])); err == nil && port > 0 && port < 65536 {
		return port
	}
	return 80
}

// siteStartTimeLimit is how long App Service waits for a site's container to
// answer on its port before it fails the start: the
// WEBSITES_CONTAINER_START_TIME_LIMIT app setting in seconds, 230 when the site
// sets none, and at most the 1800 the platform accepts.
func siteStartTimeLimit(site *Site) time.Duration {
	const defaultLimit, maxLimit = 230, 1800
	raw := strings.TrimSpace(siteAppSettings(site)["WEBSITES_CONTAINER_START_TIME_LIMIT"])
	seconds, err := strconv.Atoi(raw)
	if raw == "" || err != nil || seconds <= 0 {
		return defaultLimit * time.Second
	}
	if seconds > maxLimit {
		seconds = maxLimit
	}
	return time.Duration(seconds) * time.Second
}

// ensureStarted starts the site's container if none is running, and attaches
// a running one to the site's VNet-integration networks. Caller holds inst.mu.
func (inst *azureFunctionInstance) ensureStarted(site *Site) error {
	if inst.containerID != "" {
		select {
		case <-inst.exited:
			inst.teardownLocked()
		default:
			inst.connectNetworksLocked(inst.containerID, site)
			return nil
		}
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

// siteImageMissing explains why the simulator has nothing to run for a site.
func siteImageMissing(site *Site) error {
	stack := siteRuntimeStack(site)
	switch {
	case siteIsFunctionApp(site) && stack != "":
		return fmt.Errorf(
			"function app %q is configured with the built-in runtime stack %q, which this simulator "+
				"does not run the Azure Functions host for; the stacks it runs are %s, or configure a "+
				"container image (linuxFxVersion \"DOCKER|<image>\")",
			site.Name, stack, strings.Join(functionsHostStackNames(), ", "))
	case siteIsFunctionApp(site):
		return fmt.Errorf(
			"function app %q names no runtime: configure a built-in stack (%s) or a container image "+
				"(linuxFxVersion \"DOCKER|<image>\")", site.Name, strings.Join(functionsHostStackNames(), ", "))
	case stack != "":
		return fmt.Errorf(
			"site %q is configured with the built-in runtime stack %q, which this simulator does not "+
				"run; the stacks it runs are %s", site.Name, stack, strings.Join(appServiceLinuxStackNames(), ", "))
	}
	return fmt.Errorf(
		"site %q names no runtime: configure a built-in stack (%s) or a container image "+
			"(linuxFxVersion \"DOCKER|<image>\")", site.Name, strings.Join(appServiceLinuxStackNames(), ", "))
}

// startLocked runs the site's container the way App Service runs a Linux
// custom container: the image's own entrypoint, with the site's startup command
// (siteConfig.appCommandLine, or a sitecontainer's startUpCommand) as the
// container command in place of the image's CMD, the app settings and PORT in
// its environment, and any sidecar sitecontainers in its network namespace.
// Caller holds inst.mu.
func (inst *azureFunctionInstance) startLocked(site *Site) error {
	main := mainSiteContainer(site.ID)
	var (
		image        string
		args         []string
		containerEnv map[string]string
		binds        []string
	)
	port := siteContainerPort(site)
	if main != nil {
		image = main.Properties.Image
		args = splitStartUpCommand(main.Properties.StartUpCommand)
		containerEnv = envVarsMap(main.Properties.EnvironmentVariables)
		binds = siteContainerVolumeBinds(site.Name, main.Properties.VolumeMounts)
		if p, err := strconv.Atoi(strings.TrimSpace(main.Properties.TargetPort)); err == nil && p > 0 && p < 65536 {
			port = p
		}
	} else if stack, ok := siteBuiltInStack(site); ok {
		// The stack image's entrypoint takes the startup command as its
		// arguments and hands it to Oryx as the user startup command.
		image = stack.Image
		if site.Properties.SiteConfig != nil {
			if cmd := strings.TrimSpace(site.Properties.SiteConfig.AppCommandLine); cmd != "" {
				args = []string{cmd}
			}
		}
		if _, set := siteAppSettings(site)["WEBSITES_PORT"]; !set {
			port = stack.Port
		}
		home, err := prepareSiteHome(site)
		if err != nil {
			return err
		}
		binds = append(home, siteAzureStorageBinds(site)...)
	} else if stack, ok := siteFunctionsHostStack(site); ok {
		// The host image's entrypoint starts the Functions host on PORT; App
		// Service runs it without a startup command.
		image = stack.Image
		home, err := prepareSiteHome(site)
		if err != nil {
			return err
		}
		if err := syncFunctionsHostSecrets(site); err != nil {
			return err
		}
		binds = append(home, siteAzureStorageBinds(site)...)
	} else {
		image = siteContainerImage(site)
		if site.Properties.SiteConfig != nil {
			args = splitStartUpCommand(site.Properties.SiteConfig.AppCommandLine)
		}
		// A shared named volume like gitlab-runner's /builds dir maps to one
		// Azure Files share, so every container that mounts it sees the same
		// workspace.
		binds = siteAzureStorageBinds(site)
	}
	if image == "" {
		return siteImageMissing(site)
	}

	localImage := sim.ResolveLocalImage(image)
	ctx, cancel := context.WithTimeout(context.Background(), siteStartTimeLimit(site))
	defer cancel()

	// The host pulls the site's image with the credential the site declared
	// for its registry — its Azure Container Registry managed identity or
	// its DOCKER_REGISTRY_SERVER_* settings — as App Service does.
	registryAuth := acrWorkloadRegistryAuth(image, siteWorkloadRegistries(site, image))
	platform, err := workload.LocalImagePlatform(ctx, localImage, registryAuth)
	if err != nil {
		return err
	}
	metadataEnv, err := hostMetadataEnv()
	if err != nil {
		return err
	}
	env := workloadhost.MergeEnv(siteAppSettings(site), appServicePlatformEnv(site), map[string]string{"PORT": strconv.Itoa(port)}, metadataEnv, containerEnv)
	sink := newFuncLogSink(site)

	containerID, err := sim.StartHTTPContainer(ctx, sim.HTTPContainerConfig{
		Image:        localImage,
		Architecture: platform,
		Env:          env,
		Args:         args,
		Binds:        binds,
		Name:         fmt.Sprintf("sockerless-sim-azure-site-%s-%s", site.Name, sim.RandomHex(8)),
		Labels: map[string]string{
			"sockerless-sim-type": "azure-site",
			"sockerless-site":     site.Name,
		},
		ExtraHosts: workloadhost.ExtraHosts(),
		Sandbox:    SandboxAZF,
	})
	if err != nil {
		return fmt.Errorf("start site container: %w", err)
	}
	exited, stopWait := watchContainerExit(containerID)
	logCtx, cancelLogs := context.WithCancel(context.Background())
	logsDone := make(chan struct{})
	go func() {
		defer close(logsDone)
		sim.StreamContainerLogs(logCtx, containerID, sink)
	}()

	// Sidecar sitecontainers share the main's network namespace, so a
	// sidecar that binds a port is reachable from the main on
	// localhost:<port> — the App Service multi-container loopback contract.
	sidecarHandles, err := startSidecarContainers(ctx, site, containerID, sink)
	if err != nil {
		stopWait()
		cancelLogs()
		sim.StopAndRemoveContainer(containerID, siteStopGrace(site))
		return fmt.Errorf("start sitecontainers: %w", err)
	}

	// The container's bridge address reaches the port from the host and from a
	// harness container alike; the engine publishes 8080 on the host's
	// loopback, which also reaches it where bridge addresses are not routed.
	var candidates []string
	if ip := sim.ContainerIPv4(containerID); ip != "" {
		candidates = append(candidates, "http://"+net.JoinHostPort(ip, strconv.Itoa(port)))
	}
	if port == 8080 {
		if hostPort, err := sim.PublishedHostPort(ctx, containerID, 8080); err == nil {
			candidates = append(candidates, fmt.Sprintf("http://127.0.0.1:%d", hostPort))
		}
	}

	inst.connectNetworksLocked(containerID, site)

	inst.containerID = containerID
	inst.cancelLogs = cancelLogs
	inst.sidecarHandles = sidecarHandles
	inst.exited = exited
	inst.logsDone = logsDone
	inst.stopWait = stopWait
	inst.port = port
	inst.candidates = candidates
	inst.address = ""
	inst.stopGrace = siteStopGrace(site)
	return nil
}

// watchContainerExit returns a channel the engine's own wait closes when the
// container stops running, and the function that releases the wait.
func watchContainerExit(containerID string) (<-chan struct{}, context.CancelFunc) {
	exited := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cli := sim.DockerClient()
	if cli == nil {
		close(exited)
		return exited, cancel
	}
	wait := cli.ContainerWait(ctx, containerID, mobyclient.ContainerWaitOptions{Condition: mobycontainer.WaitConditionNotRunning})
	go func() {
		select {
		case <-wait.Result:
			close(exited)
		case err := <-wait.Error:
			if ctx.Err() == nil && err != nil {
				close(exited)
			}
		case <-ctx.Done():
		}
	}()
	return exited, cancel
}

// teardownLocked stops the instance's main container, its sidecars, and its log
// stream, clearing the recorded handles. The VNet-integration networks stay
// recorded: the integration belongs to the site, not to one container. Caller
// holds inst.mu.
func (inst *azureFunctionInstance) teardownLocked() {
	for _, h := range inst.sidecarHandles {
		h.Cancel()
	}
	if inst.stopWait != nil {
		inst.stopWait()
	}
	if inst.cancelLogs != nil {
		inst.cancelLogs()
	}
	if inst.containerID != "" {
		sim.StopAndRemoveContainer(inst.containerID, inst.stopGrace)
	}
	inst.containerID = ""
	inst.cancelLogs = nil
	inst.sidecarHandles = nil
	inst.exited = nil
	inst.logsDone = nil
	inst.stopWait = nil
	inst.port = 0
	inst.candidates = nil
	inst.address = ""
}

// stopAzureFunctionInstance tears a deleted site's container down and forgets
// the site.
func stopAzureFunctionInstance(siteName string) {
	azureFunctionInstances.Lock()
	inst := azureFunctionInstances.bySite[siteName]
	delete(azureFunctionInstances.bySite, siteName)
	azureFunctionInstances.Unlock()
	if inst != nil {
		inst.mu.Lock()
		inst.teardownLocked()
		inst.mu.Unlock()
	}
	removeSiteHome(siteName)
}

// restartAzureFunctionInstance restarts a site the way App Service does after a
// restart request or a configuration change: the running container stops, and
// the next one starts from the site's current configuration — at once for an
// Always On site, otherwise on the next request.
func restartAzureFunctionInstance(site Site) {
	inst := azfInstanceFor(site.Name)
	inst.mu.Lock()
	inst.teardownLocked()
	inst.mu.Unlock()
	startAlwaysOnSite(site)
}

// startAlwaysOnSite starts an Always On site's container in the background, as
// the platform does once the site is created or changed; a start that fails is
// recorded in the site's log, and the next request retries it.
func startAlwaysOnSite(site Site) {
	if !siteAlwaysOn(&site) || !siteRunsContainer(&site) {
		return
	}
	go func() {
		inst := azfInstanceFor(site.Name)
		inst.mu.Lock()
		defer inst.mu.Unlock()
		// The site may have been deleted or changed while the start waited.
		current, ok := azfSites.Get(site.ID)
		if !ok || !siteAlwaysOn(&current) || !siteRunsContainer(&current) {
			return
		}
		if err := inst.ensureStarted(&current); err != nil {
			injectSiteTrace(&current, fmt.Sprintf("Site start failed: %v", err))
		}
	}()
}

// siteContainerImage is the custom container image a site runs, read from its
// linuxFxVersion.
//
// The field names two different things depending on its prefix. "DOCKER|" and
// the other container prefixes name an image; a built-in runtime stack —
// "PHP|8.2", "NODE|20-lts", "DOTNETCORE|8.0" — names a version of a platform
// image App Service supplies, which siteBuiltInStack resolves.
func siteContainerImage(site *Site) string {
	if site == nil || site.Properties.SiteConfig == nil {
		return ""
	}
	prefix, value, found := strings.Cut(site.Properties.SiteConfig.LinuxFxVersion, "|")
	if !found {
		return ""
	}
	if !siteLinuxFxNamesAnImage(prefix) {
		return ""
	}
	return value
}

// siteLinuxFxNamesAnImage reports whether a linuxFxVersion prefix introduces a
// container image rather than a built-in runtime stack.
func siteLinuxFxNamesAnImage(prefix string) bool {
	switch strings.ToUpper(strings.TrimSpace(prefix)) {
	case "DOCKER", "COMPOSE", "KUBE":
		return true
	}
	return false
}

// siteRuntimeStack is the built-in runtime stack a site's linuxFxVersion
// names, or empty when it names a container image or nothing.
func siteRuntimeStack(site *Site) string {
	if site == nil || site.Properties.SiteConfig == nil {
		return ""
	}
	prefix, _, found := strings.Cut(site.Properties.SiteConfig.LinuxFxVersion, "|")
	if !found || siteLinuxFxNamesAnImage(prefix) {
		return ""
	}
	return site.Properties.SiteConfig.LinuxFxVersion
}

// siteStopGrace is how long App Service waits for a site's container to exit
// after SIGTERM before killing it: the WEBSITES_CONTAINER_STOP_TIME_LIMIT app
// setting in seconds, five when the site sets none, and at most the 120 the
// platform accepts.
func siteStopGrace(site *Site) time.Duration {
	const defaultLimit, maxLimit = 5, 120
	raw := strings.TrimSpace(siteAppSettings(site)["WEBSITES_CONTAINER_STOP_TIME_LIMIT"])
	seconds, err := strconv.Atoi(raw)
	if raw == "" || err != nil || seconds < 0 {
		return defaultLimit * time.Second
	}
	if seconds > maxLimit {
		seconds = maxLimit
	}
	return time.Duration(seconds) * time.Second
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
