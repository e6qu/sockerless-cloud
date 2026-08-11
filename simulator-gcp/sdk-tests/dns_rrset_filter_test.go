package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/dns/v1"
)

// TestDNS_RrsetsListNameTypeFilter pins the rrsets.list name+type
// filter that the Cloud Run service-discovery path uses
// (backends/gcp-common/network_discovery_clouddns.go calls
// .Name(fqdn).Type("A")). Real Cloud DNS returns only the matching
// record set; the sim must honor the query params, not return the
// whole zone.
func TestDNS_RrsetsListNameTypeFilter(t *testing.T) {
	svc := dnsService(t)
	const project = "rrset-filter-proj"

	_, err := svc.ManagedZones.Create(project, &dns.ManagedZone{
		Name:    "disc-zone",
		DnsName: "disc.example.com.",
	}).Do()
	require.NoError(t, err)

	// Two A records + one TXT in the same zone.
	mk := func(name, typ string, data ...string) {
		_, err := svc.ResourceRecordSets.Create(project, "disc-zone", &dns.ResourceRecordSet{
			Name: name, Type: typ, Ttl: 30, Rrdatas: data,
		}).Do()
		require.NoError(t, err)
	}
	mk("svc-a.disc.example.com.", "A", "10.0.0.1")
	mk("svc-b.disc.example.com.", "A", "10.0.0.2")
	mk("svc-a.disc.example.com.", "TXT", "\"hello\"")

	// name + type filter → exactly the one A record.
	got, err := svc.ResourceRecordSets.List(project, "disc-zone").
		Name("svc-a.disc.example.com.").Type("A").Do()
	require.NoError(t, err)
	require.Len(t, got.Rrsets, 1, "name+type filter must return exactly one record set")
	assert.Equal(t, "svc-a.disc.example.com.", got.Rrsets[0].Name)
	assert.Equal(t, "A", got.Rrsets[0].Type)
	assert.Equal(t, []string{"10.0.0.1"}, got.Rrsets[0].Rrdatas)

	// name-only filter → both record types at that name (A + TXT).
	byName, err := svc.ResourceRecordSets.List(project, "disc-zone").
		Name("svc-a.disc.example.com.").Do()
	require.NoError(t, err)
	require.Len(t, byName.Rrsets, 2, "name filter must return all types at that name")

	// no filter → all three.
	all, err := svc.ResourceRecordSets.List(project, "disc-zone").Do()
	require.NoError(t, err)
	require.Len(t, all.Rrsets, 3)
}
