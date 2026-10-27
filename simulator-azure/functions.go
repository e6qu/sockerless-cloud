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

// Site is an App Service or Function App site.
//
// AzureStorageAccounts is the azurestorageaccounts config sub-resource, never
// part of the site wire shape. It sits at the top level of the stored record
// because the persistence sidecar keeps exported top-level json:"-" fields
// across a SIM_PERSIST restart.
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
	// SystemIdentityClientID is the system-assigned identity's application ID,
	// which the directory reads and the wire shape does not carry.
	SystemIdentityClientID string `json:"-"`
}

type SiteProperties struct {
	State            string   `json:"state,omitempty"`
	DefaultHostName  string   `json:"defaultHostName,omitempty"`
	HostNames        []string `json:"hostNames,omitempty"`
	Enabled          bool     `json:"enabled"`
	EnabledHostNames []string `json:"enabledHostNames,omitempty"`
	// The Standard host requests reach and the Repository (SCM) host deployments
	// go to.
	HostNameSslStates []HostNameSslState `json:"hostNameSslStates,omitempty"`
	ServerFarmID      string             `json:"serverFarmId"`
	SKU               string             `json:"sku"`
	Reserved          bool               `json:"reserved,omitempty"`
	SiteConfig        *SiteConfig        `json:"siteConfig,omitempty"`
	ResourceGroup     string             `json:"resourceGroup,omitempty"`
	LastModifiedTime  string             `json:"lastModifiedTimeUtc,omitempty"`
	HTTPSOnly         bool               `json:"httpsOnly,omitempty"`
	// With client certificates enabled the front end asks for one and forwards it
	// as X-ARR-ClientCert; Required refuses a request without one outside the
	// exclusion paths.
	ClientCertEnabled        bool   `json:"clientCertEnabled"`
	ClientCertMode           string `json:"clientCertMode,omitempty"`
	ClientCertExclusionPaths string `json:"clientCertExclusionPaths,omitempty"`
	// The modern spelling of regional VNet integration (`az webapp
	// vnet-integration add`, terraform's virtual_network_subnet_id): writing it
	// joins the subnet's VNet as a swift networkConfig PUT does.
	VirtualNetworkSubnetID string `json:"virtualNetworkSubnetId,omitempty"`
	// A site normally inherits its App Service Environment from its plan; either
	// way the environment's app list reads it back.
	HostingEnvironmentProfile *HostingEnvironmentProfile `json:"hostingEnvironmentProfile,omitempty"`
	SlotSwapStatus            *SlotSwapStatus            `json:"slotSwapStatus,omitempty"`
	// SystemAssigned or a user-assigned identity's resource ID.
	KeyVaultReferenceIdentity string `json:"keyVaultReferenceIdentity,omitempty"`
}

type SlotSwapStatus struct {
	TimestampUtc        string `json:"timestampUtc"`
	SourceSlotName      string `json:"sourceSlotName"`
	DestinationSlotName string `json:"destinationSlotName"`
}

type SiteConfig struct {
	AppSettings    []NameValuePair `json:"appSettings,omitempty"`
	LinuxFxVersion string          `json:"linuxFxVersion,omitempty"`
	// The platform runs the startup command (`--startup-file`, terraform's
	// app_command_line) after the image's own entrypoint.
	AppCommandLine string `json:"appCommandLine,omitempty"`
	AlwaysOn       bool   `json:"alwaysOn,omitempty"`
	// Pull the image with the user-assigned identity AcrUserManagedIdentityID
	// names by client ID, else the system-assigned identity.
	AcrUseManagedIdentityCreds bool   `json:"acrUseManagedIdentityCreds,omitempty"`
	AcrUserManagedIdentityID   string `json:"acrUserManagedIdentityID,omitempty"`
	// None until source control is configured.
	ScmType                                string `json:"scmType,omitempty"`
	FunctionAppScaleLimit                  int    `json:"functionAppScaleLimit,omitempty"`
	FtpsState                              string `json:"ftpsState,omitempty"`
	LoadBalancing                          string `json:"loadBalancing,omitempty"`
	ManagedPipelineMode                    string `json:"managedPipelineMode,omitempty"`
	IPSecurityRestrictionsDefaultAction    string `json:"ipSecurityRestrictionsDefaultAction,omitempty"`
	MinTLSVersion                          string `json:"minTlsVersion,omitempty"`
	ScmMinTLSVersion                       string `json:"scmMinTlsVersion,omitempty"`
	ScmIPSecurityRestrictionsDefaultAction string `json:"scmIpSecurityRestrictionsDefaultAction,omitempty"`
	// The siteConfig properties the simulator does not act on, kept as sent so a
	// read returns what was written.
	Extra map[string]json.RawMessage `json:"-"`
}

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

// Decode onto the receiver, so a PATCH keeps every property it omits.
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

type HostNameSslState struct {
	Name     string `json:"name"`
	SslState string `json:"sslState"`
	HostType string `json:"hostType"`
}

func siteHostNameSslStates(defaultHost, scmHost string) []HostNameSslState {
	return []HostNameSslState{
		{Name: defaultHost, SslState: "Disabled", HostType: "Standard"},
		{Name: scmHost, SslState: "Disabled", HostType: "Repository"},
	}
}

type NameValuePair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

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

// FunctionName is tracked internally; the wire shape carries only the
// ProxyResource `name` ("<site>/<function>"). It sits at the top level so the
// persistence sidecar keeps it.
type FunctionEnvelope struct {
	ID           string                     `json:"id"`
	Name         string                     `json:"name"`
	Type         string                     `json:"type"`
	Properties   FunctionEnvelopeProperties `json:"properties"`
	FunctionName string                     `json:"-"`
}

// ScriptHref, ConfigHref, Href and InvokeURLTemplate point at the deployed
// Function App's Kudu admin and trigger URLs, which the simulator emits but
// does not serve.
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

// web_more.go's slot functions reuse the function-config store.
var azfSites sim.Store[Site]
var azfFunctionConfigs sim.Store[FunctionEnvelope]

func registerAzureFunctions(srv *sim.Server) {
	sites := sim.MakeStore[Site](srv.DB(), "azf_sites")
	azfSites = sites
	functionConfigs := sim.MakeStore[FunctionEnvelope](srv.DB(), "azf_function_configs")
	azfFunctionConfigs = functionConfigs

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web"

	// terraform-provider-azurerm checks `<name>.azurewebsites.net` before creating
	// a site; any site name held in the store reads as taken.
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
	// AzurePathNormalizationMiddleware lowercases the action.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.Web/checknameavailability", checkNameAvailabilityHandler)

	registerWebSites(srv, armBase, sites, functionConfigs)
	registerSiteConfigReads(srv, armBase, sites)
	registerSiteConfigHandlers(srv, armBase, sites)
	registerAppServiceFrontEnd(srv)
	registerAppServiceKudu(srv)
	registerSiteContainerHandlers(srv, armBase)
	registerSiteVNetIntegration(srv)
}

// siteRunsContainer: a sitecontainers site, a linuxFxVersion naming an image,
// or a built-in stack this App Service runs.
func siteRunsContainer(site *Site) bool {
	if mainSiteContainer(site.ID) != nil || siteContainerImage(site) != "" {
		return true
	}
	_, ok := sitePlatformImage(site)
	return ok
}

// Always On keeps the container running without waiting for a request.
func siteAlwaysOn(site *Site) bool {
	return site != nil && site.Properties.SiteConfig != nil && site.Properties.SiteConfig.AlwaysOn
}

// "Regardless of the function app timeout setting, 230 seconds is the maximum
// amount of time that an HTTP triggered function can take to respond to a
// request."
const azureFunctionsHTTPRequestLimit = 230 * time.Second

// The WEBSITES_PORT app setting, else 80.
func siteContainerPort(site *Site) int {
	if port, err := strconv.Atoi(strings.TrimSpace(siteAppSettings(site)["WEBSITES_PORT"])); err == nil && port > 0 && port < 65536 {
		return port
	}
	return 80
}

// WEBSITES_CONTAINER_START_TIME_LIMIT in seconds: 230 by default, at most 1800.
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

var errSiteStopped = errors.New("the site is stopped")

// "DOCKER|" and the other container prefixes name an image; a built-in stack
// ("PHP|8.2", "NODE|20-lts") names a platform image siteBuiltInStack resolves.
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

func siteLinuxFxNamesAnImage(prefix string) bool {
	switch strings.ToUpper(strings.TrimSpace(prefix)) {
	case "DOCKER", "COMPOSE", "KUBE":
		return true
	}
	return false
}

// Empty when linuxFxVersion names a container image or nothing.
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

// WEBSITES_CONTAINER_STOP_TIME_LIMIT in seconds after SIGTERM: five by
// default, at most 120.
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

// Spelled as the ConnectionStringType enum spells it.
func webConnStringType(t string) (string, bool) {
	for _, name := range []string{"MySql", "SQLServer", "SQLAzure", "Custom", "NotificationHub", "ServiceBus",
		"EventHub", "ApiHub", "DocDb", "RedisCache", "PostgreSQL"} {
		if strings.EqualFold(t, name) {
			return name, true
		}
	}
	return "", false
}

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

// Each under its type's prefix; a Key Vault reference resolves to the secret's
// value.
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

// A stopped site runs nothing, and its front end answers the stopped-site page.
func siteStopped(site *Site) bool {
	return site != nil && strings.EqualFold(site.Properties.State, "Stopped")
}

// Present connectionStrings replace the site's.
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
