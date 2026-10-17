// Command sidecar stands in for a cloud network's fixed addresses from inside
// the network: it answers DNS at the cloud's resolver address and the
// metadata endpoints at the address the cloud serves them on, and forwards
// both to the simulator that started it. It holds no capability and no secret
// but the token the simulator minted for it.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/procimage"
)

const (
	tokenHeader  = "X-Sockerless-Sidecar-Token"
	clientHeader = "X-Sockerless-Sidecar-Client"
)

// dnsTimeout bounds one forwarded query; a stub resolver retries within a few
// seconds.
const dnsTimeout = 3 * time.Second

type config struct {
	dnsListen    string
	httpListen   string
	dnsUpstream  string
	httpUpstream *url.URL
	token        string
}

func configFromEnv() (config, error) {
	c := config{
		dnsListen:   envOr("SIDECAR_DNS_LISTEN", ":53"),
		httpListen:  envOr("SIDECAR_HTTP_LISTEN", ":80"),
		dnsUpstream: os.Getenv("SIDECAR_DNS_UPSTREAM"),
		token:       os.Getenv("SIDECAR_TOKEN"),
	}
	if _, _, err := net.SplitHostPort(c.dnsUpstream); err != nil {
		return c, fmt.Errorf("SIDECAR_DNS_UPSTREAM %q: %w", c.dnsUpstream, err)
	}
	upstream, err := url.Parse(os.Getenv("SIDECAR_HTTP_UPSTREAM"))
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		return c, fmt.Errorf("SIDECAR_HTTP_UPSTREAM %q is not an http URL", os.Getenv("SIDECAR_HTTP_UPSTREAM"))
	}
	c.httpUpstream = upstream
	if c.token == "" {
		return c, errors.New("SIDECAR_TOKEN is empty")
	}
	return c, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	if len(os.Args) > 1 {
		if err := readProcess(os.Args[1:]); err != nil {
			log.Fatalf("sidecar: %v", err)
		}
		return
	}
	cfg, err := configFromEnv()
	if err != nil {
		log.Fatalf("sidecar: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, cfg, os.Stdout); err != nil {
		log.Fatalf("sidecar: %v", err)
	}
}

// run serves until ctx ends.
func run(ctx context.Context, cfg config, out io.Writer) error {
	udp, err := net.ListenPacket("udp4", cfg.dnsListen)
	if err != nil {
		return fmt.Errorf("listen dns udp %s: %w", cfg.dnsListen, err)
	}
	defer func() { _ = udp.Close() }()
	// A stub resolver retries a truncated UDP answer over TCP on the same
	// port, so the TCP listener takes the port UDP got.
	tcp, err := net.Listen("tcp4", udp.LocalAddr().String())
	if err != nil {
		return fmt.Errorf("listen dns tcp %s: %w", udp.LocalAddr(), err)
	}
	defer func() { _ = tcp.Close() }()
	httpListener, err := net.Listen("tcp4", cfg.httpListen)
	if err != nil {
		return fmt.Errorf("listen http %s: %w", cfg.httpListen, err)
	}
	fmt.Fprintf(out, "sidecar: dns %s http %s -> dns %s http %s\n", udp.LocalAddr(), httpListener.Addr(), cfg.dnsUpstream, cfg.httpUpstream)
	if boundAddrs != nil {
		boundAddrs <- [2]string{udp.LocalAddr().String(), httpListener.Addr().String()}
	}

	server := &http.Server{Handler: proxyHandler(cfg), ReadHeaderTimeout: 10 * time.Second}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); serveUDP(ctx, udp, cfg.dnsUpstream) }()
	go func() { defer wg.Done(); serveTCP(ctx, tcp, cfg.dnsUpstream) }()
	go func() { defer wg.Done(); _ = server.Serve(httpListener) }()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	_ = udp.Close()
	_ = tcp.Close()
	wg.Wait()
	return nil
}

// boundAddrs, when a test sets it, receives the listen addresses.
var boundAddrs chan [2]string

// serveUDP relays each datagram to the upstream over a socket of its own, so
// answers cannot cross between queriers, and sends the answer back to the
// querier that asked.
func serveUDP(ctx context.Context, pc net.PacketConn, upstream string) {
	buf := make([]byte, 65535)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		query := append([]byte(nil), buf[:n]...)
		go func() {
			answer, err := exchangeUDP(upstream, query)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(answer, from)
		}()
	}
}

func exchangeUDP(upstream string, query []byte) ([]byte, error) {
	conn, err := net.DialTimeout("udp4", upstream, dnsTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(dnsTimeout))
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	answer := make([]byte, 65535)
	n, err := conn.Read(answer)
	if err != nil {
		return nil, err
	}
	return answer[:n], nil
}

// serveTCP relays each connection to the upstream byte for byte: DNS over TCP
// frames its messages itself.
func serveTCP(ctx context.Context, l net.Listener, upstream string) {
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go func() {
			defer func() { _ = conn.Close() }()
			up, err := net.DialTimeout("tcp4", upstream, dnsTimeout)
			if err != nil {
				return
			}
			defer func() { _ = up.Close() }()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(up, conn); done <- struct{}{} }()
			go func() { _, _ = io.Copy(conn, up); done <- struct{}{} }()
			<-done
		}()
	}
}

// proxyHandler forwards every request to the simulator with the sidecar's
// token and the querier's address, and nothing a querier set under those
// names: the simulator trusts the pair only together.
func proxyHandler(cfg config) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(cfg.httpUpstream)
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Del(tokenHeader)
			pr.Out.Header.Del(clientHeader)
			pr.Out.Header.Set(tokenHeader, cfg.token)
			if host, _, err := net.SplitHostPort(pr.In.RemoteAddr); err == nil {
				pr.Out.Header.Set(clientHeader, host)
			}
		},
		FlushInterval: -1,
	}
	return proxy
}

// readProcess answers the simulator's read of a workload's process from inside
// the workload's pid namespace, where this program runs with permission to
// trace: "maps <pid>" copies the process's mapping table and "core <pid>"
// writes an ELF core of it, each to the file of that name under /out, the
// directory the simulator mounted to receive it.
func readProcess(args []string) error {
	if len(args) != 2 || (args[0] != "maps" && args[0] != "core") {
		return errors.New("usage: sidecar maps|core <pid>")
	}
	pid, err := strconv.Atoi(args[1])
	if err != nil {
		return fmt.Errorf("pid %q: %w", args[1], err)
	}
	out, err := os.OpenFile("/out/"+args[0], os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	switch args[0] {
	case "maps":
		maps, err := os.Open(fmt.Sprintf("/proc/%d/maps", pid))
		if err != nil {
			return errors.Join(err, out.Close())
		}
		defer maps.Close()
		if _, err := io.Copy(out, maps); err != nil {
			return errors.Join(err, out.Close())
		}
	case "core":
		if err := procimage.DumpProcess(out, pid); err != nil {
			return errors.Join(err, out.Close())
		}
	}
	return out.Close()
}
