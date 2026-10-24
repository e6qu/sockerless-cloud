package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/egress"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// Cloud NAT's data plane is an egress sidecar per subnetwork a Cloud Router's
// NAT covers, at an address the subnetwork keeps for it: an instance on the
// subnetwork with no external address has its default route pointed there,
// so what it sends out of the network leaves from the NAT's address, with
// the NAT's idle timeouts, and the NAT's logging records each connection.

// Cloud NAT's idle timeouts when a router's NAT names none.
const (
	gcpCloudNATTCPIdle = 1200 * time.Second
	gcpCloudNATUDPIdle = 30 * time.Second
)

// gcpCloudNATStopGrace is how long a NAT's sidecar gets to report what its
// connections carried when it goes.
const gcpCloudNATStopGrace = 10 * time.Second

// gcpSubnetworkNATAddress is the address a subnetwork keeps for its Cloud
// NAT's sidecar: the one before the node agent's.
func gcpSubnetworkNATAddress(cidr netip.Prefix) netip.Addr { return gcpRangeAddress(cidr, 4) }

type gcpNATEgress struct {
	handle   *sim.ContainerHandle
	token    string
	network  string
	router   string
	nat      string
	address  netip.Addr
	cidr     netip.Prefix
	subnet   ComputeSubnetwork
	logging  *ComputeRouterNATLogConfig
	natIP    string
	routerID string
}

// gcpRoutersByNetwork finds the routers of a network without reading every
// router on each lookup.
var gcpRoutersByNetwork sim.GenerationIndex[ComputeRouter]

var (
	gcpNATEgressMu sync.Mutex
	// gcpNATEgresses holds the running sidecar of each subnetwork's NAT, by
	// the subnetwork's self link.
	gcpNATEgresses = map[string]*gcpNATEgress{}
	// gcpSubnetworkNetworksUp holds the engine network of every subnetwork
	// this process realized, by the subnetwork's self link.
	gcpSubnetworkNetworksUp sync.Map
)

// gcpSubnetworkNAT is the Cloud NAT that covers a subnetwork: the first NAT
// of a router in the subnetwork's region and network whose source ranges
// name it.
type gcpSubnetworkNAT struct {
	router ComputeRouter
	nat    ComputeRouterNAT
}

func gcpCloudNATOf(subnet ComputeSubnetwork) (gcpSubnetworkNAT, bool) {
	if gcpRouters == nil {
		return gcpSubnetworkNAT{}, false
	}
	project := gcpProjectFromSelfLink(subnet.SelfLink)
	network := gcpCanonicalComputeRef(subnet.Network, project, "global", "networks", "default")
	for _, rt := range gcpRoutersByNetwork.LookupAll(gcpRouters, network, func(rt ComputeRouter) []string {
		return []string{gcpCanonicalComputeRef(rt.Network, gcpProjectFromSelfLink(rt.SelfLink), "global", "networks", "default")}
	}) {
		if gcpLastPathElement(rt.Region) != gcpLastPathElement(subnet.Region) {
			continue
		}
		for _, nat := range rt.Nats {
			if gcpNATCoversSubnetwork(nat, subnet) {
				return gcpSubnetworkNAT{router: rt, nat: nat}, true
			}
		}
	}
	return gcpSubnetworkNAT{}, false
}

func gcpNATCoversSubnetwork(nat ComputeRouterNAT, subnet ComputeSubnetwork) bool {
	switch nat.SourceSubnetworkIpRangesToNat {
	case "", "ALL_SUBNETWORKS_ALL_IP_RANGES", "ALL_SUBNETWORKS_ALL_PRIMARY_IP_RANGES":
		return true
	case "LIST_OF_SUBNETWORKS":
		project := gcpProjectFromSelfLink(subnet.SelfLink)
		region := gcpLastPathElement(subnet.Region)
		for _, named := range nat.Subnetworks {
			if normalizeComputeSubnetworkRef(project, region, named.Name) == subnet.SelfLink {
				return true
			}
		}
	}
	return false
}

func gcpLastPathElement(ref string) string { return ref[strings.LastIndex(ref, "/")+1:] }

// gcpEnsureSubnetworkNAT runs the sidecar of the NAT covering the
// subnetwork on its engine network, replaces it when another NAT covers the
// subnetwork now, and stops it when none does.
func gcpEnsureSubnetworkNAT(ctx context.Context, subnet ComputeSubnetwork, network string) error {
	covering, covered := gcpCloudNATOf(subnet)
	gcpNATEgressMu.Lock()
	running, ok := gcpNATEgresses[subnet.SelfLink]
	gcpNATEgressMu.Unlock()
	if ok && covered && running.router == covering.router.SelfLink && running.nat == covering.nat.Name {
		// The NAT's logging and timeouts follow its configuration as it is now.
		gcpNATEgressMu.Lock()
		running.logging = covering.nat.LogConfig
		gcpNATEgressMu.Unlock()
		return nil
	}
	if ok {
		gcpRemoveSubnetworkNAT(subnet.SelfLink)
	}
	if !covered {
		return nil
	}
	cidr, err := netip.ParsePrefix(subnet.IpCidrRange)
	if err != nil {
		return fmt.Errorf("subnetwork %s range %q: %w", subnet.SelfLink, subnet.IpCidrRange, err)
	}
	origin, err := workloadhost.CallbackAddr(simCallbackListenAddr)
	if err != nil {
		return fmt.Errorf("cloud nat %s: workload callback: %w", covering.nat.Name, err)
	}
	address := gcpSubnetworkNATAddress(cidr)
	name := fmt.Sprintf("sockerless-sim-nat-%s-%s-%s", gcpProjectFromSelfLink(subnet.SelfLink), gcpLastPathElement(subnet.Region), subnet.Name)
	if stale, err := sim.FindExistingContainers(map[string]string{"sockerless-sim-cloud-nat": subnet.SelfLink}); err == nil {
		for _, c := range stale {
			sim.StopAndRemoveContainer(c.ID, 0)
		}
	}
	tcpIdle, udpIdle := gcpCloudNATTCPIdle, gcpCloudNATUDPIdle
	if covering.nat.TcpEstablishedIdleTimeoutSec > 0 {
		tcpIdle = time.Duration(covering.nat.TcpEstablishedIdleTimeoutSec) * time.Second
	}
	if covering.nat.UdpIdleTimeoutSec > 0 {
		udpIdle = time.Duration(covering.nat.UdpIdleTimeoutSec) * time.Second
	}
	token := sim.NewSidecarToken()
	sim.RegisterSidecarToken(token, network)
	handle, err := sidecar.StartEgress(ctx, sidecar.EgressSpec{
		Network:      network,
		Address:      address,
		Name:         name,
		Gateway:      subnet.SelfLink,
		Sources:      []netip.Prefix{cidr},
		Local:        []netip.Prefix{cidr},
		TCPIdle:      tcpIdle,
		UDPIdle:      udpIdle,
		HTTPUpstream: "http://" + origin,
		Token:        token,
		ExtraHosts:   workloadhost.ExtraHosts(),
		Labels:       map[string]string{"sockerless-sim-cloud-nat": subnet.SelfLink},
	}, sim.NoopSink{})
	if err != nil {
		sim.UnregisterSidecarToken(token)
		return fmt.Errorf("cloud nat %s on %s: %w", covering.nat.Name, subnet.SelfLink, err)
	}
	gcpNATEgressMu.Lock()
	gcpNATEgresses[subnet.SelfLink] = &gcpNATEgress{
		handle: handle, token: token, network: network,
		router: covering.router.SelfLink, nat: covering.nat.Name, routerID: covering.router.Id,
		address: address, cidr: cidr, subnet: subnet, logging: covering.nat.LogConfig,
		natIP: gcpCloudNATAddress(covering),
	}
	gcpNATEgressMu.Unlock()
	return nil
}

// gcpCloudNATAddress is the external address a NAT translates to, when its
// configuration names one.
func gcpCloudNATAddress(covering gcpSubnetworkNAT) string {
	if len(covering.nat.NatIps) == 0 || gcpAddresses == nil {
		return ""
	}
	project := gcpProjectFromSelfLink(covering.router.SelfLink)
	region := gcpLastPathElement(covering.router.Region)
	ref := covering.nat.NatIps[0]
	ref = strings.TrimPrefix(ref, "https://www.googleapis.com/compute/v1/")
	if !strings.Contains(ref, "/") {
		ref = computeRegionalAddressLink(project, region, ref)
	}
	if addr, ok := gcpAddresses.Get(ref); ok {
		return addr.Address
	}
	return ""
}

func gcpRemoveSubnetworkNAT(subnetLink string) {
	gcpNATEgressMu.Lock()
	e := gcpNATEgresses[subnetLink]
	delete(gcpNATEgresses, subnetLink)
	gcpNATEgressMu.Unlock()
	if e == nil {
		return
	}
	sim.StopContainer(e.handle.ContainerID, gcpCloudNATStopGrace)
	e.handle.Cancel()
	sim.UnregisterSidecarToken(e.token)
}

func gcpStopNATEgresses() {
	gcpNATEgressMu.Lock()
	links := make([]string, 0, len(gcpNATEgresses))
	for link := range gcpNATEgresses {
		links = append(links, link)
	}
	gcpNATEgressMu.Unlock()
	for _, link := range links {
		gcpRemoveSubnetworkNAT(link)
	}
}

// gcpReapplyNAT follows a router's change on every subnetwork whose network
// is up, and then points every instance where its subnetwork's NAT says.
func gcpReapplyNAT() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	gcpSubnetworkNetworksUp.Range(func(key, value any) bool {
		link, _ := key.(string)
		network, _ := value.(string)
		subnet, ok := gcpSubnetworks.Get(link)
		if !ok {
			return true
		}
		if err := gcpEnsureSubnetworkNAT(ctx, subnet, network); err != nil {
			log.Printf("compute: cloud nat of %s: %v", link, err)
		}
		return true
	})
	gcpReapplyRoutes()
}

// gcpInstanceGateway is where an instance sends what leaves its network, in
// the terms of sim.ContainerConfig.Gateway: the sidecar of the Cloud NAT
// covering its subnetwork when the instance has no external address, and the
// network's own gateway otherwise, or while no NAT covers it, the gap
// BUGS.md records.
func gcpInstanceGateway(inst ComputeInstance) string {
	if len(inst.NetworkInterfaces) == 0 {
		return ""
	}
	nic := inst.NetworkInterfaces[0]
	for _, access := range nic.AccessConfigs {
		if access.Type == "" || strings.EqualFold(access.Type, "ONE_TO_ONE_NAT") {
			return ""
		}
	}
	gcpNATEgressMu.Lock()
	defer gcpNATEgressMu.Unlock()
	if e, ok := gcpNATEgresses[nic.Subnetwork]; ok {
		return e.address.String()
	}
	return ""
}

// gcpReapplyRoutes points every running instance's machine where its
// subnetwork's NAT says now. A backend that does not route is left as it is.
func gcpReapplyRoutes() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, running := range gcpRunningInstances() {
		link, handle := running.Key, running.Handle
		inst, ok := gcpInstances.Get(link)
		if !ok {
			continue
		}
		if err := handle.SetGateway(ctx, gcpInstanceGateway(inst)); err != nil && !errors.Is(err, placement.ErrNoRouteControl) {
			log.Printf("compute: route %s: %v", link, err)
		}
	}
}

// gcpTakeNATEgressReport writes what a NAT's sidecar reports to Cloud
// Logging as the NAT's logs, when its logging is enabled: one
// compute.googleapis.com/nat_flows entry per connection the NAT translated,
// with the endpoint and the network it came from, under the nat_gateway
// resource.
func gcpTakeNATEgressReport(network string, report egress.Report) {
	gcpNATEgressMu.Lock()
	e, ok := gcpNATEgresses[report.Gateway]
	var logging *ComputeRouterNATLogConfig
	if ok {
		logging = e.logging
	}
	gcpNATEgressMu.Unlock()
	if !ok || e.network != network || logging == nil || !logging.Enable {
		return
	}
	filter := strings.ToUpper(logging.Filter)
	if filter == "ERRORS_ONLY" {
		return
	}
	project := gcpProjectFromSelfLink(e.subnet.SelfLink)
	region := gcpLastPathElement(e.subnet.Region)
	resource := MonitoredResource{Type: "nat_gateway", Labels: map[string]string{
		"gateway_name": e.nat,
		"project_id":   project,
		"region":       region,
		"router_id":    e.routerID,
	}}
	var entries []LogEntry
	for _, ev := range report.Events {
		if ev.Kind != egress.Established {
			continue
		}
		connection := map[string]any{
			"src_ip":    ev.Source,
			"src_port":  ev.SourcePort,
			"dest_ip":   ev.Destination,
			"dest_port": ev.DestinationPort,
			"protocol":  gcpProtocolNumber(ev.Protocol),
			"nat_port":  ev.ViaPort,
		}
		if e.natIP != "" {
			connection["nat_ip"] = e.natIP
		}
		payload := map[string]any{
			"connection":        connection,
			"allocation_status": "OK",
			"gateway_identifiers": map[string]any{
				"gateway_name": e.nat,
				"router_name":  gcpLastPathElement(e.router),
				"region":       region,
			},
			"vpc": map[string]any{
				"project_id":      project,
				"vpc_name":        gcpLastPathElement(e.subnet.Network),
				"subnetwork_name": e.subnet.Name,
			},
		}
		if inst, ok := gcpInstanceAtAddress(e.subnet.SelfLink, ev.Source); ok {
			payload["endpoint"] = map[string]any{
				"project_id": gcpProjectFromSelfLink(inst.SelfLink),
				"vm_name":    inst.Name,
				"region":     region,
				"zone":       gcpLastPathElement(inst.Zone),
			}
		}
		entries = append(entries, LogEntry{
			Timestamp:   ev.At.UTC().Format(time.RFC3339Nano),
			Severity:    "INFO",
			JsonPayload: payload,
		})
	}
	if len(entries) == 0 {
		return
	}
	logName := fmt.Sprintf("projects/%s/logs/compute.googleapis.com%%2Fnat_flows", project)
	if err := writeLogEntries(logName, &resource, nil, entries); err != nil {
		log.Printf("cloud nat logs: %v", err)
	}
}

func gcpProtocolNumber(protocol string) int {
	switch protocol {
	case "tcp":
		return 6
	case "udp":
		return 17
	}
	return 0
}

// gcpInstanceAtAddress is the instance whose first interface holds the
// address on the subnetwork.
func gcpInstanceAtAddress(subnetLink, address string) (ComputeInstance, bool) {
	if gcpInstances == nil {
		return ComputeInstance{}, false
	}
	for _, inst := range gcpInstancesBySubnetwork.LookupAll(gcpInstances, subnetLink, func(inst ComputeInstance) []string {
		keys := make([]string, 0, len(inst.NetworkInterfaces))
		for _, ni := range inst.NetworkInterfaces {
			keys = append(keys, ni.Subnetwork)
		}
		return keys
	}) {
		if len(inst.NetworkInterfaces) > 0 && inst.NetworkInterfaces[0].NetworkIP == address {
			return inst, true
		}
	}
	return ComputeInstance{}, false
}
