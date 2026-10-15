package main

import (
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
// the zone's SOA; a private zone answers only inside its networks, none of
// which reach this listener.
func TestCloudDNSServesPublicZonesOnly(t *testing.T) {
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
