package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
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
	Identity             *SiteIdentity                     `json:"identity,omitempty"`
	Properties           SiteProperties                    `json:"properties"`
	AzureStorageAccounts map[string]*AzureStorageInfoValue `json:"-"`
	// SystemIdentityClientID is the application ID of the site's
	// system-assigned identity, which the directory reads and the site's
	// identity wire shape does not carry.
	SystemIdentityClientID string `json:"-"`
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
	ServerFarmID      string             `json:"serverFarmId"`
	SKU               string             `json:"sku"`
	Reserved          bool               `json:"reserved,omitempty"`
	SiteConfig        *SiteConfig        `json:"siteConfig,omitempty"`
	ResourceGroup     string             `json:"resourceGroup,omitempty"`
	LastModifiedTime  string             `json:"lastModifiedTimeUtc,omitempty"`
	HTTPSOnly         bool               `json:"httpsOnly,omitempty"`
	// ClientCertEnabled, ClientCertMode and ClientCertExclusionPaths are the
	// site's TLS mutual authentication: with client certificates enabled the
	// front end asks the client for one and forwards it to the app as
	// X-ARR-ClientCert; Required refuses a request without one outside the
	// exclusion paths.
	ClientCertEnabled        bool   `json:"clientCertEnabled"`
	ClientCertMode           string `json:"clientCertMode,omitempty"`
	ClientCertExclusionPaths string `json:"clientCertExclusionPaths,omitempty"`
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
	SlotSwapStatus            *SlotSwapStatus            `json:"slotSwapStatus,omitempty"`
	// KeyVaultReferenceIdentity names the identity the site resolves Key
	// Vault references with: SystemAssigned or a user-assigned identity's
	// resource ID.
	KeyVaultReferenceIdentity string `json:"keyVaultReferenceIdentity,omitempty"`
}

// SlotSwapStatus records the last successful swap an app or slot took part in.
type SlotSwapStatus struct {
	TimestampUtc        string `json:"timestampUtc"`
	SourceSlotName      string `json:"sourceSlotName"`
	DestinationSlotName string `json:"destinationSlotName"`
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
	AcrUseManagedIdentityCreds bool   `json:"acrUseManagedIdentityCreds,omitempty"`
	AcrUserManagedIdentityID   string `json:"acrUserManagedIdentityID,omitempty"`
	// ScmType is the site's source-control kind: None until source control
	// is configured, then the kind of repository it deploys from.
	ScmType                                string `json:"scmType,omitempty"`
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

	registerWebSites(srv, armBase, sites, functionConfigs)
	registerSiteConfigReads(srv, armBase, sites)
	registerSiteConfigHandlers(srv, armBase, sites)
	registerAppServiceFrontEnd(srv)
	registerAppServiceKudu(srv)
	registerSiteContainerHandlers(srv, armBase)
	registerSiteVNetIntegration(srv)
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

// errSiteStopped refuses to start a stopped site's container.
var errSiteStopped = errors.New("the site is stopped")

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

// webConnStringEnvPrefixes are the environment-variable prefixes App Service
// exposes a connection string under, by its type.
var webConnStringEnvPrefixes = map[string]string{
	"mysql":           "MYSQLCONNSTR_",
	"sqlserver":       "SQLCONNSTR_",
	"sqlazure":        "SQLAZURECONNSTR_",
	"custom":          "CUSTOMCONNSTR_",
	"notificationhub": "NOTIFICATIONHUBCONNSTR_",
	"servicebus":      "SERVICEBUSCONNSTR_",
	"eventhub":        "EVENTHUBCONNSTR_",
	"apihub":          "APIHUBCONNSTR_",
	"docdb":           "DOCDBCONNSTR_",
	"rediscache":      "REDISCACHECONNSTR_",
	"postgresql":      "POSTGRESQLCONNSTR_",
}

// webConnStringType is the ConnectionStringType a request names, spelled as
// the enum spells it, and whether it is one.
func webConnStringType(t string) (string, bool) {
	for _, name := range []string{"MySql", "SQLServer", "SQLAzure", "Custom", "NotificationHub", "ServiceBus",
		"EventHub", "ApiHub", "DocDb", "RedisCache", "PostgreSQL"} {
		if strings.EqualFold(t, name) {
			return name, true
		}
	}
	return "", false
}

// webValidateConnStrings normalizes each connection string's type to the
// enum's spelling and refuses one that names no ConnectionStringType.
func webValidateConnStrings(props map[string]AzureSiteConnStringValue) error {
	for name, v := range props {
		t, ok := webConnStringType(v.Type)
		if !ok {
			return fmt.Errorf("connection string %q has type %q, which is not one of MySql, SQLServer, SQLAzure, Custom, NotificationHub, ServiceBus, EventHub, ApiHub, DocDb, RedisCache or PostgreSQL", name, v.Type)
		}
		v.Type = t
		props[name] = v
	}
	return nil
}

// siteConnectionStringEnv is the environment App Service gives a site's
// workload for its connection strings: each under its type's prefix, a Key
// Vault reference that resolves as the secret's value.
func siteConnectionStringEnv(site *Site) map[string]string {
	out := map[string]string{}
	if site == nil {
		return out
	}
	cfg, _ := siteConfigStore.Get(site.ID)
	for name, v := range cfg.ConnectionStrings {
		prefix, ok := webConnStringEnvPrefixes[strings.ToLower(v.Type)]
		if !ok {
			continue
		}
		value := v.Value
		if _, isRef := webParseKeyVaultRef(value); isRef {
			if _, secret, resolved := webResolveKVRef(site, value); resolved {
				value = secret
			}
		}
		out[prefix+name] = value
	}
	return out
}

// siteStopped reports whether the site is stopped: App Service runs nothing
// for it and its front end answers with the stopped-site page.
func siteStopped(site *Site) bool {
	return site != nil && strings.EqualFold(site.Properties.State, "Stopped")
}

// siteConfigConnStrings reads the connectionStrings a site PUT's siteConfig
// carries, which replace the site's connection strings when present.
func siteConfigConnStrings(cfg *SiteConfig) (map[string]AzureSiteConnStringValue, bool, error) {
	raw, ok := cfg.Extra["connectionStrings"]
	if !ok || string(raw) == "null" {
		return nil, false, nil
	}
	var list []struct {
		Name             string `json:"name"`
		ConnectionString string `json:"connectionString"`
		Type             string `json:"type"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, false, fmt.Errorf("siteConfig.connectionStrings: %w", err)
	}
	out := make(map[string]AzureSiteConnStringValue, len(list))
	for _, c := range list {
		out[c.Name] = AzureSiteConnStringValue{Value: c.ConnectionString, Type: c.Type}
	}
	if err := webValidateConnStrings(out); err != nil {
		return nil, false, err
	}
	return out, true, nil
}
