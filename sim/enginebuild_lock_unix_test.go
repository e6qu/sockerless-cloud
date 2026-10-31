//go:build unix

package sim

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// A build lock held for one tag keeps another holder, here a second open of
// the same file as another process makes, out until it is released.
func TestEngineBuildLockExcludesAnotherHolder(t *testing.T) {
	const image = "sockerless-lock-test:0123456789abcdef"
	release, err := lockEngineBuild(image)
	if err != nil {
		t.Fatal(err)
	}
	other, err := os.Open(filepath.Join(os.TempDir(), "sockerless-engine-build-sockerless-lock-test_0123456789abcdef.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := unix.Flock(int(other.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("a second holder took the held lock: %v", err)
	}
	release()
	if err := unix.Flock(int(other.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("the released lock is still held: %v", err)
	}
}
