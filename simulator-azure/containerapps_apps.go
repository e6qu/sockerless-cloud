package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// Container Apps "Apps" slice (Microsoft.App/containerApps). Parallel
// to the Jobs slice in containerapps.go. Required for the
// `Config.UseApp=true` aca code path: when set, sockerless creates
// long-running ContainerApps with internal-only ingress instead of
// short-lived Jobs so peers resolve a stable per-revision FQDN.
//
// Wire format mirrors armappcontainers.ContainerApp (azure-sdk-for-go
// v2). Backend reads `properties.provisioningState`,
// `properties.latestReadyRevisionName`, and
// `properties.latestRevisionFqdn` (used to register a Private DNS
// CNAME), so each of those is populated on Create and Get.
//
// Real API: https://learn.microsoft.com/en-us/rest/api/containerapps/container-apps

// ContainerApp represents a Microsoft.App/containerApps resource.
type ContainerApp struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Identity   *SiteIdentity     `json:"identity,omitempty"`
	Properties ContainerAppProps `json:"properties"`
	SystemData *SystemData       `json:"systemData,omitempty"`
	// SystemIdentityClientID is the application ID of the app's
	// system-assigned identity, which the directory reads and the identity's
	// wire shape does not carry.
	SystemIdentityClientID string `json:"-"`
}

// ContainerAppProps holds the properties of a ContainerApp. Matches
// the field set armappcontainers.ContainerAppProperties exposes that
// the aca backend reads.
type ContainerAppProps struct {
	ProvisioningState string `json:"provisioningState"`
	// RunningStatus is Stopped once the app was stopped and Running
	// otherwise; a write keeps it.
	RunningStatus           string                `json:"runningStatus,omitempty"`
	ManagedEnvironmentID    string                `json:"managedEnvironmentId,omitempty"`
	EnvironmentID           string                `json:"environmentId,omitempty"`
	WorkloadProfileName     string                `json:"workloadProfileName,omitempty"`
	Configuration           *ContainerAppConfig   `json:"configuration,omitempty"`
	Template                *ContainerAppTemplate `json:"template,omitempty"`
	LatestRevisionName      string                `json:"latestRevisionName,omitempty"`
	LatestReadyRevisionName string                `json:"latestReadyRevisionName,omitempty"`
	LatestRevisionFqdn      string                `json:"latestRevisionFqdn,omitempty"`
	EventStreamEndpoint     string                `json:"eventStreamEndpoint,omitempty"`
}

// ContainerAppConfig mirrors armappcontainers.Configuration.
type ContainerAppConfig struct {
	ActiveRevisionsMode  string                         `json:"activeRevisionsMode,omitempty"`
	Dapr                 *ContainerAppDapr              `json:"dapr,omitempty"`
	IdentitySettings     []ContainerAppIdentitySettings `json:"identitySettings,omitempty"`
	Ingress              *ContainerAppIngress           `json:"ingress,omitempty"`
	MaxInactiveRevisions *int32                         `json:"maxInactiveRevisions,omitempty"`
	Registries           []ContainerAppRegistry         `json:"registries,omitempty"`
	Runtime              *ContainerAppRuntime           `json:"runtime,omitempty"`
	Secrets              []ContainerAppSecret           `json:"secrets,omitempty"`
	Service              *ContainerAppService           `json:"service,omitempty"`
}

// ContainerAppDapr mirrors armappcontainers.Dapr. When Enabled is true
// the sim injects a real daprd sidecar into every replica of the app —
// see acaAppDaprContainer in containerapps_dapr.go.
type ContainerAppDapr struct {
	AppID              string `json:"appId,omitempty"`
	AppPort            *int32 `json:"appPort,omitempty"`
	AppProtocol        string `json:"appProtocol,omitempty"`
	EnableAPILogging   *bool  `json:"enableApiLogging,omitempty"`
	Enabled            *bool  `json:"enabled,omitempty"`
	HTTPMaxRequestSize *int32 `json:"httpMaxRequestSize,omitempty"` // MB
	HTTPReadBufferSize *int32 `json:"httpReadBufferSize,omitempty"` // KB
	LogLevel           string `json:"logLevel,omitempty"`
}

// ContainerAppIdentitySettings mirrors armappcontainers.IdentitySettings —
// per-managed-identity lifecycle settings. `identity` is a required member
// (a user-assigned identity resource ID, or "system").
type ContainerAppIdentitySettings struct {
	Identity  string `json:"identity"`
	Lifecycle string `json:"lifecycle,omitempty"`
}

// ContainerAppRuntime mirrors armappcontainers.Runtime. Microsoft.App
// api-version 2025-01-01 declares only the `java` member (`dotnet` exists
// solely in newer preview api-versions the simulator does not serve).
type ContainerAppRuntime struct {
	Java *ContainerAppRuntimeJava `json:"java,omitempty"`
}

// ContainerAppRuntimeJava mirrors armappcontainers.RuntimeJava.
type ContainerAppRuntimeJava struct {
	EnableMetrics *bool `json:"enableMetrics,omitempty"`
}

// ContainerAppService mirrors armappcontainers.Service — the dev
// ContainerApp Service binding. `type` is a required member.
type ContainerAppService struct {
	Type string `json:"type"`
}

// ContainerAppIngress mirrors armappcontainers.Ingress. The backend
// sets External=false + TargetPort=8080 + Transport=auto.
type ContainerAppIngress struct {
	External   *bool                       `json:"external,omitempty"`
	TargetPort *int32                      `json:"targetPort,omitempty"`
	Transport  string                      `json:"transport,omitempty"`
	Fqdn       string                      `json:"fqdn,omitempty"`
	Traffic    []ContainerAppTrafficWeight `json:"traffic,omitempty"`
}

// ContainerAppTrafficWeight mirrors armappcontainers.TrafficWeight: a share of
// the app's ingress traffic, sent to a named revision or to whichever revision
// is latest.
type ContainerAppTrafficWeight struct {
	RevisionName   string `json:"revisionName,omitempty"`
	Weight         *int32 `json:"weight,omitempty"`
	LatestRevision *bool  `json:"latestRevision,omitempty"`
	Label          string `json:"label,omitempty"`
}

// UnmarshalJSON accepts the weight as a JSON number or a numeric string: `az
// containerapp ingress traffic set` sends it as a string.
func (t *ContainerAppTrafficWeight) UnmarshalJSON(data []byte) error {
	type plain ContainerAppTrafficWeight
	var wire struct {
		plain
		Weight json.RawMessage `json:"weight,omitempty"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*t = ContainerAppTrafficWeight(wire.plain)
	t.Weight = nil
	raw := bytes.TrimSpace(wire.Weight)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		raw = []byte(text)
	}
	weight, err := strconv.ParseInt(string(raw), 10, 32)
	if err != nil {
		return fmt.Errorf("traffic weight %s is not an integer", wire.Weight)
	}
	w := int32(weight)
	t.Weight = &w
	return nil
}

// ContainerAppRegistry mirrors armappcontainers.RegistryCredentials.
type ContainerAppRegistry struct {
	Server            string `json:"server,omitempty"`
	Username          string `json:"username,omitempty"`
	PasswordSecretRef string `json:"passwordSecretRef,omitempty"`
	Identity          string `json:"identity,omitempty"`
}

// ContainerAppSecret mirrors armappcontainers.Secret: a value, or the Key
// Vault secret KeyVaultURL names, read as the managed identity Identity names
// (containerapps_identity.go).
type ContainerAppSecret struct {
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	Identity    string `json:"identity,omitempty"`
	KeyVaultURL string `json:"keyVaultUrl,omitempty"`
}

// ContainerAppTemplate mirrors armappcontainers.Template.
type ContainerAppTemplate struct {
	// RevisionSuffix names the revision this template creates; empty lets the
	// platform name it.
	RevisionSuffix string             `json:"revisionSuffix,omitempty"`
	Containers     []JobContainer     `json:"containers,omitempty"`
	InitContainers []JobContainer     `json:"initContainers,omitempty"`
	Volumes        []JobVolume        `json:"volumes,omitempty"`
	Scale          *ContainerAppScale `json:"scale,omitempty"`
	// TerminationGracePeriodSeconds is how long a replica gets between SIGTERM
	// and SIGKILL; nil means the platform default, zero kills at once.
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`
}

// The running statuses of an app.
const (
	acaRunning = "Running"
	acaStopped = "Stopped"
)

// acaDefaultTerminationGrace is what Azure Container Apps grants a replica or
// a job execution between SIGTERM and SIGKILL when the template sets nothing:
// thirty seconds, per the Template schema. The Jobs API exposes no such member,
// so an execution gets the same default.
const acaDefaultTerminationGrace = 30 * time.Second

// acaAppStopGrace is the grace an app's replicas get on stop.
func acaAppStopGrace(app ContainerApp) time.Duration {
	if app.Properties.Template != nil && app.Properties.Template.TerminationGracePeriodSeconds != nil &&
		*app.Properties.Template.TerminationGracePeriodSeconds >= 0 {
		return time.Duration(*app.Properties.Template.TerminationGracePeriodSeconds) * time.Second
	}
	return acaDefaultTerminationGrace
}

// ContainerAppScale mirrors armappcontainers.Scale.
type ContainerAppScale struct {
	MinReplicas     *int32                  `json:"minReplicas,omitempty"`
	MaxReplicas     *int32                  `json:"maxReplicas,omitempty"`
	CooldownPeriod  *int32                  `json:"cooldownPeriod,omitempty"`
	PollingInterval *int32                  `json:"pollingInterval,omitempty"`
	Rules           []ContainerAppScaleRule `json:"rules,omitempty"`
}

// ContainerAppScaleRule mirrors armappcontainers.ScaleRule: a named trigger of
// one kind.
type ContainerAppScaleRule struct {
	Name       string                        `json:"name,omitempty"`
	AzureQueue *ContainerAppQueueScaleRule   `json:"azureQueue,omitempty"`
	Custom     *ContainerAppCustomScaleRule  `json:"custom,omitempty"`
	HTTP       *ContainerAppTriggerScaleRule `json:"http,omitempty"`
	TCP        *ContainerAppTriggerScaleRule `json:"tcp,omitempty"`
}

// ContainerAppTriggerScaleRule mirrors armappcontainers.HTTPScaleRule and
// TCPScaleRule, which share their shape.
type ContainerAppTriggerScaleRule struct {
	Metadata map[string]string           `json:"metadata,omitempty"`
	Auth     []ContainerAppScaleRuleAuth `json:"auth,omitempty"`
	Identity string                      `json:"identity,omitempty"`
}

type ContainerAppCustomScaleRule struct {
	Type     string                      `json:"type,omitempty"`
	Metadata map[string]string           `json:"metadata,omitempty"`
	Auth     []ContainerAppScaleRuleAuth `json:"auth,omitempty"`
	Identity string                      `json:"identity,omitempty"`
}

type ContainerAppQueueScaleRule struct {
	AccountName string                      `json:"accountName,omitempty"`
	QueueName   string                      `json:"queueName,omitempty"`
	QueueLength *int32                      `json:"queueLength,omitempty"`
	Auth        []ContainerAppScaleRuleAuth `json:"auth,omitempty"`
	Identity    string                      `json:"identity,omitempty"`
}

type ContainerAppScaleRuleAuth struct {
	SecretRef        string `json:"secretRef,omitempty"`
	TriggerParameter string `json:"triggerParameter,omitempty"`
}

// ContainerAppAuthToken mirrors armappcontainers.ContainerAppAuthToken
// — a TrackedResource whose properties carry the EasyAuth token the
// getAuthtoken action issues.
type ContainerAppAuthToken struct {
	ID         string                     `json:"id,omitempty"`
	Name       string                     `json:"name,omitempty"`
	Type       string                     `json:"type,omitempty"`
	Location   string                     `json:"location,omitempty"`
	Tags       map[string]string          `json:"tags,omitempty"`
	Properties ContainerAppAuthTokenProps `json:"properties"`
}

// ContainerAppAuthTokenProps mirrors
// armappcontainers.ContainerAppAuthTokenProperties.
type ContainerAppAuthTokenProps struct {
	Token   string `json:"token,omitempty"`
	Expires string `json:"expires,omitempty"`
}

// CustomHostnameAnalysisResult mirrors
// armappcontainers.CustomHostnameAnalysisResult — the
// listCustomHostNameAnalysis action response. Only swagger-declared
// fields are emitted.
type CustomHostnameAnalysisResult struct {
	HostName                            string   `json:"hostName,omitempty"`
	IsHostnameAlreadyVerified           bool     `json:"isHostnameAlreadyVerified"`
	CustomDomainVerificationTest        string   `json:"customDomainVerificationTest,omitempty"`
	HasConflictOnManagedEnvironment     bool     `json:"hasConflictOnManagedEnvironment"`
	ConflictWithEnvironmentCustomDomain bool     `json:"conflictWithEnvironmentCustomDomain"`
	ConflictingContainerAppResourceID   string   `json:"conflictingContainerAppResourceId,omitempty"`
	CNameRecords                        []string `json:"cNameRecords,omitempty"`
	TxtRecords                          []string `json:"txtRecords,omitempty"`
	ARecords                            []string `json:"aRecords,omitempty"`
	AlternateCNameRecords               []string `json:"alternateCNameRecords,omitempty"`
	AlternateTxtRecords                 []string `json:"alternateTxtRecords,omitempty"`
}

var acaApps sim.Store[ContainerApp]

// stampContainerAppServerDefaults applies the defaults real ACA stamps
// server-side on every write. terraform-provider-azurerm reads
// properties.template.scale.cooldownPeriod (default 300) and pollingInterval
// (default 30) and would otherwise drift to 0 on a post-apply plan; the
// ingress FQDN, the revisions mode, maxInactiveRevisions and the traffic split
// that sends everything to the latest revision are likewise server-populated.
func stampContainerAppServerDefaults(app *ContainerApp, fqdn string) {
	if cfg := app.Properties.Configuration; cfg != nil {
		if strings.EqualFold(cfg.ActiveRevisionsMode, "Multiple") {
			cfg.ActiveRevisionsMode = "Multiple"
		} else {
			cfg.ActiveRevisionsMode = "Single"
		}
		if cfg.MaxInactiveRevisions == nil {
			keep := int32(acaDefaultMaxInactiveRevisions)
			cfg.MaxInactiveRevisions = &keep
		}
		if cfg.Ingress != nil {
			cfg.Ingress.Fqdn = fqdn
			if len(cfg.Ingress.Traffic) == 0 {
				latest, all := true, int32(100)
				cfg.Ingress.Traffic = []ContainerAppTrafficWeight{{Weight: &all, LatestRevision: &latest}}
			}
		}
	}
	if app.Properties.Template == nil {
		app.Properties.Template = &ContainerAppTemplate{}
	}
	if app.Properties.Template.Scale == nil {
		app.Properties.Template.Scale = &ContainerAppScale{}
	}
	if app.Properties.Template.Scale.MaxReplicas == nil {
		maxReplicas := int32(acaDefaultMaxReplicas)
		app.Properties.Template.Scale.MaxReplicas = &maxReplicas
	}
	if app.Properties.Template.Scale.CooldownPeriod == nil {
		cooldown := int32(acaDefaultCooldownSeconds)
		app.Properties.Template.Scale.CooldownPeriod = &cooldown
	}
	if app.Properties.Template.Scale.PollingInterval == nil {
		polling := int32(acaDefaultPollingSeconds)
		app.Properties.Template.Scale.PollingInterval = &polling
	}
}

// acaAsyncOpHeaders writes the ARM LRO response headers for a Microsoft.App
// operation: Azure-AsyncOperation → the operationStatuses envelope URL,
// Location → the operationResults URL, plus Retry-After. Both routes are
// served by the shared ARM async-operation handler.
func acaAsyncOpHeaders(w http.ResponseWriter, r *http.Request, sub, loc, opID string) {
	apiVersion := r.URL.Query().Get("api-version")
	writeAzureAsyncCreateHeaders(w, opID,
		azureAsyncOperationHeader(r, sub, "Microsoft.App", loc, "operationStatuses", opID, apiVersion),
		azureAsyncOperationHeader(r, sub, "Microsoft.App", loc, "operationResults", opID, apiVersion))
}

func registerContainerAppsApps(srv *sim.Server) {
	apps := sim.MakeStore[ContainerApp](srv.DB(), "aca_apps")
	acaApps = apps
	acaRevisions = sim.MakeStore[acaRevision](srv.DB(), "aca_app_revisions")
	failInterruptedACAProvisioning()
	registerContainerAppsReplicas(srv, apps)

	const basePath = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App"

	// PUT - Create or update containerApp
	srv.HandleFunc("PUT "+basePath+"/containerApps/{appName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "appName")

		var req ContainerApp
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Location == "" {
			AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s", sub, rg, name)
		existing, exists := apps.Get(resourceID)
		if exists && existing.Properties.ProvisioningState == "Deleting" {
			AzureErrorf(w, "Conflict", http.StatusConflict,
				"Container app '%s' is being deleted and cannot be updated until the delete operation completes.", name)
			return
		}

		// Container Apps answers a PUT with an Azure-AsyncOperation header
		// that the SDK poller follows to the operationStatuses URL before
		// it reads the app. Settle the revision's replicas before answering
		// and issue the operation already Succeeded.
		// The internal FQDN format mirrors real ACA:
		// <app>.internal.<env>.<domain>, and a revision's
		// <app>--<suffix>.internal.<env>.<domain>.
		envName := acaEnvironmentName(req.Properties.EnvironmentID)
		if envName == "" {
			envName = acaEnvironmentName(req.Properties.ManagedEnvironmentID)
		}
		if envName == "" {
			envName = "sim-env"
		}
		appFqdn := azureEndpointHostname(r, name, "internal", envName)

		// Real ARM stamps `createdAt` once on resource creation and only
		// updates `lastModifiedAt` on subsequent PUT/PATCH writes — preserve
		// the original CreatedAt across updates instead of restamping.
		nowStamp := time.Now().UTC().Format(time.RFC3339Nano)
		systemData := &SystemData{
			CreatedAt:      nowStamp,
			LastModifiedAt: nowStamp,
		}
		if exists && existing.SystemData != nil && existing.SystemData.CreatedAt != "" {
			systemData.CreatedAt = existing.SystemData.CreatedAt
		}

		identity, identityClientID, err := acaApplyIdentity(req.Identity, existing.Identity, existing.SystemIdentityClientID)
		if err != nil {
			AzureError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
			return
		}
		app := ContainerApp{
			ID:                     resourceID,
			Name:                   name,
			Type:                   "Microsoft.App/containerApps",
			Location:               req.Location,
			Tags:                   req.Tags,
			Identity:               identity,
			SystemIdentityClientID: identityClientID,
			Properties: ContainerAppProps{
				ProvisioningState:       "Succeeded",
				EnvironmentID:           req.Properties.EnvironmentID,
				ManagedEnvironmentID:    req.Properties.ManagedEnvironmentID,
				WorkloadProfileName:     req.Properties.WorkloadProfileName,
				Configuration:           req.Properties.Configuration,
				Template:                req.Properties.Template,
				LatestRevisionName:      existing.Properties.LatestRevisionName,
				LatestReadyRevisionName: existing.Properties.LatestReadyRevisionName,
				LatestRevisionFqdn:      existing.Properties.LatestRevisionFqdn,
				EventStreamEndpoint: fmt.Sprintf("%s://%s/subscriptions/%s/resourceGroups/%s/containerApps/%s/eventstream",
					azureRequestScheme(r), r.Host, sub, rg, name),
			},
			SystemData: systemData,
		}
		stampContainerAppServerDefaults(&app, appFqdn)

		rev, ok := acaSettleAppIdentity(w, &app, existing, appFqdn)
		if !ok {
			return
		}
		opID := acaStoreProvisioning(&app, rev)
		acaAsyncOpHeaders(w, r, sub, req.Location, opID)

		if exists {
			sim.WriteJSON(w, http.StatusOK, app)
		} else {
			sim.WriteJSON(w, http.StatusCreated, app)
		}
	})

	// GET - Get containerApp
	srv.HandleFunc("GET "+basePath+"/containerApps/{appName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "appName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s", sub, rg, name)
		app, ok := apps.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/containerApps/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		sim.WriteJSON(w, http.StatusOK, app)
	})

	// GET - List containerApps in resource group
	srv.HandleFunc("GET "+basePath+"/containerApps", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/", sub, rg)
		all := apps.Filter(func(a ContainerApp) bool {
			return strings.HasPrefix(a.ID, prefix)
		})
		page, next, pageOK := armPage(w, r, all, func(a ContainerApp) string { return a.Name })
		if !pageOK {
			return
		}
		out := map[string]any{"value": page}
		if next != "" {
			out["nextLink"] = armNextLink(r, next)
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})

	// POST /containerApps/{appName}/listsecrets — real ACA keeps
	// secrets out of GET responses; the dedicated listSecrets POST
	// returns them. terraform-provider-azurerm refreshes via this
	// endpoint on every plan after create.
	// Single lowercase registration; AzurePathNormalizationMiddleware
	// canonicalizes any client casing to lowercase before dispatch.
	srv.HandleFunc("POST "+basePath+"/containerApps/{appName}/listsecrets", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "appName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s", sub, rg, name)
		app, ok := apps.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/containerApps/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		secrets := []ContainerAppSecret{}
		// Keep a non-nil slice when the app has a configuration but no secrets:
		// SecretsCollection.value is a REQUIRED array, so it must serialize as
		// [] not null (a configured-but-secretless app overwrote it otherwise).
		if app.Properties.Configuration != nil && app.Properties.Configuration.Secrets != nil {
			secrets = app.Properties.Configuration.Secrets
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": secrets})
	})

	// DELETE - Delete containerApp. Real Azure ARM returns 202 Accepted
	// with Azure-AsyncOperation + Location headers and an empty body; the
	// resource stays observable in provisioningState=Deleting until the
	// operation completes, then the final GET returns 404.
	srv.HandleFunc("DELETE "+basePath+"/containerApps/{appName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "appName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s", sub, rg, name)
		app, ok := apps.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/containerApps/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		app.Properties.ProvisioningState = "Deleting"
		apps.Put(resourceID, app)
		opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
			// A concurrent PUT is rejected with 409 while the state is
			// Deleting, so the record present here is still the one this
			// operation owns.
			apps.Delete(resourceID)
			syncManagedIdentityPrincipal(app.Identity, nil, "", "")
			deleteACAAppRevisions(resourceID)
			acaAppSystemLogs.Delete(resourceID)
			return nil
		})
		acaAsyncOpHeaders(w, r, sub, app.Location, opID)
		w.WriteHeader(http.StatusAccepted)
	})

	// GET - List containerApps by subscription. ARM exposes a
	// subscription-wide list alongside the resource-group-scoped one.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.App/containerApps", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		prefix := fmt.Sprintf("/subscriptions/%s/", sub)
		all := apps.Filter(func(a ContainerApp) bool {
			return strings.HasPrefix(a.ID, prefix) && strings.Contains(a.ID, "/providers/Microsoft.App/containerApps/")
		})
		page, next, pageOK := armPage(w, r, all, func(a ContainerApp) string { return azureNameIDKey(a.Name, a.ID) })
		if !pageOK {
			return
		}
		out := map[string]any{"value": page}
		if next != "" {
			out["nextLink"] = armNextLink(r, next)
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})

	// PATCH - Update containerApp: 202 with the operation while a revision
	// the patch creates provisions, else 200 with the app.
	srv.HandleFunc("PATCH "+basePath+"/containerApps/{appName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "appName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s", sub, rg, name)
		app, ok := apps.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/containerApps/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		if app.Properties.ProvisioningState == "Deleting" {
			AzureErrorf(w, "Conflict", http.StatusConflict,
				"Container app '%s' is being deleted and cannot be updated until the delete operation completes.", name)
			return
		}

		patch, err := io.ReadAll(r.Body)
		if err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to read request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		prior := app
		var patchIdentity struct {
			Identity *SiteIdentity `json:"identity"`
		}
		if err := json.Unmarshal(patch, &patchIdentity); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := applyARMMergePatch(&app, patch); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		app.Identity, app.SystemIdentityClientID, err = acaApplyIdentity(patchIdentity.Identity, prior.Identity, prior.SystemIdentityClientID)
		if err != nil {
			AzureError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
			return
		}
		// Identity and server-owned fields are not client-writable.
		app.ID, app.Name, app.Type, app.Location = prior.ID, prior.Name, prior.Type, prior.Location
		app.SystemData = prior.SystemData
		app.Properties.ProvisioningState = prior.Properties.ProvisioningState
		app.Properties.LatestRevisionName = prior.Properties.LatestRevisionName
		app.Properties.LatestReadyRevisionName = prior.Properties.LatestReadyRevisionName
		app.Properties.LatestRevisionFqdn = prior.Properties.LatestRevisionFqdn
		app.Properties.EventStreamEndpoint = prior.Properties.EventStreamEndpoint
		appFqdn := acaAppFqdn(prior)
		stampContainerAppServerDefaults(&app, appFqdn)
		if app.SystemData != nil {
			app.SystemData.LastModifiedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}

		rev, ok := acaSettleAppIdentity(w, &app, prior, appFqdn)
		if !ok {
			return
		}
		opID := acaStoreProvisioning(&app, rev)
		if app.Properties.ProvisioningState == acaProvisioningInProgress {
			acaAsyncOpHeaders(w, r, sub, app.Location, opID)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		sim.WriteJSON(w, http.StatusOK, app)
	})

	// POST /containerApps/{appName}/start — start the app's revisions.
	// LRO whose body poller terminates on provisioningState=Succeeded.
	srv.HandleFunc("POST "+basePath+"/containerApps/{appName}/start", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "appName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s", sub, rg, name)
		app, ok := apps.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/containerApps/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		app.Properties.RunningStatus = acaRunning
		apps.Put(resourceID, app)
		if err := startACAAppRevisions(r.Context(), app); err != nil {
			AzureErrorf(w, "ContainerAppRevisionFailed", http.StatusInternalServerError,
				"failed to start container app replica for %s: %v", name, err)
			return
		}
		sim.WriteJSON(w, http.StatusOK, app)
	})

	// POST /containerApps/{appName}/stop — stop the app's revisions.
	srv.HandleFunc("POST "+basePath+"/containerApps/{appName}/stop", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "appName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s", sub, rg, name)
		app, ok := apps.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/containerApps/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		app.Properties.RunningStatus = acaStopped
		apps.Put(resourceID, app)
		stopACAAppRevisions(resourceID)
		sim.WriteJSON(w, http.StatusOK, app)
	})

	// POST /containerApps/{appName}/getAuthtoken — issue the token the
	// app's log streams accept. The SDK spells the segment `getAuthtoken`
	// and the Azure CLI `getAuthToken`; azureNormalizeRequestPath folds both.
	srv.HandleFunc("POST "+basePath+"/containerApps/{appName}/getAuthtoken", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "appName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s", sub, rg, name)
		app, ok := apps.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/containerApps/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		expires := time.Now().Add(8 * time.Hour).UTC()
		token := ContainerAppAuthToken{
			ID:       resourceID,
			Name:     name,
			Type:     "Microsoft.App/containerApps",
			Location: app.Location,
			Properties: ContainerAppAuthTokenProps{
				Token:   sim.NewUUID(),
				Expires: expires.Format(time.RFC3339),
			},
		}
		acaAppAuthTokens.Store(token.Properties.Token, acaIssuedToken{resourceID: resourceID, expires: expires})
		sim.WriteJSON(w, http.StatusOK, token)
	})

	// POST /containerApps/{appName}/listCustomHostNameAnalysis — analyze
	// a custom hostname's DNS binding. The SDK sends the mixed-case
	// segment verbatim.
	srv.HandleFunc("POST "+basePath+"/containerApps/{appName}/listCustomHostNameAnalysis", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "appName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s", sub, rg, name)
		if _, ok := apps.Get(resourceID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/containerApps/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		result := CustomHostnameAnalysisResult{
			HostName:                            r.URL.Query().Get("customHostname"),
			IsHostnameAlreadyVerified:           false,
			CustomDomainVerificationTest:        "Skipped",
			HasConflictOnManagedEnvironment:     false,
			ConflictWithEnvironmentCustomDomain: false,
		}
		sim.WriteJSON(w, http.StatusOK, result)
	})
}

const acaProvisioningInProgress = "InProgress"

// failInterruptedACAProvisioning settles what an earlier process left
// provisioning: its operation fails at restart, and so do the revision and
// the app it provisioned.
func failInterruptedACAProvisioning() {
	for _, rev := range acaRevisions.List() {
		if rev.ProvisioningState == acaRevisionProvisioning {
			acaRevisions.Update(rev.ID, func(r *acaRevision) {
				r.ProvisioningState = acaRevisionFailed
				r.Active = false
				r.LastActiveTime = time.Now().UTC()
			})
		}
	}
	for _, app := range acaApps.List() {
		if app.Properties.ProvisioningState == acaProvisioningInProgress {
			acaApps.Update(app.ID, func(a *ContainerApp) { a.Properties.ProvisioningState = "Failed" })
		}
	}
}

// acaStoreProvisioning stores a written app and returns the operation its
// write answers with: InProgress while a revision it created provisions, or
// one it created before still does, and settled at once otherwise.
func acaStoreProvisioning(app *ContainerApp, rev *acaRevision) string {
	if rev != nil {
		app.Properties.ProvisioningState = acaProvisioningInProgress
		acaApps.Put(app.ID, *app)
		appID, created := app.ID, *rev
		return startAzureAsyncOperationOutcome(func() *AsyncOperationError {
			return acaProvisionRevision(appID, created)
		})
	}
	if latest, ok := acaAppRevision(app.ID, app.Properties.LatestRevisionName); ok && latest.ProvisioningState == acaRevisionProvisioning {
		app.Properties.ProvisioningState = acaProvisioningInProgress
	}
	acaApps.Put(app.ID, *app)
	return issueAzureAsyncOperation(nil)
}

// acaSettleAppIdentity validates an app's secrets against its identity, brings
// the directory in step with its system-assigned identity and reconciles its
// revisions, returning the revision the write creates; it answers the request
// and restores the directory when any of it fails.
func acaSettleAppIdentity(w http.ResponseWriter, app *ContainerApp, prev ContainerApp, appFqdn string) (*acaRevision, bool) {
	secrets := acaAppSecrets(*app)
	var containers []JobContainer
	if app.Properties.Template != nil {
		containers = append(append(containers, app.Properties.Template.InitContainers...), app.Properties.Template.Containers...)
	}
	if err := acaValidateSecretRefs(containers, secrets); err != nil {
		AzureError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return nil, false
	}
	if err := acaValidateProbes(app.Properties.Template); err != nil {
		AzureError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return nil, false
	}
	syncManagedIdentityPrincipal(prev.Identity, app.Identity, app.SystemIdentityClientID, app.Name)
	if _, err := acaResolveSecrets(app.Identity, secrets); err != nil {
		syncManagedIdentityPrincipal(app.Identity, prev.Identity, prev.SystemIdentityClientID, prev.Name)
		AzureError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return nil, false
	}
	app.Properties.RunningStatus = prev.Properties.RunningStatus
	if app.Properties.RunningStatus == "" {
		app.Properties.RunningStatus = acaRunning
	}
	rev, err := acaReconcileRevisions(app, appFqdn)
	if err != nil {
		syncManagedIdentityPrincipal(app.Identity, prev.Identity, prev.SystemIdentityClientID, prev.Name)
		AzureError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return rev, true
}

func acaEnvironmentName(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func acaAppContainer(app ContainerApp, c JobContainer, replica string, envID string, metadataEnv, secrets map[string]string) workload.Container {
	cmdEnv := acaContainerEnv(c, secrets)
	if _, ok := cmdEnv["PORT"]; !ok {
		cmdEnv["PORT"] = "8080"
	}

	var binds []string
	if app.Properties.Template != nil && len(app.Properties.Template.Volumes) > 0 && len(c.VolumeMounts) > 0 {
		volByName := make(map[string]JobVolume, len(app.Properties.Template.Volumes))
		for _, v := range app.Properties.Template.Volumes {
			volByName[v.Name] = v
		}
		for _, mp := range c.VolumeMounts {
			v, ok := volByName[mp.VolumeName]
			if !ok {
				continue
			}
			if !strings.EqualFold(v.StorageType, "AzureFile") || v.StorageName == "" {
				continue
			}
			acct, share, found := LookupEnvStorageBinding(envID, v.StorageName)
			if !found {
				continue
			}
			binds = append(binds, FileShareHostDir(acct, share)+":"+mp.MountPath)
		}
	}

	shortName := app.Name
	if len(shortName) > 24 {
		shortName = shortName[:24]
	}
	containerName := fmt.Sprintf("sockerless-sim-azure-app-%s-%s-%s", shortName, replica[strings.LastIndex(replica, "-")+1:], c.Name)
	// The host pulls the replica's image with the credential the app
	// declared for its registry, as Container Apps does.
	return workload.Container{Name: c.Name, Config: sim.ContainerConfig{
		CancelGracePeriod: acaAppStopGrace(app),
		Image:             sim.ResolveLocalImage(c.Image),
		RegistryAuth:      acrWorkloadRegistryAuth(c.Image, acaAppWorkloadRegistries(app, secrets)),
		Command:           c.Command,
		Args:              c.Args,
		Env:               workloadhost.MergeEnv(cmdEnv, metadataEnv),
		Name:              containerName,
		Labels: map[string]string{
			"sockerless-sim-type": "aca-app-replica",
			"sockerless-app-id":   app.ID,
			"sockerless-app-name": app.Name,
		},
		Binds:   binds,
		Sandbox: SandboxACA,
	}}
}

// acaAppLogSink writes an app container's output to the
// ContainerAppConsoleLogs_CL of the workspace the app's environment names.
type acaAppLogSink struct {
	app       ContainerApp
	container string
}

func (s *acaAppLogSink) WriteLog(line sim.LogLine) {
	acaConsoleLog(acaAppEnvironmentID(s.app), s.app.ID, line, monitorLogRow{
		"ContainerAppName_s": s.app.Name,
		"ContainerName_s":    s.container,
	})
}
