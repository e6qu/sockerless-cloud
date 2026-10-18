package sim

import (
	"context"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

// What a program starts beside the HTTP port stops while the database is
// still open: a stop that lists a store after the database closed was a panic
// on the way out, and the harness that restarted the simulator then found it
// had not exited cleanly.
func TestServerStopsItsListenersBeforeClosingTheDatabase(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Config{Provider: "stop-order-test", ListenAddr: addr, Persist: true, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if srv.DB() == nil {
		t.Fatal("a persistent server opens its database")
	}
	store := MakeStore[int](srv.DB(), "stop_order")
	store.Put("one", 1)

	var listed int
	srv.OnStop(func(context.Context) error {
		listed = len(store.List())
		return nil
	})
	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe() }()
	for deadline := time.Now().Add(10 * time.Second); ; {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never answered on %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("ListenAndServe: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the server did not shut down")
	}
	if listed != 1 {
		t.Fatalf("the stop hook listed %d rows, want the one the open database holds", listed)
	}
}
