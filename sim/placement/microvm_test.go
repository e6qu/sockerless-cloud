package placement

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// A guest's filesystem is its own disk: a workload that binds host paths
// into it is refused rather than run without them.
func TestMicroVMBackendServesHostDirectoryBindsAsShares(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "one.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var s shares
	mounts, err := s.mountsFor(Workload{Name: "site", Binds: []string{home + ":/home", home + ":/srv/ro:ro"}}, "10.0.0.1")
	if err != nil {
		t.Fatalf("host directory binds: %v", err)
	}
	if len(mounts) != 2 || mounts[0].Path != "/home" || mounts[1].Path != "/srv/ro" || !mounts[1].ReadOnly || mounts[0].ReadOnly {
		t.Fatalf("mounts %+v", mounts)
	}
	if mounts[0].Port == 0 || mounts[0].Port != mounts[1].Port {
		t.Fatalf("the shares are served on ports %d and %d", mounts[0].Port, mounts[1].Port)
	}
	if !strings.HasPrefix(mounts[0].Server, "10.0.0.1:/") || mounts[0].Server == mounts[1].Server {
		t.Fatalf("the shares are served as %s and %s", mounts[0].Server, mounts[1].Server)
	}
	again, err := s.mountsFor(Workload{Name: "sidecar", Binds: []string{home + ":/data"}}, "10.0.0.1")
	if err != nil || again[0].Server != mounts[0].Server {
		t.Fatalf("the same directory bound again is served as %+v (%v), want %s", again, err, mounts[0].Server)
	}
	for _, bind := range []string{"vol:/data", file + ":/etc/one.txt", filepath.Join(home, "missing") + ":/m", "/home"} {
		if _, err := s.mountsFor(Workload{Name: "w", Binds: []string{bind}}, "10.0.0.1"); !errors.Is(err, ErrBindMounts) {
			t.Fatalf("bind %q: %v, want ErrBindMounts", bind, err)
		}
	}
}
