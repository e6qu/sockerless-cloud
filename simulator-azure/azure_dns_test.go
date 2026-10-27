package main

import (
	"net"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dnsserver"
	"github.com/miekg/dns"
)

func TestAzureDNSServerAnswersConfiguredZones(t *testing.T) {
	srv, err := startAzureDNSServer(azureDNSConfig{
		ListenAddr: "127.0.0.1:0",
		TargetIPv4: net.ParseIP("127.0.0.1"),
		TargetIPv6: net.ParseIP("::1"),
		Zones:      []string{"sockerless.azure.local", "localhost"},
		TTL:        30,
	})
	if err != nil {
		t.Fatalf("start DNS server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	addr := srv.Addr()

	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn("account.blob.sockerless.azure.local"), dns.TypeA)
	resp, _, err := (&dns.Client{Net: "udp", Timeout: time.Second}).Exchange(msg, addr)
	if err != nil {
		t.Fatalf("query A over UDP: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("A query rcode = %s", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("A answer count = %d", len(resp.Answer))
	}
	if got := resp.Answer[0].(*dns.A).A.String(); got != "127.0.0.1" {
		t.Fatalf("A answer = %s", got)
	}

	msg = new(dns.Msg)
	msg.SetQuestion(dns.Fqdn("namespace.servicebus.localhost"), dns.TypeAAAA)
	resp, _, err = (&dns.Client{Net: "tcp", Timeout: time.Second}).Exchange(msg, addr)
	if err != nil {
		t.Fatalf("query AAAA over TCP: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("AAAA query rcode = %s", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("AAAA answer count = %d", len(resp.Answer))
	}
	if got := resp.Answer[0].(*dns.AAAA).AAAA.String(); got != "::1" {
		t.Fatalf("AAAA answer = %s", got)
	}
}

// Azure-provided DNS recurses for a name outside every zone.
func TestAzureDNSServerRecursesOutsideItsZones(t *testing.T) {
	upstream, err := dnsserver.Start(dnsserver.Config{Host: "127.0.0.1", Name: "upstream", Catalog: func(dnsserver.Query) dnsserver.View {
		rr, _ := dns.NewRR("account.blob.example.com. 60 IN A 203.0.113.9")
		return dnsserver.View{Zones: []dnsserver.Zone{{Apex: "example.com.", Records: []dns.RR{rr}}}}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upstream.Close() }()

	srv, err := startAzureDNSServer(azureDNSConfig{
		ListenAddr: "127.0.0.1:0",
		TargetIPv4: net.ParseIP("127.0.0.1"),
		Zones:      []string{"sockerless.azure.local"},
		TTL:        30,
		Upstreams:  upstream.Addr(),
	})
	if err != nil {
		t.Fatalf("start DNS server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	addr := srv.Addr()
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn("account.blob.example.com"), dns.TypeA)
	resp, _, err := (&dns.Client{Net: "udp", Timeout: 5 * time.Second}).Exchange(msg, addr)
	if err != nil {
		t.Fatalf("query outside zone: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("outside-zone answer = %v", resp)
	}
}

// A name a data plane registers answers ahead of every zone.
func TestAzureDNSServerServesPublicZonesAndRegisteredNames(t *testing.T) {
	zones, sets := azurePublicDNSZones, azurePublicDNSRecordSets
	t.Cleanup(func() { azurePublicDNSZones, azurePublicDNSRecordSets = zones, sets })
	azurePublicDNSZones = sim.MakeStore[PublicDnsZone](nil, "public_dns_zones")
	azurePublicDNSRecordSets = sim.MakeStore[PublicRecordSet](nil, "public_dns_record_sets")
	zoneID := publicDNSZoneID("sub", "rg", "contoso.test")
	azurePublicDNSZones.Put(zoneID, PublicDnsZone{ID: zoneID, Name: "contoso.test"})
	createPublicDNSDefaultRecords(azurePublicDNSRecordSets, zoneID, "contoso.test")
	for _, rs := range []PublicRecordSet{
		{Name: "www", Type: "A", Properties: PublicRecordSetProperties{TTL: 60, ARecords: []ARecord{{IPv4Address: "192.0.2.8"}}}},
		{Name: "alias", Type: "CNAME", Properties: PublicRecordSetProperties{TTL: 60, CNAMERecord: &CNAMERecord{CName: "www.contoso.test"}}},
		{Name: "@", Type: "TXT", Properties: PublicRecordSetProperties{TTL: 60, TXTRecords: []TXTRecord{{Value: []string{"v=spf1 -all"}}}}},
	} {
		id := publicDNSRecordID(zoneID, rs.Type, rs.Name)
		rs.ID = id
		azurePublicDNSRecordSets.Put(id, rs)
	}
	RegisterAzureDNSName("server.postgres.database.azure.com", "127.0.0.9")
	t.Cleanup(func() { UnregisterAzureDNSName("server.postgres.database.azure.com") })

	srv, err := startAzureDNSServer(azureDNSConfig{
		ListenAddr: "127.0.0.1:0",
		TargetIPv4: net.ParseIP("127.0.0.1"),
		Zones:      []string{"sockerless.azure.local"},
		TTL:        30,
		Upstreams:  "127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	addr := srv.Addr()
	ask := func(name string, qtype uint16) *dns.Msg {
		t.Helper()
		msg := new(dns.Msg)
		msg.SetQuestion(dns.Fqdn(name), qtype)
		resp, _, err := (&dns.Client{Timeout: 5 * time.Second}).Exchange(msg, addr)
		if err != nil {
			t.Fatalf("query %s: %v", name, err)
		}
		return resp
	}
	if resp := ask("alias.contoso.test", dns.TypeA); len(resp.Answer) != 2 || resp.Answer[1].(*dns.A).A.String() != "192.0.2.8" {
		t.Fatalf("CNAME chain: %v", resp.Answer)
	}
	if resp := ask("contoso.test", dns.TypeTXT); len(resp.Answer) != 1 || resp.Answer[0].(*dns.TXT).Txt[0] != "v=spf1 -all" {
		t.Fatalf("TXT: %v", resp.Answer)
	}
	if resp := ask("contoso.test", dns.TypeNS); len(resp.Answer) != 4 {
		t.Fatalf("NS: %v", resp.Answer)
	}
	resp := ask("missing.contoso.test", dns.TypeA)
	if resp.Rcode != dns.RcodeNameError || len(resp.Ns) != 1 || resp.Ns[0].(*dns.SOA).Minttl != 300 {
		t.Fatalf("NXDOMAIN with the zone's SOA: %v", resp)
	}
	if resp := ask("server.postgres.database.azure.com", dns.TypeA); len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "127.0.0.9" {
		t.Fatalf("registered name: %v", resp.Answer)
	}
}

func TestParseAzureDNSZonesRejectsEmptyConfig(t *testing.T) {
	if _, err := parseAzureDNSZones(" , "); err == nil {
		t.Fatal("expected empty zone config to fail")
	}
}

// A query comes from the virtual network whose address space holds the
// querier's address.
func TestAzureDNSServerServesPrivateZonesToLinkedVirtualNetworks(t *testing.T) {
	pubZones, pubSets := azurePublicDNSZones, azurePublicDNSRecordSets
	zones, sets, links := azurePrivateDNSZones, azurePrivateDNSRecordSets, azurePrivateDNSVNetLinks
	vnets := azureVnets
	t.Cleanup(func() {
		azurePublicDNSZones, azurePublicDNSRecordSets = pubZones, pubSets
		azurePrivateDNSZones, azurePrivateDNSRecordSets, azurePrivateDNSVNetLinks = zones, sets, links
		azureVnets = vnets
	})
	azureVnets = sim.MakeStore[VirtualNetwork](nil, "test_vnets")
	azurePublicDNSZones = sim.MakeStore[PublicDnsZone](nil, "public_dns_zones")
	azurePublicDNSRecordSets = sim.MakeStore[PublicRecordSet](nil, "public_dns_record_sets")
	azurePrivateDNSZones = sim.MakeStore[PrivateDnsZone](nil, "dns_zones")
	azurePrivateDNSRecordSets = sim.MakeStore[RecordSet](nil, "dns_record_sets")
	azurePrivateDNSVNetLinks = sim.MakeStore[VNetLink](nil, "dns_vnet_links")

	publicID := publicDNSZoneID("sub", "rg", "contoso.test")
	azurePublicDNSZones.Put(publicID, PublicDnsZone{ID: publicID, Name: "contoso.test"})
	createPublicDNSDefaultRecords(azurePublicDNSRecordSets, publicID, "contoso.test")
	publicA := publicDNSRecordID(publicID, "A", "db")
	azurePublicDNSRecordSets.Put(publicA, PublicRecordSet{ID: publicA, Name: "db", Type: "A",
		Properties: PublicRecordSetProperties{TTL: 60, ARecords: []ARecord{{IPv4Address: "203.0.113.5"}}}})

	privateID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/privateDnsZones/contoso.test"
	azurePrivateDNSZones.Put(privateID, PrivateDnsZone{ID: privateID, Name: "contoso.test"})
	azurePrivateDNSRecordSets.Put(privateID+"/SOA/@", RecordSet{ID: privateID + "/SOA/@", Name: "@", Properties: RecordSetProperties{TTL: 3600,
		SOARecord: &SOARecord{Host: "azureprivatedns.net", Email: "azureprivatedns-host.microsoft.com", SerialNumber: 1, RefreshTime: 3600, RetryTime: 300, ExpireTime: 2419200, MinimumTTL: 10}}})
	azurePrivateDNSRecordSets.Put(privateID+"/A/db", RecordSet{ID: privateID + "/A/db", Name: "db",
		Properties: RecordSetProperties{TTL: 10, ARecords: []ARecord{{IPv4Address: "10.0.0.4"}}}})
	linkedVNet := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/linked"
	azurePrivateDNSVNetLinks.Put(privateID+"/virtualNetworkLinks/l", VNetLink{ID: privateID + "/virtualNetworkLinks/l",
		Properties: VNetLinkProperties{VirtualNetwork: &VNetLinkVNet{ID: linkedVNet}}})

	srv, err := startAzureDNSServer(azureDNSConfig{
		ListenAddr: "127.0.0.1:0",
		TargetIPv4: net.ParseIP("127.0.0.1"),
		Zones:      []string{"sockerless.azure.local"},
		TTL:        30,
		Upstreams:  "127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ask := func(name string, qtype uint16) *dns.Msg {
		t.Helper()
		msg := new(dns.Msg)
		msg.SetQuestion(dns.Fqdn(name), qtype)
		resp, _, err := (&dns.Client{Timeout: 5 * time.Second}).Exchange(msg, srv.Addr())
		if err != nil {
			t.Fatalf("query %s: %v", name, err)
		}
		return resp
	}

	if resp := ask("db.contoso.test", dns.TypeA); len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "203.0.113.5" {
		t.Fatalf("outside every virtual network the public zone answers: %v", resp.Answer)
	}
	azureVnets.Put(linkedVNet, VirtualNetwork{ID: linkedVNet, Name: "linked",
		Properties: VNetProperties{AddressSpace: AddressSpace{AddressPrefixes: []string{"127.0.0.0/8"}}}})
	if resp := ask("db.contoso.test", dns.TypeA); len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "10.0.0.4" {
		t.Fatalf("the linked virtual network sees the private zone: %v", resp.Answer)
	}
	if resp := ask("missing.contoso.test", dns.TypeA); resp.Rcode != dns.RcodeNameError || len(resp.Ns) != 1 || resp.Ns[0].(*dns.SOA).Minttl != 10 {
		t.Fatalf("NXDOMAIN with the private zone's SOA: %v", resp)
	}
	azureVnets.Delete(linkedVNet)
	otherVNet := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/other"
	azureVnets.Put(otherVNet, VirtualNetwork{ID: otherVNet, Name: "other",
		Properties: VNetProperties{AddressSpace: AddressSpace{AddressPrefixes: []string{"127.0.0.0/8"}}}})
	if resp := ask("db.contoso.test", dns.TypeA); len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "203.0.113.5" {
		t.Fatalf("an unlinked virtual network sees only the public zone: %v", resp.Answer)
	}
}
