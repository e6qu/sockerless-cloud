package realexec

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type ProbeSpec struct {
	Protocol string
	Address  string
	Path     string
	Timeout  time.Duration
}

func ProbeTarget(ctx context.Context, spec ProbeSpec) error {
	if spec.Timeout <= 0 {
		spec.Timeout = 2 * time.Second
	}
	switch spec.Protocol {
	case "HTTP", "http":
		path := spec.Path
		if path == "" {
			path = "/"
		}
		u := url.URL{Scheme: "http", Host: spec.Address, Path: path}
		reqCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
		if err != nil {
			return err
		}
		client := http.Client{Timeout: spec.Timeout}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP health check returned %s", resp.Status)
		}
		return nil
	default:
		dialer := net.Dialer{Timeout: spec.Timeout}
		conn, err := dialer.DialContext(ctx, "tcp", spec.Address)
		if err != nil {
			return err
		}
		return conn.Close()
	}
}

type ProxyTarget func(context.Context) (string, error)

type TCPProxy struct {
	Address string
	ln      net.Listener
	target  ProxyTarget
	done    chan struct{}
	once    sync.Once

	// A closed proxy must have stopped resolving targets, not merely stopped
	// accepting connections. Close used to wait for the accept loop alone,
	// while the handlers it had already spawned kept calling the caller's
	// resolver — which in the AWS simulator reads the load-balancer and target
	// stores. A test that closed its proxy and moved on therefore raced its own
	// teardown, and the race detector caught exactly that. These track the
	// handlers so Close can mean what it says.
	mu       sync.Mutex
	closing  bool
	conns    map[net.Conn]struct{}
	handlers sync.WaitGroup
}

func StartTCPProxy(listenAddress string, target ProxyTarget) (*TCPProxy, error) {
	if target == nil {
		return nil, fmt.Errorf("proxy target resolver is required")
	}
	ln, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return nil, err
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

func (p *TCPProxy) handle(client net.Conn) {
	defer client.Close()
	if !p.track(client) {
		return
	}
	defer p.untrack(client)
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
