package main

import (
	"net"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

// resolveWorkloadHostAlias answers the container-host aliases
// (host.docker.internal, host.containers.internal) with the same address every
// other network tier already receives in /etc/hosts.
//
// A workload in the netns tier shares the pause container's network namespace,
// and a container joined to another container's namespace cannot be given host
// entries at all — the runtime rejects the create. So the one tier that runs a
// task on a real ENI was also the one tier where these names resolved nowhere,
// while bridge, host and plain awsvpc workloads all resolved them. Its DNS
// already arrives here (the task namespace DNATs the VPC resolver address to
// this server), so answering here restores the parity rather than granting the
// tier a route it did not have: the address is the one the simulator hands to
// every other tier, and a task that could not reach it still cannot.
//
// This is deliberately not a hosted zone. Real AWS has no such name, so a zone
// would show up in ListHostedZones and in a caller's own records.
// workloadHostAliasEntries is the source of the aliases and the address they
// answer with. It is a variable so a test can supply the entries a containerized
// simulator computes; the real function returns nothing anywhere else, which
// would otherwise leave this code untested on every machine it is written on.
var workloadHostAliasEntries = hostMetadataHostEntries

func resolveWorkloadHostAlias(q dnsmessage.Question) ([]dnsmessage.Resource, bool) {
	if q.Class != dnsmessage.ClassINET {
		return nil, false
	}
	name := normalizeDNSName(q.Name.String())
	entries := workloadHostAliasEntries()
	for _, entry := range entries {
		if !strings.EqualFold(name, entry.Name) {
			continue
		}
		// The name is ours to answer. A non-A question gets an empty answer
		// with NOERROR, which is what a name that exists without that record
		// type looks like — not NXDOMAIN, which would deny the name itself.
		if q.Type != dnsmessage.TypeA {
			return nil, true
		}
		ip := net.ParseIP(entry.IP)
		if ip == nil || ip.To4() == nil {
			return nil, true
		}
		var addr [4]byte
		copy(addr[:], ip.To4())
		return []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  dnsmessage.MustNewName(dnsFullName(entry.Name)),
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
				TTL:   30,
			},
			Body: &dnsmessage.AResource{A: addr},
		}}, true
	}
	return nil, false
}
