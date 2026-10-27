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

// A NAT gateway's data plane is an egress sidecar at the last address of the
// first attached subnet, which Azure reserves: machines on attached subnets
// route through it, so their outbound traffic leaves from the gateway's
// address, with its idle timeout, and counts in its metrics.

// Azure fixes the UDP idle timeout at four minutes; TCP's is
// idleTimeoutInMinutes.
const azureNATGatewayUDPIdle = 4 * time.Minute

// How long the sidecar gets to report its connections when the gateway goes.
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
	// Keyed by lowercased gateway ID.
	azureNATEgresses = map[string]*azureNATEgress{}
	// The lowercased virtual network ID behind each prefix network this process
	// realized, by engine network name.
	azureVNetNetworksUp sync.Map
)

// In a fixed order, with each subnet's prefix.
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

// Azure reserves the last address of every subnet; the sidecar sits there.
func azureSubnetLastAddress(prefix netip.Prefix) netip.Addr {
	base := prefix.Masked().Addr().As4()
	n := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	n += 1<<(32-prefix.Bits()) - 1
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}

// Replace the sidecar when the subnets or timeout change, and stop it when no
// subnet is attached.
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
	// The sidecar sits on the engine network of the first subnet's prefix.
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

// Reconcile every gateway, then route every machine as its subnet's gateway
// says.
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

// In sim.ContainerConfig.Gateway terms: the sidecar of the subnet's NAT
// gateway, else the network's own gateway, as Azure's default outbound access.
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

// A backend that does not route is left as it is.
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

// Every connection opened is an attempted SNAT connection, one refused a
// failed one; each ended connection adds its bytes per direction; each minute
// adds open connections and data-path availability.
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
