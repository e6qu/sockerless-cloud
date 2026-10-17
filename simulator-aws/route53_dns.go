package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dnsserver"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
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
	// r53ResolverVPCs maps the engine network whose sidecar forwarded a query
	// to the VPC, which decides the private zones the query sees.
	r53ResolverVPCs sync.Map
)

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

// networkSidecars runs one sidecar per engine network workloads land on, at
// the VPC's base-plus-two address the Route 53 Resolver answers on, forwarding
// DNS to r53DNS and metadata to the simulator. It exists once the first
// workload network needs it, so a simulator built without a listener of its
// own serves the control plane alone.
var (
	networkSidecars     *sidecar.Manager
	networkSidecarsErr  error
	networkSidecarsOnce sync.Once
)

func awsNetworkSidecars() (*sidecar.Manager, error) {
	networkSidecarsOnce.Do(func() {
		if r53DNS == nil {
			networkSidecarsErr = errNoNetworkSidecars
			return
		}
		origin, err := workloadhost.CallbackAddr(simListenAddr)
		if err != nil {
			networkSidecarsErr = fmt.Errorf("network sidecars: workload callback: %w", err)
			return
		}
		networkSidecars = sidecar.NewManager(r53DNS, "http://"+origin, sim.NoopSink{})
	})
	return networkSidecars, networkSidecarsErr
}

func route53DNSConfig(host, port string) dnsserver.Config {
	return dnsserver.Config{
		Host:    host,
		Port:    port,
		Catalog: route53Catalog,
		Name:    "route53",
		Recursion: &dnsserver.Recursion{
			Upstreams: os.Getenv(route53UpstreamOverride),
		},
	}
}

// route53RegisterResolverNetwork records that queries forwarded by the
// sidecar of the engine network come from vpcID.
func route53RegisterResolverNetwork(network, vpcID string) {
	r53ResolverVPCs.Store(network, vpcID)
}

// stopRoute53Sidecars removes every network sidecar at shutdown.
func stopRoute53Sidecars() {
	if networkSidecars != nil {
		networkSidecars.Close()
	}
}

func route53Catalog(q dnsserver.Query) dnsserver.View {
	var vpcID string
	if v, ok := r53ResolverVPCs.Load(q.Network); ok {
		vpcID, _ = v.(string)
	}
	resources := elbv2DNSRecords()
	view := dnsserver.View{Names: append(workloadHostAliasRecords(), resources...)}
	querier := route53Querier{region: route53QuerierRegion(vpcID), addr: q.Remote}
	zones := route53VisibleZones(vpcID)
	for i := range zones {
		zoneQuerier := querier
		zoneQuerier.healthy = r53LinkedServiceHealth(zones[i].Zone)
		zones[i].Records = route53Route(zones[i].Records, zoneQuerier)
	}
	for _, z := range zones {
		view.Zones = append(view.Zones, dnsserver.Zone{Apex: dns.Fqdn(z.Zone.Name), Records: route53ZoneRecords(z, zones, resources)})
	}
	return view
}

// route53QuerierRegion is the Region a query comes from: its VPC's, or the
// simulator's own for a query from outside every VPC.
func route53QuerierRegion(vpcID string) string {
	if vpcID != "" {
		for _, assoc := range r53ZoneVPCs.List() {
			for _, v := range assoc {
				if v.VPCId == vpcID && v.VPCRegion != "" {
					return v.VPCRegion
				}
			}
		}
	}
	return awsRegion()
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

func route53ZoneRecords(zone r53StoredZone, visible []r53StoredZone, resources []dns.RR) []dns.RR {
	var out []dns.RR
	for _, set := range zone.Records {
		owner := route53OwnerName(set.Name)
		if set.AliasTarget != nil && set.AliasTarget.DNSName != "" {
			for _, rr := range route53AliasRecords(visible, resources, route53OwnerName(set.AliasTarget.DNSName), set.Type, 0) {
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
// target's TTL, or, for a name outside every zone, the records of the AWS
// resource (resources) that answers to it, with the resource's TTL.
func route53AliasRecords(visible []r53StoredZone, resources []dns.RR, target, rtype string, depth int) []dns.RR {
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
		var out []dns.RR
		for _, rr := range resources {
			if rr.Header().Name == target && dns.TypeToString[rr.Header().Rrtype] == strings.ToUpper(rtype) {
				out = append(out, rr)
			}
		}
		return out
	}
	var out []dns.RR
	for _, set := range zone.Records {
		if route53OwnerName(set.Name) != target || !strings.EqualFold(set.Type, rtype) {
			continue
		}
		if set.AliasTarget != nil && set.AliasTarget.DNSName != "" {
			out = append(out, route53AliasRecords(visible, resources, route53OwnerName(set.AliasTarget.DNSName), rtype, depth+1)...)
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
	return sim.EnvOr("SIM_DNS_PORT", "0")
}
