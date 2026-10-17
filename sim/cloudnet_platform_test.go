package sim

import (
	"context"
	"testing"

	"github.com/moby/moby/client"
)

func TestEnsurePlatformNetworkTakesARangeOfTheSharedSpaceAndKeepsIt(t *testing.T) {
	InitDocker("aws", true, t.TempDir())
	ctx := context.Background()
	name := "sockerless-sim-test-platform-" + RandomHex(4)
	id, cidr, err := EnsurePlatformNetwork(ctx, name, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = DockerClient().NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
	})
	if !platformRange.Contains(cidr.Addr()) || cidr.Bits() != 24 {
		t.Fatalf("platform network took %s, want a /24 inside %s", cidr, platformRange)
	}
	again, againCIDR, err := EnsurePlatformNetwork(ctx, name, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again != id || againCIDR != cidr {
		t.Fatalf("second ensure gave %s %s, want the same network %s %s", again, againCIDR, id, cidr)
	}
	other := "sockerless-sim-test-platform-" + RandomHex(4)
	otherID, otherCIDR, err := EnsurePlatformNetwork(ctx, other, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = DockerClient().NetworkRemove(ctx, otherID, client.NetworkRemoveOptions{})
	})
	if otherCIDR.Overlaps(cidr) {
		t.Fatalf("two platform networks share %s", cidr)
	}
}
