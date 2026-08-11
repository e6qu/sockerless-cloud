package main

import (
	"bufio"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// The VPC resolver answers for the zones it hosts AND recurses for everything
// else. AmazonProvidedDNS does both, and a task cannot tell the difference —
// it has one nameserver and expects every name to resolve through it.
//
// Answering only from the hosted zones makes the resolver reachable without
// making it complete: a task can look up its own service and nothing else, so
// anything reaching a public name — a package registry, a git remote, an OIDC
// issuer — fails with ENOTFOUND. That is not a name that does not exist; it is
// a resolver that never asked.
//
// Queries outside every hosted zone are forwarded verbatim and the upstream's
// reply is returned verbatim, so record types this simulator does not model
// still resolve correctly.

// route53UpstreamTimeout bounds one upstream exchange. A resolver that cannot
// answer quickly should fail the query rather than hold the task's DNS client
// past its own patience, which would turn a slow lookup into a hung process.
const route53UpstreamTimeout = 3 * time.Second

// route53MaxDNSMessage is the largest reply accepted over UDP. 4096 is the
// common EDNS0 advertised size; a larger answer arrives truncated and the
// client retries over TCP, which is exactly the RFC 1035 fallback.
const route53MaxDNSMessage = 4096

// route53UpstreamOverride names the resolver to forward to, host:port or bare
// host (port 53 assumed), comma-separated for several. Empty means read the
// host's own resolv.conf, which is what makes this work with no configuration.
const route53UpstreamOverride = "SIM_DNS_UPSTREAM"

var (
	route53ResolvConfOnce    sync.Once
	route53ResolvConfServers []string
)

// route53UpstreamServers returns the resolvers to recurse through, nearest
// configuration first. resolv.conf is read once: it is a boot-time fact, and
// re-reading it per query would put a file open on the path of every lookup a
// task makes.
func route53UpstreamServers() []string {
	if override := strings.TrimSpace(os.Getenv(route53UpstreamOverride)); override != "" {
		return route53NormalizeServers(strings.Split(override, ","))
	}
	route53ResolvConfOnce.Do(func() {
		route53ResolvConfServers = route53NormalizeServers(route53ResolvConfNameservers("/etc/resolv.conf"))
	})
	return route53ResolvConfServers
}

// route53ResolvConfNameservers reads the nameserver lines out of a resolv.conf.
// A missing or unreadable file is not an error worth failing on — it simply
// means there is nothing to forward to, and the caller reports SERVFAIL.
func route53ResolvConfNameservers(path string) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	var servers []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			servers = append(servers, fields[1])
		}
	}
	return servers
}

// route53NormalizeServers gives each entry a port and drops the ones that would
// point back here. Forwarding to our own listener, or to the link-local address
// the task's DNAT rule rewrites INTO our listener, is a query loop: the reply
// never comes and the task's resolver waits out its full timeout on every name.
func route53NormalizeServers(raw []string) []string {
	var out []string
	for _, entry := range raw {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		host, port, err := net.SplitHostPort(entry)
		if err != nil {
			host, port = entry, "53"
		}
		if route53IsSelf(host, port) {
			continue
		}
		out = append(out, net.JoinHostPort(host, port))
	}
	return out
}

// route53IsSelf reports whether host:port is this resolver, by either the
// address it listens on or the link-local address tasks reach it through.
func route53IsSelf(host, port string) bool {
	if host == VPCResolverIPv4Address {
		return true
	}
	if r53DNSAddr == "" {
		return false
	}
	selfHost, selfPort, err := net.SplitHostPort(r53DNSAddr)
	if err != nil {
		return false
	}
	if port != selfPort {
		return false
	}
	return host == selfHost || (selfHost == "" && host == "127.0.0.1")
}

// VPCResolverIPv4Address mirrors realexec.VPCResolverIPv4 for the loop guard.
// It is stated here rather than imported so this file has no dependency on the
// execution package, which the resolver otherwise does not touch.
const VPCResolverIPv4Address = "169.254.169.253"

// route53ForwardQuery sends the query on to an upstream resolver and returns its
// reply untouched. The second result is false when no upstream answered, which
// the caller reports as SERVFAIL rather than NXDOMAIN: "I could not ask" and
// "the name does not exist" are different answers, and a client caches the
// second one.
func route53ForwardQuery(query []byte) ([]byte, bool) {
	for _, server := range route53UpstreamServers() {
		reply, ok := route53ExchangeUDP(server, query)
		if ok {
			return reply, true
		}
	}
	return nil, false
}

func route53ExchangeUDP(server string, query []byte) ([]byte, bool) {
	conn, err := net.DialTimeout("udp", server, route53UpstreamTimeout)
	if err != nil {
		return nil, false
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(route53UpstreamTimeout)); err != nil {
		return nil, false
	}
	if _, err := conn.Write(query); err != nil {
		return nil, false
	}
	buf := make([]byte, route53MaxDNSMessage)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		return nil, false
	}
	return buf[:n], true
}
