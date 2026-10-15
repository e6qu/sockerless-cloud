package main

import (
	"net"

	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
	"github.com/miekg/dns"
)

// workloadHostAliasRecords answers the container-host aliases
// (host.docker.internal, host.containers.internal) with the same address every
// other network tier already receives in /etc/hosts.
//
// A workload in the netns tier shares the pause container's network namespace,
// and a container joined to another container's namespace cannot be given host
// entries at all — the runtime rejects the create. Its DNS already arrives at
// this resolver, so answering here gives that tier the address every other
// tier gets, and nothing more.
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
