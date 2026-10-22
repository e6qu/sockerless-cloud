package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServeForwardRelaysALoopbackPortToItsUpstream starts the forwarder on a
// kernel-chosen loopback port in front of an HTTP upstream and checks that a
// request to the loopback port reaches the upstream unchanged, its Host the
// loopback address the client named.
func TestServeForwardRelaysALoopbackPortToItsUpstream(t *testing.T) {
	var gotHost, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotPath = r.Host, r.URL.Path
		_, _ = io.WriteString(w, `{"AccessKeyId":"ASIA"}`)
	}))
	defer upstream.Close()
	upstreamAddr := strings.TrimPrefix(upstream.URL, "http://")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serveForward(ctx, writer, []string{"127.0.0.1:0=" + upstreamAddr})
		_ = writer.Close()
	}()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("forwarder wrote no listen line: %v (%v)", err, <-done)
	}
	listen, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "sidecar: forward "), " -> ")
	if !ok {
		t.Fatalf("listen line %q", line)
	}
	go func() { _, _ = io.Copy(io.Discard, out) }()

	resp, err := http.Get("http://" + listen + "/v2/credentials/abc")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != `{"AccessKeyId":"ASIA"}` {
		t.Fatalf("body %q", body)
	}
	if gotHost != listen || gotPath != "/v2/credentials/abc" {
		t.Fatalf("upstream saw host %q path %q, want %q /v2/credentials/abc", gotHost, gotPath, listen)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serveForward: %v", err)
	}
}

func TestParseForwardsRefusesAMalformedPair(t *testing.T) {
	for _, args := range [][]string{nil, {"127.0.0.1:1"}, {"127.0.0.1=10.0.0.2:80"}, {"127.0.0.1:1=10.0.0.2"}} {
		if _, err := parseForwards(args); err == nil {
			t.Errorf("parseForwards(%q) accepted", args)
		}
	}
}
