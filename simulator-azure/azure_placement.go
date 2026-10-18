package main

import (
	"context"
	"fmt"
	"log"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/nodeagent"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// The services this simulator places workloads for, as SIM_PLACEMENT names
// them.
var (
	placementVirtualMachines    = placement.Service{Name: "virtualmachines"}
	placementContainerApps      = placement.Service{Name: "containerapps"}
	placementContainerInstances = placement.Service{Name: "containerinstances"}
	placementWeb                = placement.Service{Name: "web"}
)

var (
	azurePlacementMu    sync.Mutex
	azurePlacement      *placement.Table
	azureNodeAgent      *nodeagent.Agent
	azureMicroVMBackend *placement.MicroVMBackend
)

// configureAzurePlacement reads the placement table before anything is built.
func configureAzurePlacement(spec string) error {
	table, err := placement.ParseTable(spec)
	if err != nil {
		return err
	}
	table.Register(placement.ContainerBackend{})
	azurePlacementMu.Lock()
	azurePlacement = table
	azurePlacementMu.Unlock()
	return nil
}

// azurePlacementTable is the table Configure read, or the all-containers
// table for a simulator built without Configure.
func azurePlacementTable() *placement.Table {
	azurePlacementMu.Lock()
	defer azurePlacementMu.Unlock()
	if azurePlacement == nil {
		table, _ := placement.ParseTable("")
		table.Register(placement.ContainerBackend{})
		azurePlacement = table
	}
	return azurePlacement
}

// azurePlacementBackend is the backend the service's workloads run on.
func azurePlacementBackend(service placement.Service) (placement.Backend, error) {
	return azurePlacementTable().For(service)
}

// startAzurePlacement logs the table and, when a service is configured onto
// microVMs, starts the node agent on the platform network at the address
// after the sidecar's and registers the microVM backend.
func startAzurePlacement(ctx context.Context) error {
	table := azurePlacementTable()
	if services := table.Services(); len(services) > 0 {
		log.Printf("placement: %s", strings.Join(services, ", "))
	} else {
		log.Printf("placement: every service on containers")
	}
	if len(table.MicroVMServices()) == 0 {
		return nil
	}
	platform, err := azurePlatformNetwork(ctx)
	if err != nil {
		return fmt.Errorf("node agent: %w", err)
	}
	prefix, err := azureEngineNetworkPrefix(ctx, platform.Network)
	if err != nil {
		return fmt.Errorf("node agent: %w", err)
	}
	labels := map[string]string{"sockerless-sim-azure-node-agent": sim.WorkloadScope()}
	var agent *nodeagent.Agent
	adopted := false
	if sim.HasPersistentWorkloadIdentity() {
		if agent, adopted, err = nodeagent.Adopt(ctx, platform.Network, labels, sim.NoopSink{}); err != nil {
			return err
		}
	}
	if !adopted {
		agent, err = nodeagent.Start(ctx, nodeagent.Options{
			Name:    "sockerless-sim-azure-node-agent-" + sim.WorkloadScope(),
			Network: platform.Network,
			Address: prefix.Masked().Addr().Next().Next().Next(),
			Labels:  labels,
		}, sim.NoopSink{})
		if err != nil {
			return fmt.Errorf("node agent for %s: %w", strings.Join(table.MicroVMServices(), ", "), err)
		}
	}
	backend := placement.NewMicroVMBackend(agent, azureMACPrefix)
	table.Register(backend)
	azurePlacementMu.Lock()
	azureNodeAgent, azureMicroVMBackend = agent, backend
	azurePlacementMu.Unlock()
	return nil
}

// stopAzurePlacement stops the node agent, if one runs, unless the
// simulator persists: then the agent and its guests outlive the process as
// containers do, for the next process to adopt.
func stopAzurePlacement(persist bool) {
	azurePlacementMu.Lock()
	agent := azureNodeAgent
	azureNodeAgent, azureMicroVMBackend = nil, nil
	azurePlacementMu.Unlock()
	if agent != nil && !persist {
		agent.Stop()
	}
}

// azureJoinMicroVMNetwork attaches the node agent to a virtual network's
// engine network at the address Azure reserves beside the resolver's, before
// the first guest of a microVM service lands on it.
func azureJoinMicroVMNetwork(ctx context.Context, service placement.Service, vnetID string, vnetPrefix netip.Prefix, network string) error {
	table := azurePlacementTable()
	azurePlacementMu.Lock()
	backend := azureMicroVMBackend
	azurePlacementMu.Unlock()
	if backend == nil || table.KindFor(service) != placement.MicroVM {
		return nil
	}
	return backend.Join(ctx, network, azureVNetMicroVMAgentAddress(vnetID, vnetPrefix))
}

// azureLeaveMicroVMNetwork takes the node agent off an engine network the
// cloud is removing, whichever service joined it there: the engine removes
// no network a container is still on.
func azureLeaveMicroVMNetwork(ctx context.Context, network string) error {
	azurePlacementMu.Lock()
	backend := azureMicroVMBackend
	azurePlacementMu.Unlock()
	if backend == nil {
		return nil
	}
	return backend.Leave(ctx, network)
}

// azureJoinMicroVMPlatformNetwork attaches the node agent to a network of
// the platform's shape — a Container Apps environment's own — at an address
// leased from the simulator's half of it, before the first guest of a
// microVM service lands on it. The agent joined the platform network itself
// when it started.
func azureJoinMicroVMPlatformNetwork(ctx context.Context, service placement.Service, network string) error {
	table := azurePlacementTable()
	azurePlacementMu.Lock()
	backend := azureMicroVMBackend
	azurePlacementMu.Unlock()
	if backend == nil || table.KindFor(service) != placement.MicroVM {
		return nil
	}
	if _, joined := backend.Agent().AddressOn(network); joined {
		return nil
	}
	address, _, err := sim.LeasePlatformAddress(ctx, network, "sockerless-sim-azure-node-agent", netip.Addr{})
	if err != nil {
		return err
	}
	return backend.Join(ctx, network, address)
}

func azureEngineNetworkPrefix(ctx context.Context, network string) (netip.Prefix, error) {
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("inspecting a network"); err != nil {
		return netip.Prefix{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := cli.NetworkInspect(ctx, network, client.NetworkInspectOptions{})
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("inspect network %s: %w", network, err)
	}
	for _, cfg := range info.Network.IPAM.Config {
		if cfg.Subnet.IsValid() && cfg.Subnet.Addr().Is4() {
			return cfg.Subnet, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("network %s has no IPv4 range", network)
}
