package sim

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// A VPC network's bridge subnet is allocated from the reserved host-side pool
// (vpcBridgePool), never from the VPC's own CIDR: an AWS CIDR is private to
// its VPC — two live VPCs may share one — while a host subnet is exclusive.
// The allocator derives its state from the networks live on the host, so a
// restarted simulator cannot double-allocate, and a slice held by a network a
// dead simulator run left behind is reclaimed on demand.

func vpcTestDockerClient(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		t.Fatalf("docker/podman is required to exercise the VPC network path: %v", err)
	}
	return cli
}

// removeNetworksHolding clears any leftover from a previous run of this test so
// it starts from a known state, and is also the teardown.
func removeNetworksHolding(t *testing.T, cli *client.Client, subnet string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	nets, err := cli.NetworkList(ctx, client.NetworkListOptions{
		Filters: client.Filters{}.Add("label", "sockerless-sim=true"),
	})
	if err != nil {
		return
	}
	for _, n := range nets.Items {
		details, err := cli.NetworkInspect(ctx, n.ID, client.NetworkInspectOptions{})
		if err != nil {
			continue
		}
		for _, cfg := range details.Network.IPAM.Config {
			if cfg.Subnet.String() == subnet {
				_, _ = cli.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{})
				break
			}
		}
	}
}

// networkSubnet reads the single IPAM subnet of a network by ID.
func networkSubnet(t *testing.T, cli *client.Client, id string) netip.Prefix {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	details, err := cli.NetworkInspect(ctx, id, client.NetworkInspectOptions{})
	if err != nil {
		t.Fatalf("inspect network %s: %v", id, err)
	}
	if len(details.Network.IPAM.Config) != 1 {
		t.Fatalf("network %s has %d IPAM configs, want 1", id, len(details.Network.IPAM.Config))
	}
	return details.Network.IPAM.Config[0].Subnet
}

// firstFreePoolSlice returns the first candidate slice EnsureVPCNetwork would
// consider for name that no network on the host currently holds — the slice
// the allocator will take, since it scans from the name-derived offset.
func firstFreePoolSlice(t *testing.T, cli *client.Client, name string) netip.Prefix {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	used, err := hostSubnetsInUse(ctx, cli)
	if err != nil {
		t.Fatalf("list host subnets: %v", err)
	}
	start := int(vpcNetworkNameHash(name) % 256)
	for n := 0; n < 256; n++ {
		candidate := vpcBridgeSubnet((start + n) % 256)
		if !prefixOverlapsAny(used, candidate) {
			return candidate
		}
	}
	t.Fatal("no free slice in the reserved VPC bridge pool on this host")
	return netip.Prefix{}
}

// Two VPC networks — as for two live VPCs, whatever their AWS CIDRs — must
// coexist, each on its own pool slice. The AWS CIDR is not even an input:
// nothing about the bridge subnet may depend on it.
func TestEnsureVPCNetworkAllocatesDistinctPoolSlicesForLiveVPCs(t *testing.T) {
	cli := vpcTestDockerClient(t)
	dockerClient = cli

	const nameA = "sockerless-sim-vpc-vpc-pool-a"
	const nameB = "sockerless-sim-vpc-vpc-pool-b"
	t.Cleanup(func() {
		_ = RemoveDockerNetwork(nameA)
		_ = RemoveDockerNetwork(nameB)
	})

	idA, err := EnsureVPCNetwork(nameA)
	if err != nil {
		t.Fatalf("create first VPC network: %v", err)
	}
	idB, err := EnsureVPCNetwork(nameB)
	if err != nil {
		t.Fatalf("create second VPC network alongside the first: %v", err)
	}

	subnetA := networkSubnet(t, cli, idA)
	subnetB := networkSubnet(t, cli, idB)
	for name, subnet := range map[string]netip.Prefix{nameA: subnetA, nameB: subnetB} {
		if !vpcBridgePool.Overlaps(subnet) {
			t.Errorf("%s bridge subnet %s is outside the reserved pool %s", name, subnet, vpcBridgePool)
		}
		if subnet.Bits() != 24 {
			t.Errorf("%s bridge subnet %s is not a /24 pool slice", name, subnet)
		}
	}
	if subnetA == subnetB {
		t.Errorf("both live VPC networks hold the same bridge subnet %s", subnetA)
	}

	// Idempotency: asking again returns the existing network, no new slice.
	again, err := EnsureVPCNetwork(nameA)
	if err != nil {
		t.Fatalf("re-ensure existing VPC network: %v", err)
	}
	if again != idA {
		t.Errorf("re-ensure returned %q, want the existing network %q", again, idA)
	}
}

func TestEnsureVPCNetworkReclaimsASliceADeadRunLeftBehind(t *testing.T) {
	cli := vpcTestDockerClient(t)
	dockerClient = cli
	previousRun := simulatorRunID

	// The slice the live run's allocator will want first, currently free.
	const liveName = "sockerless-sim-vpc-vpc-live"
	slice := firstFreePoolSlice(t, cli, liveName)
	removeNetworksHolding(t, cli, slice.String())
	t.Cleanup(func() { removeNetworksHolding(t, cli, slice.String()) })

	simulatorRunID = "dead-run-that-exited"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The network a simulator that has since exited left behind, named for its
	// own VPC and holding the slice.
	orphan, err := cli.NetworkCreate(ctx, "sockerless-sim-vpc-vpc-dead", client.NetworkCreateOptions{
		Driver: "bridge",
		IPAM:   &network.IPAM{Config: []network.IPAMConfig{{Subnet: slice}}},
		Labels: simulatorLabels(map[string]string{"sockerless-sim-vpc": "sockerless-sim-vpc-vpc-dead"}),
	})
	if err != nil {
		t.Fatalf("create the orphaned network: %v", err)
	}

	// A later run, with a different VPC id and therefore a different name.
	simulatorRunID = "the-run-that-is-live"
	t.Cleanup(func() { simulatorRunID = previousRun })

	id, err := EnsureVPCNetwork(liveName)
	if err != nil {
		t.Fatalf("the later run could not claim the slice its dead predecessor held: %v", err)
	}
	t.Cleanup(func() { _ = RemoveDockerNetwork(liveName) })
	if id == "" || id == orphan.ID {
		t.Fatalf("expected a newly created network, got %q (orphan was %q)", id, orphan.ID)
	}
	if got := networkSubnet(t, cli, id); got != slice {
		t.Errorf("the live run's network holds %s, want the reclaimed slice %s", got, slice)
	}
	if _, err := cli.NetworkInspect(ctx, orphan.ID, client.NetworkInspectOptions{}); err == nil {
		t.Error("the orphaned network still exists, so the slice was not reclaimed")
	}
}

// The reclaim must not touch a network that belongs to the running simulator,
// nor anything this project did not create — the allocator skips their slices
// instead of deleting them.
func TestEnsureVPCNetworkLeavesLiveAndForeignNetworksAlone(t *testing.T) {
	cli := vpcTestDockerClient(t)
	dockerClient = cli
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	previousRun := simulatorRunID
	simulatorRunID = "the-run-that-is-live"
	t.Cleanup(func() { simulatorRunID = previousRun })

	// This run's own empty network, squatting the slice the next allocation
	// would otherwise pick: its owner is alive, so it must survive and the
	// allocator must take a different slice.
	const wantedName = "sockerless-sim-vpc-vpc-wants-a-slice"
	occupied := firstFreePoolSlice(t, cli, wantedName)
	own, err := cli.NetworkCreate(ctx, "sockerless-sim-vpc-own", client.NetworkCreateOptions{
		Driver: "bridge",
		IPAM:   &network.IPAM{Config: []network.IPAMConfig{{Subnet: occupied}}},
		Labels: simulatorLabels(nil),
	})
	if err != nil {
		t.Fatalf("create this run's network: %v", err)
	}
	t.Cleanup(func() { _, _ = cli.NetworkRemove(context.Background(), own.ID, client.NetworkRemoveOptions{}) })

	id, err := EnsureVPCNetwork(wantedName)
	if err != nil {
		t.Fatalf("allocate around the live network: %v", err)
	}
	t.Cleanup(func() { _ = RemoveDockerNetwork(wantedName) })
	if got := networkSubnet(t, cli, id); got == occupied {
		t.Errorf("the allocator took the live network's slice %s", occupied)
	}
	if _, err := cli.NetworkInspect(ctx, own.ID, client.NetworkInspectOptions{}); err != nil {
		t.Errorf("this run's own network was removed: %v", err)
	}
	if reclaimOrphanedSubnet(ctx, cli, occupied.String()) {
		t.Error("the reclaim removed a network belonging to the running simulator")
	}

	// A network this project did not create is never a candidate however idle
	// it is, even inside the pool and even for a dead run id.
	foreignSlice := firstFreePoolSlice(t, cli, "foreign-holder-probe")
	foreign, err := cli.NetworkCreate(ctx, "not-a-sockerless-network", client.NetworkCreateOptions{
		Driver: "bridge",
		IPAM:   &network.IPAM{Config: []network.IPAMConfig{{Subnet: foreignSlice}}},
	})
	if err != nil {
		t.Fatalf("create the foreign network: %v", err)
	}
	t.Cleanup(func() { _, _ = cli.NetworkRemove(context.Background(), foreign.ID, client.NetworkRemoveOptions{}) })

	simulatorRunID = "some-other-run"
	if reclaimOrphanedSubnet(ctx, cli, foreignSlice.String()) {
		t.Error("the reclaim removed a network the simulator did not create")
	}
	if _, err := cli.NetworkInspect(ctx, foreign.ID, client.NetworkInspectOptions{}); err != nil {
		t.Errorf("a foreign network was removed: %v", err)
	}
}
