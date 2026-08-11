package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicDNS_CreateZoneAndRecordSet(t *testing.T) {
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "dns-public-rg", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	zonesClient, err := armdns.NewZonesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	zoneResp, err := zonesClient.CreateOrUpdate(ctx, "dns-public-rg", "sdk-public.example.com", armdns.Zone{
		Location: ptrStr("global"),
		Tags: map[string]*string{
			"env": ptrStr("sdk"),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, zoneResp.Name)
	assert.Equal(t, "sdk-public.example.com", *zoneResp.Name)
	require.NotNil(t, zoneResp.Properties)
	require.NotNil(t, zoneResp.Properties.ZoneType)
	assert.Equal(t, armdns.ZoneTypePublic, *zoneResp.Properties.ZoneType)
	require.Len(t, zoneResp.Properties.NameServers, 4)
	require.NotNil(t, zoneResp.Properties.NumberOfRecordSets)
	assert.Equal(t, int64(2), *zoneResp.Properties.NumberOfRecordSets)

	recordSetsClient, err := armdns.NewRecordSetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	recordResp, err := recordSetsClient.CreateOrUpdate(ctx, "dns-public-rg", "sdk-public.example.com", "api", armdns.RecordTypeA, armdns.RecordSet{
		Properties: &armdns.RecordSetProperties{
			TTL: ptrInt64(180),
			ARecords: []*armdns.ARecord{
				{IPv4Address: ptrStr("198.51.100.25")},
			},
			Metadata: map[string]*string{
				"owner": ptrStr("sdk"),
			},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, recordResp.Name)
	assert.Equal(t, "api", *recordResp.Name)
	require.NotNil(t, recordResp.Properties)
	require.NotNil(t, recordResp.Properties.TTL)
	assert.Equal(t, int64(180), *recordResp.Properties.TTL)
	require.Len(t, recordResp.Properties.ARecords, 1)
	assert.Equal(t, "198.51.100.25", *recordResp.Properties.ARecords[0].IPv4Address)
	require.NotNil(t, recordResp.Properties.Fqdn)
	assert.Equal(t, "api.sdk-public.example.com.", *recordResp.Properties.Fqdn)

	getResp, err := recordSetsClient.Get(ctx, "dns-public-rg", "sdk-public.example.com", "api", armdns.RecordTypeA, nil)
	require.NoError(t, err)
	require.NotNil(t, getResp.Properties)
	require.Len(t, getResp.Properties.ARecords, 1)

	pager := recordSetsClient.NewListByDNSZonePager("dns-public-rg", "sdk-public.example.com", nil)
	require.True(t, pager.More())
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Value, 3)
}

func ptrInt64(v int64) *int64 {
	return &v
}
