package azure_sdk_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrivateDNS_RecordSetsCRUD exercises armprivatedns.RecordSetsClient
// for A-record Create / Get / List / Delete operations.
func TestPrivateDNS_RecordSetsCRUD(t *testing.T) {
	const rg = "dns-rs-sdk-rg"
	ensureRG(t, rg)
	const zoneName = "sdk-records.internal"
	const recordName = "www"

	// Create zone first
	putARMJSON(t,
		"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.Network/privateDnsZones/"+zoneName+"?api-version=2018-09-01",
		`{"location":"global"}`)

	rsClient, err := armprivatedns.NewRecordSetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// Create A record
	ttl := int64(300)
	createResp, err := rsClient.CreateOrUpdate(ctx, rg, zoneName, armprivatedns.RecordTypeA, recordName, armprivatedns.RecordSet{
		Properties: &armprivatedns.RecordSetProperties{
			TTL: &ttl,
			ARecords: []*armprivatedns.ARecord{
				{IPv4Address: ptrStr("10.0.0.5")},
				{IPv4Address: ptrStr("10.0.0.6")},
			},
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, recordName, *createResp.Name)
	require.NotNil(t, createResp.Properties)
	assert.Equal(t, int64(300), *createResp.Properties.TTL)
	require.Len(t, createResp.Properties.ARecords, 2)

	// Get
	getResp, err := rsClient.Get(ctx, rg, zoneName, armprivatedns.RecordTypeA, recordName, nil)
	require.NoError(t, err)
	assert.Equal(t, recordName, *getResp.Name)
	assert.Equal(t, int64(300), *getResp.Properties.TTL)
	require.Len(t, getResp.Properties.ARecords, 2)

	// List — pager should yield our record
	pager := rsClient.NewListByTypePager(rg, zoneName, armprivatedns.RecordTypeA, nil)
	var found bool
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, rs := range page.Value {
			if rs.Name != nil && *rs.Name == recordName {
				found = true
			}
		}
	}
	assert.True(t, found, "expected ListByType to include the created A record")

	// Delete
	_, err = rsClient.Delete(ctx, rg, zoneName, armprivatedns.RecordTypeA, recordName, nil)
	require.NoError(t, err)

	// Get after delete — must 404
	_, err = rsClient.Get(ctx, rg, zoneName, armprivatedns.RecordTypeA, recordName, nil)
	assert.Error(t, err, "Get after delete should fail with 404")
}

func TestPrivateDNS_RecordSetsUpdate(t *testing.T) {
	const rg = "dns-rs-update-rg"
	ensureRG(t, rg)
	const zoneName = "update-test.internal"
	const recordName = "api"

	putARMJSON(t,
		"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.Network/privateDnsZones/"+zoneName+"?api-version=2018-09-01",
		`{"location":"global"}`)

	rsClient, err := armprivatedns.NewRecordSetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	ttl := int64(60)
	_, err = rsClient.CreateOrUpdate(ctx, rg, zoneName, armprivatedns.RecordTypeA, recordName, armprivatedns.RecordSet{
		Properties: &armprivatedns.RecordSetProperties{
			TTL:      &ttl,
			ARecords: []*armprivatedns.ARecord{{IPv4Address: ptrStr("192.168.1.1")}},
		},
	}, nil)
	require.NoError(t, err)

	// Update TTL and add second IP
	ttl2 := int64(120)
	updateResp, err := rsClient.CreateOrUpdate(ctx, rg, zoneName, armprivatedns.RecordTypeA, recordName, armprivatedns.RecordSet{
		Properties: &armprivatedns.RecordSetProperties{
			TTL:      &ttl2,
			ARecords: []*armprivatedns.ARecord{{IPv4Address: ptrStr("192.168.1.1")}, {IPv4Address: ptrStr("192.168.1.2")}},
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(120), *updateResp.Properties.TTL)
	require.Len(t, updateResp.Properties.ARecords, 2)
}

func TestPrivateDNS_ListZonesAndVirtualNetworkLinks(t *testing.T) {
	rg := "dns-private-rg"
	ensureRG(t, rg)
	zoneName := "sdk-private.example.internal"
	linkName := "sdk-vnet-link"

	putARMJSON(t,
		"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.Network/privateDnsZones/"+zoneName+"?api-version=2018-09-01",
		`{"location":"global","tags":{"env":"sdk"}}`)
	putARMJSON(t,
		"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.Network/privateDnsZones/"+zoneName+"/virtualNetworkLinks/"+linkName+"?api-version=2018-09-01",
		`{"location":"global","properties":{"virtualNetwork":{"id":"/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/dns-private-rg/providers/Microsoft.Network/virtualNetworks/sdk-vnet"},"registrationEnabled":false}}`)

	zones, err := armprivatedns.NewPrivateZonesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	zonePager := zones.NewListByResourceGroupPager(rg, nil)
	require.True(t, zonePager.More())
	zonePage, err := zonePager.NextPage(ctx)
	require.NoError(t, err)

	var foundZone *armprivatedns.PrivateZone
	for _, zone := range zonePage.Value {
		if zone.Name != nil && *zone.Name == zoneName {
			foundZone = zone
			break
		}
	}
	require.NotNil(t, foundZone)
	require.NotNil(t, foundZone.Properties)
	require.NotNil(t, foundZone.Properties.NumberOfRecordSets)
	assert.Equal(t, int64(1), *foundZone.Properties.NumberOfRecordSets)

	links, err := armprivatedns.NewVirtualNetworkLinksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	linkPager := links.NewListPager(rg, zoneName, nil)
	require.True(t, linkPager.More())
	linkPage, err := linkPager.NextPage(ctx)
	require.NoError(t, err)

	var foundLink *armprivatedns.VirtualNetworkLink
	for _, link := range linkPage.Value {
		if link.Name != nil && *link.Name == linkName {
			foundLink = link
			break
		}
	}
	require.NotNil(t, foundLink)
	require.NotNil(t, foundLink.Properties)
	require.NotNil(t, foundLink.Properties.ProvisioningState)
	assert.Equal(t, armprivatedns.ProvisioningStateSucceeded, *foundLink.Properties.ProvisioningState)
}

func putARMJSON(t *testing.T, path, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, baseURL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", simARMBearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
