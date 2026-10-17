package dnsserver

import (
	"context"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func mustRR(t *testing.T, s string) dns.RR {
	t.Helper()
	rr, err := dns.NewRR(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return rr
}

func exampleZone(t *testing.T) Zone {
	t.Helper()
	var rrs []dns.RR
	for _, s := range []string{
		"example.test. 900 IN SOA ns1.example.test. admin.example.test. 1 7200 900 1209600 300",
		"example.test. 172800 IN NS ns1.example.test.",
		"ns1.example.test. 300 IN A 192.0.2.53",
		"www.example.test. 60 IN A 192.0.2.10",
		"www.example.test. 60 IN A 192.0.2.11",
		"alias.example.test. 60 IN CNAME www.example.test.",
		"outside.example.test. 60 IN CNAME api.other.test.",
		"loop1.example.test. 60 IN CNAME loop2.example.test.",
		"loop2.example.test. 60 IN CNAME loop1.example.test.",
		"a.b.c.example.test. 60 IN TXT \"deep\"",
		"*.wild.example.test. 60 IN A 192.0.2.99",
		"host.wild.example.test. 60 IN A 192.0.2.98",
		"sub.example.test. 3600 IN NS ns.sub.example.test.",
		"ns.sub.example.test. 3600 IN A 192.0.2.77",
	} {
		rrs = append(rrs, mustRR(t, s))
	}
	return Zone{Apex: "example.test.", Records: rrs}
}

func otherZone(t *testing.T) Zone {
	t.Helper()
	return Zone{Apex: "Other.Test", Records: []dns.RR{
		mustRR(t, "other.test. 600 IN SOA ns.other.test. admin.other.test. 1 7200 900 1209600 60"),
		mustRR(t, "api.other.test. 30 IN A 198.51.100.7"),
	}}
}

func newServer(t *testing.T, catalog Catalog) *Server {
	t.Helper()
	s, err := Start(Config{Host: "127.0.0.1", Catalog: catalog, Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func ask(t *testing.T, s *Server, network, name string, qtype uint16, edns bool) *dns.Msg {
	t.Helper()
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn(name), qtype)
	if edns {
		req.SetEdns0(4096, false)
	}
	client := &dns.Client{Net: network, Timeout: 5 * time.Second}
	resp, _, err := client.Exchange(req, s.Addr())
	if err != nil {
		t.Fatalf("%s %s %s: %v", network, name, dns.TypeToString[qtype], err)
	}
	return resp
}

func staticCatalog(view View) Catalog { return func(Query) View { return view } }

func answers(m *dns.Msg) []string {
	var out []string
	for _, rr := range m.Answer {
		out = append(out, strings.ReplaceAll(rr.String(), "\t", " "))
	}
	return out
}

func TestAnswersExactRecordsAuthoritatively(t *testing.T) {
	s := newServer(t, staticCatalog(View{Zones: []Zone{exampleZone(t)}}))
	resp := ask(t, s, "udp", "WWW.example.test", dns.TypeA, false)
	if resp.Rcode != dns.RcodeSuccess || !resp.Authoritative || len(resp.Answer) != 2 {
		t.Fatalf("got %v", resp)
	}
}

func TestFollowsCNAMEChainsAcrossZones(t *testing.T) {
	s := newServer(t, staticCatalog(View{Zones: []Zone{exampleZone(t), otherZone(t)}}))
	resp := ask(t, s, "udp", "outside.example.test", dns.TypeA, false)
	got := answers(resp)
	if resp.Rcode != dns.RcodeSuccess || len(got) != 2 ||
		!strings.Contains(got[0], "CNAME api.other.test.") || !strings.Contains(got[1], "198.51.100.7") {
		t.Fatalf("got %v", got)
	}
	if resp := ask(t, s, "udp", "alias.example.test", dns.TypeCNAME, false); len(resp.Answer) != 1 {
		t.Fatalf("a CNAME query answers the CNAME alone: %v", answers(resp))
	}
}

func TestStopsACircularCNAMEChain(t *testing.T) {
	s := newServer(t, staticCatalog(View{Zones: []Zone{exampleZone(t)}}))
	if resp := ask(t, s, "udp", "loop1.example.test", dns.TypeA, false); resp.Rcode != dns.RcodeServerFailure {
		t.Fatalf("got rcode %s", dns.RcodeToString[resp.Rcode])
	}
}

func TestNegativeAnswersCarryTheSOA(t *testing.T) {
	s := newServer(t, staticCatalog(View{Zones: []Zone{exampleZone(t)}}))
	for _, tc := range []struct {
		name  string
		qtype uint16
		rcode int
	}{
		{"missing.example.test", dns.TypeA, dns.RcodeNameError},
		{"www.example.test", dns.TypeMX, dns.RcodeSuccess},
		{"b.c.example.test", dns.TypeA, dns.RcodeSuccess},
	} {
		resp := ask(t, s, "udp", tc.name, tc.qtype, false)
		if resp.Rcode != tc.rcode || len(resp.Answer) != 0 || len(resp.Ns) != 1 {
			t.Fatalf("%s %s: %v", tc.name, dns.TypeToString[tc.qtype], resp)
		}
		soa, ok := resp.Ns[0].(*dns.SOA)
		if !ok || soa.Hdr.Ttl != 300 {
			t.Fatalf("%s: the negative SOA is cached for the lesser of its TTL and MINIMUM: %v", tc.name, resp.Ns[0])
		}
	}
}

func TestWildcardsSynthesizeOnlyUnderTheClosestEncloser(t *testing.T) {
	s := newServer(t, staticCatalog(View{Zones: []Zone{exampleZone(t)}}))
	resp := ask(t, s, "udp", "x.y.wild.example.test", dns.TypeA, false)
	if got := answers(resp); len(got) != 1 || !strings.HasPrefix(got[0], "x.y.wild.example.test. 60 IN A 192.0.2.99") {
		t.Fatalf("synthesized: %v", got)
	}
	if got := answers(ask(t, s, "udp", "host.wild.example.test", dns.TypeA, false)); len(got) != 1 || !strings.Contains(got[0], "192.0.2.98") {
		t.Fatalf("an exact name beats the wildcard: %v", got)
	}
	if resp := ask(t, s, "udp", "x.host.wild.example.test", dns.TypeA, false); resp.Rcode != dns.RcodeNameError {
		t.Fatalf("below an existing name the wildcard above it does not apply: %v", resp)
	}
}

func TestDelegationsAnswerAReferralWithGlue(t *testing.T) {
	s := newServer(t, staticCatalog(View{Zones: []Zone{exampleZone(t)}}))
	resp := ask(t, s, "udp", "www.sub.example.test", dns.TypeA, false)
	if resp.Authoritative || len(resp.Answer) != 0 || len(resp.Ns) != 1 || len(resp.Extra) != 1 {
		t.Fatalf("got %v", resp)
	}
}

func TestNamesAnswerAheadOfZones(t *testing.T) {
	s := newServer(t, staticCatalog(View{
		Zones: []Zone{exampleZone(t)},
		Names: []dns.RR{mustRR(t, "www.example.test. 30 IN A 203.0.113.1")},
	}))
	if got := answers(ask(t, s, "udp", "www.example.test", dns.TypeA, false)); len(got) != 1 || !strings.Contains(got[0], "203.0.113.1") {
		t.Fatalf("got %v", got)
	}
	resp := ask(t, s, "udp", "www.example.test", dns.TypeAAAA, false)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 0 {
		t.Fatalf("a name without the type is NODATA: %v", resp)
	}
}

func TestTruncatesOverUDPAndAnswersInFullOverTCP(t *testing.T) {
	zone := exampleZone(t)
	for i := range 40 {
		zone.Records = append(zone.Records, mustRR(t, "big.example.test. 60 IN TXT \""+strings.Repeat("x", 200)+strconv.Itoa(i)+"\""))
	}
	s := newServer(t, staticCatalog(View{Zones: []Zone{zone}}))
	if resp := ask(t, s, "udp", "big.example.test", dns.TypeTXT, false); !resp.Truncated {
		t.Fatalf("a 512-byte UDP answer is truncated: %d answers", len(resp.Answer))
	}
	resp := ask(t, s, "tcp", "big.example.test", dns.TypeTXT, false)
	if resp.Truncated || len(resp.Answer) != 40 {
		t.Fatalf("TCP carries every record: %d", len(resp.Answer))
	}
	if opt := ask(t, s, "udp", "www.example.test", dns.TypeA, true).IsEdns0(); opt == nil || opt.UDPSize() != maxEDNSSize {
		t.Fatalf("an EDNS0 query is answered with the server's OPT: %v", opt)
	}
}

func TestCatalogSeesWhereTheQueryArrived(t *testing.T) {
	var mu sync.Mutex
	var seen []Query
	s := newServer(t, func(q Query) View {
		mu.Lock()
		seen = append(seen, q)
		mu.Unlock()
		return View{Zones: []Zone{exampleZone(t)}}
	})
	ask(t, s, "udp", "www.example.test", dns.TypeA, false)
	ask(t, s, "tcp", "www.example.test", dns.TypeAAAA, false)
	loopback := netip.MustParseAddr("127.0.0.1")
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("queries: %v", seen)
	}
	for _, q := range seen {
		if q.Local != loopback || q.Remote != loopback || q.Name != "www.example.test." {
			t.Fatalf("query: %+v", q)
		}
	}
}

func TestRecursesOutsideItsZones(t *testing.T) {
	upstream := newServer(t, staticCatalog(View{Zones: []Zone{otherZone(t)}}))
	s, err := Start(Config{Host: "127.0.0.1", Catalog: staticCatalog(View{Zones: []Zone{exampleZone(t)}}),
		Recursion: &Recursion{Upstreams: "127.0.0.1:1," + upstream.Addr()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if got := answers(ask(t, s, "udp", "api.other.test", dns.TypeA, false)); len(got) != 1 || !strings.Contains(got[0], "198.51.100.7") {
		t.Fatalf("got %v", got)
	}
	unanswered := newServer(t, staticCatalog(View{Zones: []Zone{exampleZone(t)}}))
	if resp := ask(t, unanswered, "udp", "api.other.test", dns.TypeA, false); resp.Rcode != dns.RcodeServerFailure {
		t.Fatalf("without recursion the name's absence is unknown: %v", resp)
	}
}

func TestForwardingZonesAskTheirForwarders(t *testing.T) {
	upstream := newServer(t, staticCatalog(View{Zones: []Zone{otherZone(t)}}))
	s := newServer(t, staticCatalog(View{Zones: []Zone{exampleZone(t), {Apex: "other.test.", Forwarders: []string{upstream.Addr()}}}}))
	if got := answers(ask(t, s, "tcp", "api.other.test", dns.TypeA, false)); len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestUpstreamsLeaveOutTheServerItself(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(conf, []byte("nameserver 127.0.0.1\nnameserver 169.254.169.253\nnameserver 192.0.2.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := ResolvConf
	ResolvConf = conf
	t.Cleanup(func() { ResolvConf = saved })

	s, err := Start(Config{Host: "127.0.0.1", Catalog: staticCatalog(View{}), Recursion: &Recursion{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if got := s.upstreams(); len(got) != 3 || got[0] != "127.0.0.1:53" || got[1] != "169.254.169.253:53" || got[2] != "192.0.2.1:53" {
		t.Fatalf("from resolv.conf: %v", got)
	}

	held, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(held.LocalAddr().(*net.UDPAddr).Port)
	_ = held.Close()
	s2, err := Start(Config{Host: "127.0.0.1", Port: port, Catalog: staticCatalog(View{}),
		Recursion: &Recursion{Upstreams: "127.0.0.1:" + port + ", 192.0.2.2"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	if got := s2.upstreams(); len(got) != 1 || got[0] != "192.0.2.2:53" {
		t.Fatalf("from the configured list: %v", got)
	}
}

func TestStartRetriesAnEphemeralPortTakenOnTCP(t *testing.T) {
	held, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	takenPort := strconv.Itoa(held.Addr().(*net.TCPAddr).Port)
	saved := bindPair
	t.Cleanup(func() { bindPair = saved })
	calls := 0
	bindPair = func(host, port string) (*net.UDPConn, *net.TCPListener, error) {
		calls++
		if calls == 1 {
			return saved(host, takenPort)
		}
		return saved(host, port)
	}
	s := newServer(t, staticCatalog(View{}))
	if calls != 2 || strconv.Itoa(s.Port()) == takenPort {
		t.Fatalf("calls %d, port %d", calls, s.Port())
	}
	if _, err := Start(Config{Host: "127.0.0.1", Port: takenPort, Catalog: staticCatalog(View{})}); err == nil {
		t.Fatal("a configured port that is taken is an error, not a retry")
	}
}

func TestExchangeRejectsWhatItCannotAnswer(t *testing.T) {
	s := newServer(t, staticCatalog(View{Zones: []Zone{exampleZone(t)}}))
	req := new(dns.Msg)
	req.SetQuestion("www.example.test.", dns.TypeA)
	req.Question[0].Qclass = dns.ClassCHAOS
	if resp := s.Exchange(context.Background(), req, netip.Addr{}, netip.Addr{}); resp.Rcode != dns.RcodeRefused {
		t.Fatalf("got %v", resp)
	}
	req.Question = nil
	if resp := s.Exchange(context.Background(), req, netip.Addr{}, netip.Addr{}); resp.Rcode != dns.RcodeFormatError {
		t.Fatalf("got %v", resp)
	}
}
