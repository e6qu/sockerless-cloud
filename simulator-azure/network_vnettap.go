package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Virtual network taps (Microsoft.Network/virtualNetworkTaps) name a collector
// destination — a network interface IP configuration or a load balancer
// frontend — that mirrored traffic is delivered to. A tap on its own moves
// nothing: traffic starts flowing when a network interface's tap configuration
// references it, which is a separate resource the simulator already serves.
// That reference is the tap's real behavior, and it is what the tap's read-only
// networkInterfaceTapConfigurations property reports: the collection is
// resolved from the tap configurations that actually reference this tap, so
// attaching or removing one is visible on the tap itself.

// VirtualNetworkTap mirrors Microsoft.Network/virtualNetworkTaps.
type VirtualNetworkTap struct {
	azureNetworkResourceHeader
	Properties VirtualNetworkTapProperties `json:"properties"`
}

// VirtualNetworkTapProperties holds the collector destination and the
// interfaces mirroring to it.
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
			// A tap has to deliver somewhere: Azure rejects one that names
			// neither an interface IP configuration nor a load balancer frontend.
			if tap.Properties.DestinationNetworkInterfaceIPConfiguration == nil &&
				tap.Properties.DestinationLoadBalancerFrontEndIPConfiguration == nil {
				AzureErrorf(w, "VirtualNetworkTapDestinationRequired", http.StatusBadRequest,
					"A virtual network tap requires a destination network interface IP configuration or load balancer frontend IP configuration.")
				return false
			}
			return true
		},
		provision: func(_ context.Context, tap *VirtualNetworkTap, previous *VirtualNetworkTap) error {
			tap.Properties.ResourceGUID = generateUUID()
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

// tapConfigurationsReferencing returns the network interface tap configurations
// that mirror traffic to this tap.
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
