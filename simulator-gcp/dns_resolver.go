package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/realexec"
	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dnsserver"
	"github.com/miekg/dns"
)

var (
	cloudDNSZones      sim.Store[storedManagedZone]
	cloudDNSRecordSets sim.Store[storedResourceRecordSet]
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
		Port:    os.Getenv("SIM_DNS_PORT"),
		Name:    "cloud dns",
		Catalog: cloudDNSCatalog,
		Recursion: &dnsserver.Recursion{
			Upstreams:  os.Getenv("SIM_DNS_UPSTREAM"),
			Redirected: []netip.Addr{netip.MustParseAddr(realexec.MetadataIPv4)},
		},
	})
	if err != nil {
		return nil, err
	}
	cloudDNS = srv
	return srv, nil
}

// cloudDNSPort is the port the resolver answers on, which a VPC network's DNS
// is redirected to.
func cloudDNSPort() (int, error) {
	if cloudDNS == nil {
		return 0, fmt.Errorf("the simulator's resolver is not listening yet")
	}
	return cloudDNS.Port(), nil
}

// cloudDNSResolverNetworks maps the address a VPC network's redirected queries
// arrive at to the network, which decides the private zones a query sees.
var cloudDNSResolverNetworks sync.Map

// cloudDNSRegisterResolverIP records that queries arriving at addr come from
// network, named projects/<project>/global/networks/<name>.
func cloudDNSRegisterResolverIP(addr net.IP, network string) error {
	local, ok := netip.AddrFromSlice(addr.To4())
	if !ok {
		return fmt.Errorf("resolver address %s of %s is not IPv4", addr, network)
	}
	cloudDNSResolverNetworks.Store(local, network)
	return nil
}

func cloudDNSCatalog(q dnsserver.Query) dnsserver.View {
	var network string
	if v, ok := cloudDNSResolverNetworks.Load(q.Local); ok {
		network, _ = v.(string)
	}
	all := cloudDNSZones.ListPrefix("")
	view := dnsserver.View{Zones: cloudDNSNetworkZones(all, network, true)}
	for _, zone := range all {
		if zone.Item.Visibility != "private" {
			view.Zones = append(view.Zones, cloudDNSServedZone(zone.ID, zone.Item.ManagedZone))
		}
	}
	return view
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
