package gcp_sdk_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/dns/v1"
)

// cloudDNSResolver resolves through the simulator's Cloud DNS name server, the
// coordinate a client points its resolver at.
func cloudDNSResolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(dialCtx context.Context, network, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(dialCtx, network, dnsAddr)
		},
	}
}

// A new public zone starts with its NS and SOA record sets and four assigned
// name servers, and Cloud DNS answers the zone's records over DNS as soon as
// they are written.
func TestDNS_PublicZoneAnswersOverDNS(t *testing.T) {
	t.Parallel()
	svc := dnsService(t)
	const project = "dns-serve-project"
	const zoneName = "dns-serve-zone"
	apex := "dns-serve.example.com."

	created, err := svc.ManagedZones.Create(project, &dns.ManagedZone{Name: zoneName, DnsName: apex}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.ManagedZones.Delete(project, zoneName).Do() })
	require.Len(t, created.NameServers, 4)
	for _, ns := range created.NameServers {
		assert.True(t, strings.HasSuffix(ns, ".googledomains.com."), ns)
	}

	apexSets, err := svc.ResourceRecordSets.List(project, zoneName).Name(apex).Do()
	require.NoError(t, err)
	types := map[string][]string{}
	for _, rs := range apexSets.Rrsets {
		types[rs.Type] = rs.Rrdatas
	}
	assert.Equal(t, created.NameServers, types["NS"])
	require.Len(t, types["SOA"], 1)
	assert.True(t, strings.HasPrefix(types["SOA"][0], created.NameServers[0]+" cloud-dns-hostmaster.google.com. 1 "), types["SOA"][0])

	for _, rs := range []*dns.ResourceRecordSet{
		{Name: "www." + apex, Type: "A", Ttl: 60, Rrdatas: []string{"192.0.2.44"}},
		{Name: "alias." + apex, Type: "CNAME", Ttl: 60, Rrdatas: []string{"www." + apex}},
		{Name: apex, Type: "TXT", Ttl: 60, Rrdatas: []string{`"site-verification=abc"`}},
	} {
		_, err := svc.ResourceRecordSets.Create(project, zoneName, rs).Do()
		require.NoError(t, err)
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resolver := cloudDNSResolver()
	addrs, err := resolver.LookupHost(lookupCtx, "alias."+apex)
	require.NoError(t, err)
	assert.Equal(t, []string{"192.0.2.44"}, addrs)
	txt, err := resolver.LookupTXT(lookupCtx, apex)
	require.NoError(t, err)
	assert.Equal(t, []string{"site-verification=abc"}, txt)

	_, err = resolver.LookupHost(lookupCtx, "missing."+apex)
	var dnsErr *net.DNSError
	require.True(t, errors.As(err, &dnsErr), "a name the zone does not hold: %v", err)
	assert.True(t, dnsErr.IsNotFound, "the zone answers NXDOMAIN for it: %v", err)
}
