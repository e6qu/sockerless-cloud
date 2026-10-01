package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	dockerclient "github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// The Memorystore for Redis data plane.
//
// An instance or cluster is a real Redis engine: one container whose
// redis-server processes are the resource's nodes, each on a container port of
// its own (7000, 7001, …) and published on a host loopback port. The endpoints
// the API reports are listeners this process owns on loopback addresses of
// their own, relaying bytes to the node behind them: an instance's host and
// readEndpoint and a cluster's discovery endpoint at Redis's port 6379, and
// each cluster node at the address and port it announces. A cluster node
// announces its container port because a replica replicates from the address
// its primary announces, which has to reach the primary inside the container
// as well as from the host. Redis itself answers AUTH, replication and cluster
// redirections; the relay never reads the stream.
//
// The create starts the engine and settles once every node answers, so a
// resource reported ready is serving; the delete stops it and removes its
// volume. An engine that stopped since starts again at the next connection or
// operation that needs it. Persistence is off (`save ""`): the dataset lives in memory, an RDB
// exists only while an export, backup, import or upgrade moves it, and a fresh
// engine starts empty unless one of those staged a snapshot for it to load.

const (
	msRedisPort            = 6379
	msRedisFirstNodePort   = 7000
	msRedisBusPortOffset   = 10000
	msRedisDataPath        = "/data"
	msRedisReadyBudget     = 10 * time.Minute
	msRedisLivenessEvery   = 2 * time.Second
	msRedisProbeInterval   = 100 * time.Millisecond
	msRedisStopGrace       = 5 * time.Second
	msRedisRemovalTimeout  = 30 * time.Second
	msRedisCommandDeadline = 2 * time.Minute
	msRedisClusterSlots    = 16384
)

// msRedisEngineImage is the engine image for a Memorystore for Redis version.
// The Amazon ECR Public Gallery mirror of the Docker official images carries
// no 3.2 or 4.0 tag, so those come from Docker Hub.
func msRedisEngineImage(redisVersion string) (string, bool) {
	switch redisVersion {
	case "REDIS_3_2":
		return "docker.io/library/redis:3.2-alpine", true
	case "REDIS_4_0":
		return "docker.io/library/redis:4.0-alpine", true
	case "REDIS_5_0":
		return "public.ecr.aws/docker/library/redis:5.0-alpine", true
	case "REDIS_6_X":
		return "public.ecr.aws/docker/library/redis:6.2-alpine", true
	case "REDIS_7_0":
		return "public.ecr.aws/docker/library/redis:7.0-alpine", true
	case "REDIS_7_2":
		return "public.ecr.aws/docker/library/redis:7.2-alpine", true
	}
	return "", false
}

// msRedisClusterEngineImage is the engine of Memorystore for Redis Cluster,
// whose backups report engineVersion redis-7.2.
// msRedisEnginePlatform is the platform the engine runs on whatever the host
// is, as the Cloud SQL data plane's is.
const msRedisEnginePlatform = "linux/amd64"

const (
	msRedisClusterEngineImage   = "public.ecr.aws/docker/library/redis:7.2-alpine"
	msRedisClusterEngineVersion = "redis-7.2"
)

// msRedisEndpointRole says which node an endpoint relays to.
type msRedisEndpointRole string

const (
	msRedisPrimaryEndpoint   msRedisEndpointRole = "primary"
	msRedisReadEndpoint      msRedisEndpointRole = "read"
	msRedisDiscoveryEndpoint msRedisEndpointRole = "discovery"
	msRedisNodeEndpoint      msRedisEndpointRole = "node"
)

type msRedisEndpointRecord struct {
	Role    msRedisEndpointRole `json:"role"`
	Address string              `json:"address"`
	Node    int                 `json:"node,omitempty"`
}

// msRedisPlaneRecord is what a control-plane restart needs to serve a
// resource's endpoints again: the addresses it reported, the shape the engine
// was started with, the node that is primary, and the AUTH string the engine
// requires.
type msRedisPlaneRecord struct {
	Endpoints  []msRedisEndpointRecord `json:"endpoints,omitempty"`
	Shards     int                     `json:"shards,omitempty"`
	Replicas   int                     `json:"replicas,omitempty"`
	Primary    int                     `json:"primary"`
	AuthString string                  `json:"authString,omitempty"`
}

var msRedisPlaneRecords sim.Store[msRedisPlaneRecord]

// msRedisTopology is the shape of the engine: how many redis-server processes
// run and how they relate.
type msRedisTopology struct {
	// Cluster runs every node cluster-enabled; shard s is primary node s and
	// its replicas follow all primaries in shard order.
	Cluster  bool
	Shards   int
	Replicas int
	// Announce is the address each cluster node reports to clients.
	Announce []string
}

func (t msRedisTopology) nodes() int {
	if t.Cluster {
		return t.Shards * (t.Replicas + 1)
	}
	return 1 + t.Replicas
}

// replicaOf is the shard primary a cluster replica node follows.
func (t msRedisTopology) replicaOf(node int) int {
	return (node - t.Shards) / t.Replicas
}

// slotRange is the hash-slot range shard s owns, the slots split evenly.
func (t msRedisTopology) slotRange(shard int) (first, last int) {
	first = shard * msRedisClusterSlots / t.Shards
	last = (shard+1)*msRedisClusterSlots/t.Shards - 1
	return first, last
}

type msRedisPlane struct {
	name     string
	volume   string
	labels   map[string]string
	topology msRedisTopology
	password string
	// configs are the engine directives every node starts with, as
	// name/value pairs.
	configs [][2]string

	mu        sync.RWMutex
	image     string
	primary   int
	listeners []net.Listener
	hostPorts []int
	handle    *sim.ContainerHandle

	startMu   sync.Mutex
	attempted bool
	startErr  error

	// opMu serialises the operations that move a snapshot in or out.
	opMu sync.Mutex

	readCursor atomic.Uint64
}

var msRedisPlanes sync.Map // resource name -> *msRedisPlane

func msRedisLoadPlane(name string) (*msRedisPlane, bool) {
	value, ok := msRedisPlanes.Load(name)
	if !ok {
		return nil, false
	}
	plane, ok := value.(*msRedisPlane)
	return plane, ok
}

func msRedisVolume(name string) string {
	return "sockerless-memorystore-" + strings.NewReplacer("/", "-").Replace(name)
}

func msRedisNodeDir(node int) string {
	return fmt.Sprintf("%s/node-%d", msRedisDataPath, node)
}

func msRedisNodePort(node int) int { return msRedisFirstNodePort + node }

// msRedisEngineDirectives translates Memorystore's redisConfigs into
// redis-server directives. maxmemory-gb is Memorystore's own knob for
// maxmemory, in GiB; every other supported name is a Redis directive already.
func msRedisEngineDirectives(memorySizeGb int, redisConfigs map[string]string) ([][2]string, error) {
	var directives [][2]string
	maxmemory := int64(memorySizeGb) << 30
	names := make([]string, 0, len(redisConfigs))
	for name := range redisConfigs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := redisConfigs[name]
		if name == "maxmemory-gb" {
			gb, err := strconv.ParseFloat(value, 64)
			if err != nil || gb <= 0 {
				return nil, fmt.Errorf("redisConfigs maxmemory-gb %q is not a positive number", value)
			}
			maxmemory = int64(gb * float64(1<<30))
			continue
		}
		directives = append(directives, [2]string{name, value})
	}
	if maxmemory > 0 {
		directives = append([][2]string{{"maxmemory", strconv.FormatInt(maxmemory, 10)}}, directives...)
	}
	return directives, nil
}

// script is the container's command: every node but the primary runs in the
// background and the primary replaces the shell, so the container lives
// exactly as long as the primary does and a SIGTERM reaches it directly.
func (p *msRedisPlane) script(primary int) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	for node := 0; node < p.topology.nodes(); node++ {
		dir := msRedisNodeDir(node)
		// A replica's directory holds nothing it needs: it resynchronises
		// from its primary. The primary's may hold a snapshot staged for it.
		if p.topology.Cluster || node != primary {
			fmt.Fprintf(&b, "rm -rf %s\n", dbengine.ShellQuote(dir))
		}
		fmt.Fprintf(&b, "mkdir -p %s\n", dbengine.ShellQuote(dir))
	}
	for node := 0; node < p.topology.nodes(); node++ {
		if node == primary {
			continue
		}
		b.WriteString(p.serverCommand(node, primary))
		b.WriteString(" &\n")
	}
	b.WriteString("exec ")
	b.WriteString(p.serverCommand(primary, primary))
	b.WriteString("\n")
	return b.String()
}

func (p *msRedisPlane) serverCommand(node, primary int) string {
	args := []string{"redis-server",
		"--port", strconv.Itoa(msRedisNodePort(node)),
		"--bind", "0.0.0.0",
		"--protected-mode", "no",
		"--dir", msRedisNodeDir(node),
		"--save", "",
		"--appendonly", "no",
	}
	if p.password != "" {
		args = append(args, "--requirepass", p.password, "--masterauth", p.password)
	}
	if p.topology.Cluster {
		args = append(args,
			"--cluster-enabled", "yes",
			"--cluster-config-file", msRedisNodeDir(node)+"/nodes.conf",
			"--cluster-announce-ip", p.topology.Announce[node],
			"--cluster-announce-port", strconv.Itoa(msRedisNodePort(node)),
			"--cluster-announce-bus-port", strconv.Itoa(msRedisNodePort(node)+msRedisBusPortOffset),
		)
	} else if node != primary {
		// slaveof is the spelling every supported version accepts.
		args = append(args, "--slaveof", "127.0.0.1", strconv.Itoa(msRedisNodePort(primary)))
	}
	for _, directive := range p.configs {
		args = append(args, "--"+directive[0], directive[1])
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = dbengine.ShellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

// serve relays every connection listener accepts to the node target names.
func (p *msRedisPlane) serve(listener net.Listener, target func() (int, bool)) {
	p.mu.Lock()
	p.listeners = append(p.listeners, listener)
	p.mu.Unlock()
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go p.relayConnection(client, target)
		}
	}()
}

// Primary is the node that takes writes.
func (p *msRedisPlane) Primary() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.primary
}

func (p *msRedisPlane) primaryTarget() (int, bool) { return p.Primary(), true }

// readTarget spreads connections over the replicas, which is what the read
// endpoint balances across.
func (p *msRedisPlane) readTarget() (int, bool) {
	p.mu.RLock()
	primary := p.primary
	p.mu.RUnlock()
	replicas := make([]int, 0, p.topology.nodes()-1)
	for node := 0; node < p.topology.nodes(); node++ {
		if node != primary {
			replicas = append(replicas, node)
		}
	}
	if len(replicas) == 0 {
		return 0, false
	}
	return replicas[int(p.readCursor.Add(1)-1)%len(replicas)], true
}

func fixedTarget(node int) func() (int, bool) {
	return func() (int, bool) { return node, true }
}

func (p *msRedisPlane) relayConnection(client net.Conn, target func() (int, bool)) {
	defer client.Close()
	if err := p.Ensure(); err != nil {
		log.Printf("Memorystore %s data plane: %v", p.name, err)
		return
	}
	node, ok := target()
	if !ok {
		return
	}
	address, err := p.nodeAddress(node)
	if err != nil {
		log.Printf("Memorystore %s data plane: %v", p.name, err)
		return
	}
	backend, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		log.Printf("Memorystore %s data plane: dial node %d: %v", p.name, node, err)
		return
	}
	defer backend.Close()
	msRedisRelay(client, backend)
}

func msRedisRelay(left, right net.Conn) {
	done := make(chan struct{}, 2)
	copySide := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}
	go copySide(left, right)
	go copySide(right, left)
	<-done
}

func (p *msRedisPlane) nodeAddress(node int) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.handle == nil || node >= len(p.hostPorts) {
		return "", fmt.Errorf("the Redis engine is not running")
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(p.hostPorts[node])), nil
}

func (p *msRedisPlane) containerID() (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.handle == nil {
		return "", fmt.Errorf("the Redis engine is not running")
	}
	return p.handle.ContainerID, nil
}

// Ensure brings the engine up and returns once every node answers and the
// topology is in place. The outcome holds until Stop.
func (p *msRedisPlane) Ensure() error {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if !p.attempted {
		p.attempted = true
		p.startErr = p.bringUp()
	}
	return p.startErr
}

func (p *msRedisPlane) bringUp() error {
	p.mu.RLock()
	running := p.handle != nil
	p.mu.RUnlock()
	if !running {
		if err := p.start(); err != nil {
			return err
		}
	}
	if err := p.awaitNodes(); err != nil {
		_ = p.stopEngine()
		return err
	}
	if err := p.settle(); err != nil {
		_ = p.stopEngine()
		return err
	}
	return nil
}

func (p *msRedisPlane) start() error {
	p.mu.RLock()
	image, primary := p.image, p.primary
	p.mu.RUnlock()
	nodes := p.topology.nodes()
	publish := make([]int, nodes)
	for node := range publish {
		publish[node] = msRedisNodePort(node)
	}
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		CancelGracePeriod: msRedisStopGrace,
		Image:             image,
		Architecture:      msRedisEnginePlatform,
		Command:           []string{"/bin/sh", "-c", p.script(primary)},
		PublishPorts:      publish,
		Binds:             []string{p.volume + ":" + msRedisDataPath},
		Labels:            p.labels,
		Sandbox:           SandboxCloudRun,
	}, sim.NoopSink{})
	if err != nil {
		return fmt.Errorf("start the Redis engine: %w", err)
	}
	hostPorts, err := msRedisPublishedPorts(handle, nodes)
	if err != nil {
		handle.Cancel()
		_ = handle.Wait()
		return fmt.Errorf("start the Redis engine: %w", err)
	}
	p.mu.Lock()
	p.handle, p.hostPorts = handle, hostPorts
	p.mu.Unlock()
	return nil
}

func msRedisPublishedPorts(handle *sim.ContainerHandle, nodes int) ([]int, error) {
	hostPorts := make([]int, nodes)
	for node := range hostPorts {
		port, err := handle.PublishedPort(context.Background(), msRedisNodePort(node))
		if err != nil {
			return nil, fmt.Errorf("node %d: %w", node, err)
		}
		hostPorts[node] = port
	}
	return hostPorts, nil
}

// awaitNodes waits until every node answers PING with its dataset loaded. A
// node still loading a snapshot answers LOADING; one whose container stopped
// never will, so the wait ends there instead of at the budget.
func (p *msRedisPlane) awaitNodes() error {
	containerID, err := p.containerID()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(msRedisReadyBudget)
	nextLiveness := time.Now().Add(msRedisLivenessEvery)
	for node := 0; node < p.topology.nodes(); node++ {
		for {
			address, err := p.nodeAddress(node)
			if err != nil {
				return err
			}
			if msRedisAnswers(address, p.password) {
				break
			}
			now := time.Now()
			if !now.Before(nextLiveness) {
				if !sim.ContainerRunning(containerID) {
					return fmt.Errorf("the Redis engine stopped before accepting connections: container %s is not running", containerID)
				}
				nextLiveness = now.Add(msRedisLivenessEvery)
			}
			if !now.Before(deadline) {
				return fmt.Errorf("the Redis engine did not become ready within %s", msRedisReadyBudget)
			}
			time.Sleep(msRedisProbeInterval)
		}
	}
	return nil
}

func msRedisAnswers(address, password string) bool {
	conn, err := msRedisDial(address, password, 500*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	reply, err := conn.Do("PING")
	return err == nil && reply == "PONG"
}

// settle puts the topology in place on a freshly started or adopted engine:
// replication for an instance, slots and replicas for a cluster. It then
// removes a snapshot staged for the primary to load, so a later restart does
// not bring that dataset back.
func (p *msRedisPlane) settle() error {
	if p.topology.Cluster {
		if err := p.formCluster(); err != nil {
			return fmt.Errorf("form the Redis cluster: %w", err)
		}
		return nil
	}
	if err := p.awaitReplication(); err != nil {
		return err
	}
	p.mu.RLock()
	primary := p.primary
	p.mu.RUnlock()
	return p.exec([]string{"rm", "-f", msRedisNodeDir(primary) + "/dump.rdb"})
}

// awaitReplication waits until every replica's link to the primary is up and
// its initial synchronisation is over.
func (p *msRedisPlane) awaitReplication() error {
	p.mu.RLock()
	primary := p.primary
	p.mu.RUnlock()
	for node := 0; node < p.topology.nodes(); node++ {
		if node == primary {
			continue
		}
		if err := p.awaitInfo(node, "replication", func(info map[string]string) bool {
			return info["master_link_status"] == "up" && info["master_sync_in_progress"] == "0"
		}); err != nil {
			return fmt.Errorf("replica %d did not synchronise: %w", node, err)
		}
	}
	return nil
}

func (p *msRedisPlane) awaitInfo(node int, section string, satisfied func(map[string]string) bool) error {
	deadline := time.Now().Add(msRedisCommandDeadline)
	for {
		reply, err := p.command(node, "INFO", section)
		if err != nil {
			return err
		}
		text, _ := reply.(string)
		if satisfied(msRedisParseInfo(text)) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("INFO %s did not settle within %s", section, msRedisCommandDeadline)
		}
		time.Sleep(msRedisProbeInterval)
	}
}

func msRedisParseInfo(text string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if found {
			fields[key] = value
		}
	}
	return fields
}

// formCluster joins the nodes, gives each shard primary its slot range and
// attaches each replica to its primary. An adopted engine already formed
// keeps what it has.
func (p *msRedisPlane) formCluster() error {
	t := p.topology
	reply, err := p.command(0, "CLUSTER", "INFO")
	if err != nil {
		return err
	}
	info := msRedisParseInfo(fmt.Sprint(reply))
	if info["cluster_slots_assigned"] == strconv.Itoa(msRedisClusterSlots) &&
		info["cluster_known_nodes"] == strconv.Itoa(t.nodes()) {
		return p.awaitClusterState()
	}
	ids := make([]string, t.nodes())
	for node := range ids {
		reply, err := p.command(node, "CLUSTER", "MYID")
		if err != nil {
			return err
		}
		ids[node] = fmt.Sprint(reply)
	}
	for node := 1; node < t.nodes(); node++ {
		if _, err := p.command(0, "CLUSTER", "MEET", t.Announce[node], strconv.Itoa(msRedisNodePort(node)),
			strconv.Itoa(msRedisNodePort(node)+msRedisBusPortOffset)); err != nil {
			return err
		}
	}
	for shard := 0; shard < t.Shards; shard++ {
		first, last := t.slotRange(shard)
		if _, err := p.command(shard, "CLUSTER", "ADDSLOTSRANGE", strconv.Itoa(first), strconv.Itoa(last)); err != nil {
			return err
		}
	}
	for node := t.Shards; node < t.nodes(); node++ {
		primaryID := ids[t.replicaOf(node)]
		// A node can follow only a primary it has learned of through the
		// cluster bus.
		if err := p.awaitClusterNodes(node, primaryID); err != nil {
			return err
		}
		if _, err := p.command(node, "CLUSTER", "REPLICATE", primaryID); err != nil {
			return err
		}
	}
	return p.awaitClusterState()
}

func (p *msRedisPlane) awaitClusterNodes(node int, id string) error {
	deadline := time.Now().Add(msRedisCommandDeadline)
	for {
		reply, err := p.command(node, "CLUSTER", "NODES")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(fmt.Sprint(reply), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 2 && fields[0] == id && strings.Contains(fields[2], "master") {
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("node %d did not learn of primary %s within %s", node, id, msRedisCommandDeadline)
		}
		time.Sleep(msRedisProbeInterval)
	}
}

// awaitClusterState waits until every node reports the cluster ok and sees
// every primary and replica in its role, so the topology a client reads from
// any node is the whole one, and every replica has synchronised.
func (p *msRedisPlane) awaitClusterState() error {
	t := p.topology
	want := strconv.Itoa(t.nodes())
	for node := 0; node < t.nodes(); node++ {
		deadline := time.Now().Add(msRedisCommandDeadline)
		for {
			reply, err := p.command(node, "CLUSTER", "INFO")
			if err != nil {
				return err
			}
			info := msRedisParseInfo(fmt.Sprint(reply))
			reply, err = p.command(node, "CLUSTER", "NODES")
			if err != nil {
				return err
			}
			primaries, replicas := msRedisCountRoles(fmt.Sprint(reply))
			if info["cluster_state"] == "ok" && info["cluster_known_nodes"] == want &&
				primaries == t.Shards && replicas == t.Shards*t.Replicas {
				break
			}
			if !time.Now().Before(deadline) {
				return fmt.Errorf("node %d reported cluster_state %q with %s known nodes, %d primaries and %d replicas after %s",
					node, info["cluster_state"], info["cluster_known_nodes"], primaries, replicas, msRedisCommandDeadline)
			}
			time.Sleep(msRedisProbeInterval)
		}
	}
	for node := t.Shards; node < t.nodes(); node++ {
		if err := p.awaitInfo(node, "replication", func(info map[string]string) bool {
			return info["master_link_status"] == "up" && info["master_sync_in_progress"] == "0"
		}); err != nil {
			return fmt.Errorf("replica %d did not synchronise: %w", node, err)
		}
	}
	return nil
}

// msRedisCountRoles counts the connected primaries and replicas a CLUSTER
// NODES reply lists.
func msRedisCountRoles(nodes string) (primaries, replicas int) {
	for _, line := range strings.Split(nodes, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[7] != "connected" {
			continue
		}
		for _, flag := range strings.Split(fields[2], ",") {
			switch flag {
			case "master":
				primaries++
			case "slave":
				replicas++
			}
		}
	}
	return primaries, replicas
}

// command runs one Redis command on a node through its published port.
func (p *msRedisPlane) command(node int, args ...string) (any, error) {
	address, err := p.nodeAddress(node)
	if err != nil {
		return nil, err
	}
	conn, err := msRedisDial(address, p.password, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to node %d: %w", node, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(msRedisCommandDeadline)); err != nil {
		return nil, err
	}
	reply, err := conn.Do(args...)
	if err != nil {
		return nil, fmt.Errorf("%s on node %d: %w", strings.Join(args[:min(2, len(args))], " "), node, err)
	}
	return reply, nil
}

// Snapshot has node write its dataset as an RDB file with SAVE and returns the
// file's bytes; the file does not outlive the call.
func (p *msRedisPlane) Snapshot(node int) ([]byte, error) {
	if _, err := p.command(node, "SAVE"); err != nil {
		return nil, err
	}
	path := msRedisNodeDir(node) + "/dump.rdb"
	data, err := p.readFile(path)
	if err != nil {
		return nil, err
	}
	if err := p.exec([]string{"rm", "-f", path}); err != nil {
		return nil, err
	}
	return data, nil
}

// Restart replaces the running engine with a fresh one on the same volume.
// With rdb, the primary loads it as its dataset; without, it saves its own
// first, so the new engine — on image, when one is named — holds what the old
// one held.
func (p *msRedisPlane) Restart(rdb []byte, image string) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	p.mu.RLock()
	primary := p.primary
	p.mu.RUnlock()
	path := msRedisNodeDir(primary) + "/dump.rdb"
	if rdb != nil {
		if err := p.writeFile(path, rdb); err != nil {
			return err
		}
	} else if _, err := p.command(primary, "SAVE"); err != nil {
		return err
	}
	if image != "" {
		p.mu.Lock()
		p.image = image
		p.mu.Unlock()
	}
	if err := p.Stop(); err != nil {
		return err
	}
	return p.Ensure()
}

// Failover promotes node to primary: it stops replicating, and every other
// node, the old primary included, replicates from it.
func (p *msRedisPlane) Failover(node int) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	if _, err := p.command(node, "SLAVEOF", "NO", "ONE"); err != nil {
		return err
	}
	for other := 0; other < p.topology.nodes(); other++ {
		if other == node {
			continue
		}
		if _, err := p.command(other, "SLAVEOF", "127.0.0.1", strconv.Itoa(msRedisNodePort(node))); err != nil {
			return err
		}
	}
	p.mu.Lock()
	p.primary = node
	p.mu.Unlock()
	return p.awaitReplication()
}

// Configure applies directives to every running node with CONFIG SET, as a
// live configuration change does. An engine that is not running takes them at
// its next start.
func (p *msRedisPlane) Configure(directives [][2]string) error {
	p.mu.Lock()
	p.configs = directives
	running := p.handle != nil
	p.mu.Unlock()
	if !running {
		return nil
	}
	for node := 0; node < p.topology.nodes(); node++ {
		for _, directive := range directives {
			if _, err := p.command(node, "CONFIG", "SET", directive[0], directive[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *msRedisPlane) readFile(path string) ([]byte, error) {
	containerID, err := p.containerID()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), msRedisCommandDeadline)
	defer cancel()
	copied, err := sim.DockerClient().CopyFromContainer(ctx, containerID, dockerclient.CopyFromContainerOptions{SourcePath: path})
	if err != nil {
		return nil, fmt.Errorf("read %s from the Redis engine: %w", path, err)
	}
	defer copied.Content.Close()
	archive := tar.NewReader(copied.Content)
	for {
		header, err := archive.Next()
		if err != nil {
			return nil, fmt.Errorf("read %s from the Redis engine: %w", path, err)
		}
		if header.Typeflag == tar.TypeReg {
			return io.ReadAll(archive)
		}
	}
}

func (p *msRedisPlane) writeFile(path string, data []byte) error {
	containerID, err := p.containerID()
	if err != nil {
		return err
	}
	dir, file := path[:strings.LastIndex(path, "/")], path[strings.LastIndex(path, "/")+1:]
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: file, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), msRedisCommandDeadline)
	defer cancel()
	if _, err := sim.DockerClient().CopyToContainer(ctx, containerID, dockerclient.CopyToContainerOptions{
		DestinationPath: dir, Content: &archive,
	}); err != nil {
		return fmt.Errorf("write %s into the Redis engine: %w", path, err)
	}
	return nil
}

func (p *msRedisPlane) exec(command []string) error {
	containerID, err := p.containerID()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), msRedisCommandDeadline)
	defer cancel()
	docker := sim.DockerClient()
	created, err := docker.ExecCreate(ctx, containerID, dockerclient.ExecCreateOptions{
		Cmd: command, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("create engine command: %w", err)
	}
	attached, err := docker.ExecAttach(ctx, created.ID, dockerclient.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("attach engine command: %w", err)
	}
	output, readErr := io.ReadAll(attached.Reader)
	attached.Close()
	if readErr != nil {
		return fmt.Errorf("read engine command output: %w", readErr)
	}
	inspected, err := docker.ExecInspect(ctx, created.ID, dockerclient.ExecInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect engine command: %w", err)
	}
	if inspected.ExitCode != 0 {
		return fmt.Errorf("%s exited %d: %s", strings.Join(command, " "), inspected.ExitCode, strings.TrimSpace(string(output)))
	}
	return nil
}

// Adopt picks up the engine container an earlier control-plane process left
// for this resource, resuming it when it had stopped.
func (p *msRedisPlane) Adopt() error {
	existing, err := sim.FindExistingContainers(p.labels)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return nil
	}
	if len(existing) != 1 {
		return fmt.Errorf("found %d Redis engine containers", len(existing))
	}
	if !existing[0].Running {
		if err := sim.StartExistingContainer(existing[0].ID); err != nil {
			return fmt.Errorf("resume Redis engine container %s: %w", existing[0].ID, err)
		}
	}
	handle, err := sim.AdoptContainer(existing[0].ID, sim.ContainerConfig{CancelGracePeriod: msRedisStopGrace}, sim.NoopSink{})
	if err != nil {
		return err
	}
	hostPorts, err := msRedisPublishedPorts(handle, p.topology.nodes())
	if err != nil {
		handle.Cancel()
		_ = handle.Wait()
		return fmt.Errorf("adopt the Redis engine container %s: %w", existing[0].ID, err)
	}
	p.mu.Lock()
	p.handle, p.hostPorts = handle, hostPorts
	p.mu.Unlock()
	return nil
}

// Stop stops the engine and forgets the last start's outcome, so the next
// client starts a fresh engine on the same volume. It returns once the
// container is gone.
func (p *msRedisPlane) Stop() error {
	firstErr := p.stopEngine()
	p.startMu.Lock()
	defer p.startMu.Unlock()
	err := p.stopEngine()
	p.attempted, p.startErr = false, nil
	return errors.Join(firstErr, err)
}

func (p *msRedisPlane) stopEngine() error {
	p.mu.Lock()
	handle := p.handle
	p.handle, p.hostPorts = nil, nil
	p.mu.Unlock()
	if handle == nil {
		return nil
	}
	handle.Cancel()
	_ = handle.Wait()
	return sim.WaitContainerRemoved(handle.ContainerID, msRedisRemovalTimeout)
}

// Close stops accepting clients and stops the engine; the volume stays.
func (p *msRedisPlane) Close() error {
	p.mu.Lock()
	listeners := p.listeners
	p.listeners = nil
	p.mu.Unlock()
	for _, listener := range listeners {
		_ = listener.Close()
	}
	return p.Stop()
}

// msRedisRemovePlane closes a deleted resource's endpoints, stops its engine
// and removes its volume.
func msRedisRemovePlane(name string) {
	msRedisPlaneRecords.Delete(name)
	value, ok := msRedisPlanes.LoadAndDelete(name)
	if !ok {
		return
	}
	plane, ok := value.(*msRedisPlane)
	if !ok {
		return
	}
	if err := plane.Close(); err != nil {
		log.Printf("Memorystore %s: stop the Redis engine: %v", name, err)
	}
	sim.RemoveVolumeSettled(plane.volume, "memorystore")
}

// msRedisListen binds port on a loopback address of the endpoint's own.
func msRedisListen(identifier string, port int) (net.Listener, string, error) {
	listener, err := dbengine.ListenLoopback(identifier, port)
	if err != nil {
		return nil, "", err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, "", fmt.Errorf("listener returned address type %T", listener.Addr())
	}
	return listener, address.IP.String(), nil
}

// msRedisConn is a client connection speaking RESP2, enough for the
// control plane's own commands.
type msRedisConn struct {
	net.Conn
	reader *bufio.Reader
}

// msRedisError is an error reply from the engine.
type msRedisError string

func (e msRedisError) Error() string { return string(e) }

func msRedisDial(address, password string, timeout time.Duration) (*msRedisConn, error) {
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return nil, err
	}
	c := &msRedisConn{Conn: conn, reader: bufio.NewReader(conn)}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if password != "" {
		if _, err := c.Do("AUTH", password); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return c, nil
}

// Do sends one command and reads its reply: a string for a status or bulk
// reply, an int64 for an integer, a []any for an array and nil for a null.
func (c *msRedisConn) Do(args ...string) (any, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(arg), arg)
	}
	if _, err := io.WriteString(c.Conn, b.String()); err != nil {
		return nil, err
	}
	return msRedisReadReply(c.reader)
}

func msRedisReadReply(reader *bufio.Reader) (any, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(line, "\r\n")
	if line == "" {
		return nil, fmt.Errorf("empty RESP reply")
	}
	payload := line[1:]
	switch line[0] {
	case '+':
		return payload, nil
	case '-':
		return nil, msRedisError(payload)
	case ':':
		return strconv.ParseInt(payload, 10, 64)
	case '$':
		size, err := strconv.Atoi(payload)
		if err != nil {
			return nil, fmt.Errorf("bulk length %q: %w", payload, err)
		}
		if size < 0 {
			return nil, nil
		}
		data := make([]byte, size+2)
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, err
		}
		return string(data[:size]), nil
	case '*':
		count, err := strconv.Atoi(payload)
		if err != nil {
			return nil, fmt.Errorf("array length %q: %w", payload, err)
		}
		if count < 0 {
			return nil, nil
		}
		items := make([]any, count)
		for i := range items {
			if items[i], err = msRedisReadReply(reader); err != nil {
				return nil, err
			}
		}
		return items, nil
	}
	return nil, fmt.Errorf("unknown RESP reply %q", line)
}

// msRedisRDBMagic opens every RDB file.
const msRedisRDBMagic = "REDIS"

func msRedisIsRDB(data []byte) bool {
	return bytes.HasPrefix(data, []byte(msRedisRDBMagic))
}
