package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// dnsResourceReferenceRequest is the body of
// DnsResourceReference_GetByTargetResources: a list of Azure resource IDs whose
// referencing DNS records the caller wants to resolve.
type dnsResourceReferenceRequest struct {
	Properties struct {
		TargetResources []SubResource `json:"targetResources"`
	} `json:"properties"`
}

// dnsResourceReference pairs one target Azure resource with the DNS record sets
// that reference it (e.g. an A record whose targetResource is a public IP).
type dnsResourceReference struct {
	DNSResources   []SubResource `json:"dnsResources"`
	TargetResource SubResource   `json:"targetResource"`
}

type dnsResourceReferenceResult struct {
	Properties struct {
		DNSResourceReferences []dnsResourceReference `json:"dnsResourceReferences"`
	} `json:"properties"`
}

// registerPublicDNSMore adds the public-DNS operations beyond the core
// zone/record-set CRUD: the subscription-wide zone list, the zone and
// record-set PATCH (Update) operations, and the cross-resource
// getDnsResourceReference lookup.
func registerPublicDNSMore(srv *sim.Server, zones sim.Store[PublicDnsZone], recordSets sim.Store[PublicRecordSet]) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network"
	const subBase = "/subscriptions/{subscriptionId}/providers/Microsoft.Network"

	// Zones_List — every DNS zone in the subscription. The DNS SDK spells the
	// subscription-wide list path lowercase ("dnszones"), unlike the
	// resource-group list ("dnsZones"); match it exactly so the mux routes it.
	srv.HandleFunc("GET "+subBase+"/dnszones", func(w http.ResponseWriter, r *http.Request) {
		prefix := fmt.Sprintf("/subscriptions/%s/", sim.PathParam(r, "subscriptionId"))
		filtered := zones.Filter(func(z PublicDnsZone) bool { return strings.HasPrefix(z.ID, prefix) })
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": filtered})
	})

	// Zones_Update — PATCH the zone's tags.
	srv.HandleFunc("PATCH "+armBase+"/dnsZones/{zoneName}", func(w http.ResponseWriter, r *http.Request) {
		id := publicDNSZoneID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "zoneName"))
		var req PublicDnsZone
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !zones.Update(id, func(z *PublicDnsZone) {
			z.Tags = req.Tags
			z.Etag = generateUUID()
		}) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Network/dnsZones/%s' under resource group '%s' was not found.",
				sim.PathParam(r, "zoneName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		zone, _ := zones.Get(id)
		sim.WriteJSON(w, http.StatusOK, zone)
	})

	// RecordSets_Update — PATCH merges TTL / metadata / records onto the
	// existing record set, one route per supported record type.
	for _, rt := range []string{"A", "AAAA", "CAA", "CNAME", "MX", "NS", "PTR", "SOA", "SRV", "TXT"} {
		recordType := rt
		srv.HandleFunc("PATCH "+armBase+"/dnsZones/{zoneName}/"+recordType+"/{recordName}", func(w http.ResponseWriter, r *http.Request) {
			zoneID := publicDNSZoneID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "zoneName"))
			recordID := publicDNSRecordID(zoneID, recordType, sim.PathParam(r, "recordName"))
			var req PublicRecordSet
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if !recordSets.Update(recordID, func(rs *PublicRecordSet) {
				mergePublicRecordSet(&rs.Properties, req.Properties)
				rs.Etag = generateUUID()
			}) {
				sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"The record set '%s' of type '%s' in zone '%s' was not found.",
					sim.PathParam(r, "recordName"), recordType, sim.PathParam(r, "zoneName"))
				return
			}
			rs, _ := recordSets.Get(recordID)
			sim.WriteJSON(w, http.StatusOK, rs)
		})
	}

	// DnsResourceReference_GetByTargetResources — return, for each requested
	// Azure resource, the DNS record sets that reference it via targetResource.
	srv.HandleFunc("POST "+subBase+"/getDnsResourceReference", func(w http.ResponseWriter, r *http.Request) {
		var req dnsResourceReferenceRequest
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var result dnsResourceReferenceResult
		for _, target := range req.Properties.TargetResources {
			ref := dnsResourceReference{TargetResource: SubResource{ID: target.ID}}
			for _, rs := range recordSets.List() {
				if rs.Properties.TargetResource != nil && strings.EqualFold(rs.Properties.TargetResource.ID, target.ID) {
					ref.DNSResources = append(ref.DNSResources, SubResource{ID: rs.ID})
				}
			}
			result.Properties.DNSResourceReferences = append(result.Properties.DNSResourceReferences, ref)
		}
		sim.WriteJSON(w, http.StatusOK, result)
	})
}

// mergePublicRecordSet applies the non-empty fields of a PATCH body onto an
// existing public-DNS record set, mirroring the partial-update semantics of
// RecordSets_Update.
func mergePublicRecordSet(dst *PublicRecordSetProperties, src PublicRecordSetProperties) {
	if src.TTL != 0 {
		dst.TTL = src.TTL
	}
	if src.Metadata != nil {
		dst.Metadata = src.Metadata
	}
	if src.ARecords != nil {
		dst.ARecords = src.ARecords
	}
	if src.AAAARecords != nil {
		dst.AAAARecords = src.AAAARecords
	}
	if src.CAARecords != nil {
		dst.CAARecords = src.CAARecords
	}
	if src.CNAMERecord != nil {
		dst.CNAMERecord = src.CNAMERecord
	}
	if src.MXRecords != nil {
		dst.MXRecords = src.MXRecords
	}
	if src.NSRecords != nil {
		dst.NSRecords = src.NSRecords
	}
	if src.PTRRecords != nil {
		dst.PTRRecords = src.PTRRecords
	}
	if src.SRVRecords != nil {
		dst.SRVRecords = src.SRVRecords
	}
	if src.TXTRecords != nil {
		dst.TXTRecords = src.TXTRecords
	}
	if src.SOARecord != nil {
		dst.SOARecord = src.SOARecord
	}
	if src.TargetResource != nil {
		dst.TargetResource = src.TargetResource
	}
}

// registerPrivateDNSMore adds the private-DNS operations beyond the core
// zone/record-set/vnet-link CRUD: the subscription-wide zone list, the zone /
// record-set / virtual-network-link PATCH (Update) operations, and the
// list-all-record-sets read.
func registerPrivateDNSMore(srv *sim.Server, zones sim.Store[PrivateDnsZone], recordSets sim.Store[RecordSet], vnetLinks sim.Store[VNetLink]) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network"
	const subBase = "/subscriptions/{subscriptionId}/providers/Microsoft.Network"

	privateZoneID := func(r *http.Request) string {
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/privateDnsZones/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "zoneName"))
	}

	// PrivateZones_List — every private DNS zone in the subscription.
	srv.HandleFunc("GET "+subBase+"/privateDnsZones", func(w http.ResponseWriter, r *http.Request) {
		prefix := fmt.Sprintf("/subscriptions/%s/", sim.PathParam(r, "subscriptionId"))
		all := zones.Filter(func(z PrivateDnsZone) bool { return strings.HasPrefix(z.ID, prefix) })
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		page, next := armPage(r, all)
		out := map[string]any{"value": page}
		if next != "" {
			out["nextLink"] = armNextLink(r, next)
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})

	// PrivateZones_Update — PATCH the zone's tags.
	srv.HandleFunc("PATCH "+armBase+"/privateDnsZones/{zoneName}", func(w http.ResponseWriter, r *http.Request) {
		id := privateZoneID(r)
		var req PrivateDnsZone
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !zones.Update(id, func(z *PrivateDnsZone) { z.Tags = req.Tags }) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Network/privateDnsZones/%s' under resource group '%s' was not found.",
				sim.PathParam(r, "zoneName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		zone, _ := zones.Get(id)
		sim.WriteJSON(w, http.StatusOK, zone)
	})

	// RecordSets_List — every record set in the zone, regardless of type.
	srv.HandleFunc("GET "+armBase+"/privateDnsZones/{zoneName}/ALL", func(w http.ResponseWriter, r *http.Request) {
		prefix := privateZoneID(r) + "/"
		filtered := recordSets.Filter(func(rs RecordSet) bool { return strings.HasPrefix(rs.ID, prefix) })
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": filtered})
	})

	// RecordSets_Update — PATCH merges TTL / metadata / records onto the
	// existing record set, one route per supported record type.
	for _, rt := range []string{"A", "AAAA", "CNAME", "MX", "PTR", "SRV", "TXT"} {
		recordType := rt
		srv.HandleFunc("PATCH "+armBase+"/privateDnsZones/{zoneName}/"+recordType+"/{recordName}", func(w http.ResponseWriter, r *http.Request) {
			recordID := fmt.Sprintf("%s/%s/%s", privateZoneID(r), recordType, sim.PathParam(r, "recordName"))
			var req RecordSet
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if !recordSets.Update(recordID, func(rs *RecordSet) {
				mergePrivateRecordSet(&rs.Properties, req.Properties)
				rs.Etag = generateUUID()
			}) {
				sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"The record set '%s' of type '%s' in zone '%s' was not found.",
					sim.PathParam(r, "recordName"), recordType, sim.PathParam(r, "zoneName"))
				return
			}
			rs, _ := recordSets.Get(recordID)
			sim.WriteJSON(w, http.StatusOK, rs)
		})
	}

	// VirtualNetworkLinks_Update — PATCH the link's registration flag / tags.
	srv.HandleFunc("PATCH "+armBase+"/privateDnsZones/{zoneName}/virtualNetworkLinks/{linkName}", func(w http.ResponseWriter, r *http.Request) {
		id := privateZoneID(r) + "/virtualNetworkLinks/" + sim.PathParam(r, "linkName")
		var req VNetLink
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !vnetLinks.Update(id, func(link *VNetLink) {
			if req.Tags != nil {
				link.Tags = req.Tags
			}
			link.Properties.RegistrationEnabled = req.Properties.RegistrationEnabled
		}) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Virtual network link '%s' not found.", sim.PathParam(r, "linkName"))
			return
		}
		link, _ := vnetLinks.Get(id)
		sim.WriteJSON(w, http.StatusOK, link)
	})
}

// mergePrivateRecordSet applies the non-empty fields of a PATCH body onto an
// existing private-DNS record set, mirroring RecordSets_Update semantics.
func mergePrivateRecordSet(dst *RecordSetProperties, src RecordSetProperties) {
	if src.TTL != 0 {
		dst.TTL = src.TTL
	}
	if src.Metadata != nil {
		dst.Metadata = src.Metadata
	}
	if src.ARecords != nil {
		dst.ARecords = src.ARecords
	}
	if src.AAAARecords != nil {
		dst.AAAARecords = src.AAAARecords
	}
	if src.CNAMERecord != nil {
		dst.CNAMERecord = src.CNAMERecord
	}
	if src.MXRecords != nil {
		dst.MXRecords = src.MXRecords
	}
	if src.PTRRecords != nil {
		dst.PTRRecords = src.PTRRecords
	}
	if src.SRVRecords != nil {
		dst.SRVRecords = src.SRVRecords
	}
	if src.TXTRecords != nil {
		dst.TXTRecords = src.TXTRecords
	}
	if src.SOARecord != nil {
		dst.SOARecord = src.SOARecord
	}
}
