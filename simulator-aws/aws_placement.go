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

// SIM_PLACEMENT maps compute services to backends; containers are the default.
var awsPlacement = awsContainerPlacement()

var awsMicroVMBackend *placement.MicroVMBackend

// A locally administered prefix.
const awsMACPrefix = "0e:a7:7d"

func awsContainerPlacement() *placement.Table {
	table, err := placement.ParseTable("")
	if err != nil {
		panic(err)
	}
	table.Register(placement.ContainerBackend{})
	return table
}

func awsConfigurePlacement(cfg *sim.Config) error {
	table, err := placement.ParseTable(cfg.Placement)
	if err != nil {
		return err
	}
	table.Register(placement.ContainerBackend{})
	awsPlacement = table
	return nil
}

// Start the node agent on the platform network's third host address when a
// service runs on microVMs, or adopt one a persistent simulator left running.
// A persistent simulator gets no stop function: the agent and its guests
// outlive the process, as containers do.
func awsStartPlacement(ctx context.Context, persist bool) (stop func(context.Context) error, err error) {
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
	labels := map[string]string{"sockerless-sim-aws-node-agent": sim.WorkloadScope()}
	var agent *nodeagent.Agent
	adopted := false
	if sim.HasPersistentWorkloadIdentity() {
		if agent, adopted, err = nodeagent.Adopt(ctx, network.Name, labels, nodeagent.LogSink()); err != nil {
			return nil, err
		}
	}
	if !adopted {
		agent, err = nodeagent.Start(ctx, nodeagent.Options{
			Name:    "sockerless-sim-aws-node-agent-" + sim.WorkloadScope(),
			Network: network.Name,
			Address: subnet.Masked().Addr().Next().Next().Next(),
			Labels:  labels,
		}, nodeagent.LogSink())
		if err != nil {
			return nil, fmt.Errorf("node agent for %s: %w", strings.Join(awsPlacement.MicroVMServices(), ", "), err)
		}
	}
	awsMicroVMBackend = placement.NewMicroVMBackend(agent, awsMACPrefix)
	awsPlacement.Register(awsMicroVMBackend)
	log.Printf("placement: node agent %s serves %s", agent.ContainerID()[:12], strings.Join(awsPlacement.MicroVMServices(), ", "))
	return func(context.Context) error {
		if !persist {
			agent.Stop()
		}
		return nil
	}, nil
}

// The engine removes no network a container is still on, so leave it before
// the VPC's deletion removes it.
func awsLeaveMicroVMNetwork(ctx context.Context, network string) error {
	if awsMicroVMBackend == nil {
		return nil
	}
	return awsMicroVMBackend.Leave(ctx, network)
}

// Join at the address Amazon VPC reserves for future use, before the first
// guest lands there.
func awsJoinMicroVMNetwork(ctx context.Context, network string, cidr netip.Prefix) error {
	if awsMicroVMBackend == nil {
		return nil
	}
	return awsMicroVMBackend.Join(ctx, network, cidr.Masked().Addr().Next().Next().Next())
}
