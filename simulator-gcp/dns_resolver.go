package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

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

// startCloudDNSServer serves the public managed zones, which Cloud DNS's name
// servers answer for any querier. A private zone answers only inside the
// networks it is visible to, and no network's queries reach this listener.
func startCloudDNSServer() (*dnsserver.Server, error) {
	port := os.Getenv("SIM_DNS_PORT")
	return dnsserver.Start(dnsserver.Config{
		Port:      port,
		Name:      "cloud dns",
		Catalog:   cloudDNSCatalog,
		Recursion: &dnsserver.Recursion{Upstreams: os.Getenv("SIM_DNS_UPSTREAM")},
	})
}

func cloudDNSCatalog(dnsserver.Query) dnsserver.View {
	var view dnsserver.View
	for _, zone := range cloudDNSZones.ListPrefix("") {
		if zone.Item.Visibility == "private" {
			continue
		}
		var records []dns.RR
		for _, set := range cloudDNSRecordSets.ListPrefix(zone.ID + ":") {
			records = append(records, cloudDNSRecordSetRRs(set.Item.Record)...)
		}
		view.Zones = append(view.Zones, dnsserver.Zone{Apex: dns.Fqdn(zone.Item.DNSName), Records: records})
	}
	return view
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
