package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The load balancer category of the Instance Metadata Service, from
// "Retrieve load balancer metadata using Azure Instance Metadata Service"
// (specs/cloud-api/azure/imds-load-balancer-metadata.md.gz): what the Standard
// load balancers whose backend pools hold the machine's interface direct to
// it.

type azureIMDSLoadBalancerAddress struct {
	FrontendIPAddress string `json:"frontendIpAddress"`
	PrivateIPAddress  string `json:"privateIpAddress"`
}

type azureIMDSLoadBalancerInboundRule struct {
	FrontendIPAddress string `json:"frontendIpAddress"`
	Protocol          string `json:"protocol"`
	FrontendPort      int    `json:"frontendPort"`
	BackendPort       int    `json:"backendPort"`
	PrivateIPAddress  string `json:"privateIpAddress"`
}

type azureIMDSLoadBalancerMetadata struct {
	PublicIPAddresses []azureIMDSLoadBalancerAddress     `json:"publicIpAddresses"`
	InboundRules      []azureIMDSLoadBalancerInboundRule `json:"inboundRules"`
	OutboundRules     []azureIMDSLoadBalancerAddress     `json:"outboundRules"`
}

func handleAzureIMDSLoadBalancer(w http.ResponseWriter, r *http.Request) {
	if !mustMetadataHeader(w, r) {
		return
	}
	if _, ok := azureIMDSQuery(w, r, azureIMDSLoadBalancerVersions); !ok {
		return
	}
	format, ok := azureIMDSFormat(w, r)
	if !ok {
		return
	}
	vm, ok := azureIMDSRequest(w, r)
	if !ok {
		return
	}
	azureIMDSServeFiltered(w, r.PathValue("route"), format, map[string]any{
		"loadbalancer": azureIMDSLoadBalancerOf(vm.NIC),
	})
}

// azureIMDSLoadBalancerOf lists, for each of the interface's IP
// configurations in a Standard load balancer's backend pool, its instance
// public address and the load-balancing rules that send traffic to it. The
// simulator's load balancers keep no outbound rules, so none are listed.
func azureIMDSLoadBalancerOf(nic NetworkInterface) azureIMDSLoadBalancerMetadata {
	out := azureIMDSLoadBalancerMetadata{
		PublicIPAddresses: []azureIMDSLoadBalancerAddress{},
		InboundRules:      []azureIMDSLoadBalancerInboundRule{},
		OutboundRules:     []azureIMDSLoadBalancerAddress{},
	}
	for _, ipcfg := range azureIMDSIPConfigurations(nic) {
		private := ipcfg.Properties.PrivateIPAddress
		behindStandard := false
		for _, pool := range ipcfg.Properties.LoadBalancerBackendAddressPools {
			lb, ok := azureLoadBalancerOfPool(pool.ID)
			if !ok || lb.Sku == nil || !strings.EqualFold(lb.Sku.Name, "Standard") {
				continue
			}
			behindStandard = true
			for _, rule := range lb.Properties.LoadBalancingRules {
				if !azureLoadBalancingRuleTargetsPool(rule, pool.ID) {
					continue
				}
				frontend, ok := azureLoadBalancerFrontendAddress(lb, propertySubResourceID(rule.Properties, "frontendIPConfiguration"))
				if !ok {
					continue
				}
				backendPort := intProperty(rule.Properties, "backendPort")
				if backendPort == 0 {
					backendPort = intProperty(rule.Properties, "frontendPort")
				}
				out.InboundRules = append(out.InboundRules, azureIMDSLoadBalancerInboundRule{
					FrontendIPAddress: frontend,
					Protocol:          strings.ToLower(stringProperty(rule.Properties, "protocol")),
					FrontendPort:      intProperty(rule.Properties, "frontendPort"),
					BackendPort:       backendPort,
					PrivateIPAddress:  private,
				})
			}
		}
		if !behindStandard {
			continue
		}
		if public := azureIPConfigPublicAddress(ipcfg); public != "" {
			out.PublicIPAddresses = append(out.PublicIPAddresses, azureIMDSLoadBalancerAddress{
				FrontendIPAddress: public,
				PrivateIPAddress:  private,
			})
		}
	}
	return out
}

func azureLoadBalancerOfPool(poolID string) (LoadBalancer, bool) {
	at := sim.CaseInsensitiveIndex(poolID, "/backendAddressPools/")
	if at < 0 || azureLBs == nil {
		return LoadBalancer{}, false
	}
	return azureLBs.Get(poolID[:at])
}

func azureLoadBalancingRuleTargetsPool(rule LoadBalancerChild, poolID string) bool {
	pools := propertySubResourceIDs(rule.Properties, "backendAddressPools")
	if single := propertySubResourceID(rule.Properties, "backendAddressPool"); single != "" {
		pools = append(pools, single)
	}
	for _, candidate := range pools {
		if strings.EqualFold(candidate, poolID) {
			return true
		}
	}
	return false
}

// azureLoadBalancerFrontendAddress is the address a frontend IP configuration
// receives on: its public IP address's, or its private address.
func azureLoadBalancerFrontendAddress(lb LoadBalancer, frontendID string) (string, bool) {
	for _, frontend := range lb.Properties.FrontendIPConfigurations {
		if !strings.EqualFold(frontend.ID, frontendID) {
			continue
		}
		if pipID := propertySubResourceID(frontend.Properties, "publicIPAddress"); pipID != "" {
			pip, ok := azurePublicIPs.Get(pipID)
			if !ok || pip.Properties.PublicIPAddress == "" {
				return "", false
			}
			return pip.Properties.PublicIPAddress, true
		}
		if private := stringProperty(frontend.Properties, "privateIPAddress"); private != "" {
			return private, true
		}
		return "", false
	}
	return "", false
}
