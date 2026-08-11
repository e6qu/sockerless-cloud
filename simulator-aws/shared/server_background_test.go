package simulator

import (
	"context"
	"testing"
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
	srv.StartBackground(func(ctx context.Context) {
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
