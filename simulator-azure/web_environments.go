package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/ipam"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// App Service Environments (Microsoft.Web/hostingEnvironments) and Kubernetes
// Environments (Microsoft.Web/kubeEnvironments).
//
// An App Service Environment is a real placement scope: it occupies a stored
// Microsoft.Network subnet, leases its addresses from the public-IPv4 pool
// publicIPAddresses uses, derives its internal address from the subnet's
// prefix, and reads back the plans and sites naming it. What it reports is
// configured or computed:
//
//   - multiRoleCount is the front-end pool's worker count;
//   - hasLinuxWorkers is true when a Linux (reserved) plan is placed in it;
//   - stamp capacities are the pools' worker counts less what placed plans
//     take;
//   - inbound dependencies are the addresses and ports its networking
//     configuration exposes; outbound ones are measured
//     (web_environment_outbound.go).

// A settable id plus the read-only name and type the service resolves.
type HostingEnvironmentProfile struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

// The subnet or virtual network ID the environment occupies, plus the
// read-only name and type the service resolves.
type VirtualNetworkProfile struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Type   string `json:"type,omitempty"`
	Subnet string `json:"subnet,omitempty"`
}

type AppServiceEnvironmentResource struct {
	ID         string                          `json:"id,omitempty"`
	Name       string                          `json:"name,omitempty"`
	Type       string                          `json:"type,omitempty"`
	Kind       string                          `json:"kind,omitempty"`
	Location   string                          `json:"location,omitempty"`
	Tags       map[string]string               `json:"tags,omitempty"`
	Properties AppServiceEnvironmentProperties `json:"properties"`
}

// Exactly the AppServiceEnvironment members declared; the maximum machine
// count and front-end VM size, which the simulator does not model, are absent
// rather than invented.
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

// The envelope of the CustomDnsSuffixConfiguration and
// AseV3NetworkingConfiguration children an environment carries inline, both
// ProxyResources the SDK flattens.
type ASEConfigurationResource struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	Type       string `json:"type,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Properties any    `json:"properties,omitempty"`
}

// The four address lists are read-only, filled from the environment's leases
// and its subnet-derived address, never from the client.
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

type ASECustomDNSSuffixProperties struct {
	ProvisioningState         string `json:"provisioningState,omitempty"`
	ProvisioningDetails       string `json:"provisioningDetails,omitempty"`
	DNSSuffix                 string `json:"dnsSuffix,omitempty"`
	CertificateURL            string `json:"certificateUrl,omitempty"`
	KeyVaultReferenceIdentity string `json:"keyVaultReferenceIdentity,omitempty"`
}

// The front-end ("multiRolePools/default") and worker pools.
type WebWorkerPoolResource struct {
	ID         string                  `json:"id,omitempty"`
	Name       string                  `json:"name,omitempty"`
	Type       string                  `json:"type,omitempty"`
	Kind       string                  `json:"kind,omitempty"`
	Sku        *AppServicePlanSku      `json:"sku,omitempty"`
	Properties WebWorkerPoolProperties `json:"properties"`
}

// instanceNames is read-only, named as the App Service platform names
// instances.
type WebWorkerPoolProperties struct {
	WorkerSizeID  *int32   `json:"workerSizeId,omitempty"`
	ComputeMode   string   `json:"computeMode,omitempty"`
	WorkerSize    string   `json:"workerSize,omitempty"`
	WorkerCount   *int32   `json:"workerCount,omitempty"`
	InstanceNames []string `json:"instanceNames,omitempty"`
}

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

type KubeExtendedLocation struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

// The cluster-configuration documents are property bags, so every member
// round-trips; the Arc kubeConfig is a declared secret, dropped on read.
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

var (
	webHostingEnvironments sim.Store[AppServiceEnvironmentResource]
	webEnvironmentPools    sim.Store[WebWorkerPoolResource]
	webEnvironmentPECs     sim.Store[WebSitePrivateEndpointConnection]
	webKubeEnvironments    sim.Store[KubeEnvironmentResource]
)

const (
	aseResourceType  = "Microsoft.Web/hostingEnvironments"
	kubeResourceType = "Microsoft.Web/kubeEnvironments"
	// The specification pins the front-end pool's path segment to "default".
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

func aseResourceID(r *http.Request) string {
	return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Web", "hostingEnvironments", sim.PathParam(r, "name"))
}

func kubeResourceID(r *http.Request) string {
	return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Web", "kubeEnvironments", sim.PathParam(r, "name"))
}

// Writes the 404 when the environment does not exist.
func aseLookup(w http.ResponseWriter, r *http.Request) (AppServiceEnvironmentResource, bool) {
	ase, ok := webHostingEnvironments.Get(aseResourceID(r))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Web/hostingEnvironments/%s' under resource group '%s' was not found.",
			sim.PathParam(r, "name"), sim.PathParam(r, "resourceGroupName"))
		return AppServiceEnvironmentResource{}, false
	}
	return ase, true
}

// Case-insensitive, as Azure Resource Manager compares IDs.
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

// The plans whose hostingEnvironmentProfile names the environment.
func asePlans(aseID string) []AppServicePlan {
	return azureAppServicePlans.Filter(func(p AppServicePlan) bool {
		return p.Properties.HostingEnvironmentProfile != nil &&
			strings.EqualFold(p.Properties.HostingEnvironmentProfile.ID, aseID)
	})
}

// The sites naming it directly, plus the sites whose plan is placed in it.
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

// Fills the read-only name and type beside the id; a reference to a missing
// environment is refused rather than stored.
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

// Azure reserves a subnet's first four addresses, so the first assignable is
// the fifth, and the environment's subnet is delegated to it alone.
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

func aseSubnetPrefix(subnet Subnet) string {
	if subnet.Properties.AddressPrefix != "" {
		return subnet.Properties.AddressPrefix
	}
	if len(subnet.Properties.AddressPrefixes) > 0 {
		return subnet.Properties.AddressPrefixes[0]
	}
	return ""
}

// Every load-balancing mode but "None" publishes at least one endpoint
// internally.
func aseIsInternal(ase AppServiceEnvironmentResource) bool {
	mode := strings.TrimSpace(ase.Properties.InternalLoadBalancingMode)
	return mode != "" && !strings.EqualFold(mode, "None")
}

func aseNetworkingProperties(ase AppServiceEnvironmentResource) ASENetworkingProperties {
	var props ASENetworkingProperties
	if ase.Properties.NetworkingConfiguration == nil {
		return props
	}
	// The record round-trips through the store's JSON, so the property bag comes
	// back decoded rather than as the struct.
	raw, err := json.Marshal(ase.Properties.NetworkingConfiguration.Properties)
	if err != nil {
		return props
	}
	_ = json.Unmarshal(raw, &props)
	return props
}

// The internal subnet address when it load-balances internally, the leased
// public address otherwise.
func aseInboundAddresses(ase AppServiceEnvironmentResource) []string {
	net := aseNetworkingProperties(ase)
	if len(net.InternalInboundIPAddresses) > 0 {
		return net.InternalInboundIPAddresses
	}
	return net.ExternalInboundIPAddresses
}

// An internal-load-balancing environment gets `<name>.appserviceenvironment.net`;
// an external one `<name>.p.azurewebsites.net`, as the specification's
// AppServiceEnvironments_Get example carries.
func aseDNSSuffix(name string, internal bool) string {
	if internal {
		return name + ".appserviceenvironment.net"
	}
	return name + ".p.azurewebsites.net"
}

// Fills the computed read-only members, so a read reports the current state.
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

func aseConfigurationChild(aseID, segment, name string, props any) *ASEConfigurationResource {
	return &ASEConfigurationResource{
		ID:         aseID + "/configurations/" + segment,
		Name:       name,
		Type:       aseResourceType + "/configurations/" + segment,
		Properties: props,
	}
}

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
	registerWebEnvironmentPoolMetrics(ase)
	registerWebEnvironmentDiagnostics(ase)
	registerWebKubeEnvironments(srv)
}

func registerWebEnvironmentCRUD(srv *sim.Server, ase func(string, string, http.HandlerFunc)) {
	ase("PUT", "", func(w http.ResponseWriter, r *http.Request) {
		var req AppServiceEnvironmentResource
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := aseResourceID(r)
		name := sim.PathParam(r, "name")
		existing, updating := webHostingEnvironments.Get(id)

		if req.Location == "" && !updating {
			AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}
		if req.Properties.VirtualNetwork == nil || req.Properties.VirtualNetwork.ID == "" {
			AzureError(w, "InvalidRequestContent",
				"The 'properties.virtualNetwork.id' property is required: an App Service Environment is deployed into a subnet.",
				http.StatusBadRequest)
			return
		}
		profile, subnetPrefix, err := aseResolveVirtualNetwork(*req.Properties.VirtualNetwork)
		if err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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
			AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "%v", err)
			return
		}
		webHostingEnvironments.Put(id, next)
		// Every environment has a front-end pool; a create that omits it still reads
		// it back.
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

	// AppServiceEnvironments_Update merges the envelope's members; omitted ones
	// keep their values.
	ase("PATCH", "", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		var patch AppServiceEnvironmentResource
		if err := sim.ReadJSON(r, &patch); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if patch.Kind != "" {
			row.Kind = patch.Kind
		}
		p := patch.Properties
		if p.VirtualNetwork != nil && p.VirtualNetwork.ID != "" {
			profile, prefix, err := aseResolveVirtualNetwork(*p.VirtualNetwork)
			if err != nil {
				AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
				return
			}
			row.Properties.VirtualNetwork = &profile
			if err := aseRederiveInternalAddress(&row, prefix); err != nil {
				AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "%v", err)
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
			// The documented no-content outcome for a missing environment.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// The resource provider refuses to orphan hosted plans.
		if plans := asePlans(row.ID); len(plans) > 0 && !strings.EqualFold(r.URL.Query().Get("forceDelete"), "true") {
			AzureErrorf(w, "Conflict", http.StatusConflict,
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
		prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Web", "hostingEnvironments") + "/"
		writeASECollection(w, r, prefix)
	})
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/hostingEnvironments", func(w http.ResponseWriter, r *http.Request) {
		writeASECollection(w, r, armSubscriptionID(sim.PathParam(r, "subscriptionId"))+"/")
	})
}

func writeASECollection(w http.ResponseWriter, r *http.Request, prefix string) {
	rows := webHostingEnvironments.Filter(func(a AppServiceEnvironmentResource) bool {
		return strings.HasPrefix(strings.ToLower(a.ID), strings.ToLower(prefix))
	})
	out := make([]AppServiceEnvironmentResource, 0, len(rows))
	for _, row := range rows {
		out = append(out, aseProject(row))
	}
	writeARMCollection(w, r, out, func(a AppServiceEnvironmentResource) string { return a.ID })
}

// Ordered by key, with the nextLink paged collections carry.
func writeARMCollection[T any](w http.ResponseWriter, r *http.Request, items []T, key func(T) string) {
	page, token, pageOK := armPage(w, r, items, key)
	writeARMCollectionPage(w, r, page, token, pageOK)
}

func writeARMFixedCollection[T any](w http.ResponseWriter, r *http.Request, items []T) {
	page, token, pageOK := armSeqPage(w, r, items)
	writeARMCollectionPage(w, r, page, token, pageOK)
}

func writeARMCollectionPage[T any](w http.ResponseWriter, r *http.Request, page []T, token string, pageOK bool) {
	if !pageOK {
		return
	}
	body := map[string]any{"value": page}
	if token != "" {
		body["nextLink"] = armNextLink(r, token)
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

// Returns the profile with its read-only name and type, plus the occupied
// subnet's prefix.
func aseResolveVirtualNetwork(req VirtualNetworkProfile) (VirtualNetworkProfile, string, error) {
	out := VirtualNetworkProfile{ID: req.ID, Subnet: req.Subnet}
	// The id names the subnet (the modern spelling every example uses) or the
	// virtual network, with `subnet` naming the subnet in it.
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

func azureVnetByID(id string) (VirtualNetwork, bool) {
	if vnet, ok := azureVnets.Get(id); ok {
		return vnet, true
	}
	for _, vnet := range azureVnets.List() {
		if strings.EqualFold(vnet.ID, id) {
			return vnet, true
		}
	}
	return VirtualNetwork{}, false
}

func azureSubnetByID(id string) (Subnet, bool) {
	if subnet, ok := azureSubnets.Get(id); ok {
		return subnet, true
	}
	if azureSubnets == nil {
		return Subnet{}, false
	}
	return azureSubnetsByLowerID.Lookup(azureSubnets, strings.ToLower(id), func(subnet Subnet) []string {
		return []string{strings.ToLower(subnet.ID)}
	})
}

var azureSubnetsByLowerID sim.GenerationIndex[Subnet]

// A lease from the publicIPAddresses pool for the address it is reached at
// from outside and leaves from, and a subnet-derived address when it
// load-balances internally. An update keeps held addresses.
func aseAssignAddresses(next *AppServiceEnvironmentResource, existing AppServiceEnvironmentResource, updating bool, subnetPrefix string) error {
	current := ASENetworkingProperties{}
	if updating {
		current = aseNetworkingProperties(existing)
	}
	// The settable half of the networking configuration travels on the create
	// envelope too.
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
	// Settable on create only, as the specification says.
	if !updating && requested.InboundIPAddressOverride != "" {
		settings.InboundIPAddressOverride = requested.InboundIPAddressOverride
	}

	outbound := current.WindowsOutboundIPAddresses
	if len(outbound) == 0 {
		lease, err := ipam.ReserveAzurePublicIPv4(next.ID, nil)
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
		// An external environment answers on the address it leaves from, as the
		// specification's AppServiceEnvironments_GetVipInfo example reports.
		settings.ExternalInboundIPAddresses = outbound
	}
	next.Properties.NetworkingConfiguration = aseConfigurationChild(next.ID, "networking", "networking", settings)
	return nil
}

// After the environment moved to another subnet.
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

func aseReleaseAddresses(row AppServiceEnvironmentResource) {
	for _, addr := range aseNetworkingProperties(row).WindowsOutboundIPAddresses {
		if ip := net.ParseIP(addr); ip != nil {
			ipam.ReleasePublicIPv4(ip)
		}
	}
}

// Its worker count is the environment's front-end instance count, what
// multiRoleCount reports.
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

func registerWebEnvironmentNetworking(ase func(string, string, http.HandlerFunc)) {
	ase("GET", "/configurations/networking", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, aseConfigurationChild(row.ID, "networking", "networking", aseNetworkingProperties(row)))
	})

	// Only the three settable switches; the address lists are the platform's.
	ase("PUT", "/configurations/networking", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		var req ASEConfigurationResource
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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

	ase("GET", "/configurations/customdnssuffix", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		child := row.Properties.CustomDNSSuffixConfiguration
		if child == nil {
			// Before a suffix is set the resource exists with an empty configuration, as
			// the service answers.
			child = aseCustomDNSSuffixChild(row.ID, ASECustomDNSSuffixProperties{})
		}
		sim.WriteJSON(w, http.StatusOK, child)
	})

	ase("PUT", "/configurations/customdnssuffix", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		var req ASEConfigurationResource
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var props ASECustomDNSSuffixProperties
		if raw, err := json.Marshal(req.Properties); err == nil {
			_ = json.Unmarshal(raw, &props)
		}
		if props.DNSSuffix == "" {
			AzureError(w, "InvalidRequestContent",
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

	ase("DELETE", "/configurations/customdnssuffix", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		row.Properties.CustomDNSSuffixConfiguration = nil
		webHostingEnvironments.Put(row.ID, row)
		w.WriteHeader(http.StatusNoContent)
	})

	// The addresses the environment is reached at and leaves from.
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
		// The simulator reserves no extra virtual IPs for IP-based SSL, so there are
		// no vipMappings.
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":         row.ID + "/capacities/virtualip",
			"name":       row.Name,
			"type":       aseResourceType + "/capacities",
			"properties": props,
		})
	})

	// Computed from the environment's addresses, subnet and enabled inbound
	// features; a disabled feature's endpoint is absent.
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
				// The App Service Environment FTP endpoint: the control connection and its
				// passive data-channel range.
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
		writeARMFixedCollection(w, r, endpoints)
	})
}

// In the spelling the client used.
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

func registerWebEnvironmentPools(ase func(string, string, http.HandlerFunc)) {
	// The front-end pool's segment is the literal "default"; worker pools are
	// client-named.
	ase("GET", "/multiRolePools", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		writeARMCollection(w, r, asePoolsUnder(row.ID, "multiRolePools"), webWorkerPoolID)
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
		writeARMCollection(w, r, asePoolsUnder(row.ID, "workerPools"), webWorkerPoolID)
	})
	ase("GET", "/workerPools/{workerPoolName}", asePoolRead("workerPools"))
	ase("PUT", "/workerPools/{workerPoolName}", asePoolWrite("workerPools", false))
	ase("PATCH", "/workerPools/{workerPoolName}", asePoolWrite("workerPools", true))
	ase("GET", "/workerPools/{workerPoolName}/skus", asePoolSKUs("workerPools"))
	ase("GET", "/workerPools/{workerPoolName}/usages", asePoolUsages())

	// Computed from the pools and the plans placed in the environment.
	ase("GET", "/capacities/compute", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		page, token, pageOK := armKeyedPage(w, r, aseStampCapacities(row))
		writeARMCollectionPage(w, r, page, token, pageOK)
	})
}

func asePoolName(r *http.Request, collection string) string {
	if collection == "multiRolePools" {
		return multiRolePoolName
	}
	return sim.PathParam(r, "workerPoolName")
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

// One name per worker, in the platform's numbering.
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
		pool, found := webEnvironmentPools.Get(armChildID(row.ID, collection, name))
		if !found {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/hostingEnvironments/%s/%s/%s' was not found.", row.Name, collection, name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, asePoolProject(pool))
	}
}

func asePoolWrite(collection string, merge bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		name := asePoolName(r, collection)
		id := armChildID(row.ID, collection, name)
		var req WebWorkerPoolResource
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		existing, found := webEnvironmentPools.Get(id)
		if merge && !found {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

// Reports the SKU the pool runs. The SkuInfo carries no capacity envelope: the
// simulator enforces no worker-count limits, so declaring them would invent
// one.
func asePoolSKUs(collection string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		name := asePoolName(r, collection)
		pool, found := webEnvironmentPools.Get(armChildID(row.ID, collection, name))
		if !found {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
		writeARMFixedCollection(w, r, skus)
	}
}

// The simulator meters no pool quota, so the collection is empty, as the
// per-site and per-location usage lists answer.
func asePoolUsages() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := aseLookup(w, r); !ok {
			return
		}
		writeARMFixedCollection(w, r, []map[string]any{})
	}
}

// The swagger's WorkerSizeOptions; a pool sized outside it reports its
// capacity without the member.
var aseStampWorkerSizes = map[string]string{
	"small": "Small", "medium": "Medium", "large": "Large",
	"d1": "D1", "d2": "D2", "d3": "D3",
	"smallv3": "SmallV3", "mediumv3": "MediumV3", "largev3": "LargeV3",
	"nestedsmall": "NestedSmall", "nestedsmalllinux": "NestedSmallLinux",
	"default": "Default",
}

// Per pool, its workers less what placed plans take. Pages by pool, multi-role
// ahead of worker pools in numeric order, so a pool removed between calls
// shifts no other capacity across the page boundary.
func aseStampCapacities(row AppServiceEnvironmentResource) []azureKeyed[map[string]any] {
	plans := asePlans(row.ID)
	out := []azureKeyed[map[string]any]{}
	type keyedPool struct {
		key  string
		pool WebWorkerPoolResource
	}
	var pools []keyedPool
	for _, pool := range asePoolsUnder(row.ID, "multiRolePools") {
		pools = append(pools, keyedPool{"0/" + pool.Name, pool})
	}
	for _, pool := range asePoolsUnder(row.ID, "workerPools") {
		key := "1/" + pool.Name
		if n, err := strconv.ParseInt(pool.Name, 10, 64); err == nil {
			key = "1/" + listq.IntKey(n)
		}
		pools = append(pools, keyedPool{key, pool})
	}
	for _, kp := range pools {
		pool := kp.pool
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
		out = append(out, azureKeyed[map[string]any]{kp.key, capacity})
	}
	return out
}

func registerWebEnvironmentInventory(ase func(string, string, http.HandlerFunc)) {
	ase("GET", "/serverfarms", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		plans := asePlans(row.ID)
		writeARMCollection(w, r, plans, func(p AppServicePlan) string { return p.ID })
	})

	ase("GET", "/sites", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		writeARMCollection(w, r, aseSites(row.ID), webSiteID)
	})

	// The environment enforces no quota, so it has none to report.
	ase("GET", "/usages", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := aseLookup(w, r); !ok {
			return
		}
		writeARMFixedCollection(w, r, []map[string]any{})
	})

	// Every environment operation completes before its response, so none is in
	// flight; the specification's example reports the same empty list.
	ase("GET", "/operations", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := aseLookup(w, r); !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, []any{})
	})
}

func webSiteID(s Site) string { return s.ID }

func webWorkerPoolID(p WebWorkerPoolResource) string { return p.ID }

func aseSortedSites(aseID string) []Site {
	sites := aseSites(aseID)
	sort.Slice(sites, func(i, j int) bool { return sites[i].ID < sites[j].ID })
	return sites
}

func registerWebEnvironmentLifecycle(ase func(string, string, http.HandlerFunc)) {
	// Suspending stops the environment's apps and resuming starts them, so both
	// answer with the apps they moved.
	ase("POST", "/suspend", aseSetSuspended(true))
	ase("POST", "/resume", aseSetSuspended(false))

	// The hosted apps move with the environment, the collection the operation
	// answers with.
	ase("POST", "/changeVirtualNetwork", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		var req VirtualNetworkProfile
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.ID == "" {
			AzureError(w, "InvalidRequestContent", "The 'id' property of the virtual network profile is required.", http.StatusBadRequest)
			return
		}
		profile, prefix, err := aseResolveVirtualNetwork(req)
		if err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		row.Properties.VirtualNetwork = &profile
		if err := aseRederiveInternalAddress(&row, prefix); err != nil {
			AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "%v", err)
			return
		}
		webHostingEnvironments.Put(row.ID, row)
		aseAcceptedCollection(w, r, row, aseSites(row.ID))
	})

	// The apps restart: their containers are torn down, and the next request
	// starts each again.
	ase("POST", "/reboot", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		sites := aseSortedSites(row.ID)
		opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
			for _, site := range sites {
				restartAzureFunctionInstance(site)
				recordWebSiteEvent(site.ID, "Restart", webEventCausePlatform)
			}
			return nil
		})
		aseAccepted(w, r, row, opID)
	})

	// The simulator runs no upgrade schedule, so this notification is what makes
	// an upgrade available: upgradeAvailability goes None → Ready, which the Manual
	// preference requires before an upgrade starts.
	ase("POST", "/testUpgradeAvailableNotification", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		row.Properties.UpgradeAvailability = "Ready"
		webHostingEnvironments.Put(row.ID, row)
		w.WriteHeader(http.StatusOK)
	})

	// Refused with no upgrade available.
	ase("POST", "/upgrade", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		if !strings.EqualFold(row.Properties.UpgradeAvailability, "Ready") {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest,
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

// aseAccepted answers the two operations the specification declares 202-only,
// with the poll coordinates of an accepted operation.

// For an operation long-running via Location whose result is the collection of
// apps it moved: the collection becomes the operation's result, served by the
// Location poll. A synchronous answer is unreadable: for a long-running pageable
// operation the SDK's no-op poller replaces the pager it built with a
// zero-valued one, whose reads panic on a nil handler. The service answers 202,
// as the specification documents.
func aseAcceptedCollection(w http.ResponseWriter, r *http.Request, row AppServiceEnvironmentResource, sites []Site) {
	page, token, pageOK := armPage(w, r, sites, webSiteID)
	if !pageOK {
		return
	}
	body := map[string]any{"value": page}
	if token != "" {
		body["nextLink"] = armNextLink(r, token)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
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
	writeAzureAsyncCreateHeaders(w, opID,
		azureAsyncOperationHeader(r, sub, "Microsoft.Web", row.Location, "operationStatuses", opID, apiVersion),
		azureAsyncOperationHeader(r, sub, "Microsoft.Web", row.Location, "operationResults", opID, apiVersion))
	w.WriteHeader(http.StatusAccepted)
}

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
			if current, ok := azfSites.Get(site.ID); ok {
				if err := webApplySiteState(current); err != nil {
					AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
					return
				}
			}
			recordWebSiteEvent(site.ID, operation, webEventCausePlatform)
		}
		row.Properties.Suspended = boolPtr(suspended)
		webHostingEnvironments.Put(row.ID, row)
		aseAcceptedCollection(w, r, row, aseSites(row.ID))
	}
}

func registerWebEnvironmentPrivateEndpoints(ase func(string, string, http.HandlerFunc)) {
	ase("GET", "/privateEndpointConnections", func(w http.ResponseWriter, r *http.Request) {
		row, ok := aseLookup(w, r)
		if !ok {
			return
		}
		prefix := row.ID + "/privateEndpointConnections/"
		conns := webEnvironmentPECs.Filter(func(c WebSitePrivateEndpointConnection) bool {
			return strings.HasPrefix(c.ID, prefix)
		})
		page, token, pageOK := armPage(w, r, conns, func(c WebSitePrivateEndpointConnection) string { return c.ID })
		out := make([]map[string]any, 0, len(page))
		for _, conn := range page {
			out = append(out, renderWebSitePEC(conn))
		}
		writeARMCollectionPage(w, r, out, token, pageOK)
	})

	ase("GET", "/privateEndpointConnections/{privateEndpointConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		conn, ok := aseLookupPEC(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, renderWebSitePEC(conn))
	})

	// The owner moves the state of a connection a private endpoint opened.
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
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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

	ase("DELETE", "/privateEndpointConnections/{privateEndpointConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		conn, ok := aseLookupPEC(w, r)
		if !ok {
			return
		}
		webEnvironmentPECs.Delete(conn.ID)
		sim.WriteJSON(w, http.StatusOK, map[string]any{"id": conn.ID})
	})

	// An App Service Environment publishes no named group: an endpoint reaches the
	// environment itself, so Microsoft's table names no group-id for
	// Microsoft.Web/hostingEnvironments and the specification's example is an
	// empty wrapper.
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
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Private endpoint connection '%s' not found.", name)
		return WebSitePrivateEndpointConnection{}, false
	}
	return conn, true
}

func registerWebEnvironmentPoolMetrics(ase func(string, string, http.HandlerFunc)) {
	// Microsoft.Insights publishes no metric series for these pools here, so the
	// definitions are an empty collection, which is the answer.
	definitions := func(w http.ResponseWriter, r *http.Request) {
		if webEnvironmentMissing(w, r) {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
	}
	ase("GET", "/multiRolePools/"+multiRolePoolName+"/metricdefinitions", definitions)
	ase("GET", "/multiRolePools/"+multiRolePoolName+"/instances/{instance}/metricdefinitions", definitions)
	ase("GET", "/workerPools/{workerPoolName}/metricdefinitions", definitions)
	ase("GET", "/workerPools/{workerPoolName}/instances/{instance}/metricdefinitions", definitions)
	// Measured, not catalogued — see web_environment_outbound.go.
	ase("GET", "/outboundNetworkDependenciesEndpoints", handleASEOutboundNetworkDependencies)
}

func registerWebKubeEnvironments(srv *sim.Server) {
	kube := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/kubeEnvironments/{name}"+suffix, h)
	}

	kube("PUT", "", func(w http.ResponseWriter, r *http.Request) {
		var req KubeEnvironmentResource
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := kubeResourceID(r)
		name := sim.PathParam(r, "name")
		existing, updating := webKubeEnvironments.Get(id)
		if req.Location == "" && !updating {
			AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
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
			// The type is read-only; an Arc-connected cluster is a custom location.
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
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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
		prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Web", "kubeEnvironments") + "/"
		writeKubeCollection(w, r, prefix)
	})
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/kubeEnvironments", func(w http.ResponseWriter, r *http.Request) {
		writeKubeCollection(w, r, armSubscriptionID(sim.PathParam(r, "subscriptionId"))+"/")
	})
}

func kubeLookup(w http.ResponseWriter, r *http.Request) (KubeEnvironmentResource, bool) {
	row, ok := webKubeEnvironments.Get(kubeResourceID(r))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
	out := make([]KubeEnvironmentResource, 0, len(rows))
	for _, row := range rows {
		out = append(out, kubeProject(row))
	}
	writeARMCollection(w, r, out, func(k KubeEnvironmentResource) string { return k.ID })
}

// The cluster's name under the address it answers on, as App Service shapes an
// Arc-connected cluster's domain.
func kubeDefaultDomain(name, staticIP string) string {
	if staticIP == "" {
		return ""
	}
	return fmt.Sprintf("%s.%s.k4apps.io", name, strings.ReplaceAll(staticIP, ".", "-"))
}

// The Arc kubeConfig is a declared secret, never handed back.
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
