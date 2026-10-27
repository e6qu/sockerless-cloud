package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// registerNetworkMoreOps adds the operations that cross-reference load
// balancers, interfaces, public IPs and virtual networks: association lists,
// VIP swap, inbound NAT port mappings, per-rule health, IP-based migration,
// DDoS status, cloud-service reservations, and subnet link reads.
func registerNetworkMoreOps(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	subBase := azureNetworkSubBase()

	lbID := func(r *http.Request) string {
		return azureLoadBalancerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "loadBalancerName"))
	}
	nicID := func(r *http.Request) string {
		return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "networkInterfaces", sim.PathParam(r, "networkInterfaceName"))
	}

	// LoadBalancerNetworkInterfaces_List: interfaces whose IP configurations
	// reference one of the load balancer's backend pools.
	srv.HandleFunc("GET "+armBase+"/loadBalancers/{loadBalancerName}/networkInterfaces", func(w http.ResponseWriter, r *http.Request) {
		lb, ok := azureLBs.Get(lbID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Load balancer %q not found.", sim.PathParam(r, "loadBalancerName"))
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

	// NetworkInterfaceLoadBalancers_List: load balancers whose backend pools the
	// interface's IP configurations reference.
	srv.HandleFunc("GET "+armBase+"/networkInterfaces/{networkInterfaceName}/loadBalancers", func(w http.ResponseWriter, r *http.Request) {
		nic, ok := azureNICs.Get(nicID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Network interface %q not found.", sim.PathParam(r, "networkInterfaceName"))
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

	// LoadBalancers_SwapPublicIpAddresses is long-running in Azure; the swap
	// applies synchronously and answers the terminal 200.
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
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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

	srv.HandleFunc("POST "+armBase+"/loadBalancers/{loadBalancerName}/backendAddressPools/{backendPoolName}/queryInboundNatRulePortMapping", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IPAddress string `json:"ipAddress"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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

	// LoadBalancerLoadBalancingRules_Health reports what the rule's health probe
	// last recorded, which the data plane routes by.
	srv.HandleFunc("POST "+armBase+"/loadBalancers/{loadBalancerName}/loadBalancingRules/{loadBalancingRuleName}/health", func(w http.ResponseWriter, r *http.Request) {
		lb, ok := azureLBs.Get(lbID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Load balancer %q not found.", sim.PathParam(r, "loadBalancerName"))
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Load balancing rule %q not found.", ruleName)
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
			if azureLoadBalancerTargetInRotation(lb, rule, target) {
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

	// LoadBalancers_MigrateToIpBased echoes the named pools that exist.
	srv.HandleFunc("POST "+armBase+"/loadBalancers/{loadBalancerName}/migrateToIpBased", func(w http.ResponseWriter, r *http.Request) {
		lb, ok := azureLBs.Get(lbID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Load balancer %q not found.", sim.PathParam(r, "loadBalancerName"))
			return
		}
		var req struct {
			Pools []string `json:"pools"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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

	// The simulator models no DDoS protection plans, so no IP is
	// workload-protected.
	srv.HandleFunc("POST "+armBase+"/publicIPAddresses/{publicIPName}/ddosProtectionStatus", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "publicIPAddresses", sim.PathParam(r, "publicIPName"))
		pip, ok := azurePublicIPs.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Public IP address %q not found.", sim.PathParam(r, "publicIPName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"publicIpAddressId":   pip.ID,
			"publicIpAddress":     pip.Properties.PublicIPAddress,
			"isWorkloadProtected": "False",
		})
	})

	// Answers the public IP resource.
	srv.HandleFunc("POST "+armBase+"/publicIPAddresses/{publicIPName}/reserveCloudServicePublicIpAddress", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "publicIPAddresses", sim.PathParam(r, "publicIPName"))
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		pip, ok := azurePublicIPs.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Public IP address %q not found.", sim.PathParam(r, "publicIPName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, pip)
	})

	// No resource body in the terminal response.
	srv.HandleFunc("POST "+armBase+"/publicIPAddresses/{publicIPName}/disassociateCloudServiceReservedPublicIp", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "publicIPAddresses", sim.PathParam(r, "publicIPName"))
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if _, ok := azurePublicIPs.Get(id); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Public IP address %q not found.", sim.PathParam(r, "publicIPName"))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// ResourceNavigationLinks_List and ServiceAssociationLinks_List: a subnet with
	// no delegations has no links, but an absent subnet is ResourceNotFound, not
	// an empty list.
	linkList := func(w http.ResponseWriter, r *http.Request) {
		if !azureSubnetExists(r) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'subnets/%s' under virtualNetworks '%s' was not found.",
				sim.PathParam(r, "subnetName"), sim.PathParam(r, "vnetName"))
			return
		}
		azureWriteList(w, []any{})
	}
	srv.HandleFunc("GET "+armBase+"/virtualNetworks/{vnetName}/subnets/{subnetName}/ResourceNavigationLinks", linkList)
	srv.HandleFunc("GET "+armBase+"/virtualNetworks/{vnetName}/subnets/{subnetName}/ServiceAssociationLinks", linkList)

	// VirtualNetworks_ListDdosProtectionStatus covers every public IP attached
	// through an interface to a subnet of the virtual network. Its final state
	// comes via Location, so it answers 202 and the poll carries the list.
	srv.HandleFunc("POST "+armBase+"/virtualNetworks/{vnetName}/ddosProtectionStatus", func(w http.ResponseWriter, r *http.Request) {
		vnetID := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "virtualNetworks", sim.PathParam(r, "vnetName"))
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
		// The SDK builds its pager from the polled result and drops the one it
		// constructed, so a list here would never be read.
		vnet, ok := azureVnets.Get(vnetID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Network/virtualNetworks/%s' under resource group '%s' was not found.",
				sim.PathParam(r, "vnetName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		payload, err := json.Marshal(map[string]any{"value": results})
		if err != nil {
			AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
				"The DDoS protection status could not be rendered: %v", err)
			return
		}
		opID := issueAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
			return payload, nil
		})
		w.Header().Set("Location", azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"),
			"Microsoft.Network", vnet.Location, "operationResults", opID, r.URL.Query().Get("api-version")))
		setAzureAsyncOperationRetryAfter(w, opID)
		w.WriteHeader(http.StatusAccepted)
	})
}

// The prefix is a load balancer's backendAddressPools path.
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

func loadBalancerIDFromChildID(childID, marker string) string {
	if i := strings.Index(childID, marker); i >= 0 {
		return childID[:i]
	}
	return ""
}

// Child collections answer for the subnet's own contents, so an absent subnet
// is ResourceNotFound rather than an empty collection.
func azureSubnetExists(r *http.Request) bool {
	if azureSubnets == nil {
		return false
	}
	id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "virtualNetworks", sim.PathParam(r, "vnetName"), "subnets", sim.PathParam(r, "subnetName"))
	_, ok := azureSubnets.Get(id)
	return ok
}
