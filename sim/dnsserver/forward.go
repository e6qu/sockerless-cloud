package dnsserver

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// forwardTimeout bounds one upstream exchange: a resolver library's own retry
// budget is a few seconds, and the next upstream deserves a turn inside it.
const forwardTimeout = 3 * time.Second

// forward asks each upstream in turn, over UDP and then over TCP when the UDP
// answer comes back truncated, and relays the first answer. With no upstream
// answering the name's absence is unknown, which is SERVFAIL rather than an
// NXDOMAIN a client would cache.
func forward(ctx context.Context, req *dns.Msg, upstreams []string) *dns.Msg {
	query := req.Copy()
	query.RecursionDesired = true
	for _, upstream := range upstreams {
		resp, err := exchange(ctx, "udp", query, upstream)
		if err == nil && resp.Truncated {
			resp, err = exchange(ctx, "tcp", query, upstream)
		}
		if err != nil {
			continue
		}
		resp.Id = req.Id
		resp.RecursionDesired = req.RecursionDesired
		return resp
	}
	fail := new(dns.Msg)
	fail.SetRcode(req, dns.RcodeServerFailure)
	return fail
}

func exchange(ctx context.Context, network string, query *dns.Msg, upstream string) (*dns.Msg, error) {
	ctx, cancel := context.WithTimeout(ctx, forwardTimeout)
	defer cancel()
	client := &dns.Client{Net: network, UDPSize: maxEDNSSize}
	resp, _, err := client.ExchangeContext(ctx, query, upstream)
	return resp, err
}

// ResolvConf is the host file upstreams are read from by default.
var ResolvConf = "/etc/resolv.conf"

// upstreams returns the servers to recurse through, as host:port: those the
// Recursion lists (port 53 when none is given), or else the nameservers of the
// host's resolv.conf, read once. An entry that points back at this server's
// own listener is left out: forwarding to it is a query loop whose reply
// never comes.
func (s *Server) upstreams() []string {
	rec := s.cfg.Recursion
	if rec == nil {
		return nil
	}
	s.upstreamOnce.Do(func() {
		raw := splitServers(rec.Upstreams)
		if len(raw) == 0 {
			if cfg, err := dns.ClientConfigFromFile(ResolvConf); err == nil {
				for _, server := range cfg.Servers {
					raw = append(raw, net.JoinHostPort(server, cfg.Port))
				}
			}
		}
		for _, entry := range raw {
			host, port, err := net.SplitHostPort(entry)
			if err != nil {
				continue
			}
			addr, err := netip.ParseAddr(host)
			if err == nil && s.isSelf(addr.Unmap(), port) {
				continue
			}
			s.upstreamList = append(s.upstreamList, entry)
		}
	})
	return s.upstreamList
}

func splitServers(list string) []string {
	var out []string
	for _, entry := range strings.Split(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(entry); err != nil {
			entry = net.JoinHostPort(entry, "53")
		}
		out = append(out, entry)
	}
	return out
}

func (s *Server) isSelf(addr netip.Addr, port string) bool {
	if port != strconv.Itoa(s.port) {
		return false
	}
	if s.cfg.Host == "" {
		return addr.IsLoopback() || addr.IsUnspecified()
	}
	host, err := netip.ParseAddr(s.cfg.Host)
	return err == nil && host == addr
}
