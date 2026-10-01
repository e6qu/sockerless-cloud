package workload

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestPostBootstrapSurfacesATruncatedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer cannot be hijacked")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	body, code, err := PostBootstrap(context.Background(), srv.URL, nil, "", 5*time.Second)
	if err == nil {
		t.Fatalf("a response cut off after %q was returned as complete (exit %d)", body, code)
	}
	if !strings.Contains(err.Error(), "read bootstrap response") {
		t.Fatalf("error does not name the failed read: %v", err)
	}
}

func TestPostBootstrapReportsTheExitCode(t *testing.T) {
	cases := []struct {
		header string
		status int
		want   int
	}{
		{"", http.StatusOK, 0},
		{"", http.StatusInternalServerError, 1},
		{"7", http.StatusOK, 7},
		{"0", http.StatusBadGateway, 0},
		{"not-a-number", http.StatusBadRequest, 1},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type %q, want the application/json default", got)
			}
			if tc.header != "" {
				w.Header().Set(ExitCodeHeader, tc.header)
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte("out"))
		}))
		body, code, err := PostBootstrap(context.Background(), srv.URL, strings.NewReader("{}"), "", 5*time.Second)
		srv.Close()
		if err != nil || code != tc.want || string(body) != "out" {
			t.Errorf("header %q status %d: body %q code %d err %v, want code %d", tc.header, tc.status, body, code, err, tc.want)
		}
	}
}

// refusingFirstListener drops the first connection it accepts, the way a
// bootstrap's published port answers before the listener behind it is up.
type refusingFirstListener struct {
	net.Listener
	accepted atomic.Int32
}

func (l *refusingFirstListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.accepted.Add(1) > 1 {
			return conn, nil
		}
		_ = conn.Close()
	}
}

func TestPostBootstrapRetriesUntilTheListenerIsUp(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &refusingFirstListener{Listener: inner}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("up"))
	})}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	body, code, err := PostBootstrap(context.Background(), "http://"+inner.Addr().String()+"/", nil, "", 5*time.Second)
	if err != nil || code != 0 || string(body) != "up" {
		t.Fatalf("body %q code %d err %v", body, code, err)
	}
	if got := listener.accepted.Load(); got < 2 {
		t.Fatalf("the bootstrap answered on connection %d; PostBootstrap did not retry the dropped first connection", got)
	}
}

// unreachableURL names port 0, which no listener can hold, so every dial to it
// fails.
const unreachableURL = "http://127.0.0.1:0/"

func TestPostBootstrapStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := PostBootstrap(ctx, unreachableURL, nil, "", time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v, want the context's deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("PostBootstrap kept retrying for %s after its context ended", elapsed)
	}
}

func TestFirstReachableReturnsTheFirstListeningCandidate(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	live := "http://" + l.Addr().String()
	got, err := FirstReachable(context.Background(),
		[]string{unreachableURL, "http:///no-host", live}, 5*time.Second)
	if err != nil || got != live {
		t.Fatalf("got %q, %v; want %q", got, err, live)
	}
}

func TestDialAddressDefaultsThePortByScheme(t *testing.T) {
	for raw, want := range map[string]string{
		"http://10.0.0.1":        "10.0.0.1:80",
		"https://example.test":   "example.test:443",
		"http://10.0.0.1:8080/x": "10.0.0.1:8080",
		"https://[::1]":          "[::1]:443",
	} {
		got, err := dialAddress(raw)
		if err != nil || got != want {
			t.Errorf("dialAddress(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := dialAddress("http://"); err == nil {
		t.Error("a URL without a host was accepted")
	}
}

func TestGroupMembersCannotChooseTheirPlatformOrNetwork(t *testing.T) {
	_, err := StartGroup(context.Background(),
		Container{Name: "app", Config: sim.ContainerConfig{Image: "img", Architecture: "linux/amd64"}}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "names its platform") {
		t.Fatalf("a member naming its own platform was started: %v", err)
	}
	_, err = StartSidecars(context.Background(), "main-id",
		[]Container{{Name: "proxy", Config: sim.ContainerConfig{Image: "img", Network: "vpc"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "names its own network") {
		t.Fatalf("a sidecar naming its own network was started: %v", err)
	}
}

func TestStoppingANilGroupIsANoOp(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("stopping a nil group panicked: %v", r)
		}
	}()
	var g *Group
	g.Stop(time.Second)
}
