package main

import (
	"context"
	"io"
	"net"
	"net/url"
	"testing"
	"time"
)

// A query sent to one of the sidecar's other local addresses (Azure's
// 168.63.129.16 on its loopback) is answered from that address: a stub
// resolver's connected socket drops an answer from any other source.
func TestRunAnswersUDPFromTheAddressQueried(t *testing.T) {
	upstream, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upstream.Close() }()
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := upstream.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = upstream.WriteTo(append([]byte("udp:"), buf[:n]...), from)
		}
	}()
	httpUpstream, _ := url.Parse("http://127.0.0.1:1")

	boundAddrs = make(chan [2]string, 1)
	defer func() { boundAddrs = nil }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, config{
			dnsListen:    "0.0.0.0:0",
			httpListen:   "127.0.0.1:0",
			dnsUpstream:  upstream.LocalAddr().String(),
			httpUpstream: httpUpstream,
			token:        "t0k3n",
		}, io.Discard)
	}()
	var bound [2]string
	select {
	case bound = <-boundAddrs:
	case err := <-done:
		t.Fatalf("sidecar exited: %v", err)
	}
	_, port, _ := net.SplitHostPort(bound[0])

	conn, err := net.Dial("udp4", net.JoinHostPort("127.0.0.2", port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write([]byte("q"))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "udp:q" {
		t.Fatalf("answer %q, %v: the reply did not come from the address queried", buf[:n], err)
	}
}
