package dnsserver

import (
	"context"
	"net/netip"
	"strings"

	"github.com/miekg/dns"
)

// maxChain bounds a CNAME chain, which a zone can make circular.
const maxChain = 8

// maxEDNSSize is the largest UDP payload the server advertises.
const maxEDNSSize = 4096

// Exchange answers one query as the server would on the wire, before any
// truncation for UDP.
func (s *Server) Exchange(ctx context.Context, req *dns.Msg, remote, local netip.Addr) *dns.Msg {
	return s.exchange(ctx, req, Query{Remote: remote, Local: local})
}

// exchange answers req for the querier from names; from's Name and Type are
// taken from the question.
func (s *Server) exchange(ctx context.Context, req *dns.Msg, from Query) *dns.Msg {
	resp := new(dns.Msg)
	if len(req.Question) != 1 || req.Opcode != dns.OpcodeQuery {
		resp.SetRcode(req, dns.RcodeFormatError)
		if req.Opcode != dns.OpcodeQuery {
			resp.Rcode = dns.RcodeNotImplemented
		}
		return withEDNS(req, resp)
	}
	q := req.Question[0]
	if q.Qclass != dns.ClassINET && q.Qclass != dns.ClassANY {
		resp.SetRcode(req, dns.RcodeRefused)
		return withEDNS(req, resp)
	}
	name := dns.CanonicalName(q.Name)
	from.Name, from.Type = name, q.Qtype
	view := s.cfg.Catalog(from)

	if owned := ownedBy(view.Names, name); len(owned) > 0 {
		resp.SetReply(req)
		resp.Authoritative = true
		resp.Answer = ofType(owned, q.Qtype)
		return withEDNS(req, resp)
	}

	zone, ok := closestZone(view.Zones, name)
	if !ok {
		return withEDNS(req, forward(ctx, req, s.upstreams()))
	}
	if len(zone.Forwarders) > 0 {
		return withEDNS(req, forward(ctx, req, zone.Forwarders))
	}
	resp.SetReply(req)
	resolve(view, zone, name, q.Qtype, resp)
	return withEDNS(req, resp)
}

// resolve fills resp with the zone's answer to name/qtype, following CNAMEs
// into whichever visible zone holds their targets (RFC 1034 §4.3.2).
func resolve(view View, zone Zone, name string, qtype uint16, resp *dns.Msg) {
	resp.Authoritative = true
	for range maxChain {
		if ns, cut := delegation(zone, name); cut != "" && qtype != dns.TypeDS {
			if len(resp.Answer) == 0 {
				resp.Authoritative = false
				resp.Ns = ns
				resp.Extra = glue(zone, ns)
			}
			return
		}
		rrs := ownedBy(zone.Records, name)
		if len(rrs) == 0 {
			if hasDescendant(zone.Records, name) {
				nodata(zone, resp)
				return
			}
			rrs = wildcard(zone, name)
			if len(rrs) == 0 {
				resp.Rcode = dns.RcodeNameError
				resp.Ns = negative(zone)
				return
			}
		}
		if matched := ofType(rrs, qtype); len(matched) > 0 {
			resp.Answer = append(resp.Answer, matched...)
			return
		}
		cname := ofType(rrs, dns.TypeCNAME)
		if len(cname) == 0 {
			nodata(zone, resp)
			return
		}
		target, ok := cname[0].(*dns.CNAME)
		if !ok {
			return
		}
		resp.Answer = append(resp.Answer, target)
		name = dns.CanonicalName(target.Target)
		next, ok := closestZone(view.Zones, name)
		if !ok || len(next.Forwarders) > 0 {
			return
		}
		zone = next
	}
	resp.Rcode = dns.RcodeServerFailure
}

func nodata(zone Zone, resp *dns.Msg) {
	resp.Ns = negative(zone)
}

// negative is the zone's SOA as a negative answer carries it, with the TTL a
// resolver may cache the absence for: the lesser of the SOA's own TTL and its
// MINIMUM field (RFC 2308 §5).
func negative(zone Zone) []dns.RR {
	for _, rr := range ownedBy(zone.Records, zone.Apex) {
		soa, ok := rr.(*dns.SOA)
		if !ok {
			continue
		}
		cp := *soa
		cp.Hdr.Ttl = min(cp.Hdr.Ttl, cp.Minttl)
		return []dns.RR{&cp}
	}
	return nil
}

// delegation returns the NS set of the highest zone cut at or above name and
// below the apex.
func delegation(zone Zone, name string) ([]dns.RR, string) {
	labels := dns.SplitDomainName(name)
	apexLabels := dns.CountLabel(zone.Apex)
	for i := len(labels) - apexLabels - 1; i >= 0; i-- {
		owner := dns.Fqdn(strings.Join(labels[i:], "."))
		if ns := ofType(ownedBy(zone.Records, owner), dns.TypeNS); len(ns) > 0 {
			return ns, owner
		}
	}
	return nil, ""
}

// glue is the address records the zone holds for a delegation's name servers.
func glue(zone Zone, ns []dns.RR) []dns.RR {
	var out []dns.RR
	for _, rr := range ns {
		server, ok := rr.(*dns.NS)
		if !ok {
			continue
		}
		host := dns.CanonicalName(server.Ns)
		for _, a := range ownedBy(zone.Records, host) {
			if t := a.Header().Rrtype; t == dns.TypeA || t == dns.TypeAAAA {
				out = append(out, a)
			}
		}
	}
	return out
}

// wildcard returns the records a wildcard synthesizes for name: the source of
// synthesis is "*." under name's closest encloser, the longest ancestor that
// exists in the zone (RFC 4592 §3.3.1).
func wildcard(zone Zone, name string) []dns.RR {
	labels := dns.SplitDomainName(name)
	for i := 1; i < len(labels); i++ {
		encloser := dns.Fqdn(strings.Join(labels[i:], "."))
		if !dns.IsSubDomain(zone.Apex, encloser) {
			return nil
		}
		if encloser != zone.Apex && len(ownedBy(zone.Records, encloser)) == 0 && !hasDescendant(zone.Records, encloser) {
			continue
		}
		var out []dns.RR
		for _, rr := range ownedBy(zone.Records, "*."+encloser) {
			cp := dns.Copy(rr)
			cp.Header().Name = name
			out = append(out, cp)
		}
		return out
	}
	return nil
}

func closestZone(zones []Zone, name string) (Zone, bool) {
	best, found := Zone{}, false
	for _, z := range zones {
		apex := dns.CanonicalName(z.Apex)
		if !dns.IsSubDomain(apex, name) {
			continue
		}
		if !found || dns.CountLabel(apex) > dns.CountLabel(best.Apex) {
			z.Apex = apex
			best, found = z, true
		}
	}
	return best, found
}

func ownedBy(rrs []dns.RR, name string) []dns.RR {
	var out []dns.RR
	for _, rr := range rrs {
		if strings.EqualFold(rr.Header().Name, name) {
			out = append(out, rr)
		}
	}
	return out
}

func hasDescendant(rrs []dns.RR, name string) bool {
	for _, rr := range rrs {
		owner := rr.Header().Name
		if !strings.EqualFold(owner, name) && dns.IsSubDomain(name, dns.CanonicalName(owner)) {
			return true
		}
	}
	return false
}

func ofType(rrs []dns.RR, qtype uint16) []dns.RR {
	var out []dns.RR
	for _, rr := range rrs {
		if qtype == dns.TypeANY || rr.Header().Rrtype == qtype {
			out = append(out, rr)
		}
	}
	return out
}

// withEDNS answers an EDNS0 query with an OPT record of the server's own
// (RFC 6891 §6.1.1).
func withEDNS(req, resp *dns.Msg) *dns.Msg {
	if req.IsEdns0() != nil && resp.IsEdns0() == nil {
		resp.SetEdns0(maxEDNSSize, false)
	}
	return resp
}
