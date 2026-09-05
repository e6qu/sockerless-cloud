package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A private DNS zone group attaches a private endpoint to one or more private
// DNS zones. Attaching it is what makes the linked resource's name resolve to
// the endpoint's private address inside the virtual network, and Azure does
// that by writing A records into the referenced zone. The simulator writes the
// same records into the same private DNS zone store its own record-set surface
// serves, so a record published by a zone group is a record `az network
// private-dns record-set a show` returns and a resolver in the zone's linked
// virtual network answers with. Removing the group — or the endpoint — takes
// those records back out.

// PrivateDNSZoneGroup mirrors
// Microsoft.Network/privateEndpoints/privateDnsZoneGroups.
type PrivateDNSZoneGroup struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// PrivateDnsZoneGroup declares id, name, etag and properties; the ARM
	// resource carries no `type` member.
	Etag       string                        `json:"etag,omitempty"`
	Properties PrivateDNSZoneGroupProperties `json:"properties"`
}

type PrivateDNSZoneGroupProperties struct {
	ProvisioningState     string                 `json:"provisioningState,omitempty"`
	PrivateDNSZoneConfigs []PrivateDNSZoneConfig `json:"privateDnsZoneConfigs,omitempty"`
}

// PrivateDNSZoneConfig binds the endpoint to one private DNS zone.
type PrivateDNSZoneConfig struct {
	Name       string                         `json:"name,omitempty"`
	Properties PrivateDNSZoneConfigProperties `json:"properties"`
}

// PrivateDNSZoneConfigProperties names the zone and reports the records the
// binding produced.
type PrivateDNSZoneConfigProperties struct {
	PrivateDNSZoneID string                     `json:"privateDnsZoneId,omitempty"`
	RecordSets       []PrivateEndpointRecordSet `json:"recordSets,omitempty"`
}

// PrivateEndpointRecordSet describes one record the group published.
type PrivateEndpointRecordSet struct {
	RecordType        string   `json:"recordType,omitempty"`
	RecordSetName     string   `json:"recordSetName,omitempty"`
	Fqdn              string   `json:"fqdn,omitempty"`
	ProvisioningState string   `json:"provisioningState,omitempty"`
	TTL               int      `json:"ttl,omitempty"`
	IPAddresses       []string `json:"ipAddresses,omitempty"`
}

var azurePEDNSZoneGroups sim.Store[PrivateDNSZoneGroup]

// azurePrivateEndpointRecordTTL is the time-to-live Azure Private Link gives
// the A records a private DNS zone group publishes.
const azurePrivateEndpointRecordTTL = 10

func registerPrivateEndpointDNSZoneGroups(srv *sim.Server) {
	azurePEDNSZoneGroups = sim.MakeStore[PrivateDNSZoneGroup](srv.DB(), "network_private_dns_zone_groups")

	armBase := azureNetworkArmBase()
	base := armBase + "/privateEndpoints/{privateEndpointName}/privateDnsZoneGroups"

	endpointID := func(r *http.Request) string {
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/privateEndpoints/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
			sim.PathParam(r, "privateEndpointName"))
	}
	groupID := func(r *http.Request) string {
		return azureNetworkChildID(endpointID(r), "privateDnsZoneGroups", sim.PathParam(r, "privateDnsZoneGroupName"))
	}

	srv.HandleFunc("PUT "+base+"/{privateDnsZoneGroupName}", func(w http.ResponseWriter, r *http.Request) {
		pe, ok := azurePrivateEndpoints.Get(endpointID(r))
		if !ok {
			azureNetworkResourceNotFound(w, "Microsoft.Network/privateEndpoints",
				sim.PathParam(r, "privateEndpointName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		var req PrivateDNSZoneGroup
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := groupID(r)
		if previous, found := azurePEDNSZoneGroups.Get(id); found {
			// A rewrite republishes from scratch, so records for zones the new
			// configuration dropped do not linger.
			deletePrivateEndpointDNSRecords(previous)
		}
		group := PrivateDNSZoneGroup{
			ID:   id,
			Name: sim.PathParam(r, "privateDnsZoneGroupName"),
			Etag: azureNetworkEtag(),
			Properties: PrivateDNSZoneGroupProperties{
				ProvisioningState:     "Succeeded",
				PrivateDNSZoneConfigs: req.Properties.PrivateDNSZoneConfigs,
			},
		}
		if err := publishPrivateEndpointDNSRecords(&group, pe); err != nil {
			AzureErrorf(w, "PrivateDnsZoneNotFound", http.StatusBadRequest, "%v", err)
			return
		}
		azurePEDNSZoneGroups.Put(id, group)
		sim.WriteJSON(w, http.StatusOK, group)
	})

	srv.HandleFunc("GET "+base+"/{privateDnsZoneGroupName}", func(w http.ResponseWriter, r *http.Request) {
		group, ok := azurePEDNSZoneGroups.Get(groupID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Private DNS zone group %q was not found.", sim.PathParam(r, "privateDnsZoneGroupName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, group)
	})

	srv.HandleFunc("DELETE "+base+"/{privateDnsZoneGroupName}", func(w http.ResponseWriter, r *http.Request) {
		group, ok := azurePEDNSZoneGroups.Get(groupID(r))
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		deletePrivateEndpointDNSRecords(group)
		azurePEDNSZoneGroups.Delete(group.ID)
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := azurePrivateEndpoints.Get(endpointID(r)); !ok {
			azureNetworkResourceNotFound(w, "Microsoft.Network/privateEndpoints",
				sim.PathParam(r, "privateEndpointName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		azureWriteList(w, privateEndpointDNSZoneGroups(endpointID(r)))
	})
}

func privateEndpointDNSZoneGroups(endpointID string) []PrivateDNSZoneGroup {
	prefix := endpointID + "/privateDnsZoneGroups/"
	return azurePEDNSZoneGroups.Filter(func(g PrivateDNSZoneGroup) bool {
		return strings.HasPrefix(g.ID, prefix)
	})
}

// publishPrivateEndpointDNSRecords writes an A record into each referenced zone
// for every name the endpoint's connections make private, and records what it
// wrote on the configuration.
func publishPrivateEndpointDNSRecords(group *PrivateDNSZoneGroup, pe PrivateEndpoint) error {
	address := ""
	if len(pe.Properties.NetworkInterfaces) > 0 {
		address = azurePlatformNICPrivateIP(pe.Properties.NetworkInterfaces[0].ID)
	}
	for i := range group.Properties.PrivateDNSZoneConfigs {
		cfg := &group.Properties.PrivateDNSZoneConfigs[i]
		zoneID := cfg.Properties.PrivateDNSZoneID
		zone, ok := azurePrivateDNSZones.Get(zoneID)
		if !ok {
			return fmt.Errorf("private DNS zone %q was not found", zoneID)
		}
		cfg.Properties.RecordSets = nil
		for _, name := range privateEndpointRecordNames(pe, zone.Name) {
			recordID := zone.ID + "/A/" + name
			fqdn := name + "." + zone.Name + "."
			record := RecordSet{
				ID:   recordID,
				Name: name,
				Type: "Microsoft.Network/privateDnsZones/A",
				Etag: generateUUID(),
				Properties: RecordSetProperties{
					TTL:  azurePrivateEndpointRecordTTL,
					Fqdn: fqdn,
				},
			}
			if address != "" {
				record.Properties.ARecords = []ARecord{{IPv4Address: address}}
			}
			azurePrivateDNSRecordSets.Put(recordID, record)
			cfg.Properties.RecordSets = append(cfg.Properties.RecordSets, PrivateEndpointRecordSet{
				RecordType:        "A",
				RecordSetName:     name,
				Fqdn:              fqdn,
				ProvisioningState: "Succeeded",
				TTL:               azurePrivateEndpointRecordTTL,
				IPAddresses:       recordAddresses(address),
			})
		}
	}
	return nil
}

func recordAddresses(address string) []string {
	if address == "" {
		return nil
	}
	return []string{address}
}

// privateEndpointRecordNames returns the record names the endpoint publishes in
// one zone: the linked resource's own name where the zone is the private-link
// zone Azure defines for that resource type, and the endpoint's name where the
// consumer brought a zone of its own (a private link service target, which has
// no Azure-defined zone).
func privateEndpointRecordNames(pe PrivateEndpoint, zoneName string) []string {
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	for _, conn := range privateEndpointRequestedConnections(&pe) {
		targetID := conn.Properties.PrivateLinkServiceID
		targetName := targetID[strings.LastIndex(targetID, "/")+1:]
		target, ok := privateLinkTargetFor(targetID)
		if !ok || target.dnsSuffixes == nil {
			continue
		}
		for _, group := range privateEndpointGroupsFor(conn, target) {
			if _, zone := target.dnsSuffixes(group); strings.EqualFold(zone, zoneName) {
				add(targetName)
			}
		}
	}
	if len(names) == 0 {
		add(pe.Name)
	}
	return names
}

// privateEndpointGroupsFor returns the groups a connection asked for, falling
// back to every group the target publishes when the request named none.
func privateEndpointGroupsFor(conn PrivateLinkServiceConnection, target azurePrivateLinkTarget) []string {
	if len(conn.Properties.GroupIDs) > 0 {
		return conn.Properties.GroupIDs
	}
	return target.groupIDs
}

// deletePrivateEndpointDNSRecords removes the records a group published.
func deletePrivateEndpointDNSRecords(group PrivateDNSZoneGroup) {
	for _, cfg := range group.Properties.PrivateDNSZoneConfigs {
		for _, record := range cfg.Properties.RecordSets {
			azurePrivateDNSRecordSets.Delete(cfg.Properties.PrivateDNSZoneID + "/A/" + record.RecordSetName)
		}
	}
}
