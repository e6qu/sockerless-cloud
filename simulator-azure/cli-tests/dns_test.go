package azure_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const dnsAPIVersion = "2018-09-01"

func dnsURL(path string) string {
	return armURL("Microsoft.Network", path, dnsAPIVersion)
}

func TestPrivateDNS_CreateAndShowZone(t *testing.T) {
	url := dnsURL("privateDnsZones/cli-test.local")

	out := runCLI(t, azRest("PUT", url, `{"location":"global"}`))

	var zone struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Location string `json:"location"`
	}
	parseJSON(t, out, &zone)
	assert.Equal(t, "cli-test.local", zone.Name)
	assert.Equal(t, "global", zone.Location)

	// GET
	out = runCLI(t, azRest("GET", url, ""))
	parseJSON(t, out, &zone)
	assert.Equal(t, "cli-test.local", zone.Name)

	// Cleanup
	runCLI(t, azRest("DELETE", url, ""))
}

func TestPrivateDNS_ListZonesByResourceGroup(t *testing.T) {
	zoneURL := dnsURL("privateDnsZones/cli-list.local")
	runCLI(t, azRest("PUT", zoneURL, `{"location":"global"}`))

	out := runCLI(t, azRest("GET", dnsURL("privateDnsZones"), ""))
	var zones struct {
		Value []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"value"`
	}
	parseJSON(t, out, &zones)
	found := false
	for _, zone := range zones.Value {
		if zone.Name == "cli-list.local" {
			found = true
			assert.Equal(t, "Microsoft.Network/privateDnsZones", zone.Type)
		}
	}
	assert.True(t, found, "expected list response to include cli-list.local")

	runCLI(t, azRest("DELETE", zoneURL, ""))
}

func TestPrivateDNS_CreateRecordSet(t *testing.T) {
	zoneURL := dnsURL("privateDnsZones/record-test.local")
	runCLI(t, azRest("PUT", zoneURL, `{"location":"global"}`))

	recordURL := dnsURL("privateDnsZones/record-test.local/A/myrecord")
	out := runCLI(t, azRest("PUT", recordURL,
		`{"properties":{"ttl":300,"aRecords":[{"ipv4Address":"10.0.0.1"}]}}`))

	var record struct {
		Name       string `json:"name"`
		Properties struct {
			TTL      int `json:"ttl"`
			ARecords []struct {
				IPv4Address string `json:"ipv4Address"`
			} `json:"aRecords"`
		} `json:"properties"`
	}
	parseJSON(t, out, &record)
	assert.Equal(t, "myrecord", record.Name)
	require.Len(t, record.Properties.ARecords, 1)
	assert.Equal(t, "10.0.0.1", record.Properties.ARecords[0].IPv4Address)

	// Cleanup
	runCLI(t, azRest("DELETE", recordURL, ""))
	runCLI(t, azRest("DELETE", zoneURL, ""))
}

func TestPrivateDNS_VNetLink(t *testing.T) {
	zoneURL := dnsURL("privateDnsZones/link-test.local")
	runCLI(t, azRest("PUT", zoneURL, `{"location":"global"}`))

	linkURL := dnsURL("privateDnsZones/link-test.local/virtualNetworkLinks/mylink")
	out := runCLI(t, azRest("PUT", linkURL,
		`{"location":"global","properties":{"virtualNetwork":{"id":"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/myvnet"},"registrationEnabled":false}}`))

	var link struct {
		Name       string `json:"name"`
		Properties struct {
			ProvisioningState string `json:"provisioningState"`
		} `json:"properties"`
	}
	parseJSON(t, out, &link)
	assert.Equal(t, "mylink", link.Name)
	assert.Equal(t, "Succeeded", link.Properties.ProvisioningState)

	// GET
	out = runCLI(t, azRest("GET", linkURL, ""))
	parseJSON(t, out, &link)
	assert.Equal(t, "mylink", link.Name)

	out = runCLI(t, azRest("GET", dnsURL("privateDnsZones/link-test.local/virtualNetworkLinks"), ""))
	var links struct {
		Value []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"value"`
	}
	parseJSON(t, out, &links)
	require.Len(t, links.Value, 1)
	assert.Equal(t, "mylink", links.Value[0].Name)
	assert.Equal(t, "Microsoft.Network/privateDnsZones/virtualNetworkLinks", links.Value[0].Type)

	// Cleanup
	runCLI(t, azRest("DELETE", linkURL, ""))
	runCLI(t, azRest("DELETE", zoneURL, ""))
}

func TestPrivateDNS_DeleteZone(t *testing.T) {
	zoneURL := dnsURL("privateDnsZones/delete-test.local")
	runCLI(t, azRest("PUT", zoneURL, `{"location":"global"}`))
	runCLI(t, azRest("DELETE", zoneURL, ""))

	// Verify deletion - GET should fail
	cmd := azRest("GET", zoneURL, "")
	_, err := cmd.CombinedOutput()
	assert.Error(t, err, "Expected GET to fail after deletion")
}

func TestPrivateDNS_RecordSetListAndDelete(t *testing.T) {
	// Create zone
	zoneURL := dnsURL("privateDnsZones/list-delete.local")
	runCLI(t, azRest("PUT", zoneURL, `{"location":"global"}`))
	defer runCLI(t, azRest("DELETE", zoneURL, ""))

	// Create two A records
	r1URL := dnsURL("privateDnsZones/list-delete.local/A/host1")
	r2URL := dnsURL("privateDnsZones/list-delete.local/A/host2")
	runCLI(t, azRest("PUT", r1URL, `{"properties":{"ttl":60,"aRecords":[{"ipv4Address":"10.1.0.1"}]}}`))
	runCLI(t, azRest("PUT", r2URL, `{"properties":{"ttl":60,"aRecords":[{"ipv4Address":"10.1.0.2"}]}}`))

	// List A records for this zone
	listURL := dnsURL("privateDnsZones/list-delete.local/A")
	out := runCLI(t, azRest("GET", listURL, ""))
	var list struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, out, &list)
	names := make(map[string]bool)
	for _, rs := range list.Value {
		names[rs.Name] = true
	}
	assert.True(t, names["host1"], "expected host1 in list")
	assert.True(t, names["host2"], "expected host2 in list")

	// Delete host1 and verify it is gone
	runCLI(t, azRest("DELETE", r1URL, ""))
	cmd := azRest("GET", r1URL, "")
	_, err := cmd.CombinedOutput()
	assert.Error(t, err, "GET after DELETE should fail for host1")

	// host2 must still be present
	out = runCLI(t, azRest("GET", r2URL, ""))
	var r2 struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &r2)
	assert.Equal(t, "host2", r2.Name)
}

func TestPublicDNS_CreateZoneAndRecordSet(t *testing.T) {
	zoneURL := armURL("Microsoft.Network", "dnsZones/cli-public.example.com", "2018-05-01")
	out := runCLI(t, azRest("PUT", zoneURL, `{"location":"global","tags":{"env":"cli"}}`))

	var zone struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Type       string `json:"type"`
		Location   string `json:"location"`
		Properties struct {
			ZoneType           string   `json:"zoneType"`
			NameServers        []string `json:"nameServers"`
			NumberOfRecordSets int64    `json:"numberOfRecordSets"`
		} `json:"properties"`
	}
	parseJSON(t, out, &zone)
	assert.Equal(t, "cli-public.example.com", zone.Name)
	assert.Equal(t, "Microsoft.Network/dnsZones", zone.Type)
	assert.Equal(t, "global", zone.Location)
	assert.Equal(t, "Public", zone.Properties.ZoneType)
	require.Len(t, zone.Properties.NameServers, 4)
	assert.Equal(t, int64(2), zone.Properties.NumberOfRecordSets)

	recordURL := armURL("Microsoft.Network", "dnsZones/cli-public.example.com/A/www", "2018-05-01")
	out = runCLI(t, azRest("PUT", recordURL,
		`{"properties":{"TTL":300,"ARecords":[{"ipv4Address":"203.0.113.10"}],"metadata":{"owner":"cli"}}}`))

	var record struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Type       string `json:"type"`
		Properties struct {
			TTL      int64  `json:"TTL"`
			Fqdn     string `json:"fqdn"`
			ARecords []struct {
				IPv4Address string `json:"ipv4Address"`
			} `json:"ARecords"`
			Metadata map[string]string `json:"metadata"`
		} `json:"properties"`
	}
	parseJSON(t, out, &record)
	assert.Equal(t, "www", record.Name)
	assert.Equal(t, "Microsoft.Network/dnsZones/A", record.Type)
	assert.Equal(t, int64(300), record.Properties.TTL)
	assert.Equal(t, "www.cli-public.example.com.", record.Properties.Fqdn)
	require.Len(t, record.Properties.ARecords, 1)
	assert.Equal(t, "203.0.113.10", record.Properties.ARecords[0].IPv4Address)
	assert.Equal(t, "cli", record.Properties.Metadata["owner"])

	out = runCLI(t, azRest("GET", armURL("Microsoft.Network", "dnsZones/cli-public.example.com/all", "2018-05-01"), ""))
	var records struct {
		Value []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"value"`
	}
	parseJSON(t, out, &records)
	require.Len(t, records.Value, 3)

	runCLI(t, azRest("DELETE", recordURL, ""))
	runCLI(t, azRest("DELETE", zoneURL, ""))
}
