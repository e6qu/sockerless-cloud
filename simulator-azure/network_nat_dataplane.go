package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/egress"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// An Azure NAT Gateway's data plane is an egress sidecar on its virtual
// network, at the last address of the first subnet it is attached to, which
// Azure reserves: a machine on an attached subnet has its default route
// pointed there, so what it sends out of the network leaves from the
// gateway's address, with the gateway's idle timeout, and counts in the
// gateway's metrics.

// azureNATGatewayUDPIdle is the UDP idle timeout of a NAT gateway, which
// Azure fixes at four minutes; the TCP one is the gateway's
// idleTimeoutInMinutes.
const azureNATGatewayUDPIdle = 4 * time.Minute

// azureNATGatewayStopGrace is how long a gateway's sidecar gets to report
// what its connections carried when the gateway goes.
const azureNATGatewayStopGrace = 10 * time.Second

type azureNATEgress struct {
	handle  *sim.ContainerHandle
	token   string
	network string
	address netip.Addr
	sources string
	idle    time.Duration
}

var (
	azureNATEgressMu sync.Mutex
	// azureNATEgresses holds the running sidecar of each NAT gateway, by the
	// gateway's lower-cased resource id.
	azureNATEgresses = map[string]*azureNATEgress{}
	// azureVNetNetworksUp holds the lower-cased resource id of the virtual
	// network behind every prefix network this process realized, by the
	// engine network's name.
	azureVNetNetworksUp sync.Map
)

// azureNATGatewaySubnets are the subnets attached to the gateway, in a fixed
// order, with the prefix of each.
func azureNATGatewaySubnets(gatewayID string) ([]Subnet, []netip.Prefix) {
	subnets := azureSubnets.Filter(func(sn Subnet) bool {
		return sn.Properties.NatGateway != nil && strings.EqualFold(sn.Properties.NatGateway.ID, gatewayID)
	})
	sort.Slice(subnets, func(i, j int) bool { return strings.ToLower(subnets[i].ID) < strings.ToLower(subnets[j].ID) })
	var kept []Subnet
	var prefixes []netip.Prefix
	for _, sn := range subnets {
		cidr, err := azureSubnetIPv4CIDR(sn.Properties)
		if err != nil {
			continue
		}
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			continue
		}
		kept = append(kept, sn)
		prefixes = append(prefixes, prefix.Masked())
	}
	return kept, prefixes
}

// azureSubnetLastAddress is the last address of a subnet's range, one Azure
// reserves in every subnet, where the gateway's sidecar sits inside the
// virtual network's engine network.
func azureSubnetLastAddress(prefix netip.Prefix) netip.Addr {
	base := prefix.Masked().Addr().As4()
	n := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	n += 1<<(32-prefix.Bits()) - 1
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}

// azureEnsureNATGatewayEgress runs the gateway's sidecar on its virtual
// network when that network is up, replaces it when the gateway's subnets or
// timeout changed, and stops it when no subnet is attached any more.
func azureEnsureNATGatewayEgress(ctx context.Context, gw NatGateway) error {
	subnets, prefixes := azureNATGatewaySubnets(gw.ID)
	key := strings.ToLower(gw.ID)
	if len(subnets) == 0 {
		azureRemoveNATGatewayEgress(gw.ID)
		return nil
	}
	vnetID := azureVNetIDOfSubnet(subnets[0].ID)
	vnet, ok := azureVnets.Get(vnetID)
	if !ok {
		return fmt.Errorf("virtual network %s of NAT gateway %s not found", vnetID, gw.ID)
	}
	// The sidecar sits on the engine network of the prefix the gateway's
	// first subnet lies in.
	prefix, first, err := azureVNetPrefixOfSubnet(vnet, subnets[0])
	if err != nil {
		return err
	}
	network := dockerNetForVNetPrefix(vnet.ID, prefix, first)
	if _, up := azureVNetNetworksUp.Load(network); !up {
		return nil
	}
	vnetPrefixes, err := azureVNetIPv4Prefixes(vnet)
	if err != nil {
		return err
	}
	idle := time.Duration(gw.Properties.IdleTimeoutInMinutes) * time.Minute
	if idle <= 0 {
		idle = 4 * time.Minute
	}
	address := azureSubnetLastAddress(prefixes[0])
	sources := make([]string, len(prefixes))
	for i, p := range prefixes {
		sources[i] = p.String()
	}
	wanted := strings.Join(sources, ",")
	azureNATEgressMu.Lock()
	running, ok := azureNATEgresses[key]
	azureNATEgressMu.Unlock()
	if ok && running.network == network && running.address == address && running.sources == wanted && running.idle == idle {
		return nil
	}
	if ok {
		azureRemoveNATGatewayEgress(gw.ID)
	}
	origin, err := workloadhost.CallbackAddr(simListenAddr)
	if err != nil {
		return fmt.Errorf("NAT gateway %s: workload callback: %w", gw.ID, err)
	}
	if stale, err := sim.FindExistingContainers(map[string]string{"sockerless-sim-nat-gateway": key}); err == nil {
		for _, c := range stale {
			sim.StopAndRemoveContainer(c.ID, 0)
		}
	}
	token := sim.NewSidecarToken()
	sim.RegisterSidecarToken(token, network)
	handle, err := sidecar.StartEgress(ctx, sidecar.EgressSpec{
		Network:      network,
		Address:      address,
		Name:         "sockerless-sim-nat-" + sanitizeContainerName(gw.Name) + "-" + sim.RandomHex(4),
		Gateway:      gw.ID,
		Sources:      prefixes,
		Local:        vnetPrefixes,
		TCPIdle:      idle,
		UDPIdle:      azureNATGatewayUDPIdle,
		HTTPUpstream: "http://" + origin,
		Token:        token,
		ExtraHosts:   workloadhost.ExtraHosts(),
		Labels:       map[string]string{"sockerless-sim-nat-gateway": key},
	}, sim.NoopSink{})
	if err != nil {
		sim.UnregisterSidecarToken(token)
		return fmt.Errorf("NAT gateway %s: %w", gw.ID, err)
	}
	azureNATEgressMu.Lock()
	azureNATEgresses[key] = &azureNATEgress{handle: handle, token: token, network: network, address: address, sources: wanted, idle: idle}
	azureNATEgressMu.Unlock()
	return nil
}

func azureRemoveNATGatewayEgress(gatewayID string) {
	key := strings.ToLower(gatewayID)
	azureNATEgressMu.Lock()
	e := azureNATEgresses[key]
	delete(azureNATEgresses, key)
	azureNATEgressMu.Unlock()
	if e == nil {
		return
	}
	sim.StopContainer(e.handle.ContainerID, azureNATGatewayStopGrace)
	e.handle.Cancel()
	sim.UnregisterSidecarToken(e.token)
}

func azureStopNATEgresses() {
	azureNATEgressMu.Lock()
	ids := make([]string, 0, len(azureNATEgresses))
	for id := range azureNATEgresses {
		ids = append(ids, id)
	}
	azureNATEgressMu.Unlock()
	for _, id := range ids {
		azureRemoveNATGatewayEgress(id)
	}
}

// azureEnsureVNetNATGateways runs the sidecar of every NAT gateway attached
// to a subnet of the virtual network, once its network is up.
func azureEnsureVNetNATGateways(ctx context.Context, vnetID string) error {
	if azureNatGateways == nil {
		return nil
	}
	for _, gw := range azureNatGateways.List() {
		subnets, _ := azureNATGatewaySubnets(gw.ID)
		if len(subnets) == 0 || !strings.EqualFold(azureVNetIDOfSubnet(subnets[0].ID), vnetID) {
			continue
		}
		if err := azureEnsureNATGatewayEgress(ctx, gw); err != nil {
			return err
		}
	}
	return nil
}

// azureReapplyNAT follows a gateway's or a subnet's change on every gateway,
// and then points every machine where its subnet's gateway says.
func azureReapplyNAT() {
	if azureNatGateways == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, gw := range azureNatGateways.List() {
		if err := azureEnsureNATGatewayEgress(ctx, gw); err != nil {
			log.Printf("network: NAT gateway %s: %v", gw.ID, err)
		}
	}
	azureReapplyRoutes()
}

// azureVMGateway is where a machine sends what leaves its virtual network,
// in the terms of sim.ContainerConfig.Gateway: the sidecar of the NAT
// gateway its subnet is attached to, and the network's own gateway
// otherwise, as Azure's default outbound access.
func azureVMGateway(nic NetworkInterface) string {
	ipcfg := azurePrimaryIPConfig(nic)
	if ipcfg.Properties.Subnet == nil {
		return ""
	}
	subnet, ok := azureSubnetByID(ipcfg.Properties.Subnet.ID)
	if !ok || subnet.Properties.NatGateway == nil {
		return ""
	}
	azureNATEgressMu.Lock()
	defer azureNATEgressMu.Unlock()
	if e, ok := azureNATEgresses[strings.ToLower(subnet.Properties.NatGateway.ID)]; ok {
		return e.address.String()
	}
	return ""
}

// azureReapplyRoutes points every running machine where its subnet's NAT
// gateway says now. A backend that does not route is left as it is.
func azureReapplyRoutes() {
	if azureVMs == nil || azureNICs == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, w := range azureRunningVMs() {
		vm, ok := azureVMs.Get(w.booted.vmID)
		if !ok || len(vm.Properties.NetworkProfile.NetworkInterfaces) == 0 {
			continue
		}
		nic, ok := azureNICs.Get(vm.Properties.NetworkProfile.NetworkInterfaces[0].ID)
		if !ok {
			continue
		}
		if err := w.handle.SetGateway(ctx, azureVMGateway(nic)); err != nil && !errors.Is(err, placement.ErrNoRouteControl) {
			log.Printf("compute: route %s: %v", vm.ID, err)
		}
	}
}

// azureTakeNATEgressReport turns what a gateway's sidecar reports into the
// gateway's Azure Monitor metrics: every connection a source opens is an
// attempted SNAT connection, one the destination refuses a failed one, every
// one that ends its bytes in each direction, and each minute the connections
// open and the data path's availability.
func azureTakeNATEgressReport(network string, report egress.Report) {
	key := strings.ToLower(report.Gateway)
	azureNATEgressMu.Lock()
	e, ok := azureNATEgresses[key]
	azureNATEgressMu.Unlock()
	if !ok || e.network != network {
		return
	}
	gw, ok := azureNatGateways.Get(report.Gateway)
	if !ok {
		return
	}
	for _, ev := range report.Events {
		protocol := map[string]string{"Protocol": strings.ToUpper(ev.Protocol)}
		switch ev.Kind {
		case egress.Opened:
			azureRecordMetric(gw.ID, "SNATConnectionCount", "Count", 1, map[string]string{"Protocol": strings.ToUpper(ev.Protocol), "ConnectionState": "Attempted"}, ev.At)
		case egress.Closed:
			if ev.Reason == egress.ReasonUnreachable {
				azureRecordMetric(gw.ID, "SNATConnectionCount", "Count", 1, map[string]string{"Protocol": strings.ToUpper(ev.Protocol), "ConnectionState": "Failed"}, ev.At)
				continue
			}
			azureRecordMetric(gw.ID, "ByteCount", "Bytes", float64(ev.BytesOut), map[string]string{"Protocol": strings.ToUpper(ev.Protocol), "Direction": "Out"}, ev.At)
			azureRecordMetric(gw.ID, "ByteCount", "Bytes", float64(ev.BytesIn), map[string]string{"Protocol": strings.ToUpper(ev.Protocol), "Direction": "In"}, ev.At)
		case egress.Active:
			azureRecordMetric(gw.ID, "TotalConnectionCount", "Count", float64(ev.Active), protocol, ev.At)
			azureRecordMetric(gw.ID, "DatapathAvailability", "Count", 100, nil, ev.At)
		}
	}
}
