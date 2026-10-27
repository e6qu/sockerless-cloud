package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/ipam"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
)

type PublicIPAddress struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Location string            `json:"location"`
	Tags     map[string]string `json:"tags"`
	Sku      *SkuName          `json:"sku,omitempty"`
	// terraform-provider-azurerm drifts an absent list to null, so emit a non-nil
	// slice.
	Zones      []string                  `json:"zones"`
	Properties PublicIPAddressProperties `json:"properties"`
}

type PublicIPAddressProperties struct {
	PublicIPAddress          string `json:"ipAddress,omitempty"`
	PublicIPAllocationMethod string `json:"publicIPAllocationMethod,omitempty"`
	PublicIPAddressVersion   string `json:"publicIPAddressVersion,omitempty"`
	IdleTimeoutInMinutes     int32  `json:"idleTimeoutInMinutes,omitempty"`
	// A non-nil list keeps the provider's flatten from drifting to null.
	IPTags            []any  `json:"ipTags"`
	ProvisioningState string `json:"provisioningState,omitempty"`
}

type PublicIPPrefix struct {
	ID         string                   `json:"id"`
	Name       string                   `json:"name"`
	Type       string                   `json:"type"`
	Location   string                   `json:"location"`
	Tags       map[string]string        `json:"tags"`
	Sku        *SkuName                 `json:"sku,omitempty"`
	Zones      []string                 `json:"zones"`
	Properties PublicIPPrefixProperties `json:"properties"`
}

type PublicIPPrefixProperties struct {
	IPPrefix               string `json:"ipPrefix,omitempty"`
	PrefixLength           int32  `json:"prefixLength,omitempty"`
	PublicIPAddressVersion string `json:"publicIPAddressVersion,omitempty"`
	ProvisioningState      string `json:"provisioningState,omitempty"`
	ResourceGUID           string `json:"resourceGuid,omitempty"`
}

type LoadBalancer struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type"`
	Location   string                 `json:"location"`
	Tags       map[string]string      `json:"tags"`
	Sku        *SkuName               `json:"sku,omitempty"`
	Properties LoadBalancerProperties `json:"properties"`
}

type LoadBalancerProperties struct {
	FrontendIPConfigurations []LoadBalancerChild `json:"frontendIPConfigurations"`
	BackendAddressPools      []LoadBalancerChild `json:"backendAddressPools"`
	LoadBalancingRules       []LoadBalancerChild `json:"loadBalancingRules"`
	Probes                   []LoadBalancerChild `json:"probes"`
	ProvisioningState        string              `json:"provisioningState,omitempty"`
}

type LoadBalancerChild struct {
	ID         string         `json:"id,omitempty"`
	Name       string         `json:"name,omitempty"`
	Type       string         `json:"type,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

type NetworkInterface struct {
	ID         string                     `json:"id"`
	Name       string                     `json:"name"`
	Type       string                     `json:"type"`
	Location   string                     `json:"location"`
	Tags       map[string]string          `json:"tags,omitempty"`
	Properties NetworkInterfaceProperties `json:"properties"`
}

type NetworkInterfaceProperties struct {
	IPConfigurations            []NetworkInterfaceIPConfiguration `json:"ipConfigurations,omitempty"`
	DNSSettings                 map[string]any                    `json:"dnsSettings,omitempty"`
	EnableAcceleratedNetworking bool                              `json:"enableAcceleratedNetworking,omitempty"`
	EnableIPForwarding          bool                              `json:"enableIPForwarding,omitempty"`
	NetworkSecurityGroup        *SubResource                      `json:"networkSecurityGroup,omitempty"`
	ProvisioningState           string                            `json:"provisioningState,omitempty"`
	MacAddress                  string                            `json:"macAddress,omitempty"`
	Primary                     bool                              `json:"primary,omitempty"`
	VirtualMachine              *SubResource                      `json:"virtualMachine,omitempty"`
}

type NetworkInterfaceIPConfiguration struct {
	ID         string                                    `json:"id,omitempty"`
	Name       string                                    `json:"name"`
	Type       string                                    `json:"type,omitempty"`
	Properties NetworkInterfaceIPConfigurationProperties `json:"properties"`
}

type NetworkInterfaceIPConfigurationProperties struct {
	Subnet                          *SubResource        `json:"subnet,omitempty"`
	PublicIPAddress                 *SubResource        `json:"publicIPAddress,omitempty"`
	LoadBalancerBackendAddressPools []LoadBalancerChild `json:"loadBalancerBackendAddressPools,omitempty"`
	PrivateIPAddress                string              `json:"privateIPAddress,omitempty"`
	PrivateIPAllocationMethod       string              `json:"privateIPAllocationMethod,omitempty"`
	PrivateIPAddressVersion         string              `json:"privateIPAddressVersion,omitempty"`
	Primary                         bool                `json:"primary,omitempty"`
	ProvisioningState               string              `json:"provisioningState,omitempty"`
	// A network security group rule written against an application security
	// group resolves to its members when the simulator programs the filter.
	ApplicationSecurityGroups []SubResource `json:"applicationSecurityGroups,omitempty"`
	// A workload joins an application gateway's backend through its interface, as
	// it joins a load balancer's; the pool reports these members and the gateway
	// forwards to them.
	ApplicationGatewayBackendAddressPools []SubResource `json:"applicationGatewayBackendAddressPools,omitempty"`
}

type VirtualMachine struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Location string            `json:"location"`
	Tags     map[string]string `json:"tags,omitempty"`
	// The Instance Metadata Service mints tokens for the identities named here.
	Identity   *SiteIdentity `json:"identity,omitempty"`
	Properties VMProperties  `json:"properties"`
	// SystemIdentityClientID is the system-assigned identity's application ID,
	// which the directory reads and the wire shape does not carry.
	SystemIdentityClientID string `json:"-"`
}

type VMProperties struct {
	HardwareProfile map[string]any   `json:"hardwareProfile,omitempty"`
	StorageProfile  map[string]any   `json:"storageProfile,omitempty"`
	OSProfile       map[string]any   `json:"osProfile,omitempty"`
	NetworkProfile  VMNetworkProfile `json:"networkProfile,omitempty"`
	// RetrieveBootDiagnosticsData reads bootDiagnostics, and a dropped member
	// would read back missing and drift in a client that sent it.
	DiagnosticsProfile map[string]any `json:"diagnosticsProfile,omitempty"`
	// Priority and EvictionPolicy make a machine a Spot machine and decide what
	// an eviction does.
	Priority          string          `json:"priority,omitempty"`
	EvictionPolicy    string          `json:"evictionPolicy,omitempty"`
	ProvisioningState string          `json:"provisioningState,omitempty"`
	VMID              string          `json:"vmId,omitempty"`
	InstanceView      *VMInstanceView `json:"instanceView,omitempty"`
	// The machine reads UserData back from the Instance Metadata Service; unlike
	// osProfile.customData, nothing in the guest runs it.
	UserData string `json:"userData,omitempty"`
}

type VMNetworkProfile struct {
	NetworkInterfaces []VMNetworkInterfaceRef `json:"networkInterfaces,omitempty"`
}

type VMNetworkInterfaceRef struct {
	ID         string         `json:"id"`
	Properties map[string]any `json:"properties,omitempty"`
}

type VMInstanceView struct {
	Statuses []VMStatus `json:"statuses,omitempty"`
}

type VMStatus struct {
	Code          string `json:"code"`
	Level         string `json:"level"`
	DisplayStatus string `json:"displayStatus"`
	Message       string `json:"message,omitempty"`
	Time          string `json:"time,omitempty"`
}

var (
	azurePublicIPs        sim.Store[PublicIPAddress]
	azurePublicIPPrefixes sim.Store[PublicIPPrefix]
	azureLBs              sim.Store[LoadBalancer]
	azureNICs             sim.Store[NetworkInterface]
	azureVMs              sim.Store[VirtualMachine]
	azureVMStates         sim.Store[string]
	// A generalized machine can be captured, and its instance view reports the OS
	// state.
	azureVMGeneralized sim.Store[bool]
)

func registerCompute(srv *sim.Server) {
	azurePublicIPs = sim.MakeStore[PublicIPAddress](srv.DB(), "network_public_ips")
	azurePublicIPPrefixes = sim.MakeStore[PublicIPPrefix](srv.DB(), "network_public_ip_prefixes")
	azureLBs = sim.MakeStore[LoadBalancer](srv.DB(), "network_load_balancers")
	azureNICs = sim.MakeStore[NetworkInterface](srv.DB(), "network_interfaces")
	azureVMs = sim.MakeStore[VirtualMachine](srv.DB(), "compute_virtual_machines")
	registerComputeOperations(srv)
	azureVMStates = sim.MakeStore[string](srv.DB(), "compute_virtual_machine_states")
	azureVMGeneralized = sim.MakeStore[bool](srv.DB(), "compute_virtual_machine_generalized")
	registerVirtualMachineOperationRecovery(srv)

	registerComputeCatalog(srv)
	registerVirtualMachineImages(srv)
	registerComputeImages(srv)
	registerPublicIPAddresses(srv)
	registerPublicIPPrefixes(srv)
	registerLoadBalancers(srv)
	registerNetworkInterfaces(srv)
	registerVirtualMachines(srv)
	registerVirtualMachineOperations(srv)
	registerVirtualMachineExtensions(srv)
	registerVirtualMachinePatchesAndCapture(srv)
}

func registerComputeCatalog(srv *sim.Server) {
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/vmSizes", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": azureVMSizeCatalogue()})
	})

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/skus", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": []map[string]any{
				{
					"resourceType": "virtualMachines",
					"name":         "Standard_B1s",
					"tier":         "Standard",
					"size":         "B1s",
					"family":       "standardBSFamily",
					"locations":    []string{"eastus", "westeurope"},
					"capabilities": []map[string]string{
						{"name": "vCPUs", "value": "1"},
						{"name": "MemoryGB", "value": "1"},
					},
				},
			},
		})
	})
}

// Public IPs, prefixes and load balancers read sku_tier back as a
// forces-replacement field; Azure defaults it to Regional.
func azureDefaultSkuTier(sku **SkuName, defaultName string) {
	if *sku == nil {
		*sku = &SkuName{Name: defaultName}
	}
	if (*sku).Tier == "" {
		(*sku).Tier = "Regional"
	}
}

func registerPublicIPAddresses(srv *sim.Server) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network"

	srv.HandleFunc("PUT "+armBase+"/publicIPAddresses/{publicIPName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "publicIPName")
		var req PublicIPAddress
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := armResourceID(sub, rg, "Microsoft.Network", "publicIPAddresses", name)
		pip := PublicIPAddress{
			ID:       id,
			Name:     name,
			Type:     "Microsoft.Network/publicIPAddresses",
			Location: req.Location,
			Tags:     req.Tags,
			Sku:      req.Sku,
			Zones:    req.Zones,
			Properties: PublicIPAddressProperties{
				PublicIPAddress:          req.Properties.PublicIPAddress,
				PublicIPAllocationMethod: req.Properties.PublicIPAllocationMethod,
				PublicIPAddressVersion:   req.Properties.PublicIPAddressVersion,
				IdleTimeoutInMinutes:     req.Properties.IdleTimeoutInMinutes,
				IPTags:                   req.Properties.IPTags,
				ProvisioningState:        "Succeeded",
			},
		}
		azureDefaultSkuTier(&pip.Sku, "Standard")
		if pip.Tags == nil {
			pip.Tags = map[string]string{}
		}
		if pip.Zones == nil {
			pip.Zones = []string{}
		}
		if pip.Properties.IPTags == nil {
			pip.Properties.IPTags = []any{}
		}
		if pip.Properties.IdleTimeoutInMinutes == 0 {
			pip.Properties.IdleTimeoutInMinutes = 4
		}
		if pip.Properties.PublicIPAllocationMethod == "" {
			pip.Properties.PublicIPAllocationMethod = "Dynamic"
		}
		if pip.Properties.PublicIPAddressVersion == "" {
			pip.Properties.PublicIPAddressVersion = "IPv4"
		}
		if pip.Properties.PublicIPAddress == "" && strings.EqualFold(pip.Properties.PublicIPAllocationMethod, "Static") {
			ip, err := ipam.ReserveAzurePublicIPv4(id, nil)
			if err != nil {
				AzureErrorf(w, "OperationNotAllowed", http.StatusServiceUnavailable, "failed to reserve real public IPv4 lease: %v", err)
				return
			}
			pip.Properties.PublicIPAddress = ip.String()
		}
		azurePublicIPs.Put(id, pip)
		sim.WriteJSON(w, http.StatusOK, pip)
	})

	srv.HandleFunc("GET "+armBase+"/publicIPAddresses/{publicIPName}", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "publicIPAddresses", sim.PathParam(r, "publicIPName"))
		pip, ok := azurePublicIPs.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		sim.WriteJSON(w, http.StatusOK, pip)
	})

	srv.HandleFunc("GET "+armBase+"/publicIPAddresses", func(w http.ResponseWriter, r *http.Request) {
		prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "publicIPAddresses") + "/"
		items := azurePublicIPs.Filter(func(p PublicIPAddress) bool { return strings.HasPrefix(p.ID, prefix) })
		if items == nil {
			items = []PublicIPAddress{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": items})
	})

	srv.HandleFunc("DELETE "+armBase+"/publicIPAddresses/{publicIPName}", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "publicIPAddresses", sim.PathParam(r, "publicIPName"))
		if pip, ok := azurePublicIPs.Get(id); ok {
			ipam.ReleasePublicIPv4(net.ParseIP(pip.Properties.PublicIPAddress))
		}
		azurePublicIPs.Delete(id)
		w.WriteHeader(http.StatusOK)
	})
}

func registerPublicIPPrefixes(srv *sim.Server) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network"

	srv.HandleFunc("PUT "+armBase+"/publicIPPrefixes/{publicIPPrefixName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "publicIPPrefixName")
		var req PublicIPPrefix
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := azurePublicIPPrefixID(sub, rg, name)
		prefix := PublicIPPrefix{
			ID:       id,
			Name:     name,
			Type:     "Microsoft.Network/publicIPPrefixes",
			Location: req.Location,
			Tags:     req.Tags,
			Sku:      req.Sku,
			Zones:    req.Zones,
			Properties: PublicIPPrefixProperties{
				IPPrefix:               req.Properties.IPPrefix,
				PrefixLength:           req.Properties.PrefixLength,
				PublicIPAddressVersion: req.Properties.PublicIPAddressVersion,
				ProvisioningState:      "Succeeded",
				ResourceGUID:           sim.NewUUID(),
			},
		}
		azureDefaultSkuTier(&prefix.Sku, "Standard")
		if prefix.Tags == nil {
			prefix.Tags = map[string]string{}
		}
		if prefix.Zones == nil {
			prefix.Zones = []string{}
		}
		if prefix.Properties.PublicIPAddressVersion == "" {
			prefix.Properties.PublicIPAddressVersion = "IPv4"
		}
		if prefix.Properties.PrefixLength == 0 {
			prefix.Properties.PrefixLength = 28
		}
		if prefix.Properties.IPPrefix == "" && strings.EqualFold(prefix.Properties.PublicIPAddressVersion, "IPv4") {
			prefix.Properties.IPPrefix = azurePublicIPPrefixCIDR(prefix.Properties.PrefixLength, len(azurePublicIPPrefixes.List()))
		}
		azurePublicIPPrefixes.Put(id, prefix)
		sim.WriteJSON(w, http.StatusOK, prefix)
	})

	srv.HandleFunc("GET "+armBase+"/publicIPPrefixes/{publicIPPrefixName}", func(w http.ResponseWriter, r *http.Request) {
		id := azurePublicIPPrefixID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "publicIPPrefixName"))
		prefix, ok := azurePublicIPPrefixes.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		sim.WriteJSON(w, http.StatusOK, prefix)
	})

	srv.HandleFunc("GET "+armBase+"/publicIPPrefixes", func(w http.ResponseWriter, r *http.Request) {
		prefixPath := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "publicIPPrefixes") + "/"
		items := azurePublicIPPrefixes.Filter(func(p PublicIPPrefix) bool { return strings.HasPrefix(p.ID, prefixPath) })
		if items == nil {
			items = []PublicIPPrefix{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": items})
	})

	srv.HandleFunc("DELETE "+armBase+"/publicIPPrefixes/{publicIPPrefixName}", func(w http.ResponseWriter, r *http.Request) {
		id := azurePublicIPPrefixID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "publicIPPrefixName"))
		azurePublicIPPrefixes.Delete(id)
		w.WriteHeader(http.StatusOK)
	})
}

func azurePublicIPPrefixID(sub, rg, name string) string {
	return armResourceID(sub, rg, "Microsoft.Network", "publicIPPrefixes", name)
}

func azurePublicIPPrefixCIDR(prefixLength int32, index int) string {
	if prefixLength <= 0 || prefixLength > 32 {
		prefixLength = 28
	}
	blockSize := 1
	if prefixLength < 32 {
		blockSize = 1 << (32 - prefixLength)
	}
	offset := (index * blockSize) % 256
	return fmt.Sprintf("203.0.113.%d/%d", offset, prefixLength)
}

func registerLoadBalancers(srv *sim.Server) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network"
	registerAzureLoadBalancerDataPlane(srv)
	srv.StartBackground("Azure Load Balancer health prober", func(ctx context.Context) {
		lbplane.SweepEvery(ctx, azureHealthProbeSweep, azureSweepLoadBalancerProbes)
	})

	srv.HandleFunc("PUT "+armBase+"/loadBalancers/{loadBalancerName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "loadBalancerName")
		var req LoadBalancer
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := azureLoadBalancerID(sub, rg, name)
		lb := LoadBalancer{
			ID:       id,
			Name:     name,
			Type:     "Microsoft.Network/loadBalancers",
			Location: req.Location,
			Tags:     req.Tags,
			Sku:      req.Sku,
			Properties: LoadBalancerProperties{
				FrontendIPConfigurations: normalizeLoadBalancerChildren(id, "frontendIPConfigurations", "Microsoft.Network/loadBalancers/frontendIPConfigurations", req.Properties.FrontendIPConfigurations),
				BackendAddressPools:      normalizeLoadBalancerChildren(id, "backendAddressPools", "Microsoft.Network/loadBalancers/backendAddressPools", req.Properties.BackendAddressPools),
				LoadBalancingRules:       normalizeLoadBalancerChildren(id, "loadBalancingRules", "Microsoft.Network/loadBalancers/loadBalancingRules", req.Properties.LoadBalancingRules),
				Probes:                   normalizeLoadBalancerChildren(id, "probes", "Microsoft.Network/loadBalancers/probes", req.Properties.Probes),
				ProvisioningState:        "Succeeded",
			},
		}
		azureDefaultSkuTier(&lb.Sku, "Standard")
		if lb.Tags == nil {
			lb.Tags = map[string]string{}
		}
		azureLBs.Put(id, lb)
		sim.WriteJSON(w, http.StatusOK, lb)
	})

	srv.HandleFunc("GET "+armBase+"/loadBalancers/{loadBalancerName}", func(w http.ResponseWriter, r *http.Request) {
		id := azureLoadBalancerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "loadBalancerName"))
		lb, ok := azureLBs.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		sim.WriteJSON(w, http.StatusOK, lb)
	})

	srv.HandleFunc("GET "+armBase+"/loadBalancers", func(w http.ResponseWriter, r *http.Request) {
		prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "loadBalancers") + "/"
		items := azureLBs.Filter(func(lb LoadBalancer) bool { return strings.HasPrefix(lb.ID, prefix) })
		if items == nil {
			items = []LoadBalancer{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": items})
	})

	srv.HandleFunc("DELETE "+armBase+"/loadBalancers/{loadBalancerName}", func(w http.ResponseWriter, r *http.Request) {
		id := azureLoadBalancerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "loadBalancerName"))
		azureLBs.Delete(id)
		w.WriteHeader(http.StatusOK)
	})

	// backendAddressPools is the only load-balancer child collection with its own
	// write operations; probes and loadBalancingRules change through the parent
	// PUT.
	registerLoadBalancerChild(srv, "backendAddressPools", "backendAddressPoolName", "Microsoft.Network/loadBalancers/backendAddressPools", true,
		func(lb *LoadBalancer) *[]LoadBalancerChild { return &lb.Properties.BackendAddressPools })
	registerLoadBalancerChild(srv, "probes", "probeName", "Microsoft.Network/loadBalancers/probes", false,
		func(lb *LoadBalancer) *[]LoadBalancerChild { return &lb.Properties.Probes })
	registerLoadBalancerChild(srv, "loadBalancingRules", "loadBalancingRuleName", "Microsoft.Network/loadBalancers/loadBalancingRules", false,
		func(lb *LoadBalancer) *[]LoadBalancerChild { return &lb.Properties.LoadBalancingRules })
}

func registerLoadBalancerChild(srv *sim.Server, collection, paramName, resourceType string, writable bool, children func(*LoadBalancer) *[]LoadBalancerChild) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network"
	pattern := armBase + "/loadBalancers/{loadBalancerName}/" + collection + "/{" + paramName + "}"

	putHandler := func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		lbName := sim.PathParam(r, "loadBalancerName")
		childName := sim.PathParam(r, paramName)
		lbID := azureLoadBalancerID(sub, rg, lbName)
		var req LoadBalancerChild
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		child := normalizeLoadBalancerChild(lbID, collection, resourceType, childName, req)
		if !azureLBs.Update(lbID, func(lb *LoadBalancer) {
			upsertLoadBalancerChild(children(lb), child)
		}) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", lbID)
			return
		}
		sim.WriteJSON(w, http.StatusOK, child)
	}
	deleteHandler := func(w http.ResponseWriter, r *http.Request) {
		lbID := azureLoadBalancerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "loadBalancerName"))
		childName := sim.PathParam(r, paramName)
		azureLBs.Update(lbID, func(lb *LoadBalancer) {
			removeLoadBalancerChild(children(lb), childName)
		})
		w.WriteHeader(http.StatusOK)
	}
	if writable {
		srv.HandleFunc("PUT "+pattern, putHandler)
		srv.HandleFunc("DELETE "+pattern, deleteHandler)
	}

	srv.HandleFunc("GET "+pattern, func(w http.ResponseWriter, r *http.Request) {
		lbID := azureLoadBalancerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "loadBalancerName"))
		childName := sim.PathParam(r, paramName)
		lb, ok := azureLBs.Get(lbID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", lbID)
			return
		}
		for _, child := range *children(&lb) {
			if strings.EqualFold(child.Name, childName) {
				sim.WriteJSON(w, http.StatusOK, child)
				return
			}
		}
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", lbID+"/"+collection+"/"+childName)
	})

}

func azureLoadBalancerID(sub, rg, name string) string {
	return armResourceID(sub, rg, "Microsoft.Network", "loadBalancers", name)
}

func normalizeLoadBalancerChildren(lbID, collection, resourceType string, children []LoadBalancerChild) []LoadBalancerChild {
	out := make([]LoadBalancerChild, 0, len(children))
	for _, child := range children {
		out = append(out, normalizeLoadBalancerChild(lbID, collection, resourceType, child.Name, child))
	}
	return out
}

func normalizeLoadBalancerChild(lbID, collection, resourceType, name string, child LoadBalancerChild) LoadBalancerChild {
	if name == "" {
		name = child.Name
	}
	child.ID = lbID + "/" + collection + "/" + name
	child.Name = name
	child.Type = resourceType
	if child.Properties == nil {
		child.Properties = map[string]any{}
	}
	child.Properties["provisioningState"] = "Succeeded"
	return child
}

func upsertLoadBalancerChild(children *[]LoadBalancerChild, child LoadBalancerChild) {
	for i := range *children {
		if strings.EqualFold((*children)[i].Name, child.Name) {
			(*children)[i] = child
			return
		}
	}
	*children = append(*children, child)
}

func removeLoadBalancerChild(children *[]LoadBalancerChild, name string) {
	filtered := (*children)[:0]
	for _, child := range *children {
		if !strings.EqualFold(child.Name, name) {
			filtered = append(filtered, child)
		}
	}
	*children = filtered
}

func registerAzureLoadBalancerDataPlane(srv *sim.Server) {
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			lb, frontend, ok := azureLoadBalancerFromDataPlaneHost(r.Host)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			handleAzureLoadBalancerDataPlane(w, r, lb, frontend)
		})
	})
}

// A handler wrapper runs this lookup ahead of every request, so it reads an
// index rather than scanning load balancers.
var azureLBsByFrontendAddress sim.GenerationIndex[LoadBalancer]

// A virtual machine joins a load balancer's or an application gateway's
// backend pool through its interface, so both look members up here.
var azureNICsByBackendPool sim.GenerationIndex[NetworkInterface]

// Pool IDs compare case-insensitively, as Azure Resource Manager IDs do.
func azureNICsInBackendPool(poolID string) []NetworkInterface {
	if azureNICs == nil || poolID == "" {
		return nil
	}
	return azureNICsByBackendPool.LookupAll(azureNICs, strings.ToLower(poolID),
		func(nic NetworkInterface) []string {
			var pools []string
			for _, ipcfg := range nic.Properties.IPConfigurations {
				for _, ref := range ipcfg.Properties.LoadBalancerBackendAddressPools {
					pools = append(pools, strings.ToLower(ref.ID))
				}
				for _, ref := range ipcfg.Properties.ApplicationGatewayBackendAddressPools {
					pools = append(pools, strings.ToLower(ref.ID))
				}
			}
			return pools
		})
}

// The index rebuild pays the public-IP store read once rather than per
// request.
func azureLoadBalancerFrontendAddresses(lb LoadBalancer) []string {
	var addresses []string
	for _, frontend := range lb.Properties.FrontendIPConfigurations {
		pipID := propertySubResourceID(frontend.Properties, "publicIPAddress")
		if pipID == "" {
			continue
		}
		if pip, ok := azurePublicIPs.Get(pipID); ok {
			addresses = append(addresses, strings.ToLower(pip.Properties.PublicIPAddress))
		}
	}
	return addresses
}

func azureLoadBalancerFromDataPlaneHost(host string) (LoadBalancer, LoadBalancerChild, bool) {
	hostname := lbplane.Hostname(host)
	if hostname == "" {
		return LoadBalancer{}, LoadBalancerChild{}, false
	}
	lb, ok := azureLBsByFrontendAddress.Lookup(azureLBs, hostname,
		azureLoadBalancerFrontendAddresses)
	if !ok {
		return LoadBalancer{}, LoadBalancerChild{}, false
	}
	for _, frontend := range lb.Properties.FrontendIPConfigurations {
		pipID := propertySubResourceID(frontend.Properties, "publicIPAddress")
		if pipID == "" {
			continue
		}
		if pip, found := azurePublicIPs.Get(pipID); found &&
			strings.EqualFold(pip.Properties.PublicIPAddress, hostname) {
			return lb, frontend, true
		}
	}
	return LoadBalancer{}, LoadBalancerChild{}, false
}

func handleAzureLoadBalancerDataPlane(w http.ResponseWriter, r *http.Request, lb LoadBalancer, frontend LoadBalancerChild) {
	rule, ok := azureLoadBalancerRuleForRequest(r, lb, frontend)
	if !ok {
		http.Error(w, "no matching load-balancing rule", http.StatusNotFound)
		return
	}
	target, ok := azureHealthyLoadBalancerTarget(lb, rule)
	if !ok {
		http.Error(w, "no healthy backends", http.StatusServiceUnavailable)
		return
	}
	// A load balancer forwards at layer 4: the backend receives the request as the
	// client sent it, Host header included, bounded by the rule's idle timeout.
	idle := time.Duration(intProperty(rule.Properties, "idleTimeoutInMinutes")) * time.Minute
	if idle <= 0 {
		idle = 4 * time.Minute
	}
	sim.DeclareWait(r.Context(), idle)
	_ = lbplane.Serve(w, r, lbplane.Upstream{
		Scheme:   "http",
		Address:  target.Address,
		Path:     r.URL.EscapedPath(),
		RawQuery: r.URL.RawQuery,
		Timeout:  idle,
		// A load balancer passes the client's address through, so the backend's
		// network security groups see the client.
		Admit: func(client netip.Addr) error {
			return azureAdmitIngress("Tcp", client, target.Address, target.Port)
		},
	}, lbplane.Answers{
		// Azure Load Balancer sends no reset for a dropped packet: the connection ends
		// without an answer.
		NotAdmitted: func(w http.ResponseWriter, _ error) { lbplane.CloseWithoutAnswer(w) },
		Failed:      func(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusBadGateway) },
	})
}

func azureLoadBalancerRuleForRequest(r *http.Request, lb LoadBalancer, frontend LoadBalancerChild) (LoadBalancerChild, bool) {
	port := 80
	if _, p, err := net.SplitHostPort(r.Host); err == nil {
		if parsed, perr := strconv.Atoi(p); perr == nil {
			port = parsed
		}
	}
	for _, rule := range lb.Properties.LoadBalancingRules {
		if !strings.EqualFold(propertySubResourceID(rule.Properties, "frontendIPConfiguration"), frontend.ID) {
			continue
		}
		if intProperty(rule.Properties, "frontendPort") == port {
			return rule, true
		}
	}
	return LoadBalancerChild{}, false
}

type azureLBTarget struct {
	Address string
	Port    int
}

func azureHealthyLoadBalancerTarget(lb LoadBalancer, rule LoadBalancerChild) (azureLBTarget, bool) {
	for _, target := range azureLoadBalancerTargets(lb, rule) {
		if azureLoadBalancerTargetInRotation(lb, rule, target) {
			return target, true
		}
	}
	return azureLBTarget{}, false
}

// azureLoadBalancerTargetInRotation: a backend its health probe marked up, or
// any backend of a rule with no probe.
func azureLoadBalancerTargetInRotation(lb LoadBalancer, rule LoadBalancerChild, target azureLBTarget) bool {
	probe, hasProbe := azureLoadBalancerRuleProbe(lb, rule)
	if !hasProbe {
		return true
	}
	health, _ := azureLoadBalancerHealth.Health(azureLoadBalancerHealthKey(lb, probe, target))
	return health.State == lbplane.HealthHealthy
}

func azureLoadBalancerTargets(lb LoadBalancer, rule LoadBalancerChild) []azureLBTarget {
	backendPort := intProperty(rule.Properties, "backendPort")
	if backendPort == 0 {
		backendPort = intProperty(rule.Properties, "frontendPort")
	}
	poolIDs := propertySubResourceIDs(rule.Properties, "backendAddressPools")
	if poolID := propertySubResourceID(rule.Properties, "backendAddressPool"); poolID != "" {
		poolIDs = append(poolIDs, poolID)
	}
	var targets []azureLBTarget
	for _, poolID := range poolIDs {
		for _, nic := range azureNICsInBackendPool(poolID) {
			for _, ipcfg := range nic.Properties.IPConfigurations {
				if !azureIPConfigInBackendPool(ipcfg, poolID) || ipcfg.Properties.PrivateIPAddress == "" {
					continue
				}
				targets = append(targets, azureLBTarget{
					Address: net.JoinHostPort(ipcfg.Properties.PrivateIPAddress, strconv.Itoa(backendPort)),
					Port:    backendPort,
				})
			}
		}
		for _, pool := range lb.Properties.BackendAddressPools {
			if !strings.EqualFold(pool.ID, poolID) {
				continue
			}
			for _, address := range propertyObjectSlice(pool.Properties, "loadBalancerBackendAddresses") {
				ip := stringProperty(address, "ipAddress")
				if ip == "" {
					continue
				}
				targets = append(targets, azureLBTarget{
					Address: net.JoinHostPort(ip, strconv.Itoa(backendPort)),
					Port:    backendPort,
				})
			}
		}
	}
	return targets
}

func azureIPConfigInBackendPool(ipcfg NetworkInterfaceIPConfiguration, poolID string) bool {
	for _, pool := range ipcfg.Properties.LoadBalancerBackendAddressPools {
		if strings.EqualFold(pool.ID, poolID) {
			return true
		}
	}
	return false
}

// azureHealthProbeSweep is how often the prober looks for due probes; each
// probe's intervalInSeconds sets how often one backend is probed.
const azureHealthProbeSweep = 250 * time.Millisecond

var azureLoadBalancerHealth = lbplane.NewHealthTracker[string]()

func azureLoadBalancerHealthKey(lb LoadBalancer, probe LoadBalancerChild, target azureLBTarget) string {
	return strings.ToLower(lb.ID) + "|" + strings.ToLower(probe.ID) + "|" + target.Address
}

// A rule without a probe sends traffic to every backend in its pool.
func azureLoadBalancerRuleProbe(lb LoadBalancer, rule LoadBalancerChild) (LoadBalancerChild, bool) {
	probeID := propertySubResourceID(rule.Properties, "probe")
	if probeID == "" {
		return LoadBalancerChild{}, false
	}
	for _, probe := range lb.Properties.Probes {
		if strings.EqualFold(probe.ID, probeID) {
			return probe, true
		}
	}
	return LoadBalancerChild{ID: probeID}, true
}

// azureProbePolicy: a probe every intervalInSeconds (default 15), and
// probeThreshold consecutive results — numberOfProbes on an older probe,
// default 1 — to move a backend into or out of rotation.
func azureProbePolicy(probe LoadBalancerChild) lbplane.HealthPolicy {
	interval := time.Duration(intProperty(probe.Properties, "intervalInSeconds")) * time.Second
	if interval <= 0 {
		interval = 15 * time.Second
	}
	threshold := intProperty(probe.Properties, "probeThreshold")
	if threshold <= 0 {
		threshold = intProperty(probe.Properties, "numberOfProbes")
	}
	if threshold <= 0 {
		threshold = 1
	}
	return lbplane.HealthPolicy{
		Interval:                interval,
		InitialHealthyThreshold: threshold,
		HealthyThreshold:        threshold,
		UnhealthyThreshold:      threshold,
	}
}

func azureSweepLoadBalancerProbes(ctx context.Context, now time.Time) {
	if azureLBs == nil {
		return
	}
	var targets []lbplane.HealthTarget[string]
	seen := map[string]bool{}
	for _, lb := range azureLBs.List() {
		for _, rule := range lb.Properties.LoadBalancingRules {
			probe, ok := azureLoadBalancerRuleProbe(lb, rule)
			if !ok {
				continue
			}
			policy := azureProbePolicy(probe)
			for _, target := range azureLoadBalancerTargets(lb, rule) {
				key := azureLoadBalancerHealthKey(lb, probe, target)
				if seen[key] {
					continue
				}
				seen[key] = true
				targets = append(targets, lbplane.HealthTarget[string]{
					Key:    key,
					Policy: policy,
					Probe: func(ctx context.Context) (int, error) {
						return azureProbeBackend(ctx, probe, target)
					},
				})
			}
		}
	}
	azureLoadBalancerHealth.Sweep(ctx, now, targets)
}

// azureProbeBackend: Tcp succeeds on an accepted connection; Http and Https
// succeed on a 200 alone, a redirect included marking the backend down, and do
// not validate the certificate. A probe times out after its interval or 30
// seconds, whichever is shorter.
func azureProbeBackend(ctx context.Context, probe LoadBalancerChild, target azureLBTarget) (int, error) {
	if probe.Properties == nil {
		return 0, fmt.Errorf("health probe %s does not exist", probe.ID)
	}
	port := intProperty(probe.Properties, "port")
	if port == 0 {
		port = target.Port
	}
	address := replacePort(target.Address, port)
	timeout := min(azureProbePolicy(probe).Interval, 30*time.Second)
	switch strings.ToLower(stringProperty(probe.Properties, "protocol")) {
	case "http", "https":
		return lbplane.ProbeHTTP(ctx, lbplane.HTTPProbe{
			Scheme:  strings.ToLower(stringProperty(probe.Properties, "protocol")),
			Address: address,
			Path:    stringProperty(probe.Properties, "requestPath"),
			Timeout: timeout,
			Match:   lbplane.StatusRange(http.StatusOK, http.StatusOK),
		})
	default:
		return 0, lbplane.ProbeTCP(ctx, address, timeout)
	}
}

func propertySubResourceID(props map[string]any, key string) string {
	value, ok := props[key]
	if !ok {
		return ""
	}
	if sub, ok := value.(SubResource); ok {
		return sub.ID
	}
	if child, ok := value.(LoadBalancerChild); ok {
		return child.ID
	}
	if raw, ok := value.(map[string]any); ok {
		return stringProperty(raw, "id")
	}
	return ""
}

func propertySubResourceIDs(props map[string]any, key string) []string {
	value, ok := props[key]
	if !ok {
		return nil
	}
	var ids []string
	for _, item := range anySlice(value) {
		if raw, ok := item.(map[string]any); ok {
			if id := stringProperty(raw, "id"); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func propertyObjectSlice(props map[string]any, key string) []map[string]any {
	var out []map[string]any
	for _, item := range anySlice(props[key]) {
		if raw, ok := item.(map[string]any); ok {
			if nested, ok := raw["properties"].(map[string]any); ok {
				out = append(out, nested)
			} else {
				out = append(out, raw)
			}
		}
	}
	return out
}

func anySlice(value any) []any {
	switch v := value.(type) {
	case []any:
		return v
	case []map[string]any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func intProperty(props map[string]any, key string) int {
	switch v := props[key].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := strconv.Atoi(v.String())
		return i
	default:
		return 0
	}
}

func stringProperty(props map[string]any, key string) string {
	if value, ok := props[key].(string); ok {
		return value
	}
	return ""
}

func replacePort(address string, port int) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func registerNetworkInterfaces(srv *sim.Server) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network"

	srv.HandleFunc("PUT "+armBase+"/networkInterfaces/{networkInterfaceName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "networkInterfaceName")
		var req NetworkInterface
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := armResourceID(sub, rg, "Microsoft.Network", "networkInterfaces", name)
		nic := NetworkInterface{
			ID:         id,
			Name:       name,
			Type:       "Microsoft.Network/networkInterfaces",
			Location:   req.Location,
			Tags:       req.Tags,
			Properties: req.Properties,
		}
		nic.Properties.ProvisioningState = "Succeeded"
		nic.Properties.MacAddress = formatAzureMAC(azureNICMAC(id))
		primary := azurePrimaryIPConfigIndex(nic)
		for i := range nic.Properties.IPConfigurations {
			ipcfg := &nic.Properties.IPConfigurations[i]
			if ipcfg.Name == "" {
				ipcfg.Name = fmt.Sprintf("ipconfig%d", i+1)
			}
			ipcfg.ID = id + "/ipConfigurations/" + ipcfg.Name
			ipcfg.Type = "Microsoft.Network/networkInterfaces/ipConfigurations"
			ipcfg.Properties.ProvisioningState = "Succeeded"
			if ipcfg.Properties.PrivateIPAllocationMethod == "" {
				ipcfg.Properties.PrivateIPAllocationMethod = "Dynamic"
			}
			if ipcfg.Properties.PrivateIPAddressVersion == "" {
				ipcfg.Properties.PrivateIPAddressVersion = "IPv4"
			}
			ipcfg.Properties.Primary = i == primary
			if ipcfg.Properties.Subnet == nil {
				AzureError(w, "InvalidRequestFormat", "network interface IP configuration requires a subnet reference.", http.StatusBadRequest)
				return
			}
		}
		for _, ipcfg := range nic.Properties.IPConfigurations {
			first := nic.Properties.IPConfigurations[primary]
			if !strings.EqualFold(ipcfg.Properties.Subnet.ID, first.Properties.Subnet.ID) {
				AzureErrorf(w, "IpConfigurationsOnSameNicCannotUseDifferentSubnets", http.StatusBadRequest,
					"IP configurations on same NIC %s and %s cannot use different subnets.", first.ID, ipcfg.ID)
				return
			}
		}
		var prev *NetworkInterface
		if stored, ok := azureNICs.Get(id); ok {
			prev = &stored
		}
		if err := azureLeaseNICAddresses(&nic, prev); err != nil {
			azureWriteAddressLeaseError(w, id, err)
			return
		}
		azureNICs.Put(id, nic)
		azureReconcileAutoRegisteredRecords()
		azureReapplyIngress()
		sim.WriteJSON(w, http.StatusOK, nic)
	})

	srv.HandleFunc("GET "+armBase+"/networkInterfaces/{networkInterfaceName}", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "networkInterfaces", sim.PathParam(r, "networkInterfaceName"))
		nic, ok := azureNICs.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		sim.WriteJSON(w, http.StatusOK, nic)
	})

	srv.HandleFunc("GET "+armBase+"/networkInterfaces", func(w http.ResponseWriter, r *http.Request) {
		prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "networkInterfaces") + "/"
		items := azureNICs.Filter(func(n NetworkInterface) bool { return strings.HasPrefix(n.ID, prefix) })
		if items == nil {
			items = []NetworkInterface{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": items})
	})

	srv.HandleFunc("DELETE "+armBase+"/networkInterfaces/{networkInterfaceName}", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "networkInterfaces", sim.PathParam(r, "networkInterfaceName"))
		if nic, ok := azureNICs.Get(id); ok {
			azureReleaseNICAddresses(nic)
		}
		azureNICs.Delete(id)
		w.WriteHeader(http.StatusOK)
	})
}

// Azure never echoes osProfile.adminPassword.
func stripVMAdminPassword(vm *VirtualMachine) {
	if vm.Properties.OSProfile != nil {
		delete(vm.Properties.OSProfile, "adminPassword")
	}
}

func registerVirtualMachines(srv *sim.Server) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute"

	logger := srv.Logger()

	srv.HandleFunc("PUT "+armBase+"/virtualMachines/{vmName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "vmName")
		var req VirtualMachine
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := armResourceID(sub, rg, "Microsoft.Compute", "virtualMachines", name)
		vm := VirtualMachine{
			ID:         id,
			Name:       name,
			Type:       "Microsoft.Compute/virtualMachines",
			Location:   req.Location,
			Tags:       req.Tags,
			Properties: req.Properties,
		}
		stripVMAdminPassword(&vm)
		if vm.Location == "" {
			AzureError(w, "LocationRequired", "The location property is required for this definition.", http.StatusBadRequest)
			return
		}
		// Azure validates the request before provisioning: a networkProfile, image or
		// size the Compute resource provider cannot accept is a 400/404, not a failed
		// operation.
		if fault := azureValidateVMRequest(r.Context(), vm); fault != nil {
			AzureError(w, fault.code, fault.message, fault.status)
			return
		}
		prev, _ := azureVMs.Get(id)
		identity, identityClientID, err := applyManagedIdentity(req.Identity, prev.Identity, prev.SystemIdentityClientID)
		if err != nil {
			AzureError(w, "InvalidParameter", err.Error(), http.StatusBadRequest)
			return
		}
		vm.Identity, vm.SystemIdentityClientID = identity, identityClientID
		created, busy := false, false
		azureVMs.Upsert(id, func(stored *VirtualMachine) {
			if stored.ID != "" && azureVMOperationRunning(stored.Properties.ProvisioningState) {
				busy = true
				return
			}
			created = stored.ID == ""
			if vm.Properties.VMID == "" {
				vm.Properties.VMID = stored.Properties.VMID
			}
			if vm.Properties.VMID == "" {
				vm.Properties.VMID = sim.NewUUID()
			}
			vm.Properties.ProvisioningState = "Updating"
			if created {
				vm.Properties.ProvisioningState = "Creating"
			}
			*stored = vm
		})
		if busy {
			azureVMOperationConflict(w, "CreateOrUpdate", id)
			return
		}
		syncManagedIdentityPrincipal(prev.Identity, vm.Identity, vm.SystemIdentityClientID, vm.Name)
		priorState, _ := azureVMStates.Get(id)
		if created {
			priorState = "PowerState/deallocated"
		}
		azureVMStates.Put(id, "PowerState/starting")
		for _, nicRef := range vm.Properties.NetworkProfile.NetworkInterfaces {
			azureNICs.Update(nicRef.ID, func(nic *NetworkInterface) {
				nic.Properties.VirtualMachine = &SubResource{ID: id}
			})
		}
		azureReconcileAutoRegisteredRecords()
		opID := azureRunVMOperation(id, func(ctx context.Context) *AsyncOperationError {
			// The boot leases a dynamic address, which the registered record names.
			defer azureReconcileAutoRegisteredRecords()
			if err := azureBootVM(ctx, vm); err != nil {
				logger.Error().
					Err(err).
					Str("subscription", sub).
					Str("resource_group", rg).
					Str("vm", name).
					Msg("failed to boot the virtual machine")
				azureVMStates.Put(id, priorState)
				return azureVMBootFailure(id, err)
			}
			azureVMStates.Put(id, "PowerState/running")
			return nil
		})
		asyncOperation, _ := computeOperationURLs(r, sub, vm.Location, opID)
		w.Header().Set("Azure-AsyncOperation", asyncOperation)
		setAzureAsyncOperationRetryAfter(w, opID)
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		stored, _ := azureVMs.Get(id)
		sim.WriteJSON(w, status, virtualMachineWithInstanceView(stored))
	})

	// VirtualMachines_Update touches only the blocks present, so a tags-only PATCH
	// — from the portal, `az vm update --set tags…` or the azurerm provider —
	// leaves the profiles alone. Tags replace wholesale, as in Azure.
	srv.HandleFunc("PATCH "+armBase+"/virtualMachines/{vmName}", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Compute", "virtualMachines", sim.PathParam(r, "vmName"))
		var patch struct {
			Tags       map[string]string `json:"tags"`
			Identity   *SiteIdentity     `json:"identity"`
			Properties *struct {
				HardwareProfile map[string]any    `json:"hardwareProfile"`
				StorageProfile  map[string]any    `json:"storageProfile"`
				OSProfile       map[string]any    `json:"osProfile"`
				NetworkProfile  *VMNetworkProfile `json:"networkProfile"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &patch); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		prev, ok := azureVMs.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		identity, identityClientID, err := applyManagedIdentityPatch(patch.Identity, prev.Identity, prev.SystemIdentityClientID)
		if err != nil {
			AzureError(w, "InvalidParameter", err.Error(), http.StatusBadRequest)
			return
		}
		if !azureVMs.Update(id, func(vm *VirtualMachine) {
			if patch.Tags != nil {
				vm.Tags = patch.Tags
			}
			vm.Identity, vm.SystemIdentityClientID = identity, identityClientID
			if patch.Properties == nil {
				return
			}
			if patch.Properties.HardwareProfile != nil {
				vm.Properties.HardwareProfile = patch.Properties.HardwareProfile
			}
			if patch.Properties.StorageProfile != nil {
				vm.Properties.StorageProfile = patch.Properties.StorageProfile
			}
			if patch.Properties.OSProfile != nil {
				vm.Properties.OSProfile = patch.Properties.OSProfile
			}
			if patch.Properties.NetworkProfile != nil {
				vm.Properties.NetworkProfile = *patch.Properties.NetworkProfile
			}
			stripVMAdminPassword(vm)
		}) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		syncManagedIdentityPrincipal(prev.Identity, identity, identityClientID, prev.Name)
		azureReconcileAutoRegisteredRecords()
		vm, _ := azureVMs.Get(id)
		sim.WriteJSON(w, http.StatusOK, vm)
	})

	srv.HandleFunc("GET "+armBase+"/virtualMachines/{vmName}", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Compute", "virtualMachines", sim.PathParam(r, "vmName"))
		vm, ok := azureVMs.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		if strings.EqualFold(r.URL.Query().Get("$expand"), "instanceView") {
			vm = virtualMachineWithInstanceView(vm)
		}
		sim.WriteJSON(w, http.StatusOK, vm)
	})

	srv.HandleFunc("GET "+armBase+"/virtualMachines/{vmName}/instanceView", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Compute", "virtualMachines", sim.PathParam(r, "vmName"))
		vm, ok := azureVMs.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		sim.WriteJSON(w, http.StatusOK, virtualMachineWithInstanceView(vm).Properties.InstanceView)
	})

	srv.HandleFunc("GET "+armBase+"/virtualMachines", func(w http.ResponseWriter, r *http.Request) {
		prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Compute", "virtualMachines") + "/"
		items := azureVMs.Filter(func(vm VirtualMachine) bool { return strings.HasPrefix(vm.ID, prefix) })
		if items == nil {
			items = []VirtualMachine{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": items})
	})

	// VirtualMachines_ListAll pages over a resource-ID ordering, so the offset
	// continuation token addresses the same machine on every request.
	// `statusOnly=true` asks for the instance view.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/virtualMachines", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
		all := azureVMs.Filter(func(vm VirtualMachine) bool { return strings.HasPrefix(vm.ID, prefix) })
		statusOnly := strings.EqualFold(r.URL.Query().Get("statusOnly"), "true")
		for i := range all {
			if statusOnly {
				all[i] = virtualMachineWithInstanceView(all[i])
			}
		}
		page, next, pageOK := armPage(w, r, all, func(vm VirtualMachine) string { return vm.ID })
		if !pageOK {
			return
		}
		out := map[string]any{"value": page}
		if next != "" {
			out["nextLink"] = armNextLink(r, next)
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})

	srv.HandleFunc("DELETE "+armBase+"/virtualMachines/{vmName}", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Compute", "virtualMachines", sim.PathParam(r, "vmName"))
		vm, ok := azureVMs.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		if !azureClaimVMOperationAs(id, "Deleting", "") {
			azureVMOperationConflict(w, "Delete", id)
			return
		}
		opID := azureRunVMOperation(id, func(ctx context.Context) *AsyncOperationError {
			if err := azureDestroyVM(ctx, vm); err != nil {
				logger.Error().Err(err).Str("vm", id).Msg("failed to delete the virtual machine")
				return azureVMHaltFailure(id, err)
			}
			azureForgetVM(id)
			return nil
		})
		writeAzureVMOperationAccepted(w, r, vm, opID)
	})

	// Each action names the power state the machine passes through and the one it
	// settles in; a boot follows the halt only for start and restart.
	for _, action := range []struct {
		name, transition, settled string
		halt, boot                bool
	}{
		{name: "start", transition: "PowerState/starting", settled: "PowerState/running", boot: true},
		{name: "powerOff", transition: "PowerState/stopping", settled: "PowerState/stopped", halt: true},
		{name: "deallocate", transition: "PowerState/deallocating", settled: "PowerState/deallocated", halt: true},
		{name: "restart", transition: "PowerState/stopping", settled: "PowerState/running", halt: true, boot: true},
	} {
		srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/"+action.name, func(w http.ResponseWriter, r *http.Request) {
			vm, id, ok := azureLookupVM(w, r)
			if !ok {
				return
			}
			priorState, _ := azureVMStates.Get(id)
			if !azureClaimVMOperation(id, action.transition) {
				azureVMOperationConflict(w, action.name, id)
				return
			}
			opID := azureRunVMOperation(id, func(ctx context.Context) *AsyncOperationError {
				if action.halt {
					if err := azureHaltVM(ctx, id); err != nil {
						azureVMStates.Put(id, priorState)
						return azureVMHaltFailure(id, err)
					}
				}
				if action.boot {
					if action.halt {
						azureVMStates.Put(id, "PowerState/starting")
					}
					if err := azureBootVM(ctx, vm); err != nil {
						logger.Error().Err(err).Str("vm", id).Str("action", action.name).
							Msg("failed to boot the virtual machine")
						if action.halt {
							priorState = "PowerState/stopped"
						}
						azureVMStates.Put(id, priorState)
						return azureVMBootFailure(id, err)
					}
				}
				azureVMStates.Put(id, action.settled)
				return nil
			})
			writeAzureVMOperationAccepted(w, r, vm, opID)
		})
	}
}

func virtualMachineWithInstanceView(vm VirtualMachine) VirtualMachine {
	state, ok := azureVMStates.Get(vm.ID)
	if !ok {
		state = "PowerState/running"
	}
	display := strings.TrimPrefix(state, "PowerState/")
	statuses := []VMStatus{
		azureVMProvisioningStatus(vm),
		{Code: state, Level: "Info", DisplayStatus: "VM " + display},
	}
	if generalized, _ := azureVMGeneralized.Get(vm.ID); generalized {
		statuses = append(statuses, VMStatus{
			Code: "OSState/generalized", Level: "Info", DisplayStatus: "VM generalized",
		})
	}
	vm.Properties.InstanceView = &VMInstanceView{Statuses: statuses}
	return vm
}
