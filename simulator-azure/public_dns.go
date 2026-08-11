package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

type PublicDnsZone struct {
	ID         string                  `json:"id"`
	Name       string                  `json:"name"`
	Type       string                  `json:"type"`
	Location   string                  `json:"location"`
	Etag       string                  `json:"etag,omitempty"`
	Tags       map[string]string       `json:"tags,omitempty"`
	Properties PublicDnsZoneProperties `json:"properties"`
}

type PublicDnsZoneProperties struct {
	MaxNumberOfRecordSets          int64    `json:"maxNumberOfRecordSets,omitempty"`
	MaxNumberOfRecordsPerRecordSet int64    `json:"maxNumberOfRecordsPerRecordSet,omitempty"`
	NumberOfRecordSets             int64    `json:"numberOfRecordSets,omitempty"`
	NameServers                    []string `json:"nameServers,omitempty"`
	ZoneType                       string   `json:"zoneType,omitempty"`
}

type PublicRecordSet struct {
	ID         string                    `json:"id"`
	Name       string                    `json:"name"`
	Type       string                    `json:"type"`
	Etag       string                    `json:"etag,omitempty"`
	Properties PublicRecordSetProperties `json:"properties"`
}

type PublicRecordSetProperties struct {
	TTL               int64             `json:"TTL,omitempty"`
	Fqdn              string            `json:"fqdn,omitempty"`
	ProvisioningState string            `json:"provisioningState,omitempty"`
	ARecords          []ARecord         `json:"ARecords,omitempty"`
	AAAARecords       []AAAARecord      `json:"AAAARecords,omitempty"`
	CAARecords        []CAARecord       `json:"caaRecords,omitempty"`
	CNAMERecord       *CNAMERecord      `json:"CNAMERecord,omitempty"`
	MXRecords         []MXRecord        `json:"MXRecords,omitempty"`
	NSRecords         []NSRecord        `json:"NSRecords,omitempty"`
	PTRRecords        []PTRRecord       `json:"PTRRecords,omitempty"`
	SOARecord         *PublicSOARecord  `json:"SOARecord,omitempty"`
	SRVRecords        []SRVRecord       `json:"SRVRecords,omitempty"`
	TXTRecords        []TXTRecord       `json:"TXTRecords,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	TargetResource    *SubResource      `json:"targetResource,omitempty"`
}

// PublicSOARecord is the public-DNS (2018-05-01) SoaRecord wire shape.
// It is distinct from the private-DNS SOARecord because the two APIs
// spell the minimum-TTL member differently: public DNS uses minimumTTL,
// private DNS uses minimumTtl.
type PublicSOARecord struct {
	Host         string `json:"host,omitempty"`
	Email        string `json:"email,omitempty"`
	SerialNumber int64  `json:"serialNumber,omitempty"`
	RefreshTime  int32  `json:"refreshTime,omitempty"`
	RetryTime    int32  `json:"retryTime,omitempty"`
	ExpireTime   int32  `json:"expireTime,omitempty"`
	MinimumTTL   int32  `json:"minimumTTL,omitempty"`
}

type CAARecord struct {
	Flags int32  `json:"flags"`
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

type NSRecord struct {
	NSDName string `json:"nsdname"`
}

var (
	azurePublicDNSZones      sim.Store[PublicDnsZone]
	azurePublicDNSRecordSets sim.Store[PublicRecordSet]
)

func registerPublicDNS(srv *sim.Server) {
	zones := sim.MakeStore[PublicDnsZone](srv.DB(), "public_dns_zones")
	recordSets := sim.MakeStore[PublicRecordSet](srv.DB(), "public_dns_record_sets")
	azurePublicDNSZones = zones
	azurePublicDNSRecordSets = recordSets

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network"

	srv.HandleFunc("PUT "+armBase+"/dnsZones/{zoneName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		zoneName := sim.PathParam(r, "zoneName")

		var req PublicDnsZone
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		location := req.Location
		if location == "" {
			location = "global"
		}
		resourceID := publicDNSZoneID(sub, rg, zoneName)
		_, exists := zones.Get(resourceID)

		zone := PublicDnsZone{
			ID:       resourceID,
			Name:     zoneName,
			Type:     "Microsoft.Network/dnsZones",
			Location: strings.ToLower(location),
			Etag:     generateUUID(),
			Tags:     req.Tags,
			Properties: PublicDnsZoneProperties{
				MaxNumberOfRecordSets:          5000,
				MaxNumberOfRecordsPerRecordSet: 20,
				NameServers:                    publicDNSNameServers(zoneName),
				ZoneType:                       "Public",
			},
		}
		zones.Put(resourceID, zone)

		if !exists {
			createPublicDNSDefaultRecords(recordSets, resourceID, zoneName)
		}
		refreshPublicDNSRecordCount(zones, recordSets, resourceID)
		zone, _ = zones.Get(resourceID)

		status := http.StatusOK
		if !exists {
			status = http.StatusCreated
		}
		sim.WriteJSON(w, status, zone)
	})

	srv.HandleFunc("GET "+armBase+"/dnsZones/{zoneName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		zoneName := sim.PathParam(r, "zoneName")

		zone, ok := zones.Get(publicDNSZoneID(sub, rg, zoneName))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Network/dnsZones/%s' under resource group '%s' was not found.", zoneName, rg)
			return
		}
		sim.WriteJSON(w, http.StatusOK, zone)
	})

	srv.HandleFunc("GET "+armBase+"/dnsZones", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/dnsZones/", sub, rg)
		filtered := zones.Filter(func(z PublicDnsZone) bool {
			return strings.HasPrefix(z.ID, prefix)
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": filtered})
	})

	srv.HandleFunc("DELETE "+armBase+"/dnsZones/{zoneName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		zoneName := sim.PathParam(r, "zoneName")
		zoneID := publicDNSZoneID(sub, rg, zoneName)

		if zones.Delete(zoneID) {
			records := recordSets.Filter(func(rs PublicRecordSet) bool {
				return strings.HasPrefix(rs.ID, zoneID+"/")
			})
			for _, rs := range records {
				recordSets.Delete(rs.ID)
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv.HandleFunc("GET "+armBase+"/dnsZones/{zoneName}/recordsets", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		zoneName := sim.PathParam(r, "zoneName")
		writePublicDNSRecordSetList(w, r, recordSets, publicDNSZoneID(sub, rg, zoneName), "")
	})

	for _, rt := range []string{"A", "AAAA", "CAA", "CNAME", "MX", "NS", "PTR", "SOA", "SRV", "TXT"} {
		recordType := rt
		srv.HandleFunc(
			"PUT "+armBase+"/dnsZones/{zoneName}/"+recordType+"/{recordName}",
			func(w http.ResponseWriter, r *http.Request) {
				sub := sim.PathParam(r, "subscriptionId")
				rg := sim.PathParam(r, "resourceGroupName")
				zoneName := sim.PathParam(r, "zoneName")
				recordName := sim.PathParam(r, "recordName")
				zoneID := publicDNSZoneID(sub, rg, zoneName)
				if _, ok := zones.Get(zoneID); !ok {
					sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
						"The Resource 'Microsoft.Network/dnsZones/%s' under resource group '%s' was not found.", zoneName, rg)
					return
				}

				var req PublicRecordSet
				if err := sim.ReadJSON(r, &req); err != nil {
					sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
					return
				}
				recordID := publicDNSRecordID(zoneID, recordType, recordName)
				_, exists := recordSets.Get(recordID)
				rs := publicDNSRecordFromRequest(recordID, zoneName, recordType, recordName, req)
				recordSets.Put(recordID, rs)
				refreshPublicDNSRecordCount(zones, recordSets, zoneID)

				status := http.StatusOK
				if !exists {
					status = http.StatusCreated
				}
				sim.WriteJSON(w, status, rs)
			})

		srv.HandleFunc(
			"GET "+armBase+"/dnsZones/{zoneName}/"+recordType+"/{recordName}",
			func(w http.ResponseWriter, r *http.Request) {
				sub := sim.PathParam(r, "subscriptionId")
				rg := sim.PathParam(r, "resourceGroupName")
				zoneName := sim.PathParam(r, "zoneName")
				recordName := sim.PathParam(r, "recordName")
				recordID := publicDNSRecordID(publicDNSZoneID(sub, rg, zoneName), recordType, recordName)

				rs, ok := recordSets.Get(recordID)
				if !ok {
					sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
						"The record set '%s' of type '%s' in zone '%s' was not found.", recordName, recordType, zoneName)
					return
				}
				sim.WriteJSON(w, http.StatusOK, rs)
			})

		srv.HandleFunc(
			"DELETE "+armBase+"/dnsZones/{zoneName}/"+recordType+"/{recordName}",
			func(w http.ResponseWriter, r *http.Request) {
				sub := sim.PathParam(r, "subscriptionId")
				rg := sim.PathParam(r, "resourceGroupName")
				zoneName := sim.PathParam(r, "zoneName")
				recordName := sim.PathParam(r, "recordName")
				zoneID := publicDNSZoneID(sub, rg, zoneName)
				recordID := publicDNSRecordID(zoneID, recordType, recordName)

				if recordSets.Delete(recordID) {
					refreshPublicDNSRecordCount(zones, recordSets, zoneID)
					w.WriteHeader(http.StatusOK)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})

		srv.HandleFunc(
			"GET "+armBase+"/dnsZones/{zoneName}/"+recordType,
			func(w http.ResponseWriter, r *http.Request) {
				sub := sim.PathParam(r, "subscriptionId")
				rg := sim.PathParam(r, "resourceGroupName")
				zoneName := sim.PathParam(r, "zoneName")
				prefix := publicDNSRecordPrefix(publicDNSZoneID(sub, rg, zoneName), recordType)
				writePublicDNSRecordSetListWithPrefix(w, r, recordSets, prefix)
			})
	}

	srv.HandleFunc("GET "+armBase+"/dnsZones/{zoneName}/all", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		zoneName := sim.PathParam(r, "zoneName")
		writePublicDNSRecordSetList(w, r, recordSets, publicDNSZoneID(sub, rg, zoneName), "")
	})

	registerPublicDNSMore(srv, zones, recordSets)
}

func publicDNSZoneID(sub, rg, zoneName string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/dnsZones/%s", sub, rg, zoneName)
}

func publicDNSRecordID(zoneID, recordType, recordName string) string {
	return fmt.Sprintf("%s/%s/%s", zoneID, recordType, recordName)
}

func publicDNSRecordPrefix(zoneID, recordType string) string {
	if recordType == "" {
		return zoneID + "/"
	}
	return fmt.Sprintf("%s/%s/", zoneID, recordType)
}

func publicDNSNameServers(zoneName string) []string {
	label := strings.NewReplacer(".", "-").Replace(strings.ToLower(zoneName))
	return []string{
		"ns1-" + label + ".azure-dns.com",
		"ns2-" + label + ".azure-dns.net",
		"ns3-" + label + ".azure-dns.org",
		"ns4-" + label + ".azure-dns.info",
	}
}

func createPublicDNSDefaultRecords(recordSets sim.Store[PublicRecordSet], zoneID, zoneName string) {
	nsRecords := make([]NSRecord, 0, 4)
	for _, nameServer := range publicDNSNameServers(zoneName) {
		nsRecords = append(nsRecords, NSRecord{NSDName: nameServer + "."})
	}
	recordSets.Put(publicDNSRecordID(zoneID, "NS", "@"), PublicRecordSet{
		ID:   publicDNSRecordID(zoneID, "NS", "@"),
		Name: "@",
		Type: "Microsoft.Network/dnsZones/NS",
		Etag: generateUUID(),
		Properties: PublicRecordSetProperties{
			TTL:       172800,
			Fqdn:      zoneName + ".",
			NSRecords: nsRecords,
		},
	})
	recordSets.Put(publicDNSRecordID(zoneID, "SOA", "@"), PublicRecordSet{
		ID:   publicDNSRecordID(zoneID, "SOA", "@"),
		Name: "@",
		Type: "Microsoft.Network/dnsZones/SOA",
		Etag: generateUUID(),
		Properties: PublicRecordSetProperties{
			TTL:  3600,
			Fqdn: zoneName + ".",
			SOARecord: &PublicSOARecord{
				Host:         nsRecords[0].NSDName,
				Email:        "azuredns-hostmaster.microsoft.com",
				SerialNumber: 1,
				RefreshTime:  3600,
				RetryTime:    300,
				ExpireTime:   2419200,
				MinimumTTL:   300,
			},
		},
	})
}

func publicDNSRecordFromRequest(recordID, zoneName, recordType, recordName string, req PublicRecordSet) PublicRecordSet {
	ttl := req.Properties.TTL
	if ttl == 0 {
		ttl = 3600
	}
	return PublicRecordSet{
		ID:   recordID,
		Name: recordName,
		Type: "Microsoft.Network/dnsZones/" + recordType,
		Etag: generateUUID(),
		Properties: PublicRecordSetProperties{
			TTL:               ttl,
			Fqdn:              publicDNSFQDN(zoneName, recordName),
			ProvisioningState: "Succeeded",
			ARecords:          req.Properties.ARecords,
			AAAARecords:       req.Properties.AAAARecords,
			CAARecords:        req.Properties.CAARecords,
			CNAMERecord:       req.Properties.CNAMERecord,
			MXRecords:         req.Properties.MXRecords,
			NSRecords:         req.Properties.NSRecords,
			PTRRecords:        req.Properties.PTRRecords,
			SOARecord:         req.Properties.SOARecord,
			SRVRecords:        req.Properties.SRVRecords,
			TXTRecords:        req.Properties.TXTRecords,
			Metadata:          req.Properties.Metadata,
			TargetResource:    req.Properties.TargetResource,
		},
	}
}

func publicDNSFQDN(zoneName, recordName string) string {
	if recordName == "@" {
		return zoneName + "."
	}
	return recordName + "." + zoneName + "."
}

func refreshPublicDNSRecordCount(zones sim.Store[PublicDnsZone], recordSets sim.Store[PublicRecordSet], zoneID string) {
	zones.Update(zoneID, func(z *PublicDnsZone) {
		records := recordSets.Filter(func(rs PublicRecordSet) bool {
			return strings.HasPrefix(rs.ID, zoneID+"/")
		})
		z.Properties.NumberOfRecordSets = int64(len(records))
	})
}

func writePublicDNSRecordSetList(w http.ResponseWriter, r *http.Request, recordSets sim.Store[PublicRecordSet], zoneID, recordType string) {
	writePublicDNSRecordSetListWithPrefix(w, r, recordSets, publicDNSRecordPrefix(zoneID, recordType))
}

func writePublicDNSRecordSetListWithPrefix(w http.ResponseWriter, r *http.Request, recordSets sim.Store[PublicRecordSet], prefix string) {
	suffix := r.URL.Query().Get("$recordsetnamesuffix")
	filtered := recordSets.Filter(func(rs PublicRecordSet) bool {
		if !strings.HasPrefix(rs.ID, prefix) {
			return false
		}
		return suffix == "" || strings.HasSuffix(rs.Name, suffix)
	})
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].ID < filtered[j].ID
	})
	if topRaw := r.URL.Query().Get("$top"); topRaw != "" {
		if top, err := strconv.Atoi(topRaw); err == nil && top >= 0 && top < len(filtered) {
			filtered = filtered[:top]
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": filtered})
}
