package sim

import (
	"os"
	"testing"
)

// A server that does not persist keeps its payloads in a temporary directory
// and removes it when it stops, so no run leaves its bytes behind.
func TestAnEphemeralPayloadStoreIsRemovedWhenTheServerStops(t *testing.T) {
	t.Parallel()
	srv, err := NewServer(Config{Provider: "aws", LogLevel: "disabled"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Payloads("objects"); err != nil {
		t.Fatal(err)
	}
	srv.ephemeralMu.Lock()
	dirs := append([]string(nil), srv.ephemeralPayloads...)
	srv.ephemeralMu.Unlock()
	if len(dirs) != 1 {
		t.Fatalf("the server holds payload directories %v, want one", dirs)
	}
	if _, err := os.Stat(dirs[0]); err != nil {
		t.Fatalf("the payload directory is missing while the server runs: %v", err)
	}
	srv.StopBackground()
	if _, err := os.Stat(dirs[0]); !os.IsNotExist(err) {
		t.Fatalf("the payload directory outlived the server: %v", err)
	}
}
