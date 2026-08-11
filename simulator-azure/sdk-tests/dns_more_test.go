package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPublicDNS_ZonesListUpdateAndResourceReference covers the public-DNS
// operations beyond core CRUD: the subscription-wide zone list, the zone and
// record-set PATCH (Update) operations, and the cross-resource
// getDnsResourceReference lookup.
func TestPublicDNS_ZonesListUpdateAndResourceReference(t *testing.T) {
	rg := "dns-more-rg"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	zonesClient, err := armdns.NewZonesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	zone := "more.example.com"
	_, err = zonesClient.CreateOrUpdate(ctx, rg, zone, armdns.Zone{Location: ptrStr("global")}, nil)
	require.NoError(t, err)

	// Zones_Update — PATCH the zone tags.
	updResp, err := zonesClient.Update(ctx, rg, zone, armdns.ZoneUpdate{
		Tags: map[string]*string{"tier": ptrStr("public")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, updResp.Tags["tier"])
	assert.Equal(t, "public", *updResp.Tags["tier"])

	// Zones_List — every zone in the subscription.
	pager := zonesClient.NewListPager(nil)
	require.True(t, pager.More())
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	found := false
	for _, z := range page.Value {
		if z.Name != nil && *z.Name == zone {
			found = true
		}
	}
	assert.True(t, found, "subscription-wide list should include the created zone")

	recordSetsClient, err := armdns.NewRecordSetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = recordSetsClient.CreateOrUpdate(ctx, rg, zone, "www", armdns.RecordTypeA, armdns.RecordSet{
		Properties: &armdns.RecordSetProperties{
			TTL:      ptrInt64(300),
			ARecords: []*armdns.ARecord{{IPv4Address: ptrStr("203.0.113.10")}},
		},
	}, nil)
	require.NoError(t, err)

	// RecordSets_Update — PATCH merges the new TTL onto the record set.
	recUpd, err := recordSetsClient.Update(ctx, rg, zone, "www", armdns.RecordTypeA, armdns.RecordSet{
		Properties: &armdns.RecordSetProperties{
			TTL:      ptrInt64(60),
			ARecords: []*armdns.ARecord{{IPv4Address: ptrStr("203.0.113.20")}},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, recUpd.Properties)
	require.NotNil(t, recUpd.Properties.TTL)
	assert.Equal(t, int64(60), *recUpd.Properties.TTL)
	require.Len(t, recUpd.Properties.ARecords, 1)
	assert.Equal(t, "203.0.113.20", *recUpd.Properties.ARecords[0].IPv4Address)

	// DnsResourceReference_GetByTargetResources — no records reference the
	// probe target, so it resolves to an empty dnsResources list.
	refClient, err := armdns.NewResourceReferenceClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	target := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Network/publicIPAddresses/none"
	refResp, err := refClient.GetByTargetResources(ctx, armdns.ResourceReferenceRequest{
		Properties: &armdns.ResourceReferenceRequestProperties{
			TargetResources: []*armdns.SubResource{{ID: ptrStr(target)}},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, refResp.Properties)
	require.Len(t, refResp.Properties.DNSResourceReferences, 1)
	require.NotNil(t, refResp.Properties.DNSResourceReferences[0].TargetResource)
	assert.Equal(t, target, *refResp.Properties.DNSResourceReferences[0].TargetResource.ID)
}

// TestPrivateDNS_ListUpdateAllAndLinkUpdate covers the private-DNS operations
// beyond core CRUD: the subscription-wide zone list, the zone /
// virtual-network-link PATCH (Update) operations, and the list-all-record-sets
// read.
func TestPrivateDNS_ListUpdateAllAndLinkUpdate(t *testing.T) {
	rg := "pdns-more-rg"
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, rg, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)

	zonesClient, err := armprivatedns.NewPrivateZonesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	zone := "more.private"
	createPoller, err := zonesClient.BeginCreateOrUpdate(ctx, rg, zone, armprivatedns.PrivateZone{Location: ptrStr("global")}, nil)
	require.NoError(t, err)
	_, err = createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// PrivateZones_Update — PATCH the zone tags.
	updPoller, err := zonesClient.BeginUpdate(ctx, rg, zone, armprivatedns.PrivateZone{
		Tags: map[string]*string{"team": ptrStr("net")},
	}, nil)
	require.NoError(t, err)
	updResp, err := updPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, updResp.Tags["team"])
	assert.Equal(t, "net", *updResp.Tags["team"])

	// PrivateZones_List — every private zone in the subscription.
	pager := zonesClient.NewListPager(nil)
	require.True(t, pager.More())
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, page.Value)

	// A record + RecordSets_Update (PATCH) + RecordSets_List (ALL).
	recordSetsClient, err := armprivatedns.NewRecordSetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = recordSetsClient.CreateOrUpdate(ctx, rg, zone, armprivatedns.RecordTypeA, "db", armprivatedns.RecordSet{
		Properties: &armprivatedns.RecordSetProperties{
			TTL:      to.Ptr[int64](300),
			ARecords: []*armprivatedns.ARecord{{IPv4Address: ptrStr("10.0.0.4")}},
		},
	}, nil)
	require.NoError(t, err)

	recUpd, err := recordSetsClient.Update(ctx, rg, zone, armprivatedns.RecordTypeA, "db", armprivatedns.RecordSet{
		Properties: &armprivatedns.RecordSetProperties{
			TTL:      to.Ptr[int64](120),
			ARecords: []*armprivatedns.ARecord{{IPv4Address: ptrStr("10.0.0.5")}},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, recUpd.Properties)
	require.NotNil(t, recUpd.Properties.TTL)
	assert.Equal(t, int64(120), *recUpd.Properties.TTL)

	allPager := recordSetsClient.NewListPager(rg, zone, nil)
	require.True(t, allPager.More())
	allPage, err := allPager.NextPage(ctx)
	require.NoError(t, err)
	// SOA (auto-created) + the A record.
	require.GreaterOrEqual(t, len(allPage.Value), 2)

	// VirtualNetworkLinks_Update — PATCH the link's registration flag.
	linksClient, err := armprivatedns.NewVirtualNetworkLinksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Network/virtualNetworks/more-vnet"
	createLink, err := linksClient.BeginCreateOrUpdate(ctx, rg, zone, "more-link", armprivatedns.VirtualNetworkLink{
		Location: ptrStr("global"),
		Properties: &armprivatedns.VirtualNetworkLinkProperties{
			VirtualNetwork:      &armprivatedns.SubResource{ID: ptrStr(vnetID)},
			RegistrationEnabled: to.Ptr(false),
		},
	}, nil)
	require.NoError(t, err)
	_, err = createLink.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	updLink, err := linksClient.BeginUpdate(ctx, rg, zone, "more-link", armprivatedns.VirtualNetworkLink{
		Properties: &armprivatedns.VirtualNetworkLinkProperties{RegistrationEnabled: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)
	linkResp, err := updLink.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, linkResp.Properties)
	require.NotNil(t, linkResp.Properties.RegistrationEnabled)
	assert.True(t, *linkResp.Properties.RegistrationEnabled)
}
