package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrivateDNS_NonARecordTypes exercises armprivatedns.RecordSetsClient for
// every non-A record type the sim registers via its generic record-type loop
// (AAAA / CNAME / MX / PTR / SRV / TXT). A-records have a dedicated handler and
// were already covered; these types share a separate code path that maps each
// type's distinct properties, so they each need a round-trip assertion.
func TestPrivateDNS_NonARecordTypes(t *testing.T) {
	const rg = "dns-rs-nonA-rg"
	ensureRG(t, rg)
	const zoneName = "sdk-nonrecords.internal"

	putARMJSON(t,
		"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.Network/privateDnsZones/"+zoneName+"?api-version=2018-09-01",
		`{"location":"global"}`)

	rsClient, err := armprivatedns.NewRecordSetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	ttl := int64(300)

	t.Run("AAAA", func(t *testing.T) {
		const name = "v6"
		resp, err := rsClient.CreateOrUpdate(ctx, rg, zoneName, armprivatedns.RecordTypeAAAA, name, armprivatedns.RecordSet{
			Properties: &armprivatedns.RecordSetProperties{
				TTL:         &ttl,
				AaaaRecords: []*armprivatedns.AaaaRecord{{IPv6Address: ptrStr("2001:db8::1")}},
			},
		}, nil)
		require.NoError(t, err)
		require.Len(t, resp.Properties.AaaaRecords, 1)
		assert.Equal(t, "2001:db8::1", *resp.Properties.AaaaRecords[0].IPv6Address)

		got, err := rsClient.Get(ctx, rg, zoneName, armprivatedns.RecordTypeAAAA, name, nil)
		require.NoError(t, err)
		require.Len(t, got.Properties.AaaaRecords, 1)
		assert.Equal(t, "2001:db8::1", *got.Properties.AaaaRecords[0].IPv6Address)

		_, err = rsClient.Delete(ctx, rg, zoneName, armprivatedns.RecordTypeAAAA, name, nil)
		require.NoError(t, err)
		_, err = rsClient.Get(ctx, rg, zoneName, armprivatedns.RecordTypeAAAA, name, nil)
		assert.Error(t, err, "Get after delete should 404")
	})

	t.Run("CNAME", func(t *testing.T) {
		const name = "alias"
		resp, err := rsClient.CreateOrUpdate(ctx, rg, zoneName, armprivatedns.RecordTypeCNAME, name, armprivatedns.RecordSet{
			Properties: &armprivatedns.RecordSetProperties{
				TTL:         &ttl,
				CnameRecord: &armprivatedns.CnameRecord{Cname: ptrStr("target.sdk-nonrecords.internal")},
			},
		}, nil)
		require.NoError(t, err)
		require.NotNil(t, resp.Properties.CnameRecord)
		assert.Equal(t, "target.sdk-nonrecords.internal", *resp.Properties.CnameRecord.Cname)

		got, err := rsClient.Get(ctx, rg, zoneName, armprivatedns.RecordTypeCNAME, name, nil)
		require.NoError(t, err)
		require.NotNil(t, got.Properties.CnameRecord)
		assert.Equal(t, "target.sdk-nonrecords.internal", *got.Properties.CnameRecord.Cname)
	})

	t.Run("MX", func(t *testing.T) {
		const name = "mail"
		pref := int32(10)
		resp, err := rsClient.CreateOrUpdate(ctx, rg, zoneName, armprivatedns.RecordTypeMX, name, armprivatedns.RecordSet{
			Properties: &armprivatedns.RecordSetProperties{
				TTL:       &ttl,
				MxRecords: []*armprivatedns.MxRecord{{Preference: &pref, Exchange: ptrStr("mx.sdk-nonrecords.internal")}},
			},
		}, nil)
		require.NoError(t, err)
		require.Len(t, resp.Properties.MxRecords, 1)
		assert.Equal(t, int32(10), *resp.Properties.MxRecords[0].Preference)
		assert.Equal(t, "mx.sdk-nonrecords.internal", *resp.Properties.MxRecords[0].Exchange)

		got, err := rsClient.Get(ctx, rg, zoneName, armprivatedns.RecordTypeMX, name, nil)
		require.NoError(t, err)
		require.Len(t, got.Properties.MxRecords, 1)
		assert.Equal(t, "mx.sdk-nonrecords.internal", *got.Properties.MxRecords[0].Exchange)
	})

	t.Run("PTR", func(t *testing.T) {
		const name = "5"
		resp, err := rsClient.CreateOrUpdate(ctx, rg, zoneName, armprivatedns.RecordTypePTR, name, armprivatedns.RecordSet{
			Properties: &armprivatedns.RecordSetProperties{
				TTL:        &ttl,
				PtrRecords: []*armprivatedns.PtrRecord{{Ptrdname: ptrStr("host.sdk-nonrecords.internal")}},
			},
		}, nil)
		require.NoError(t, err)
		require.Len(t, resp.Properties.PtrRecords, 1)
		assert.Equal(t, "host.sdk-nonrecords.internal", *resp.Properties.PtrRecords[0].Ptrdname)

		got, err := rsClient.Get(ctx, rg, zoneName, armprivatedns.RecordTypePTR, name, nil)
		require.NoError(t, err)
		require.Len(t, got.Properties.PtrRecords, 1)
		assert.Equal(t, "host.sdk-nonrecords.internal", *got.Properties.PtrRecords[0].Ptrdname)
	})

	t.Run("SRV", func(t *testing.T) {
		const name = "_sip._tcp"
		prio, weight, port := int32(1), int32(5), int32(5060)
		resp, err := rsClient.CreateOrUpdate(ctx, rg, zoneName, armprivatedns.RecordTypeSRV, name, armprivatedns.RecordSet{
			Properties: &armprivatedns.RecordSetProperties{
				TTL: &ttl,
				SrvRecords: []*armprivatedns.SrvRecord{
					{Priority: &prio, Weight: &weight, Port: &port, Target: ptrStr("sip.sdk-nonrecords.internal")},
				},
			},
		}, nil)
		require.NoError(t, err)
		require.Len(t, resp.Properties.SrvRecords, 1)
		assert.Equal(t, int32(5060), *resp.Properties.SrvRecords[0].Port)
		assert.Equal(t, "sip.sdk-nonrecords.internal", *resp.Properties.SrvRecords[0].Target)

		got, err := rsClient.Get(ctx, rg, zoneName, armprivatedns.RecordTypeSRV, name, nil)
		require.NoError(t, err)
		require.Len(t, got.Properties.SrvRecords, 1)
		assert.Equal(t, int32(5060), *got.Properties.SrvRecords[0].Port)
	})

	t.Run("TXT", func(t *testing.T) {
		const name = "txt"
		resp, err := rsClient.CreateOrUpdate(ctx, rg, zoneName, armprivatedns.RecordTypeTXT, name, armprivatedns.RecordSet{
			Properties: &armprivatedns.RecordSetProperties{
				TTL:        &ttl,
				TxtRecords: []*armprivatedns.TxtRecord{{Value: []*string{ptrStr("v=spf1 -all")}}},
			},
		}, nil)
		require.NoError(t, err)
		require.Len(t, resp.Properties.TxtRecords, 1)
		require.Len(t, resp.Properties.TxtRecords[0].Value, 1)
		assert.Equal(t, "v=spf1 -all", *resp.Properties.TxtRecords[0].Value[0])

		got, err := rsClient.Get(ctx, rg, zoneName, armprivatedns.RecordTypeTXT, name, nil)
		require.NoError(t, err)
		require.Len(t, got.Properties.TxtRecords, 1)
		require.Len(t, got.Properties.TxtRecords[0].Value, 1)
		assert.Equal(t, "v=spf1 -all", *got.Properties.TxtRecords[0].Value[0])

		// ListByType should include the TXT record.
		pager := rsClient.NewListByTypePager(rg, zoneName, armprivatedns.RecordTypeTXT, nil)
		var found bool
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			for _, rs := range page.Value {
				if rs.Name != nil && *rs.Name == name {
					found = true
				}
			}
		}
		assert.True(t, found, "ListByType must include the TXT record")
	})
}
