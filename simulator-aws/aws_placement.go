package main

import (
	"context"
	"fmt"
	"log"
	"net/netip"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/nodeagent"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// awsPlacement maps each compute service to the backend its workloads run on,
// from SIM_PLACEMENT; every service runs on containers until configured
// otherwise.
var awsPlacement = awsContainerPlacement()

// awsMicroVMBackend is set when the table names a microVM service and the
// node agent started.
var awsMicroVMBackend *placement.MicroVMBackend

// awsMACPrefix is the locally administered prefix an Amazon EC2 elastic network
// interface's MAC address carries.
const awsMACPrefix = "0e:a7:7d"

func awsContainerPlacement() *placement.Table {
	table, err := placement.ParseTable("")
	if err != nil {
		panic(err)
	}
	table.Register(placement.ContainerBackend{})
	return table
}

// awsConfigurePlacement reads the placement table from the configuration.
func awsConfigurePlacement(cfg *sim.Config) error {
	table, err := placement.ParseTable(cfg.Placement)
	if err != nil {
		return err
	}
	table.Register(placement.ContainerBackend{})
	awsPlacement = table
	return nil
}

// awsStartPlacement starts the node agent when a service is configured onto
// microVMs, on the platform network at its third host address, and returns
// what stops it. A table naming no microVM service starts nothing.
func awsStartPlacement(ctx context.Context) (stop func(context.Context) error, err error) {
	if services := awsPlacement.Services(); len(services) > 0 {
		log.Printf("placement: %s", strings.Join(services, ", "))
	} else {
		log.Printf("placement: every service on containers")
	}
	if len(awsPlacement.MicroVMServices()) == 0 {
		return func(context.Context) error { return nil }, nil
	}
	network, err := awsPlatformNetwork(ctx)
	if err != nil {
		return nil, fmt.Errorf("node agent: platform network: %w", err)
	}
	subnet, err := awsNetworkSubnet(ctx, network.Name)
	if err != nil {
		return nil, fmt.Errorf("node agent: %w", err)
	}
	agent, err := nodeagent.Start(ctx, nodeagent.Options{
		Name:    "sockerless-sim-aws-node-agent-" + sim.WorkloadScope(),
		Network: network.Name,
		Address: subnet.Masked().Addr().Next().Next().Next(),
		Labels:  map[string]string{"sockerless-sim-aws-node-agent": sim.WorkloadScope()},
	}, sim.NoopSink{})
	if err != nil {
		return nil, fmt.Errorf("node agent for %s: %w", strings.Join(awsPlacement.MicroVMServices(), ", "), err)
	}
	awsMicroVMBackend = placement.NewMicroVMBackend(agent, awsMACPrefix)
	awsPlacement.Register(awsMicroVMBackend)
	log.Printf("placement: node agent %s serves %s", agent.ContainerID()[:12], strings.Join(awsPlacement.MicroVMServices(), ", "))
	return func(context.Context) error {
		agent.Stop()
		return nil
	}, nil
}

// awsJoinMicroVMNetwork makes the node agent a member of a VPC's engine
// network at the address Amazon VPC reserves for future use, before the
// first guest lands there. A table with no microVM service has no agent to
// join.
func awsJoinMicroVMNetwork(ctx context.Context, network string, cidr netip.Prefix) error {
	if awsMicroVMBackend == nil {
		return nil
	}
	return awsMicroVMBackend.Join(ctx, network, cidr.Masked().Addr().Next().Next().Next())
}
