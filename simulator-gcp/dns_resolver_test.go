package main

import (
	"encoding/json"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dnsserver"
	"github.com/miekg/dns"
)

func putCloudDNSZone(t *testing.T, project string, zone ManagedZone, records ...ResourceRecordSet) {
	t.Helper()
	zone.NameServers = cloudDNSNameServers(zone)
	cloudDNSZones.Put(project+"/"+zone.Name, storedManagedZone{ManagedZone: zone})
	for _, rs := range append(cloudDNSApexRecordSets(zone), records...) {
		cloudDNSRecordSets.Put(dnsRecordSetKey(project, zone.Name, rs.Name, rs.Type), storedResourceRecordSet{Project: project, Zone: zone.Name, Record: rs})
	}
}

// Cloud DNS answers a public zone for any querier, negative answers carrying
// the zone's SOA; a private zone answers only inside its networks.
func TestCloudDNSServesPublicZonesOutsideEveryNetwork(t *testing.T) {
	zones, sets := cloudDNSZones, cloudDNSRecordSets
	t.Cleanup(func() { cloudDNSZones, cloudDNSRecordSets = zones, sets })
	cloudDNSZones = sim.MakeStore[storedManagedZone](nil, "dns_zones")
	cloudDNSRecordSets = sim.MakeStore[storedResourceRecordSet](nil, "dns_record_sets")
	putCloudDNSZone(t, "p", ManagedZone{Name: "pub", DNSName: "pub.test.", ID: "7", Visibility: "public"},
		ResourceRecordSet{Name: "www.pub.test.", Type: "A", TTL: 60, Rrdatas: []string{"192.0.2.1"}})
	putCloudDNSZone(t, "p", ManagedZone{Name: "pub-sub", DNSName: "sub.pub.test.", ID: "8", Visibility: "public"},
		ResourceRecordSet{Name: "www.sub.pub.test.", Type: "A", TTL: 60, Rrdatas: []string{"192.0.2.2"}})
	putCloudDNSZone(t, "p", ManagedZone{Name: "priv", DNSName: "priv.test.", ID: "9", Visibility: "private"},
		ResourceRecordSet{Name: "db.priv.test.", Type: "A", TTL: 60, Rrdatas: []string{"10.0.0.1"}})

	srv, err := dnsserver.Start(dnsserver.Config{Host: "127.0.0.1", Catalog: cloudDNSCatalog, Name: "cloud dns test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ask := func(name string, qtype uint16) *dns.Msg {
		t.Helper()
		req := new(dns.Msg)
		req.SetQuestion(name, qtype)
		resp, _, err := (&dns.Client{Timeout: 5 * time.Second}).Exchange(req, srv.Addr())
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := ask("www.pub.test.", dns.TypeA); len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "192.0.2.1" {
		t.Fatalf("public record: %v", resp)
	}
	if resp := ask("www.sub.pub.test.", dns.TypeA); len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "192.0.2.2" {
		t.Fatalf("the more specific zone answers its names: %v", resp)
	}
	resp := ask("missing.pub.test.", dns.TypeA)
	if resp.Rcode != dns.RcodeNameError || len(resp.Ns) != 1 {
		t.Fatalf("NXDOMAIN with the SOA: %v", resp)
	}
	if soa := resp.Ns[0].(*dns.SOA); soa.Ns != "ns-cloud-c1.googledomains.com." || soa.Minttl != 300 {
		t.Fatalf("SOA: %v", soa)
	}
	if resp := ask("db.priv.test.", dns.TypeA); resp.Rcode != dns.RcodeServerFailure || len(resp.Answer) != 0 {
		t.Fatalf("a private zone is not served here: %v", resp)
	}
}

func TestCloudDNSNameServers(t *testing.T) {
	if got := cloudDNSNameServers(ManagedZone{ID: "12", Visibility: "public"}); len(got) != 4 || got[0] != "ns-cloud-c1.googledomains.com." || got[3] != "ns-cloud-c4.googledomains.com." {
		t.Fatalf("public: %v", got)
	}
	if got := cloudDNSNameServers(ManagedZone{ID: "12", Visibility: "private"}); len(got) != 1 || got[0] != cloudDNSPrivateNameServer {
		t.Fatalf("private: %v", got)
	}
}

// A private zone answers the networks it is visible to, ahead of a public zone
// of the same name; a forwarding zone hands its names to its target name
// servers, and a peering zone answers from the zones its target network sees.
func TestCloudDNSServesPrivateZonesToTheirNetworks(t *testing.T) {
	zones, sets := cloudDNSZones, cloudDNSRecordSets
	t.Cleanup(func() {
		cloudDNSZones, cloudDNSRecordSets = zones, sets
		cloudDNSResolverNetworks.Delete(netip.MustParseAddr("127.0.0.1"))
	})
	cloudDNSZones = sim.MakeStore[storedManagedZone](nil, "dns_zones")
	cloudDNSRecordSets = sim.MakeStore[storedResourceRecordSet](nil, "dns_record_sets")
	visibleTo := func(networks ...string) map[string]any {
		var list []any
		for _, n := range networks {
			list = append(list, map[string]any{"networkUrl": n})
		}
		return map[string]any{"networks": list}
	}

	putCloudDNSZone(t, "p", ManagedZone{Name: "pub", DNSName: "shared.test.", ID: "1", Visibility: "public"},
		ResourceRecordSet{Name: "db.shared.test.", Type: "A", TTL: 60, Rrdatas: []string{"203.0.113.1"}})
	putCloudDNSZone(t, "p", ManagedZone{Name: "priv", DNSName: "shared.test.", ID: "2", Visibility: "private",
		PrivateVisibilityConfig: visibleTo("https://www.googleapis.com/compute/v1/projects/p/global/networks/a")},
		ResourceRecordSet{Name: "db.shared.test.", Type: "A", TTL: 60, Rrdatas: []string{"10.0.0.2"}})
	putCloudDNSZone(t, "p", ManagedZone{Name: "hub", DNSName: "hub.test.", ID: "3", Visibility: "private",
		PrivateVisibilityConfig: visibleTo("projects/p/global/networks/hub")},
		ResourceRecordSet{Name: "svc.hub.test.", Type: "A", TTL: 60, Rrdatas: []string{"10.9.0.9"}})
	peering, _ := json.Marshal(map[string]any{"targetNetwork": map[string]any{"networkUrl": "projects/p/global/networks/hub"}})
	putCloudDNSZone(t, "p", ManagedZone{Name: "to-hub", DNSName: "hub.test.", ID: "4", Visibility: "private",
		PrivateVisibilityConfig: visibleTo("projects/p/global/networks/a"), PeeringConfig: peering})
	forwarding, _ := json.Marshal(map[string]any{"targetNameServers": []any{map[string]any{"ipv4Address": "10.1.0.2"}}})
	putCloudDNSZone(t, "p", ManagedZone{Name: "corp", DNSName: "corp.example.", ID: "5", Visibility: "private",
		PrivateVisibilityConfig: visibleTo("projects/p/global/networks/a"), ForwardingConfig: forwarding})

	srv, err := dnsserver.Start(dnsserver.Config{Host: "127.0.0.1", Catalog: cloudDNSCatalog, Name: "cloud dns test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ask := func(name string) *dns.Msg {
		t.Helper()
		req := new(dns.Msg)
		req.SetQuestion(name, dns.TypeA)
		resp, _, err := (&dns.Client{Timeout: 5 * time.Second}).Exchange(req, srv.Addr())
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	answer := func(resp *dns.Msg) string {
		if len(resp.Answer) != 1 {
			return dns.RcodeToString[resp.Rcode]
		}
		return resp.Answer[0].(*dns.A).A.String()
	}

	if got := answer(ask("db.shared.test.")); got != "203.0.113.1" {
		t.Fatalf("outside every network the public zone answers, got %s", got)
	}
	if err := cloudDNSRegisterResolverIP(net.ParseIP("127.0.0.1"), "projects/p/global/networks/a"); err != nil {
		t.Fatal(err)
	}
	if got := answer(ask("db.shared.test.")); got != "10.0.0.2" {
		t.Fatalf("network a sees its private zone, got %s", got)
	}
	if got := answer(ask("svc.hub.test.")); got != "10.9.0.9" {
		t.Fatalf("the peering zone answers from the hub network's zones, got %s", got)
	}
	var corp *dnsserver.Zone
	for _, z := range cloudDNSCatalog(dnsserver.Query{Local: netip.MustParseAddr("127.0.0.1")}).Zones {
		if z.Apex == "corp.example." {
			corp = &z
		}
	}
	if corp == nil || len(corp.Forwarders) != 1 || corp.Forwarders[0] != "10.1.0.2:53" {
		t.Fatalf("network a sees the forwarding zone with its target: %+v", corp)
	}
	if err := cloudDNSRegisterResolverIP(net.ParseIP("127.0.0.1"), "projects/p/global/networks/b"); err != nil {
		t.Fatal(err)
	}
	if got := answer(ask("db.shared.test.")); got != "203.0.113.1" {
		t.Fatalf("network b sees only the public zone, got %s", got)
	}
}

func TestCloudDNSForwarders(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"targetNameServers": []any{
		map[string]any{"ipv4Address": "10.1.0.2", "forwardingPath": "private"},
		map[string]any{"ipv4Address": "10.1.0.3"},
	}})
	got := cloudDNSForwarders(ManagedZone{ForwardingConfig: cfg})
	if len(got) != 2 || got[0] != "10.1.0.2:53" || got[1] != "10.1.0.3:53" {
		t.Fatalf("forwarders = %v", got)
	}
}
