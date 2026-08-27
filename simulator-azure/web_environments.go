package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"path"
	"sort"
	"strings"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// App Service Environments (Microsoft.Web/hostingEnvironments) and Kubernetes
// Environments (Microsoft.Web/kubeEnvironments).
//
// An App Service Environment here is a real placement scope, not a label: it
// occupies a Microsoft.Network subnet that must exist in the simulator's own
// network slice, it leases its inbound and outbound addresses from the same
// public-IPv4 lease pool Microsoft.Network/publicIPAddresses reserves from and
// derives its internal address from that subnet's prefix, and the App Service
// plans and sites that name it are read back through it. Everything the
// environment reports about itself is either what the client configured or
// what the simulator computed from those resources:
//
//   - multiRoleCount is the front-end pool's worker count;
//   - hasLinuxWorkers is true when an App Service plan placed in the
//     environment is a Linux (reserved) plan;
//   - the stamp capacities are the pools' worker counts less the capacity the
//     plans placed in the environment have already taken;
//   - the inbound network dependencies are the addresses and ports the
//     environment's own networking configuration exposes.
//
// Five documented operations are not served, and each answers a declared
// 501 gap naming the reason rather than falling through to a bare mux 404:
//
//   - ListMultiRoleMetricDefinitions, ListMultiRolePoolInstanceMetricDefinitions,
//     ListWebWorkerMetricDefinitions and ListWorkerPoolInstanceMetricDefinitions.
//     A metric definition is a promise that the named metric exists and can be
//     read at the declared time grains. The simulator publishes no
//     Microsoft.Insights metric series for a front-end or worker pool — the
//     pools are a placement model here, not a fleet of virtual machines
//     emitting counters — so declaring definitions would advertise series no
//     client could ever read.
//   - GetOutboundNetworkDependenciesEndpoints. Its answer is Microsoft's
//     published catalog of the platform endpoints an environment reaches
//     outbound (App Service management, Azure SQL, Azure Storage, Azure
//     Monitor, …, with their per-region address ranges). That is somebody
//     else's published data, exactly like the Provider_*Stacks runtime-stack
//     catalog this repository has declined to invent; a partial list would be
//     a fabricated network contract. The inbound half IS served, because it is
//     computed from the environment's own subnet, addresses and feature flags.

// wire types

// HostingEnvironmentProfile is the App Service Environment reference an App
// Service plan or a site carries (the swagger's HostingEnvironmentProfile:
// a settable id plus the read-only name and type the service resolves).
type HostingEnvironmentProfile struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

// VirtualNetworkProfile is the swagger VirtualNetworkProfile: the subnet (or
// virtual network) resource id the environment occupies, plus the read-only
// name and type the service resolves from it.
type VirtualNetworkProfile struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Type   string `json:"type,omitempty"`
	Subnet string `json:"subnet,omitempty"`
}

// AppServiceEnvironmentResource is the swagger AppServiceEnvironmentResource:
// a tracked ARM resource whose properties describe the environment.
type AppServiceEnvironmentResource struct {
	ID         string                          `json:"id,omitempty"`
	Name       string                          `json:"name,omitempty"`
	Type       string                          `json:"type,omitempty"`
	Kind       string                          `json:"kind,omitempty"`
	Location   string                          `json:"location,omitempty"`
	Tags       map[string]string               `json:"tags,omitempty"`
	Properties AppServiceEnvironmentProperties `json:"properties"`
}

// AppServiceEnvironmentProperties holds exactly the members the swagger's
// AppServiceEnvironment definition declares. Members the simulator does not
// model — the maximum machine count it enforces no limit for, the front-end VM
// size it runs no front-end machines for — are absent rather than invented, so
// a client reads back only what was configured or genuinely computed.
type AppServiceEnvironmentProperties struct {
	ProvisioningState         string                 `json:"provisioningState,omitempty"`
	Status                    string                 `json:"status,omitempty"`
	VirtualNetwork            *VirtualNetworkProfile `json:"virtualNetwork,omitempty"`
	InternalLoadBalancingMode string                 `json:"internalLoadBalancingMode,omitempty"`
	MultiSize                 string                 `json:"multiSize,omitempty"`
	MultiRoleCount            *int32                 `json:"multiRoleCount,omitempty"`
	IpsslAddressCount         *int32                 `json:"ipsslAddressCount,omitempty"`
	DNSSuffix                 string                 `json:"dnsSuffix,omitempty"`
	FrontEndScaleFactor       *int32                 `json:"frontEndScaleFactor,omitempty"`
	Suspended                 *bool                  `json:"suspended,omitempty"`
	ClusterSettings           []NameValuePair        `json:"clusterSettings,omitempty"`
	UserWhitelistedIPRanges   []string               `json:"userWhitelistedIpRanges,omitempty"`
	HasLinuxWorkers           *bool                  `json:"hasLinuxWorkers,omitempty"`
	UpgradePreference         string                 `json:"upgradePreference,omitempty"`
	DedicatedHostCount        *int32                 `json:"dedicatedHostCount,omitempty"`
	ZoneRedundant             *bool                  `json:"zoneRedundant,omitempty"`

	CustomDNSSuffixConfiguration *ASEConfigurationResource `json:"customDnsSuffixConfiguration,omitempty"`
	NetworkingConfiguration      *ASEConfigurationResource `json:"networkingConfiguration,omitempty"`
	UpgradeAvailability          string                    `json:"upgradeAvailability,omitempty"`
}

// ASEConfigurationResource is the shared envelope of the two configuration
// children an environment carries inline on its own read — the swagger's
// CustomDnsSuffixConfiguration and AseV3NetworkingConfiguration, both
// ProxyResources whose properties are flattened by the SDK.
type ASEConfigurationResource struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	Type       string `json:"type,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Properties any    `json:"properties,omitempty"`
}

// ASENetworkingProperties is the swagger AseV3NetworkingConfigurationProperties.
// The four address lists are read-only: the simulator fills them from the
// leases it reserved for the environment and the address it derived from its
// subnet, never from client input.
type ASENetworkingProperties struct {
	WindowsOutboundIPAddresses         []string `json:"windowsOutboundIpAddresses"`
	LinuxOutboundIPAddresses           []string `json:"linuxOutboundIpAddresses"`
	ExternalInboundIPAddresses         []string `json:"externalInboundIpAddresses"`
	InternalInboundIPAddresses         []string `json:"internalInboundIpAddresses"`
	AllowNewPrivateEndpointConnections *bool    `json:"allowNewPrivateEndpointConnections,omitempty"`
	FtpEnabled                         *bool    `json:"ftpEnabled,omitempty"`
	RemoteDebugEnabled                 *bool    `json:"remoteDebugEnabled,omitempty"`
	InboundIPAddressOverride           string   `json:"inboundIpAddressOverride,omitempty"`
}

// ASECustomDNSSuffixProperties is the swagger
// CustomDnsSuffixConfigurationProperties.
type ASECustomDNSSuffixProperties struct {
	ProvisioningState         string `json:"provisioningState,omitempty"`
	ProvisioningDetails       string `json:"provisioningDetails,omitempty"`
	DNSSuffix                 string `json:"dnsSuffix,omitempty"`
	CertificateURL            string `json:"certificateUrl,omitempty"`
	KeyVaultReferenceIdentity string `json:"keyVaultReferenceIdentity,omitempty"`
}

// WebWorkerPoolResource is the swagger WorkerPoolResource — the front-end
// ("multiRolePools/default") and worker pools of an environment.
type WebWorkerPoolResource struct {
	ID         string                  `json:"id,omitempty"`
	Name       string                  `json:"name,omitempty"`
	Type       string                  `json:"type,omitempty"`
	Kind       string                  `json:"kind,omitempty"`
	Sku        *AppServicePlanSku      `json:"sku,omitempty"`
	Properties WebWorkerPoolProperties `json:"properties"`
}

// WebWorkerPoolProperties is the swagger WorkerPool. instanceNames is
// read-only and reports the pool's instances, which the simulator names the
// way the App Service platform does.
type WebWorkerPoolProperties struct {
	WorkerSizeID  *int32   `json:"workerSizeId,omitempty"`
	ComputeMode   string   `json:"computeMode,omitempty"`
	WorkerSize    string   `json:"workerSize,omitempty"`
	WorkerCount   *int32   `json:"workerCount,omitempty"`
	InstanceNames []string `json:"instanceNames,omitempty"`
}

// KubeEnvironmentResource is the swagger KubeEnvironment.
type KubeEnvironmentResource struct {
	ID               string                    `json:"id,omitempty"`
	Name             string                    `json:"name,omitempty"`
	Type             string                    `json:"type,omitempty"`
	Kind             string                    `json:"kind,omitempty"`
	Location         string                    `json:"location,omitempty"`
	Tags             map[string]string         `json:"tags,omitempty"`
	ExtendedLocation *KubeExtendedLocation     `json:"extendedLocation,omitempty"`
	Properties       KubeEnvironmentProperties `json:"properties"`
}

// KubeExtendedLocation is the swagger ExtendedLocation.
type KubeExtendedLocation struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

// KubeEnvironmentProperties is the swagger KubeEnvironmentProperties. The
// cluster-configuration documents are carried as property bags so every member
// a client sends round-trips unchanged; the Arc kubeConfig is a declared
// secret and is dropped on read.
type KubeEnvironmentProperties struct {
	ProvisioningState           string         `json:"provisioningState,omitempty"`
	DeploymentErrors            string         `json:"deploymentErrors,omitempty"`
	InternalLoadBalancerEnabled *bool          `json:"internalLoadBalancerEnabled,omitempty"`
	DefaultDomain               string         `json:"defaultDomain,omitempty"`
	StaticIP                    string         `json:"staticIp,omitempty"`
	EnvironmentType             string         `json:"environmentType,omitempty"`
	ArcConfiguration            map[string]any `json:"arcConfiguration,omitempty"`
	AppLogsConfiguration        map[string]any `json:"appLogsConfiguration,omitempty"`
	ContainerAppsConfiguration  map[string]any `json:"containerAppsConfiguration,omitempty"`
	AksResourceID               string         `json:"aksResourceID,omitempty"`
}

// stores

var (
	webHostingEnvironments sim.Store[AppServiceEnvironmentResource]
	webEnvironmentPools    sim.Store[WebWorkerPoolResource]
	webEnvironmentPECs     sim.Store[WebSitePrivateEndpointConnection]
	webKubeEnvironments    sim.Store[KubeEnvironmentResource]
)

const (
	aseResourceType  = "Microsoft.Web/hostingEnvironments"
	kubeResourceType = "Microsoft.Web/kubeEnvironments"
	// multiRolePoolName is the only name the front-end pool has: the
	// specification pins the path segment to the literal "default".
	multiRolePoolName = "default"
)

func int32Ptr(v int32) *int32 { return &v }
func boolPtr(v bool) *bool    { return &v }

func int32Value(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

// identifiers and lookups

func aseResourceID(r *http.Request) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/hostingEnvironments/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
}

func kubeResourceID(r *http.Request) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/kubeEnvironments/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
}

// aseLookup resolves the environment the request addresses, writing ARM's 404
// when it does not exist.
func aseLookup(w http.ResponseWriter, r *http.Request) (AppServiceEnvironmentResource, bool) {
	ase, ok := webHostingEnvironments.Get(aseResourceID(r))
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Web/hostingEnvironments/%s' under resource group '%s' was not found.",
			sim.PathParam(r, "name"), sim.PathParam(r, "resourceGroupName"))
		return AppServiceEnvironmentResource{}, false
	}
	return ase, true
}

// aseByID resolves an environment by resource id, case-insensitively as ARM
// compares resource ids.
func aseByID(id string) (AppServiceEnvironmentResource, bool) {
	if id == "" {
		return AppServiceEnvironmentResource{}, false
	}
	if ase, ok := webHostingEnvironments.Get(id); ok {
		return ase, true
	}
	for _, ase := range webHostingEnvironments.List() {
		if strings.EqualFold(ase.ID, id) {
			return ase, true
		}
	}
	return AppServiceEnvironmentResource{}, false
}

// asePlans are the App Service plans placed in the environment: the plans
// whose hostingEnvironmentProfile names it.
func asePlans(aseID string) []AppServicePlan {
	return azureAppServicePlans.Filter(func(p AppServicePlan) bool {
		return p.Properties.HostingEnvironmentProfile != nil &&
			strings.EqualFold(p.Properties.HostingEnvironmentProfile.ID, aseID)
	})
}

// aseSites are the apps running in the environment: the sites that name it
// directly, plus the sites whose App Service plan is placed in it.
func aseSites(aseID string) []Site {
	planIDs := map[string]bool{}
	for _, plan := range asePlans(aseID) {
		planIDs[strings.ToLower(plan.ID)] = true
	}
	return azfSites.Filter(func(s Site) bool {
		if s.Properties.HostingEnvironmentProfile != nil &&
			strings.EqualFold(s.Properties.HostingEnvironmentProfile.ID, aseID) {
			return true
		}
		return planIDs[strings.ToLower(s.Properties.ServerFarmID)]
	})
}

// webResolveHostingEnvironmentProfile resolves the App Service Environment an
// App Service plan or a site is placed in, filling the read-only name and type
// the service reports beside the id. A reference to an environment that does
// not exist is refused rather than stored.
func webResolveHostingEnvironmentProfile(req *HostingEnvironmentProfile) (*HostingEnvironmentProfile, error) {
	if req == nil || req.ID == "" {
		return nil, nil
	}
	ase, ok := aseByID(req.ID)
	if !ok {
		return nil, fmt.Errorf("the App Service Environment %q does not exist", req.ID)
	}
	return &HostingEnvironmentProfile{ID: ase.ID, Name: ase.Name, Type: ase.Type}, nil
}

// addresses

// aseInternalInboundAddress is the address an internal-load-balancing
// environment answers on inside its own subnet. Azure reserves the first four
// addresses of every subnet, so the first address it can assign is the fifth —
// and an App Service Environment subnet is delegated to the environment alone,
// so nothing else is competing for it.
func aseInternalInboundAddress(subnetPrefix string) (string, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(subnetPrefix))
	if err != nil {
		return "", fmt.Errorf("subnet address prefix %q is not a CIDR: %w", subnetPrefix, err)
	}
	addr := prefix.Masked().Addr()
	for i := 0; i < 4; i++ {
		addr = addr.Next()
		if !addr.IsValid() || !prefix.Contains(addr) {
			return "", fmt.Errorf("subnet address prefix %q is too small to host an App Service Environment", subnetPrefix)
		}
	}
	return addr.String(), nil
}

// aseSubnetPrefix reads the address prefix of the subnet the environment
// occupies out of the simulator's own Microsoft.Network store.
func aseSubnetPrefix(subnet Subnet) string {
	if subnet.Properties.AddressPrefix != "" {
		return subnet.Properties.AddressPrefix
	}
	if len(subnet.Properties.AddressPrefixes) > 0 {
		return subnet.Properties.AddressPrefixes[0]
	}
	return ""
}

// aseIsInternal reports an environment whose endpoints are served inside its
// virtual network — every load-balancing mode other than "None" publishes at
// least one endpoint internally.
func aseIsInternal(ase AppServiceEnvironmentResource) bool {
	mode := strings.TrimSpace(ase.Properties.InternalLoadBalancingMode)
	return mode != "" && !strings.EqualFold(mode, "None")
}

// aseNetworkingProperties reads the environment's networking configuration
// off its own record.
func aseNetworkingProperties(ase AppServiceEnvironmentResource) ASENetworkingProperties {
	var props ASENetworkingProperties
	if ase.Properties.NetworkingConfiguration == nil {
		return props
	}
	// The record round-trips through the store's JSON encoding, so the
	// property bag comes back as a decoded document rather than the struct.
	raw, err := json.Marshal(ase.Properties.NetworkingConfiguration.Properties)
	if err != nil {
		return props
	}
	_ = json.Unmarshal(raw, &props)
	return props
}

// aseInboundAddresses are the addresses clients reach the environment at:
// its internal subnet address when it load-balances internally, its leased
// public address otherwise.
func aseInboundAddresses(ase AppServiceEnvironmentResource) []string {
	net := aseNetworkingProperties(ase)
	if len(net.InternalInboundIPAddresses) > 0 {
		return net.InternalInboundIPAddresses
	}
	return net.ExternalInboundIPAddresses
}

// projection

// aseDNSSuffix is the domain the apps in the environment are published under.
// An internal-load-balancing environment gets Microsoft's default internal
// suffix `<name>.appserviceenvironment.net`; an externally load-balanced one
// gets `<name>.p.azurewebsites.net`, the suffix the specification's own
// AppServiceEnvironments_Get example carries.
func aseDNSSuffix(name string, internal bool) string {
	if internal {
		return name + ".appserviceenvironment.net"
	}
	return name + ".p.azurewebsites.net"
}

// aseProject fills the read-only members the service computes, so a read
// reports the environment's current truth rather than the state frozen at
// create time.
func aseProject(ase AppServiceEnvironmentResource) AppServiceEnvironmentResource {
	ase.Properties.MultiRoleCount = nil
	if pool, ok := webEnvironmentPools.Get(ase.ID + "/multiRolePools/" + multiRolePoolName); ok {
		ase.Properties.MultiRoleCount = pool.Properties.WorkerCount
	}
	linux := false
	for _, plan := range asePlans(ase.ID) {
		if plan.Properties.Reserved {
			linux = true
			break
		}
	}
	ase.Properties.HasLinuxWorkers = boolPtr(linux)
	return ase
}

// aseConfigurationChild renders one of the environment's configuration
// children at its own resource coordinates.
func aseConfigurationChild(aseID, segment, name string, props any) *ASEConfigurationResource {
	return &ASEConfigurationResource{
		ID:         aseID + "/configurations/" + segment,
		Name:       name,
		Type:       aseResourceType + "/configurations/" + segment,
		Properties: props,
	}
}

// registration

func registerWebEnvironments(srv *sim.Server) {
	webHostingEnvironments = sim.MakeStore[AppServiceEnvironmentResource](srv.DB(), "web_hosting_environments")
	webEnvironmentPools = sim.MakeStore[WebWorkerPoolResource](srv.DB(), "web_environment_pools")
	webEnvironmentPECs = sim.MakeStore[WebSitePrivateEndpointConnection](srv.DB(), "web_environment_private_endpoint_connections")
	webKubeEnvironments = sim.MakeStore[KubeEnvironmentResource](srv.DB(), "web_kube_environments")

	ase := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/hostingEnvironments/{name}"+suffix, h)
	}

	registerWebEnvironmentCRUD(srv, ase)
	registerWebEnvironmentNetworking(ase)
	registerWebEnvironmentPools(ase)
	registerWebEnvironmentInventory(ase)
	registerWebEnvironmentLifecycle(ase)
	registerWebEnvironmentPrivateEndpoints(ase)
	registerWebEnvironmentDeclaredGaps(ase)
	registerWebEnvironmentDiagnostics(ase)
	registerWebKubeEnvironments(srv)
}

// registerWebEnvironmentCRUD mounts AppServiceEnvironments_CreateOrUpdate,
// _Get, _Update, _Delete, _List and _ListByResourceGroup.
func registerWebEnvironmentCRUD(srv *sim.Server, ase func(string, string, http.HandlerFunc)) {
	ase("PUT", "", func(w http.ResponseWriter, r *http.Request) {
		var req AppServiceEnvironmentResource
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := aseResourceID(r)
		name := sim.PathParam(r, "name")
		existing, updating := webHostingEnvironments.Get(id)

		if req.Location == "" && !updating {
			sim.AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}
		if req.Properties.VirtualNetwork == nil || req.Properties.VirtualNetwork.ID == "" {
			sim.AzureError(w, "InvalidRequestContent",
				"The 'properties.virtualNetwork.id' property is required: an App Service Environment is deployed into a subnet.",
				http.StatusBadRequest)
			return
		}
		profile, subnetPrefix, err := aseResolveVirtualNetwork(*req.Properties.VirtualNetwork)
		if err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}

		next := AppServiceEnvironmentResource{
			ID:         id,
			Name:       name,
			Type:       aseResourceType,
			Kind:       req.Kind,
			Location:   req.Location,
			Tags:       req.Tags,
			Properties: req.Properties,
		}
		if updating {
			if next.Location == "" {
				next.Location = existing.Location
			}
			if next.Kind == "" {
				next.Kind = existing.Kind
			}
		}
		next.Properties.VirtualNetwork = &profile
		next.Properties.ProvisioningState = "Succeeded"
		next.Properties.Status = "Ready"
		next.Properties.Suspended = boolPtr(false)
		next.Properties.UpgradeAvailability = "None"
		if next.Properties.UpgradePreference == "" {
			next.Properties.UpgradePreference = "None"
		}
		if updating {
			next.Properties.Suspended = existing.Properties.Suspended
			next.Properties.UpgradeAvailability = existing.Properties.UpgradeAvailability
			next.Properties.CustomDNSSuffixConfiguration = existing.Properties.CustomDNSSuffixConfiguration
		} else {
			next.Properties.CustomDNSSuffixConfiguration = nil
		}
		if next.Properties.DNSSuffix == "" {
			next.Properties.DNSSuffix = aseDNSSuffix(name, aseIsInternal(next))
		}
		if err := aseAssignAddresses(&next, existing, updating, subnetPrefix); err != nil {
			sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "%v", err)
			return
		}
		webHostingEnvironments.Put(id, next)
		// Every App Service Environment has a front-end pool; the worker pools
		// are created by the client. A create that omits it still reads back
		// the pool the platform always has.
		aseEnsureMultiRolePool(id, next.Kind)
		sim.WriteJSON(w, http.StatusOK, aseProject(next))
	})

	ase("GET", "", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, aseProject(row))
	})

	// PATCH — AppServiceEnvironments_Update merges the members the envelope
	// carries onto the environment; everything it omits keeps its value.
	ase("PATCH", "", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		var patch AppServiceEnvironmentResource
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if patch.Kind != "" {
			row.Kind = patch.Kind
		}
		p := patch.Properties
		if p.VirtualNetwork != nil && p.VirtualNetwork.ID != "" {
			profile, prefix, err := aseResolveVirtualNetwork(*p.VirtualNetwork)
			if err != nil {
				sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
				return
			}
			row.Properties.VirtualNetwork = &profile
			if err := aseRederiveInternalAddress(&row, prefix); err != nil {
				sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "%v", err)
				return
			}
		}
		if p.InternalLoadBalancingMode != "" {
			row.Properties.InternalLoadBalancingMode = p.InternalLoadBalancingMode
		}
		if p.MultiSize != "" {
			row.Properties.MultiSize = p.MultiSize
		}
		if p.IpsslAddressCount != nil {
			row.Properties.IpsslAddressCount = p.IpsslAddressCount
		}
		if p.DNSSuffix != "" {
			row.Properties.DNSSuffix = p.DNSSuffix
		}
		if p.FrontEndScaleFactor != nil {
			row.Properties.FrontEndScaleFactor = p.FrontEndScaleFactor
		}
		if p.ClusterSettings != nil {
			row.Properties.ClusterSettings = p.ClusterSettings
		}
		if p.UserWhitelistedIPRanges != nil {
			row.Properties.UserWhitelistedIPRanges = p.UserWhitelistedIPRanges
		}
		if p.UpgradePreference != "" {
			row.Properties.UpgradePreference = p.UpgradePreference
		}
		if p.DedicatedHostCount != nil {
			row.Properties.DedicatedHostCount = p.DedicatedHostCount
		}
		if p.ZoneRedundant != nil {
			row.Properties.ZoneRedundant = p.ZoneRedundant
		}
		webHostingEnvironments.Put(row.ID, row)
		sim.WriteJSON(w, http.StatusOK, aseProject(row))
	})

	ase("DELETE", "", func(w http.ResponseWriter, r *http.Request) {
		row, ok := webHostingEnvironments.Get(aseResourceID(r))
		if !ok {
			// A delete of an environment that is not there is the operation's
			// documented no-content outcome, not an error.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// An environment still hosting App Service plans cannot be deleted;
		// the resource provider refuses rather than orphaning them.
		if plans := asePlans(row.ID); len(plans) > 0 && !strings.EqualFold(r.URL.Query().Get("forceDelete"), "true") {
			sim.AzureErrorf(w, "Conflict", http.StatusConflict,
				"App Service Environment '%s' cannot be deleted because %d App Service plan(s) are still deployed in it. Delete them first, or repeat the request with forceDelete=true.",
				row.Name, len(plans))
			return
		}
		aseReleaseAddresses(row)
		webHostingEnvironments.Delete(row.ID)
		prefix := row.ID + "/"
		for _, pool := range webEnvironmentPools.Filter(func(p WebWorkerPoolResource) bool {
			return strings.HasPrefix(p.ID, prefix)
		}) {
			webEnvironmentPools.Delete(pool.ID)
		}
		for _, pec := range webEnvironmentPECs.Filter(func(c WebSitePrivateEndpointConnection) bool {
			return strings.HasPrefix(c.ID, prefix)
		}) {
			webEnvironmentPECs.Delete(pec.ID)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv.HandleFunc("GET "+webProvider+"/hostingEnvironments", func(w http.ResponseWriter, r *http.Request) {
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/hostingEnvironments/",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"))
		writeASECollection(w, r, prefix)
	})
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/hostingEnvironments", func(w http.ResponseWriter, r *http.Request) {
		writeASECollection(w, r, "/subscriptions/"+sim.PathParam(r, "subscriptionId")+"/")
	})
}

func writeASECollection(w http.ResponseWriter, r *http.Request, prefix string) {
	rows := webHostingEnvironments.Filter(func(a AppServiceEnvironmentResource) bool {
		return strings.HasPrefix(strings.ToLower(a.ID), strings.ToLower(prefix))
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	out := make([]AppServiceEnvironmentResource, 0, len(rows))
	for _, row := range rows {
		out = append(out, aseProject(row))
	}
	writeARMCollection(w, r, out)
}

// writeARMCollection writes one page of an ARM collection with the nextLink
// the specification's paged collections carry.
func writeARMCollection[T any](w http.ResponseWriter, r *http.Request, items []T) {
	page, token := armPage(r, items)
	body := map[string]any{"value": page}
	if token != "" {
		body["nextLink"] = armNextLink(r, token)
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

// aseResolveVirtualNetwork resolves the virtual-network profile against the
// simulator's Microsoft.Network store and returns it with the read-only name
// and type the service fills in, plus the address prefix of the subnet the
// environment will occupy.
func aseResolveVirtualNetwork(req VirtualNetworkProfile) (VirtualNetworkProfile, string, error) {
	out := VirtualNetworkProfile{ID: req.ID, Subnet: req.Subnet}
	// The id names either the subnet itself (the modern spelling every
	// example uses) or the virtual network, with `subnet` naming the subnet
	// inside it.
	subnetID := req.ID
	if !strings.Contains(strings.ToLower(req.ID), "/subnets/") {
		if req.Subnet == "" {
			return out, "", fmt.Errorf("the App Service Environment's virtual network reference %q names a virtual network but no subnet", req.ID)
		}
		subnetID = strings.TrimSuffix(req.ID, "/") + "/subnets/" + req.Subnet
	}
	subnet, ok := azureSubnetByID(subnetID)
	if !ok {
		return out, "", fmt.Errorf("the subnet %q does not exist", subnetID)
	}
	prefix := aseSubnetPrefix(subnet)
	if prefix == "" {
		return out, "", fmt.Errorf("the subnet %q carries no address prefix", subnetID)
	}
	if strings.Contains(strings.ToLower(req.ID), "/subnets/") {
		out.Name = subnet.Name
		out.Type = "Microsoft.Network/virtualNetworks/subnets"
	} else {
		out.Name = path.Base(req.ID)
		out.Type = "Microsoft.Network/virtualNetworks"
	}
	return out, prefix, nil
}

// azureSubnetByID resolves a subnet by resource id, case-insensitively.
func azureSubnetByID(id string) (Subnet, bool) {
	if subnet, ok := azureSubnets.Get(id); ok {
		return subnet, true
	}
	for _, subnet := range azureSubnets.List() {
		if strings.EqualFold(subnet.ID, id) {
			return subnet, true
		}
	}
	return Subnet{}, false
}

// aseAssignAddresses gives the environment its addresses: a lease from the
// same public-IPv4 pool Microsoft.Network/publicIPAddresses reserves from for
// the address it is reached at from outside and the address it leaves from,
// and an address derived from its own subnet when it load-balances internally.
// An update keeps the addresses the environment already holds.
func aseAssignAddresses(next *AppServiceEnvironmentResource, existing AppServiceEnvironmentResource, updating bool, subnetPrefix string) error {
	current := ASENetworkingProperties{}
	if updating {
		current = aseNetworkingProperties(existing)
	}
	// The client-settable half of the networking configuration travels on the
	// create envelope too, so honor it there.
	requested := aseNetworkingProperties(*next)
	settings := ASENetworkingProperties{
		AllowNewPrivateEndpointConnections: current.AllowNewPrivateEndpointConnections,
		FtpEnabled:                         current.FtpEnabled,
		RemoteDebugEnabled:                 current.RemoteDebugEnabled,
		InboundIPAddressOverride:           current.InboundIPAddressOverride,
	}
	if requested.AllowNewPrivateEndpointConnections != nil {
		settings.AllowNewPrivateEndpointConnections = requested.AllowNewPrivateEndpointConnections
	}
	if requested.FtpEnabled != nil {
		settings.FtpEnabled = requested.FtpEnabled
	}
	if requested.RemoteDebugEnabled != nil {
		settings.RemoteDebugEnabled = requested.RemoteDebugEnabled
	}
	// inboundIpAddressOverride is settable on create only, exactly as the
	// specification's description says.
	if !updating && requested.InboundIPAddressOverride != "" {
		settings.InboundIPAddressOverride = requested.InboundIPAddressOverride
	}

	outbound := current.WindowsOutboundIPAddresses
	if len(outbound) == 0 {
		lease, err := realexec.ReserveAzurePublicIPv4(next.ID, nil)
		if err != nil {
			return fmt.Errorf("failed to reserve a public IPv4 lease for the App Service Environment: %w", err)
		}
		outbound = []string{lease.String()}
	}
	settings.WindowsOutboundIPAddresses = outbound
	settings.LinuxOutboundIPAddresses = outbound

	settings.ExternalInboundIPAddresses = []string{}
	settings.InternalInboundIPAddresses = []string{}
	if aseIsInternal(*next) {
		internal := settings.InboundIPAddressOverride
		if internal == "" {
			derived, err := aseInternalInboundAddress(subnetPrefix)
			if err != nil {
				return err
			}
			internal = derived
		}
		settings.InternalInboundIPAddresses = []string{internal}
	} else {
		// An externally load-balanced environment answers on the same address
		// it leaves from, which is what the specification's own
		// AppServiceEnvironments_GetVipInfo example reports.
		settings.ExternalInboundIPAddresses = outbound
	}
	next.Properties.NetworkingConfiguration = aseConfigurationChild(next.ID, "networking", "networking", settings)
	return nil
}

// aseRederiveInternalAddress recomputes the internal inbound address after the
// environment moved to another subnet.
func aseRederiveInternalAddress(row *AppServiceEnvironmentResource, subnetPrefix string) error {
	settings := aseNetworkingProperties(*row)
	if !aseIsInternal(*row) {
		settings.InternalInboundIPAddresses = []string{}
		row.Properties.NetworkingConfiguration = aseConfigurationChild(row.ID, "networking", "networking", settings)
		return nil
	}
	internal := settings.InboundIPAddressOverride
	if internal == "" {
		derived, err := aseInternalInboundAddress(subnetPrefix)
		if err != nil {
			return err
		}
		internal = derived
	}
	settings.InternalInboundIPAddresses = []string{internal}
	settings.ExternalInboundIPAddresses = []string{}
	row.Properties.NetworkingConfiguration = aseConfigurationChild(row.ID, "networking", "networking", settings)
	return nil
}

// aseReleaseAddresses returns the environment's public lease to the pool a
// delete frees it from.
func aseReleaseAddresses(row AppServiceEnvironmentResource) {
	for _, addr := range aseNetworkingProperties(row).WindowsOutboundIPAddresses {
		if ip := net.ParseIP(addr); ip != nil {
			realexec.ReleasePublicIPv4(ip)
		}
	}
}

// aseEnsureMultiRolePool materializes the front-end pool every environment
// has. Its worker count is the environment's front-end instance count, which
// is what AppServiceEnvironment.multiRoleCount reports.
func aseEnsureMultiRolePool(aseID, kind string) {
	id := aseID + "/multiRolePools/" + multiRolePoolName
	if _, ok := webEnvironmentPools.Get(id); ok {
		return
	}
	webEnvironmentPools.Put(id, WebWorkerPoolResource{
		ID:   id,
		Name: multiRolePoolName,
		Type: aseResourceType + "/multiRolePools",
		Kind: kind,
		Properties: WebWorkerPoolProperties{
			ComputeMode: "Dedicated",
			WorkerCount: int32Ptr(0),
		},
	})
}

// networking configuration, custom domain suffix, addresses and dependencies

func registerWebEnvironmentNetworking(ase func(string, string, http.HandlerFunc)) {
	// AppServiceEnvironments_GetAseV3NetworkingConfiguration
	ase("GET", "/configurations/networking", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, aseConfigurationChild(row.ID, "networking", "networking", aseNetworkingProperties(row)))
	})

	// AppServiceEnvironments_UpdateAseNetworkingConfiguration — the three
	// settable switches; the address lists are the platform's and are not
	// taken from the request.
	ase("PUT", "/configurations/networking", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		var req ASEConfigurationResource
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var patch ASENetworkingProperties
		if raw, err := json.Marshal(req.Properties); err == nil {
			_ = json.Unmarshal(raw, &patch)
		}
		settings := aseNetworkingProperties(row)
		if patch.AllowNewPrivateEndpointConnections != nil {
			settings.AllowNewPrivateEndpointConnections = patch.AllowNewPrivateEndpointConnections
		}
		if patch.FtpEnabled != nil {
			settings.FtpEnabled = patch.FtpEnabled
		}
		if patch.RemoteDebugEnabled != nil {
			settings.RemoteDebugEnabled = patch.RemoteDebugEnabled
		}
		child := aseConfigurationChild(row.ID, "networking", "networking", settings)
		row.Properties.NetworkingConfiguration = child
		webHostingEnvironments.Put(row.ID, row)
		sim.WriteJSON(w, http.StatusOK, child)
	})

	// AppServiceEnvironments_GetAseCustomDnsSuffixConfiguration
	ase("GET", "/configurations/customdnssuffix", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		child := row.Properties.CustomDNSSuffixConfiguration
		if child == nil {
			// No custom domain suffix has been configured; the resource
			// exists with an empty configuration, which is what the service
			// answers before one is set.
			child = aseCustomDNSSuffixChild(row.ID, ASECustomDNSSuffixProperties{})
		}
		sim.WriteJSON(w, http.StatusOK, child)
	})

	// AppServiceEnvironments_UpdateAseCustomDnsSuffixConfiguration
	ase("PUT", "/configurations/customdnssuffix", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		var req ASEConfigurationResource
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var props ASECustomDNSSuffixProperties
		if raw, err := json.Marshal(req.Properties); err == nil {
			_ = json.Unmarshal(raw, &props)
		}
		if props.DNSSuffix == "" {
			sim.AzureError(w, "InvalidRequestContent",
				"The 'properties.dnsSuffix' property is required to configure a custom domain suffix.",
				http.StatusBadRequest)
			return
		}
		props.ProvisioningState = "Succeeded"
		props.ProvisioningDetails = ""
		child := aseCustomDNSSuffixChild(row.ID, props)
		row.Properties.CustomDNSSuffixConfiguration = child
		webHostingEnvironments.Put(row.ID, row)
		sim.WriteJSON(w, http.StatusOK, child)
	})

	// AppServiceEnvironments_DeleteAseCustomDnsSuffixConfiguration
	ase("DELETE", "/configurations/customdnssuffix", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		row.Properties.CustomDNSSuffixConfiguration = nil
		webHostingEnvironments.Put(row.ID, row)
		w.WriteHeader(http.StatusNoContent)
	})

	// AppServiceEnvironments_GetVipInfo — the addresses the environment is
	// reached at and leaves from.
	ase("GET", "/capacities/virtualip", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		settings := aseNetworkingProperties(row)
		props := map[string]any{
			"outboundIpAddresses": settings.WindowsOutboundIPAddresses,
		}
		if len(settings.InternalInboundIPAddresses) > 0 {
			props["internalIpAddress"] = settings.InternalInboundIPAddresses[0]
			props["serviceIpAddress"] = settings.InternalInboundIPAddresses[0]
		} else if len(settings.ExternalInboundIPAddresses) > 0 {
			props["serviceIpAddress"] = settings.ExternalInboundIPAddresses[0]
		}
		// vipMappings is absent: the simulator reserves no additional virtual
		// IPs for IP-based SSL bindings, so there are none to map.
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":         row.ID + "/capacities/virtualip",
			"name":       row.Name,
			"type":       aseResourceType + "/capacities",
			"properties": props,
		})
	})

	// AppServiceEnvironments_GetInboundNetworkDependenciesEndpoints — computed
	// from the environment's own networking: the addresses it answers on, the
	// subnet it occupies, and the inbound features it has enabled. An endpoint
	// whose feature is off is absent rather than listed.
	ase("GET", "/inboundNetworkDependenciesEndpoints", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		settings := aseNetworkingProperties(row)
		inbound := aseInboundAddresses(row)
		var endpoints []map[string]any
		if len(inbound) > 0 {
			cidrs := make([]string, 0, len(inbound))
			for _, addr := range inbound {
				cidrs = append(cidrs, addr+"/32")
			}
			ports := []string{"80", "443"}
			if settings.FtpEnabled != nil && *settings.FtpEnabled {
				// The App Service Environment FTP endpoint: the control
				// connection and its passive data-channel range.
				ports = append(ports, "21", "990", "10001-10020")
			}
			if settings.RemoteDebugEnabled != nil && *settings.RemoteDebugEnabled {
				ports = append(ports, "4024")
			}
			endpoints = append(endpoints, map[string]any{
				"description": "App Service Environment VIP",
				"endpoints":   cidrs,
				"ports":       ports,
			})
		}
		if row.Properties.VirtualNetwork != nil {
			if subnet, found := azureSubnetByID(aseSubnetIDOf(row)); found {
				if prefix := aseSubnetPrefix(subnet); prefix != "" {
					endpoints = append(endpoints, map[string]any{
						"description": "App Service Environment subnet",
						"endpoints":   []string{prefix},
						"ports":       []string{"All"},
					})
				}
			}
		}
		if endpoints == nil {
			endpoints = []map[string]any{}
		}
		writeARMCollection(w, r, endpoints)
	})
}

// aseSubnetIDOf is the subnet resource id the environment occupies, in the
// spelling the client used.
func aseSubnetIDOf(row AppServiceEnvironmentResource) string {
	profile := row.Properties.VirtualNetwork
	if profile == nil {
		return ""
	}
	if strings.Contains(strings.ToLower(profile.ID), "/subnets/") {
		return profile.ID
	}
	if profile.Subnet == "" {
		return ""
	}
	return strings.TrimSuffix(profile.ID, "/") + "/subnets/" + profile.Subnet
}

func aseCustomDNSSuffixChild(aseID string, props ASECustomDNSSuffixProperties) *ASEConfigurationResource {
	child := aseConfigurationChild(aseID, "customdnssuffix", "customDnsSuffix", props)
	return child
}

// front-end and worker pools

func registerWebEnvironmentPools(ase func(string, string, http.HandlerFunc)) {
	// multiRolePools — the front-end pool. Its path segment is the literal
	// "default"; the worker pools are named by the client.
	ase("GET", "/multiRolePools", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		writeARMCollection(w, r, asePoolsUnder(row.ID, "multiRolePools"))
	})
	ase("GET", "/multiRolePools/"+multiRolePoolName, asePoolRead("multiRolePools"))
	ase("PUT", "/multiRolePools/"+multiRolePoolName, asePoolWrite("multiRolePools", false))
	ase("PATCH", "/multiRolePools/"+multiRolePoolName, asePoolWrite("multiRolePools", true))
	ase("GET", "/multiRolePools/"+multiRolePoolName+"/skus", asePoolSKUs("multiRolePools"))
	ase("GET", "/multiRolePools/"+multiRolePoolName+"/usages", asePoolUsages())

	ase("GET", "/workerPools", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		writeARMCollection(w, r, asePoolsUnder(row.ID, "workerPools"))
	})
	ase("GET", "/workerPools/{workerPoolName}", asePoolRead("workerPools"))
	ase("PUT", "/workerPools/{workerPoolName}", asePoolWrite("workerPools", false))
	ase("PATCH", "/workerPools/{workerPoolName}", asePoolWrite("workerPools", true))
	ase("GET", "/workerPools/{workerPoolName}/skus", asePoolSKUs("workerPools"))
	ase("GET", "/workerPools/{workerPoolName}/usages", asePoolUsages())

	// AppServiceEnvironments_ListCapacities — the environment's stamp
	// capacity, computed from its pools and the plans placed in it.
	ase("GET", "/capacities/compute", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		writeARMCollection(w, r, aseStampCapacities(row))
	})
}

// asePoolName is the pool the request addresses: the literal front-end pool
// or the named worker pool.
func asePoolName(r *http.Request, collection string) string {
	if collection == "multiRolePools" {
		return multiRolePoolName
	}
	return sim.PathParam(r, "workerPoolName")
}

func asePoolID(aseID, collection, name string) string {
	return aseID + "/" + collection + "/" + name
}

func asePoolsUnder(aseID, collection string) []WebWorkerPoolResource {
	prefix := aseID + "/" + collection + "/"
	pools := webEnvironmentPools.Filter(func(p WebWorkerPoolResource) bool {
		return strings.HasPrefix(p.ID, prefix)
	})
	sort.Slice(pools, func(i, j int) bool { return pools[i].ID < pools[j].ID })
	for i := range pools {
		pools[i] = asePoolProject(pools[i])
	}
	return pools
}

// asePoolProject fills the pool's read-only instanceNames — one name per
// worker the pool runs, in the platform's own numbering.
func asePoolProject(pool WebWorkerPoolResource) WebWorkerPoolResource {
	count := int32Value(pool.Properties.WorkerCount)
	names := make([]string, 0, count)
	for i := int32(0); i < count; i++ {
		names = append(names, fmt.Sprintf("%s_%d", pool.Name, i))
	}
	pool.Properties.InstanceNames = names
	return pool
}

func asePoolRead(collection string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		name := asePoolName(r, collection)
		pool, found := webEnvironmentPools.Get(asePoolID(row.ID, collection, name))
		if !found {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/hostingEnvironments/%s/%s/%s' was not found.", row.Name, collection, name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, asePoolProject(pool))
	}
}

// asePoolWrite serves both the create-or-update (PUT) and the merge (PATCH)
// spelling of a pool write.
func asePoolWrite(collection string, merge bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		name := asePoolName(r, collection)
		id := asePoolID(row.ID, collection, name)
		var req WebWorkerPoolResource
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		existing, found := webEnvironmentPools.Get(id)
		if merge && !found {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/hostingEnvironments/%s/%s/%s' was not found.", row.Name, collection, name)
			return
		}
		pool := WebWorkerPoolResource{
			ID:   id,
			Name: name,
			Type: aseResourceType + "/" + collection,
			Kind: req.Kind,
			Sku:  req.Sku,
			Properties: WebWorkerPoolProperties{
				WorkerSizeID: req.Properties.WorkerSizeID,
				ComputeMode:  req.Properties.ComputeMode,
				WorkerSize:   req.Properties.WorkerSize,
				WorkerCount:  req.Properties.WorkerCount,
			},
		}
		if found {
			if pool.Kind == "" {
				pool.Kind = existing.Kind
			}
			if pool.Sku == nil {
				pool.Sku = existing.Sku
			}
			if merge {
				if pool.Properties.WorkerSizeID == nil {
					pool.Properties.WorkerSizeID = existing.Properties.WorkerSizeID
				}
				if pool.Properties.ComputeMode == "" {
					pool.Properties.ComputeMode = existing.Properties.ComputeMode
				}
				if pool.Properties.WorkerSize == "" {
					pool.Properties.WorkerSize = existing.Properties.WorkerSize
				}
				if pool.Properties.WorkerCount == nil {
					pool.Properties.WorkerCount = existing.Properties.WorkerCount
				}
			}
		}
		if pool.Properties.ComputeMode == "" {
			// Every machine in an App Service Environment is dedicated to it.
			pool.Properties.ComputeMode = "Dedicated"
		}
		if pool.Properties.WorkerCount == nil {
			pool.Properties.WorkerCount = int32Ptr(0)
		}
		if pool.Sku != nil {
			tier, family := appServicePlanSkuTier(pool.Sku.Name)
			if pool.Sku.Tier == "" {
				pool.Sku.Tier = tier
			}
			if pool.Sku.Family == "" {
				pool.Sku.Family = family
			}
			if pool.Sku.Size == "" {
				pool.Sku.Size = pool.Sku.Name
			}
		}
		webEnvironmentPools.Put(id, pool)
		sim.WriteJSON(w, http.StatusOK, asePoolProject(pool))
	}
}

// asePoolSKUs serves AppServiceEnvironments_ListMultiRolePoolSkus and
// _ListWorkerPoolSkus. It reports the SKU the pool actually runs — the one
// discovery answer the simulator can give from real state. The SkuInfo carries
// no capacity envelope: the simulator enforces no minimum, maximum or default
// worker count for a pool, so declaring one would invent a scale limit that
// nothing here applies.
func asePoolSKUs(collection string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		name := asePoolName(r, collection)
		pool, found := webEnvironmentPools.Get(asePoolID(row.ID, collection, name))
		if !found {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/hostingEnvironments/%s/%s/%s' was not found.", row.Name, collection, name)
			return
		}
		skus := []map[string]any{}
		if pool.Sku != nil && pool.Sku.Name != "" {
			skus = append(skus, map[string]any{
				"resourceType": aseResourceType + "/" + collection,
				"sku": map[string]any{
					"name":   pool.Sku.Name,
					"tier":   pool.Sku.Tier,
					"size":   pool.Sku.Size,
					"family": pool.Sku.Family,
				},
			})
		}
		writeARMCollection(w, r, skus)
	}
}

// asePoolUsages serves AppServiceEnvironments_ListMultiRoleUsages and
// _ListWebWorkerUsages. The simulator meters no pool quota — it applies no
// limit a usage could be reported against — so the collection is empty, the
// same truthful answer the per-site and per-location usage lists give.
func asePoolUsages() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := aseLookup(w, r); !ok {
			return
		}
		writeARMCollection(w, r, []map[string]any{})
	}
}

// aseStampWorkerSizes are the machine sizes the StampCapacity `workerSize`
// member can name — the swagger's WorkerSizeOptions enumeration. A pool
// configured with a size outside it reports its capacity without the member
// rather than with a value the enumeration does not allow.
var aseStampWorkerSizes = map[string]string{
	"small": "Small", "medium": "Medium", "large": "Large",
	"d1": "D1", "d2": "D2", "d3": "D3",
	"smallv3": "SmallV3", "mediumv3": "MediumV3", "largev3": "LargeV3",
	"nestedsmall": "NestedSmall", "nestedsmalllinux": "NestedSmallLinux",
	"default": "Default",
}

// aseStampCapacities computes the environment's capacity: for every pool, the
// workers it runs, less the capacity the App Service plans placed in the
// environment on that pool have already taken.
func aseStampCapacities(row AppServiceEnvironmentResource) []map[string]any {
	plans := asePlans(row.ID)
	out := []map[string]any{}
	pools := append(asePoolsUnder(row.ID, "multiRolePools"), asePoolsUnder(row.ID, "workerPools")...)
	for _, pool := range pools {
		total := int64(int32Value(pool.Properties.WorkerCount))
		var taken int64
		linux := false
		for _, plan := range plans {
			if plan.Properties.TargetWorkerSizeID != nil &&
				int32Value(plan.Properties.TargetWorkerSizeID) != int32Value(pool.Properties.WorkerSizeID) {
				continue
			}
			taken += int64(plan.Sku.Capacity)
			if plan.Properties.Reserved {
				linux = true
			}
		}
		available := total - taken
		if available < 0 {
			available = 0
		}
		capacity := map[string]any{
			"name":              pool.Name,
			"totalCapacity":     total,
			"availableCapacity": available,
			"unit":              "Workers",
			"computeMode":       pool.Properties.ComputeMode,
			"isLinux":           linux,
		}
		if pool.Properties.WorkerSizeID != nil {
			capacity["workerSizeId"] = int32Value(pool.Properties.WorkerSizeID)
		}
		if size, ok := aseStampWorkerSizes[strings.ToLower(pool.Properties.WorkerSize)]; ok {
			capacity["workerSize"] = size
		}
		out = append(out, capacity)
	}
	return out
}

// inventory: the plans and apps placed in the environment

func registerWebEnvironmentInventory(ase func(string, string, http.HandlerFunc)) {
	// AppServiceEnvironments_ListAppServicePlans
	ase("GET", "/serverfarms", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		plans := asePlans(row.ID)
		sort.Slice(plans, func(i, j int) bool { return plans[i].ID < plans[j].ID })
		writeARMCollection(w, r, plans)
	})

	// AppServiceEnvironments_ListWebApps
	ase("GET", "/sites", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		writeARMCollection(w, r, aseSortedSites(row.ID))
	})

	// AppServiceEnvironments_ListUsages — the environment enforces no quota
	// in the simulator, so it has none to report.
	ase("GET", "/usages", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := aseLookup(w, r); !ok {
			return
		}
		writeARMCollection(w, r, []map[string]any{})
	})

	// AppServiceEnvironments_ListOperations — the operations currently
	// running on the environment. Every operation this simulator serves on an
	// environment completes before its response is written, so none is ever
	// in flight; the specification's own example reports the same empty list.
	ase("GET", "/operations", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := aseLookup(w, r); !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, []any{})
	})
}

func aseSortedSites(aseID string) []Site {
	sites := aseSites(aseID)
	sort.Slice(sites, func(i, j int) bool { return sites[i].ID < sites[j].ID })
	return sites
}

// lifecycle

func registerWebEnvironmentLifecycle(ase func(string, string, http.HandlerFunc)) {
	// AppServiceEnvironments_Suspend / _Resume — suspending an environment
	// stops the apps running in it and resuming starts them again, which is
	// why both answer with the apps they moved.
	ase("POST", "/suspend", aseSetSuspended(true))
	ase("POST", "/resume", aseSetSuspended(false))

	// AppServiceEnvironments_ChangeVnet — move the environment into another
	// subnet. The apps it hosts come with it, which is the collection the
	// operation answers with.
	ase("POST", "/changeVirtualNetwork", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		var req VirtualNetworkProfile
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.ID == "" {
			sim.AzureError(w, "InvalidRequestContent", "The 'id' property of the virtual network profile is required.", http.StatusBadRequest)
			return
		}
		profile, prefix, err := aseResolveVirtualNetwork(req)
		if err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		row.Properties.VirtualNetwork = &profile
		if err := aseRederiveInternalAddress(&row, prefix); err != nil {
			sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "%v", err)
			return
		}
		webHostingEnvironments.Put(row.ID, row)
		aseAcceptedCollection(w, r, row, aseSortedSites(row.ID))
	})

	// AppServiceEnvironments_Reboot — reboot the machines of the environment.
	// The apps running on them restart, so their site containers are torn
	// down; the next request to an app starts it again.
	ase("POST", "/reboot", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		sites := aseSortedSites(row.ID)
		opID := issueAzureAsyncOperation(func() {
			for _, site := range sites {
				stopAzureFunctionInstance(site.Name)
				recordWebSiteEvent(site.ID, "Restart", webEventCausePlatform)
			}
		})
		aseAccepted(w, r, row, opID)
	})

	// AppServiceEnvironments_TestUpgradeAvailableNotification — announce that
	// an upgrade is available for the environment. The simulator runs no
	// platform-upgrade schedule of its own, so this notification is the only
	// thing that can make one available: it moves upgradeAvailability from
	// None to Ready, which is the state the documented Manual upgrade
	// preference requires before an upgrade can be started by hand.
	ase("POST", "/testUpgradeAvailableNotification", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		row.Properties.UpgradeAvailability = "Ready"
		webHostingEnvironments.Put(row.ID, row)
		w.WriteHeader(http.StatusOK)
	})

	// AppServiceEnvironments_Upgrade — initiate the available upgrade. An
	// environment with no upgrade available is refused, exactly as an upgrade
	// can only be initiated once one is ready.
	ase("POST", "/upgrade", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		if !strings.EqualFold(row.Properties.UpgradeAvailability, "Ready") {
			sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest,
				"No upgrade is available for App Service Environment '%s'.", row.Name)
			return
		}
		id := row.ID
		opID := issueAzureAsyncOperation(func() {
			webHostingEnvironments.Update(id, func(a *AppServiceEnvironmentResource) {
				a.Properties.UpgradeAvailability = "None"
				a.Properties.Status = "Ready"
			})
		})
		aseAccepted(w, r, row, opID)
	})
}

// aseAccepted answers the two operations the specification declares with a
// 202-only contract, with the poll coordinates ARM puts on an accepted
// long-running operation.

// aseAcceptedCollection answers an App Service Environment operation that is
// long-running with its final state via Location and whose result is a
// collection of the apps it moved. The collection is recorded as the
// operation's result, so the Location poll serves it once the operation
// completes.
//
// Answering the collection synchronously instead is a shape the generated
// client cannot read: for an operation declared both long-running and
// pageable, the SDK builds a pager, hands it to the poller as the response to
// fill, and — on the synchronous branch — its no-op poller unmarshals the
// terminal body into a fresh zero-valued pager and assigns that over the one
// it built, so every read calls through a nil handler and panics. The service
// answers 202 here, and the specification documents it; matching that is what
// makes the operation readable.
func aseAcceptedCollection(w http.ResponseWriter, r *http.Request, row AppServiceEnvironmentResource, sites []Site) {
	page, token := armPage(r, sites)
	body := map[string]any{"value": page}
	if token != "" {
		body["nextLink"] = armNextLink(r, token)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		sim.AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
			"The operation result could not be rendered: %v", err)
		return
	}
	opID := issueAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
		return payload, nil
	})
	aseAccepted(w, r, row, opID)
}

func aseAccepted(w http.ResponseWriter, r *http.Request, row AppServiceEnvironmentResource, opID string) {
	sub := sim.PathParam(r, "subscriptionId")
	apiVersion := r.URL.Query().Get("api-version")
	writeAzureAsyncCreateHeaders(w,
		azureAsyncOperationHeader(r, sub, "Microsoft.Web", row.Location, "operationStatuses", opID, apiVersion),
		azureAsyncOperationHeader(r, sub, "Microsoft.Web", row.Location, "operationResults", opID, apiVersion))
	w.WriteHeader(http.StatusAccepted)
}

// aseSetSuspended stops or starts every app in the environment and records the
// environment's own suspended state.
func aseSetSuspended(suspended bool) http.HandlerFunc {
	state, operation := "Running", "Start"
	if suspended {
		state, operation = "Stopped", "Stop"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		for _, site := range aseSites(row.ID) {
			azfSites.Update(site.ID, func(s *Site) { s.Properties.State = state })
			recordWebSiteEvent(site.ID, operation, webEventCausePlatform)
		}
		row.Properties.Suspended = boolPtr(suspended)
		webHostingEnvironments.Put(row.ID, row)
		aseAcceptedCollection(w, r, row, aseSortedSites(row.ID))
	}
}

// private endpoint connections

func registerWebEnvironmentPrivateEndpoints(ase func(string, string, http.HandlerFunc)) {
	// AppServiceEnvironments_GetPrivateEndpointConnectionList
	ase("GET", "/privateEndpointConnections", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		prefix := row.ID + "/privateEndpointConnections/"
		conns := webEnvironmentPECs.Filter(func(c WebSitePrivateEndpointConnection) bool {
			return strings.HasPrefix(c.ID, prefix)
		})
		sort.Slice(conns, func(i, j int) bool { return conns[i].ID < conns[j].ID })
		out := make([]map[string]any, 0, len(conns))
		for _, conn := range conns {
			out = append(out, renderWebSitePEC(conn))
		}
		writeARMCollection(w, r, out)
	})

	// AppServiceEnvironments_GetPrivateEndpointConnection
	ase("GET", "/privateEndpointConnections/{privateEndpointConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		conn, ok := aseLookupPEC(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, renderWebSitePEC(conn))
	})

	// AppServiceEnvironments_ApproveOrRejectPrivateEndpointConnection — the
	// environment's owner moves the state of a connection a private endpoint
	// opened; the connection itself is created by that endpoint.
	ase("PUT", "/privateEndpointConnections/{privateEndpointConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		conn, ok := aseLookupPEC(w, r)
		if !ok {
			return
		}
		var req struct {
			Properties struct {
				PrivateLinkServiceConnectionState *PrivateLinkServiceConnectionState `json:"privateLinkServiceConnectionState"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if conn.Properties == nil {
			conn.Properties = map[string]any{}
		}
		if state := req.Properties.PrivateLinkServiceConnectionState; state != nil {
			conn.Properties["privateLinkServiceConnectionState"] = map[string]any{
				"status":          state.Status,
				"description":     state.Description,
				"actionsRequired": state.ActionsRequired,
			}
		}
		conn.Properties["provisioningState"] = "Succeeded"
		webEnvironmentPECs.Put(conn.ID, conn)
		sim.WriteJSON(w, http.StatusOK, renderWebSitePEC(conn))
	})

	// AppServiceEnvironments_DeletePrivateEndpointConnection
	ase("DELETE", "/privateEndpointConnections/{privateEndpointConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		conn, ok := aseLookupPEC(w, r)
		if !ok {
			return
		}
		webEnvironmentPECs.Delete(conn.ID)
		sim.WriteJSON(w, http.StatusOK, map[string]any{"id": conn.ID})
	})

	// AppServiceEnvironments_GetPrivateLinkResources — an App Service
	// Environment publishes no named private-link group: a private endpoint
	// reaches the environment itself rather than one of several sub-resources,
	// which is why Microsoft's private-link resource table names no group-id
	// token for Microsoft.Web/hostingEnvironments and the specification's own
	// example answers with an empty wrapper.
	ase("GET", "/privateLinkResources", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := aseLookup(w, r); !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
	})
}

func aseLookupPEC(w http.ResponseWriter, r *http.Request) (WebSitePrivateEndpointConnection, bool) {
	row, ok := aseLookup(w, r)
	if !ok {
		return WebSitePrivateEndpointConnection{}, false
	}
	name := sim.PathParam(r, "privateEndpointConnectionName")
	conn, found := webEnvironmentPECs.Get(row.ID + "/privateEndpointConnections/" + name)
	if !found {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Private endpoint connection '%s' not found.", name)
		return WebSitePrivateEndpointConnection{}, false
	}
	return conn, true
}

// declared gaps

// registerWebEnvironmentDeclaredGaps mounts the five documented operations the
// simulator does not answer, so a client is told what is missing and why
// instead of receiving a bare routing 404 from inside a resource whose other
// operations all work. The reasons are the ones recorded at the top of this
// file and beside the coverage floor.
func registerWebEnvironmentDeclaredGaps(ase func(string, string, http.HandlerFunc)) {
	const metricReason = "the simulator publishes no Microsoft.Insights metric series for an App Service Environment pool, so it has no metric definitions to declare"
	const outboundReason = "the outbound network dependencies of an App Service Environment are Microsoft's published catalog of platform endpoints and address ranges, which this simulator does not vendor"

	// The gap does not depend on the environment existing: the operation is
	// unimplemented whatever it is asked about, and answering a 404 for an
	// absent environment first would report the operation as one the
	// simulator serves.
	gap := func(operation, reason string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			sim.AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
				"%s is not implemented by the simulator: %s.", operation, reason)
		}
	}
	ase("GET", "/multiRolePools/"+multiRolePoolName+"/metricdefinitions",
		gap("AppServiceEnvironments_ListMultiRoleMetricDefinitions", metricReason))
	ase("GET", "/multiRolePools/"+multiRolePoolName+"/instances/{instance}/metricdefinitions",
		gap("AppServiceEnvironments_ListMultiRolePoolInstanceMetricDefinitions", metricReason))
	ase("GET", "/workerPools/{workerPoolName}/metricdefinitions",
		gap("AppServiceEnvironments_ListWebWorkerMetricDefinitions", metricReason))
	ase("GET", "/workerPools/{workerPoolName}/instances/{instance}/metricdefinitions",
		gap("AppServiceEnvironments_ListWorkerPoolInstanceMetricDefinitions", metricReason))
	ase("GET", "/outboundNetworkDependenciesEndpoints",
		gap("AppServiceEnvironments_GetOutboundNetworkDependenciesEndpoints", outboundReason))
}

// Kubernetes environments

func registerWebKubeEnvironments(srv *sim.Server) {
	kube := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/kubeEnvironments/{name}"+suffix, h)
	}

	kube("PUT", "", func(w http.ResponseWriter, r *http.Request) {
		var req KubeEnvironmentResource
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := kubeResourceID(r)
		name := sim.PathParam(r, "name")
		existing, updating := webKubeEnvironments.Get(id)
		if req.Location == "" && !updating {
			sim.AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}
		next := KubeEnvironmentResource{
			ID:               id,
			Name:             name,
			Type:             kubeResourceType,
			Kind:             req.Kind,
			Location:         req.Location,
			Tags:             req.Tags,
			ExtendedLocation: req.ExtendedLocation,
			Properties:       req.Properties,
		}
		if updating && next.Location == "" {
			next.Location = existing.Location
		}
		if next.ExtendedLocation != nil && next.ExtendedLocation.Name != "" && next.ExtendedLocation.Type == "" {
			// The extended location's type is read-only; an Arc-connected
			// cluster is a custom location.
			next.ExtendedLocation.Type = "CustomLocation"
		}
		next.Properties.ProvisioningState = "Succeeded"
		next.Properties.DeploymentErrors = ""
		if next.Properties.StaticIP == "" {
			if updating {
				next.Properties.StaticIP = existing.Properties.StaticIP
			} else {
				next.Properties.StaticIP = envStaticIP(id)
			}
		}
		if next.Properties.DefaultDomain == "" {
			if updating && existing.Properties.DefaultDomain != "" {
				next.Properties.DefaultDomain = existing.Properties.DefaultDomain
			} else {
				next.Properties.DefaultDomain = kubeDefaultDomain(name, next.Properties.StaticIP)
			}
		}
		webKubeEnvironments.Put(id, next)
		sim.WriteJSON(w, http.StatusOK, kubeProject(next))
	})

	kube("GET", "", func(w http.ResponseWriter, r *http.Request) {
		row, ok := kubeLookup(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, kubeProject(row))
	})

	kube("PATCH", "", func(w http.ResponseWriter, r *http.Request) {
		row, ok := kubeLookup(w, r)
		if !ok {
			return
		}
		var patch KubeEnvironmentResource
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if patch.Kind != "" {
			row.Kind = patch.Kind
		}
		p := patch.Properties
		if p.InternalLoadBalancerEnabled != nil {
			row.Properties.InternalLoadBalancerEnabled = p.InternalLoadBalancerEnabled
		}
		if p.StaticIP != "" {
			row.Properties.StaticIP = p.StaticIP
		}
		if p.AksResourceID != "" {
			row.Properties.AksResourceID = p.AksResourceID
		}
		if p.ArcConfiguration != nil {
			row.Properties.ArcConfiguration = p.ArcConfiguration
		}
		if p.AppLogsConfiguration != nil {
			row.Properties.AppLogsConfiguration = p.AppLogsConfiguration
		}
		if p.ContainerAppsConfiguration != nil {
			row.Properties.ContainerAppsConfiguration = p.ContainerAppsConfiguration
		}
		webKubeEnvironments.Put(row.ID, row)
		sim.WriteJSON(w, http.StatusOK, kubeProject(row))
	})

	kube("DELETE", "", func(w http.ResponseWriter, r *http.Request) {
		if !webKubeEnvironments.Delete(kubeResourceID(r)) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("GET "+webProvider+"/kubeEnvironments", func(w http.ResponseWriter, r *http.Request) {
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/kubeEnvironments/",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"))
		writeKubeCollection(w, r, prefix)
	})
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/kubeEnvironments", func(w http.ResponseWriter, r *http.Request) {
		writeKubeCollection(w, r, "/subscriptions/"+sim.PathParam(r, "subscriptionId")+"/")
	})
}

func kubeLookup(w http.ResponseWriter, r *http.Request) (KubeEnvironmentResource, bool) {
	row, ok := webKubeEnvironments.Get(kubeResourceID(r))
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Web/kubeEnvironments/%s' under resource group '%s' was not found.",
			sim.PathParam(r, "name"), sim.PathParam(r, "resourceGroupName"))
		return KubeEnvironmentResource{}, false
	}
	return row, true
}

func writeKubeCollection(w http.ResponseWriter, r *http.Request, prefix string) {
	rows := webKubeEnvironments.Filter(func(k KubeEnvironmentResource) bool {
		return strings.HasPrefix(strings.ToLower(k.ID), strings.ToLower(prefix))
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	out := make([]KubeEnvironmentResource, 0, len(rows))
	for _, row := range rows {
		out = append(out, kubeProject(row))
	}
	writeARMCollection(w, r, out)
}

// kubeDefaultDomain is the domain the apps in a Kubernetes environment are
// published under: the cluster's own name under the address it answers on,
// the shape App Service uses for an Arc-connected cluster.
func kubeDefaultDomain(name, staticIP string) string {
	if staticIP == "" {
		return ""
	}
	return fmt.Sprintf("%s.%s.k4apps.io", name, strings.ReplaceAll(staticIP, ".", "-"))
}

// kubeProject drops the members a read never carries: the Arc kubeConfig is
// declared a secret, so the service does not hand it back.
func kubeProject(row KubeEnvironmentResource) KubeEnvironmentResource {
	if row.Properties.ArcConfiguration == nil {
		return row
	}
	arc := make(map[string]any, len(row.Properties.ArcConfiguration))
	for k, v := range row.Properties.ArcConfiguration {
		if strings.EqualFold(k, "kubeConfig") {
			continue
		}
		arc[k] = v
	}
	row.Properties.ArcConfiguration = arc
	return row
}
