package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/ti-mo/conntrack"
	"golang.org/x/sys/unix"

	"github.com/e6qu/sockerless-cloud/sim/sidecar/egress"
)

// As `sidecar egress` the program is a cloud's NAT gateway from inside the
// network: a guest routed through it sends every connection that leaves the
// network to its address, the kernel hands each one to the proxy here, and
// the proxy opens it toward the destination from the gateway's own address,
// so the destination sees the gateway and not the guest. The proxy applies
// the gateway's idle timeouts and reports every connection to the simulator,
// which turns the reports into the gateway's metrics and logs.

const egressTable = "sockerless_egress"

// egressSnapshotPeriod is how often the active connection count is reported:
// the minute the clouds' NAT metrics are kept at.
const egressSnapshotPeriod = time.Minute

type egressConfig struct {
	sources  []netip.Prefix
	local    []netip.Prefix
	tcpIdle  time.Duration
	udpIdle  time.Duration
	gateway  string
	upstream string
	token    string
}

func egressConfigFromEnv() (egressConfig, error) {
	var cfg egressConfig
	var err error
	if cfg.sources, err = parsePrefixes(os.Getenv(egress.EnvSources)); err != nil || len(cfg.sources) == 0 {
		return cfg, fmt.Errorf("%s %q: at least one CIDR is required", egress.EnvSources, os.Getenv(egress.EnvSources))
	}
	if cfg.local, err = parsePrefixes(os.Getenv(egress.EnvLocal)); err != nil {
		return cfg, fmt.Errorf("%s %q: %w", egress.EnvLocal, os.Getenv(egress.EnvLocal), err)
	}
	if cfg.tcpIdle, err = parseSeconds(os.Getenv(egress.EnvTCPIdle)); err != nil {
		return cfg, fmt.Errorf("%s: %w", egress.EnvTCPIdle, err)
	}
	if cfg.udpIdle, err = parseSeconds(os.Getenv(egress.EnvUDPIdle)); err != nil {
		return cfg, fmt.Errorf("%s: %w", egress.EnvUDPIdle, err)
	}
	cfg.gateway = os.Getenv(egress.EnvGateway)
	cfg.upstream = os.Getenv("SIDECAR_HTTP_UPSTREAM")
	cfg.token = os.Getenv("SIDECAR_TOKEN")
	if cfg.gateway == "" || cfg.upstream == "" || cfg.token == "" {
		return cfg, errors.New("the gateway, the HTTP upstream and the token are required")
	}
	return cfg, nil
}

func parsePrefixes(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		p, err := netip.ParsePrefix(item)
		if err != nil || !p.Addr().Is4() {
			return nil, fmt.Errorf("%q is not an IPv4 CIDR", item)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func parseSeconds(s string) (time.Duration, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a positive number of seconds", s)
	}
	return time.Duration(n) * time.Second, nil
}

// serveEgress carries connections for the sources until ctx ends.
func serveEgress(ctx context.Context, out io.Writer) error {
	cfg, err := egressConfigFromEnv()
	if err != nil {
		return err
	}
	tcp, err := net.ListenTCP("tcp4", &net.TCPAddr{})
	if err != nil {
		return fmt.Errorf("listen tcp: %w", err)
	}
	defer func() { _ = tcp.Close() }()
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return fmt.Errorf("listen udp: %w", err)
	}
	defer func() { _ = udp.Close() }()
	if err := receiveOriginalDestinations(udp); err != nil {
		return err
	}
	tcpPort := portOf(tcp.Addr())
	udpPort := portOf(udp.LocalAddr())
	if err := programRedirect(cfg, tcpPort, udpPort); err != nil {
		return fmt.Errorf("program the redirect: %w", err)
	}
	defer removeRedirect()
	tracker, err := conntrack.Dial(nil)
	if err != nil {
		return fmt.Errorf("open the connection tracker: %w", err)
	}
	defer func() { _ = tracker.Close() }()
	p := &egressProxy{cfg: cfg, reporter: newReporter(cfg), tracker: tracker, stopping: make(chan struct{})}
	fmt.Fprintf(out, "sidecar: egress %s tcp %s udp %s for %s -> %s\n", cfg.gateway, tcp.Addr(), udp.LocalAddr(), joinPrefixes(cfg.sources), cfg.upstream)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); p.reporter.run(p.stopping) }()
	go func() { defer wg.Done(); p.acceptTCP(ctx, tcp) }()
	go func() { defer wg.Done(); p.serveUDP(ctx, udp) }()
	ticker := time.NewTicker(egressSnapshotPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.snapshot()
		case <-ctx.Done():
			_ = tcp.Close()
			_ = udp.Close()
			p.closeAll()
			// The reporter goes last, with what the closes recorded.
			close(p.stopping)
			wg.Wait()
			return nil
		}
	}
}

// addrPortOf is a TCP address as an address and port.
func addrPortOf(addr net.Addr) netip.AddrPort {
	if a, ok := addr.(*net.TCPAddr); ok {
		return a.AddrPort()
	}
	return netip.AddrPort{}
}

// portOf is the port of a TCP or UDP address.
func portOf(addr net.Addr) int {
	switch a := addr.(type) {
	case *net.TCPAddr:
		return a.Port
	case *net.UDPAddr:
		return a.Port
	}
	return 0
}

func joinPrefixes(prefixes []netip.Prefix) string {
	parts := make([]string, len(prefixes))
	for i, p := range prefixes {
		parts[i] = p.String()
	}
	return strings.Join(parts, ",")
}

// programRedirect makes the kernel hand the proxy every connection a source
// opens toward an address outside the network: a nat prerouting chain
// redirects TCP and UDP from the sources to the proxy's ports, after a chain
// that returns what stays local.
func programRedirect(cfg egressConfig, tcpPort, udpPort int) error {
	removeRedirect()
	c, err := nftables.New()
	if err != nil {
		return err
	}
	defer func() { _ = c.CloseLasting() }()
	table := c.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: egressTable})
	redirect := c.AddChain(&nftables.Chain{Name: "redirect", Table: table})
	for _, local := range cfg.local {
		c.AddRule(&nftables.Rule{Table: table, Chain: redirect, Exprs: append(matchPrefix(16, local), &expr.Verdict{Kind: expr.VerdictReturn})})
	}
	for _, proto := range []struct {
		number byte
		port   int
	}{{unix.IPPROTO_TCP, tcpPort}, {unix.IPPROTO_UDP, udpPort}} {
		c.AddRule(&nftables.Rule{Table: table, Chain: redirect, Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto.number}},
			&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(uint16(proto.port))},
			&expr.Redir{RegisterProtoMin: 1},
		}})
	}
	prerouting := c.AddChain(&nftables.Chain{
		Name:     "prerouting",
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityNATDest,
	})
	for _, source := range cfg.sources {
		exprs := []expr.Any{
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceName("eth0")},
		}
		exprs = append(exprs, matchPrefix(12, source)...)
		exprs = append(exprs, &expr.Verdict{Kind: expr.VerdictJump, Chain: "redirect"})
		c.AddRule(&nftables.Rule{Table: table, Chain: prerouting, Exprs: exprs})
	}
	return c.Flush()
}

func removeRedirect() {
	c, err := nftables.New()
	if err != nil {
		return
	}
	defer func() { _ = c.CloseLasting() }()
	c.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: egressTable})
	_ = c.Flush()
}

// matchPrefix compares the IPv4 address at offset of the network header,
// 12 for the source and 16 for the destination, with a prefix.
func matchPrefix(offset uint32, prefix netip.Prefix) []expr.Any {
	addr := prefix.Masked().Addr().As4()
	mask := net.CIDRMask(prefix.Bits(), 32)
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr[:]},
	}
}

func interfaceName(name string) []byte {
	b := make([]byte, unix.IFNAMSIZ)
	copy(b, name)
	return b
}

// receiveOriginalDestinations makes the UDP socket report, with each
// datagram, the address the datagram was sent to before the redirect.
func receiveOriginalDestinations(conn *net.UDPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_RECVORIGDSTADDR, 1)
	}); err != nil {
		return err
	}
	return sockErr
}

// originalDestination is where a redirected TCP connection was going.
func originalDestination(conn *net.TCPConn) (netip.AddrPort, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	var (
		addr    *unix.IPv6Mreq
		sockErr error
	)
	if err := raw.Control(func(fd uintptr) {
		addr, sockErr = unix.GetsockoptIPv6Mreq(int(fd), unix.IPPROTO_IP, unix.SO_ORIGINAL_DST)
	}); err != nil {
		return netip.AddrPort{}, err
	}
	if sockErr != nil {
		return netip.AddrPort{}, sockErr
	}
	// The option fills a sockaddr_in: the port at bytes 2-3, the address at
	// 4-7, both in network order.
	port := uint16(addr.Multiaddr[2])<<8 | uint16(addr.Multiaddr[3])
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte(addr.Multiaddr[4:8])), port), nil
}

type egressProxy struct {
	cfg      egressConfig
	reporter *reporter
	tracker  *conntrack.Conn
	stopping chan struct{}

	mu     sync.Mutex
	flows  map[*flow]struct{}
	udp    map[string]*udpFlow
	closed bool
}

// flow is one connection through the gateway and what it carried.
type flow struct {
	proto    string
	src      netip.AddrPort
	dst      netip.AddrPort
	bytesOut atomic.Int64
	bytesIn  atomic.Int64
	last     atomic.Int64
	done     chan struct{}
	once     sync.Once
	closeFn  func()
}

func (p *egressProxy) open(proto string, src, dst netip.AddrPort, closeFn func()) *flow {
	f := &flow{proto: proto, src: src, dst: dst, done: make(chan struct{}), closeFn: closeFn}
	f.touch()
	p.mu.Lock()
	if p.flows == nil {
		p.flows = map[*flow]struct{}{}
	}
	p.flows[f] = struct{}{}
	p.mu.Unlock()
	p.reporter.add(f.event(egress.Opened))
	return f
}

func (f *flow) touch() { f.last.Store(time.Now().UnixNano()) }

func (f *flow) event(kind egress.Kind) egress.Event {
	return egress.Event{
		Kind:            kind,
		Protocol:        f.proto,
		Source:          f.src.Addr().String(),
		SourcePort:      int(f.src.Port()),
		Destination:     f.dst.Addr().String(),
		DestinationPort: int(f.dst.Port()),
		BytesOut:        f.bytesOut.Load(),
		BytesIn:         f.bytesIn.Load(),
		At:              time.Now().UTC(),
	}
}

// close ends the flow once, for the reason given first.
func (p *egressProxy) close(f *flow, reason string) {
	f.once.Do(func() {
		close(f.done)
		f.closeFn()
		p.mu.Lock()
		delete(p.flows, f)
		p.mu.Unlock()
		ev := f.event(egress.Closed)
		ev.Reason = reason
		p.reporter.add(ev)
	})
}

// watchIdle ends the flow once nothing passed for idle; each byte moves the
// deadline, which is what the gateway's idle timeout means.
func (p *egressProxy) watchIdle(f *flow, idle time.Duration) {
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case <-f.done:
			return
		case <-timer.C:
			since := time.Since(time.Unix(0, f.last.Load()))
			if since >= idle {
				p.close(f, egress.ReasonIdle)
				return
			}
			timer.Reset(idle - since)
		}
	}
}

func (p *egressProxy) snapshot() {
	p.mu.Lock()
	n := len(p.flows)
	p.mu.Unlock()
	p.reporter.add(egress.Event{Kind: egress.Active, Active: n, At: time.Now().UTC()})
}

func (p *egressProxy) closeAll() {
	p.mu.Lock()
	p.closed = true
	flows := make([]*flow, 0, len(p.flows))
	for f := range p.flows {
		flows = append(flows, f)
	}
	p.mu.Unlock()
	for _, f := range flows {
		p.close(f, egress.ReasonClosed)
	}
}

func (p *egressProxy) acceptTCP(ctx context.Context, ln *net.TCPListener) {
	for {
		conn, err := ln.AcceptTCP()
		if err != nil {
			return
		}
		go p.handleTCP(ctx, conn)
	}
}

func (p *egressProxy) handleTCP(ctx context.Context, client *net.TCPConn) {
	dst, err := originalDestination(client)
	if err != nil {
		_ = client.Close()
		return
	}
	src := addrPortOf(client.RemoteAddr())
	var upstream *net.TCPConn
	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = client.Close()
			if upstream != nil {
				_ = upstream.Close()
			}
		})
	}
	f := p.open("tcp", src, dst, closeBoth)
	// The kernel's own connection timeout bounds the dial, as the gateway's
	// forwarded handshake is bounded by the source's retries.
	dialed, err := (&net.Dialer{}).DialContext(ctx, "tcp4", dst.String())
	if err != nil {
		p.close(f, egress.ReasonUnreachable)
		return
	}
	upstream, _ = dialed.(*net.TCPConn)
	established := f.event(egress.Established)
	established.ViaPort = portOf(upstream.LocalAddr())
	p.reporter.add(established)
	go p.watchIdle(f, p.cfg.tcpIdle)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		relay(upstream, client, &f.bytesOut, f.touch)
		_ = upstream.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		relay(client, upstream, &f.bytesIn, f.touch)
		_ = client.CloseWrite()
	}()
	wg.Wait()
	p.close(f, egress.ReasonClosed)
}

// relay copies src to dst, counting the bytes and marking activity.
func relay(dst io.Writer, src io.Reader, count *atomic.Int64, touch func()) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			count.Add(int64(n))
			touch()
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// udpFlow is one source's datagrams to one destination, which the gateway
// answers from the socket the redirect delivered them to, so the kernel
// restores the destination's address on the way back.
type udpFlow struct {
	flow     *flow
	upstream *net.UDPConn
}

func (p *egressProxy) serveUDP(ctx context.Context, conn *net.UDPConn) {
	buf := make([]byte, 65535)
	oob := make([]byte, 256)
	for {
		n, oobn, _, client, err := conn.ReadMsgUDP(buf, oob)
		if err != nil {
			return
		}
		local, ok := redirectedUDPDestination(oob[:oobn])
		if !ok {
			continue
		}
		p.forwardUDP(ctx, conn, client.AddrPort(), local, buf[:n])
	}
}

// redirectedUDPDestination is the address a datagram was delivered to after
// the redirect: the socket's own, which with the sender's names the reverse
// of the connection the tracker holds the original destination under.
func redirectedUDPDestination(oob []byte) (netip.AddrPort, bool) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return netip.AddrPort{}, false
	}
	for i := range msgs {
		if msgs[i].Header.Level != unix.IPPROTO_IP || msgs[i].Header.Type != unix.IP_ORIGDSTADDR {
			continue
		}
		sa, err := unix.ParseOrigDstAddr(&msgs[i])
		if err != nil {
			return netip.AddrPort{}, false
		}
		if in4, ok := sa.(*unix.SockaddrInet4); ok {
			return netip.AddrPortFrom(netip.AddrFrom4(in4.Addr), uint16(in4.Port)), true
		}
	}
	return netip.AddrPort{}, false
}

// originalUDPDestination asks the connection tracker where a redirected
// datagram flow was going: the entry whose reply direction runs from the
// socket's address to the sender holds the destination the sender named.
func (p *egressProxy) originalUDPDestination(local, src netip.AddrPort) (netip.AddrPort, error) {
	var query conntrack.Flow
	query.TupleReply = conntrack.Tuple{
		IP:    conntrack.IPTuple{SourceAddress: local.Addr(), DestinationAddress: src.Addr()},
		Proto: conntrack.ProtoTuple{Protocol: unix.IPPROTO_UDP, SourcePort: local.Port(), DestinationPort: src.Port()},
	}
	entry, err := p.tracker.Get(query)
	if err != nil {
		return netip.AddrPort{}, err
	}
	dst := netip.AddrPortFrom(entry.TupleOrig.IP.DestinationAddress.Unmap(), entry.TupleOrig.Proto.DestinationPort)
	if !dst.IsValid() || dst == local {
		return netip.AddrPort{}, errors.New("the datagram was not redirected")
	}
	return dst, nil
}

func (p *egressProxy) forwardUDP(ctx context.Context, conn *net.UDPConn, src, local netip.AddrPort, payload []byte) {
	key := src.String()
	p.mu.Lock()
	if p.udp == nil {
		p.udp = map[string]*udpFlow{}
	}
	uf, ok := p.udp[key]
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return
	}
	if !ok {
		dst, err := p.originalUDPDestination(local, src)
		if err != nil {
			return
		}
		upstream, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(dst))
		if err != nil {
			f := p.open("udp", src, dst, func() {})
			p.close(f, egress.ReasonUnreachable)
			return
		}
		uf = &udpFlow{upstream: upstream}
		uf.flow = p.open("udp", src, dst, func() {
			_ = upstream.Close()
			p.mu.Lock()
			delete(p.udp, key)
			p.mu.Unlock()
		})
		p.mu.Lock()
		p.udp[key] = uf
		p.mu.Unlock()
		established := uf.flow.event(egress.Established)
		established.ViaPort = portOf(upstream.LocalAddr())
		p.reporter.add(established)
		go p.watchIdle(uf.flow, p.cfg.udpIdle)
		go func() {
			buf := make([]byte, 65535)
			for {
				n, err := upstream.Read(buf)
				if err != nil {
					return
				}
				uf.flow.bytesIn.Add(int64(n))
				uf.flow.touch()
				if _, err := conn.WriteToUDPAddrPort(buf[:n], src); err != nil {
					return
				}
			}
		}()
	}
	if _, err := uf.upstream.Write(payload); err != nil {
		p.close(uf.flow, egress.ReasonUnreachable)
		return
	}
	uf.flow.bytesOut.Add(int64(len(payload)))
	uf.flow.touch()
}

// reporter sends the events to the simulator as they happen, in one request
// per batch that accumulated while the previous one was in flight.
type reporter struct {
	cfg    egressConfig
	client *http.Client
	mu     sync.Mutex
	queue  []egress.Event
	kick   chan struct{}
}

// reporterQueueLimit bounds what a reporter keeps while the simulator does
// not answer; the oldest events go first.
const reporterQueueLimit = 10000

func newReporter(cfg egressConfig) *reporter {
	return &reporter{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}, kick: make(chan struct{}, 1)}
}

func (r *reporter) add(ev egress.Event) {
	r.mu.Lock()
	r.queue = append(r.queue, ev)
	if len(r.queue) > reporterQueueLimit {
		r.queue = r.queue[len(r.queue)-reporterQueueLimit:]
	}
	r.mu.Unlock()
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

func (r *reporter) run(stopping <-chan struct{}) {
	for {
		select {
		case <-r.kick:
			r.flush(context.Background())
		case <-stopping:
			r.flush(context.Background())
			return
		}
	}
}

func (r *reporter) flush(ctx context.Context) {
	r.mu.Lock()
	batch := r.queue
	r.queue = nil
	r.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	body, err := json.Marshal(egress.Report{Gateway: r.cfg.gateway, Events: batch})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.cfg.upstream, "/")+egress.ReportPath, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, r.cfg.token)
	resp, err := r.client.Do(req)
	if err != nil {
		r.requeue(batch)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode >= 500 {
		r.requeue(batch)
	}
}

// requeue puts a batch the simulator did not take back ahead of what came
// after it, for the next flush.
func (r *reporter) requeue(batch []egress.Event) {
	r.mu.Lock()
	r.queue = append(batch, r.queue...)
	if len(r.queue) > reporterQueueLimit {
		r.queue = r.queue[len(r.queue)-reporterQueueLimit:]
	}
	r.mu.Unlock()
}
