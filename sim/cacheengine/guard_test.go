package cacheengine

import (
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

func startCluster(t *testing.T, name string, shards int) *Plane {
	t.Helper()
	cfg := engineTestConfig(t, name)
	cfg.Image = redisImage
	cfg.Cluster = true
	wanted := []Endpoint{{Role: DiscoveryEndpoint}}
	nodes := make([]int, shards)
	for node := range shards {
		nodes[node] = node
		wanted = append(wanted, Endpoint{Role: NodeEndpoint, Node: node})
	}
	listeners, endpoints, err := Bind(cfg.Owner, wanted)
	if err != nil {
		t.Fatal(err)
	}
	plane := New(cfg, Record{Endpoints: endpoints, Nodes: nodes, Shards: shards})
	t.Cleanup(plane.Remove)
	plane.ServeEndpoints(listeners)
	if err := plane.Ensure(); err != nil {
		t.Fatal(err)
	}
	return plane
}

func nodeEndpointAddress(t *testing.T, plane *Plane, node int) string {
	t.Helper()
	for _, endpoint := range plane.Endpoints() {
		if endpoint.Role == NodeEndpoint && endpoint.Node == node {
			return net.JoinHostPort(endpoint.Address, fmt.Sprint(endpoint.PortOrDefault()))
		}
	}
	t.Fatalf("no endpoint for node %d", node)
	return ""
}

func dbSize(t *testing.T, plane *Plane, node int) int64 {
	t.Helper()
	reply, err := plane.Command(node, "DBSIZE")
	if err != nil {
		t.Fatal(err)
	}
	size, _ := reply.(int64)
	return size
}

// A plane that refuses writes answers each write as a read-only replica does
// and discards a transaction that held one; a diverted plane sends every
// command to the source node that owns its key, answers the topology as its
// own, and hands its clients back when the diversion ends.
func TestGuardRefusesWritesAndDivertsToASource(t *testing.T) {
	source := startCluster(t, "guard-source", 2)
	target := startCluster(t, "guard-target", 1)
	endpoint := nodeEndpointAddress(t, target, 0)

	open, err := Dial(endpoint, "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	mustDo(t, open, "PING")
	target.RefuseWrites(true)
	if _, err := open.Do("SET", "anything", "v"); err == nil {
		t.Fatal("a connection opened before writes were refused still writes")
	}
	refused, err := Dial(endpoint, "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refused.Do("GET", "anything"); err != nil {
		t.Fatalf("GET while writes are refused: %v", err)
	}
	if _, err := refused.Do("SET", "anything", "v"); err == nil || !strings.HasPrefix(err.Error(), "READONLY") {
		t.Fatalf("SET while writes are refused = %v, want READONLY", err)
	}
	mustDo(t, refused, "MULTI")
	if _, err := refused.Do("SET", "anything", "v"); err == nil || !strings.HasPrefix(err.Error(), "READONLY") {
		t.Fatalf("SET in a transaction while writes are refused = %v, want READONLY", err)
	}
	if _, err := refused.Do("EXEC"); err == nil || !strings.HasPrefix(err.Error(), "EXECABORT") {
		t.Fatalf("EXEC after a refused write = %v, want EXECABORT", err)
	}
	_ = refused.Close()
	target.RefuseWrites(false)
	if dbSize(t, target, 0) != 0 {
		t.Fatal("a refused write reached the engine")
	}

	target.Divert(&Diversion{
		Seed: nodeEndpointAddress(t, source, 0),
		Dial: func(address string) (net.Conn, error) { return net.DialTimeout("tcp", address, 5*time.Second) },
	})
	diverted, err := Dial(endpoint, "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		mustDo(t, diverted, "SET", fmt.Sprintf("diverted-%d", i), "v")
	}
	executed, _ := mustDo(t, diverted, "EVAL", "return 1", "0").(int64)
	if executed != 1 {
		t.Fatalf("EVAL through the diversion = %d", executed)
	}
	tx, err := diverted.Pipeline([][]string{{"MULTI"}, {"INCR", "counter{x}"}, {"INCR", "counter{x}"}, {"EXEC"}})
	if err != nil {
		t.Fatal(err)
	}
	if results, _ := tx[3].([]any); len(results) != 2 || fmt.Sprint(results[1]) != "2" {
		t.Fatalf("a transaction through the diversion answered %v", tx)
	}
	if got := dbSize(t, source, 0) + dbSize(t, source, 1); got != 21 {
		t.Fatalf("the source holds %d keys, want 21", got)
	}
	if dbSize(t, target, 0) != 0 {
		t.Fatal("a diverted command reached the plane")
	}
	shards, _ := mustDo(t, diverted, "CLUSTER", "SLOTS").([]any)
	if len(shards) != 1 {
		t.Fatalf("CLUSTER SLOTS through the diversion = %v, want the plane's one shard", shards)
	}
	owner, _ := shards[0].([]any)[2].([]any)
	if at := net.JoinHostPort(fmt.Sprint(owner[0]), fmt.Sprint(owner[1])); at != endpoint {
		t.Fatalf("CLUSTER SLOTS names %s, not the endpoint %s", at, endpoint)
	}

	target.Divert(nil)
	if _, err := diverted.Do("PING"); err == nil {
		t.Fatal("a diverted connection outlived its diversion")
	}
	served, err := Dial(endpoint, "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer served.Close()
	mustDo(t, served, "SET", "served", "v")
	if dbSize(t, target, 0) != 1 {
		t.Fatal("the plane does not serve its endpoint once the diversion ends")
	}
}

// AssignSlots hands each primary the slots it is given, keys and all.
func TestAssignSlotsMovesSlotsToTheirNewOwner(t *testing.T) {
	plane := startCluster(t, "assign-slots", 2)
	before, err := plane.ShardSlots()
	if err != nil {
		t.Fatal(err)
	}
	owned := map[int][]int{}
	for node, fields := range before {
		if owned[node], err = SlotList(fields); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := plane.Dial(0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	moving := owned[0][:100]
	key := ""
	for i := 0; key == ""; i++ {
		if candidate := fmt.Sprintf("moving-%d", i); slices.Contains(moving, KeySlot(candidate)) {
			key = candidate
		}
	}
	mustDo(t, conn, "SET", key, "carried")
	want := map[int][]int{0: owned[0][100:], 1: append(slices.Clone(owned[1]), moving...)}
	if err := plane.AssignSlots(want); err != nil {
		t.Fatal(err)
	}
	after, err := plane.ShardSlots()
	if err != nil {
		t.Fatal(err)
	}
	for node, slots := range want {
		got, err := SlotList(after[node])
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		expected := slices.Sorted(slices.Values(slots))
		if !slices.Equal(got, expected) {
			t.Fatalf("node %d owns %d slots, want %d", node, len(got), len(expected))
		}
	}
	if dbSize(t, plane, 1) != 1 {
		t.Fatal("the key did not move with its slot")
	}
}
