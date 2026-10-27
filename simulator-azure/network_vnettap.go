package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A virtual network tap names the collector — an interface IP configuration or
// a load balancer frontend — that mirrored traffic goes to. Traffic flows once
// an interface's tap configuration references it, and the read-only
// networkInterfaceTapConfigurations resolves from those references.

type VirtualNetworkTap struct {
	azureNetworkResourceHeader
	Properties VirtualNetworkTapProperties `json:"properties"`
}

type VirtualNetworkTapProperties struct {
	NetworkInterfaceTapConfigurations              []SubResource `json:"networkInterfaceTapConfigurations,omitempty"`
	ResourceGUID                                   string        `json:"resourceGuid,omitempty"`
	ProvisioningState                              string        `json:"provisioningState,omitempty"`
	DestinationNetworkInterfaceIPConfiguration     *SubResource  `json:"destinationNetworkInterfaceIPConfiguration,omitempty"`
	DestinationLoadBalancerFrontEndIPConfiguration *SubResource  `json:"destinationLoadBalancerFrontEndIPConfiguration,omitempty"`
	DestinationPort                                int           `json:"destinationPort,omitempty"`
}

var azureVirtualNetworkTaps sim.Store[VirtualNetworkTap]

func registerNetworkVirtualNetworkTaps(srv *sim.Server) {
	azureVirtualNetworkTaps = sim.MakeStore[VirtualNetworkTap](srv.DB(), "network_virtual_network_taps")

	registerAzureNetworkResource(srv, azureNetworkResourceSpec[VirtualNetworkTap]{
		collection:   "virtualNetworkTaps",
		nameParam:    "tapName",
		resourceType: "Microsoft.Network/virtualNetworkTaps",
		store:        azureVirtualNetworkTaps,
		header: func(tap *VirtualNetworkTap) *azureNetworkResourceHeader {
			return &tap.azureNetworkResourceHeader
		},
		validate: func(w http.ResponseWriter, _ *http.Request, tap *VirtualNetworkTap) bool {
			// Azure rejects a tap naming neither an interface IP configuration nor a load
			// balancer frontend.
			if tap.Properties.DestinationNetworkInterfaceIPConfiguration == nil &&
				tap.Properties.DestinationLoadBalancerFrontEndIPConfiguration == nil {
				AzureErrorf(w, "VirtualNetworkTapDestinationRequired", http.StatusBadRequest,
					"A virtual network tap requires a destination network interface IP configuration or load balancer frontend IP configuration.")
				return false
			}
			return true
		},
		provision: func(_ context.Context, tap *VirtualNetworkTap, previous *VirtualNetworkTap) error {
			tap.Properties.ResourceGUID = sim.NewUUID()
			if previous != nil && previous.Properties.ResourceGUID != "" {
				tap.Properties.ResourceGUID = previous.Properties.ResourceGUID
			}
			tap.Properties.ProvisioningState = "Succeeded"
			return nil
		},
		project: func(tap *VirtualNetworkTap) {
			tap.Properties.NetworkInterfaceTapConfigurations = tapConfigurationsReferencing(tap.ID)
		},
	})
}

func tapConfigurationsReferencing(tapID string) []SubResource {
	if azureNICTapConfigs == nil {
		return nil
	}
	var out []SubResource
	for _, cfg := range azureNICTapConfigs.List() {
		ref, ok := cfg.Properties["virtualNetworkTap"].(map[string]any)
		if !ok {
			continue
		}
		if id, ok := ref["id"].(string); ok && strings.EqualFold(id, tapID) {
			out = append(out, SubResource{ID: cfg.ID})
		}
	}
	return out
}
