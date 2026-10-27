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

// placementCompute is the service Compute Engine instances are placed as,
// and placementRun the one Cloud Run services, jobs, worker pools and
// instances are, Cloud Functions among them, which run as Cloud Run
// services. An instance's first container is placed and the others join it
// as members of its namespace. placementRedis places the nodes of
// Memorystore for Redis instances and clusters, and placementCloudBuild the
// steps of Cloud Build builds.
var (
	placementCompute    = placement.Service{Name: "compute"}
	placementRun        = placement.Service{Name: "run"}
	placementRedis      = placement.Service{Name: "redis"}
	placementCloudBuild = placement.Service{Name: "cloudbuild"}
)

// gcpPlacement maps services to the backend their workloads run on: the
// all-containers table until gcpConfigurePlacement replaces it with the one
// SIM_PLACEMENT spells, so in-process unit tests run on containers.
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
		if agent, adopted, err = nodeagent.Adopt(ctx, platform.Network, labels, nodeagent.LogSink()); err != nil {
			return err
		}
	}
	if !adopted {
		agent, err = nodeagent.Start(ctx, nodeagent.Options{
			Name:    "sockerless-sim-gcp-nodeagent-" + strings.TrimPrefix(platform.Network, "sockerless-sim-gcp-platform-"),
			Network: platform.Network,
			Address: cidr.Masked().Addr().Next().Next().Next(),
			Labels:  labels,
		}, nodeagent.LogSink())
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
