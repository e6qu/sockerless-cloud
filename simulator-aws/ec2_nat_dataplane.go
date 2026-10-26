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
// metrics. A regional gateway runs one sidecar in each zone it covers, at
// its address in that zone, and a subnet leaves through its own zone's.

// ec2NatGatewayIdleTimeout is how long a NAT gateway keeps an idle
// connection: 350 seconds, after which it sends nothing on it.
const ec2NatGatewayIdleTimeout = 350 * time.Second

// ec2NatGatewayStopGrace is how long a gateway's sidecar gets to report what
// its connections carried when the gateway goes.
const ec2NatGatewayStopGrace = 10 * time.Second

type ec2NatEgress struct {
	gateway string
	address string
	handle  *sim.ContainerHandle
	token   string
	network string
}

var (
	ec2NatEgressMu sync.Mutex
	// ec2NatEgresses holds the running sidecars by gateway id, and a
	// regional gateway's by gateway id and zone.
	ec2NatEgresses = map[string]*ec2NatEgress{}
)

func ec2NatEgressKey(natID, zone string) string {
	if zone == "" {
		return natID
	}
	return natID + "/" + zone
}

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

// ec2EnsureNatGatewayEgress starts the gateway's sidecars once: a zonal
// gateway's at its primary address, a regional gateway's at its address in
// each zone it covers, and replaces one a zone's address moved away from.
func ec2EnsureNatGatewayEgress(ctx context.Context, natgw EC2NatGateway, network awsWorkloadNetwork) error {
	if natgw.State != "available" {
		return nil
	}
	if ec2NatAvailabilityMode(natgw) != "regional" {
		if len(natgw.NatGatewayAddresses) == 0 {
			return nil
		}
		return ec2EnsureNatEgress(ctx, natgw, "", natgw.NatGatewayAddresses[0].PrivateIp, network)
	}
	for _, zone := range natgw.Zones {
		if zone.PrivateIp == "" {
			continue
		}
		if err := ec2EnsureNatEgress(ctx, natgw, zone.AvailabilityZone, zone.PrivateIp, network); err != nil {
			return err
		}
	}
	return nil
}

// ec2EnsureNatEgress starts one sidecar of the gateway; a container a
// previous simulator process left for it is replaced, its token having died
// with that process.
func ec2EnsureNatEgress(ctx context.Context, natgw EC2NatGateway, zone, privateIP string, network awsWorkloadNetwork) error {
	key := ec2NatEgressKey(natgw.NatGatewayId, zone)
	ec2NatEgressMu.Lock()
	running, ok := ec2NatEgresses[key]
	ec2NatEgressMu.Unlock()
	if ok {
		if running.address == privateIP {
			return nil
		}
		ec2StopNatEgress(key)
	}
	ec2NatEgressMu.Lock()
	defer ec2NatEgressMu.Unlock()
	if _, ok := ec2NatEgresses[key]; ok {
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
	address, err := netip.ParseAddr(privateIP)
	if err != nil {
		return fmt.Errorf("NAT gateway %s private address %q: %w", natgw.NatGatewayId, privateIP, err)
	}
	origin, err := workloadhost.CallbackAddr(simCallbackListenAddr)
	if err != nil {
		return fmt.Errorf("NAT gateway %s: workload callback: %w", natgw.NatGatewayId, err)
	}
	labels := map[string]string{"sockerless-sim-nat-gateway": natgw.NatGatewayId}
	name := "sockerless-sim-nat-" + natgw.NatGatewayId
	if zone != "" {
		labels["sockerless-sim-nat-gateway-zone"] = zone
		name += "-" + zone
	}
	if stale, err := sim.FindExistingContainers(labels); err == nil {
		for _, c := range stale {
			sim.StopAndRemoveContainer(c.ID, 0)
		}
	}
	token := sim.NewSidecarToken()
	sim.RegisterSidecarToken(token, network.Name)
	handle, err := sidecar.StartEgress(ctx, sidecar.EgressSpec{
		Network:      network.Name,
		Address:      address,
		Name:         name,
		Gateway:      natgw.NatGatewayId,
		Sources:      []netip.Prefix{cidr},
		Local:        []netip.Prefix{cidr},
		TCPIdle:      ec2NatGatewayIdleTimeout,
		UDPIdle:      ec2NatGatewayIdleTimeout,
		HTTPUpstream: "http://" + origin,
		Token:        token,
		ExtraHosts:   workloadhost.ExtraHosts(),
		Labels:       labels,
	}, sim.NoopSink{})
	if err != nil {
		sim.UnregisterSidecarToken(token)
		return fmt.Errorf("NAT gateway %s: %w", natgw.NatGatewayId, err)
	}
	ec2NatEgresses[key] = &ec2NatEgress{gateway: natgw.NatGatewayId, address: privateIP, handle: handle, token: token, network: network.Name}
	return nil
}

// ec2RemoveNatGatewayEgress stops every sidecar of the gateway.
func ec2RemoveNatGatewayEgress(natID string) {
	ec2NatEgressMu.Lock()
	var keys []string
	for key, e := range ec2NatEgresses {
		if e.gateway == natID {
			keys = append(keys, key)
		}
	}
	ec2NatEgressMu.Unlock()
	for _, key := range keys {
		ec2StopNatEgress(key)
	}
}

// ec2StopNatEgress stops one sidecar, with the grace it needs to report what
// its connections carried.
func ec2StopNatEgress(key string) {
	ec2NatEgressMu.Lock()
	e := ec2NatEgresses[key]
	delete(ec2NatEgresses, key)
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
	keys := make([]string, 0, len(ec2NatEgresses))
	for key := range ec2NatEgresses {
		keys = append(keys, key)
	}
	ec2NatEgressMu.Unlock()
	for _, key := range keys {
		ec2StopNatEgress(key)
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
// the NAT gateway its route table's 0.0.0.0/0 route names (a regional
// gateway's address in the subnet's zone), no route at all for one naming a
// gateway that is gone or, regional, does not cover the zone, and the
// network's own gateway for an internet gateway. A table with no route out leaves the workload on
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
		if ec2NatAvailabilityMode(natgw) == "regional" {
			subnet, _ := ec2Subnets.Get(subnetID)
			if zone, ok := ec2NatGatewayZoneOf(natgw, subnet.AvailabilityZone); ok && zone.PrivateIp != "" {
				return zone.PrivateIp
			}
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
	for _, running := range ec2RunningMachines() {
		id, handle := running.Key, running.Handle
		inst := ec2InstanceRecord(id)
		if inst.VpcId != vpcID {
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
