package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/gwlb"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// A Gateway Load Balancer's data plane is one node per load balancer and
// zone, a sidecar on the load balancer's network at its address in the
// zone's subnet and on each of the zone's Gateway Load Balancer endpoints'
// networks at the endpoint's address. A route that names an endpoint sends
// what it carries to the endpoint's address; the node wraps it in GENEVE to
// an appliance the target group's flow stickiness picks, and sends what the
// appliance returns on as the endpoint's subnet routes it.

const (
	elbv2GatewayInterfaceType  = "gateway_load_balancer"
	elbv2EndpointInterfaceType = "gateway_load_balancer_endpoint"
	elbv2GatewayNodeStopGrace  = 5 * time.Second
	elbv2GatewayNodeStartLimit = 3 * time.Minute
)

// The load balancer's network interface in each of its subnets carries its
// address there, which is where its node stands.
func elbv2GatewayInterfaceDescription(lb ELBv2LoadBalancer) string {
	return "ELB gwy/" + lb.Name + "/" + elbv2LoadBalancerID(lb.Arn)
}

// Every request a Gateway Load Balancer's targets admit looks up the load
// balancer's network interfaces.
var ec2NetworkInterfacesByDescription sim.GenerationIndex[EC2NetworkInterface]

func elbv2GatewayInterfaces(lb ELBv2LoadBalancer) []EC2NetworkInterface {
	return ec2NetworkInterfacesByDescription.LookupAll(ec2NetworkInterfaces, elbv2GatewayInterfaceDescription(lb), func(eni EC2NetworkInterface) []string {
		return []string{eni.Description}
	})
}

func elbv2LoadBalancerSubnetIDs(lb ELBv2LoadBalancer) []string {
	subnets := slices.Clone(lb.Subnets)
	for _, m := range lb.SubnetMappings {
		if !slices.Contains(subnets, m.SubnetId) {
			subnets = append(subnets, m.SubnetId)
		}
	}
	return subnets
}

// elbv2SyncGatewayInterfaces gives a Gateway Load Balancer a network
// interface with an address in each of its subnets, and removes the ones in
// subnets it left.
func elbv2SyncGatewayInterfaces(lb ELBv2LoadBalancer) error {
	if lb.Type != "gateway" {
		return nil
	}
	description := elbv2GatewayInterfaceDescription(lb)
	subnets := elbv2LoadBalancerSubnetIDs(lb)
	have := map[string]bool{}
	for _, eni := range elbv2GatewayInterfaces(lb) {
		if !slices.Contains(subnets, eni.SubnetId) {
			ec2NetworkInterfaces.Delete(eni.NetworkInterfaceId)
			continue
		}
		have[eni.SubnetId] = true
	}
	for _, subnetID := range subnets {
		if have[subnetID] {
			continue
		}
		if _, err := elbv2CreateManagedInterface(subnetID, description, elbv2GatewayInterfaceType, nil); err != nil {
			return err
		}
	}
	return nil
}

func elbv2RemoveGatewayInterfaces(lb ELBv2LoadBalancer) {
	for _, eni := range elbv2GatewayInterfaces(lb) {
		ec2NetworkInterfaces.Delete(eni.NetworkInterfaceId)
	}
}

// elbv2CreateManagedInterface is a network interface AWS creates on a
// service's behalf, at the next free address of the subnet.
func elbv2CreateManagedInterface(subnetID, description, interfaceType string, securityGroups []string) (EC2NetworkInterface, error) {
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return EC2NetworkInterface{}, fmt.Errorf("subnet %s not found", subnetID)
	}
	address, err := AllocateSubnetIP(subnetID)
	if err != nil {
		return EC2NetworkInterface{}, err
	}
	eni := EC2NetworkInterface{
		NetworkInterfaceId: ec2ID("eni"),
		SubnetId:           subnetID,
		VpcId:              subnet.VpcId,
		PrivateIpAddress:   address,
		Status:             "in-use",
		Description:        description,
		InterfaceType:      interfaceType,
		SecurityGroupIds:   securityGroups,
		OwnerId:            ec2Owner(),
	}
	ec2NetworkInterfaces.Put(eni.NetworkInterfaceId, eni)
	return eni, nil
}

// elbv2GatewayLoadBalancerOfEndpoint is the Gateway Load Balancer an
// available Gateway Load Balancer endpoint's service fronts.
func elbv2GatewayLoadBalancerOfEndpoint(ep EC2VpcEndpoint) (ELBv2LoadBalancer, bool) {
	if ep.VpcEndpointType != "GatewayLoadBalancer" || (ep.State != "Available" && ep.State != "Pending") {
		return ELBv2LoadBalancer{}, false
	}
	for _, cfg := range ec2VpcEndpointServices.List() {
		if cfg.ServiceName != ep.ServiceName {
			continue
		}
		for _, arn := range cfg.GatewayLoadBalancerArns {
			if lb, ok := elbv2LoadBalancers.Get(arn); ok && lb.Type == "gateway" {
				return lb, true
			}
		}
	}
	return ELBv2LoadBalancer{}, false
}

// ec2EndpointAddress is the address of a Gateway Load Balancer endpoint's
// network interface, the next hop a route naming the endpoint goes to.
func ec2EndpointAddress(endpointID string) (EC2NetworkInterface, bool) {
	ep, ok := ec2VpcEndpoints.Get(endpointID)
	if !ok || ep.VpcEndpointType != "GatewayLoadBalancer" || len(ep.NetworkInterfaceIds) == 0 {
		return EC2NetworkInterface{}, false
	}
	eni, ok := ec2NetworkInterfaces.Get(ep.NetworkInterfaceIds[0])
	return eni, ok && eni.PrivateIpAddress != ""
}

type elbv2GatewayNodeKey struct {
	lbArn string
	zone  string
}

func (k elbv2GatewayNodeKey) String() string { return elbv2LoadBalancerID(k.lbArn) + "/" + k.zone }

// elbv2GatewayNodeDesign is what places a node: change it and the node is
// replaced.
type elbv2GatewayNodeDesign struct {
	vpcID     string
	address   string
	endpoints []elbv2GatewayNodeEndpoint
}

type elbv2GatewayNodeEndpoint struct {
	id      string
	vpcID   string
	subnet  string
	address netip.Prefix
}

func (d elbv2GatewayNodeDesign) signature() string {
	parts := []string{d.vpcID, d.address}
	for _, e := range d.endpoints {
		parts = append(parts, e.id+"="+e.address.String())
	}
	return strings.Join(parts, ",")
}

type elbv2GatewayNode struct {
	design    elbv2GatewayNodeDesign
	signature string
	handle    *sim.ContainerHandle
	token     string
}

var (
	elbv2GatewayNodesMu sync.Mutex
	elbv2GatewayNodes   = map[elbv2GatewayNodeKey]*elbv2GatewayNode{}
	// A reconcile asked for while one runs runs once more after it.
	elbv2GatewayReconcileWanted = make(chan struct{}, 1)
	// Closed and replaced whenever what a node's state reads may have changed.
	elbv2GatewayStateMu      sync.Mutex
	elbv2GatewayStateChanged = make(chan struct{})
)

func elbv2RequestGatewayReconcile() {
	select {
	case elbv2GatewayReconcileWanted <- struct{}{}:
	default:
	}
}

// elbv2NotifyGatewayState wakes the nodes waiting on their state.
func elbv2NotifyGatewayState() {
	elbv2GatewayStateMu.Lock()
	close(elbv2GatewayStateChanged)
	elbv2GatewayStateChanged = make(chan struct{})
	elbv2GatewayStateMu.Unlock()
}

func elbv2GatewayStateSignal() <-chan struct{} {
	elbv2GatewayStateMu.Lock()
	defer elbv2GatewayStateMu.Unlock()
	return elbv2GatewayStateChanged
}

func registerELBv2GatewayNodeState(srv *sim.Server) {
	srv.HandleFunc("GET "+gwlb.StatePath, handleELBv2GatewayNodeState)
}

// startELBv2GatewayNodes reconciles the nodes at once, reading the Amazon EC2
// and Elastic Load Balancing stores, so it starts once every slice is
// registered and only where workloads run.
func startELBv2GatewayNodes(srv *sim.Server) {
	srv.StartBackground("Gateway Load Balancer nodes", func(ctx context.Context) {
		defer elbv2StopGatewayNodes()
		elbv2RequestGatewayReconcile()
		for {
			select {
			case <-ctx.Done():
				return
			case <-elbv2GatewayReconcileWanted:
				elbv2ReconcileGatewayNodes(ctx)
			}
		}
	})
}

// elbv2DesiredGatewayNodes is one node per Gateway Load Balancer and zone
// that holds an available endpoint of the load balancer's service, in a
// zone the load balancer has a subnet in.
func elbv2DesiredGatewayNodes() map[elbv2GatewayNodeKey]elbv2GatewayNodeDesign {
	desired := map[elbv2GatewayNodeKey]elbv2GatewayNodeDesign{}
	for _, ep := range ec2VpcEndpoints.List() {
		lb, ok := elbv2GatewayLoadBalancerOfEndpoint(ep)
		if !ok || len(ep.SubnetIds) == 0 {
			continue
		}
		eni, ok := ec2EndpointAddress(ep.VpcEndpointId)
		if !ok {
			continue
		}
		subnet, ok := ec2Subnets.Get(eni.SubnetId)
		if !ok {
			continue
		}
		key := elbv2GatewayNodeKey{lbArn: lb.Arn, zone: subnet.AvailabilityZone}
		design, ok := desired[key]
		if !ok {
			lbENI, found := elbv2GatewayInterfaceInZone(lb, subnet.AvailabilityZone)
			if !found {
				continue
			}
			design = elbv2GatewayNodeDesign{vpcID: lb.VpcID, address: lbENI.PrivateIpAddress}
		}
		vpc, ok := ec2Vpcs.Get(subnet.VpcId)
		if !ok {
			continue
		}
		cidr, err := netip.ParsePrefix(vpc.CidrBlock)
		addr, aerr := netip.ParseAddr(eni.PrivateIpAddress)
		if err != nil || aerr != nil {
			continue
		}
		design.endpoints = append(design.endpoints, elbv2GatewayNodeEndpoint{
			id: ep.VpcEndpointId, vpcID: subnet.VpcId, subnet: eni.SubnetId, address: netip.PrefixFrom(addr, cidr.Bits()),
		})
		desired[key] = design
	}
	for key, design := range desired {
		slices.SortFunc(design.endpoints, func(a, b elbv2GatewayNodeEndpoint) int { return strings.Compare(a.id, b.id) })
		desired[key] = design
	}
	return desired
}

func elbv2GatewayInterfaceInZone(lb ELBv2LoadBalancer, zone string) (EC2NetworkInterface, bool) {
	for _, eni := range elbv2GatewayInterfaces(lb) {
		if subnet, ok := ec2Subnets.Get(eni.SubnetId); ok && subnet.AvailabilityZone == zone {
			return eni, true
		}
	}
	return EC2NetworkInterface{}, false
}

// elbv2ReconcileGatewayNodes starts the nodes the load balancers and
// endpoints call for, replaces the ones whose placement changed, and stops
// the rest.
func elbv2ReconcileGatewayNodes(ctx context.Context) {
	if ec2VpcEndpoints == nil || elbv2LoadBalancers == nil {
		return
	}
	desired := elbv2DesiredGatewayNodes()
	elbv2GatewayNodesMu.Lock()
	var stale []elbv2GatewayNodeKey
	for key, node := range elbv2GatewayNodes {
		if design, ok := desired[key]; !ok || design.signature() != node.signature {
			stale = append(stale, key)
		}
	}
	elbv2GatewayNodesMu.Unlock()
	for _, key := range stale {
		elbv2StopGatewayNode(key)
	}
	for key, design := range desired {
		elbv2GatewayNodesMu.Lock()
		_, running := elbv2GatewayNodes[key]
		elbv2GatewayNodesMu.Unlock()
		if running {
			continue
		}
		startCtx, cancel := context.WithTimeout(ctx, elbv2GatewayNodeStartLimit)
		err := elbv2StartGatewayNode(startCtx, key, design)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[sim-elbv2] Gateway Load Balancer node %s: %v\n", key, err)
		}
	}
	elbv2NotifyGatewayState()
}

func elbv2StartGatewayNode(ctx context.Context, key elbv2GatewayNodeKey, design elbv2GatewayNodeDesign) error {
	network, err := ec2EnsureVPCNetwork(ctx, design.vpcID)
	if err != nil {
		return err
	}
	address, err := netip.ParseAddr(design.address)
	if err != nil {
		return err
	}
	var endpoints []sidecar.GatewayLoadBalancerEndpoint
	for _, e := range design.endpoints {
		endpointNetwork, err := ec2EnsureVPCNetwork(ctx, e.vpcID)
		if err != nil {
			return fmt.Errorf("endpoint %s: %w", e.id, err)
		}
		endpoints = append(endpoints, sidecar.GatewayLoadBalancerEndpoint{ID: e.id, Network: endpointNetwork.Name, Address: e.address})
	}
	origin, err := workloadhost.CallbackAddr(simCallbackListenAddr)
	if err != nil {
		return fmt.Errorf("workload callback: %w", err)
	}
	labels := map[string]string{"sockerless-sim-gwlb": elbv2LoadBalancerID(key.lbArn), "sockerless-sim-gwlb-zone": key.zone}
	if stale, err := sim.FindExistingContainers(labels); err == nil {
		for _, c := range stale {
			sim.StopAndRemoveContainer(c.ID, 0)
		}
	}
	token := sim.NewSidecarToken()
	sim.RegisterSidecarToken(token, gwlb.TokenNetwork(key.String()))
	elbv2GatewayNodesMu.Lock()
	elbv2GatewayNodes[key] = &elbv2GatewayNode{design: design, signature: design.signature(), token: token}
	elbv2GatewayNodesMu.Unlock()
	handle, err := sidecar.StartGatewayLoadBalancerNode(ctx, sidecar.GatewayLoadBalancerNodeSpec{
		Node:         key.String(),
		Name:         "sockerless-sim-gwlb-" + elbv2LoadBalancerID(key.lbArn) + "-" + key.zone,
		Network:      network.Name,
		Address:      address,
		Endpoints:    endpoints,
		HTTPUpstream: "http://" + origin,
		Token:        token,
		ExtraHosts:   workloadhost.ExtraHosts(),
		Labels:       labels,
	}, sim.NoopSink{})
	if err != nil {
		elbv2GatewayNodesMu.Lock()
		delete(elbv2GatewayNodes, key)
		elbv2GatewayNodesMu.Unlock()
		sim.UnregisterSidecarToken(token)
		return err
	}
	elbv2GatewayNodesMu.Lock()
	if node := elbv2GatewayNodes[key]; node != nil && node.token == token {
		node.handle = handle
	}
	elbv2GatewayNodesMu.Unlock()
	return nil
}

func elbv2StopGatewayNode(key elbv2GatewayNodeKey) {
	elbv2GatewayNodesMu.Lock()
	node := elbv2GatewayNodes[key]
	delete(elbv2GatewayNodes, key)
	elbv2GatewayNodesMu.Unlock()
	if node == nil {
		return
	}
	if node.handle != nil {
		sim.StopContainer(node.handle.ContainerID, elbv2GatewayNodeStopGrace)
		node.handle.Cancel()
	}
	sim.UnregisterSidecarToken(node.token)
}

func elbv2StopGatewayNodes() {
	elbv2GatewayNodesMu.Lock()
	keys := make([]elbv2GatewayNodeKey, 0, len(elbv2GatewayNodes))
	for key := range elbv2GatewayNodes {
		keys = append(keys, key)
	}
	elbv2GatewayNodesMu.Unlock()
	for _, key := range keys {
		elbv2StopGatewayNode(key)
	}
}

// handleELBv2GatewayNodeState answers a node's long poll with its state once
// it differs from the version the node holds, or after a while unchanged.
func handleELBv2GatewayNodeState(w http.ResponseWriter, r *http.Request) {
	nodeName := r.URL.Query().Get("node")
	if nodeName == "" || sim.SidecarNetwork(r.Context()) != gwlb.TokenNetwork(nodeName) {
		http.Error(w, "only a Gateway Load Balancer node asks here, for itself", http.StatusUnauthorized)
		return
	}
	held := r.URL.Query().Get("version")
	if held != "" {
		elbv2GatewayEndpointsServed(nodeName)
	}
	deadline := time.NewTimer(gwlb.StateWaitSeconds * time.Second)
	defer deadline.Stop()
	for {
		changed := elbv2GatewayStateSignal()
		state, ok := elbv2GatewayNodeStateFor(nodeName)
		if !ok {
			http.Error(w, "no such Gateway Load Balancer node", http.StatusNotFound)
			return
		}
		if state.Version != held {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(state)
			return
		}
		select {
		case <-changed:
		case <-deadline.C:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(state)
			return
		case <-r.Context().Done():
			return
		}
	}
}

// elbv2GatewayNodeStateFor is a node's state: the targets of the group its
// load balancer's listener forwards to, in the node's zone unless the load
// balancer balances across zones, with their health; the group's flow
// stickiness and failover; and where each endpoint's subnet routes what
// comes back.
func elbv2GatewayNodeStateFor(nodeName string) (gwlb.State, bool) {
	elbv2GatewayNodesMu.Lock()
	var key elbv2GatewayNodeKey
	var node *elbv2GatewayNode
	for k, n := range elbv2GatewayNodes {
		if k.String() == nodeName {
			key, node = k, n
		}
	}
	elbv2GatewayNodesMu.Unlock()
	if node == nil {
		return gwlb.State{}, false
	}
	state := gwlb.State{Stickiness: gwlb.FiveTuple, IdleTimeS: gwlb.DefaultIdleTimeoutSeconds, Targets: []gwlb.Target{}}
	lb, ok := elbv2LoadBalancers.Get(key.lbArn)
	if ok {
		crossZone := strings.EqualFold(elbv2LoadBalancerAttributes(lb.Arn)["load_balancing.cross_zone.enabled"], "true")
		for _, listener := range elbv2Listeners.Filter(func(l ELBv2Listener) bool { return l.LoadBalancerArn == lb.Arn }) {
			for _, tgArn := range elbv2ActionForwardTargets(listener.DefaultActions) {
				tg, ok := elbv2TargetGroups.Get(tgArn)
				if !ok {
					continue
				}
				attrs := tg.Attributes
				if strings.EqualFold(attrs["stickiness.enabled"], "true") && attrs["stickiness.type"] != "" {
					state.Stickiness = attrs["stickiness.type"]
				}
				state.Rebalance = attrs["target_failover.on_unhealthy"] == "rebalance"
				for _, target := range tg.Targets {
					address, zone := elbv2TargetAddressAndZone(tg, target)
					if address == "" || (!crossZone && zone != key.zone) {
						continue
					}
					state.Targets = append(state.Targets, gwlb.Target{Address: address, Healthy: target.DeregisteringAt.IsZero() && elbv2TargetReceivesTraffic(tg, target)})
				}
			}
		}
	}
	for _, e := range node.design.endpoints {
		state.Endpoints = append(state.Endpoints, gwlb.Endpoint{
			ID:         e.id,
			Onward:     elbv2EndpointOnward(e),
			Attachment: elbv2GatewayAttachment(key.lbArn, e.id),
			Sources:    elbv2EndpointSources(e),
		})
	}
	sum := sha256.New()
	_ = json.NewEncoder(sum).Encode(state)
	state.Version = hex.EncodeToString(sum.Sum(nil))[:16]
	return state, true
}

// elbv2EndpointOnward is the next hop the endpoint's subnet sends what the
// appliances return to: the network's own gateway for an internet gateway,
// the address another target resolves to, and nothing when the subnet names
// no way out or routes back into an endpoint.
func elbv2EndpointOnward(e elbv2GatewayNodeEndpoint) string {
	next := ec2SubnetGateway(e.subnet)
	if next == sim.NoGateway || next == e.address.Addr().String() {
		return ""
	}
	if next == "" {
		return e.address.Masked().Addr().Next().String()
	}
	return next
}

// The attachment option names the attachment between the endpoint and the
// load balancer; the simulator derives it from the pair.
func elbv2GatewayAttachment(lbArn, endpointID string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(lbArn + "|" + endpointID))
	return h.Sum64()
}

// elbv2TargetAddressAndZone is where a target receives traffic and the zone
// it is in: an instance's private address and its subnet's zone, or an IP
// target's address and the zone of the load balancer's subnet holding it.
func elbv2TargetAddressAndZone(tg ELBv2TargetGroup, target ELBv2TargetDescription) (string, string) {
	if strings.HasPrefix(target.ID, "i-") {
		inst := ec2InstanceRecord(target.ID)
		if inst.PrivateIpAddress == "" {
			return "", ""
		}
		subnet, _ := ec2Subnets.Get(inst.SubnetId)
		return inst.PrivateIpAddress, subnet.AvailabilityZone
	}
	address, err := netip.ParseAddr(target.ID)
	if err != nil {
		return "", ""
	}
	for _, subnet := range ec2Subnets.Filter(func(s EC2Subnet) bool { return s.VpcId == tg.VpcID }) {
		if cidr, err := netip.ParsePrefix(subnet.CidrBlock); err == nil && cidr.Contains(address) {
			return target.ID, subnet.AvailabilityZone
		}
	}
	return target.ID, ""
}

// elbv2EndpointSources are the IPv4 ranges of the subnets of the endpoint's
// VPC whose route tables send their default route through the endpoint.
func elbv2EndpointSources(e elbv2GatewayNodeEndpoint) []string {
	var sources []string
	for _, subnet := range ec2Subnets.Filter(func(s EC2Subnet) bool { return s.VpcId == e.vpcID }) {
		rt, ok := ec2SubnetRouteTable(subnet.SubnetId)
		if !ok {
			continue
		}
		for _, route := range rt.Routes {
			if route.DestinationCidrBlock == "0.0.0.0/0" && route.VpcEndpointId == e.id {
				sources = append(sources, subnet.CidrBlock)
			}
		}
	}
	slices.Sort(sources)
	return sources
}

// ec2EndpointReadyState is the state an accepted endpoint of a type enters: a
// Gateway Load Balancer endpoint is Pending until a node serves it, every
// other type Available at once. An API-only simulator runs no node, so its
// endpoints have no data plane to wait for.
func ec2EndpointReadyState(endpointType string) string {
	if endpointType == "GatewayLoadBalancer" && sim.DockerClient() != nil {
		return "Pending"
	}
	return "Available"
}

// elbv2GatewayEndpointsServed makes the node's Pending endpoints Available: a
// node asks again only after it has applied the state it held, so its links
// and their rules are in place.
func elbv2GatewayEndpointsServed(nodeName string) {
	elbv2GatewayNodesMu.Lock()
	var endpoints []string
	for key, node := range elbv2GatewayNodes {
		if key.String() == nodeName {
			for _, e := range node.design.endpoints {
				endpoints = append(endpoints, e.id)
			}
		}
	}
	elbv2GatewayNodesMu.Unlock()
	for _, id := range endpoints {
		ep, ok := ec2VpcEndpoints.Get(id)
		if !ok || ep.State != "Pending" {
			continue
		}
		ep.State = "Available"
		ec2VpcEndpoints.Put(id, ep)
		for _, c := range ec2VpcEndpointConns.List() {
			if c.VpcEndpointId == id && c.VpcEndpointState == "Pending" {
				c.VpcEndpointState = "Available"
				ec2VpcEndpointConns.Put(c.VpcEndpointConnectionId, c)
			}
		}
	}
}
