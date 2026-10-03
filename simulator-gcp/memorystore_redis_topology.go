package main

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Topology changes on a running engine. An instance gains or loses replicas
// while its primary keeps serving; a cluster gains shards by meeting new
// primaries and moving an equal share of the slots onto them, and loses
// shards by moving their slots onto the rest and deleting their nodes.

// ResizeInstance runs replicas replica nodes behind the instance's primary.
func (p *msRedisPlane) ResizeInstance(replicas int) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	primary := p.Primary()
	var current []int
	for _, node := range p.nodeOrder() {
		if node != primary {
			current = append(current, node)
		}
	}
	if replicas < len(current) {
		if err := p.removeNodes(current[replicas:]); err != nil {
			return err
		}
	} else {
		added := p.allocateNodes(replicas - len(current))
		if err := p.startNodes(added); err != nil {
			return errors.Join(err, p.removeNodes(added))
		}
		for _, node := range added {
			if err := p.awaitSynchronised(node); err != nil {
				return err
			}
		}
	}
	p.mu.Lock()
	p.replicas = replicas
	p.mu.Unlock()
	want := strconv.Itoa(replicas)
	return p.awaitInfo(primary, "replication", func(info map[string]string) bool {
		return info["connected_slaves"] == want
	})
}

// allocateNodes reserves count new node indices.
func (p *msRedisPlane) allocateNodes(count int) []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	added := make([]int, 0, count)
	for i := 0; i < count; i++ {
		index := p.nextNodeIndexLocked()
		p.nodes[index] = &msRedisNode{index: index}
		added = append(added, index)
	}
	return added
}

// removeNodes stops nodes, stops serving them and deletes their directories,
// so an index taken again starts as a new node.
func (p *msRedisPlane) removeNodes(nodes []int) error {
	var errs []error
	for _, node := range nodes {
		errs = append(errs, p.stopNode(node))
		p.stopServing("node-" + strconv.Itoa(node))
		p.mu.Lock()
		delete(p.nodes, node)
		kept := p.endpoints[:0]
		for _, endpoint := range p.endpoints {
			if endpoint.Role != msRedisNodeEndpoint || endpoint.Node != node {
				kept = append(kept, endpoint)
			}
		}
		p.endpoints = kept
		p.mu.Unlock()
	}
	if remaining := p.nodeOrder(); len(remaining) > 0 && len(nodes) > 0 {
		command := []string{"rm", "-rf"}
		for _, node := range nodes {
			command = append(command, msRedisNodeDir(node))
		}
		if _, err := p.exec(remaining[0], command, nil); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// addClusterNodes starts count new cluster nodes, each served at an endpoint
// of its own and announcing it, and meets them into the cluster. Nodes that
// will take slots wait until every node holds the whole slot map as well.
func (p *msRedisPlane) addClusterNodes(count int, primaries bool) ([]int, error) {
	added := p.allocateNodes(count)
	for _, node := range added {
		key := "node-" + strconv.Itoa(node)
		listener, ip, err := msRedisListen(p.name+"#"+key, msRedisPort)
		if err != nil {
			return nil, errors.Join(err, p.removeNodes(added))
		}
		p.mu.Lock()
		p.nodes[node].announce = ip
		p.endpoints = append(p.endpoints, msRedisEndpointRecord{Role: msRedisNodeEndpoint, Address: ip, Port: msRedisPort, Node: node})
		p.mu.Unlock()
		p.serve(key, listener, fixedTarget(node))
	}
	if err := p.startNodes(added); err != nil {
		return nil, errors.Join(err, p.removeNodes(added))
	}
	for _, node := range p.nodeOrder() {
		if err := p.meet(node, added); err != nil {
			return nil, err
		}
	}
	if err := p.awaitKnown(primaries); err != nil {
		return nil, err
	}
	if err := p.applyUsers(added); err != nil {
		return nil, err
	}
	return added, nil
}

// awaitKnown waits until every node knows every other one and, with slots,
// holds the whole slot map, which is when the cluster manager finds the nodes
// agreeing about the configuration.
func (p *msRedisPlane) awaitKnown(slotMap bool) error {
	order := p.nodeOrder()
	want := strconv.Itoa(len(order))
	slots := strconv.Itoa(msRedisClusterSlots)
	for _, node := range order {
		if err := p.awaitClusterInfo(node, func(info map[string]string) bool {
			return info["cluster_known_nodes"] == want &&
				(!slotMap || info["cluster_slots_assigned"] == slots && info["cluster_state"] == "ok")
		}); err != nil {
			return fmt.Errorf("node %d did not learn of every node: %w", node, err)
		}
	}
	return nil
}

func (p *msRedisPlane) awaitClusterInfo(node int, satisfied func(map[string]string) bool) error {
	deadline := time.Now().Add(msRedisCommandDeadline)
	for {
		reply, err := p.command(node, "CLUSTER", "INFO")
		if err != nil {
			return err
		}
		if satisfied(msRedisParseInfo(fmt.Sprint(reply))) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("CLUSTER INFO did not settle within %s", msRedisCommandDeadline)
		}
		time.Sleep(msRedisProbeInterval)
	}
}

// clusterManager runs one redis-cli --cluster subcommand from via's container
// against via at its network address, authenticating as the engine's default
// user. The cluster manager takes the address it was given as via's own and
// has other nodes migrate keys to it, so a loopback address would send them
// to themselves.
func (p *msRedisPlane) clusterManager(via int, subcommand string, args ...string) error {
	ip, err := p.nodeIP(via)
	if err != nil {
		return err
	}
	command := []string{"redis-cli", "--cluster", subcommand, net.JoinHostPort(ip, strconv.Itoa(msRedisPort))}
	command = append(append(command, args...), "--cluster-yes")
	var env []string
	if password := p.currentPassword(); password != "" {
		env = append(env, "REDISCLI_AUTH="+password)
	}
	if _, err := p.exec(via, command, env); err != nil {
		return fmt.Errorf("redis-cli --cluster %s: %w", subcommand, err)
	}
	return nil
}

// ResizeCluster reshapes a running cluster to shards shards of replicas
// replicas each.
func (p *msRedisPlane) ResizeCluster(shards, replicas int) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	primaries, followers, err := p.shardMap()
	if err != nil {
		return err
	}
	ids := map[int]string{}
	for _, node := range p.nodeOrder() {
		if ids[node], err = p.nodeID(node); err != nil {
			return err
		}
	}

	if shards < len(primaries) {
		byIndex := append([]int(nil), primaries...)
		sort.Sort(sort.Reverse(sort.IntSlice(byIndex)))
		removed := byIndex[:len(primaries)-shards]
		kept := map[int]bool{}
		for _, node := range primaries {
			kept[node] = true
		}
		for _, node := range removed {
			delete(kept, node)
		}
		var keep []int
		for _, node := range primaries {
			if kept[node] {
				keep = append(keep, node)
			}
		}
		via := byIndex[len(byIndex)-1]
		if err := p.balanceSlots(keep, removed, ids); err != nil {
			return err
		}
		if err := p.awaitDrained(via, removed, ids); err != nil {
			return err
		}
		for _, primary := range removed {
			for _, node := range append(append([]int(nil), followers[primary]...), primary) {
				if err := p.clusterManager(via, "del-node", ids[node]); err != nil {
					return err
				}
				if err := p.removeNodes([]int{node}); err != nil {
					return err
				}
			}
		}
		primaries = primaries[:0]
		for _, node := range byIndex {
			if kept[node] {
				primaries = append(primaries, node)
			}
		}
		sort.Ints(primaries)
	}

	want := map[string]int{}
	for _, primary := range primaries {
		have := followers[primary]
		if replicas < len(have) {
			for _, node := range have[replicas:] {
				if err := p.clusterManager(primary, "del-node", ids[node]); err != nil {
					return err
				}
				if err := p.removeNodes([]int{node}); err != nil {
					return err
				}
			}
			continue
		}
		want[ids[primary]] = replicas - len(have)
	}

	if grow := shards - len(primaries); grow > 0 {
		added, err := p.addClusterNodes(grow, true)
		if err != nil {
			return err
		}
		for _, node := range added {
			if ids[node], err = p.nodeID(node); err != nil {
				return err
			}
			want[ids[node]] = replicas
		}
		if err := p.balanceSlots(append(append([]int(nil), primaries...), added...), nil, ids); err != nil {
			return err
		}
	}
	if err := p.addReplicas(want); err != nil {
		return err
	}

	p.mu.Lock()
	p.shards, p.replicas = shards, replicas
	p.mu.Unlock()
	return p.awaitClusterState(shards, replicas)
}

// addReplicas starts, for each primary ID, that many nodes following it. The
// replicas start together, so their initial synchronisations overlap.
func (p *msRedisPlane) addReplicas(want map[string]int) error {
	var primaries []string
	total := 0
	for id, count := range want {
		for i := 0; i < count; i++ {
			primaries = append(primaries, id)
		}
		total += count
	}
	if total == 0 {
		return nil
	}
	sort.Strings(primaries)
	added, err := p.addClusterNodes(total, false)
	if err != nil {
		return err
	}
	for i, node := range added {
		if err := p.follow(node, primaries[i]); err != nil {
			return err
		}
	}
	return nil
}

// awaitDrained waits until via sees no slot on the primaries a shrink
// removes, which the cluster manager's del-node requires of each.
func (p *msRedisPlane) awaitDrained(via int, removed []int, ids map[int]string) error {
	deadline := time.Now().Add(msRedisCommandDeadline)
	for {
		view, err := p.clusterView(via)
		if err != nil {
			return err
		}
		var owning []string
		for _, node := range removed {
			if entry, ok := view.byID(ids[node]); ok && len(entry.Slots) > 0 {
				owning = append(owning, fmt.Sprintf("node %d (%s)", node, strings.Join(entry.Slots, " ")))
			}
		}
		if len(owning) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s still own slots after the rebalance", strings.Join(owning, ", "))
		}
		time.Sleep(msRedisProbeInterval)
	}
}

// SetReadEndpoint starts or stops serving an instance's read endpoint and
// returns its address.
func (p *msRedisPlane) SetReadEndpoint(enabled bool, port int) (string, error) {
	p.mu.RLock()
	var current string
	for _, endpoint := range p.endpoints {
		if endpoint.Role == msRedisReadEndpoint {
			current = endpoint.Address
		}
	}
	p.mu.RUnlock()
	if !enabled {
		p.stopServing(string(msRedisReadEndpoint))
		p.mu.Lock()
		kept := p.endpoints[:0]
		for _, endpoint := range p.endpoints {
			if endpoint.Role != msRedisReadEndpoint {
				kept = append(kept, endpoint)
			}
		}
		p.endpoints = kept
		p.mu.Unlock()
		return "", nil
	}
	if current != "" {
		return current, nil
	}
	listener, ip, err := msRedisListen(p.name+"#read", port)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	p.endpoints = append(p.endpoints, msRedisEndpointRecord{Role: msRedisReadEndpoint, Address: ip, Port: port})
	p.mu.Unlock()
	p.serve(string(msRedisReadEndpoint), listener, p.readTarget)
	return ip, nil
}
