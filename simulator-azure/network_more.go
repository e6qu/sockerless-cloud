package main

import (
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// registerNetworkMoreOps adds the remaining Microsoft.Network operations that
// cross-reference load balancers, network interfaces, public IP addresses and
// virtual networks: the LB/NIC association lists, the load-balancer VIP swap,
// inbound-NAT port-mapping query, per-rule health, NIC/IP-based migration, the
// public-IP DDoS-protection and cloud-service reservation operations, and the
// subnet resource/service-association link reads plus the virtual-network DDoS
// protection status.
func registerNetworkMoreOps(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	subBase := azureNetworkSubBase()

	lbID := func(r *http.Request) string {
		return azureLoadBalancerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "loadBalancerName"))
	}
	nicID := func(r *http.Request) string {
		return "/subscriptions/" + sim.PathParam(r, "subscriptionId") +
			"/resourceGroups/" + sim.PathParam(r, "resourceGroupName") +
			"/providers/Microsoft.Network/networkInterfaces/" + sim.PathParam(r, "networkInterfaceName")
	}

	// LoadBalancerNetworkInterfaces_List — the network interfaces whose IP
	// configurations reference one of the load balancer's backend pools.
	srv.HandleFunc("GET "+armBase+"/loadBalancers/{loadBalancerName}/networkInterfaces", func(w http.ResponseWriter, r *http.Request) {
		lb, ok := azureLBs.Get(lbID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Load balancer %q not found.", sim.PathParam(r, "loadBalancerName"))
			return
		}
		poolPrefix := lb.ID + "/backendAddressPools/"
		var nics []NetworkInterface
		for _, nic := range azureNICs.List() {
			if networkInterfaceReferencesPoolPrefix(nic, poolPrefix) {
				nics = append(nics, nic)
			}
		}
		azureWriteList(w, nics)
	})

	// NetworkInterfaceLoadBalancers_List — the load balancers whose backend
	// pools are referenced by the network interface's IP configurations.
	srv.HandleFunc("GET "+armBase+"/networkInterfaces/{networkInterfaceName}/loadBalancers", func(w http.ResponseWriter, r *http.Request) {
		nic, ok := azureNICs.Get(nicID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Network interface %q not found.", sim.PathParam(r, "networkInterfaceName"))
			return
		}
		var lbs []LoadBalancer
		for _, lb := range azureLBs.List() {
			if networkInterfaceReferencesPoolPrefix(nic, lb.ID+"/backendAddressPools/") {
				lbs = append(lbs, lb)
			}
		}
		azureWriteList(w, lbs)
	})

	// LoadBalancers_SwapPublicIpAddresses — reassign the public IP of each
	// referenced frontend IP configuration. Long-running in Azure; the sim
	// applies the swap synchronously and returns the terminal 200.
	srv.HandleFunc("POST "+subBase+"/locations/{location}/setLoadBalancerFrontendPublicIpAddresses", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FrontendIPConfigurations []struct {
				ID         string `json:"id"`
				Properties struct {
					PublicIPAddress *SubResource `json:"publicIPAddress"`
				} `json:"properties"`
			} `json:"frontendIPConfigurations"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		for _, fe := range req.FrontendIPConfigurations {
			id := loadBalancerIDFromChildID(fe.ID, "/frontendIPConfigurations/")
			if id == "" {
				continue
			}
			pip := ""
			if fe.Properties.PublicIPAddress != nil {
				pip = fe.Properties.PublicIPAddress.ID
			}
			azureLBs.Update(id, func(lb *LoadBalancer) {
				for i := range lb.Properties.FrontendIPConfigurations {
					if strings.EqualFold(lb.Properties.FrontendIPConfigurations[i].ID, fe.ID) {
						if lb.Properties.FrontendIPConfigurations[i].Properties == nil {
							lb.Properties.FrontendIPConfigurations[i].Properties = map[string]any{}
						}
						lb.Properties.FrontendIPConfigurations[i].Properties["publicIPAddress"] = map[string]any{"id": pip}
					}
				}
			})
		}
		w.WriteHeader(http.StatusOK)
	})

	// LoadBalancers_ListInboundNatRulePortMappings — the per-rule port
	// mappings for the inbound NAT rules created against a backend pool.
	srv.HandleFunc("POST "+armBase+"/loadBalancers/{loadBalancerName}/backendAddressPools/{backendPoolName}/queryInboundNatRulePortMapping", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IPAddress string `json:"ipAddress"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		rulePrefix := lbID(r) + "/inboundNatRules/"
		type portMapping struct {
			InboundNatRuleName string `json:"inboundNatRuleName"`
			Protocol           string `json:"protocol,omitempty"`
			FrontendPort       int    `json:"frontendPort,omitempty"`
			BackendPort        int    `json:"backendPort,omitempty"`
		}
		var mappings []portMapping
		for _, rule := range azureLBInboundNatRules.Filter(func(c LoadBalancerChild) bool { return strings.HasPrefix(c.ID, rulePrefix) }) {
			mappings = append(mappings, portMapping{
				InboundNatRuleName: rule.Name,
				Protocol:           stringProperty(rule.Properties, "protocol"),
				FrontendPort:       intProperty(rule.Properties, "frontendPort"),
				BackendPort:        intProperty(rule.Properties, "backendPort"),
			})
		}
		if mappings == nil {
			mappings = []portMapping{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"inboundNatRulePortMappings": mappings})
	})

	// LoadBalancerLoadBalancingRules_Health — live health of the backend
	// instances behind a load-balancing rule, computed by probing each
	// target exactly as the data-plane proxy does.
	srv.HandleFunc("POST "+armBase+"/loadBalancers/{loadBalancerName}/loadBalancingRules/{loadBalancingRuleName}/health", func(w http.ResponseWriter, r *http.Request) {
		lb, ok := azureLBs.Get(lbID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Load balancer %q not found.", sim.PathParam(r, "loadBalancerName"))
			return
		}
		ruleName := sim.PathParam(r, "loadBalancingRuleName")
		var rule LoadBalancerChild
		found := false
		for _, candidate := range lb.Properties.LoadBalancingRules {
			if strings.EqualFold(candidate.Name, ruleName) {
				rule = candidate
				found = true
				break
			}
		}
		if !found {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Load balancing rule %q not found.", ruleName)
			return
		}
		type addressHealth struct {
			IPAddress string `json:"ipAddress,omitempty"`
			State     string `json:"state"`
		}
		up, down := 0, 0
		addresses := []addressHealth{}
		for _, target := range azureLoadBalancerTargets(lb, rule) {
			ip, _, _ := strings.Cut(target.Address, ":")
			if azureProbeLoadBalancerTarget(r.Context(), lb, rule, target) {
				up++
				addresses = append(addresses, addressHealth{IPAddress: ip, State: "Up"})
			} else {
				down++
				addresses = append(addresses, addressHealth{IPAddress: ip, State: "Down"})
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"up":                           up,
			"down":                         down,
			"loadBalancerBackendAddresses": addresses,
		})
	})

	// LoadBalancers_MigrateToIpBased — migrate the named NIC-based backend
	// pools to IP-based. The sim echoes the pools that exist on the LB.
	srv.HandleFunc("POST "+armBase+"/loadBalancers/{loadBalancerName}/migrateToIpBased", func(w http.ResponseWriter, r *http.Request) {
		lb, ok := azureLBs.Get(lbID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Load balancer %q not found.", sim.PathParam(r, "loadBalancerName"))
			return
		}
		var req struct {
			Pools []string `json:"pools"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		want := req.Pools
		if len(want) == 0 {
			for _, pool := range lb.Properties.BackendAddressPools {
				want = append(want, pool.Name)
			}
		}
		migrated := []string{}
		for _, name := range want {
			for _, pool := range lb.Properties.BackendAddressPools {
				if strings.EqualFold(pool.Name, name) {
					migrated = append(migrated, pool.Name)
					break
				}
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"migratedPools": migrated})
	})

	// PublicIPAddresses_DdosProtectionStatus — the DDoS protection status of
	// a public IP. The sim does not model DDoS protection plans, so an IP is
	// reported as not workload-protected.
	srv.HandleFunc("POST "+armBase+"/publicIPAddresses/{publicIPName}/ddosProtectionStatus", func(w http.ResponseWriter, r *http.Request) {
		id := "/subscriptions/" + sim.PathParam(r, "subscriptionId") +
			"/resourceGroups/" + sim.PathParam(r, "resourceGroupName") +
			"/providers/Microsoft.Network/publicIPAddresses/" + sim.PathParam(r, "publicIPName")
		pip, ok := azurePublicIPs.Get(id)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Public IP address %q not found.", sim.PathParam(r, "publicIPName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"publicIpAddressId":   pip.ID,
			"publicIpAddress":     pip.Properties.PublicIPAddress,
			"isWorkloadProtected": "False",
		})
	})

	// PublicIPAddresses_ReserveCloudServicePublicIpAddress — reserve the
	// public IP for a cloud service. The sim returns the public IP resource.
	srv.HandleFunc("POST "+armBase+"/publicIPAddresses/{publicIPName}/reserveCloudServicePublicIpAddress", func(w http.ResponseWriter, r *http.Request) {
		id := "/subscriptions/" + sim.PathParam(r, "subscriptionId") +
			"/resourceGroups/" + sim.PathParam(r, "resourceGroupName") +
			"/providers/Microsoft.Network/publicIPAddresses/" + sim.PathParam(r, "publicIPName")
		var body map[string]any
		_ = sim.ReadJSON(r, &body)
		pip, ok := azurePublicIPs.Get(id)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Public IP address %q not found.", sim.PathParam(r, "publicIPName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, pip)
	})

	// PublicIPAddresses_DisassociateCloudServiceReservedPublicIp — release a
	// cloud-service reservation. No resource body in the terminal response.
	srv.HandleFunc("POST "+armBase+"/publicIPAddresses/{publicIPName}/disassociateCloudServiceReservedPublicIp", func(w http.ResponseWriter, r *http.Request) {
		id := "/subscriptions/" + sim.PathParam(r, "subscriptionId") +
			"/resourceGroups/" + sim.PathParam(r, "resourceGroupName") +
			"/providers/Microsoft.Network/publicIPAddresses/" + sim.PathParam(r, "publicIPName")
		var body map[string]any
		_ = sim.ReadJSON(r, &body)
		if _, ok := azurePublicIPs.Get(id); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Public IP address %q not found.", sim.PathParam(r, "publicIPName"))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// ResourceNavigationLinks_List / ServiceAssociationLinks_List — the links
	// other Azure services create when they delegate the subnet. A subnet with
	// no delegated services carries no links, but the subnet has to exist for
	// the question to have an answer at all: these read a child collection of a
	// subnet, and a collection under a subnet that was never created is a
	// ResourceNotFound, not an empty list.
	linkList := func(w http.ResponseWriter, r *http.Request) {
		if !azureSubnetExists(r) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'subnets/%s' under virtualNetworks '%s' was not found.",
				sim.PathParam(r, "subnetName"), sim.PathParam(r, "vnetName"))
			return
		}
		azureWriteList(w, []any{})
	}
	srv.HandleFunc("GET "+armBase+"/virtualNetworks/{vnetName}/subnets/{subnetName}/ResourceNavigationLinks", linkList)
	srv.HandleFunc("GET "+armBase+"/virtualNetworks/{vnetName}/subnets/{subnetName}/ServiceAssociationLinks", linkList)

	// VirtualNetworks_ListDdosProtectionStatus — the DDoS protection status of
	// every public IP attached (through a NIC) to a subnet of the vnet.
	srv.HandleFunc("POST "+armBase+"/virtualNetworks/{vnetName}/ddosProtectionStatus", func(w http.ResponseWriter, r *http.Request) {
		vnetID := "/subscriptions/" + sim.PathParam(r, "subscriptionId") +
			"/resourceGroups/" + sim.PathParam(r, "resourceGroupName") +
			"/providers/Microsoft.Network/virtualNetworks/" + sim.PathParam(r, "vnetName")
		subnetPrefix := vnetID + "/subnets/"
		results := []map[string]any{}
		for _, nic := range azureNICs.List() {
			for _, ipcfg := range nic.Properties.IPConfigurations {
				if ipcfg.Properties.Subnet == nil || !strings.HasPrefix(ipcfg.Properties.Subnet.ID, subnetPrefix) {
					continue
				}
				if ipcfg.Properties.PublicIPAddress == nil {
					continue
				}
				pip, ok := azurePublicIPs.Get(ipcfg.Properties.PublicIPAddress.ID)
				if !ok {
					continue
				}
				results = append(results, map[string]any{
					"publicIpAddressId":   pip.ID,
					"publicIpAddress":     pip.Properties.PublicIPAddress,
					"isWorkloadProtected": "False",
				})
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": results})
	})
}

// networkInterfaceReferencesPoolPrefix reports whether any of the network
// interface's IP configurations references a backend address pool whose ID
// starts with the given prefix (a load balancer's backendAddressPools path).
func networkInterfaceReferencesPoolPrefix(nic NetworkInterface, poolPrefix string) bool {
	for _, ipcfg := range nic.Properties.IPConfigurations {
		for _, pool := range ipcfg.Properties.LoadBalancerBackendAddressPools {
			if strings.HasPrefix(pool.ID, poolPrefix) {
				return true
			}
		}
	}
	return false
}

// loadBalancerIDFromChildID returns the load balancer resource ID from a child
// resource ID (e.g. a frontend IP configuration ID) by trimming at the marker.
func loadBalancerIDFromChildID(childID, marker string) string {
	if i := strings.Index(childID, marker); i >= 0 {
		return childID[:i]
	}
	return ""
}

// azureSubnetExists reports whether the subnet a request addresses through the
// {subscriptionId}/{resourceGroupName}/{vnetName}/{subnetName} path parameters
// is one the simulator holds. Child collections under a subnet answer for the
// subnet's own contents, so an absent subnet is a ResourceNotFound rather than
// an empty collection.
func azureSubnetExists(r *http.Request) bool {
	if azureSubnets == nil {
		return false
	}
	id := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/%s/subnets/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
		sim.PathParam(r, "vnetName"), sim.PathParam(r, "subnetName"))
	_, ok := azureSubnets.Get(id)
	return ok
}
