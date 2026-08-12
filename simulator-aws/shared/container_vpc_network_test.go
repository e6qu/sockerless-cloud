package simulator

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// A VPC network is named for its VPC and its subnet is the VPC's CIDR, so a
// simulator that exits without removing it leaves the subnet held under a name
// no later run looks for: the next run's different VPC id misses the name
// lookup and then cannot create its own network, because the subnet is taken.
// The failure is total — every awsvpc task fails — and the only cure was
// removing the network by hand.

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

func TestEnsureVPCNetworkReclaimsASubnetADeadRunLeftBehind(t *testing.T) {
	cli := vpcTestDockerClient(t)
	// A subnet outside the ranges the simulator suites use, so a concurrent
	// suite cannot be holding it.
	const subnet = "10.201.0.0/16"
	removeNetworksHolding(t, cli, subnet)
	t.Cleanup(func() { removeNetworksHolding(t, cli, subnet) })

	dockerClient = cli
	previousRun := simulatorRunID
	simulatorRunID = "dead-run-that-exited"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The network a simulator that has since exited left behind, named for its
	// own VPC and holding the subnet.
	orphan, err := cli.NetworkCreate(ctx, "sockerless-sim-vpc-vpc-dead", client.NetworkCreateOptions{
		Driver: "bridge",
		IPAM:   &network.IPAM{Config: []network.IPAMConfig{{Subnet: netip.MustParsePrefix(subnet)}}},
		Labels: simulatorLabels(map[string]string{"sockerless-sim-vpc": "sockerless-sim-vpc-vpc-dead"}),
	})
	if err != nil {
		t.Fatalf("create the orphaned network: %v", err)
	}

	// A later run, with a different VPC id and therefore a different name.
	simulatorRunID = "the-run-that-is-live"
	t.Cleanup(func() { simulatorRunID = previousRun })

	id, err := EnsureVPCNetwork("sockerless-sim-vpc-vpc-live", subnet)
	if err != nil {
		t.Fatalf("the later run could not claim the subnet its dead predecessor held: %v", err)
	}
	t.Cleanup(func() { _ = RemoveDockerNetwork("sockerless-sim-vpc-vpc-live") })
	if id == "" || id == orphan.ID {
		t.Fatalf("expected a newly created network, got %q (orphan was %q)", id, orphan.ID)
	}
	if _, err := cli.NetworkInspect(ctx, orphan.ID, client.NetworkInspectOptions{}); err == nil {
		t.Error("the orphaned network still exists, so the subnet was not reclaimed")
	}
}

// The reclaim must not touch a network that is in use, nor one belonging to the
// running simulator, nor anything this project did not create.
func TestReclaimOrphanedSubnetLeavesLiveAndForeignNetworksAlone(t *testing.T) {
	cli := vpcTestDockerClient(t)
	const subnet = "10.202.0.0/16"
	removeNetworksHolding(t, cli, subnet)
	dockerClient = cli
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	previousRun := simulatorRunID
	simulatorRunID = "the-run-that-is-live"
	t.Cleanup(func() { simulatorRunID = previousRun })

	// This run's own network: empty, but its owner is alive.
	own, err := cli.NetworkCreate(ctx, "sockerless-sim-vpc-own", client.NetworkCreateOptions{
		Driver: "bridge",
		IPAM:   &network.IPAM{Config: []network.IPAMConfig{{Subnet: netip.MustParsePrefix(subnet)}}},
		Labels: simulatorLabels(nil),
	})
	if err != nil {
		t.Fatalf("create this run's network: %v", err)
	}
	t.Cleanup(func() { _, _ = cli.NetworkRemove(context.Background(), own.ID, client.NetworkRemoveOptions{}) })

	if reclaimOrphanedSubnet(ctx, cli, subnet) {
		t.Error("the reclaim removed a network belonging to the running simulator")
	}
	if _, err := cli.NetworkInspect(ctx, own.ID, client.NetworkInspectOptions{}); err != nil {
		t.Errorf("this run's own network was removed: %v", err)
	}

	// A network this project did not create, holding a different subnet, is
	// never a candidate however idle it is.
	const foreignSubnet = "10.203.0.0/16"
	foreign, err := cli.NetworkCreate(ctx, "not-a-sockerless-network", client.NetworkCreateOptions{
		Driver: "bridge",
		IPAM:   &network.IPAM{Config: []network.IPAMConfig{{Subnet: netip.MustParsePrefix(foreignSubnet)}}},
	})
	if err != nil {
		t.Fatalf("create the foreign network: %v", err)
	}
	t.Cleanup(func() { _, _ = cli.NetworkRemove(context.Background(), foreign.ID, client.NetworkRemoveOptions{}) })

	simulatorRunID = "some-other-run"
	if reclaimOrphanedSubnet(ctx, cli, foreignSubnet) {
		t.Error("the reclaim removed a network the simulator did not create")
	}
	if _, err := cli.NetworkInspect(ctx, foreign.ID, client.NetworkInspectOptions{}); err != nil {
		t.Errorf("a foreign network was removed: %v", err)
	}
}
