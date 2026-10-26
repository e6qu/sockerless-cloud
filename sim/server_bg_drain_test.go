package sim

import (
	"context"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Work handed to package bg that still runs when the shutdown signal arrives
// writes its store before the database closes; a write after the close would
// panic the simulator on its way out.
func TestServerDrainsBackgroundWorkBeforeClosingTheDatabase(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Config{Provider: "bg-drain-test", ListenAddr: addr, Persist: true, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := MakeStore[int](srv.DB(), "bg_drain")
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

	running, release := make(chan struct{}), make(chan struct{})
	bg.Go(func() {
		close(running)
		<-release
		store.Put("late", 1)
	})
	<-running
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		t.Fatalf("the server shut down while background work was still running: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("ListenAndServe: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the server did not shut down")
	}
}

// A drain must see work handed to the server's own lifecycle, or it returns
// while that work still reads the stores the next test replaces.
func TestAwaitDrainsServerLifecycleWork(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := NewServer(Config{Provider: "bg-test", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new sim server: %v", err)
	}
	t.Cleanup(srv.StopBackground)

	var finished atomic.Bool
	started := make(chan struct{})
	run, ok := bg.Handoff(func() {
		close(started)
		time.Sleep(250 * time.Millisecond)
		finished.Store(true)
	})
	if !ok {
		t.Fatal("work offered outside a drain was refused")
	}
	srv.StartBackground("test lifecycle step", func(context.Context) { run() })
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the background worker never started")
	}

	bg.Await()

	if !finished.Load() {
		t.Fatal("Await returned while work on the server's lifecycle was still running")
	}
}
