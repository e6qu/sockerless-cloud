package sim

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestScopedDataDirResolutionOrder(t *testing.T) {
	t.Setenv("SIM_TEST_SLICE_DATA_DIR", "")
	t.Setenv("SIM_DATA_DIR", "")
	if got, want := ScopedDataDir("SIM_TEST_SLICE_DATA_DIR", "slice", "sim-slice"), filepath.Join(os.TempDir(), "sim-slice"); got != want {
		t.Fatalf("no configuration: %q, want %q", got, want)
	}
	t.Setenv("SIM_DATA_DIR", "/data")
	if got := ScopedDataDir("SIM_TEST_SLICE_DATA_DIR", "slice", "sim-slice"); got != "/data/slice" {
		t.Fatalf("SIM_DATA_DIR: %q", got)
	}
	t.Setenv("SIM_TEST_SLICE_DATA_DIR", "/override")
	if got := ScopedDataDir("SIM_TEST_SLICE_DATA_DIR", "slice", "sim-slice"); got != "/override" {
		t.Fatalf("override: %q", got)
	}
	if got := ScopedDataDir("", "slice", "sim-slice"); got != "/data/slice" {
		t.Fatalf("no override variable: %q", got)
	}
}

func TestEnsureWritableDirIgnoresTheUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := EnsureWritableDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o777 {
		t.Fatalf("mode %o, want 777", perm)
	}
	if err := EnsureWritableDir(dir); err != nil {
		t.Fatalf("an existing directory: %v", err)
	}
}
