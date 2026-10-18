package placement

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestGuestMACFollowsTheCloudsPrefix(t *testing.T) {
	addr := netip.MustParseAddr("10.86.0.7")
	if got := guestMAC("42:01", addr, "gce-boot"); got != "42:01:0a:56:00:07" {
		t.Fatalf("Compute Engine MAC = %s, want the address under 42:01", got)
	}
	aws := guestMAC("0e:a7:7d", addr, "aws-instance-i-1")
	if len(aws) != 17 || aws[:9] != "0e:a7:7d:" {
		t.Fatalf("three-octet prefix MAC = %s", aws)
	}
	if again := guestMAC("0e:a7:7d", netip.MustParseAddr("10.0.0.9"), "aws-instance-i-1"); again != aws {
		t.Fatalf("a hashed MAC changed with the address: %s then %s", aws, again)
	}
}

func TestMatchesLabelsAsksForEveryWantedLabel(t *testing.T) {
	have := map[string]string{"sockerless-sim-state": "abc", "sockerless-sim-instance": "i-1"}
	if !matchesLabels(have, map[string]string{"sockerless-sim-instance": "i-1", "sockerless-sim-state": "abc"}) {
		t.Fatal("every wanted label is there")
	}
	if !matchesLabels(have, map[string]string{"sockerless-sim-instance": ""}) {
		t.Fatal("an empty wanted value asks only for the key")
	}
	if matchesLabels(have, map[string]string{"sockerless-sim-instance": "i-2"}) {
		t.Fatal("a different value must not match")
	}
	if matchesLabels(have, map[string]string{"sockerless-sim-type": "vm"}) {
		t.Fatal("a missing key must not match")
	}
}

// A guest's filesystem is its own disk: a bind of a host directory reaches
// it as a share of that directory, a bind of one host file as a share
// listing that file alone, and a bind that names nothing there is refused
// rather than run without.
func TestMicroVMBackendServesHostPathBindsAsShares(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "one.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var s shares
	mounts, err := s.mountsFor(context.Background(), Workload{Name: "site", Binds: []string{home + ":/home", home + ":/srv/ro:ro", file + ":/etc/one.txt"}}, "site", "10.0.0.1", "", "", sim.NoopSink{})
	if err != nil {
		t.Fatalf("host path binds: %v", err)
	}
	if len(mounts) != 3 || mounts[0].Path != "/home" || mounts[1].Path != "/srv/ro" || !mounts[1].ReadOnly || mounts[0].ReadOnly {
		t.Fatalf("mounts %+v", mounts)
	}
	if mounts[0].Port == 0 || mounts[0].Port != mounts[1].Port || mounts[2].Port != mounts[0].Port {
		t.Fatalf("the shares are served on ports %d, %d and %d", mounts[0].Port, mounts[1].Port, mounts[2].Port)
	}
	if !strings.HasPrefix(mounts[0].Server, "10.0.0.1:/") || mounts[0].Server == mounts[1].Server {
		t.Fatalf("the shares are served as %s and %s", mounts[0].Server, mounts[1].Server)
	}
	if mounts[2].Path != "/etc/one.txt" || mounts[2].File != "one.txt" || mounts[0].File != "" {
		t.Fatalf("the file's share is %+v", mounts[2])
	}
	again, err := s.mountsFor(context.Background(), Workload{Name: "sidecar", Binds: []string{home + ":/data"}}, "sidecar", "10.0.0.1", "", "", sim.NoopSink{})
	if err != nil || again[0].Server != mounts[0].Server {
		t.Fatalf("the same directory bound again is served as %+v (%v), want %s", again, err, mounts[0].Server)
	}
	for _, bind := range []string{filepath.Join(home, "missing") + ":/m", "/home", ":/x"} {
		if _, err := s.mountsFor(context.Background(), Workload{Name: "w", Binds: []string{bind}}, "w", "10.0.0.1", "", "", sim.NoopSink{}); !errors.Is(err, ErrBindMounts) {
			t.Fatalf("bind %q: %v, want ErrBindMounts", bind, err)
		}
	}
	if _, err := s.mountsFor(context.Background(), Workload{Name: "w", Binds: []string{"vol:/data"}}, "w", "10.0.0.1", "", "", sim.NoopSink{}); !errors.Is(err, ErrBindMounts) {
		t.Fatalf("a volume with no agent address: %v, want ErrBindMounts", err)
	}
}
