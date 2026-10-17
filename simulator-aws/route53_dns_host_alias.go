package main

import (
	"net"

	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
	"github.com/miekg/dns"
)

// workloadHostAliasRecords answers the container-host aliases
// (host.docker.internal, host.containers.internal) with the address a
// workload receives for them in /etc/hosts, so a workload whose DNS arrives
// at this resolver reaches the host by the same names.
//
// The names are served ahead of the zones rather than as a hosted zone: real
// AWS has no such name, so a zone would show up in ListHostedZones. A question
// for another type at one of them is answered NOERROR with no records, which
// says the name exists.
func workloadHostAliasRecords() []dns.RR {
	var out []dns.RR
	for _, entry := range workloadHostAliasEntries() {
		ip := net.ParseIP(entry.IP).To4()
		if ip == nil {
			continue
		}
		out = append(out, &dns.A{
			Hdr: dns.RR_Header{Name: dns.CanonicalName(entry.Name), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30},
			A:   ip,
		})
	}
	return out
}

// workloadHostAliasEntries is the source of the aliases and the address they
// answer with. It is a variable so a test can supply the entries a containerized
// simulator computes; the real function returns nothing anywhere else.
var workloadHostAliasEntries = workloadhost.OuterHostEntries
