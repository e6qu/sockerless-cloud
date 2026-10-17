package main

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/dnsserver"
	"github.com/miekg/dns"
)

// withRoute53Resolver gives the test its own Route 53 stores and a resolver
// serving them on loopback.
func withRoute53Resolver(t *testing.T) *dnsserver.Server {
	t.Helper()
	bg.Await()
	zones, vpcs, server := r53Zones, r53ZoneVPCs, r53DNS
	t.Cleanup(func() { r53Zones, r53ZoneVPCs, r53DNS = zones, vpcs, server })
	r53Zones = sim.MakeStore[r53StoredZone](nil, "route53_zones")
	r53ZoneVPCs = sim.MakeStore[[]R53VPC](nil, "route53_zone_vpcs")
	srv, err := dnsserver.Start(route53DNSConfig("127.0.0.1", "0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	r53DNS = srv
	return srv
}

func putRoute53Zone(t *testing.T, id, name string, private bool, vpcs []string, records ...R53ResourceRecordSet) {
	t.Helper()
	zone := r53StoredZone{
		Zone: R53HostedZone{Id: "/hostedzone/" + id, Name: name, Config: &R53HostedZoneConfig{PrivateZone: private}},
		Records: append([]R53ResourceRecordSet{{
			Name: name, Type: "SOA", TTL: new(int64(900)),
			ResourceRecords: &R53ResourceRecords{Items: []R53ResourceRecord{{Value: "ns-1.awsdns-00.com. awsdns-hostmaster.amazon.com. 1 7200 900 1209600 86400"}}},
		}}, records...),
	}
	r53Zones.Put(id, zone)
	var assoc []R53VPC
	for _, v := range vpcs {
		assoc = append(assoc, R53VPC{VPCId: v, VPCRegion: "us-east-1"})
	}
	if len(assoc) > 0 {
		r53ZoneVPCs.Put(id, assoc)
	}
}

func aSet(name string, ips ...string) R53ResourceRecordSet {
	var items []R53ResourceRecord
	for _, ip := range ips {
		items = append(items, R53ResourceRecord{Value: ip})
	}
	return R53ResourceRecordSet{Name: name, Type: "A", TTL: new(int64(60)), ResourceRecords: &R53ResourceRecords{Items: items}}
}

func askRoute53(t *testing.T, srv *dnsserver.Server, name string, qtype uint16) *dns.Msg {
	t.Helper()
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn(name), qtype)
	resp, _, err := (&dns.Client{Timeout: 10 * time.Second}).Exchange(req, srv.Addr())
	if err != nil {
		t.Fatalf("query %s: %v", name, err)
	}
	return resp
}

// askRoute53Network asks through the listener the resolver opens for an
// engine network, which is where a VPC's sidecar forwards its queries, with
// the network registered as the VPC's.
func askRoute53Network(t *testing.T, srv *dnsserver.Server, network, name string, qtype uint16) *dns.Msg {
	t.Helper()
	vpcID := strings.TrimSuffix(network, "-net")
	route53RegisterResolverNetwork(network, vpcID)
	t.Cleanup(func() { r53ResolverVPCs.Delete(network) })
	port, err := srv.ListenNetwork(network)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.CloseNetwork(network) })
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn(name), qtype)
	resp, _, err := (&dns.Client{Timeout: 10 * time.Second}).Exchange(req, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("query %s through %s: %v", name, network, err)
	}
	return resp
}

func onlyA(t *testing.T, resp *dns.Msg) []string {
	t.Helper()
	var out []string
	for _, rr := range resp.Answer {
		if a, ok := rr.(*dns.A); ok {
			out = append(out, a.A.String())
		}
	}
	return out
}

// A private hosted zone answers only inside the VPCs it is associated with:
// the same name asked from another VPC, or from outside every VPC, does not
// exist there.
func TestRoute53PrivateZonesAnswerOnlyTheirVPCs(t *testing.T) {
	t.Setenv(route53UpstreamOverride, "127.0.0.1:1")
	srv := withRoute53Resolver(t)
	putRoute53Zone(t, "ZPRIV", "internal.test.", true, []string{"vpc-1"}, aSet("db.internal.test.", "10.0.0.5"))

	if resp := askRoute53(t, srv, "db.internal.test", dns.TypeA); resp.Rcode != dns.RcodeServerFailure {
		t.Fatalf("outside every VPC the private zone is invisible and the name recursed for: %v", resp)
	}
	other := askRoute53Network(t, srv, "vpc-2-net", "db.internal.test", dns.TypeA)
	if other.Rcode != dns.RcodeServerFailure {
		t.Fatalf("another VPC does not see the zone: %v", other)
	}
	if got := onlyA(t, askRoute53Network(t, srv, "vpc-1-net", "db.internal.test", dns.TypeA)); len(got) != 1 || got[0] != "10.0.0.5" {
		t.Fatalf("the associated VPC resolves it: %v", got)
	}
}

// Inside an associated VPC a private zone takes precedence over a public zone
// of the same name.
func TestRoute53PrivateZoneShadowsThePublicOneInsideItsVPC(t *testing.T) {
	srv := withRoute53Resolver(t)
	putRoute53Zone(t, "ZPUB", "shared.test.", false, nil, aSet("www.shared.test.", "198.51.100.1"))
	putRoute53Zone(t, "ZPRIV", "shared.test.", true, []string{"vpc-1"}, aSet("www.shared.test.", "10.0.0.1"))

	if got := onlyA(t, askRoute53(t, srv, "www.shared.test", dns.TypeA)); len(got) != 1 || got[0] != "198.51.100.1" {
		t.Fatalf("outside the VPC: %v", got)
	}
	if got := onlyA(t, askRoute53Network(t, srv, "vpc-1-net", "www.shared.test", dns.TypeA)); len(got) != 1 || got[0] != "10.0.0.1" {
		t.Fatalf("inside the VPC: %v", got)
	}
}

// An alias record answers with its target's record set under its own name.
func TestRoute53AliasRecordsAnswerWithTheirTarget(t *testing.T) {
	srv := withRoute53Resolver(t)
	putRoute53Zone(t, "ZA", "alias.test.", false, nil,
		aSet("origin.alias.test.", "192.0.2.4", "192.0.2.5"),
		R53ResourceRecordSet{Name: "alias.test.", Type: "A", AliasTarget: &R53AliasTarget{DNSName: "origin.alias.test."}},
		R53ResourceRecordSet{Name: `\052.alias.test.`, Type: "A", AliasTarget: &R53AliasTarget{DNSName: "origin.alias.test."}},
	)
	resp := askRoute53(t, srv, "alias.test", dns.TypeA)
	if got := onlyA(t, resp); len(got) != 2 || resp.Answer[0].Header().Name != "alias.test." {
		t.Fatalf("got %v", resp.Answer)
	}
	if got := onlyA(t, askRoute53(t, srv, "any.alias.test", dns.TypeA)); len(got) != 2 {
		t.Fatalf("a wildcard alias listed with Route 53's escape: %v", got)
	}
	putRoute53Zone(t, "ZL", "loop.test.", false, nil,
		R53ResourceRecordSet{Name: "loop.loop.test.", Type: "A", AliasTarget: &R53AliasTarget{DNSName: "loop.loop.test."}})
	if resp := askRoute53(t, srv, "loop.loop.test", dns.TypeA); len(resp.Answer) != 0 {
		t.Fatalf("a circular alias answers nothing: %v", resp.Answer)
	}
}

// A task's only nameserver is this resolver, so a name outside every hosted
// zone is recursed for; answering NXDOMAIN instead left a task unable to reach
// a package registry.
func TestRoute53RecursesOutsideItsZones(t *testing.T) {
	upstream, err := dnsserver.Start(dnsserver.Config{Host: "127.0.0.1", Name: "upstream", Catalog: func(dnsserver.Query) dnsserver.View {
		rr, _ := dns.NewRR("registry.npmjs.org. 60 IN A 203.0.113.7")
		return dnsserver.View{Zones: []dnsserver.Zone{{Apex: "npmjs.org.", Records: []dns.RR{rr}}}}
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
	t.Setenv(route53UpstreamOverride, upstream.Addr())
	srv := withRoute53Resolver(t)

	resp := askRoute53(t, srv, "registry.npmjs.org", dns.TypeA)
	if got := onlyA(t, resp); len(got) != 1 || got[0] != "203.0.113.7" {
		t.Fatalf("got %v", resp)
	}
}

// The configured upstreams reach the recursion unchanged.
func TestRoute53RecursionTakesTheConfiguredUpstreams(t *testing.T) {
	t.Setenv(route53UpstreamOverride, "9.9.9.9")
	rec := route53DNSConfig("", "0").Recursion
	if rec == nil || rec.Upstreams != "9.9.9.9" {
		t.Fatalf("recursion = %+v", rec)
	}
}

// The container-host alias comes back from the resolver with the address a
// workload's /etc/hosts carries; a question for another type is NOERROR with
// no records, not a denial.
func TestRoute53AnswersTheWorkloadHostAlias(t *testing.T) {
	previous := workloadHostAliasEntries
	t.Cleanup(func() { workloadHostAliasEntries = previous })
	workloadHostAliasEntries = func() []sim.HostEntry {
		return []sim.HostEntry{{IP: "172.17.0.1", Name: "host.docker.internal"}, {IP: "172.17.0.1", Name: "host.containers.internal"}}
	}
	srv := withRoute53Resolver(t)
	for _, name := range []string{"host.docker.internal", "host.containers.internal"} {
		if got := onlyA(t, askRoute53(t, srv, name, dns.TypeA)); len(got) != 1 || got[0] != "172.17.0.1" {
			t.Fatalf("%s: %v", name, got)
		}
	}
	if resp := askRoute53(t, srv, "host.docker.internal", dns.TypeAAAA); resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 0 {
		t.Fatalf("AAAA: %v", resp)
	}
}
