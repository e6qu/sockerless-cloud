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
// address on the VPC network. A workload whose route table sends 0.0.0.0/0 to
// the gateway gets that address as default route, so its egress leaves from
// the gateway with the gateway's idle timeout and counts in its metrics. A
// regional gateway runs one sidecar per covered zone.

// Amazon VPC drops a NAT connection idle for 350 seconds.
const ec2NatGatewayIdleTimeout = 350 * time.Second

// Grace for the sidecar to report what its connections carried.
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
	// Keyed by gateway id, plus zone for a regional gateway.
	ec2NatEgresses = map[string]*ec2NatEgress{}
)

func ec2NatEgressKey(natID, zone string) string {
	if zone == "" {
		return natID
	}
	return natID + "/" + zone
}

// The VPC's network exists by now.
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

// A zonal gateway runs at its primary address, a regional one at its address
// in each covered zone; a sidecar whose zone address moved is replaced.
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

// Replace a container a previous process left: its token died with that
// process.
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

// The subnet's associated table, else the VPC's main table.
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

// Returns the NAT gateway's private address the 0.0.0.0/0 route names (a
// regional gateway's address in the subnet's zone); no route when that gateway
// is gone or does not cover the zone; and the network's own gateway for an
// internet gateway. A table with no route out also leaves the workload on the
// network's gateway.
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

// A backend that does not route is left alone.
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
		if handle := procs.mainHandle(); handle != nil {
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

func ec2RouteTableVPC(routeTableID string) string {
	rt, _ := ec2RouteTables.Get(routeTableID)
	return rt.VpcId
}

// Publish AWS/NATGateway metrics under NatGatewayId: an attempt per opened
// connection, an establishment per accepted one, bytes per direction and idle
// timeouts per ended one, and active connections per minute.
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
