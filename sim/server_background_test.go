package sim

import (
	"context"
	"testing"
	"time"
)

func TestServerDrainsBackgroundWorkersBeforeClosingSQLite(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	dataDir := t.TempDir()
	srv, err := NewServer(Config{
		Provider: "background-shutdown-test",
		Persist:  true,
		DataDir:  dataDir,
	})
	if err != nil {
		t.Fatalf("new persistent server: %v", err)
	}

	store := MakeStore[string](srv.DB(), "background_shutdown")
	started := make(chan struct{})
	srv.StartBackground("test worker", func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		store.Put("worker", "drained")
	})
	<-started

	srv.stopBackground()
	if err := CloseDB(srv.DB()); err != nil {
		t.Fatalf("close database after draining workers: %v", err)
	}
	srv.db = nil

	reopened, err := OpenDB(dataDir)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB(reopened) })
	reopenedStore := MakeStore[string](reopened, "background_shutdown")
	value, ok := reopenedStore.Get("worker")
	if !ok {
		t.Fatal("background worker did not persist its shutdown state")
	}
	if value != "drained" {
		t.Fatalf("shutdown state = %q, want drained", value)
	}
}

// A worker that does not return when cancelled holds shutdown up; the server
// names it while it waits, so a stalled stop says what it is waiting on.
func TestServerNamesTheBackgroundWorkersItWaitsOn(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := NewServer(Config{Provider: "background-report-test"})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{})
	srv.StartBackground("stubborn worker", func(context.Context) {
		close(started)
		<-release
	})
	srv.StartBackground("stubborn worker", func(context.Context) { <-release })
	srv.StartBackground("polite worker", func(ctx context.Context) { <-ctx.Done() })
	<-started

	stopped := make(chan struct{})
	go func() {
		srv.stopBackground()
		close(stopped)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for srv.runningBackground() != "stubborn worker x2" {
		if time.Now().After(deadline) {
			t.Fatalf("running workers = %q, want only the two that ignore cancellation", srv.runningBackground())
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stopBackground did not return once the workers did")
	}
	if running := srv.runningBackground(); running != "" {
		t.Fatalf("workers still counted after they returned: %q", running)
	}
}
