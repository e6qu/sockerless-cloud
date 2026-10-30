package main

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// gceMetadataComputeInstanceKeys are the predefined instance keys a Compute
// Engine instance reads, each from the instance resource as the store holds it
// now.
func gceMetadataComputeInstanceKeys(r *http.Request, inst ComputeInstance) []gceMetadataEntry {
	workload := gceMetadataWorkloadFor(r)
	project := workload.projectID()
	projectRef := project
	if number, ok := workload.projectNumber(); ok {
		projectRef = number
	}
	zone := lastSegment(inst.Zone)
	entries := []gceMetadataEntry{
		constantLeaf("cpu-platform", gcpInstanceCPUPlatform(inst)),
		constantLeaf("description", inst.Description),
		{name: "disks", list: true, children: func(*http.Request) []gceMetadataEntry { return gceMetadataDisks(inst) }},
		{name: "guest-attributes", gate: gceGuestAttributesGate, children: func(r *http.Request) []gceMetadataEntry {
			return gceMetadataGuestAttributesChildren(r, inst)
		}},
		constantLeaf("hostname", fmt.Sprintf("%s.%s.c.%s.internal", inst.Name, zone, project)),
		constantLeaf("id", gceMetadataInstanceID(inst)),
		constantLeaf("machine-type", fmt.Sprintf("projects/%s/machineTypes/%s", projectRef, lastSegment(inst.MachineType))),
		// The simulator runs no host maintenance, so no event is ever pending.
		constantLeaf("maintenance-event", "NONE"),
		constantLeaf("name", inst.Name),
		{name: "tags", jsonDefault: true, value: func(*http.Request) any { return gceMetadataTags(inst) }},
		constantLeaf("zone", fmt.Sprintf("projects/%s/zones/%s", projectRef, zone)),
	}
	if image, err := gcpInstanceBootImage(inst); err == nil {
		entries = append(entries, constantLeaf("image", gcpComputeResourcePath(image.SelfLink)))
	}
	return entries
}

// gceMetadataInstanceID is the instance's id, a number in recursive JSON.
func gceMetadataInstanceID(inst ComputeInstance) any {
	if id, err := strconv.ParseInt(inst.Id, 10, 64); err == nil {
		return id
	}
	return inst.Id
}

func gceMetadataTags(inst ComputeInstance) []string {
	if inst.Tags == nil || inst.Tags.Items == nil {
		return []string{}
	}
	return inst.Tags.Items
}

// gceMetadataDisks lists the instance's attached disks by index, each with the
// keys the predefined-keys reference names.
func gceMetadataDisks(inst ComputeInstance) []gceMetadataEntry {
	entries := make([]gceMetadataEntry, 0, len(inst.Disks))
	for i, disk := range inst.Disks {
		index := strconv.Itoa(i)
		keys := []gceMetadataEntry{
			constantLeaf("device-name", disk.DeviceName),
			constantLeaf("index", disk.Index),
		}
		if disk.Interface != "" {
			keys = append(keys, constantLeaf("interface", disk.Interface))
		}
		keys = append(keys, constantLeaf("mode", disk.Mode), constantLeaf("type", disk.Type))
		entries = append(entries, gceMetadataEntry{name: index, jsonName: index, children: func(*http.Request) []gceMetadataEntry { return keys }})
	}
	return entries
}

// gceInterfaceKeys are the keys of the reading instance's interface nic.
func gceInterfaceKeys(r *http.Request, inst ComputeInstance, index int) map[string]string {
	iface := inst.NetworkInterfaces[index]
	keys := map[string]string{
		"ip":  iface.NetworkIP,
		"mac": gceInterfaceMAC(iface.NetworkIP),
		"mtu": strconv.Itoa(gceNetworkMTU),
	}
	if number, ok := gceMetadataWorkloadFor(r).projectNumber(); ok {
		keys["network"] = "projects/" + number + "/networks/" + lastSegment(iface.Network)
	}
	if gcpSubnetworks != nil {
		if subnet, ok := gcpSubnetworks.Get(iface.Subnetwork); ok {
			keys["gateway"] = subnet.GatewayAddress
			if prefix, err := netip.ParsePrefix(subnet.IpCidrRange); err == nil {
				keys["subnetmask"] = net.IP(net.CIDRMask(prefix.Bits(), 32)).String()
			}
		}
	}
	if booted, ok := gcpInstanceBootedAs(inst.SelfLink); ok && index == 0 {
		keys["dns-servers"] = strings.Join(booted.dns, "\n")
	}
	return keys
}

// gceMetadataInterfacesChildren lists the reading workload's interfaces by
// index: a Compute Engine instance's, or the one a Cloud Run worker pool or
// instance with Direct VPC holds on its subnetwork, which Cloud Run's
// container contract reports by its `ip` alone.
func gceMetadataInterfacesChildren(r *http.Request) []gceMetadataEntry {
	inst, ok := gcpMetadataInstanceFor(r)
	if !ok {
		if ip := gceMetadataWorkloadFor(r).vpcAddress; ip != "" {
			return []gceMetadataEntry{{name: "0", jsonName: "0", children: func(*http.Request) []gceMetadataEntry {
				return []gceMetadataEntry{constantLeaf("ip", ip)}
			}}}
		}
		return []gceMetadataEntry{}
	}
	children := make([]gceMetadataEntry, 0, len(inst.NetworkInterfaces))
	for i := range inst.NetworkInterfaces {
		nic := strconv.Itoa(i)
		children = append(children, gceMetadataEntry{name: nic, jsonName: nic, children: func(r *http.Request) []gceMetadataEntry {
			return gceMetadataInterfaceChildren(r, inst, i)
		}})
	}
	return children
}

func gceMetadataInterfaceChildren(r *http.Request, inst ComputeInstance, index int) []gceMetadataEntry {
	entries := gceKeyEntries(gceInterfaceKeys(r, inst, index))
	configs := inst.NetworkInterfaces[index].AccessConfigs
	if len(configs) == 0 {
		return entries
	}
	entries = append(entries, gceMetadataEntry{name: "access-configs", list: true, children: func(*http.Request) []gceMetadataEntry {
		out := make([]gceMetadataEntry, 0, len(configs))
		for i, config := range configs {
			name := strconv.Itoa(i)
			keys := gceKeyEntries(map[string]string{"external-ip": config.NatIP, "type": config.Type})
			out = append(out, gceMetadataEntry{name: name, jsonName: name, children: func(*http.Request) []gceMetadataEntry { return keys }})
		}
		return out
	}})
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries
}

// cloudPlatformScope is the scope a Cloud Run or Cloud Build workload's
// tokens carry: access scopes are a Compute Engine instance setting, and the
// serverless services grant the broad scope and leave access to IAM.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// metadataServiceAccountScopes are the access scopes the workload's account
// holds: the ones the instance's service account was given, or the broad
// scope for a workload that is no instance.
func metadataServiceAccountScopes(r *http.Request) []string {
	inst, ok := gcpMetadataInstanceFor(r)
	if !ok {
		return []string{cloudPlatformScope}
	}
	scopes := []string{}
	if len(inst.ServiceAccounts) == 0 {
		return scopes
	}
	switch held := inst.ServiceAccounts[0]["scopes"].(type) {
	case []string:
		scopes = append(scopes, held...)
	case []any:
		for _, scope := range held {
			if s, ok := scope.(string); ok {
				scopes = append(scopes, s)
			}
		}
	}
	return scopes
}
