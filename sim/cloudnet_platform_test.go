package sim

import (
	"context"
	"net/netip"
	"runtime"
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

// The engine allocates from the upper half of a platform network and the
// simulator leases the lower half, above the addresses its sidecars take, so
// a guest the engine does not place never collides with a container.
func TestPlatformNetworkLeasesItsLowerHalf(t *testing.T) {
	InitDocker("aws", true, t.TempDir())
	ctx := context.Background()
	name := "sockerless-sim-test-platform-lease-" + RandomHex(4)
	_, cidr, err := EnsurePlatformNetwork(ctx, name, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = RemoveDockerNetworkContext(ctx, name) }()
	details, err := DockerClient().NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := details.Network.IPAM.Config[0].IPRange; got != platformDynamicRange(cidr) {
		t.Fatalf("the engine allocates from %s, want %s", got, platformDynamicRange(cidr))
	}
	first, release, err := LeasePlatformAddress(ctx, name, "first", netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	octets := first.As4()
	if octets[3] != platformLeaseFloor || !cidr.Contains(first) {
		t.Fatalf("the first lease is %s, want the floor of %s", first, cidr)
	}
	if _, _, err := LeasePlatformAddress(ctx, name, "again", first); err == nil {
		t.Fatal("a leased address was leased again")
	}
	second, releaseSecond, err := LeasePlatformAddress(ctx, name, "second", netip.Addr{})
	if err != nil || second == first || !cidr.Contains(second) {
		t.Fatalf("second lease %s (%v)", second, err)
	}
	releaseSecond()
	release()
	third, releaseThird, err := LeasePlatformAddress(ctx, name, "third", netip.Addr{})
	if err != nil || third != first {
		t.Fatalf("after the releases the lease is %s (%v), want %s again", third, err, first)
	}
	defer releaseThird()
	upper := cidr.Masked().Addr().As4()
	upper[3] = 200
	if _, _, err := LeasePlatformAddress(ctx, name, "upper", netip.AddrFrom4(upper)); err == nil {
		t.Fatal("an address of the engine's half was leased")
	}
	elsewhere := netip.MustParseAddr("10.200.0.7")
	noop, err := ReservePlatformAddress(ctx, name, elsewhere, "elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	noop()
	held, err := ReservePlatformAddress(ctx, name, second, "adopted")
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	if _, _, err := LeasePlatformAddress(ctx, name, "collides", second); err == nil {
		t.Fatal("an adopted address was leased")
	}
}

// A container holds its address on the engine until it is removed, after
// its lease went back, so a lease skips what the engine still shows
// attached and takes it once the container is gone.
func TestPlatformNetworkLeaseSkipsAnAddressTheEngineStillHolds(t *testing.T) {
	InitDocker("aws", true, t.TempDir())
	ctx := context.Background()
	name := "sockerless-sim-test-platform-held-" + RandomHex(4)
	if _, _, err := EnsurePlatformNetwork(ctx, name, nil); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = RemoveDockerNetworkContext(ctx, name) }()
	first, release, err := LeasePlatformAddress(ctx, name, "first", netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	release()
	const image = "mirror.gcr.io/library/alpine:3.21"
	if err := PullImage(ctx, image, ""); err != nil {
		t.Fatal(err)
	}
	holder, err := StartContainerSyncContext(ctx, ContainerConfig{
		Image:        image,
		Architecture: "linux/" + runtime.GOARCH,
		Command:      []string{"/bin/sleep", "300"},
		Name:         "sockerless-sim-test-platform-holder-" + RandomHex(4),
		Network:      name,
		IPv4Address:  first.String(),
	}, NoopSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Cancel()
	if _, _, err := LeasePlatformAddress(ctx, name, "collides", first); err == nil {
		t.Fatalf("%s was leased while a container holds it", first)
	}
	next, releaseNext, err := LeasePlatformAddress(ctx, name, "next", netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	if next == first {
		t.Fatalf("the lease took %s, which a container holds", first)
	}
	releaseNext()
	holder.Cancel()
	<-holder.Exited()
	again, releaseAgain, err := LeasePlatformAddress(ctx, name, "again", netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	defer releaseAgain()
	if again != first {
		t.Fatalf("after the container is gone the lease is %s, want %s", again, first)
	}
}
