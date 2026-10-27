package main

import (
	"context"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// An application security group names a set of network interfaces so a
// security rule can target workload identity instead of addresses. The group
// holds only its resource GUID and provisioning state; its membership resolves
// on demand from the interfaces and private endpoints referencing it. Rules
// naming a source group filter on azureApplicationSecurityGroupMemberIPs, and
// azureNICInApplicationSecurityGroups decides whether a destination-scoped
// rule applies to an interface.

type ApplicationSecurityGroup struct {
	azureNetworkResourceHeader
	Properties ApplicationSecurityGroupProperties `json:"properties"`
}

type ApplicationSecurityGroupProperties struct {
	ResourceGUID      string `json:"resourceGuid,omitempty"`
	ProvisioningState string `json:"provisioningState,omitempty"`
}

var azureASGs sim.Store[ApplicationSecurityGroup]

func registerNetworkApplicationSecurityGroups(srv *sim.Server) {
	azureASGs = sim.MakeStore[ApplicationSecurityGroup](srv.DB(), "network_application_security_groups")

	registerAzureNetworkResource(srv, azureNetworkResourceSpec[ApplicationSecurityGroup]{
		collection:   "applicationSecurityGroups",
		nameParam:    "applicationSecurityGroupName",
		resourceType: "Microsoft.Network/applicationSecurityGroups",
		store:        azureASGs,
		header: func(asg *ApplicationSecurityGroup) *azureNetworkResourceHeader {
			return &asg.azureNetworkResourceHeader
		},
		provision: func(_ context.Context, asg *ApplicationSecurityGroup, previous *ApplicationSecurityGroup) error {
			// Assigned once, at creation.
			asg.Properties.ResourceGUID = sim.NewUUID()
			if previous != nil && previous.Properties.ResourceGUID != "" {
				asg.Properties.ResourceGUID = previous.Properties.ResourceGUID
			}
			asg.Properties.ProvisioningState = "Succeeded"
			return nil
		},
	})
}

// As host CIDRs ready for a packet filter. A group with no members contributes
// nothing, so a rule against it matches no traffic, unlike the "any address"
// default of an absent prefix.
func azureApplicationSecurityGroupMemberIPs(refs []SubResource) []string {
	if len(refs) == 0 {
		return nil
	}
	wanted := map[string]bool{}
	for _, ref := range refs {
		if ref.ID != "" {
			wanted[strings.ToLower(ref.ID)] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	add := func(ip string) {
		if ip == "" || seen[ip] {
			return
		}
		seen[ip] = true
		out = append(out, ip+"/32")
	}
	for group := range wanted {
		if azureNICs != nil {
			for _, nic := range azureNICsByApplicationSecurityGroup.LookupAll(azureNICs, group, azureNICApplicationSecurityGroupKeys) {
				for _, ipcfg := range nic.Properties.IPConfigurations {
					if azureReferencesAnyGroup(ipcfg.Properties.ApplicationSecurityGroups, wanted) {
						add(ipcfg.Properties.PrivateIPAddress)
					}
				}
			}
		}
		if azurePrivateEndpoints != nil {
			for _, pe := range azurePrivateEndpointsByApplicationSecurityGroup.LookupAll(azurePrivateEndpoints, group, func(pe PrivateEndpoint) []string {
				return azureSubResourceKeys(pe.Properties.ApplicationSecurityGroups)
			}) {
				for _, ref := range pe.Properties.NetworkInterfaces {
					add(azurePlatformNICPrivateIP(ref.ID))
				}
			}
		}
	}
	return out
}

func azureNICInApplicationSecurityGroups(nic NetworkInterface, refs []SubResource) bool {
	wanted := map[string]bool{}
	for _, ref := range refs {
		if ref.ID != "" {
			wanted[strings.ToLower(ref.ID)] = true
		}
	}
	for _, ipcfg := range nic.Properties.IPConfigurations {
		if azureReferencesAnyGroup(ipcfg.Properties.ApplicationSecurityGroups, wanted) {
			return true
		}
	}
	return false
}

func azureReferencesAnyGroup(refs []SubResource, wanted map[string]bool) bool {
	for _, ref := range refs {
		if wanted[strings.ToLower(ref.ID)] {
			return true
		}
	}
	return false
}

var (
	azureNICsByApplicationSecurityGroup             sim.GenerationIndex[NetworkInterface]
	azurePrivateEndpointsByApplicationSecurityGroup sim.GenerationIndex[PrivateEndpoint]
)

// Lowercased.
func azureNICApplicationSecurityGroupKeys(nic NetworkInterface) []string {
	var keys []string
	for _, ipcfg := range nic.Properties.IPConfigurations {
		keys = append(keys, azureSubResourceKeys(ipcfg.Properties.ApplicationSecurityGroups)...)
	}
	return keys
}

func azureSubResourceKeys(refs []SubResource) []string {
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		keys = append(keys, strings.ToLower(ref.ID))
	}
	return keys
}
