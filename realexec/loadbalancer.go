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
}

func StartTCPProxy(listenAddress string, target ProxyTarget) (*TCPProxy, error) {
	if target == nil {
		return nil, fmt.Errorf("proxy target resolver is required")
	}
	ln, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return nil, err
	}
	p := &TCPProxy{Address: ln.Addr().String(), ln: ln, target: target, done: make(chan struct{})}
	go p.serve()
	return p, nil
}

func (p *TCPProxy) Close() error {
	var err error
	p.once.Do(func() {
		err = p.ln.Close()
		<-p.done
	})
	return err
}

func (p *TCPProxy) serve() {
	defer close(p.done)
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(conn)
	}
}

func (p *TCPProxy) handle(client net.Conn) {
	defer client.Close()
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
