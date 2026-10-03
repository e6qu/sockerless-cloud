package sim

import (
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"
)

// A request blocked on its own context, as every long poll is, ends when the
// server shuts down instead of holding the drain to its bound.
func TestServerShutdownEndsAnOpenLongPoll(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(Config{Provider: "shutdown-long-poll-test", ListenAddr: addr})
	if err != nil {
		t.Fatal(err)
	}
	const pollLimit = time.Minute
	polling := make(chan struct{})
	srv.HandleFunc("GET /poll", func(w http.ResponseWriter, r *http.Request) {
		close(polling)
		limit := time.NewTimer(pollLimit)
		defer limit.Stop()
		select {
		case <-r.Context().Done():
		case <-limit.C:
		}
		w.WriteHeader(http.StatusOK)
	})

	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe() }()

	var conn net.Conn
	for deadline := time.Now().Add(10 * time.Second); conn == nil; {
		conn, err = net.Dial("tcp", addr)
		if err != nil {
			if time.Now().After(deadline) {
				t.Fatalf("server never answered on %s: %v", addr, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := io.WriteString(conn, "GET /poll HTTP/1.1\r\nHost: "+addr+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-polling:
	case err := <-served:
		t.Fatalf("server stopped before the poll arrived: %v", err)
	}

	stopping := time.Now()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	const bound = 2 * time.Second
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("ListenAndServe: %v", err)
		}
		if took := time.Since(stopping); took > bound {
			t.Fatalf("shutdown with an open long poll took %s, want under %s", took, bound)
		}
		t.Logf("shutdown with an open long poll took %s", time.Since(stopping))
	case <-time.After(pollLimit):
		t.Fatalf("server still serving %s after SIGTERM", pollLimit)
	}
}
