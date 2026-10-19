package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/egress"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// A NAT gateway's data plane is an egress sidecar at the gateway's private
// address on its VPC's network: an instance or task whose subnet's route
// table sends 0.0.0.0/0 to the gateway has its default route pointed at
// that address, so what it sends out of the VPC leaves from the gateway's
// address, with the gateway's idle timeout, and counts in the gateway's
// metrics.

// ec2NatGatewayIdleTimeout is how long a NAT gateway keeps an idle
// connection: 350 seconds, after which it sends nothing on it.
const ec2NatGatewayIdleTimeout = 350 * time.Second

// ec2NatGatewayStopGrace is how long a gateway's sidecar gets to report what
// its connections carried when the gateway goes.
const ec2NatGatewayStopGrace = 10 * time.Second

type ec2NatEgress struct {
	handle  *sim.ContainerHandle
	token   string
	network string
}

var (
	ec2NatEgressMu sync.Mutex
	ec2NatEgresses = map[string]*ec2NatEgress{}
)

// ec2EnsureVPCNatGateways runs the egress sidecar of every available NAT
// gateway of the VPC on the VPC's network, which exists by now.
func ec2EnsureVPCNatGateways(ctx context.Context, vpcID string, network awsWorkloadNetwork) error {
	if ec2NatGateways == nil {
		return nil
	}
	for _, natgw := range ec2NatGateways.Filter(func(n EC2NatGateway) bool { return n.VpcId == vpcID }) {
		if err := ec2EnsureNatGatewayEgress(ctx, natgw, network); err != nil {
			return err
		}
	}
	return nil
}

// ec2EnsureNatGatewayEgress starts the gateway's sidecar once; a container
// a previous simulator process left for the gateway is replaced, its token
// having died with that process.
func ec2EnsureNatGatewayEgress(ctx context.Context, natgw EC2NatGateway, network awsWorkloadNetwork) error {
	if natgw.State != "available" || len(natgw.NatGatewayAddresses) == 0 {
		return nil
	}
	ec2NatEgressMu.Lock()
	defer ec2NatEgressMu.Unlock()
	if _, ok := ec2NatEgresses[natgw.NatGatewayId]; ok {
		return nil
	}
	vpc, ok := ec2Vpcs.Get(natgw.VpcId)
	if !ok {
		return fmt.Errorf("vpc %s of NAT gateway %s not found", natgw.VpcId, natgw.NatGatewayId)
	}
	cidr, err := netip.ParsePrefix(vpc.CidrBlock)
	if err != nil {
		return fmt.Errorf("vpc %s CIDR %q: %w", vpc.VpcId, vpc.CidrBlock, err)
	}
	address, err := netip.ParseAddr(natgw.NatGatewayAddresses[0].PrivateIp)
	if err != nil {
		return fmt.Errorf("NAT gateway %s private address %q: %w", natgw.NatGatewayId, natgw.NatGatewayAddresses[0].PrivateIp, err)
	}
	origin, err := workloadhost.CallbackAddr(simListenAddr)
	if err != nil {
		return fmt.Errorf("NAT gateway %s: workload callback: %w", natgw.NatGatewayId, err)
	}
	if stale, err := sim.FindExistingContainers(map[string]string{"sockerless-sim-nat-gateway": natgw.NatGatewayId}); err == nil {
		for _, c := range stale {
			sim.StopAndRemoveContainer(c.ID, 0)
		}
	}
	token := sim.NewSidecarToken()
	sim.RegisterSidecarToken(token, network.Name)
	handle, err := sidecar.StartEgress(ctx, sidecar.EgressSpec{
		Network:      network.Name,
		Address:      address,
		Name:         "sockerless-sim-nat-" + natgw.NatGatewayId,
		Gateway:      natgw.NatGatewayId,
		Sources:      []netip.Prefix{cidr},
		Local:        []netip.Prefix{cidr},
		TCPIdle:      ec2NatGatewayIdleTimeout,
		UDPIdle:      ec2NatGatewayIdleTimeout,
		HTTPUpstream: "http://" + origin,
		Token:        token,
		ExtraHosts:   workloadhost.ExtraHosts(),
		Labels:       map[string]string{"sockerless-sim-nat-gateway": natgw.NatGatewayId},
	}, sim.NoopSink{})
	if err != nil {
		sim.UnregisterSidecarToken(token)
		return fmt.Errorf("NAT gateway %s: %w", natgw.NatGatewayId, err)
	}
	ec2NatEgresses[natgw.NatGatewayId] = &ec2NatEgress{handle: handle, token: token, network: network.Name}
	return nil
}

// ec2RemoveNatGatewayEgress stops the gateway's sidecar, with the grace it
// needs to report what its connections carried.
func ec2RemoveNatGatewayEgress(natID string) {
	ec2NatEgressMu.Lock()
	e := ec2NatEgresses[natID]
	delete(ec2NatEgresses, natID)
	ec2NatEgressMu.Unlock()
	if e == nil {
		return
	}
	sim.StopContainer(e.handle.ContainerID, ec2NatGatewayStopGrace)
	e.handle.Cancel()
	sim.UnregisterSidecarToken(e.token)
}

// ec2StopNatGatewayEgresses stops every gateway's sidecar at shutdown.
func ec2StopNatGatewayEgresses() {
	ec2NatEgressMu.Lock()
	ids := make([]string, 0, len(ec2NatEgresses))
	for id := range ec2NatEgresses {
		ids = append(ids, id)
	}
	ec2NatEgressMu.Unlock()
	for _, id := range ids {
		ec2RemoveNatGatewayEgress(id)
	}
}

// ec2SubnetRouteTable is the route table the subnet uses: the one associated
// with it, else the VPC's main table.
func ec2SubnetRouteTable(subnetID string) (EC2RouteTable, bool) {
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return EC2RouteTable{}, false
	}
	var main EC2RouteTable
	var hasMain bool
	for _, rt := range ec2RouteTables.Filter(func(rt EC2RouteTable) bool { return rt.VpcId == subnet.VpcId }) {
		for _, assoc := range rt.Associations {
			if assoc.SubnetId == subnetID {
				return rt, true
			}
			if assoc.Main {
				main, hasMain = rt, true
			}
		}
	}
	return main, hasMain
}

// ec2SubnetGateway is where a workload in the subnet sends what leaves the
// VPC, in the terms of sim.ContainerConfig.Gateway: the private address of
// the NAT gateway its route table's 0.0.0.0/0 route names, no route at all
// for one naming a gateway that is gone, and the network's own gateway for
// an internet gateway. A table with no route out leaves the workload on
// the network's gateway too, the gap BUGS.md records.
func ec2SubnetGateway(subnetID string) string {
	rt, ok := ec2SubnetRouteTable(subnetID)
	if !ok {
		return ""
	}
	for _, route := range rt.Routes {
		if route.DestinationCidrBlock != "0.0.0.0/0" {
			continue
		}
		if route.NatGatewayId == "" {
			return ""
		}
		natgw, ok := ec2NatGateways.Get(route.NatGatewayId)
		if !ok || natgw.State != "available" || len(natgw.NatGatewayAddresses) == 0 {
			return sim.NoGateway
		}
		return natgw.NatGatewayAddresses[0].PrivateIp
	}
	return ""
}

// ec2ReapplyRoutes points every running instance and awsvpc task of the VPC
// at what its subnet's route table says now. A backend that does not route
// is left as it is.
func ec2ReapplyRoutes(vpcID string) {
	if vpcID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ec2WorkloadsMu.Lock()
	instanceIDs := make([]string, 0, len(ec2Workloads))
	for id := range ec2Workloads {
		instanceIDs = append(instanceIDs, id)
	}
	ec2WorkloadsMu.Unlock()
	for _, id := range instanceIDs {
		inst := ec2InstanceRecord(id)
		if inst.VpcId != vpcID {
			continue
		}
		handle, ok := ec2WorkloadHandle(id)
		if !ok {
			continue
		}
		ec2SetGateway(ctx, "instance "+id, handle, ec2SubnetGateway(inst.SubnetId))
	}
	ecsProcessHandles.Range(func(key, value any) bool {
		taskID, ok := key.(string)
		if !ok {
			return true
		}
		procs, ok := value.(*ecsTaskProcesses)
		if !ok {
			return true
		}
		_, subnetID, ok := ecsTaskENIInfo(taskID)
		if !ok {
			return true
		}
		if subnet, ok := ec2Subnets.Get(subnetID); !ok || subnet.VpcId != vpcID {
			return true
		}
		if handle := procs.Handles[procs.MainContainerName]; handle != nil {
			ec2SetGateway(ctx, "task "+taskID, handle, ec2SubnetGateway(subnetID))
		}
		return true
	})
}

func ec2SetGateway(ctx context.Context, what string, handle placement.Handle, gateway string) {
	if err := handle.SetGateway(ctx, gateway); err != nil && !errors.Is(err, placement.ErrNoRouteControl) {
		fmt.Fprintf(os.Stderr, "[sim-ec2] %s: apply its route table: %v\n", what, err)
	}
}

// ec2RouteTableVPC is the VPC a route table belongs to.
func ec2RouteTableVPC(routeTableID string) string {
	rt, _ := ec2RouteTables.Get(routeTableID)
	return rt.VpcId
}

// ec2TakeNatEgressReport turns what a gateway's sidecar reports into the
// gateway's Amazon CloudWatch metrics, in the AWS/NATGateway namespace under
// the NatGatewayId dimension: every connection a source opens counts an
// attempt, every one the destination accepts an establishment, every one
// that ends its bytes in each direction and, when the gateway's idle timeout
// ended it, an idle timeout, and each minute the connections open.
func ec2TakeNatEgressReport(network string, report egress.Report) {
	natgw, ok := ec2NatGateways.Get(report.Gateway)
	if !ok || ecsVPCNetworkName(natgw.VpcId) != network {
		return
	}
	datum := func(name string, value float64, unit string, at time.Time) {
		cwStoreDatum(CWMetricDatum{
			Namespace:  "AWS/NATGateway",
			MetricName: name,
			Dimensions: []CWDimension{{Name: "NatGatewayId", Value: natgw.NatGatewayId}},
			Value:      value,
			Unit:       unit,
			Timestamp:  float64(at.UTC().Unix()),
		})
	}
	for _, ev := range report.Events {
		switch ev.Kind {
		case egress.Opened:
			datum("ConnectionAttemptCount", 1, "Count", ev.At)
		case egress.Established:
			datum("ConnectionEstablishedCount", 1, "Count", ev.At)
		case egress.Closed:
			datum("BytesInFromSource", float64(ev.BytesOut), "Bytes", ev.At)
			datum("BytesOutToDestination", float64(ev.BytesOut), "Bytes", ev.At)
			datum("BytesInFromDestination", float64(ev.BytesIn), "Bytes", ev.At)
			datum("BytesOutToSource", float64(ev.BytesIn), "Bytes", ev.At)
			if ev.Reason == egress.ReasonIdle {
				datum("IdleTimeoutCount", 1, "Count", ev.At)
			}
		case egress.Active:
			datum("ActiveConnectionCount", float64(ev.Active), "Count", ev.At)
		}
	}
}
