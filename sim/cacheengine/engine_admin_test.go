package cacheengine

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// A plane with an admin user replicates and administers as that user, while
// clients reach only the users the plane applies: here a default user that
// requires a password and only reads.
func TestAdminUserLeavesTheDefaultUserToUsers(t *testing.T) {
	cfg := engineTestConfig(t, t.Name())
	cfg.Image = redisImage
	cfg.Password = "admin-secret-of-the-plane"
	cfg.AdminUser = "plane-admin"
	cfg.Users = func() ([]User, error) {
		return []User{{Name: "default", Passwords: []string{"default-password"}, Rule: []string{"~*", "-@all", "+@read"}}}, nil
	}
	listeners, endpoints, err := Bind(cfg.Owner, []Endpoint{{Role: PrimaryEndpoint, Port: 6381}, {Role: ReadEndpoint, Port: 6381}})
	if err != nil {
		t.Fatal(err)
	}
	plane := New(cfg, Record{Endpoints: endpoints, Nodes: []int{0, 1}, Replicas: 1})
	t.Cleanup(plane.Remove)
	plane.ServeEndpoints(listeners)
	if err := plane.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := plane.Command(plane.Primary(), "SET", "written", "by-the-plane"); err != nil {
		t.Fatal(err)
	}
	if _, err := plane.Command(plane.Primary(), "WAIT", "1", "0"); err != nil {
		t.Fatal(err)
	}
	address := endpointAddress(t, plane.Endpoints(), ReadEndpoint)
	anonymous, err := Dial(address, "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer anonymous.Close()
	if _, err := anonymous.Do("GET", "written"); err == nil || !strings.Contains(err.Error(), "NOAUTH") {
		t.Fatalf("a client without a password read the dataset: %v", err)
	}
	reader, err := Dial(address, "default-password", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if value := mustDo(t, reader, "GET", "written"); value != "by-the-plane" {
		t.Fatalf("the replica holds %v", value)
	}
	if _, err := reader.Do("SET", "written", "by-a-client"); err == nil || !strings.Contains(err.Error(), "NOPERM") {
		t.Fatalf("the read-only default user wrote: %v", err)
	}
	if err := plane.Reboot(plane.Replicas()); err != nil {
		t.Fatal(err)
	}
}

// The Answer hook answers the commands it takes in turn with a node's replies
// on a pipelined connection, and an upgrade restarts every node, empty, on
// the image it names.
func TestMemcachedAnswersInTurnAndUpgrades(t *testing.T) {
	base := engineTestConfig(t, t.Name())
	cfg := MemcachedConfig{
		Name: base.Name, Owner: base.Owner, Image: "mirror.gcr.io/library/memcached:1.6.17-alpine", Platform: base.Platform,
		Labels: base.Labels, NodeLabel: base.NodeLabel, Backend: base.Backend, Network: base.Network,
		Answer: func(command []string) ([]byte, bool) {
			if len(command) == 3 && command[0] == "config" && command[1] == "get" && command[2] == "cluster" {
				return []byte("CONFIG cluster 0 3\r\n1\n\r\nEND\r\n"), true
			}
			return nil, false
		},
	}
	listeners, endpoints, err := Bind(cfg.Owner, []Endpoint{{Role: DiscoveryEndpoint, Port: MemcachedPort}, {Role: NodeEndpoint, Port: MemcachedPort, Node: 0}})
	if err != nil {
		t.Fatal(err)
	}
	plane := NewMemcached(cfg, Record{Endpoints: endpoints, Nodes: []int{0}})
	t.Cleanup(plane.Remove)
	plane.ServeEndpoints(listeners)
	if err := plane.Ensure(); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", endpointAddress(t, plane.Endpoints(), DiscoveryEndpoint), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	request := "set config 0 0 18\r\nconfig get cluster\r\nconfig get cluster\r\nget config\r\nmn\r\nversion\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	for _, want := range []string{
		"STORED\r\n", "CONFIG cluster 0 3\r\n", "1\n", "\r\n", "END\r\n",
		"VALUE config 0 18\r\n", "config get cluster\r\n", "END\r\n", "MN\r\n",
	} {
		got, err := reader.ReadString('\n')
		if err != nil || got != want {
			t.Fatalf("read %q (%v), want %q", got, err, want)
		}
	}
	if version, _ := reader.ReadString('\n'); !strings.HasPrefix(version, "VERSION 1.6.17") {
		t.Fatalf("version = %q", version)
	}
	node := endpointAddress(t, plane.Endpoints(), NodeEndpoint)
	if reply := memcachedDo(t, node, "set kept 0 0 1\r\nx\r\n"); reply != "STORED" {
		t.Fatalf("set = %q", reply)
	}
	if err := plane.Upgrade(memcachedImage); err != nil {
		t.Fatal(err)
	}
	if reply := memcachedDo(t, node, "version\r\n"); !strings.HasPrefix(reply, "VERSION 1.6.22") {
		t.Fatalf("an upgraded node runs %q", reply)
	}
	if reply := memcachedDo(t, node, "get kept\r\n"); reply != "END" {
		t.Fatalf("an upgraded node kept %q", reply)
	}
}

// A cluster administered as an admin user upgrades to another image with
// every shard keeping its keys.
func TestClusterUpgradeKeepsEveryShardsKeys(t *testing.T) {
	cfg := engineTestConfig(t, t.Name())
	cfg.Image = "mirror.gcr.io/library/redis:7.0-alpine"
	cfg.Cluster = true
	cfg.Password = "cluster-admin-secret"
	cfg.AdminUser = "plane-admin"
	cfg.Users = func() ([]User, error) {
		return []User{{Name: "default", Rule: []string{"on", "nopass", "~*", "&*", "+@all"}}}, nil
	}
	listeners, endpoints, err := Bind(cfg.Owner, []Endpoint{
		{Role: DiscoveryEndpoint}, {Role: NodeEndpoint, Node: 0}, {Role: NodeEndpoint, Node: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	cluster := New(cfg, Record{Endpoints: endpoints, Nodes: []int{0, 1}, Shards: 2})
	t.Cleanup(cluster.Remove)
	cluster.ServeEndpoints(listeners)
	if err := cluster.Ensure(); err != nil {
		t.Fatal(err)
	}
	owners, err := cluster.slotOwners()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("key-%d", i)
		if _, err := cluster.Command(owners[KeySlot(key)], "SET", key, "kept"); err != nil {
			t.Fatal(err)
		}
	}
	if err := cluster.Upgrade(redisImage); err != nil {
		t.Fatal(err)
	}
	for _, index := range cluster.Nodes() {
		info, err := cluster.Command(index, "INFO", "server")
		if version := ParseInfo(fmt.Sprint(info))["redis_version"]; err != nil || !strings.HasPrefix(version, "7.2.") {
			t.Fatalf("node %d runs %q (%v)", index, version, err)
		}
	}
	owners, err = cluster.slotOwners()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("key-%d", i)
		if value, err := cluster.Command(owners[KeySlot(key)], "GET", key); err != nil || value != "kept" {
			t.Fatalf("%s = %v, %v after the upgrade", key, value, err)
		}
	}
}
