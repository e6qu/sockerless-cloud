package cacheengine

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// Topology changes on a running engine. An instance gains or loses replicas
// while its primary keeps serving; a cluster gains shards by meeting new
// primaries and moving an equal share of the slots onto them, and loses
// shards by moving their slots onto the rest and deleting their nodes.

// ResizeInstance runs replicas replica nodes behind the instance's primary.
func (p *Plane) ResizeInstance(replicas int) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	primary := p.Primary()
	current := p.Replicas()
	if replicas < len(current) {
		if err := p.removeNodes(current[replicas:]); err != nil {
			return err
		}
	} else {
		added := p.allocateNodes(replicas - len(current))
		if err := p.startNodes(added); err != nil {
			return errors.Join(err, p.removeNodes(added))
		}
		for _, index := range added {
			if err := p.awaitSynchronised(index); err != nil {
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
func (p *Plane) allocateNodes(count int) []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	added := make([]int, 0, count)
	for i := 0; i < count; i++ {
		index := p.nextNodeIndexLocked()
		p.nodes[index] = &node{index: index}
		added = append(added, index)
	}
	return added
}

// removeNodes stops nodes, stops serving them and deletes their directories,
// so an index taken again starts as a new node.
func (p *Plane) removeNodes(nodes []int) error {
	var errs []error
	for _, index := range nodes {
		errs = append(errs, p.stopNode(index))
		p.stopServing(nodeEndpointKey(index))
		p.mu.Lock()
		delete(p.nodes, index)
		kept := p.endpoints[:0]
		var released []Endpoint
		for _, endpoint := range p.endpoints {
			if endpoint.Role == NodeEndpoint && endpoint.Node == index {
				released = append(released, endpoint)
				continue
			}
			kept = append(kept, endpoint)
		}
		p.endpoints = kept
		p.mu.Unlock()
		for _, endpoint := range released {
			dbengine.ReleaseLoopback(Identifier(p.cfg.Owner, endpoint))
		}
	}
	if remaining := p.Nodes(); len(remaining) > 0 && len(nodes) > 0 {
		command := []string{"rm", "-rf"}
		for _, index := range nodes {
			command = append(command, nodeDir(index))
		}
		if _, err := p.Exec(remaining[0], command, nil); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ExposeNodes serves every node that has no endpoint of its own at one on
// port, as an instance whose replicas each answer at an address of their own
// does once a resize adds one.
func (p *Plane) ExposeNodes(port int) error {
	p.mu.RLock()
	exposed := map[int]bool{}
	for _, endpoint := range p.endpoints {
		if endpoint.Role == NodeEndpoint {
			exposed[endpoint.Node] = true
		}
	}
	var missing []int
	for _, index := range p.nodeOrderLocked() {
		if !exposed[index] {
			missing = append(missing, index)
		}
	}
	p.mu.RUnlock()
	for _, index := range missing {
		endpoint := Endpoint{Role: NodeEndpoint, Port: port, Node: index}
		listener, ip, err := Listen(Identifier(p.cfg.Owner, endpoint), port)
		if err != nil {
			return err
		}
		endpoint.Address = ip
		p.AddEndpoints(endpoint)
		p.serve(endpoint, listener, fixedTarget(index))
	}
	return nil
}

// addClusterNodes starts count new cluster nodes, each served at an endpoint
// of its own and announcing it, and meets them into the cluster. Nodes that
// will take slots wait until every node holds the whole slot map as well.
func (p *Plane) addClusterNodes(count int, primaries bool) ([]int, error) {
	added := p.allocateNodes(count)
	for _, index := range added {
		endpoint := Endpoint{Role: NodeEndpoint, Port: Port, Node: index}
		listener, ip, err := Listen(Identifier(p.cfg.Owner, endpoint), Port)
		if err != nil {
			return nil, errors.Join(err, p.removeNodes(added))
		}
		endpoint.Address = ip
		p.mu.Lock()
		p.nodes[index].announce = ip
		p.endpoints = append(p.endpoints, endpoint)
		p.mu.Unlock()
		p.serve(endpoint, listener, fixedTarget(index))
	}
	if err := p.startNodes(added); err != nil {
		return nil, errors.Join(err, p.removeNodes(added))
	}
	for _, index := range p.Nodes() {
		if err := p.meet(index, added); err != nil {
			return nil, err
		}
	}
	if err := p.awaitKnown(primaries); err != nil {
		return nil, err
	}
	if err := p.ApplyUsers(added); err != nil {
		return nil, err
	}
	return added, nil
}

// awaitKnown waits until every node knows every other one and, with slots,
// holds the whole slot map, which is when the cluster manager finds the nodes
// agreeing about the configuration.
func (p *Plane) awaitKnown(slotMap bool) error {
	order := p.Nodes()
	want := strconv.Itoa(len(order))
	slots := strconv.Itoa(ClusterSlots)
	for _, index := range order {
		if err := p.awaitClusterInfo(index, func(info map[string]string) bool {
			return info["cluster_known_nodes"] == want &&
				(!slotMap || info["cluster_slots_assigned"] == slots && info["cluster_state"] == "ok")
		}); err != nil {
			return fmt.Errorf("node %d did not learn of every node: %w", index, err)
		}
	}
	return nil
}

func (p *Plane) awaitClusterInfo(index int, satisfied func(map[string]string) bool) error {
	err := p.awaitEngine(index, true, func() (bool, error) {
		reply, err := p.Command(index, "CLUSTER", "INFO")
		if err != nil {
			return false, err
		}
		return satisfied(ParseInfo(fmt.Sprint(reply))), nil
	})
	if err != nil {
		return fmt.Errorf("CLUSTER INFO: %w", err)
	}
	return nil
}

// clusterManager runs one redis-cli --cluster subcommand from via's container
// against via at its network address, authenticating as the engine's default
// user. The cluster manager takes the address it was given as via's own and
// has other nodes migrate keys to it, so a loopback address would send them
// to themselves.
func (p *Plane) clusterManager(via int, subcommand string, args ...string) error {
	ip, err := p.NodeIP(via)
	if err != nil {
		return err
	}
	command := []string{"redis-cli", "--cluster", subcommand, net.JoinHostPort(ip, strconv.Itoa(Port))}
	command = append(append(command, args...), "--cluster-yes")
	var env []string
	if password := p.Password(); password != "" {
		env = append(env, "REDISCLI_AUTH="+password)
	}
	if _, err := p.Exec(via, command, env); err != nil {
		return fmt.Errorf("redis-cli --cluster %s: %w", subcommand, err)
	}
	return nil
}

// ResizeCluster reshapes a running cluster to shards shards of replicas
// replicas each.
func (p *Plane) ResizeCluster(shards, replicas int) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	primaries, followers, err := p.ShardMap()
	if err != nil {
		return err
	}
	ids := map[int]string{}
	for _, index := range p.Nodes() {
		if ids[index], err = p.nodeID(index); err != nil {
			return err
		}
	}

	if shards < len(primaries) {
		byIndex := append([]int(nil), primaries...)
		sort.Sort(sort.Reverse(sort.IntSlice(byIndex)))
		removed := byIndex[:len(primaries)-shards]
		kept := map[int]bool{}
		for _, index := range primaries {
			kept[index] = true
		}
		for _, index := range removed {
			delete(kept, index)
		}
		var keep []int
		for _, index := range primaries {
			if kept[index] {
				keep = append(keep, index)
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
			for _, index := range append(append([]int(nil), followers[primary]...), primary) {
				if err := p.clusterManager(via, "del-node", ids[index]); err != nil {
					return err
				}
				if err := p.removeNodes([]int{index}); err != nil {
					return err
				}
			}
		}
		primaries = primaries[:0]
		for _, index := range byIndex {
			if kept[index] {
				primaries = append(primaries, index)
			}
		}
		sort.Ints(primaries)
	}

	want := map[string]int{}
	for _, primary := range primaries {
		have := followers[primary]
		if replicas < len(have) {
			for _, index := range have[replicas:] {
				if err := p.clusterManager(primary, "del-node", ids[index]); err != nil {
					return err
				}
				if err := p.removeNodes([]int{index}); err != nil {
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
		for _, index := range added {
			if ids[index], err = p.nodeID(index); err != nil {
				return err
			}
			want[ids[index]] = replicas
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
func (p *Plane) addReplicas(want map[string]int) error {
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
	for i, index := range added {
		if err := p.follow(index, primaries[i]); err != nil {
			return err
		}
	}
	return nil
}

// awaitDrained waits until via sees no slot on the primaries a shrink
// removes, which the cluster manager's del-node requires of each.
func (p *Plane) awaitDrained(via int, removed []int, ids map[int]string) error {
	var owning []string
	err := p.awaitEngine(via, true, func() (bool, error) {
		view, err := p.clusterView(via)
		if err != nil {
			return false, err
		}
		owning = owning[:0]
		for _, index := range removed {
			if entry, ok := view.byID(ids[index]); ok && len(entry.Slots) > 0 {
				owning = append(owning, fmt.Sprintf("node %d (%s)", index, strings.Join(entry.Slots, " ")))
			}
		}
		return len(owning) == 0, nil
	})
	if err != nil {
		return fmt.Errorf("%s still own slots after the rebalance: %w", strings.Join(owning, ", "), err)
	}
	return nil
}
