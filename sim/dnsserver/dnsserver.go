// Package dnsserver is the DNS listener each simulator answers its zones on.
// A cloud supplies a Catalog — the zones and names a querier can see — and the
// server owns everything that is DNS rather than cloud: the wire, longest-zone
// matching, wildcards (RFC 4592), CNAME chains, delegations, NXDOMAIN versus
// NODATA with the zone's SOA as the negative answer (RFC 2308), truncation and
// EDNS0 on UDP, and recursion through upstream servers for names outside every
// zone.
package dnsserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
)

// Query names what a Catalog decides visibility from.
type Query struct {
	// Name is the lower-case, fully qualified question name.
	Name string
	Type uint16
	// Remote is the querier's address.
	Remote netip.Addr
	// Local is the address the query arrived at.
	Local netip.Addr
	// Network names the cloud network whose sidecar forwarded the query: the
	// listener ListenNetwork opened for it. Empty for a query at the server's
	// own port.
	Network string
}

// Zone is one zone a querier sees. Records hold every record set of the zone,
// apex SOA and NS included, owner names fully qualified.
type Zone struct {
	Apex    string
	Records []dns.RR
	// Forwarders, when set, make the zone a forwarding zone: its names are
	// answered by these servers (host:port) and Records are not read.
	Forwarders []string
}

// View is everything one query can see.
type View struct {
	Zones []Zone
	// Names are answered by exact owner name ahead of every zone: addresses a
	// platform gives a network outside any zone it hosts.
	Names []dns.RR
}

// Catalog returns what a query can see.
type Catalog func(Query) View

// Config configures a Server.
type Config struct {
	// Host is the IPv4 address to listen on; empty listens on all of them.
	Host string
	// Port is the port to listen on; empty or "0" lets the kernel choose one
	// free on both UDP and TCP.
	Port    string
	Catalog Catalog
	// Name labels the server in logs.
	Name string
	// Recursion, when set, recurses for names outside every zone. Without it,
	// or with no upstream answering, such a name is answered SERVFAIL: its
	// absence is not known.
	Recursion *Recursion
}

// Recursion names the servers a name outside every zone is recursed through.
type Recursion struct {
	// Upstreams is a comma-separated host[:port] list; empty reads the host's
	// resolv.conf.
	Upstreams string
}

// Server is a running DNS listener.
type Server struct {
	cfg  Config
	udp  *net.UDPConn
	tcp  *net.TCPListener
	port int
	wg   sync.WaitGroup
	once sync.Once

	upstreamOnce sync.Once
	upstreamList []string

	networksMu sync.Mutex
	networks   map[string]*networkListener
}

// networkListener is the UDP and TCP pair a cloud network's sidecar forwards
// to; a query arriving on it is attributed to that network.
type networkListener struct {
	udp  *net.UDPConn
	tcp  *net.TCPListener
	port int
}

// ephemeralAttempts bounds the search for a port free on both protocols: the
// kernel picks a UDP port without regard to TCP, and on a busy host its twin
// is often taken.
const ephemeralAttempts = 20

// Start binds the same port on UDP and TCP — a resolver given a truncated UDP
// answer retries over TCP on the same port — and serves both.
func Start(cfg Config) (*Server, error) {
	if cfg.Catalog == nil {
		return nil, errors.New("dnsserver: a catalog is required")
	}
	port := cfg.Port
	if port == "" {
		port = "0"
	}
	attempts := 1
	if port == "0" {
		attempts = ephemeralAttempts
	}
	var lastErr error
	for range attempts {
		udp, tcp, err := bindPair(cfg.Host, port)
		if err != nil {
			lastErr = err
			continue
		}
		s := &Server{cfg: cfg, udp: udp, tcp: tcp, port: int(addrPort(udp.LocalAddr()).Port())}
		pc := ipv4.NewPacketConn(udp)
		if err := pc.SetControlMessage(ipv4.FlagDst, true); err != nil {
			_ = udp.Close()
			_ = tcp.Close()
			return nil, fmt.Errorf("dnsserver: read query destinations: %w", err)
		}
		s.wg.Add(2)
		go s.serveUDP(pc, "")
		go s.serveTCP(tcp, "")
		log.Printf("%s dns: serving on UDP and TCP %s", cfg.Name, s.Addr())
		return s, nil
	}
	if port != "0" {
		return nil, fmt.Errorf("dnsserver: bind %s:%s: %w", cfg.Host, port, lastErr)
	}
	return nil, fmt.Errorf("dnsserver: no kernel-chosen port was free on both UDP and TCP after %d attempts: %w", attempts, lastErr)
}

// bindPair takes one port on both protocols, or neither.
var bindPair = func(host, port string) (*net.UDPConn, *net.TCPListener, error) {
	udpAddr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, port))
	if err != nil {
		return nil, nil, err
	}
	udp, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		return nil, nil, err
	}
	tcp, err := net.ListenTCP("tcp4", net.TCPAddrFromAddrPort(addrPort(udp.LocalAddr())))
	if err != nil {
		_ = udp.Close()
		return nil, nil, err
	}
	return udp, tcp, nil
}

// Port is the port the server answers on.
func (s *Server) Port() int { return s.port }

// Addr is the host:port the server answers on.
func (s *Server) Addr() string {
	host := s.cfg.Host
	if host == "" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(s.port))
}

// Close stops both listeners and waits for every query in flight.
func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		err = errors.Join(s.udp.Close(), s.tcp.Close())
		s.networksMu.Lock()
		for name, l := range s.networks {
			err = errors.Join(err, l.udp.Close(), l.tcp.Close())
			delete(s.networks, name)
		}
		s.networksMu.Unlock()
		s.wg.Wait()
	})
	return err
}

// ListenNetwork opens a listener pair on a kernel-chosen port for the named
// cloud network and returns the port; a query it receives carries the name as
// Query.Network. The network's sidecar forwards to it from inside the network,
// so it binds every address. A second call for a name already listening
// returns its port.
func (s *Server) ListenNetwork(network string) (int, error) {
	if network == "" {
		return 0, errors.New("dnsserver: a network listener needs a name")
	}
	s.networksMu.Lock()
	defer s.networksMu.Unlock()
	if l, ok := s.networks[network]; ok {
		return l.port, nil
	}
	var lastErr error
	for range ephemeralAttempts {
		udp, tcp, err := bindPair("", "0")
		if err != nil {
			lastErr = err
			continue
		}
		pc := ipv4.NewPacketConn(udp)
		if err := pc.SetControlMessage(ipv4.FlagDst, true); err != nil {
			_ = udp.Close()
			_ = tcp.Close()
			return 0, fmt.Errorf("dnsserver: read query destinations: %w", err)
		}
		l := &networkListener{udp: udp, tcp: tcp, port: int(addrPort(udp.LocalAddr()).Port())}
		if s.networks == nil {
			s.networks = map[string]*networkListener{}
		}
		s.networks[network] = l
		s.wg.Add(2)
		go s.serveUDP(pc, network)
		go s.serveTCP(tcp, network)
		return l.port, nil
	}
	return 0, fmt.Errorf("dnsserver: no kernel-chosen port was free on both UDP and TCP after %d attempts: %w", ephemeralAttempts, lastErr)
}

// CloseNetwork stops the named network's listener pair; a name that is not
// listening is left alone.
func (s *Server) CloseNetwork(network string) error {
	s.networksMu.Lock()
	l, ok := s.networks[network]
	delete(s.networks, network)
	s.networksMu.Unlock()
	if !ok {
		return nil
	}
	return errors.Join(l.udp.Close(), l.tcp.Close())
}

func (s *Server) serveUDP(pc *ipv4.PacketConn, network string) {
	defer s.wg.Done()
	buf := make([]byte, dns.MaxMsgSize)
	for {
		n, cm, src, err := pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		udpSrc, ok := src.(*net.UDPAddr)
		if !ok {
			continue
		}
		var local netip.Addr
		if cm != nil {
			local, _ = netip.AddrFromSlice(cm.Dst.To4())
		}
		query := append([]byte(nil), buf[:n]...)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			resp := s.respond(query, addrPort(udpSrc).Addr(), local, network, true)
			if resp == nil {
				return
			}
			var reply *ipv4.ControlMessage
			if local.IsValid() {
				reply = &ipv4.ControlMessage{Src: local.AsSlice()}
			}
			_, _ = pc.WriteTo(resp, reply, udpSrc)
		}()
	}
}

// tcpIdle is how long a TCP connection may sit between queries (RFC 7766
// leaves the value to the server).
const tcpIdle = 10 * time.Second

func (s *Server) serveTCP(tcp *net.TCPListener, network string) {
	defer s.wg.Done()
	for {
		conn, err := tcp.AcceptTCP()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		s.wg.Add(1)
		go s.serveConn(conn, network)
	}
}

func (s *Server) serveConn(conn *net.TCPConn, network string) {
	defer s.wg.Done()
	defer func() { _ = conn.Close() }()
	remote := addrPort(conn.RemoteAddr()).Addr()
	local := addrPort(conn.LocalAddr()).Addr()
	lenBuf := make([]byte, 2)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(tcpIdle))
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return
		}
		msg := make([]byte, int(lenBuf[0])<<8|int(lenBuf[1]))
		if _, err := io.ReadFull(conn, msg); err != nil {
			return
		}
		resp := s.respond(msg, remote, local, network, false)
		if resp == nil {
			return
		}
		out := append([]byte{byte(len(resp) >> 8), byte(len(resp))}, resp...)
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}

// addrPort reads a socket address with any IPv4-mapped prefix removed.
func addrPort(a net.Addr) netip.AddrPort {
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return netip.AddrPort{}
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

// respond answers one packed query. A nil result drops it: a message that does
// not parse has no ID to answer to.
func (s *Server) respond(packed []byte, remote, local netip.Addr, network string, udp bool) []byte {
	req := new(dns.Msg)
	if err := req.Unpack(packed); err != nil {
		return nil
	}
	resp := s.exchange(context.Background(), req, Query{Remote: remote, Local: local, Network: network})
	if udp {
		size := dns.MinMsgSize
		if opt := req.IsEdns0(); opt != nil && int(opt.UDPSize()) > size {
			size = int(opt.UDPSize())
		}
		resp.Truncate(size)
	}
	out, err := resp.Pack()
	if err != nil {
		fail := new(dns.Msg)
		fail.SetRcode(req, dns.RcodeServerFailure)
		out, _ = fail.Pack()
	}
	return out
}
