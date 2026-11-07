package cacheengine

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// handshakeDeadline bounds the TLS handshake of a client that connects and
// then says nothing.
const handshakeDeadline = 30 * time.Second

// Identifier is the loopback reservation an endpoint of owner holds: the
// owner itself for its primary or discovery endpoint, and owner#<key> for
// every other.
func Identifier(owner string, endpoint Endpoint) string {
	switch endpoint.Role {
	case PrimaryEndpoint, DiscoveryEndpoint:
		return owner
	}
	return owner + "#" + endpoint.Key()
}

// Listen binds port on a loopback address of the identifier's own and
// returns the listener with the address it took.
func Listen(identifier string, port int) (net.Listener, string, error) {
	listener, err := dbengine.ListenLoopback(identifier, port)
	if err != nil {
		return nil, "", err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, "", fmt.Errorf("listener returned address type %T", listener.Addr())
	}
	return listener, address.IP.String(), nil
}

// Bind binds each endpoint at Endpoint.Port (Redis's own when unset) on a
// loopback address: the address an earlier endpoint of the same identifier
// took, or one of its own on which every port its endpoints serve is free. It
// fills in each endpoint's Address.
func Bind(owner string, endpoints []Endpoint) (map[string]net.Listener, []Endpoint, error) {
	var identifiers []string
	ports := map[string][]int{}
	for _, endpoint := range endpoints {
		identifier := Identifier(owner, endpoint)
		if _, seen := ports[identifier]; !seen {
			identifiers = append(identifiers, identifier)
		}
		ports[identifier] = append(ports[identifier], endpoint.PortOrDefault())
	}
	listeners := map[string]net.Listener{}
	bound := map[string][]net.Listener{}
	for _, identifier := range identifiers {
		group, err := dbengine.ListenLoopbackPorts(identifier, ports[identifier])
		if err != nil {
			for _, listeners := range bound {
				for _, listener := range listeners {
					_ = listener.Close()
				}
			}
			return nil, nil, err
		}
		bound[identifier] = group
	}
	result := make([]Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		identifier := Identifier(owner, endpoint)
		listener := bound[identifier][0]
		bound[identifier] = bound[identifier][1:]
		address, ok := listener.Addr().(*net.TCPAddr)
		if !ok {
			return nil, nil, fmt.Errorf("listener returned address type %T", listener.Addr())
		}
		listeners[endpoint.Key()] = listener
		endpoint.Address = address.IP.String()
		result = append(result, endpoint)
	}
	return listeners, result, nil
}

// Rebind binds every recorded endpoint at the address it recorded, as a
// control-plane process that starts again serves the endpoints its state
// reported.
func Rebind(owner string, endpoints []Endpoint) (map[string]net.Listener, error) {
	listeners := map[string]net.Listener{}
	for _, endpoint := range endpoints {
		listener, err := dbengine.ListenLoopbackAt(Identifier(owner, endpoint), endpoint.Address, endpoint.PortOrDefault())
		if err != nil {
			CloseListeners(listeners)
			return nil, fmt.Errorf("rebind %s at %s: %w", owner, endpoint.Address, err)
		}
		listeners[endpoint.Key()] = listener
	}
	return listeners, nil
}

// CloseListeners closes every listener of a set the plane does not serve.
func CloseListeners(listeners map[string]net.Listener) {
	for _, listener := range listeners {
		_ = listener.Close()
	}
}

// AddEndpoints records endpoints as the plane's own, alongside what it
// already serves.
func (p *Plane) AddEndpoints(endpoints ...Endpoint) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, endpoint := range endpoints {
		if endpoint.Role == NodeEndpoint {
			if n, ok := p.nodes[endpoint.Node]; ok {
				n.announce = endpoint.Address
			}
		}
	}
	p.endpoints = append(p.endpoints, endpoints...)
}

// ServeEndpoints serves each endpoint the plane records from the listener
// bound for it.
func (p *Plane) ServeEndpoints(listeners map[string]net.Listener) {
	for _, endpoint := range p.Endpoints() {
		listener, ok := listeners[endpoint.Key()]
		if !ok {
			continue
		}
		switch endpoint.Role {
		case PrimaryEndpoint:
			p.serve(endpoint, listener, p.primaryTarget)
		case ReadEndpoint:
			p.serve(endpoint, listener, p.readTarget)
		case DiscoveryEndpoint:
			p.serve(endpoint, listener, p.discoveryTarget)
		case NodeEndpoint:
			p.serve(endpoint, listener, fixedTarget(endpoint.Node))
		}
	}
}

// serve relays every connection listener accepts for endpoint to the node
// target names.
func (p *Plane) serve(endpoint Endpoint, listener net.Listener, target func() (int, bool)) {
	p.mu.Lock()
	p.listeners[endpoint.Key()] = listener
	p.mu.Unlock()
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go p.relayConnection(client, target, endpoint.Plaintext)
		}
	}()
}

func (p *Plane) stopServing(key string) {
	p.mu.Lock()
	listener := p.listeners[key]
	delete(p.listeners, key)
	p.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
}

// CloseEndpoint stops serving the endpoint with key and forgets it. The
// loopback address stays with the owner, whose other endpoints may share it.
func (p *Plane) CloseEndpoint(key string) {
	p.stopServing(key)
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := p.endpoints[:0]
	for _, endpoint := range p.endpoints {
		if endpoint.Key() != key {
			kept = append(kept, endpoint)
		}
	}
	p.endpoints = kept
}

func (p *Plane) primaryTarget() (int, bool) { return p.Primary(), true }

// readTarget spreads connections over the replicas, which is what the read
// endpoint balances across, or reaches the primary of an instance with none
// when the plane's Config says so.
func (p *Plane) readTarget() (int, bool) {
	replicas := p.Replicas()
	if len(replicas) == 0 {
		return p.Primary(), p.cfg.ReadFromPrimaryWithoutReplicas
	}
	return replicas[int(p.readCursor.Add(1)-1)%len(replicas)], true
}

// discoveryTarget is the node a cluster's discovery endpoint reaches; every
// node answers a client's topology queries.
func (p *Plane) discoveryTarget() (int, bool) {
	order := p.Nodes()
	if len(order) == 0 {
		return 0, false
	}
	return order[int(p.readCursor.Add(1)-1)%len(order)], true
}

func fixedTarget(node int) func() (int, bool) {
	return func() (int, bool) { return node, true }
}

// SetReadEndpoint starts or stops serving an instance's read endpoint and
// returns its address.
func (p *Plane) SetReadEndpoint(enabled bool, port int) (string, error) {
	p.mu.RLock()
	var current string
	for _, endpoint := range p.endpoints {
		if endpoint.Role == ReadEndpoint {
			current = endpoint.Address
		}
	}
	p.mu.RUnlock()
	if !enabled {
		p.stopServing(string(ReadEndpoint))
		p.mu.Lock()
		kept := p.endpoints[:0]
		for _, endpoint := range p.endpoints {
			if endpoint.Role != ReadEndpoint {
				kept = append(kept, endpoint)
			}
		}
		p.endpoints = kept
		p.mu.Unlock()
		return "", nil
	}
	if current != "" {
		return current, nil
	}
	endpoint := Endpoint{Role: ReadEndpoint, Port: port}
	listener, ip, err := Listen(Identifier(p.cfg.Owner, endpoint), port)
	if err != nil {
		return "", err
	}
	endpoint.Address = ip
	p.mu.Lock()
	p.endpoints = append(p.endpoints, endpoint)
	p.mu.Unlock()
	p.serve(endpoint, listener, p.readTarget)
	return ip, nil
}

func (p *Plane) relayConnection(client net.Conn, target func() (int, bool), plaintext bool) {
	defer client.Close()
	p.mu.RLock()
	tlsConfig := p.tlsConfig
	p.mu.RUnlock()
	if tlsConfig != nil && !plaintext {
		secured, ok := terminateTLS(client, tlsConfig, p.cfg.TLSPreferred)
		if !ok {
			return
		}
		client = secured
	}
	if err := p.Ensure(); err != nil {
		log.Printf("%s data plane: %v", p.cfg.Name, err)
		return
	}
	if diversion := p.divertedTo(client); diversion != nil {
		p.relaySequential(client, nil, diversion, false)
		return
	}
	index, ok := target()
	if !ok {
		return
	}
	address, err := p.nodeAddress(index)
	if err != nil {
		log.Printf("%s data plane: %v", p.cfg.Name, err)
		return
	}
	backend, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		log.Printf("%s data plane: dial node %d: %v", p.cfg.Name, index, err)
		return
	}
	defer backend.Close()
	rewrite := p.cfg.Cluster && !p.announcesHostnames.Load()
	if p.refusesWrites() {
		p.relaySequential(client, backend, nil, rewrite)
		return
	}
	defer p.relayedPlain(client)()
	if p.cfg.ExchangeCredential != nil || rewrite {
		p.relayCommands(client, backend, rewrite)
		return
	}
	Relay(client, backend)
}

// tlsRecordHandshake opens every TLS record that carries a ClientHello.
const tlsRecordHandshake = 0x16

// terminateTLS completes the TLS handshake of a client. With preferred, a
// client whose first byte opens no TLS record stays in plaintext, as an
// endpoint accepting both encrypted and unencrypted connections serves it.
func terminateTLS(client net.Conn, config *tls.Config, preferred bool) (net.Conn, bool) {
	if err := client.SetDeadline(time.Now().Add(handshakeDeadline)); err != nil {
		return nil, false
	}
	if preferred {
		peeked := &peekedConn{Conn: client, reader: bufio.NewReader(client)}
		first, err := peeked.reader.Peek(1)
		if err != nil {
			return nil, false
		}
		client = peeked
		if first[0] != tlsRecordHandshake {
			return client, client.SetDeadline(time.Time{}) == nil
		}
	}
	server := tls.Server(client, config)
	if err := server.Handshake(); err != nil {
		return nil, false
	}
	if err := server.SetDeadline(time.Time{}); err != nil {
		return nil, false
	}
	return server, true
}

// peekedConn reads what a peek buffered before the rest of the connection.
type peekedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *peekedConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

func (c *peekedConn) CloseWrite() error {
	if closer, ok := c.Conn.(halfCloser); ok {
		return closer.CloseWrite()
	}
	return nil
}

type halfCloser interface {
	CloseWrite() error
}

// Relay copies both directions until either side finishes, half-closing the
// other side's write direction so a client's EOF reaches the engine.
func Relay(left, right net.Conn) {
	done := make(chan struct{}, 2)
	copySide := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if closer, ok := dst.(halfCloser); ok {
			_ = closer.CloseWrite()
		}
		done <- struct{}{}
	}
	go copySide(left, right)
	go copySide(right, left)
	<-done
}

// relayCommands reads each command the client sends. An AUTH, or a HELLO
// that authenticates, whose password the plane's ExchangeCredential accepts
// reaches the engine as the user it names carrying the engine's own
// credential; any other credential reaches the engine as the client sent it,
// which the engine refuses, so replies keep the order of the commands. With
// rewrite, the replies that name nodes (a MOVED or ASK redirection, and the
// cluster topology commands' answers) name each node's endpoint instead of
// its network address.
func (p *Plane) relayCommands(client, backend net.Conn, rewrite bool) {
	commands := &commandQueue{}
	expiring := &expiringConn{Conn: client}
	client = expiring
	clientAddress := addrPort(client.RemoteAddr()).Addr()
	done := make(chan struct{}, 2)
	go func() {
		if rewrite {
			_ = relayReplies(client, backend, commands, p.endpointAddresses)
		} else {
			_, _ = io.Copy(client, backend)
		}
		if closer, ok := client.(halfCloser); ok {
			_ = closer.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		defer func() {
			if closer, ok := backend.(halfCloser); ok {
				_ = closer.CloseWrite()
			}
			done <- struct{}{}
		}()
		reader := bufio.NewReader(client)
		for {
			args, raw, err := ReadCommand(reader)
			if err != nil {
				return
			}
			if p.cfg.ExchangeCredential != nil {
				if rewritten, grant, ok := ExchangeCredential(args, p.Password(), clientAddress, p.cfg.ExchangeCredential); ok {
					raw = EncodeCommand(rewritten)
					expiring.extend(grant.Lifetime)
				}
			}
			if rewrite {
				commands.push(args)
			}
			if _, err := backend.Write(raw); err != nil {
				return
			}
		}
	}()
	<-done
}

// ExchangeCredential rewrites an AUTH, or a HELLO with AUTH, whose credential
// authorize accepts, to authenticate as the engine user authorize names.
func ExchangeCredential(args []string, secret string, client netip.Addr, authorize func(Credential) (Grant, bool)) ([]string, Grant, bool) {
	if len(args) == 0 {
		return nil, Grant{}, false
	}
	switch strings.ToUpper(args[0]) {
	case "AUTH":
		if len(args) < 2 || len(args) > 3 {
			return nil, Grant{}, false
		}
		username := "default"
		if len(args) == 3 {
			username = args[1]
		}
		grant, ok := authorize(Credential{Username: username, Password: args[len(args)-1], Client: client})
		if !ok {
			return nil, Grant{}, false
		}
		return []string{args[0], grant.User, secret}, grant, true
	case "HELLO":
		for i := 1; i+2 < len(args); i++ {
			if !strings.EqualFold(args[i], "AUTH") {
				continue
			}
			grant, ok := authorize(Credential{Username: args[i+1], Password: args[i+2], Client: client})
			if !ok {
				return nil, Grant{}, false
			}
			rewritten := append([]string(nil), args...)
			rewritten[i+1], rewritten[i+2] = grant.User, secret
			return rewritten, grant, true
		}
	}
	return nil, Grant{}, false
}

// expiringConn closes once the lifetime the last accepted credential granted
// runs out; a credential granted for good stops the clock.
type expiringConn struct {
	net.Conn
	mu    sync.Mutex
	timer *time.Timer
}

func (c *expiringConn) extend(lifetime time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if lifetime > 0 {
		c.timer = time.AfterFunc(lifetime, func() { _ = c.Close() })
	}
}

func (c *expiringConn) CloseWrite() error {
	if closer, ok := c.Conn.(halfCloser); ok {
		return closer.CloseWrite()
	}
	return nil
}

func nodeEndpointKey(index int) string { return "node-" + strconv.Itoa(index) }

func addrPort(addr net.Addr) netip.AddrPort {
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return netip.AddrPort{}
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}
