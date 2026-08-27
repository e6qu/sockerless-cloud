package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Azure Static Web Apps (Microsoft.Web/staticSites) — the full StaticSites_*
// family of the vendored web-arm-openapi-2025-03-01 swagger: the ARM resource
// CRUD, builds (environments), application settings in both the appsettings
// and functionappsettings spellings, custom domains with DNS-backed
// validation, users and role invitations, deployment secrets and API-key
// reset, basic auth, database connections, linked backends, user-provided
// function apps, private endpoint connections and private link resources,
// zip deployments (Azure-AsyncOperation LROs), workflow-file preview and
// repository detach.
//
// Every sub-collection with an independent lifecycle lives in its own store,
// keyed by the canonical ARM resource id, so the whole tree hangs off
// webStaticSites and a site delete cascades by id prefix.

// StaticSiteResource mirrors armappservice.StaticSiteARMResource.
type StaticSiteResource struct {
	ID         string                `json:"id,omitempty"`
	Name       string                `json:"name,omitempty"`
	Type       string                `json:"type"`
	Location   string                `json:"location"`
	Tags       map[string]string     `json:"tags,omitempty"`
	Kind       string                `json:"kind,omitempty"`
	Sku        *StaticSiteSku        `json:"sku,omitempty"`
	Identity   map[string]any        `json:"identity,omitempty"`
	Properties *StaticSiteProperties `json:"properties,omitempty"`
}

// StaticSiteSku mirrors armappservice.SKUDescription (the subset the
// simulator round-trips).
type StaticSiteSku struct {
	Name string `json:"name,omitempty"`
	Tier string `json:"tier,omitempty"`
}

// StaticSiteProperties mirrors armappservice.StaticSite. The read-only
// aggregate members (customDomains, userProvidedFunctionApps, linkedBackends,
// databaseConnections) are never stored — staticSiteView assembles them from
// the child stores on every read, so they always reflect the live children.
// repositoryToken is write-only in real Azure (a GET never returns it); the
// simulator moves it into the site's secrets record before storing.
type StaticSiteProperties struct {
	DefaultHostname           string                                         `json:"defaultHostname,omitempty"`
	RepositoryURL             string                                         `json:"repositoryUrl,omitempty"`
	Branch                    string                                         `json:"branch,omitempty"`
	CustomDomains             []string                                       `json:"customDomains,omitempty"`
	RepositoryToken           string                                         `json:"repositoryToken,omitempty"`
	BuildProperties           *StaticSiteBuildProperties                     `json:"buildProperties,omitempty"`
	StagingEnvironmentPolicy  string                                         `json:"stagingEnvironmentPolicy,omitempty"`
	AllowConfigFileUpdates    *bool                                          `json:"allowConfigFileUpdates,omitempty"`
	TemplateProperties        map[string]any                                 `json:"templateProperties,omitempty"`
	KeyVaultReferenceIdentity string                                         `json:"keyVaultReferenceIdentity,omitempty"`
	UserProvidedFunctionApps  []StaticSiteUserProvidedFunctionAppARMResource `json:"userProvidedFunctionApps,omitempty"`
	LinkedBackends            []StaticSiteLinkedBackend                      `json:"linkedBackends,omitempty"`
	Provider                  string                                         `json:"provider,omitempty"`
	EnterpriseGradeCdnStatus  string                                         `json:"enterpriseGradeCdnStatus,omitempty"`
	PublicNetworkAccess       string                                         `json:"publicNetworkAccess,omitempty"`
	DatabaseConnections       []DatabaseConnectionOverview                   `json:"databaseConnections,omitempty"`
}

// StaticSiteBuildProperties mirrors the swagger StaticSiteBuildProperties —
// the build *configuration* block (app/api locations and build commands)
// carried inside StaticSite.buildProperties and the workflow preview request.
type StaticSiteBuildProperties struct {
	AppLocation                        string `json:"appLocation,omitempty"`
	APILocation                        string `json:"apiLocation,omitempty"`
	AppArtifactLocation                string `json:"appArtifactLocation,omitempty"`
	OutputLocation                     string `json:"outputLocation,omitempty"`
	AppBuildCommand                    string `json:"appBuildCommand,omitempty"`
	APIBuildCommand                    string `json:"apiBuildCommand,omitempty"`
	SkipGithubActionWorkflowGeneration *bool  `json:"skipGithubActionWorkflowGeneration,omitempty"`
	GithubActionSecretNameOverride     string `json:"githubActionSecretNameOverride,omitempty"`
}

// StaticSiteBuildARMResource mirrors armappservice.StaticSiteBuildARMResource
// (one build environment: "default" for production, one per staging
// environment created by a build-scoped zip deployment).
type StaticSiteBuildARMResource struct {
	ID         string                               `json:"id,omitempty"`
	Name       string                               `json:"name,omitempty"`
	Type       string                               `json:"type,omitempty"`
	Kind       string                               `json:"kind,omitempty"`
	Properties StaticSiteBuildARMResourceProperties `json:"properties"`
}

// StaticSiteBuildARMResourceProperties mirrors the swagger definition of the
// same name. The nested aggregate arrays are assembled from the child stores
// at read time, exactly like the site-level ones.
type StaticSiteBuildARMResourceProperties struct {
	BuildID                  string                                         `json:"buildId,omitempty"`
	SourceBranch             string                                         `json:"sourceBranch,omitempty"`
	PullRequestTitle         string                                         `json:"pullRequestTitle,omitempty"`
	Hostname                 string                                         `json:"hostname,omitempty"`
	CreatedTimeUTC           string                                         `json:"createdTimeUtc,omitempty"`
	LastUpdatedOn            string                                         `json:"lastUpdatedOn,omitempty"`
	Status                   string                                         `json:"status,omitempty"`
	UserProvidedFunctionApps []StaticSiteUserProvidedFunctionAppARMResource `json:"userProvidedFunctionApps,omitempty"`
	LinkedBackends           []StaticSiteLinkedBackend                      `json:"linkedBackends,omitempty"`
	DatabaseConnections      []DatabaseConnectionOverview                   `json:"databaseConnections,omitempty"`
}

// StaticSiteLinkedBackend mirrors the swagger StaticSiteLinkedBackend — the
// flattened backend link nested inside site and build views.
type StaticSiteLinkedBackend struct {
	BackendResourceID string `json:"backendResourceId,omitempty"`
	Region            string `json:"region,omitempty"`
	CreatedOn         string `json:"createdOn,omitempty"`
	ProvisioningState string `json:"provisioningState,omitempty"`
}

// StaticSiteLinkedBackendARMResource mirrors
// armappservice.StaticSiteLinkedBackendARMResource.
type StaticSiteLinkedBackendARMResource struct {
	ID         string                  `json:"id,omitempty"`
	Name       string                  `json:"name,omitempty"`
	Type       string                  `json:"type,omitempty"`
	Kind       string                  `json:"kind,omitempty"`
	Properties StaticSiteLinkedBackend `json:"properties"`
}

// StaticSiteUserProvidedFunctionAppARMResource mirrors the swagger resource
// of the same name; the same shape serves the nested
// StaticSiteUserProvidedFunctionApp items in site and build views.
type StaticSiteUserProvidedFunctionAppARMResource struct {
	ID         string                                      `json:"id,omitempty"`
	Name       string                                      `json:"name,omitempty"`
	Type       string                                      `json:"type,omitempty"`
	Kind       string                                      `json:"kind,omitempty"`
	Properties StaticSiteUserProvidedFunctionAppProperties `json:"properties"`
}

// StaticSiteUserProvidedFunctionAppProperties mirrors the swagger
// StaticSiteUserProvidedFunctionAppProperties.
type StaticSiteUserProvidedFunctionAppProperties struct {
	FunctionAppResourceID string `json:"functionAppResourceId,omitempty"`
	FunctionAppRegion     string `json:"functionAppRegion,omitempty"`
	CreatedOn             string `json:"createdOn,omitempty"`
}

// DatabaseConnection mirrors armappservice.DatabaseConnection. The
// connection string is returned only by the "...WithDetails" (POST .../show)
// operations, exactly as real Azure does; the plain GETs strip it.
type DatabaseConnection struct {
	ID         string                       `json:"id,omitempty"`
	Name       string                       `json:"name,omitempty"`
	Type       string                       `json:"type,omitempty"`
	Kind       string                       `json:"kind,omitempty"`
	Properties DatabaseConnectionProperties `json:"properties"`
}

// DatabaseConnectionProperties mirrors the swagger definition of the same name.
type DatabaseConnectionProperties struct {
	ResourceID         string `json:"resourceId,omitempty"`
	ConnectionIdentity string `json:"connectionIdentity,omitempty"`
	ConnectionString   string `json:"connectionString,omitempty"`
	Region             string `json:"region,omitempty"`
}

// DatabaseConnectionOverview mirrors the swagger DatabaseConnectionOverview —
// the connection summary nested inside site and build views (never the
// connection string).
type DatabaseConnectionOverview struct {
	ResourceID         string `json:"resourceId,omitempty"`
	ConnectionIdentity string `json:"connectionIdentity,omitempty"`
	Region             string `json:"region,omitempty"`
	Name               string `json:"name,omitempty"`
}

// StaticSiteCustomDomainOverviewARMResource mirrors the swagger resource of
// the same name. ValidationMethod is the write-only member of the create
// request; it is persisted (json:"-" fields ride the persistence sidecar) so
// re-validation on later reads checks the same DNS record kind the caller
// asked for, but it is never a member of the overview response.
type StaticSiteCustomDomainOverviewARMResource struct {
	ID         string                                              `json:"id,omitempty"`
	Name       string                                              `json:"name,omitempty"`
	Type       string                                              `json:"type,omitempty"`
	Kind       string                                              `json:"kind,omitempty"`
	Properties StaticSiteCustomDomainOverviewARMResourceProperties `json:"properties"`

	ValidationMethod string `json:"-"`
}

// StaticSiteCustomDomainOverviewARMResourceProperties mirrors the swagger
// definition of the same name.
type StaticSiteCustomDomainOverviewARMResourceProperties struct {
	DomainName      string `json:"domainName,omitempty"`
	CreatedOn       string `json:"createdOn,omitempty"`
	Status          string `json:"status,omitempty"`
	ValidationToken string `json:"validationToken,omitempty"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
}

// StaticSiteUserARMResource mirrors armappservice.StaticSiteUserARMResource.
type StaticSiteUserARMResource struct {
	ID         string                              `json:"id,omitempty"`
	Name       string                              `json:"name,omitempty"`
	Type       string                              `json:"type,omitempty"`
	Kind       string                              `json:"kind,omitempty"`
	Properties StaticSiteUserARMResourceProperties `json:"properties"`
}

// StaticSiteUserARMResourceProperties mirrors the swagger definition of the
// same name.
type StaticSiteUserARMResourceProperties struct {
	Provider    string `json:"provider,omitempty"`
	UserID      string `json:"userId,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Roles       string `json:"roles,omitempty"`
}

// StaticSiteBasicAuthPropertiesARMResource mirrors the swagger resource of
// the same name. The password is write-only in real Azure — a GET reports
// only secretState — so it is persisted through the sidecar and never
// serialized.
type StaticSiteBasicAuthPropertiesARMResource struct {
	ID         string                                             `json:"id,omitempty"`
	Name       string                                             `json:"name,omitempty"`
	Type       string                                             `json:"type,omitempty"`
	Kind       string                                             `json:"kind,omitempty"`
	Properties StaticSiteBasicAuthPropertiesARMResourceProperties `json:"properties"`

	Password string `json:"-"`
}

// StaticSiteBasicAuthPropertiesARMResourceProperties mirrors the swagger
// definition of the same name, minus the write-only password (held on the
// resource wrapper above).
type StaticSiteBasicAuthPropertiesARMResourceProperties struct {
	SecretURL                  string   `json:"secretUrl,omitempty"`
	ApplicableEnvironmentsMode string   `json:"applicableEnvironmentsMode"`
	Environments               []string `json:"environments,omitempty"`
	SecretState                string   `json:"secretState,omitempty"`
}

// StaticSiteRemotePrivateEndpointConnection is the stored ARM representation
// of a private endpoint connection on a static site, matching the swagger
// RemotePrivateEndpointConnectionARMResource shape (properties kept as a map,
// following the Cosmos DB private-endpoint-connection precedent).
type StaticSiteRemotePrivateEndpointConnection struct {
	ID         string         `json:"id,omitempty"`
	Name       string         `json:"name,omitempty"`
	Type       string         `json:"type,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

// staticSiteSecrets is storage-only state for one static site: the
// deployment API key served by listSecrets and the repository token, which
// real Azure accepts on writes but never returns on reads.
type staticSiteSecrets struct {
	APIKey          string `json:"apiKey"`
	RepositoryToken string `json:"repositoryToken,omitempty"`
}

// staticSiteSettings is the storage shape for one application-settings bag.
// Static Web Apps expose the same bag under two spellings — config/appsettings
// and config/functionappsettings (the pre-rename name of the same settings, as
// both operations' swagger descriptions state) — so one record per scope
// serves all four site/build × app/functionapp operations. ID repeats the
// store key so a site delete can cascade by prefix; the wire response is
// hand-assembled and never serializes this type.
type staticSiteSettings struct {
	ID       string            `json:"id"`
	Settings map[string]string `json:"settings"`
}

var (
	webStaticSites         sim.Store[StaticSiteResource]
	webStaticSiteSecrets   sim.Store[staticSiteSecrets]
	webStaticSiteBuilds    sim.Store[StaticSiteBuildARMResource]
	webStaticSiteSettings  sim.Store[staticSiteSettings]
	webStaticSiteDomains   sim.Store[StaticSiteCustomDomainOverviewARMResource]
	webStaticSiteUsers     sim.Store[StaticSiteUserARMResource]
	webStaticSiteBasicAuth sim.Store[StaticSiteBasicAuthPropertiesARMResource]
	webStaticSiteDBConns   sim.Store[DatabaseConnection]
	webStaticSiteBackends  sim.Store[StaticSiteLinkedBackendARMResource]
	webStaticSiteFnApps    sim.Store[StaticSiteUserProvidedFunctionAppARMResource]
	webStaticSitePECs      sim.Store[StaticSiteRemotePrivateEndpointConnection]
)

const staticSitesPattern = webProvider + "/staticSites/{name}"

// webSubscriptionProvider is the subscription-scoped Microsoft.Web route
// prefix (no resource group), used by the subscription-wide static-site list
// and the location-scoped workflow preview.
const webSubscriptionProvider = "/subscriptions/{subscriptionId}/providers/Microsoft.Web"

func staticSiteARMID(r *http.Request) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/staticSites/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
}

// staticSiteOr404 loads the addressed static site, writing the canonical ARM
// 404 (the same envelope webMissing writes for sites) when it does not exist.
func staticSiteOr404(w http.ResponseWriter, r *http.Request) (StaticSiteResource, bool) {
	ss, ok := webStaticSites.Get(staticSiteARMID(r))
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Web/staticSites/%s' was not found.", sim.PathParam(r, "name"))
		return ss, false
	}
	return ss, true
}

func staticSiteBuildARMID(r *http.Request) string {
	return staticSiteARMID(r) + "/builds/" + sim.PathParam(r, "environmentName")
}

// staticSiteBuildOr404 loads the addressed build, writing the canonical ARM
// 404 when either the site or the build environment does not exist.
func staticSiteBuildOr404(w http.ResponseWriter, r *http.Request) (StaticSiteBuildARMResource, bool) {
	if _, ok := staticSiteOr404(w, r); !ok {
		return StaticSiteBuildARMResource{}, false
	}
	b, ok := webStaticSiteBuilds.Get(staticSiteBuildARMID(r))
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Web/staticSites/%s/builds/%s' was not found.",
			sim.PathParam(r, "name"), sim.PathParam(r, "environmentName"))
		return b, false
	}
	return b, true
}

func staticSiteNow() string { return time.Now().UTC().Format(time.RFC3339) }

// staticSiteFlexInt decodes a JSON integer that clients spell either as a
// number or as a quoted string — the Azure CLI sends
// numHoursToExpiration="12" and real Azure accepts both spellings.
type staticSiteFlexInt int

func (f *staticSiteFlexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("numHoursToExpiration: %w", err)
	}
	*f = staticSiteFlexInt(n)
	return nil
}

// staticSiteAPIKey mints a deployment API key with the shape real Static Web
// Apps deployment tokens have: a long lowercase hex string.
func staticSiteAPIKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

func registerWebStaticSites(srv *sim.Server) {
	webStaticSites = sim.MakeStore[StaticSiteResource](srv.DB(), "web_static_sites")
	webStaticSiteSecrets = sim.MakeStore[staticSiteSecrets](srv.DB(), "web_static_site_secrets")
	webStaticSiteBuilds = sim.MakeStore[StaticSiteBuildARMResource](srv.DB(), "web_static_site_builds")
	webStaticSiteSettings = sim.MakeStore[staticSiteSettings](srv.DB(), "web_static_site_settings")
	webStaticSiteDomains = sim.MakeStore[StaticSiteCustomDomainOverviewARMResource](srv.DB(), "web_static_site_custom_domains")
	webStaticSiteUsers = sim.MakeStore[StaticSiteUserARMResource](srv.DB(), "web_static_site_users")
	webStaticSiteBasicAuth = sim.MakeStore[StaticSiteBasicAuthPropertiesARMResource](srv.DB(), "web_static_site_basic_auth")
	webStaticSiteDBConns = sim.MakeStore[DatabaseConnection](srv.DB(), "web_static_site_database_connections")
	webStaticSiteBackends = sim.MakeStore[StaticSiteLinkedBackendARMResource](srv.DB(), "web_static_site_linked_backends")
	webStaticSiteFnApps = sim.MakeStore[StaticSiteUserProvidedFunctionAppARMResource](srv.DB(), "web_static_site_function_apps")
	webStaticSitePECs = sim.MakeStore[StaticSiteRemotePrivateEndpointConnection](srv.DB(), "web_static_site_pecs")

	base := staticSitesPattern

	// Resource CRUD + lists.
	srv.HandleFunc("GET "+webSubscriptionProvider+"/staticSites", handleStaticSitesListBySubscription)
	srv.HandleFunc("GET "+webProvider+"/staticSites", handleStaticSitesListByResourceGroup)
	srv.HandleFunc("GET "+base, handleStaticSiteGet)
	srv.HandleFunc("PUT "+base, handleStaticSitePut)
	srv.HandleFunc("PATCH "+base, handleStaticSitePatch)
	srv.HandleFunc("DELETE "+base, handleStaticSiteDelete)
	srv.HandleFunc("POST "+base+"/detach", handleStaticSiteDetach)

	// Secrets and settings. The /listsecrets route is registered lowercase:
	// AzurePathNormalizationMiddleware canonicalizes every casing of that
	// segment to lowercase before the mux.
	srv.HandleFunc("POST "+base+"/listsecrets", handleStaticSiteListSecrets)
	srv.HandleFunc("POST "+base+"/resetapikey", handleStaticSiteResetAPIKey)
	srv.HandleFunc("PUT "+base+"/config/appsettings", handleStaticSiteSettingsPut)
	srv.HandleFunc("PUT "+base+"/config/functionappsettings", handleStaticSiteSettingsPut)
	srv.HandleFunc("POST "+base+"/listAppSettings", handleStaticSiteSettingsList)
	srv.HandleFunc("POST "+base+"/listFunctionAppSettings", handleStaticSiteSettingsList)
	srv.HandleFunc("PUT "+base+"/builds/{environmentName}/config/appsettings", handleStaticSiteBuildSettingsPut)
	srv.HandleFunc("PUT "+base+"/builds/{environmentName}/config/functionappsettings", handleStaticSiteBuildSettingsPut)
	srv.HandleFunc("POST "+base+"/builds/{environmentName}/listAppSettings", handleStaticSiteBuildSettingsList)
	srv.HandleFunc("POST "+base+"/builds/{environmentName}/listFunctionAppSettings", handleStaticSiteBuildSettingsList)

	// Builds (environments).
	srv.HandleFunc("GET "+base+"/builds", handleStaticSiteBuildsList)
	srv.HandleFunc("GET "+base+"/builds/{environmentName}", handleStaticSiteBuildGet)
	srv.HandleFunc("DELETE "+base+"/builds/{environmentName}", handleStaticSiteBuildDelete)

	// Zip deployments (Azure-AsyncOperation LROs).
	srv.HandleFunc("POST "+base+"/zipdeploy", handleStaticSiteZipDeploy)
	srv.HandleFunc("POST "+base+"/builds/{environmentName}/zipdeploy", handleStaticSiteBuildZipDeploy)

	// Functions.
	srv.HandleFunc("GET "+base+"/functions", handleStaticSiteFunctionsList)
	srv.HandleFunc("GET "+base+"/builds/{environmentName}/functions", handleStaticSiteBuildFunctionsList)

	// Users, roles and invitations.
	srv.HandleFunc("POST "+base+"/authproviders/{authprovider}/listUsers", handleStaticSiteUsersList)
	srv.HandleFunc("PATCH "+base+"/authproviders/{authprovider}/users/{userid}", handleStaticSiteUserUpdate)
	srv.HandleFunc("DELETE "+base+"/authproviders/{authprovider}/users/{userid}", handleStaticSiteUserDelete)
	srv.HandleFunc("POST "+base+"/createUserInvitation", handleStaticSiteCreateUserInvitation)
	srv.HandleFunc("POST "+base+"/listConfiguredRoles", handleStaticSiteListConfiguredRoles)

	// Custom domains.
	srv.HandleFunc("GET "+base+"/customDomains", handleStaticSiteCustomDomainsList)
	srv.HandleFunc("GET "+base+"/customDomains/{domainName}", handleStaticSiteCustomDomainGet)
	srv.HandleFunc("PUT "+base+"/customDomains/{domainName}", handleStaticSiteCustomDomainPut)
	srv.HandleFunc("DELETE "+base+"/customDomains/{domainName}", handleStaticSiteCustomDomainDelete)
	srv.HandleFunc("POST "+base+"/customDomains/{domainName}/validate", handleStaticSiteCustomDomainValidate)

	// Basic auth.
	srv.HandleFunc("GET "+base+"/basicAuth", handleStaticSiteBasicAuthList)
	srv.HandleFunc("GET "+base+"/basicAuth/{basicAuthName}", handleStaticSiteBasicAuthGet)
	srv.HandleFunc("PUT "+base+"/basicAuth/{basicAuthName}", handleStaticSiteBasicAuthPut)

	// Database connections — the same six operations at site and build scope.
	registerStaticSiteDatabaseConnections(srv, base, "")
	registerStaticSiteDatabaseConnections(srv, base+"/builds/{environmentName}", "builds/")

	// Linked backends — the same five operations at site and build scope.
	registerStaticSiteLinkedBackends(srv, base, "")
	registerStaticSiteLinkedBackends(srv, base+"/builds/{environmentName}", "builds/")

	// User-provided function apps — the same four operations at both scopes.
	registerStaticSiteUserProvidedFunctionApps(srv, base, "")
	registerStaticSiteUserProvidedFunctionApps(srv, base+"/builds/{environmentName}", "builds/")

	// Private endpoint connections + private link resources.
	srv.HandleFunc("GET "+base+"/privateEndpointConnections", handleStaticSitePECList)
	srv.HandleFunc("GET "+base+"/privateEndpointConnections/{privateEndpointConnectionName}", handleStaticSitePECGet)
	srv.HandleFunc("PUT "+base+"/privateEndpointConnections/{privateEndpointConnectionName}", handleStaticSitePECPut)
	srv.HandleFunc("DELETE "+base+"/privateEndpointConnections/{privateEndpointConnectionName}", handleStaticSitePECDelete)
	srv.HandleFunc("GET "+base+"/privateLinkResources", handleStaticSitePrivateLinkResources)

	// Workflow-file preview (location-scoped).
	srv.HandleFunc("POST "+webSubscriptionProvider+"/locations/{location}/previewStaticSiteWorkflowFile", handleStaticSitePreviewWorkflow)
}

// staticSiteView returns the site with its read-only aggregate members
// (customDomains, userProvidedFunctionApps, linkedBackends,
// databaseConnections) assembled from the child stores, as real Azure
// reports them on every GET.
func staticSiteView(ss StaticSiteResource) StaticSiteResource {
	props := StaticSiteProperties{}
	if ss.Properties != nil {
		props = *ss.Properties
	}
	sitePrefix := ss.ID + "/"
	buildSeg := ss.ID + "/builds/"
	siteScoped := func(id string) bool {
		return strings.HasPrefix(id, sitePrefix) && !strings.HasPrefix(id, buildSeg)
	}
	for _, d := range webStaticSiteDomains.Filter(func(d StaticSiteCustomDomainOverviewARMResource) bool { return siteScoped(d.ID) }) {
		props.CustomDomains = append(props.CustomDomains, d.Properties.DomainName)
	}
	sort.Strings(props.CustomDomains)
	props.UserProvidedFunctionApps = append(props.UserProvidedFunctionApps,
		webStaticSiteFnApps.Filter(func(f StaticSiteUserProvidedFunctionAppARMResource) bool { return siteScoped(f.ID) })...)
	for _, b := range webStaticSiteBackends.Filter(func(b StaticSiteLinkedBackendARMResource) bool { return siteScoped(b.ID) }) {
		props.LinkedBackends = append(props.LinkedBackends, b.Properties)
	}
	for _, c := range webStaticSiteDBConns.Filter(func(c DatabaseConnection) bool { return siteScoped(c.ID) }) {
		props.DatabaseConnections = append(props.DatabaseConnections, DatabaseConnectionOverview{
			ResourceID:         c.Properties.ResourceID,
			ConnectionIdentity: c.Properties.ConnectionIdentity,
			Region:             c.Properties.Region,
			Name:               c.Name,
		})
	}
	ss.Properties = &props
	return ss
}

func staticSiteCollection(filter func(StaticSiteResource) bool) map[string]any {
	out := []StaticSiteResource{}
	for _, ss := range webStaticSites.Filter(filter) {
		out = append(out, staticSiteView(ss))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return map[string]any{"value": out}
}

func handleStaticSitesListBySubscription(w http.ResponseWriter, r *http.Request) {
	prefix := "/subscriptions/" + sim.PathParam(r, "subscriptionId") + "/"
	sim.WriteJSON(w, http.StatusOK, staticSiteCollection(func(s StaticSiteResource) bool {
		return strings.HasPrefix(s.ID, prefix)
	}))
}

func handleStaticSitesListByResourceGroup(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/staticSites/",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"))
	sim.WriteJSON(w, http.StatusOK, staticSiteCollection(func(s StaticSiteResource) bool {
		return strings.HasPrefix(s.ID, prefix)
	}))
}

func handleStaticSiteGet(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, staticSiteView(ss))
}

func handleStaticSitePut(w http.ResponseWriter, r *http.Request) {
	var req StaticSiteResource
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	if req.Location == "" {
		sim.AzureError(w, "LocationRequired", "The location property is required for this definition.", http.StatusBadRequest)
		return
	}
	name := sim.PathParam(r, "name")
	id := staticSiteARMID(r)
	props := req.Properties
	if props == nil {
		props = &StaticSiteProperties{}
	}
	props.DefaultHostname = name + ".azurestaticapps.net"
	// The repository token is write-only: it moves to the secrets record and
	// never appears on a read.
	repoToken := props.RepositoryToken
	props.RepositoryToken = ""
	// Read-only aggregates are assembled from the child stores on every read;
	// whatever the caller sent is ignored, as real ARM ignores read-only
	// members.
	props.CustomDomains = nil
	props.UserProvidedFunctionApps = nil
	props.LinkedBackends = nil
	props.DatabaseConnections = nil
	// Defaults real Azure applies on create.
	if props.Provider == "" {
		props.Provider = "None"
	}
	if props.StagingEnvironmentPolicy == "" {
		props.StagingEnvironmentPolicy = "Enabled"
	}
	if props.AllowConfigFileUpdates == nil {
		allow := true
		props.AllowConfigFileUpdates = &allow
	}
	if props.PublicNetworkAccess == "" {
		props.PublicNetworkAccess = "Enabled"
	}
	if props.EnterpriseGradeCdnStatus == "" {
		props.EnterpriseGradeCdnStatus = "Disabled"
	}
	if props.KeyVaultReferenceIdentity == "" {
		props.KeyVaultReferenceIdentity = "SystemAssigned"
	}
	sku := req.Sku
	if sku == nil {
		sku = &StaticSiteSku{Name: "Free", Tier: "Free"}
	}
	identity := req.Identity
	if identity != nil {
		if t, _ := identity["type"].(string); strings.Contains(t, "SystemAssigned") {
			if pid, _ := identity["principalId"].(string); pid == "" {
				identity["principalId"] = generateUUID()
			}
			identity["tenantId"] = simTenantID
		}
	}
	ss := StaticSiteResource{
		ID:         id,
		Name:       name,
		Type:       "Microsoft.Web/staticSites",
		Location:   req.Location,
		Tags:       req.Tags,
		Kind:       req.Kind,
		Sku:        sku,
		Identity:   identity,
		Properties: props,
	}
	webStaticSites.Put(ss.ID, ss)

	// The deployment API key exists from the moment the site does; a
	// re-deploying PUT keeps the existing key.
	secrets, ok := webStaticSiteSecrets.Get(id)
	if !ok {
		secrets = staticSiteSecrets{APIKey: staticSiteAPIKey()}
	}
	if repoToken != "" {
		secrets.RepositoryToken = repoToken
	}
	webStaticSiteSecrets.Put(id, secrets)

	// The production build environment ("default") exists for every static
	// site; staging environments are created by build-scoped deployments.
	buildID := id + "/builds/default"
	if _, ok := webStaticSiteBuilds.Get(buildID); !ok {
		webStaticSiteBuilds.Put(buildID, StaticSiteBuildARMResource{
			ID:   buildID,
			Name: "default",
			Type: "Microsoft.Web/staticSites/builds",
			Properties: StaticSiteBuildARMResourceProperties{
				BuildID:        "default",
				SourceBranch:   props.Branch,
				Hostname:       props.DefaultHostname,
				CreatedTimeUTC: staticSiteNow(),
				LastUpdatedOn:  staticSiteNow(),
				Status:         "Ready",
			},
		})
	}
	sim.WriteJSON(w, http.StatusOK, staticSiteView(ss))
}

func handleStaticSitePatch(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	var req StaticSiteResource
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	if req.Tags != nil {
		ss.Tags = req.Tags
	}
	if req.Properties != nil {
		p := req.Properties
		cur := ss.Properties
		if cur == nil {
			cur = &StaticSiteProperties{}
			ss.Properties = cur
		}
		if p.RepositoryURL != "" {
			cur.RepositoryURL = p.RepositoryURL
		}
		if p.Branch != "" {
			cur.Branch = p.Branch
		}
		if p.Provider != "" {
			cur.Provider = p.Provider
		}
		if p.BuildProperties != nil {
			cur.BuildProperties = p.BuildProperties
		}
		if p.StagingEnvironmentPolicy != "" {
			cur.StagingEnvironmentPolicy = p.StagingEnvironmentPolicy
		}
		if p.AllowConfigFileUpdates != nil {
			cur.AllowConfigFileUpdates = p.AllowConfigFileUpdates
		}
		if p.PublicNetworkAccess != "" {
			cur.PublicNetworkAccess = p.PublicNetworkAccess
		}
		if p.EnterpriseGradeCdnStatus != "" {
			cur.EnterpriseGradeCdnStatus = p.EnterpriseGradeCdnStatus
		}
		if p.TemplateProperties != nil {
			cur.TemplateProperties = p.TemplateProperties
		}
		if p.RepositoryToken != "" {
			secrets, _ := webStaticSiteSecrets.Get(ss.ID)
			if secrets.APIKey == "" {
				secrets.APIKey = staticSiteAPIKey()
			}
			secrets.RepositoryToken = p.RepositoryToken
			webStaticSiteSecrets.Put(ss.ID, secrets)
		}
	}
	webStaticSites.Put(ss.ID, ss)
	sim.WriteJSON(w, http.StatusOK, staticSiteView(ss))
}

func handleStaticSiteDelete(w http.ResponseWriter, r *http.Request) {
	id := staticSiteARMID(r)
	if !webStaticSites.Delete(id) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	staticSiteCascadeDelete(id)
	w.WriteHeader(http.StatusOK)
}

// staticSiteCascadeDelete removes every child record stored under a deleted
// static site — builds, settings, domains, users, basic auth, database
// connections, backends, function-app links, private endpoint connections and
// the secrets record — so a site recreated under the same name does not
// inherit the deleted site's children.
func staticSiteCascadeDelete(siteID string) {
	prefix := siteID + "/"
	for _, b := range webStaticSiteBuilds.Filter(func(b StaticSiteBuildARMResource) bool { return strings.HasPrefix(b.ID, prefix) }) {
		webStaticSiteBuilds.Delete(b.ID)
	}
	for _, d := range webStaticSiteDomains.Filter(func(d StaticSiteCustomDomainOverviewARMResource) bool { return strings.HasPrefix(d.ID, prefix) }) {
		webStaticSiteDomains.Delete(d.ID)
	}
	for _, u := range webStaticSiteUsers.Filter(func(u StaticSiteUserARMResource) bool { return strings.HasPrefix(u.ID, prefix) }) {
		webStaticSiteUsers.Delete(u.ID)
	}
	for _, ba := range webStaticSiteBasicAuth.Filter(func(b StaticSiteBasicAuthPropertiesARMResource) bool { return strings.HasPrefix(b.ID, prefix) }) {
		webStaticSiteBasicAuth.Delete(ba.ID)
	}
	for _, c := range webStaticSiteDBConns.Filter(func(c DatabaseConnection) bool { return strings.HasPrefix(c.ID, prefix) }) {
		webStaticSiteDBConns.Delete(c.ID)
	}
	for _, b := range webStaticSiteBackends.Filter(func(b StaticSiteLinkedBackendARMResource) bool { return strings.HasPrefix(b.ID, prefix) }) {
		webStaticSiteBackends.Delete(b.ID)
	}
	for _, f := range webStaticSiteFnApps.Filter(func(f StaticSiteUserProvidedFunctionAppARMResource) bool { return strings.HasPrefix(f.ID, prefix) }) {
		webStaticSiteFnApps.Delete(f.ID)
	}
	for _, p := range webStaticSitePECs.Filter(func(p StaticSiteRemotePrivateEndpointConnection) bool { return strings.HasPrefix(p.ID, prefix) }) {
		webStaticSitePECs.Delete(p.ID)
	}
	for _, s := range webStaticSiteSettings.Filter(func(s staticSiteSettings) bool { return strings.HasPrefix(s.ID, prefix) }) {
		webStaticSiteSettings.Delete(s.ID)
	}
	webStaticSiteSecrets.Delete(siteID)
}

func handleStaticSiteDetach(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	webStaticSites.Update(ss.ID, func(s *StaticSiteResource) {
		if s.Properties == nil {
			s.Properties = &StaticSiteProperties{}
		}
		s.Properties.RepositoryURL = ""
		s.Properties.Branch = ""
		s.Properties.Provider = "None"
	})
	webStaticSiteSecrets.Update(ss.ID, func(s *staticSiteSecrets) { s.RepositoryToken = "" })
	w.WriteHeader(http.StatusOK)
}

func handleStaticSiteListSecrets(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	secrets, _ := webStaticSiteSecrets.Get(ss.ID)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"id":         ss.ID + "/secrets",
		"name":       "secrets",
		"type":       "Microsoft.Web/staticSites/secrets",
		"properties": map[string]string{"apiKey": secrets.APIKey},
	})
}

func handleStaticSiteResetAPIKey(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	var req struct {
		Properties struct {
			RepositoryToken        string `json:"repositoryToken"`
			ShouldUpdateRepository bool   `json:"shouldUpdateRepository"`
		} `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	secrets, _ := webStaticSiteSecrets.Get(ss.ID)
	secrets.APIKey = staticSiteAPIKey()
	if req.Properties.RepositoryToken != "" {
		secrets.RepositoryToken = req.Properties.RepositoryToken
	}
	webStaticSiteSecrets.Put(ss.ID, secrets)
	w.WriteHeader(http.StatusOK)
}

// staticSiteSettingsScope returns the settings-store key for a scope (site or
// build ARM id). Both the appsettings and functionappsettings spellings of
// the API address the same bag, so both resolve to the same key.
func staticSiteSettingsScope(scopeID string) string { return scopeID + "/config/appsettings" }

// staticSiteSettingsName reports the config-resource leaf name the response
// carries, derived from which spelling of the route the client called.
func staticSiteSettingsName(r *http.Request) string {
	if strings.Contains(strings.ToLower(r.URL.Path), "functionappsettings") {
		return "functionappsettings"
	}
	return "appsettings"
}

func writeStaticSiteSettings(w http.ResponseWriter, scopeID, leaf string, buildScope bool, settings map[string]string) {
	typ := "Microsoft.Web/staticSites/config"
	if buildScope {
		typ = "Microsoft.Web/staticSites/builds/config"
	}
	if settings == nil {
		settings = map[string]string{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"id":         scopeID + "/config/" + leaf,
		"name":       leaf,
		"type":       typ,
		"properties": settings,
	})
}

func staticSiteSettingsUpsert(w http.ResponseWriter, r *http.Request, scopeID string, buildScope bool) {
	var req struct {
		Properties map[string]string `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	if req.Properties == nil {
		req.Properties = map[string]string{}
	}
	key := staticSiteSettingsScope(scopeID)
	webStaticSiteSettings.Put(key, staticSiteSettings{ID: key, Settings: req.Properties})
	writeStaticSiteSettings(w, scopeID, staticSiteSettingsName(r), buildScope, req.Properties)
}

func handleStaticSiteSettingsPut(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	staticSiteSettingsUpsert(w, r, ss.ID, false)
}

func handleStaticSiteSettingsList(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	stored, _ := webStaticSiteSettings.Get(staticSiteSettingsScope(ss.ID))
	writeStaticSiteSettings(w, ss.ID, staticSiteSettingsName(r), false, stored.Settings)
}

func handleStaticSiteBuildSettingsPut(w http.ResponseWriter, r *http.Request) {
	b, ok := staticSiteBuildOr404(w, r)
	if !ok {
		return
	}
	staticSiteSettingsUpsert(w, r, b.ID, true)
}

func handleStaticSiteBuildSettingsList(w http.ResponseWriter, r *http.Request) {
	b, ok := staticSiteBuildOr404(w, r)
	if !ok {
		return
	}
	stored, _ := webStaticSiteSettings.Get(staticSiteSettingsScope(b.ID))
	writeStaticSiteSettings(w, b.ID, staticSiteSettingsName(r), true, stored.Settings)
}

// staticSiteBuildView returns the build with its read-only aggregate members
// assembled from the child stores.
func staticSiteBuildView(b StaticSiteBuildARMResource) StaticSiteBuildARMResource {
	prefix := b.ID + "/"
	b.Properties.UserProvidedFunctionApps = append(b.Properties.UserProvidedFunctionApps,
		webStaticSiteFnApps.Filter(func(f StaticSiteUserProvidedFunctionAppARMResource) bool { return strings.HasPrefix(f.ID, prefix) })...)
	for _, lb := range webStaticSiteBackends.Filter(func(lb StaticSiteLinkedBackendARMResource) bool { return strings.HasPrefix(lb.ID, prefix) }) {
		b.Properties.LinkedBackends = append(b.Properties.LinkedBackends, lb.Properties)
	}
	for _, c := range webStaticSiteDBConns.Filter(func(c DatabaseConnection) bool { return strings.HasPrefix(c.ID, prefix) }) {
		b.Properties.DatabaseConnections = append(b.Properties.DatabaseConnections, DatabaseConnectionOverview{
			ResourceID:         c.Properties.ResourceID,
			ConnectionIdentity: c.Properties.ConnectionIdentity,
			Region:             c.Properties.Region,
			Name:               c.Name,
		})
	}
	return b
}

func handleStaticSiteBuildsList(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	prefix := ss.ID + "/builds/"
	out := []StaticSiteBuildARMResource{}
	for _, b := range webStaticSiteBuilds.Filter(func(b StaticSiteBuildARMResource) bool { return strings.HasPrefix(b.ID, prefix) }) {
		out = append(out, staticSiteBuildView(b))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleStaticSiteBuildGet(w http.ResponseWriter, r *http.Request) {
	b, ok := staticSiteBuildOr404(w, r)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, staticSiteBuildView(b))
}

func handleStaticSiteBuildDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := staticSiteOr404(w, r); !ok {
		return
	}
	env := sim.PathParam(r, "environmentName")
	if env == "default" {
		// Real Azure refuses to delete the production environment; it is
		// deleted with the site.
		sim.AzureError(w, "BadRequest",
			"The default build of a static site cannot be deleted. Delete the static site instead.",
			http.StatusBadRequest)
		return
	}
	id := staticSiteBuildARMID(r)
	if !webStaticSiteBuilds.Delete(id) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Remove everything scoped under the deleted environment.
	prefix := id + "/"
	for _, c := range webStaticSiteDBConns.Filter(func(c DatabaseConnection) bool { return strings.HasPrefix(c.ID, prefix) }) {
		webStaticSiteDBConns.Delete(c.ID)
	}
	for _, b := range webStaticSiteBackends.Filter(func(b StaticSiteLinkedBackendARMResource) bool { return strings.HasPrefix(b.ID, prefix) }) {
		webStaticSiteBackends.Delete(b.ID)
	}
	for _, f := range webStaticSiteFnApps.Filter(func(f StaticSiteUserProvidedFunctionAppARMResource) bool { return strings.HasPrefix(f.ID, prefix) }) {
		webStaticSiteFnApps.Delete(f.ID)
	}
	webStaticSiteSettings.Delete(staticSiteSettingsScope(id))
	w.WriteHeader(http.StatusOK)
}

// StaticSiteZipDeployment mirrors the swagger StaticSiteZipDeployment request
// properties.
type StaticSiteZipDeployment struct {
	AppZipURL        string `json:"appZipUrl,omitempty"`
	APIZipURL        string `json:"apiZipUrl,omitempty"`
	DeploymentTitle  string `json:"deploymentTitle,omitempty"`
	Provider         string `json:"provider,omitempty"`
	FunctionLanguage string `json:"functionLanguage,omitempty"`
}

// staticSiteZipDeploy runs one zip deployment as the Azure-AsyncOperation LRO
// real Azure issues: the build flips to Uploading immediately, the 202 carries
// the operation headers, and the polled operation completes with the build
// Ready.
func staticSiteZipDeploy(w http.ResponseWriter, r *http.Request, ss StaticSiteResource, env string) {
	var req struct {
		Properties StaticSiteZipDeployment `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	buildID := ss.ID + "/builds/" + env
	hostname := ""
	if ss.Properties != nil {
		hostname = ss.Properties.DefaultHostname
	}
	if env != "default" && hostname != "" {
		hostname = strings.TrimSuffix(hostname, ".azurestaticapps.net") + "-" + env + ".azurestaticapps.net"
	}
	if b, ok := webStaticSiteBuilds.Get(buildID); ok {
		b.Properties.Status = "Uploading"
		b.Properties.LastUpdatedOn = staticSiteNow()
		webStaticSiteBuilds.Put(buildID, b)
	} else {
		webStaticSiteBuilds.Put(buildID, StaticSiteBuildARMResource{
			ID:   buildID,
			Name: env,
			Type: "Microsoft.Web/staticSites/builds",
			Properties: StaticSiteBuildARMResourceProperties{
				BuildID:        env,
				Hostname:       hostname,
				CreatedTimeUTC: staticSiteNow(),
				LastUpdatedOn:  staticSiteNow(),
				Status:         "Uploading",
			},
		})
	}
	opID := issueAzureAsyncOperation(func() {
		webStaticSiteBuilds.Update(buildID, func(b *StaticSiteBuildARMResource) {
			b.Properties.Status = "Ready"
			b.Properties.LastUpdatedOn = staticSiteNow()
		})
	})
	sub := sim.PathParam(r, "subscriptionId")
	apiVersion := r.URL.Query().Get("api-version")
	opURL := azureAsyncOperationHeader(r, sub, "Microsoft.Web", ss.Location, "operationStatuses", opID, apiVersion)
	locationURL := azureAsyncOperationHeader(r, sub, "Microsoft.Web", ss.Location, "operationResults", opID, apiVersion)
	writeAzureAsyncCreateHeaders(w, opURL, locationURL)
	w.WriteHeader(http.StatusAccepted)
}

func handleStaticSiteZipDeploy(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	staticSiteZipDeploy(w, r, ss, "default")
}

func handleStaticSiteBuildZipDeploy(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	staticSiteZipDeploy(w, r, ss, sim.PathParam(r, "environmentName"))
}

// Managed functions are produced by the Static Web Apps build pipeline (Oryx
// building the repository's api folder), which has no ARM surface the
// simulator could run; with no pipeline-built functions, the list is
// truthfully empty. Functions brought via linked user-provided function apps
// are listed by the userProvidedFunctionApps operations, not here — matching
// real Azure.
func handleStaticSiteFunctionsList(w http.ResponseWriter, r *http.Request) {
	if _, ok := staticSiteOr404(w, r); !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
}

func handleStaticSiteBuildFunctionsList(w http.ResponseWriter, r *http.Request) {
	if _, ok := staticSiteBuildOr404(w, r); !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
}

func handleStaticSiteUsersList(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	provider := sim.PathParam(r, "authprovider")
	prefix := ss.ID + "/authproviders/"
	out := []StaticSiteUserARMResource{}
	for _, u := range webStaticSiteUsers.Filter(func(u StaticSiteUserARMResource) bool { return strings.HasPrefix(u.ID, prefix) }) {
		if provider != "all" && !strings.EqualFold(u.Properties.Provider, provider) {
			continue
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleStaticSiteUserUpdate(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	provider := sim.PathParam(r, "authprovider")
	userID := sim.PathParam(r, "userid")
	id := ss.ID + "/authproviders/" + provider + "/users/" + userID
	var req StaticSiteUserARMResource
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	if !webStaticSiteUsers.Update(id, func(u *StaticSiteUserARMResource) {
		u.Properties.Roles = req.Properties.Roles
	}) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The user '%s' was not found on static site '%s'.", userID, ss.Name)
		return
	}
	u, _ := webStaticSiteUsers.Get(id)
	sim.WriteJSON(w, http.StatusOK, u)
}

func handleStaticSiteUserDelete(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	provider := sim.PathParam(r, "authprovider")
	userID := sim.PathParam(r, "userid")
	if !webStaticSiteUsers.Delete(ss.ID + "/authproviders/" + provider + "/users/" + userID) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The user '%s' was not found on static site '%s'.", userID, ss.Name)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleStaticSiteCreateUserInvitation(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	var req struct {
		Properties struct {
			Domain               string            `json:"domain"`
			Provider             string            `json:"provider"`
			UserDetails          string            `json:"userDetails"`
			Roles                string            `json:"roles"`
			NumHoursToExpiration staticSiteFlexInt `json:"numHoursToExpiration"`
		} `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	p := req.Properties
	if p.Provider == "" || p.UserDetails == "" {
		sim.AzureError(w, "BadRequest", "provider and userDetails are required.", http.StatusBadRequest)
		return
	}
	// The invitation domain must be one the site serves — its default
	// hostname or a registered custom domain.
	valid := ss.Properties != nil && strings.EqualFold(p.Domain, ss.Properties.DefaultHostname)
	if !valid {
		for _, d := range webStaticSiteDomains.Filter(func(d StaticSiteCustomDomainOverviewARMResource) bool {
			return strings.HasPrefix(d.ID, ss.ID+"/")
		}) {
			if strings.EqualFold(p.Domain, d.Properties.DomainName) {
				valid = true
				break
			}
		}
	}
	if !valid {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest,
			"The domain '%s' is not a valid domain for this static site.", p.Domain)
		return
	}
	// The end-user acceptance handshake happens on the site's hostname,
	// outside the ARM plane, so the simulator records the invited user at
	// invitation time — the ARM-visible end state of an accepted invitation.
	userID := generateUUID()
	user := StaticSiteUserARMResource{
		ID:   ss.ID + "/authproviders/" + p.Provider + "/users/" + userID,
		Name: userID,
		Type: "Microsoft.Web/staticSites/authproviders/users",
		Properties: StaticSiteUserARMResourceProperties{
			Provider:    p.Provider,
			UserID:      userID,
			DisplayName: p.UserDetails,
			Roles:       p.Roles,
		},
	}
	webStaticSiteUsers.Put(user.ID, user)

	hours := int(p.NumHoursToExpiration)
	if hours <= 0 {
		hours = 1
	}
	hostname := ""
	if ss.Properties != nil {
		hostname = ss.Properties.DefaultHostname
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"properties": map[string]any{
			"expiresOn":     time.Now().UTC().Add(time.Duration(hours) * time.Hour).Format(time.RFC3339),
			"invitationUrl": fmt.Sprintf("https://%s/.auth/invitations/%s", hostname, generateUUID()),
		},
	})
}

func handleStaticSiteListConfiguredRoles(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	// The built-in roles plus every role assigned to a user of this site.
	seen := map[string]bool{"anonymous": true, "authenticated": true}
	roles := []string{"anonymous", "authenticated"}
	for _, u := range webStaticSiteUsers.Filter(func(u StaticSiteUserARMResource) bool { return strings.HasPrefix(u.ID, ss.ID+"/") }) {
		for _, role := range strings.Split(u.Properties.Roles, ",") {
			role = strings.TrimSpace(role)
			if role != "" && !seen[role] {
				seen[role] = true
				roles = append(roles, role)
			}
		}
	}
	sort.Strings(roles)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"properties": roles})
}

// staticSiteDomainDNSValidated answers, truthfully off the simulator's Azure
// DNS record sets, whether the domain's ownership proof is in place:
// cname-delegation needs a CNAME at the domain pointing to the site's default
// hostname; dns-txt-token needs a TXT record at the domain (or its _dnsauth
// child) carrying the validation token.
func staticSiteDomainDNSValidated(method, domain, token, defaultHostname string) (bool, string) {
	// A record set's FQDN is derived from its ARM id
	// (.../dnsZones/{zone}/{TYPE}/{relativeName}); the record set body's fqdn
	// member is optional on writes.
	fqdnOf := func(rs PublicRecordSet) string {
		if f := strings.TrimSuffix(rs.Properties.Fqdn, "."); f != "" {
			return f
		}
		segs := strings.Split(rs.ID, "/")
		if len(segs) < 3 {
			return ""
		}
		rel, zone := segs[len(segs)-1], segs[len(segs)-3]
		if rel == "@" {
			return zone
		}
		return rel + "." + zone
	}
	if method == "dns-txt-token" {
		for _, rs := range azurePublicDNSRecordSets.List() {
			if !strings.HasSuffix(rs.Type, "/TXT") {
				continue
			}
			fq := fqdnOf(rs)
			if !strings.EqualFold(fq, domain) && !strings.EqualFold(fq, "_dnsauth."+domain) {
				continue
			}
			for _, txt := range rs.Properties.TXTRecords {
				for _, v := range txt.Value {
					if v == token {
						return true, ""
					}
				}
			}
		}
		return false, fmt.Sprintf("No TXT record carrying the validation token was found for '%s'.", domain)
	}
	for _, rs := range azurePublicDNSRecordSets.List() {
		if !strings.HasSuffix(rs.Type, "/CNAME") || rs.Properties.CNAMERecord == nil {
			continue
		}
		if !strings.EqualFold(fqdnOf(rs), domain) {
			continue
		}
		if strings.EqualFold(strings.TrimSuffix(rs.Properties.CNAMERecord.CName, "."), defaultHostname) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("No CNAME record pointing from '%s' to '%s' was found.", domain, defaultHostname)
}

// staticSiteRefreshDomain re-evaluates a still-validating domain against the
// current DNS state — real Azure's validation poller observing that the
// required record has appeared — and persists the transition to Ready.
func staticSiteRefreshDomain(d StaticSiteCustomDomainOverviewARMResource, ss StaticSiteResource) StaticSiteCustomDomainOverviewARMResource {
	if d.Properties.Status != "Validating" {
		return d
	}
	hostname := ""
	if ss.Properties != nil {
		hostname = ss.Properties.DefaultHostname
	}
	ok, _ := staticSiteDomainDNSValidated(d.ValidationMethod, d.Properties.DomainName, d.Properties.ValidationToken, hostname)
	if !ok {
		return d
	}
	d.Properties.Status = "Ready"
	d.Properties.ErrorMessage = ""
	webStaticSiteDomains.Put(d.ID, d)
	return d
}

func handleStaticSiteCustomDomainsList(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	prefix := ss.ID + "/customDomains/"
	out := []StaticSiteCustomDomainOverviewARMResource{}
	for _, d := range webStaticSiteDomains.Filter(func(d StaticSiteCustomDomainOverviewARMResource) bool { return strings.HasPrefix(d.ID, prefix) }) {
		out = append(out, staticSiteRefreshDomain(d, ss))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleStaticSiteCustomDomainGet(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	d, found := webStaticSiteDomains.Get(ss.ID + "/customDomains/" + sim.PathParam(r, "domainName"))
	if !found {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The custom domain '%s' was not found on static site '%s'.", sim.PathParam(r, "domainName"), ss.Name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, staticSiteRefreshDomain(d, ss))
}

func handleStaticSiteCustomDomainPut(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	var req struct {
		Properties struct {
			ValidationMethod string `json:"validationMethod"`
		} `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	method := req.Properties.ValidationMethod
	if method == "" {
		method = "cname-delegation"
	}
	domainName := sim.PathParam(r, "domainName")
	id := ss.ID + "/customDomains/" + domainName
	d, existed := webStaticSiteDomains.Get(id)
	if !existed {
		d = StaticSiteCustomDomainOverviewARMResource{
			ID:   id,
			Name: domainName,
			Type: "Microsoft.Web/staticSites/customDomains",
			Properties: StaticSiteCustomDomainOverviewARMResourceProperties{
				DomainName: domainName,
				CreatedOn:  staticSiteNow(),
			},
		}
	}
	d.ValidationMethod = method
	if method == "dns-txt-token" && d.Properties.ValidationToken == "" {
		d.Properties.ValidationToken = staticSiteAPIKey()
	}
	hostname := ""
	if ss.Properties != nil {
		hostname = ss.Properties.DefaultHostname
	}
	if validated, _ := staticSiteDomainDNSValidated(method, domainName, d.Properties.ValidationToken, hostname); validated {
		d.Properties.Status = "Ready"
		d.Properties.ErrorMessage = ""
	} else {
		d.Properties.Status = "Validating"
	}
	webStaticSiteDomains.Put(id, d)
	sim.WriteJSON(w, http.StatusOK, d)
}

func handleStaticSiteCustomDomainDelete(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	if !webStaticSiteDomains.Delete(ss.ID + "/customDomains/" + sim.PathParam(r, "domainName")) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The custom domain '%s' was not found on static site '%s'.", sim.PathParam(r, "domainName"), ss.Name)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleStaticSiteCustomDomainValidate(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	var req struct {
		Properties struct {
			ValidationMethod string `json:"validationMethod"`
		} `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	method := req.Properties.ValidationMethod
	if method == "" {
		method = "cname-delegation"
	}
	domainName := sim.PathParam(r, "domainName")
	// A domain already registered to a different static site can never be
	// added.
	suffix := "/customDomains/" + domainName
	for _, d := range webStaticSiteDomains.Filter(func(d StaticSiteCustomDomainOverviewARMResource) bool {
		return strings.HasSuffix(strings.ToLower(d.ID), strings.ToLower(suffix))
	}) {
		if !strings.HasPrefix(d.ID, ss.ID+"/") {
			sim.AzureErrorf(w, "Conflict", http.StatusConflict,
				"The custom domain '%s' is already associated with another static site.", domainName)
			return
		}
	}
	if method == "dns-txt-token" {
		// The token handshake happens after creation (the token is minted by
		// the create); at validate time the domain is addable.
		w.WriteHeader(http.StatusOK)
		return
	}
	hostname := ""
	if ss.Properties != nil {
		hostname = ss.Properties.DefaultHostname
	}
	token := ""
	if d, found := webStaticSiteDomains.Get(ss.ID + "/customDomains/" + domainName); found {
		token = d.Properties.ValidationToken
	}
	if validated, why := staticSiteDomainDNSValidated(method, domainName, token, hostname); !validated {
		sim.AzureError(w, "BadRequest", why, http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// staticSiteDefaultBasicAuth is the record real Azure reports before basic
// auth has ever been configured: applicable to no environments, no secret set.
func staticSiteDefaultBasicAuth(siteID string) StaticSiteBasicAuthPropertiesARMResource {
	return StaticSiteBasicAuthPropertiesARMResource{
		ID:   siteID + "/basicAuth/default",
		Name: "default",
		Type: "Microsoft.Web/staticSites/basicAuth",
		Properties: StaticSiteBasicAuthPropertiesARMResourceProperties{
			ApplicableEnvironmentsMode: "SpecifiedEnvironments",
			SecretState:                "None",
		},
	}
}

func handleStaticSiteBasicAuthList(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	ba, found := webStaticSiteBasicAuth.Get(ss.ID + "/basicAuth/default")
	if !found {
		ba = staticSiteDefaultBasicAuth(ss.ID)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []StaticSiteBasicAuthPropertiesARMResource{ba}})
}

func handleStaticSiteBasicAuthGet(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	ba, found := webStaticSiteBasicAuth.Get(ss.ID + "/basicAuth/" + sim.PathParam(r, "basicAuthName"))
	if !found {
		ba = staticSiteDefaultBasicAuth(ss.ID)
	}
	sim.WriteJSON(w, http.StatusOK, ba)
}

func handleStaticSiteBasicAuthPut(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	var req struct {
		Properties struct {
			Password                   string   `json:"password"`
			SecretURL                  string   `json:"secretUrl"`
			ApplicableEnvironmentsMode string   `json:"applicableEnvironmentsMode"`
			Environments               []string `json:"environments"`
		} `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	if req.Properties.ApplicableEnvironmentsMode == "" {
		sim.AzureError(w, "BadRequest", "applicableEnvironmentsMode is required.", http.StatusBadRequest)
		return
	}
	ba := staticSiteDefaultBasicAuth(ss.ID)
	ba.Properties.ApplicableEnvironmentsMode = req.Properties.ApplicableEnvironmentsMode
	ba.Properties.Environments = req.Properties.Environments
	ba.Properties.SecretURL = req.Properties.SecretURL
	ba.Password = req.Properties.Password
	switch {
	case req.Properties.Password != "":
		ba.Properties.SecretState = "Password"
	case req.Properties.SecretURL != "":
		ba.Properties.SecretState = "SecretUrl"
	default:
		ba.Properties.SecretState = "None"
	}
	webStaticSiteBasicAuth.Put(ba.ID, ba)
	sim.WriteJSON(w, http.StatusOK, ba)
}

// registerStaticSiteDatabaseConnections mounts the six database-connection
// operations at one scope (site or build); idSuffix distinguishes the
// resource type the build-scoped children carry.
func registerStaticSiteDatabaseConnections(srv *sim.Server, pattern, scope string) {
	buildScoped := scope != ""
	resType := "Microsoft.Web/staticSites/databaseConnections"
	if buildScoped {
		resType = "Microsoft.Web/staticSites/builds/databaseConnections"
	}
	scopeOr404 := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		if buildScoped {
			b, ok := staticSiteBuildOr404(w, r)
			return b.ID, ok
		}
		ss, ok := staticSiteOr404(w, r)
		return ss.ID, ok
	}
	// The connection string is a secret: the plain reads strip it, the
	// ".../show" spellings return it — exactly the WithDetails split the
	// swagger draws.
	redact := func(c DatabaseConnection) DatabaseConnection {
		c.Properties.ConnectionString = ""
		return c
	}
	list := func(withDetails bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			scopeID, ok := scopeOr404(w, r)
			if !ok {
				return
			}
			prefix := scopeID + "/databaseConnections/"
			out := []DatabaseConnection{}
			for _, c := range webStaticSiteDBConns.Filter(func(c DatabaseConnection) bool { return strings.HasPrefix(c.ID, prefix) }) {
				if !withDetails {
					c = redact(c)
				}
				out = append(out, c)
			}
			sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
			sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
		}
	}
	get := func(withDetails bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			scopeID, ok := scopeOr404(w, r)
			if !ok {
				return
			}
			c, found := webStaticSiteDBConns.Get(scopeID + "/databaseConnections/" + sim.PathParam(r, "databaseConnectionName"))
			if !found {
				sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"The database connection '%s' was not found.", sim.PathParam(r, "databaseConnectionName"))
				return
			}
			if !withDetails {
				c = redact(c)
			}
			sim.WriteJSON(w, http.StatusOK, c)
		}
	}
	srv.HandleFunc("GET "+pattern+"/databaseConnections", list(false))
	srv.HandleFunc("POST "+pattern+"/showDatabaseConnections", list(true))
	srv.HandleFunc("GET "+pattern+"/databaseConnections/{databaseConnectionName}", get(false))
	srv.HandleFunc("POST "+pattern+"/databaseConnections/{databaseConnectionName}/show", get(true))
	srv.HandleFunc("PUT "+pattern+"/databaseConnections/{databaseConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		scopeID, ok := scopeOr404(w, r)
		if !ok {
			return
		}
		var req DatabaseConnection
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.ResourceID == "" || req.Properties.Region == "" {
			sim.AzureError(w, "BadRequest", "resourceId and region are required.", http.StatusBadRequest)
			return
		}
		name := sim.PathParam(r, "databaseConnectionName")
		c := DatabaseConnection{
			ID:         scopeID + "/databaseConnections/" + name,
			Name:       name,
			Type:       resType,
			Properties: req.Properties,
		}
		webStaticSiteDBConns.Put(c.ID, c)
		sim.WriteJSON(w, http.StatusOK, redact(c))
	})
	srv.HandleFunc("PATCH "+pattern+"/databaseConnections/{databaseConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		scopeID, ok := scopeOr404(w, r)
		if !ok {
			return
		}
		var req DatabaseConnection
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		id := scopeID + "/databaseConnections/" + sim.PathParam(r, "databaseConnectionName")
		if !webStaticSiteDBConns.Update(id, func(c *DatabaseConnection) {
			if req.Properties.ResourceID != "" {
				c.Properties.ResourceID = req.Properties.ResourceID
			}
			if req.Properties.ConnectionIdentity != "" {
				c.Properties.ConnectionIdentity = req.Properties.ConnectionIdentity
			}
			if req.Properties.ConnectionString != "" {
				c.Properties.ConnectionString = req.Properties.ConnectionString
			}
			if req.Properties.Region != "" {
				c.Properties.Region = req.Properties.Region
			}
		}) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The database connection '%s' was not found.", sim.PathParam(r, "databaseConnectionName"))
			return
		}
		c, _ := webStaticSiteDBConns.Get(id)
		sim.WriteJSON(w, http.StatusOK, redact(c))
	})
	srv.HandleFunc("DELETE "+pattern+"/databaseConnections/{databaseConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		scopeID, ok := scopeOr404(w, r)
		if !ok {
			return
		}
		if webStaticSiteDBConns.Delete(scopeID + "/databaseConnections/" + sim.PathParam(r, "databaseConnectionName")) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// staticSiteResolveBackend checks a backendResourceId against the simulator's
// own resource stores — the backend types Static Web Apps can link are App
// Service / Azure Functions sites (Microsoft.Web/sites), Azure Container Apps
// (Microsoft.App/containerApps) and Azure API Management services
// (Microsoft.ApiManagement/service).
func staticSiteResolveBackend(backendID string) (string, bool) {
	lower := strings.ToLower(backendID)
	switch {
	case strings.Contains(lower, "/providers/microsoft.web/sites/"):
		found := azfSites.Filter(func(s Site) bool { return strings.EqualFold(s.ID, backendID) })
		if len(found) == 0 {
			return fmt.Sprintf("The backend '%s' does not exist.", backendID), false
		}
	case strings.Contains(lower, "/providers/microsoft.app/containerapps/"):
		found := acaApps.Filter(func(a ContainerApp) bool { return strings.EqualFold(a.ID, backendID) })
		if len(found) == 0 {
			return fmt.Sprintf("The backend '%s' does not exist.", backendID), false
		}
	case strings.Contains(lower, "/providers/microsoft.apimanagement/service/"):
		found := apimServices.Filter(func(s APIMService) bool { return strings.EqualFold(s.ID, backendID) })
		if len(found) == 0 {
			return fmt.Sprintf("The backend '%s' does not exist.", backendID), false
		}
	default:
		return fmt.Sprintf("The resource type of backend '%s' is not supported for linking to a static site.", backendID), false
	}
	return "", true
}

// registerStaticSiteLinkedBackends mounts the five linked-backend operations
// at one scope (site or build).
func registerStaticSiteLinkedBackends(srv *sim.Server, pattern, scope string) {
	buildScoped := scope != ""
	resType := "Microsoft.Web/staticSites/linkedBackends"
	if buildScoped {
		resType = "Microsoft.Web/staticSites/builds/linkedBackends"
	}
	scopeOr404 := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		if buildScoped {
			b, ok := staticSiteBuildOr404(w, r)
			return b.ID, ok
		}
		ss, ok := staticSiteOr404(w, r)
		return ss.ID, ok
	}
	srv.HandleFunc("GET "+pattern+"/linkedBackends", func(w http.ResponseWriter, r *http.Request) {
		scopeID, ok := scopeOr404(w, r)
		if !ok {
			return
		}
		prefix := scopeID + "/linkedBackends/"
		out := webStaticSiteBackends.Filter(func(b StaticSiteLinkedBackendARMResource) bool { return strings.HasPrefix(b.ID, prefix) })
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		if out == nil {
			out = []StaticSiteLinkedBackendARMResource{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	srv.HandleFunc("GET "+pattern+"/linkedBackends/{linkedBackendName}", func(w http.ResponseWriter, r *http.Request) {
		scopeID, ok := scopeOr404(w, r)
		if !ok {
			return
		}
		b, found := webStaticSiteBackends.Get(scopeID + "/linkedBackends/" + sim.PathParam(r, "linkedBackendName"))
		if !found {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The linked backend '%s' was not found.", sim.PathParam(r, "linkedBackendName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, b)
	})
	srv.HandleFunc("PUT "+pattern+"/linkedBackends/{linkedBackendName}", func(w http.ResponseWriter, r *http.Request) {
		scopeID, ok := scopeOr404(w, r)
		if !ok {
			return
		}
		var req StaticSiteLinkedBackendARMResource
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.BackendResourceID == "" {
			sim.AzureError(w, "BadRequest", "backendResourceId is required.", http.StatusBadRequest)
			return
		}
		if why, ok := staticSiteResolveBackend(req.Properties.BackendResourceID); !ok {
			sim.AzureError(w, "BadRequest", why, http.StatusBadRequest)
			return
		}
		name := sim.PathParam(r, "linkedBackendName")
		b := StaticSiteLinkedBackendARMResource{
			ID:   scopeID + "/linkedBackends/" + name,
			Name: name,
			Type: resType,
			Properties: StaticSiteLinkedBackend{
				BackendResourceID: req.Properties.BackendResourceID,
				Region:            req.Properties.Region,
				CreatedOn:         staticSiteNow(),
				ProvisioningState: "Succeeded",
			},
		}
		webStaticSiteBackends.Put(b.ID, b)
		sim.WriteJSON(w, http.StatusOK, b)
	})
	srv.HandleFunc("DELETE "+pattern+"/linkedBackends/{linkedBackendName}", func(w http.ResponseWriter, r *http.Request) {
		scopeID, ok := scopeOr404(w, r)
		if !ok {
			return
		}
		if webStaticSiteBackends.Delete(scopeID + "/linkedBackends/" + sim.PathParam(r, "linkedBackendName")) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv.HandleFunc("POST "+pattern+"/linkedBackends/{linkedBackendName}/validate", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := scopeOr404(w, r); !ok {
			return
		}
		var req StaticSiteLinkedBackendARMResource
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.BackendResourceID == "" {
			sim.AzureError(w, "BadRequest", "backendResourceId is required.", http.StatusBadRequest)
			return
		}
		if why, ok := staticSiteResolveBackend(req.Properties.BackendResourceID); !ok {
			sim.AzureError(w, "BadRequest", why, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// registerStaticSiteUserProvidedFunctionApps mounts the four user-provided
// function-app operations at one scope (site or build). Registration links an
// EXISTING Microsoft.Web site: the functionAppResourceId is resolved against
// the simulator's real sites store and rejected when the site does not exist,
// as real Azure rejects it.
func registerStaticSiteUserProvidedFunctionApps(srv *sim.Server, pattern, scope string) {
	buildScoped := scope != ""
	resType := "Microsoft.Web/staticSites/userProvidedFunctionApps"
	if buildScoped {
		resType = "Microsoft.Web/staticSites/builds/userProvidedFunctionApps"
	}
	scopeOr404 := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		if buildScoped {
			b, ok := staticSiteBuildOr404(w, r)
			return b.ID, ok
		}
		ss, ok := staticSiteOr404(w, r)
		return ss.ID, ok
	}
	srv.HandleFunc("GET "+pattern+"/userProvidedFunctionApps", func(w http.ResponseWriter, r *http.Request) {
		scopeID, ok := scopeOr404(w, r)
		if !ok {
			return
		}
		prefix := scopeID + "/userProvidedFunctionApps/"
		out := webStaticSiteFnApps.Filter(func(f StaticSiteUserProvidedFunctionAppARMResource) bool { return strings.HasPrefix(f.ID, prefix) })
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		if out == nil {
			out = []StaticSiteUserProvidedFunctionAppARMResource{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})
	srv.HandleFunc("GET "+pattern+"/userProvidedFunctionApps/{functionAppName}", func(w http.ResponseWriter, r *http.Request) {
		scopeID, ok := scopeOr404(w, r)
		if !ok {
			return
		}
		f, found := webStaticSiteFnApps.Get(scopeID + "/userProvidedFunctionApps/" + sim.PathParam(r, "functionAppName"))
		if !found {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The user provided function app '%s' was not found.", sim.PathParam(r, "functionAppName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, f)
	})
	srv.HandleFunc("PUT "+pattern+"/userProvidedFunctionApps/{functionAppName}", func(w http.ResponseWriter, r *http.Request) {
		scopeID, ok := scopeOr404(w, r)
		if !ok {
			return
		}
		var req StaticSiteUserProvidedFunctionAppARMResource
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		fnID := req.Properties.FunctionAppResourceID
		if fnID == "" {
			sim.AzureError(w, "BadRequest", "functionAppResourceId is required.", http.StatusBadRequest)
			return
		}
		resolved := azfSites.Filter(func(s Site) bool { return strings.EqualFold(s.ID, fnID) })
		if len(resolved) == 0 {
			sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"The function app '%s' does not exist.", fnID)
			return
		}
		region := req.Properties.FunctionAppRegion
		if region == "" {
			region = resolved[0].Location
		}
		name := sim.PathParam(r, "functionAppName")
		f := StaticSiteUserProvidedFunctionAppARMResource{
			ID:   scopeID + "/userProvidedFunctionApps/" + name,
			Name: name,
			Type: resType,
			Properties: StaticSiteUserProvidedFunctionAppProperties{
				FunctionAppResourceID: resolved[0].ID,
				FunctionAppRegion:     region,
				CreatedOn:             staticSiteNow(),
			},
		}
		webStaticSiteFnApps.Put(f.ID, f)
		sim.WriteJSON(w, http.StatusOK, f)
	})
	srv.HandleFunc("DELETE "+pattern+"/userProvidedFunctionApps/{functionAppName}", func(w http.ResponseWriter, r *http.Request) {
		scopeID, ok := scopeOr404(w, r)
		if !ok {
			return
		}
		if webStaticSiteFnApps.Delete(scopeID + "/userProvidedFunctionApps/" + sim.PathParam(r, "functionAppName")) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func handleStaticSitePECList(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	prefix := ss.ID + "/privateEndpointConnections/"
	out := webStaticSitePECs.Filter(func(c StaticSiteRemotePrivateEndpointConnection) bool { return strings.HasPrefix(c.ID, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if out == nil {
		out = []StaticSiteRemotePrivateEndpointConnection{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleStaticSitePECGet(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	c, found := webStaticSitePECs.Get(ss.ID + "/privateEndpointConnections/" + sim.PathParam(r, "privateEndpointConnectionName"))
	if !found {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The private endpoint connection '%s' was not found.", sim.PathParam(r, "privateEndpointConnectionName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleStaticSitePECPut(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	var req StaticSiteRemotePrivateEndpointConnection
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	id := ss.ID + "/privateEndpointConnections/" + sim.PathParam(r, "privateEndpointConnectionName")
	props := req.Properties
	if props == nil {
		props = map[string]any{}
	}
	// The owner's approve/reject PUT carries only the connection state; the
	// endpoint linkage established when the connection was created survives.
	if existing, found := webStaticSitePECs.Get(id); found {
		if props["privateEndpoint"] == nil {
			props["privateEndpoint"] = existing.Properties["privateEndpoint"]
		}
		if props["ipAddresses"] == nil && existing.Properties["ipAddresses"] != nil {
			props["ipAddresses"] = existing.Properties["ipAddresses"]
		}
	}
	props["provisioningState"] = "Succeeded"
	c := StaticSiteRemotePrivateEndpointConnection{
		ID:         id,
		Name:       path.Base(id),
		Type:       "Microsoft.Web/staticSites/privateEndpointConnections",
		Properties: props,
	}
	webStaticSitePECs.Put(id, c)
	sim.WriteJSON(w, http.StatusOK, c)
}

func handleStaticSitePECDelete(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	if webStaticSitePECs.Delete(ss.ID + "/privateEndpointConnections/" + sim.PathParam(r, "privateEndpointConnectionName")) {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleStaticSitePrivateLinkResources(w http.ResponseWriter, r *http.Request) {
	ss, ok := staticSiteOr404(w, r)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []map[string]any{{
			"id":   ss.ID + "/privateLinkResources/staticSites",
			"name": "staticSites",
			"type": "Microsoft.Web/staticSites/privateLinkResources",
			"properties": map[string]any{
				"groupId":           "staticSites",
				"requiredMembers":   []string{"staticSites"},
				"requiredZoneNames": []string{"privatelink.azurestaticapps.net"},
			},
		}},
	})
}

func handleStaticSitePreviewWorkflow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Properties struct {
			RepositoryURL   string                     `json:"repositoryUrl"`
			Branch          string                     `json:"branch"`
			BuildProperties *StaticSiteBuildProperties `json:"buildProperties"`
		} `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	p := req.Properties
	if p.RepositoryURL == "" || p.Branch == "" {
		sim.AzureError(w, "BadRequest", "repositoryUrl and branch are required.", http.StatusBadRequest)
		return
	}
	bp := p.BuildProperties
	if bp == nil {
		bp = &StaticSiteBuildProperties{}
	}
	appLocation := bp.AppLocation
	if appLocation == "" {
		appLocation = "/"
	}
	outputLocation := bp.OutputLocation
	if outputLocation == "" {
		outputLocation = bp.AppArtifactLocation
	}
	contents := fmt.Sprintf(`name: Azure Static Web Apps CI/CD

on:
  push:
    branches:
      - %[1]s
  pull_request:
    types: [opened, synchronize, reopened, closed]
    branches:
      - %[1]s

jobs:
  build_and_deploy_job:
    if: github.event_name == 'push' || (github.event_name == 'pull_request' && github.event.action != 'closed')
    runs-on: ubuntu-latest
    name: Build and Deploy Job
    steps:
      - uses: actions/checkout@v3
        with:
          submodules: true
      - name: Build And Deploy
        id: builddeploy
        uses: Azure/static-web-apps-deploy@v1
        with:
          azure_static_web_apps_api_token: ${{ secrets.AZURE_STATIC_WEB_APPS_API_TOKEN }}
          repo_token: ${{ secrets.GITHUB_TOKEN }}
          action: "upload"
          app_location: %[2]q
          api_location: %[3]q
          output_location: %[4]q

  close_pull_request_job:
    if: github.event_name == 'pull_request' && github.event.action == 'closed'
    runs-on: ubuntu-latest
    name: Close Pull Request Job
    steps:
      - name: Close Pull Request
        id: closepullrequest
        uses: Azure/static-web-apps-deploy@v1
        with:
          azure_static_web_apps_api_token: ${{ secrets.AZURE_STATIC_WEB_APPS_API_TOKEN }}
          action: "close"
`, p.Branch, appLocation, bp.APILocation, outputLocation)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"properties": map[string]any{
			"path":     ".github/workflows/azure-static-web-apps.yml",
			"contents": contents,
		},
	})
}
