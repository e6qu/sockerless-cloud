package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPublicDNS_ListUpdateAndResourceReference exercises the public-DNS
// operations beyond core CRUD through `az rest`: the subscription-wide zone
// list, the zone and record-set PATCH (Update) operations, and the
// getDnsResourceReference lookup.
func TestPublicDNS_ListUpdateAndResourceReference(t *testing.T) {
	zoneURL := armURL("Microsoft.Network", "dnsZones/cli-more.example.com", "2018-05-01")
	runCLI(t, azRest("PUT", zoneURL, `{"location":"global"}`))
	defer runCLI(t, azRest("DELETE", zoneURL, ""))

	// Zones_Update (PATCH tags).
	out := runCLI(t, azRest("PATCH", zoneURL, `{"tags":{"tier":"public"}}`))
	var zone struct {
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, out, &zone)
	assert.Equal(t, "public", zone.Tags["tier"])

	// Zones_List (subscription-wide). The DNS subscription list path is lowercase.
	listURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Network/dnszones?api-version=2018-05-01", baseURL, subscriptionID)
	out = runCLI(t, azRest("GET", listURL, ""))
	var list struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, z := range list.Value {
		if z.Name == "cli-more.example.com" {
			found = true
		}
	}
	assert.True(t, found, "subscription-wide list should include the created zone")

	// RecordSets_Update (PATCH the record TTL).
	recordURL := armURL("Microsoft.Network", "dnsZones/cli-more.example.com/A/web", "2018-05-01")
	runCLI(t, azRest("PUT", recordURL, `{"properties":{"TTL":300,"ARecords":[{"ipv4Address":"203.0.113.5"}]}}`))
	out = runCLI(t, azRest("PATCH", recordURL, `{"properties":{"TTL":45,"ARecords":[{"ipv4Address":"203.0.113.6"}]}}`))
	var record struct {
		Properties struct {
			TTL      int64 `json:"TTL"`
			ARecords []struct {
				IPv4Address string `json:"ipv4Address"`
			} `json:"ARecords"`
		} `json:"properties"`
	}
	parseJSON(t, out, &record)
	assert.Equal(t, int64(45), record.Properties.TTL)
	require.Len(t, record.Properties.ARecords, 1)
	assert.Equal(t, "203.0.113.6", record.Properties.ARecords[0].IPv4Address)

	// DnsResourceReference_GetByTargetResources (subscription-level POST).
	refURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Network/getDnsResourceReference?api-version=2018-05-01", baseURL, subscriptionID)
	target := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/publicIPAddresses/none", subscriptionID, resourceGroup)
	out = runCLI(t, azRest("POST", refURL, fmt.Sprintf(`{"properties":{"targetResources":[{"id":%q}]}}`, target)))
	var ref struct {
		Properties struct {
			DNSResourceReferences []struct {
				TargetResource struct {
					ID string `json:"id"`
				} `json:"targetResource"`
			} `json:"dnsResourceReferences"`
		} `json:"properties"`
	}
	parseJSON(t, out, &ref)
	require.Len(t, ref.Properties.DNSResourceReferences, 1)
	assert.Equal(t, target, ref.Properties.DNSResourceReferences[0].TargetResource.ID)
}

// TestPrivateDNS_ListUpdateAllAndLinkUpdate exercises the private-DNS
// operations beyond core CRUD through `az rest`: the subscription-wide zone
// list, the zone / virtual-network-link PATCH (Update) operations, and the
// list-all-record-sets read.
func TestPrivateDNS_ListUpdateAllAndLinkUpdate(t *testing.T) {
	zoneURL := dnsURL("privateDnsZones/cli-more.private")
	runCLI(t, azRest("PUT", zoneURL, `{"location":"global"}`))
	defer runCLI(t, azRest("DELETE", zoneURL, ""))

	// PrivateZones_Update (PATCH tags).
	out := runCLI(t, azRest("PATCH", zoneURL, `{"tags":{"team":"net"}}`))
	var zone struct {
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, out, &zone)
	assert.Equal(t, "net", zone.Tags["team"])

	// PrivateZones_List (subscription-wide).
	listURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Network/privateDnsZones?api-version=%s", baseURL, subscriptionID, dnsAPIVersion)
	out = runCLI(t, azRest("GET", listURL, ""))
	var list struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, z := range list.Value {
		if z.Name == "cli-more.private" {
			found = true
		}
	}
	assert.True(t, found, "subscription-wide private zone list should include the created zone")

	// An A record + RecordSets_List (ALL) + RecordSets_Update (PATCH).
	recordURL := dnsURL("privateDnsZones/cli-more.private/A/svc")
	runCLI(t, azRest("PUT", recordURL, `{"properties":{"ttl":300,"aRecords":[{"ipv4Address":"10.2.0.1"}]}}`))

	allURL := dnsURL("privateDnsZones/cli-more.private/ALL")
	out = runCLI(t, azRest("GET", allURL, ""))
	var all struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, out, &all)
	// SOA (auto) + the A record.
	require.GreaterOrEqual(t, len(all.Value), 2)

	out = runCLI(t, azRest("PATCH", recordURL, `{"properties":{"ttl":75,"aRecords":[{"ipv4Address":"10.2.0.2"}]}}`))
	var record struct {
		Properties struct {
			TTL int `json:"ttl"`
		} `json:"properties"`
	}
	parseJSON(t, out, &record)
	assert.Equal(t, 75, record.Properties.TTL)

	// VirtualNetworkLinks_Update (PATCH registration flag).
	linkURL := dnsURL("privateDnsZones/cli-more.private/virtualNetworkLinks/cli-link")
	vnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/cli-more-vnet", subscriptionID, resourceGroup)
	runCLI(t, azRest("PUT", linkURL, fmt.Sprintf(`{"location":"global","properties":{"virtualNetwork":{"id":%q},"registrationEnabled":false}}`, vnetID)))
	out = runCLI(t, azRest("PATCH", linkURL, `{"properties":{"registrationEnabled":true}}`))
	var link struct {
		Properties struct {
			RegistrationEnabled bool `json:"registrationEnabled"`
		} `json:"properties"`
	}
	parseJSON(t, out, &link)
	assert.True(t, link.Properties.RegistrationEnabled)
}
