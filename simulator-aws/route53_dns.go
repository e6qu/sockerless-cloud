package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Route 53 is the authoritative DNS server for its hosted zones. The HTTP
// API lets callers mutate the store; this UDP/TCP DNS listener answers real
// DNS queries against that same store, so `dig @<sim> <name>` returns what
// ChangeResourceRecordSets wrote. There is no second source of truth and no
// snapshot: each query walks r53Zones directly.

var (
	r53DNSAddr string
	r53DNSOnce sync.Once
)

func startRoute53DNSServer() {
	r53DNSOnce.Do(func() {
		udpConn, tcpLn := bindRoute53DNS(envOr("SIM_DNS_PORT", "5353"))
		r53DNSAddr = udpConn.LocalAddr().String()
		log.Printf("route53 dns: serving on UDP and TCP %s", r53DNSAddr)
		go serveRoute53DNSUDP(udpConn)
		go serveRoute53DNSTCP(tcpLn)
	})
}

// route53DNSPort is the port the simulator's resolver answers on, which a
// workload namespace's DNS is redirected to. It reads the bound address rather
// than the configured one so an ephemeral port resolves to what was actually
// taken.
func route53DNSPort() (int, error) {
	if r53DNSAddr == "" {
		return 0, fmt.Errorf("the simulator's resolver is not listening yet")
	}
	_, port, err := net.SplitHostPort(r53DNSAddr)
	if err != nil {
		return 0, fmt.Errorf("read resolver port from %q: %w", r53DNSAddr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return 0, fmt.Errorf("resolver port %q is not usable", port)
	}
	return n, nil
}

// r53DNSEphemeralAttempts bounds the search for a port free on both protocols.
// Twenty is far past what contention explains and short of hanging: if that
// many kernel-chosen ports are all taken on TCP, the host has a problem the
// simulator should report rather than keep asking about.
const r53DNSEphemeralAttempts = 20

// bindRoute53DNS acquires the same port on UDP and TCP, which is what a DNS
// server needs: a resolver that gets a truncated UDP answer retries the query
// over TCP on the same port.
//
// Asking the kernel for a free port ("0") asks it about *one* protocol. The two
// port spaces are independent, so a UDP port it hands out says nothing about
// whether that TCP port is free, and on a busy host it often is not — which
// crashed the simulator at startup and left everything waiting on a server that
// never came up. An ephemeral port is therefore retried until both protocols
// answer to the same number; a port the operator configured is not, because
// then the address is the request and failing to honour it is a real error.
func bindRoute53DNS(port string) (net.PacketConn, net.Listener) {
	ephemeral := port == "0"
	attempts := 1
	if ephemeral {
		attempts = r53DNSEphemeralAttempts
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		udpConn, tcpLn, err := r53DNSBindPair(port)
		if err == nil {
			return udpConn, tcpLn
		}
		if !ephemeral {
			panic("route53 dns: bind configured endpoint :" + port + ": " + err.Error())
		}
		lastErr = err
	}
	panic("route53 dns: no kernel-chosen port was free on both UDP and TCP after " +
		strconv.Itoa(attempts) + " attempts: " + lastErr.Error())
}

// r53DNSBindPair takes one port on both protocols, or neither. It is a variable
// so a test can drive the retry above deterministically: whether the kernel
// hands out a port whose TCP twin is taken is not something a test can arrange
// by holding ports and hoping.
var r53DNSBindPair = func(port string) (net.PacketConn, net.Listener, error) {
	udpConn, err := net.ListenPacket("udp", ":"+port)
	if err != nil {
		return nil, nil, err
	}
	tcpLn, err := net.Listen("tcp", udpConn.LocalAddr().String())
	if err != nil {
		_ = udpConn.Close()
		return nil, nil, err
	}
	return udpConn, tcpLn, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func serveRoute53DNSUDP(c net.PacketConn) {
	buf := make([]byte, 65535)
	for {
		n, addr, err := c.ReadFrom(buf)
		if err != nil {
			if isClosedErr(err) {
				return
			}
			continue
		}
		query := append([]byte(nil), buf[:n]...)
		go func(q []byte, from net.Addr) {
			resp, _ := answerRoute53DNS(q)
			if resp != nil {
				_, _ = c.WriteTo(resp, from)
			}
		}(query, addr)
	}
}

func serveRoute53DNSTCP(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if isClosedErr(err) {
				return
			}
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			lenBuf := make([]byte, 2)
			if _, err := readFull(c, lenBuf); err != nil {
				return
			}
			msgLen := int(lenBuf[0])<<8 | int(lenBuf[1])
			if msgLen < 12 || msgLen > 65535 {
				return
			}
			msgBuf := make([]byte, msgLen)
			if _, err := readFull(c, msgBuf); err != nil {
				return
			}
			resp, _ := answerRoute53DNS(msgBuf)
			if resp == nil {
				return
			}
			out := make([]byte, 2+len(resp))
			out[0] = byte(len(resp) >> 8)
			out[1] = byte(len(resp))
			copy(out[2:], resp)
			_, _ = conn.Write(out)
		}(conn)
	}
}

func readFull(c net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := c.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func isClosedErr(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "use of closed") ||
		strings.Contains(err.Error(), "closed network"))
}

// answerRoute53DNS parses a DNS query packet, resolves it against the Route 53
// store, and returns a packed DNS response. A nil response means "drop the
// packet" — used only for malformed queries that have no question to echo back.
func answerRoute53DNS(query []byte) ([]byte, error) {
	var p dnsmessage.Parser
	hdr, err := p.Start(query)
	if err != nil {
		return nil, err
	}
	q, err := p.Question()
	if err != nil {
		// No question: mirror real DNS — reply with the header and FORMERR.
		return buildRoute53DNSResponse(hdr, dnsmessage.RCodeFormatError, q, nil), nil
	}

	// Outside every hosted zone the VPC resolver recurses, exactly as
	// AmazonProvidedDNS does. Answering NXDOMAIN here instead leaves a task able
	// to resolve its own services and nothing else on the internet.
	if zoneID, _ := longestMatchingZone(normalizeDNSName(q.Name.String())); zoneID == "" {
		if forwarded, ok := route53ForwardQuery(query); ok {
			return forwarded, nil
		}
		// No upstream answered. That is not evidence the name is absent, and a
		// client caches NXDOMAIN — SERVFAIL leaves it free to retry.
		return buildRoute53DNSResponse(hdr, dnsmessage.RCodeServerFailure, q, nil), nil
	}

	answers, rcode := resolveRoute53(q)
	resp := buildRoute53DNSResponse(hdr, rcode, q, answers)
	return resp, nil
}

func buildRoute53DNSResponse(qHdr dnsmessage.Header, rcode dnsmessage.RCode, q dnsmessage.Question, answers []dnsmessage.Resource) []byte {
	out := dnsmessage.Header{
		ID:               qHdr.ID,
		Response:         true,
		OpCode:           qHdr.OpCode,
		Authoritative:    true,
		RecursionDesired: qHdr.RecursionDesired,
		RCode:            rcode,
	}
	b := dnsmessage.NewBuilder(nil, out)
	b.EnableCompression()
	if q.Name.Length != 0 {
		if err := b.StartQuestions(); err == nil {
			_ = b.Question(q)
		}
	}
	if len(answers) > 0 {
		_ = b.StartAnswers()
		for _, a := range answers {
			_ = packAnyResource(&b, a)
		}
	}
	msg, err := b.Finish()
	if err != nil {
		return nil
	}
	return msg
}

func packAnyResource(b *dnsmessage.Builder, r dnsmessage.Resource) error {
	h := r.Header
	h.Class = dnsmessage.ClassINET
	switch body := r.Body.(type) {
	case *dnsmessage.AResource:
		return b.AResource(h, *body)
	case *dnsmessage.AAAAResource:
		return b.AAAAResource(h, *body)
	case *dnsmessage.CNAMEResource:
		return b.CNAMEResource(h, *body)
	case *dnsmessage.TXTResource:
		return b.TXTResource(h, *body)
	case *dnsmessage.NSResource:
		return b.NSResource(h, *body)
	case *dnsmessage.MXResource:
		return b.MXResource(h, *body)
	case *dnsmessage.PTRResource:
		return b.PTRResource(h, *body)
	case *dnsmessage.SRVResource:
		return b.SRVResource(h, *body)
	case *dnsmessage.SOAResource:
		return b.SOAResource(h, *body)
	default:
		return nil
	}
}

// resolveRoute53 walks the Route 53 store, finds the longest-suffix matching
// hosted zone, and returns answer records for the question. It honors exact
// records first, then wildcard records per RFC 4592.
func resolveRoute53(q dnsmessage.Question) ([]dnsmessage.Resource, dnsmessage.RCode) {
	qName := normalizeDNSName(q.Name.String())
	qType := q.Type

	zoneID, zoneName := longestMatchingZone(qName)
	if zoneID == "" {
		return nil, dnsmessage.RCodeNameError
	}
	stored, ok := r53Zones.Get(zoneID)
	if !ok {
		return nil, dnsmessage.RCodeServerFailure
	}

	hdrTTL := uint32(300)

	// First pass: exact match.
	var answers []dnsmessage.Resource
	matched := false
	for _, rr := range stored.Records {
		if !strings.EqualFold(strings.TrimSuffix(rr.Name, "."), qName) {
			continue
		}
		rrType := strings.ToUpper(rr.Type)
		if rrType != typeNameForQType(qType) {
			continue
		}
		matched = true
		if rr.AliasTarget != nil && rr.AliasTarget.DNSName != "" {
			answers = append(answers, resolveAlias(rr.AliasTarget.DNSName, qType)...)
			continue
		}
		ttl := hdrTTL
		if rr.TTL != nil && *rr.TTL > 0 {
			ttl = uint32(*rr.TTL)
		}
		answers = append(answers, recordsFromRRSet(rr, qName, ttl)...)
	}
	if matched {
		return answers, dnsmessage.RCodeSuccess
	}

	// Second pass: wildcard match. Find the closest enclosing wildcard by
	// walking labels from the query name outward.
	wildcardRR, _ := findWildcardMatch(stored.Records, qName, qType)
	if wildcardRR != nil {
		ttl := hdrTTL
		if wildcardRR.TTL != nil && *wildcardRR.TTL > 0 {
			ttl = uint32(*wildcardRR.TTL)
		}
		answers = append(answers, recordsFromRRSet(*wildcardRR, qName, ttl)...)
		return answers, dnsmessage.RCodeSuccess
	}

	// SOA at the apex is the negative-caching signal real Route 53 returns
	// for NXDOMAIN inside an existing zone. Other types with no records
	// return NOERROR with no answers (NODATA).
	if qType != dnsmessage.TypeSOA {
		soa := findSOA(stored.Records, zoneName)
		if soa != nil {
			return nil, dnsmessage.RCodeNameError
		}
	}

	return answers, dnsmessage.RCodeSuccess
}

func resolveAlias(target string, qType dnsmessage.Type) []dnsmessage.Resource {
	targetName := normalizeDNSName(strings.TrimSuffix(target, "."))
	zoneID, _ := longestMatchingZone(targetName)
	if zoneID == "" {
		return nil
	}
	stored, ok := r53Zones.Get(zoneID)
	if !ok {
		return nil
	}
	var out []dnsmessage.Resource
	for _, rr := range stored.Records {
		if !strings.EqualFold(strings.TrimSuffix(rr.Name, "."), targetName) {
			continue
		}
		rrType := strings.ToUpper(rr.Type)
		if rrType == typeNameForQType(qType) || (rrType == "A" && qType == dnsmessage.TypeA) {
			if rr.AliasTarget != nil && rr.AliasTarget.DNSName != "" {
				out = append(out, resolveAlias(rr.AliasTarget.DNSName, qType)...)
				continue
			}
			out = append(out, recordsFromRRSet(rr, targetName, 60)...)
		}
	}
	return out
}

func longestMatchingZone(qName string) (zoneID, zoneName string) {
	var best string
	var bestID string
	for _, sz := range r53Zones.List() {
		zone := strings.TrimSuffix(strings.ToLower(sz.Zone.Name), ".")
		if zone == "" {
			continue
		}
		if qName == zone || strings.HasSuffix(qName, "."+zone) {
			if len(zone) > len(best) {
				best = zone
				bestID = r53ZoneIDFromPath(sz.Zone.Id)
			}
		}
	}
	return bestID, best
}

func findWildcardMatch(records []R53ResourceRecordSet, qName string, qType dnsmessage.Type) (*R53ResourceRecordSet, string) {
	labels := strings.Split(qName, ".")
	for i := range labels {
		candidate := "*." + strings.Join(labels[i+1:], ".")
		if candidate == "*." {
			continue
		}
		for j := range records {
			rr := &records[j]
			if !strings.EqualFold(strings.TrimSuffix(rr.Name, "."), candidate) {
				continue
			}
			if strings.ToUpper(rr.Type) != typeNameForQType(qType) {
				continue
			}
			return rr, candidate
		}
	}
	return nil, ""
}

func findSOA(records []R53ResourceRecordSet, zoneName string) *R53ResourceRecordSet {
	apex := dnsFullName(zoneName)
	for i := range records {
		if strings.EqualFold(records[i].Name, apex) && strings.EqualFold(records[i].Type, "SOA") {
			return &records[i]
		}
	}
	return nil
}

func recordsFromRRSet(rr R53ResourceRecordSet, name string, ttl uint32) []dnsmessage.Resource {
	if rr.ResourceRecords == nil {
		return nil
	}
	rrType := strings.ToUpper(rr.Type)
	dnsName, err := dnsmessage.NewName(dnsFullName(name))
	if err != nil {
		return nil
	}
	out := make([]dnsmessage.Resource, 0, len(rr.ResourceRecords.Items))
	hdr := dnsmessage.ResourceHeader{Name: dnsName, Class: dnsmessage.ClassINET, TTL: ttl}
	for _, rec := range rr.ResourceRecords.Items {
		val := rec.Value
		var body dnsmessage.ResourceBody
		switch rrType {
		case "A":
			ip := net.ParseIP(strings.TrimSpace(val))
			if ip4 := ip.To4(); ip4 != nil {
				var a [4]byte
				copy(a[:], ip4)
				body = &dnsmessage.AResource{A: a}
			}
		case "AAAA":
			ip := net.ParseIP(strings.TrimSpace(val))
			if ip.To4() == nil && ip != nil {
				var aaaa [16]byte
				copy(aaaa[:], ip.To16())
				body = &dnsmessage.AAAAResource{AAAA: aaaa}
			}
		case "CNAME", "PTR":
			n, err := dnsmessage.NewName(dnsFullName(strings.TrimSpace(val)))
			if err == nil {
				if rrType == "CNAME" {
					body = &dnsmessage.CNAMEResource{CNAME: n}
				} else {
					body = &dnsmessage.PTRResource{PTR: n}
				}
			}
		case "NS":
			n, err := dnsmessage.NewName(dnsFullName(strings.TrimSpace(val)))
			if err == nil {
				body = &dnsmessage.NSResource{NS: n}
			}
		case "TXT":
			body = &dnsmessage.TXTResource{TXT: splitTXTChunks(val)}
		case "MX":
			pref, host := splitMX(val)
			n, err := dnsmessage.NewName(dnsFullName(host))
			if err == nil {
				body = &dnsmessage.MXResource{Pref: pref, MX: n}
			}
		case "SRV":
			prio, weight, port, target := splitSRV(val)
			n, err := dnsmessage.NewName(dnsFullName(target))
			if err == nil {
				body = &dnsmessage.SRVResource{Priority: prio, Weight: weight, Port: port, Target: n}
			}
		case "SOA":
			body = buildSOAFromValue(val)
		}
		if body != nil {
			out = append(out, dnsmessage.Resource{Header: hdr, Body: body})
		}
	}
	return out
}

func buildSOAFromValue(val string) dnsmessage.ResourceBody {
	// Route 53 SOA value: "ns hostmaster serial refresh retry expire minttl"
	fields := strings.Fields(strings.TrimSpace(val))
	if len(fields) < 7 {
		return nil
	}
	serial, _ := strconv.ParseUint(fields[2], 10, 32)
	refresh, _ := strconv.ParseUint(fields[3], 10, 32)
	retry, _ := strconv.ParseUint(fields[4], 10, 32)
	expire, _ := strconv.ParseUint(fields[5], 10, 32)
	minTTL, _ := strconv.ParseUint(fields[6], 10, 32)
	ns, err := dnsmessage.NewName(dnsFullName(fields[0]))
	if err != nil {
		return nil
	}
	mbox, err := dnsmessage.NewName(dnsFullName(strings.ReplaceAll(fields[1], "@", ".")))
	if err != nil {
		return nil
	}
	return &dnsmessage.SOAResource{
		NS: ns, MBox: mbox,
		Serial: uint32(serial), Refresh: uint32(refresh),
		Retry: uint32(retry), Expire: uint32(expire), MinTTL: uint32(minTTL),
	}
}

func splitTXTChunks(val string) []string {
	const chunk = 255
	if len(val) == 0 {
		return []string{""}
	}
	out := []string{}
	for i := 0; i < len(val); i += chunk {
		end := i + chunk
		if end > len(val) {
			end = len(val)
		}
		out = append(out, val[i:end])
	}
	return out
}

func splitMX(val string) (uint16, string) {
	fields := strings.Fields(strings.TrimSpace(val))
	if len(fields) < 2 {
		return 0, val
	}
	pref, _ := strconv.ParseUint(fields[0], 10, 16)
	return uint16(pref), fields[1]
}

func splitSRV(val string) (prio, weight, port uint16, target string) {
	fields := strings.Fields(strings.TrimSpace(val))
	if len(fields) < 4 {
		return 0, 0, 0, val
	}
	p, _ := strconv.ParseUint(fields[0], 10, 16)
	w, _ := strconv.ParseUint(fields[1], 10, 16)
	pt, _ := strconv.ParseUint(fields[2], 10, 16)
	return uint16(p), uint16(w), uint16(pt), fields[3]
}

func typeNameForQType(t dnsmessage.Type) string {
	switch t {
	case dnsmessage.TypeA:
		return "A"
	case dnsmessage.TypeAAAA:
		return "AAAA"
	case dnsmessage.TypeCNAME:
		return "CNAME"
	case dnsmessage.TypeTXT:
		return "TXT"
	case dnsmessage.TypeNS:
		return "NS"
	case dnsmessage.TypeMX:
		return "MX"
	case dnsmessage.TypePTR:
		return "PTR"
	case dnsmessage.TypeSRV:
		return "SRV"
	case dnsmessage.TypeSOA:
		return "SOA"
	default:
		return ""
	}
}

func normalizeDNSName(s string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
}

func dnsFullName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "."
	}
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}
