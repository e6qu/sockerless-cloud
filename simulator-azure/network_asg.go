package main

import (
	"context"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Application security groups (Microsoft.Network/applicationSecurityGroups)
// name a set of network interfaces so a network security group rule can be
// written against workload identity instead of against addresses. The group
// itself carries no configuration — its whole meaning is the membership other
// resources declare by referencing it, and the effect that membership has when
// a security rule names it. Membership is resolved on demand from the network
// interfaces and private endpoints that reference the group, which is why the
// group's own record holds nothing but its resource GUID and provisioning
// state, exactly as the real resource does.
//
// azureApplicationSecurityGroupMemberIPs is the resolution that makes the group
// real: the addresses it returns become the source addresses of the packet
// filter the simulator programs for a rule that names the group, and
// azureNICInApplicationSecurityGroups decides whether a rule scoped to a
// destination group applies to a given interface at all.

// ApplicationSecurityGroup mirrors Microsoft.Network/applicationSecurityGroups.
type ApplicationSecurityGroup struct {
	azureNetworkResourceHeader
	Properties ApplicationSecurityGroupProperties `json:"properties"`
}

// ApplicationSecurityGroupProperties holds the group's read-only state.
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
			// The resource GUID is assigned once, at creation, and survives
			// every later update of the same resource.
			asg.Properties.ResourceGUID = generateUUID()
			if previous != nil && previous.Properties.ResourceGUID != "" {
				asg.Properties.ResourceGUID = previous.Properties.ResourceGUID
			}
			asg.Properties.ProvisioningState = "Succeeded"
			return nil
		},
	})
}

// azureApplicationSecurityGroupMemberIPs returns the private addresses of every
// interface and private endpoint that belongs to any of the referenced groups,
// as host CIDRs ready for a packet filter. Group references that name a group
// with no members contribute nothing, so a rule written against an empty group
// matches no traffic — the real behavior, and the opposite of the "any address"
// default an absent address prefix carries.
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
	if azureNICs != nil {
		for _, nic := range azureNICs.List() {
			for _, ipcfg := range nic.Properties.IPConfigurations {
				if azureReferencesAnyGroup(ipcfg.Properties.ApplicationSecurityGroups, wanted) {
					add(ipcfg.Properties.PrivateIPAddress)
				}
			}
		}
	}
	if azurePrivateEndpoints != nil {
		for _, pe := range azurePrivateEndpoints.List() {
			if !azureReferencesAnyGroup(pe.Properties.ApplicationSecurityGroups, wanted) {
				continue
			}
			for _, ref := range pe.Properties.NetworkInterfaces {
				add(azurePlatformNICPrivateIP(ref.ID))
			}
		}
	}
	return out
}

// azureNICInApplicationSecurityGroups reports whether any IP configuration of
// the interface belongs to one of the referenced groups.
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
