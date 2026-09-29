package lbplane

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// ProxyTarget resolves the address one accepted connection is forwarded to. A
// load balancer picks the target per connection, so registration and health
// changes apply to the next connection without restarting the proxy.
type ProxyTarget func(context.Context) (string, error)

// TCPProxy forwards every connection it accepts to the address its target
// resolves, copying bytes both ways until either peer closes.
type TCPProxy struct {
	Address string
	ln      net.Listener
	target  ProxyTarget
	done    chan struct{}
	once    sync.Once

	// Close tracks the handlers, not only the accept loop: a handler still
	// resolving its target reads the caller's stores, and a caller that closes
	// the proxy and moves on must not race it.
	mu       sync.Mutex
	closing  bool
	conns    map[net.Conn]struct{}
	handlers sync.WaitGroup
}

// StartTCPProxy binds listenAddress and proxies each connection as raw TCP.
func StartTCPProxy(listenAddress string, target ProxyTarget) (*TCPProxy, error) {
	ln, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return nil, err
	}
	return ServeTCPProxy(ln, target)
}

// StartTLSProxy binds listenAddress, terminates TLS with config, and proxies the
// decrypted byte stream of each connection to its target.
func StartTLSProxy(listenAddress string, config *tls.Config, target ProxyTarget) (*TCPProxy, error) {
	if config == nil {
		return nil, fmt.Errorf("a TLS proxy needs a TLS configuration")
	}
	ln, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return nil, err
	}
	return ServeTCPProxy(tls.NewListener(ln, config), target)
}

// ServeTCPProxy proxies the connections ln accepts and owns ln from then on.
func ServeTCPProxy(ln net.Listener, target ProxyTarget) (*TCPProxy, error) {
	if target == nil {
		_ = ln.Close()
		return nil, fmt.Errorf("proxy target resolver is required")
	}
	p := &TCPProxy{
		Address: ln.Addr().String(),
		ln:      ln,
		target:  target,
		done:    make(chan struct{}),
		conns:   map[net.Conn]struct{}{},
	}
	go p.serve()
	return p, nil
}

// Close stops the proxy and returns once no handler is still running. In-flight
// client connections are closed rather than waited out: a proxied stream can
// last for hours by design, so waiting for one to end would make Close block
// for as long as its busiest connection.
func (p *TCPProxy) Close() error {
	var err error
	p.once.Do(func() {
		err = p.ln.Close()
		// The accept loop has stopped, so no further handler can be added and
		// the wait below cannot race an Add.
		<-p.done

		p.mu.Lock()
		p.closing = true
		for conn := range p.conns {
			_ = conn.Close()
		}
		p.mu.Unlock()

		p.handlers.Wait()
	})
	return err
}

// track registers a client connection, reporting false if the proxy is already
// closing — in which case the handler must not start.
func (p *TCPProxy) track(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing {
		return false
	}
	p.conns[conn] = struct{}{}
	return true
}

func (p *TCPProxy) untrack(conn net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.conns, conn)
}

func (p *TCPProxy) serve() {
	defer close(p.done)
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.handlers.Add(1)
		go func() {
			defer p.handlers.Done()
			p.handle(conn)
		}()
	}
}

// tlsHandshakeTimeout bounds a client that connects and never finishes the
// handshake, which would otherwise hold a handler for the connection's life.
const tlsHandshakeTimeout = 30 * time.Second

func (p *TCPProxy) handle(client net.Conn) {
	defer client.Close()
	if !p.track(client) {
		return
	}
	defer p.untrack(client)
	// Finish the handshake before choosing a target, so a client that fails it
	// never reaches one.
	if tlsConn, ok := client.(*tls.Conn); ok {
		_ = tlsConn.SetDeadline(time.Now().Add(tlsHandshakeTimeout))
		if err := tlsConn.HandshakeContext(context.Background()); err != nil {
			return
		}
		_ = tlsConn.SetDeadline(time.Time{})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	address, err := p.target(ctx)
	if err != nil {
		return
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	upstream, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return
	}
	defer upstream.Close()
	errs := make(chan error, 2)
	go func() {
		_, err := io.Copy(upstream, client)
		errs <- err
	}()
	go func() {
		_, err := io.Copy(client, upstream)
		errs <- err
	}()
	<-errs
}
