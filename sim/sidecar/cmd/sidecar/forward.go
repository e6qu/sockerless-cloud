package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// forwardDialTimeout bounds the connect to an upstream on the workload's
// own network.
const forwardDialTimeout = 5 * time.Second

type forwardPair struct {
	listen   string
	upstream string
}

func parseForwards(args []string) ([]forwardPair, error) {
	if len(args) == 0 {
		return nil, errors.New("usage: sidecar forward <listen>=<upstream> [<listen>=<upstream>]")
	}
	pairs := make([]forwardPair, 0, len(args))
	for _, arg := range args {
		listen, upstream, ok := strings.Cut(arg, "=")
		if !ok {
			return nil, fmt.Errorf("forward %q: want <listen>=<upstream>", arg)
		}
		for _, addr := range []string{listen, upstream} {
			if _, _, err := net.SplitHostPort(addr); err != nil {
				return nil, fmt.Errorf("forward %q: %w", arg, err)
			}
		}
		pairs = append(pairs, forwardPair{listen: listen, upstream: upstream})
	}
	return pairs, nil
}

// serveForward listens on each pair's address in the network namespace this
// program owns and relays every connection to the pair's upstream byte for
// byte until ctx ends: a workload joined to the namespace reaches an
// endpoint of its network on its own loopback, where a client that accepts
// plain HTTP only on a loopback address takes it.
func serveForward(ctx context.Context, out io.Writer, args []string) error {
	pairs, err := parseForwards(args)
	if err != nil {
		return err
	}
	listeners := make([]net.Listener, 0, len(pairs))
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	for _, pair := range pairs {
		l, err := net.Listen("tcp", pair.listen)
		if err != nil {
			return fmt.Errorf("listen forward %s: %w", pair.listen, err)
		}
		listeners = append(listeners, l)
	}
	relays := make([]string, len(pairs))
	for i, pair := range pairs {
		relays[i] = listeners[i].Addr().String() + " -> " + pair.upstream
	}
	fmt.Fprintf(out, "sidecar: forward %s\n", strings.Join(relays, ", "))
	var wg sync.WaitGroup
	for i, pair := range pairs {
		wg.Add(1)
		go func() { defer wg.Done(); forwardConnections(ctx, listeners[i], pair.upstream) }()
	}
	<-ctx.Done()
	for _, l := range listeners {
		_ = l.Close()
	}
	wg.Wait()
	return nil
}

func forwardConnections(ctx context.Context, l net.Listener, upstream string) {
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
			up, err := net.DialTimeout("tcp", upstream, forwardDialTimeout)
			if err != nil {
				return
			}
			defer func() { _ = up.Close() }()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(up, conn); closeWrite(up); done <- struct{}{} }()
			go func() { _, _ = io.Copy(conn, up); closeWrite(conn); done <- struct{}{} }()
			<-done
			<-done
		}()
	}
}

// closeWrite passes a half-close on, so a client that shuts its sending side
// down still reads the whole answer.
func closeWrite(c net.Conn) {
	if tcp, ok := c.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
}
