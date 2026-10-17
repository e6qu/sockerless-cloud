package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestRunForwardsDNSAndHTTPWithTheToken starts the sidecar on kernel-chosen
// ports against an in-process upstream of each kind and checks that a UDP
// query, a TCP query and an HTTP request each reach their upstream, the HTTP
// one with the token and the querier's address.
func TestRunForwardsDNSAndHTTPWithTheToken(t *testing.T) {
	udpUpstream, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = udpUpstream.Close() }()
	tcpUpstream, err := net.Listen("tcp4", udpUpstream.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tcpUpstream.Close() }()
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := udpUpstream.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = udpUpstream.WriteTo(append([]byte("udp:"), buf[:n]...), from)
		}
	}()
	go func() {
		for {
			conn, err := tcpUpstream.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				buf := make([]byte, 512)
				n, _ := conn.Read(buf)
				_, _ = conn.Write(append([]byte("tcp:"), buf[:n]...))
			}()
		}
	}()
	var gotToken, gotClient, gotHost string
	httpUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get(tokenHeader)
		gotClient = r.Header.Get(clientHeader)
		gotHost = r.Host
		_, _ = io.WriteString(w, "metadata")
	}))
	defer httpUpstream.Close()
	upstreamURL, _ := url.Parse(httpUpstream.URL)

	boundAddrs = make(chan [2]string, 1)
	defer func() { boundAddrs = nil }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, config{
			dnsListen:    "127.0.0.1:0",
			httpListen:   "127.0.0.1:0",
			dnsUpstream:  udpUpstream.LocalAddr().String(),
			httpUpstream: upstreamURL,
			token:        "t0k3n",
		}, io.Discard)
	}()
	var bound [2]string
	select {
	case bound = <-boundAddrs:
	case err := <-done:
		t.Fatalf("sidecar exited: %v", err)
	}

	conn, err := net.Dial("udp4", bound[0])
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write([]byte("q1"))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "udp:q1" {
		t.Fatalf("udp answer %q, %v", buf[:n], err)
	}
	_ = conn.Close()

	tconn, err := net.Dial("tcp4", bound[0])
	if err != nil {
		t.Fatal(err)
	}
	_ = tconn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = tconn.Write([]byte("q2"))
	n, err = tconn.Read(buf)
	if err != nil || string(buf[:n]) != "tcp:q2" {
		t.Fatalf("tcp answer %q, %v", buf[:n], err)
	}
	_ = tconn.Close()

	req, _ := http.NewRequest(http.MethodGet, "http://"+bound[1]+"/latest/meta-data/", nil)
	req.Host = "169.254.169.254"
	req.Header.Set(tokenHeader, "forged")
	req.Header.Set(clientHeader, "10.9.9.9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "metadata" {
		t.Fatalf("http body %q", body)
	}
	if gotToken != "t0k3n" || gotClient != "127.0.0.1" || gotHost != "169.254.169.254" {
		t.Fatalf("upstream saw token %q client %q host %q", gotToken, gotClient, gotHost)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}
