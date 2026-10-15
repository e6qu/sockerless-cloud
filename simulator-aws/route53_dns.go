package main

import (
	"fmt"
	"log"
	"net/netip"
	"os"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim/dnsserver"
	"github.com/miekg/dns"
)

// Route 53 is the authoritative DNS server for its hosted zones, and inside a
// VPC it is also the resolver: a query arriving through a VPC's resolver
// address sees that VPC's private zones as well as the public ones, and a name
// outside every zone is recursed for, as AmazonProvidedDNS does. Each query
// reads r53Zones directly, so `dig` returns what ChangeResourceRecordSets wrote.

var (
	r53DNS     *dnsserver.Server
	r53DNSOnce sync.Once
	// r53ResolverVPCs maps the address a VPC's redirected queries arrive at to
	// the VPC, which decides the private zones a query sees.
	r53ResolverVPCs sync.Map
)

// vpcResolverAddr is the link-local address tasks reach the resolver through.
var vpcResolverAddr = netip.MustParseAddr("169.254.169.253")

// route53UpstreamOverride names the resolvers to recurse through, host[:port]
// comma-separated; empty reads the host's resolv.conf.
const route53UpstreamOverride = "SIM_DNS_UPSTREAM"

func startRoute53DNSServer() {
	r53DNSOnce.Do(func() {
		srv, err := dnsserver.Start(route53DNSConfig("", route53DNSBindPort()))
		if err != nil {
			panic("route53 dns: " + err.Error())
		}
		r53DNS = srv
	})
}

func route53DNSConfig(host, port string) dnsserver.Config {
	return dnsserver.Config{
		Host:    host,
		Port:    port,
		Catalog: route53Catalog,
		Name:    "route53",
		Recursion: &dnsserver.Recursion{
			Upstreams:  os.Getenv(route53UpstreamOverride),
			Redirected: []netip.Addr{vpcResolverAddr},
		},
	}
}

// route53DNSPort is the port the resolver answers on, which a workload
// namespace's DNS is redirected to.
func route53DNSPort() (int, error) {
	if r53DNS == nil {
		return 0, fmt.Errorf("the simulator's resolver is not listening yet")
	}
	return r53DNS.Port(), nil
}

// route53RegisterResolverAddress records that queries arriving at addr come
// from vpcID.
func route53RegisterResolverAddress(addr netip.Addr, vpcID string) {
	r53ResolverVPCs.Store(addr.Unmap(), vpcID)
}

func route53Catalog(q dnsserver.Query) dnsserver.View {
	var vpcID string
	if v, ok := r53ResolverVPCs.Load(q.Local); ok {
		vpcID, _ = v.(string)
	}
	view := dnsserver.View{Names: workloadHostAliasRecords()}
	zones := route53VisibleZones(vpcID)
	for _, z := range zones {
		view.Zones = append(view.Zones, dnsserver.Zone{Apex: dns.Fqdn(z.Zone.Name), Records: route53ZoneRecords(z, zones)})
	}
	return view
}

// route53VisibleZones lists the zones a query from vpcID sees, private ones
// first: inside a VPC a private zone takes precedence over a public zone of
// the same name.
func route53VisibleZones(vpcID string) []r53StoredZone {
	var private, public []r53StoredZone
	for _, z := range r53Zones.List() {
		if z.Zone.Config == nil || !z.Zone.Config.PrivateZone {
			public = append(public, z)
			continue
		}
		if vpcID == "" {
			continue
		}
		vpcs, _ := r53ZoneVPCs.Get(r53ZoneIDFromPath(z.Zone.Id))
		for _, v := range vpcs {
			if v.VPCId == vpcID {
				private = append(private, z)
				break
			}
		}
	}
	return append(private, public...)
}

// route53DefaultTTL is the TTL a record set without one is served with.
const route53DefaultTTL = 300

// route53MaxAliasDepth bounds an alias chain, which records can make circular.
const route53MaxAliasDepth = 8

func route53ZoneRecords(zone r53StoredZone, visible []r53StoredZone) []dns.RR {
	var out []dns.RR
	for _, set := range zone.Records {
		owner := route53OwnerName(set.Name)
		if set.AliasTarget != nil && set.AliasTarget.DNSName != "" {
			for _, rr := range route53AliasRecords(visible, route53OwnerName(set.AliasTarget.DNSName), set.Type, 0) {
				cp := dns.Copy(rr)
				cp.Header().Name = owner
				out = append(out, cp)
			}
			continue
		}
		out = append(out, route53SetRecords(owner, set)...)
	}
	return out
}

// route53AliasRecords returns the records an alias to target serves: the
// target's own record set of the same type, which Route 53 answers with the
// target's TTL.
func route53AliasRecords(visible []r53StoredZone, target, rtype string, depth int) []dns.RR {
	if depth >= route53MaxAliasDepth {
		return nil
	}
	var zone *r53StoredZone
	for i := range visible {
		apex := dns.Fqdn(strings.ToLower(visible[i].Zone.Name))
		if dns.IsSubDomain(apex, target) && (zone == nil || dns.CountLabel(apex) > dns.CountLabel(dns.Fqdn(zone.Zone.Name))) {
			zone = &visible[i]
		}
	}
	if zone == nil {
		return nil
	}
	var out []dns.RR
	for _, set := range zone.Records {
		if route53OwnerName(set.Name) != target || !strings.EqualFold(set.Type, rtype) {
			continue
		}
		if set.AliasTarget != nil && set.AliasTarget.DNSName != "" {
			out = append(out, route53AliasRecords(visible, route53OwnerName(set.AliasTarget.DNSName), rtype, depth+1)...)
			continue
		}
		out = append(out, route53SetRecords(target, set)...)
	}
	return out
}

// route53SetRecords parses a record set's values, which Route 53 holds in
// presentation format.
func route53SetRecords(owner string, set R53ResourceRecordSet) []dns.RR {
	if set.ResourceRecords == nil {
		return nil
	}
	ttl := int64(route53DefaultTTL)
	if set.TTL != nil {
		ttl = *set.TTL
	}
	var out []dns.RR
	for _, rec := range set.ResourceRecords.Items {
		rr, err := dns.NewRR(fmt.Sprintf("%s %d IN %s %s", owner, ttl, strings.ToUpper(set.Type), rec.Value))
		if err != nil || rr == nil {
			log.Printf("route53 dns: serve %s %s %q: %v", owner, set.Type, rec.Value, err)
			continue
		}
		out = append(out, rr)
	}
	return out
}

// route53OwnerName is a record name as DNS compares it. Route 53 lists "*" as
// the octal escape \052.
func route53OwnerName(name string) string {
	return dns.CanonicalName(strings.ReplaceAll(strings.TrimSpace(name), `\052`, "*"))
}

// route53DNSBindPort is the port the resolver binds: SIM_DNS_PORT when set,
// otherwise a kernel-chosen one. Nothing reads the resolver at a fixed port:
// workload namespaces are redirected to the bound address.
func route53DNSBindPort() string {
	return envOr("SIM_DNS_PORT", "0")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
