package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dnsserver"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
	"github.com/miekg/dns"
)

var (
	cloudDNSZones               sim.Store[storedManagedZone]
	cloudDNSRecordSets          sim.Store[storedResourceRecordSet]
	cloudDNSPolicies            sim.Store[storedDNSPolicy]
	cloudDNSResponsePolicies    sim.Store[storedDNSResponsePolicy]
	cloudDNSResponsePolicyRules sim.Store[storedDNSResponsePolicyRule]
)

// cloudDNSPrivateNameServer is the name server every private zone's NS and
// SOA name.
const cloudDNSPrivateNameServer = "ns-gcp-private.googledomains.com."

// cloudDNSNameServers are the name servers Cloud DNS assigns a zone: four
// ns-cloud-<shard><n>.googledomains.com. servers for a public zone, picked by
// the zone's ID, and one for a private zone.
func cloudDNSNameServers(zone ManagedZone) []string {
	if zone.Visibility == "private" {
		return []string{cloudDNSPrivateNameServer}
	}
	id, _ := strconv.ParseUint(zone.ID, 10, 64)
	shard := string(rune('a' + id%5))
	out := make([]string, 0, 4)
	for n := 1; n <= 4; n++ {
		out = append(out, fmt.Sprintf("ns-cloud-%s%d.googledomains.com.", shard, n))
	}
	return out
}

// cloudDNSApexTTL is the TTL Cloud DNS gives a new zone's NS and SOA.
const cloudDNSApexTTL = 21600

// cloudDNSApexRecordSets are the NS and SOA record sets every new managed zone
// starts with.
func cloudDNSApexRecordSets(zone ManagedZone) []ResourceRecordSet {
	apex := dns.Fqdn(zone.DNSName)
	return []ResourceRecordSet{
		{Name: apex, Type: "NS", TTL: cloudDNSApexTTL, Rrdatas: zone.NameServers},
		{Name: apex, Type: "SOA", TTL: cloudDNSApexTTL, Rrdatas: []string{
			zone.NameServers[0] + " cloud-dns-hostmaster.google.com. 1 21600 3600 259200 300",
		}},
	}
}

// cloudDNS is the listener Cloud DNS's name servers answer public zones on,
// and the resolver a VPC network's machines ask at the metadata server's
// address.
var cloudDNS *dnsserver.Server

// startCloudDNSServer serves public managed zones to any querier, and private,
// forwarding and peering zones to the VPC networks they are visible to.
func startCloudDNSServer() (*dnsserver.Server, error) {
	srv, err := dnsserver.Start(dnsserver.Config{
		Port:      os.Getenv("SIM_DNS_PORT"),
		Name:      "cloud dns",
		Catalog:   cloudDNSCatalog,
		Recursion: &dnsserver.Recursion{Upstreams: os.Getenv("SIM_DNS_UPSTREAM")},
	})
	if err != nil {
		return nil, err
	}
	cloudDNS = srv
	// An API-only simulator places no workload, so it runs no sidecar and
	// needs no callback address, which only a container engine can give.
	if sim.RequireContainerRuntime("starting network sidecars") != nil {
		return srv, nil
	}
	origin, err := workloadhost.CallbackAddr(simCallbackListenAddr)
	if err != nil {
		_ = srv.Close()
		return nil, fmt.Errorf("workload callback: %w", err)
	}
	networkSidecars = sidecar.NewManager(cloudDNS, "http://"+origin, sim.NoopSink{})
	return srv, nil
}

// networkSidecars runs one sidecar per engine network workloads land on,
// forwarding DNS to cloudDNS and metadata to the simulator.
var networkSidecars *sidecar.Manager

// cloudDNSResolverNetworks maps an engine network whose sidecar forwards
// queries here to the VPC network it realizes, named
// projects/<project>/global/networks/<name>, which decides the private zones a
// query through that sidecar sees.
var cloudDNSResolverNetworks sync.Map

// cloudDNSMetadataName is the name every network resolves to its metadata
// server: the sidecar that forwards to the simulator.
const cloudDNSMetadataName = "metadata.google.internal."

func cloudDNSCatalog(q dnsserver.Query) dnsserver.View {
	var network string
	if v, ok := cloudDNSResolverNetworks.Load(q.Network); ok {
		network, _ = v.(string)
	}
	all := cloudDNSZones.ListPrefix("")
	view := dnsserver.View{
		Zones: cloudDNSNetworkZones(all, network, true),
		Names: cloudDNSResponsePolicyAnswer(network, q.Name),
	}
	if q.Name == cloudDNSMetadataName {
		if sidecar, ok := gcpNetworkSidecar(q.Network); ok {
			view.Names = append(view.Names, &dns.A{
				Hdr: dns.RR_Header{Name: cloudDNSMetadataName, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 0},
				A:   net.IP(sidecar.AsSlice()),
			})
		}
	}
	// A network whose DNS policy names alternative name servers sends them
	// every name its private zones do not hold, in place of Google's
	// resolvers and the public zones they reach.
	if alternatives := cloudDNSAlternativeNameServers(network); len(alternatives) > 0 {
		view.Zones = append(view.Zones, dnsserver.Zone{Apex: ".", Forwarders: alternatives})
		return view
	}
	for _, zone := range all {
		if zone.Item.Visibility != "private" {
			view.Zones = append(view.Zones, cloudDNSServedZone(zone.ID, zone.Item.ManagedZone))
		}
	}
	return view
}

// cloudDNSPolicyNetworks reads the network URLs a policy binds, in the
// projects/<project>/global/networks/<name> form.
func cloudDNSPolicyNetworks(project string, networks []json.RawMessage) []string {
	var out []string
	for _, raw := range networks {
		var n struct {
			NetworkURL string `json:"networkUrl"`
		}
		if err := json.Unmarshal(raw, &n); err != nil {
			log.Printf("cloud dns: policy network %s: %v", raw, err)
			continue
		}
		if n.NetworkURL != "" {
			out = append(out, normalizeComputeGlobalNetworkRef(project, n.NetworkURL))
		}
	}
	return out
}

func cloudDNSAlternativeNameServers(network string) []string {
	if network == "" || cloudDNSPolicies == nil {
		return nil
	}
	for _, stored := range cloudDNSPolicies.List() {
		if !slices.Contains(cloudDNSPolicyNetworks(stored.Project, stored.Policy.Networks), network) || len(stored.Policy.AlternativeNameServerConfig) == 0 {
			continue
		}
		var cfg struct {
			TargetNameServers []struct {
				IPv4Address string `json:"ipv4Address"`
			} `json:"targetNameServers"`
		}
		if err := json.Unmarshal(stored.Policy.AlternativeNameServerConfig, &cfg); err != nil {
			log.Printf("cloud dns: alternative name servers of policy %s: %v", stored.Policy.Name, err)
			continue
		}
		var out []string
		for _, target := range cfg.TargetNameServers {
			if target.IPv4Address != "" {
				out = append(out, net.JoinHostPort(target.IPv4Address, "53"))
			}
		}
		return out
	}
	return nil
}

// cloudDNSResponsePolicyAnswer is the local data the response policies bound
// to network answer name with. The most specific rule decides — an exact name
// over a wildcard, a longer wildcard over a shorter one — and a rule whose
// behavior is bypassResponsePolicy leaves the name to ordinary resolution.
func cloudDNSResponsePolicyAnswer(network, name string) []dns.RR {
	if network == "" || cloudDNSResponsePolicies == nil {
		return nil
	}
	var best *DNSResponsePolicyRule
	bestLabels := -1
	for _, policy := range cloudDNSResponsePolicies.List() {
		if !slices.Contains(cloudDNSPolicyNetworks(policy.Project, policy.Policy.Networks), network) {
			continue
		}
		for _, stored := range cloudDNSResponsePolicyRules.ListPrefix(dnsResponsePolicyRuleKey(policy.Project, policy.Policy.ResponsePolicyName, "")) {
			rule := stored.Item.Rule
			labels, ok := cloudDNSRuleMatch(dns.CanonicalName(rule.DNSName), name)
			if ok && labels > bestLabels {
				best, bestLabels = &rule, labels
			}
		}
	}
	if best == nil || best.Behavior == "bypassResponsePolicy" || len(best.LocalData) == 0 {
		return nil
	}
	var data struct {
		LocalDatas []ResourceRecordSet `json:"localDatas"`
	}
	if err := json.Unmarshal(best.LocalData, &data); err != nil {
		log.Printf("cloud dns: local data of rule %s: %v", best.RuleName, err)
		return nil
	}
	var out []dns.RR
	for _, set := range data.LocalDatas {
		for _, rr := range cloudDNSRecordSetRRs(set) {
			rr.Header().Name = name
			out = append(out, rr)
		}
	}
	return out
}

// cloudDNSRuleMatch reports whether a rule's DNS name covers name, and how
// specific the match is: an exact name outranks every wildcard.
func cloudDNSRuleMatch(rule, name string) (int, bool) {
	if rule == name {
		return math.MaxInt, true
	}
	if suffix, ok := strings.CutPrefix(rule, "*."); ok && dns.IsSubDomain(suffix, name) && name != suffix {
		return dns.CountLabel(suffix), true
	}
	return 0, false
}

// cloudDNSNetworkZones lists the private zones visible to network, which take
// precedence over public zones of the same name. A peering zone answers its
// names from the zones visible to its target network, one hop deep, as Cloud
// DNS does not chain peering.
func cloudDNSNetworkZones(all []sim.Keyed[storedManagedZone], network string, followPeering bool) []dnsserver.Zone {
	if network == "" {
		return nil
	}
	var zones []dnsserver.Zone
	for _, stored := range all {
		zone := stored.Item.ManagedZone
		project, _, _ := strings.Cut(stored.ID, "/")
		if zone.Visibility != "private" || !cloudDNSVisibleTo(zone, project, network) {
			continue
		}
		if target := cloudDNSPeeringTarget(zone, project); target != "" {
			if !followPeering {
				continue
			}
			apex := dns.Fqdn(zone.DNSName)
			for _, peered := range cloudDNSNetworkZones(all, target, false) {
				if dns.IsSubDomain(apex, dns.CanonicalName(peered.Apex)) {
					zones = append(zones, peered)
				}
			}
			continue
		}
		zones = append(zones, cloudDNSServedZone(stored.ID, zone))
	}
	return zones
}

func cloudDNSServedZone(key string, zone ManagedZone) dnsserver.Zone {
	served := dnsserver.Zone{Apex: dns.Fqdn(zone.DNSName), Forwarders: cloudDNSForwarders(zone)}
	if len(served.Forwarders) > 0 {
		return served
	}
	for _, set := range cloudDNSRecordSets.ListPrefix(key + ":") {
		served.Records = append(served.Records, cloudDNSRecordSetRRs(set.Item.Record)...)
	}
	return served
}

func cloudDNSVisibleTo(zone ManagedZone, project, network string) bool {
	networks, _ := zone.PrivateVisibilityConfig["networks"].([]any)
	for _, n := range networks {
		entry, _ := n.(map[string]any)
		url, _ := entry["networkUrl"].(string)
		if url != "" && normalizeComputeGlobalNetworkRef(project, url) == network {
			return true
		}
	}
	return false
}

// cloudDNSForwarders reads a forwarding zone's target name servers; Cloud DNS
// forwards to each on port 53.
func cloudDNSForwarders(zone ManagedZone) []string {
	if len(zone.ForwardingConfig) == 0 {
		return nil
	}
	var cfg struct {
		TargetNameServers []struct {
			IPv4Address string `json:"ipv4Address"`
		} `json:"targetNameServers"`
	}
	if err := json.Unmarshal(zone.ForwardingConfig, &cfg); err != nil {
		log.Printf("cloud dns: forwarding config of %s: %v", zone.Name, err)
		return nil
	}
	var out []string
	for _, target := range cfg.TargetNameServers {
		if target.IPv4Address != "" {
			out = append(out, net.JoinHostPort(target.IPv4Address, "53"))
		}
	}
	return out
}

func cloudDNSPeeringTarget(zone ManagedZone, project string) string {
	if len(zone.PeeringConfig) == 0 {
		return ""
	}
	var cfg struct {
		TargetNetwork struct {
			NetworkURL string `json:"networkUrl"`
		} `json:"targetNetwork"`
	}
	if err := json.Unmarshal(zone.PeeringConfig, &cfg); err != nil {
		log.Printf("cloud dns: peering config of %s: %v", zone.Name, err)
		return ""
	}
	if cfg.TargetNetwork.NetworkURL == "" {
		return ""
	}
	return normalizeComputeGlobalNetworkRef(project, cfg.TargetNetwork.NetworkURL)
}

// cloudDNSRecordSetRRs parses a record set's rrdatas, which Cloud DNS holds in
// presentation format.
func cloudDNSRecordSetRRs(set ResourceRecordSet) []dns.RR {
	var out []dns.RR
	for _, data := range set.Rrdatas {
		rr, err := dns.NewRR(fmt.Sprintf("%s %d IN %s %s", dns.Fqdn(set.Name), set.TTL, strings.ToUpper(set.Type), data))
		if err != nil || rr == nil {
			log.Printf("cloud dns: serve %s %s %q: %v", set.Name, set.Type, data, err)
			continue
		}
		out = append(out, rr)
	}
	return out
}
