package cacheengine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ClusterNode is one line of a CLUSTER NODES reply.
type ClusterNode struct {
	ID        string
	IP        string
	Hostname  string
	Flags     []string
	PrimaryID string
	Connected bool
	Slots     []string
}

func (n ClusterNode) has(flag string) bool {
	for _, f := range n.Flags {
		if f == flag {
			return true
		}
	}
	return false
}

func (n ClusterNode) Primary() bool { return n.has("master") }

// ClusterView is a node's account of the cluster.
type ClusterView []ClusterNode

func ParseClusterNodes(text string) ClusterView {
	var view ClusterView
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		address, _, _ := strings.Cut(fields[1], "@")
		ip, _, _ := strings.Cut(address, ":")
		hostname := ""
		if _, aux, found := strings.Cut(fields[1], ","); found {
			hostname, _, _ = strings.Cut(aux, ",")
		}
		node := ClusterNode{
			ID:        fields[0],
			IP:        ip,
			Hostname:  hostname,
			Flags:     strings.Split(fields[2], ","),
			Connected: fields[7] == "connected",
			Slots:     fields[8:],
		}
		if fields[3] != "-" {
			node.PrimaryID = fields[3]
		}
		view = append(view, node)
	}
	return view
}

func (v ClusterView) byID(id string) (ClusterNode, bool) {
	for _, node := range v {
		if node.ID == id {
			return node, true
		}
	}
	return ClusterNode{}, false
}

func (v ClusterView) self() (ClusterNode, bool) {
	for _, node := range v {
		if node.has("myself") {
			return node, true
		}
	}
	return ClusterNode{}, false
}

// hostnamesKnown reports whether the node has learned every node's announced
// hostname. Until gossip carries it, the node answers CLUSTER SLOTS and
// CLUSTER SHARDS with "?" as that node's endpoint, which no client can dial.
func (v ClusterView) hostnamesKnown() bool {
	for _, node := range v {
		if node.Hostname == "" {
			return false
		}
	}
	return true
}

// countRoles counts the connected primaries and replicas.
func (v ClusterView) countRoles() (primaries, replicas int) {
	for _, node := range v {
		if !node.Connected || node.has("fail") || node.has("handshake") {
			continue
		}
		switch {
		case node.has("master"):
			primaries++
		case node.has("slave"):
			replicas++
		}
	}
	return primaries, replicas
}

func (p *Plane) clusterView(node int) (ClusterView, error) {
	reply, err := p.Command(node, "CLUSTER", "NODES")
	if err != nil {
		return nil, err
	}
	return ParseClusterNodes(fmt.Sprint(reply)), nil
}

// ShardMap is the cluster's shards as node indices: each primary, ordered by
// the first slot it owns, and the replicas following each.
func (p *Plane) ShardMap() (primaries []int, followers map[int][]int, err error) {
	order := p.Nodes()
	byID := map[string]int{}
	for _, node := range order {
		id, err := p.nodeID(node)
		if err != nil {
			return nil, nil, err
		}
		byID[id] = node
	}
	view, err := p.clusterView(order[0])
	if err != nil {
		return nil, nil, err
	}
	followers = map[int][]int{}
	firstSlot := map[int]int{}
	for _, entry := range view {
		node, ours := byID[entry.ID]
		if !ours || entry.has("fail") {
			continue
		}
		if entry.Primary() {
			primaries = append(primaries, node)
			firstSlot[node] = ClusterSlots
			for _, slots := range entry.Slots {
				first, _, _ := strings.Cut(slots, "-")
				if value, err := strconv.Atoi(first); err == nil && value < firstSlot[node] {
					firstSlot[node] = value
				}
			}
			continue
		}
		if primary, ok := byID[entry.PrimaryID]; ok {
			followers[primary] = append(followers[primary], node)
		}
	}
	sort.Slice(primaries, func(i, j int) bool {
		if firstSlot[primaries[i]] != firstSlot[primaries[j]] {
			return firstSlot[primaries[i]] < firstSlot[primaries[j]]
		}
		return primaries[i] < primaries[j]
	})
	for primary := range followers {
		sort.Ints(followers[primary])
	}
	return primaries, followers, nil
}

// ShardSlots is the slot ranges each primary of the cluster owns, as CLUSTER
// NODES spells them, keyed by node index.
func (p *Plane) ShardSlots() (map[int][]string, error) {
	order := p.Nodes()
	byID := map[string]int{}
	for _, node := range order {
		id, err := p.nodeID(node)
		if err != nil {
			return nil, err
		}
		byID[id] = node
	}
	view, err := p.clusterView(order[0])
	if err != nil {
		return nil, err
	}
	slots := map[int][]string{}
	for _, entry := range view {
		if node, ours := byID[entry.ID]; ours && entry.Primary() {
			for _, field := range entry.Slots {
				if !strings.HasPrefix(field, "[") {
					slots[node] = append(slots[node], field)
				}
			}
		}
	}
	return slots, nil
}

func (p *Plane) nodeID(node int) (string, error) {
	reply, err := p.Command(node, "CLUSTER", "MYID")
	if err != nil {
		return "", err
	}
	return fmt.Sprint(reply), nil
}
