package simulator

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// A VPC network is named for its VPC and its subnet is the VPC's CIDR, so a
// simulator that exits without removing it leaves the subnet held under a name
// no later run looks for: the next run's different VPC id misses the name
// lookup and then cannot create its own network, because the subnet is taken.
// The failure is total — every awsvpc task fails — and the only cure was
// removing the network by hand.

func vpcTestDockerClient(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
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
	nets, err := cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", "sockerless-sim=true")),
	})
	if err != nil {
		return
	}
	for _, n := range nets {
		details, err := cli.NetworkInspect(ctx, n.ID, network.InspectOptions{})
		if err != nil {
			continue
		}
		for _, cfg := range details.IPAM.Config {
			if cfg.Subnet == subnet {
				_ = cli.NetworkRemove(ctx, n.ID)
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
	orphan, err := cli.NetworkCreate(ctx, "sockerless-sim-vpc-vpc-dead", network.CreateOptions{
		Driver: "bridge",
		IPAM:   &network.IPAM{Config: []network.IPAMConfig{{Subnet: subnet}}},
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
	if _, err := cli.NetworkInspect(ctx, orphan.ID, network.InspectOptions{}); err == nil {
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
	own, err := cli.NetworkCreate(ctx, "sockerless-sim-vpc-own", network.CreateOptions{
		Driver: "bridge",
		IPAM:   &network.IPAM{Config: []network.IPAMConfig{{Subnet: subnet}}},
		Labels: simulatorLabels(nil),
	})
	if err != nil {
		t.Fatalf("create this run's network: %v", err)
	}
	t.Cleanup(func() { _ = cli.NetworkRemove(context.Background(), own.ID) })

	if reclaimOrphanedSubnet(ctx, cli, subnet) {
		t.Error("the reclaim removed a network belonging to the running simulator")
	}
	if _, err := cli.NetworkInspect(ctx, own.ID, network.InspectOptions{}); err != nil {
		t.Errorf("this run's own network was removed: %v", err)
	}

	// A network this project did not create, holding a different subnet, is
	// never a candidate however idle it is.
	const foreignSubnet = "10.203.0.0/16"
	foreign, err := cli.NetworkCreate(ctx, "not-a-sockerless-network", network.CreateOptions{
		Driver: "bridge",
		IPAM:   &network.IPAM{Config: []network.IPAMConfig{{Subnet: foreignSubnet}}},
	})
	if err != nil {
		t.Fatalf("create the foreign network: %v", err)
	}
	t.Cleanup(func() { _ = cli.NetworkRemove(context.Background(), foreign.ID) })

	simulatorRunID = "some-other-run"
	if reclaimOrphanedSubnet(ctx, cli, foreignSubnet) {
		t.Error("the reclaim removed a network the simulator did not create")
	}
	if _, err := cli.NetworkInspect(ctx, foreign.ID, network.InspectOptions{}); err != nil {
		t.Errorf("a foreign network was removed: %v", err)
	}
}
