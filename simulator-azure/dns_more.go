package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

type dnsResourceReferenceRequest struct {
	Properties struct {
		TargetResources []SubResource `json:"targetResources"`
	} `json:"properties"`
}

// dnsResourceReference pairs a target resource with the record sets that
// reference it, such as an A record whose targetResource is a public IP.
type dnsResourceReference struct {
	DNSResources   []SubResource `json:"dnsResources"`
	TargetResource SubResource   `json:"targetResource"`
}

type dnsResourceReferenceResult struct {
	Properties struct {
		DNSResourceReferences []dnsResourceReference `json:"dnsResourceReferences"`
	} `json:"properties"`
}

func registerPublicDNSMore(srv *sim.Server, zones sim.Store[PublicDnsZone], recordSets sim.Store[PublicRecordSet]) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network"
	const subBase = "/subscriptions/{subscriptionId}/providers/Microsoft.Network"

	// The DNS SDK spells the subscription-wide list "dnszones", unlike the
	// resource group's "dnsZones".
	srv.HandleFunc("GET "+subBase+"/dnszones", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
		filtered := zones.Filter(func(z PublicDnsZone) bool { return strings.HasPrefix(z.ID, prefix) })
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": filtered})
	})

	srv.HandleFunc("PATCH "+armBase+"/dnsZones/{zoneName}", func(w http.ResponseWriter, r *http.Request) {
		id := publicDNSZoneID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "zoneName"))
		var req PublicDnsZone
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !zones.Update(id, func(z *PublicDnsZone) {
			z.Tags = req.Tags
			z.Etag = sim.NewUUID()
		}) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Network/dnsZones/%s' under resource group '%s' was not found.",
				sim.PathParam(r, "zoneName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		zone, _ := zones.Get(id)
		sim.WriteJSON(w, http.StatusOK, zone)
	})

	// RecordSets_Update merges TTL, metadata and records onto the record set.
	for _, rt := range []string{"A", "AAAA", "CAA", "CNAME", "MX", "NS", "PTR", "SOA", "SRV", "TXT"} {
		recordType := rt
		srv.HandleFunc("PATCH "+armBase+"/dnsZones/{zoneName}/"+recordType+"/{recordName}", func(w http.ResponseWriter, r *http.Request) {
			zoneID := publicDNSZoneID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "zoneName"))
			recordID := publicDNSRecordID(zoneID, recordType, sim.PathParam(r, "recordName"))
			var req PublicRecordSet
			if err := sim.ReadJSON(r, &req); err != nil {
				AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if !recordSets.Update(recordID, func(rs *PublicRecordSet) {
				mergePublicRecordSet(&rs.Properties, req.Properties)
				rs.Etag = sim.NewUUID()
			}) {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"The record set '%s' of type '%s' in zone '%s' was not found.",
					sim.PathParam(r, "recordName"), recordType, sim.PathParam(r, "zoneName"))
				return
			}
			rs, _ := recordSets.Get(recordID)
			sim.WriteJSON(w, http.StatusOK, rs)
		})
	}

	srv.HandleFunc("POST "+subBase+"/getDnsResourceReference", func(w http.ResponseWriter, r *http.Request) {
		var req dnsResourceReferenceRequest
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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

// mergePublicRecordSet applies the non-empty fields, RecordSets_Update's
// partial-update semantics.
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

func registerPrivateDNSMore(srv *sim.Server, zones sim.Store[PrivateDnsZone], recordSets sim.Store[RecordSet], vnetLinks sim.Store[VNetLink]) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network"
	const subBase = "/subscriptions/{subscriptionId}/providers/Microsoft.Network"

	privateZoneID := func(r *http.Request) string {
		return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "privateDnsZones", sim.PathParam(r, "zoneName"))
	}

	srv.HandleFunc("GET "+subBase+"/privateDnsZones", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
		all := zones.Filter(func(z PrivateDnsZone) bool { return strings.HasPrefix(z.ID, prefix) })
		page, next, pageOK := armPage(w, r, all, func(z PrivateDnsZone) string { return azureNameIDKey(z.Name, z.ID) })
		if !pageOK {
			return
		}
		out := map[string]any{"value": page}
		if next != "" {
			out["nextLink"] = armNextLink(r, next)
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})

	srv.HandleFunc("PATCH "+armBase+"/privateDnsZones/{zoneName}", func(w http.ResponseWriter, r *http.Request) {
		id := privateZoneID(r)
		var req PrivateDnsZone
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !zones.Update(id, func(z *PrivateDnsZone) { z.Tags = req.Tags }) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Network/privateDnsZones/%s' under resource group '%s' was not found.",
				sim.PathParam(r, "zoneName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		zone, _ := zones.Get(id)
		sim.WriteJSON(w, http.StatusOK, zone)
	})

	// RecordSets_List: every record set in the zone, of any type.
	srv.HandleFunc("GET "+armBase+"/privateDnsZones/{zoneName}/ALL", func(w http.ResponseWriter, r *http.Request) {
		prefix := privateZoneID(r) + "/"
		filtered := recordSets.Filter(func(rs RecordSet) bool { return strings.HasPrefix(rs.ID, prefix) })
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": filtered})
	})

	// RecordSets_Update merges TTL, metadata and records onto the record set.
	for _, rt := range []string{"A", "AAAA", "CNAME", "MX", "PTR", "SRV", "TXT"} {
		recordType := rt
		srv.HandleFunc("PATCH "+armBase+"/privateDnsZones/{zoneName}/"+recordType+"/{recordName}", func(w http.ResponseWriter, r *http.Request) {
			recordID := fmt.Sprintf("%s/%s/%s", privateZoneID(r), recordType, sim.PathParam(r, "recordName"))
			var req RecordSet
			if err := sim.ReadJSON(r, &req); err != nil {
				AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if !recordSets.Update(recordID, func(rs *RecordSet) {
				mergePrivateRecordSet(&rs.Properties, req.Properties)
				rs.Etag = sim.NewUUID()
			}) {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"The record set '%s' of type '%s' in zone '%s' was not found.",
					sim.PathParam(r, "recordName"), recordType, sim.PathParam(r, "zoneName"))
				return
			}
			rs, _ := recordSets.Get(recordID)
			sim.WriteJSON(w, http.StatusOK, rs)
		})
	}

	srv.HandleFunc("PATCH "+armBase+"/privateDnsZones/{zoneName}/virtualNetworkLinks/{linkName}", func(w http.ResponseWriter, r *http.Request) {
		id := privateZoneID(r) + "/virtualNetworkLinks/" + sim.PathParam(r, "linkName")
		var req VNetLink
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !vnetLinks.Update(id, func(link *VNetLink) {
			if req.Tags != nil {
				link.Tags = req.Tags
			}
			link.Properties.RegistrationEnabled = req.Properties.RegistrationEnabled
		}) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Virtual network link '%s' not found.", sim.PathParam(r, "linkName"))
			return
		}
		azureReconcileAutoRegisteredRecords()
		link, _ := vnetLinks.Get(id)
		sim.WriteJSON(w, http.StatusOK, link)
	})
}

// mergePrivateRecordSet applies the non-empty fields, RecordSets_Update's
// partial-update semantics.
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
