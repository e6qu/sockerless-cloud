package cacheengine

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

const (
	redisImage     = "mirror.gcr.io/library/redis:7.2-alpine"
	valkeyImage    = "mirror.gcr.io/valkey/valkey:8.0-alpine"
	memcachedImage = "mirror.gcr.io/library/memcached:1.6.22-alpine"
)

// engineTestConfig is a plane on containers on an engine network of the
// test's own.
func engineTestConfig(t *testing.T, name string) Config {
	t.Helper()
	if _, err := sim.InitDocker("cacheengine", true, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(sim.WorkloadScope() + "\x00" + name))
	scoped := "sockerless-cacheengine-test-" + hex.EncodeToString(digest[:6])
	network := "sockerless-cacheengine-test-" + hex.EncodeToString(digest[6:10])
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = sim.DockerClient().NetworkRemove(ctx, network, client.NetworkRemoveOptions{})
	})
	return Config{
		Name:      "cache " + name,
		LogTag:    "cacheengine",
		Owner:     scoped,
		Platform:  "linux/amd64",
		Volume:    scoped,
		Labels:    map[string]string{"sockerless-cacheengine-test": scoped},
		NodeLabel: "sockerless-cacheengine-test-node",
		Backend:   func() (placement.Backend, error) { return placement.ContainerBackend{}, nil },
		Network: func(ctx context.Context) (string, []string, error) {
			_, _, err := sim.EnsurePlatformNetwork(ctx, network, nil)
			return network, nil, err
		},
	}
}

func endpointAddress(t *testing.T, endpoints []Endpoint, role Role) string {
	t.Helper()
	for _, endpoint := range endpoints {
		if endpoint.Role == role {
			return net.JoinHostPort(endpoint.Address, strconv.Itoa(endpoint.PortOrDefault()))
		}
	}
	t.Fatalf("no %s endpoint among %+v", role, endpoints)
	return ""
}

func mustDo(t *testing.T, conn *Conn, args ...string) any {
	t.Helper()
	reply, err := conn.Do(args...)
	if err != nil {
		t.Fatalf("%s: %v", strings.Join(args, " "), err)
	}
	return reply
}

// An instance runs a primary and a replica behind a TLS primary endpoint and
// a read endpoint; a write through the one is read back through the other,
// across a reboot of the replica, a failover and a restart.
func TestInstanceReplicatesBehindItsEndpoints(t *testing.T) {
	cfg := engineTestConfig(t, t.Name())
	cfg.Image = valkeyImage
	cfg.Password = "instance-secret"
	authority, err := NewAuthority(pkix.Name{CommonName: "cacheengine test CA"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLS, err = ServerTLS(authority.CertPEM, authority.KeyPEM); err != nil {
		t.Fatal(err)
	}
	listeners, endpoints, err := Bind(cfg.Owner, []Endpoint{{Role: PrimaryEndpoint, Port: 6380}, {Role: ReadEndpoint, Port: 6380}})
	if err != nil {
		t.Fatal(err)
	}
	plane := New(cfg, Record{Endpoints: endpoints, Nodes: []int{0, 1}, Replicas: 1})
	t.Cleanup(plane.Remove)
	plane.ServeEndpoints(listeners)
	if err := plane.Ensure(); err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(authority.CertPEM))
	clientTLS := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	dial := func(role Role) *Conn {
		conn, err := DialTLS(endpointAddress(t, plane.Endpoints(), role), clientTLS, "", cfg.Password, 10*time.Second)
		if err != nil {
			t.Fatalf("connect to the %s endpoint: %v", role, err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	primary := dial(PrimaryEndpoint)
	mustDo(t, primary, "SET", "greeting", "hello")
	if acknowledged := mustDo(t, primary, "WAIT", "1", "0"); acknowledged != int64(1) {
		t.Fatalf("WAIT 1 = %v", acknowledged)
	}
	if value := mustDo(t, dial(ReadEndpoint), "GET", "greeting"); value != "hello" {
		t.Fatalf("the read endpoint returned %v", value)
	}
	if _, err := DialTLS(endpointAddress(t, plane.Endpoints(), PrimaryEndpoint), clientTLS, "", "wrong", 10*time.Second); err == nil {
		t.Fatal("the engine accepted a wrong password")
	}

	if err := plane.Reboot(plane.Replicas()); err != nil {
		t.Fatal(err)
	}
	if value := mustDo(t, dial(ReadEndpoint), "GET", "greeting"); value != "hello" {
		t.Fatalf("a rebooted replica holds %v", value)
	}
	if err := plane.Failover(1); err != nil {
		t.Fatal(err)
	}
	if plane.Primary() != 1 {
		t.Fatalf("primary is node %d after the failover", plane.Primary())
	}
	promoted := dial(PrimaryEndpoint)
	mustDo(t, promoted, "SET", "after", "failover")
	if err := plane.Restart(nil, ""); err != nil {
		t.Fatal(err)
	}
	if value := mustDo(t, dial(PrimaryEndpoint), "GET", "after"); value != "failover" {
		t.Fatalf("a restarted engine holds %v", value)
	}
}

// A cluster of two shards forms behind its discovery endpoint, announcing the
// endpoint of each node, and takes keys copied from another engine into the
// shard that owns each one's slot.
func TestClusterTakesCopiedKeys(t *testing.T) {
	sourceCfg := engineTestConfig(t, t.Name()+"-source")
	sourceCfg.Image = redisImage
	sourceListeners, sourceEndpoints, err := Bind(sourceCfg.Owner, []Endpoint{{Role: PrimaryEndpoint}})
	if err != nil {
		t.Fatal(err)
	}
	source := New(sourceCfg, Record{Endpoints: sourceEndpoints, Nodes: []int{0}})
	t.Cleanup(source.Remove)
	source.ServeEndpoints(sourceListeners)
	if err := source.Ensure(); err != nil {
		t.Fatal(err)
	}
	writer, err := Dial(endpointAddress(t, source.Endpoints(), PrimaryEndpoint), "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	for i := 0; i < 50; i++ {
		mustDo(t, writer, "SET", fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i))
	}
	mustDo(t, writer, "SET", "expiring", "soon", "EX", "3600")

	cfg := engineTestConfig(t, t.Name())
	cfg.Image = redisImage
	cfg.Cluster = true
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
	discovery, err := Dial(endpointAddress(t, cluster.Endpoints(), DiscoveryEndpoint), "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer discovery.Close()
	shards, _ := mustDo(t, discovery, "CLUSTER", "SLOTS").([]any)
	if len(shards) != 2 {
		t.Fatalf("CLUSTER SLOTS = %v", shards)
	}
	announced := map[string]bool{}
	for _, endpoint := range cluster.Endpoints() {
		if endpoint.Role == NodeEndpoint {
			announced[endpoint.Address] = true
		}
	}
	for _, shard := range shards {
		fields, _ := shard.([]any)
		owner, _ := fields[2].([]any)
		if !announced[fmt.Sprint(owner[0])] {
			t.Fatalf("a shard's primary announces %v, not a node endpoint", owner[0])
		}
	}

	reader, err := Dial(endpointAddress(t, source.Endpoints(), PrimaryEndpoint), "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	copied, err := cluster.CopyFrom(reader)
	if err != nil || copied != 51 {
		t.Fatalf("copied %d keys: %v", copied, err)
	}
	owners, err := cluster.slotOwners()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("key-%d", i)
		if value, err := cluster.Command(owners[KeySlot(key)], "GET", key); err != nil || value != fmt.Sprintf("value-%d", i) {
			t.Fatalf("%s = %v, %v", key, value, err)
		}
	}
	if ttl, err := cluster.Command(owners[KeySlot("expiring")], "TTL", "expiring"); err != nil || ttl.(int64) <= 0 {
		t.Fatalf("the copied key's time to live = %v, %v", ttl, err)
	}
}

func memcachedDo(t *testing.T, address, request string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(line)
}

// Memcached nodes each answer at an endpoint of their own; a resize adds one
// and a reboot empties the node it restarts.
func TestMemcachedNodesServeTheirEndpoints(t *testing.T) {
	base := engineTestConfig(t, t.Name())
	cfg := MemcachedConfig{
		Name: base.Name, Owner: base.Owner, Image: memcachedImage, Platform: base.Platform,
		Labels: base.Labels, NodeLabel: base.NodeLabel, Backend: base.Backend, Network: base.Network,
	}
	listeners, endpoints, err := Bind(cfg.Owner, []Endpoint{
		{Role: DiscoveryEndpoint, Port: MemcachedPort}, {Role: NodeEndpoint, Port: MemcachedPort, Node: 0}, {Role: NodeEndpoint, Port: MemcachedPort, Node: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	plane := NewMemcached(cfg, Record{Endpoints: endpoints, Nodes: []int{0, 1}})
	t.Cleanup(plane.Remove)
	plane.ServeEndpoints(listeners)
	if err := plane.Ensure(); err != nil {
		t.Fatal(err)
	}
	node := func(index int) string {
		for _, endpoint := range plane.Endpoints() {
			if endpoint.Role == NodeEndpoint && endpoint.Node == index {
				return net.JoinHostPort(endpoint.Address, strconv.Itoa(endpoint.Port))
			}
		}
		t.Fatalf("no endpoint for node %d", index)
		return ""
	}
	if reply := memcachedDo(t, node(0), "set greeting 0 0 5\r\nhello\r\n"); reply != "STORED" {
		t.Fatalf("set = %q", reply)
	}
	if reply := memcachedDo(t, node(0), "get greeting\r\n"); reply != "VALUE greeting 0 5" {
		t.Fatalf("get from node 0 = %q", reply)
	}
	if reply := memcachedDo(t, node(1), "get greeting\r\n"); reply != "END" {
		t.Fatalf("node 1 shares node 0's items: %q", reply)
	}
	if reply := memcachedDo(t, endpointAddress(t, plane.Endpoints(), DiscoveryEndpoint), "version\r\n"); !strings.HasPrefix(reply, "VERSION 1.6.22") {
		t.Fatalf("the discovery endpoint answered %q", reply)
	}
	if err := plane.Resize(3, MemcachedPort); err != nil {
		t.Fatal(err)
	}
	if reply := memcachedDo(t, node(2), "set added 0 0 1\r\nx\r\n"); reply != "STORED" {
		t.Fatalf("set on the added node = %q", reply)
	}
	if err := plane.Reboot([]int{0}); err != nil {
		t.Fatal(err)
	}
	if reply := memcachedDo(t, node(0), "get greeting\r\n"); reply != "END" {
		t.Fatalf("a rebooted node kept %q", reply)
	}
	if err := plane.Resize(1, MemcachedPort); err != nil {
		t.Fatal(err)
	}
	if got := plane.Nodes(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("nodes after shrinking to one: %v", got)
	}
}

// A cluster whose endpoints serve a port other than Redis's own redirects a
// client to the endpoint that serves the key's slot at that port, whether its
// engine announces endpoints by hostname (Redis 7) or names its nodes'
// network addresses, which the endpoints rewrite (Redis 6.2).
func TestClusterRedirectsToItsEndpointsOnTheirPort(t *testing.T) {
	for name, image := range map[string]string{"redis-6.2": "mirror.gcr.io/library/redis:6.2-alpine", "redis-7.2": redisImage} {
		t.Run(name, func(t *testing.T) {
			const port = 6390
			cfg := engineTestConfig(t, t.Name())
			cfg.Image = image
			cfg.Cluster = true
			listeners, endpoints, err := Bind(cfg.Owner, []Endpoint{
				{Role: DiscoveryEndpoint, Port: port}, {Role: NodeEndpoint, Port: port, Node: 0}, {Role: NodeEndpoint, Port: port, Node: 1},
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
			served := map[string]bool{}
			for _, endpoint := range cluster.Endpoints() {
				if endpoint.Role == NodeEndpoint {
					served[net.JoinHostPort(endpoint.Address, strconv.Itoa(port))] = true
				}
			}
			discovery, err := Dial(endpointAddress(t, cluster.Endpoints(), DiscoveryEndpoint), "", 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer discovery.Close()
			shards, _ := mustDo(t, discovery, "CLUSTER", "SLOTS").([]any)
			if len(shards) != 2 {
				t.Fatalf("CLUSTER SLOTS = %v", shards)
			}
			for _, shard := range shards {
				owner, _ := shard.([]any)[2].([]any)
				if at := net.JoinHostPort(fmt.Sprint(owner[0]), fmt.Sprint(owner[1])); !served[at] {
					t.Fatalf("CLUSTER SLOTS names %s, not a node endpoint among %v", at, served)
				}
			}
			moved := 0
			for i := 0; i < 20; i++ {
				key := fmt.Sprintf("key-%d", i)
				_, err := discovery.Do("SET", key, "v")
				var refused Error
				if err == nil || !errors.As(err, &refused) {
					continue
				}
				fields := strings.Fields(string(refused))
				if len(fields) != 3 || fields[0] != "MOVED" || !served[fields[2]] {
					t.Fatalf("SET %s: %v", key, err)
				}
				owner, err := Dial(fields[2], "", 10*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				mustDo(t, owner, "SET", key, "v")
				_ = owner.Close()
				moved++
			}
			if moved == 0 {
				t.Fatal("no key's slot belonged to the other shard, so no redirection was followed")
			}
		})
	}
}

// An instance copies each of the source's databases into the database of the
// same number.
func TestInstanceCopiesEveryDatabase(t *testing.T) {
	plane := func(name string) *Plane {
		cfg := engineTestConfig(t, name)
		cfg.Image = redisImage
		listeners, endpoints, err := Bind(cfg.Owner, []Endpoint{{Role: PrimaryEndpoint}})
		if err != nil {
			t.Fatal(err)
		}
		p := New(cfg, Record{Endpoints: endpoints, Nodes: []int{0}})
		t.Cleanup(p.Remove)
		p.ServeEndpoints(listeners)
		if err := p.Ensure(); err != nil {
			t.Fatal(err)
		}
		return p
	}
	source, target := plane(t.Name()+"-source"), plane(t.Name())
	writer, err := Dial(endpointAddress(t, source.Endpoints(), PrimaryEndpoint), "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	mustDo(t, writer, "SET", "in-zero", "0")
	mustDo(t, writer, "SELECT", "3")
	mustDo(t, writer, "SET", "in-three", "3")
	reader, err := Dial(endpointAddress(t, source.Endpoints(), PrimaryEndpoint), "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if copied, err := target.CopyFrom(reader); err != nil || copied != 2 {
		t.Fatalf("copied %d keys: %v", copied, err)
	}
	check, err := Dial(endpointAddress(t, target.Endpoints(), PrimaryEndpoint), "", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	if value := mustDo(t, check, "GET", "in-zero"); value != "0" {
		t.Fatalf("database 0 holds %v", value)
	}
	if value, _ := check.Do("GET", "in-three"); value != nil {
		t.Fatalf("database 0 took database 3's key: %v", value)
	}
	mustDo(t, check, "SELECT", "3")
	if value := mustDo(t, check, "GET", "in-three"); value != "3" {
		t.Fatalf("database 3 holds %v", value)
	}
}
