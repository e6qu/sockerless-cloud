package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/nodeagent"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// placementCompute is the service Compute Engine instances are placed as;
// Cloud Run and Cloud Functions still run their containers through
// sim/workload, whose container groups join the owner's namespace.
var placementCompute = placement.Service{Name: "compute"}

// gcpPlacement maps services to the backend their workloads run on. It starts
// as the all-containers table, which is what a process built without
// Configure (the in-process unit tests) runs on; Configure replaces it with
// the one SIM_PLACEMENT spells.
var gcpPlacement = gcpContainerPlacement()

// gcpMicroVMBackend is the node agent's backend once a service is configured
// onto microVMs, and nil otherwise.
var gcpMicroVMBackend *placement.MicroVMBackend

// gceMACPrefix is the organizationally unique identifier Compute Engine's
// virtual network interfaces carry.
const gceMACPrefix = "42:01"

func gcpContainerPlacement() *placement.Table {
	table, err := placement.ParseTable("")
	if err != nil {
		panic(err)
	}
	table.Register(placement.ContainerBackend{})
	return table
}

// gcpConfigurePlacement reads the placement table from the configuration.
func gcpConfigurePlacement(cfg *sim.Config) error {
	table, err := placement.ParseTable(cfg.Placement)
	if err != nil {
		return err
	}
	table.Register(placement.ContainerBackend{})
	gcpPlacement = table
	return nil
}

// gcpStartPlacement starts the node agent on the platform network when a
// service is configured onto microVMs, and logs the table either way.
func gcpStartPlacement(ctx context.Context) error {
	services := gcpPlacement.MicroVMServices()
	if len(services) == 0 {
		fmt.Fprintln(os.Stderr, "  placement: every service on containers")
		return nil
	}
	fmt.Fprintf(os.Stderr, "  placement: %s\n", strings.Join(gcpPlacement.Services(), ", "))
	platform, err := gcpPlatformNetwork(ctx)
	if err != nil {
		return fmt.Errorf("node agent: %w", err)
	}
	cidr, err := gcpNetworkRange(ctx, platform.Network)
	if err != nil {
		return fmt.Errorf("node agent: %w", err)
	}
	labels := map[string]string{"sockerless-sim-gcp-node-agent": sim.WorkloadScope()}
	var agent *nodeagent.Agent
	adopted := false
	if sim.HasPersistentWorkloadIdentity() {
		if agent, adopted, err = nodeagent.Adopt(ctx, platform.Network, labels, sim.NoopSink{}); err != nil {
			return err
		}
	}
	if !adopted {
		agent, err = nodeagent.Start(ctx, nodeagent.Options{
			Name:    "sockerless-sim-gcp-nodeagent-" + strings.TrimPrefix(platform.Network, "sockerless-sim-gcp-platform-"),
			Network: platform.Network,
			Address: cidr.Masked().Addr().Next().Next().Next(),
			Labels:  labels,
		}, sim.NoopSink{})
		if err != nil {
			return fmt.Errorf("node agent: %w", err)
		}
	}
	gcpMicroVMBackend = placement.NewMicroVMBackend(agent, gceMACPrefix)
	gcpPlacement.Register(gcpMicroVMBackend)
	fmt.Fprintf(os.Stderr, "  node agent on %s for %s\n", platform.Network, strings.Join(services, ", "))
	return nil
}

// gcpStopPlacement stops the node agent, if one runs, unless the simulator
// persists: then the agent and its guests outlive the process as containers
// do, for the next process to adopt.
func gcpStopPlacement(persist bool) {
	if gcpMicroVMBackend != nil {
		if !persist {
			gcpMicroVMBackend.Agent().Stop()
		}
		gcpMicroVMBackend = nil
	}
}
