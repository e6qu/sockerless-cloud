package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
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

	"github.com/moby/moby/api/pkg/stdcopy"
	dockerclient "github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// The Memorystore for Redis data plane.
//
// An instance or cluster is a real Redis engine: one container per node, each
// a redis-server listening on Redis's port 6379, on a Docker network every
// Memorystore engine of this simulator shares, and published on a host
// loopback port. The endpoints the API reports are listeners this process owns
// on loopback addresses of their own, relaying bytes to the node behind them:
// an instance's host and readEndpoint, a cluster's discovery endpoint, and each
// cluster node at the address it announces as its hostname. Instance replicas
// follow their primary by its network alias; cluster nodes meet over the
// network and announce the relay's address to clients, so a cluster client
// follows redirections to endpoints it can reach. Redis itself answers AUTH,
// replication and cluster redirections; a relay terminates TLS when the
// resource encrypts in transit, and on a cluster that authenticates with IAM it
// exchanges a verified access token for the engine's own credential.
//
// The create starts the engine and settles once every node answers, so a
// resource reported ready is serving; the delete stops it and removes its
// volume. Adding a node starts another container while the rest keep serving.

const (
	msRedisPort            = 6379
	msRedisTLSPort         = 6378
	msRedisDataPath        = "/data"
	msRedisReadyBudget     = 10 * time.Minute
	msRedisLivenessEvery   = 2 * time.Second
	msRedisProbeInterval   = 100 * time.Millisecond
	msRedisStopGrace       = 5 * time.Second
	msRedisRemovalTimeout  = 30 * time.Second
	msRedisCommandDeadline = 2 * time.Minute
	msRedisReshardDeadline = 10 * time.Minute
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

// msRedisEnginePlatform is the platform the engine runs on whatever the host
// is, as the Cloud SQL data plane's is.
const msRedisEnginePlatform = "linux/amd64"

// Memorystore for Redis Cluster runs Redis 7.2, the engineVersion its backups
// report.
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
	Port    int                 `json:"port,omitempty"`
	Node    int                 `json:"node,omitempty"`
}

func (e msRedisEndpointRecord) port() int {
	if e.Port == 0 {
		return msRedisPort
	}
	return e.Port
}

func (e msRedisEndpointRecord) key() string {
	if e.Role == msRedisNodeEndpoint {
		return "node-" + strconv.Itoa(e.Node)
	}
	return string(e.Role)
}

// msRedisPlaneRecord is what a control-plane restart needs to serve a
// resource's endpoints again: the addresses it reported, the nodes the engine
// runs, its shape, the node that is primary, the AUTH string an instance
// requires, and the credential a cluster's engine requires of the clients the
// service authenticated.
type msRedisPlaneRecord struct {
	Endpoints  []msRedisEndpointRecord `json:"endpoints,omitempty"`
	Nodes      []int                   `json:"nodes,omitempty"`
	Shards     int                     `json:"shards,omitempty"`
	Replicas   int                     `json:"replicas,omitempty"`
	Primary    int                     `json:"primary"`
	AuthString string                  `json:"authString,omitempty"`
	Secret     string                  `json:"secret,omitempty"`
}

var msRedisPlaneRecords sim.Store[msRedisPlaneRecord]

// msRedisPlaneSpec is how a resource's engine runs, which the resource's
// configuration decides.
type msRedisPlaneSpec struct {
	Cluster     bool
	Image       string
	Configs     [][2]string
	Persistence msRedisPersistence
	// CA names the certificate authority whose certificates the endpoints
	// present; empty serves plaintext.
	CA string
	// IAMAuth exchanges a verified access token for the engine's credential.
	IAMAuth bool
	// TokenAuth runs the cluster's token-auth users as engine users.
	TokenAuth bool
	// AclPolicy names the ACL policy whose rules define the cluster's other
	// engine users.
	AclPolicy string
}

type msRedisNode struct {
	index    int
	announce string
	handle   *sim.ContainerHandle
	hostPort int
	ip       string
}

type msRedisPlane struct {
	name    string
	volume  string
	cluster bool
	labels  map[string]string

	mu          sync.RWMutex
	image       string
	password    string
	configs     [][2]string
	persistence msRedisPersistence
	primary     int
	shards      int
	replicas    int
	nodes       map[int]*msRedisNode
	endpoints   []msRedisEndpointRecord
	listeners   map[string]net.Listener
	tlsConfig   *tls.Config
	iamAuth     bool
	tokenAuth   bool
	aclPolicy   string
	// policyUsers are the engine users the applied ACL policy defines, keyed
	// by lowercased username, which an IAM principal of that email becomes.
	policyUsers map[string]string
	snapshots   *time.Timer
	closed      bool

	startMu   sync.Mutex
	attempted bool
	startErr  error

	// opMu serialises the operations that move a snapshot in or out or change
	// the topology.
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

// msRedisScopedName names an engine object of a resource after the resource
// and the simulator that owns it, so two simulators serving a resource of the
// same name never share a volume or a network alias.
func msRedisScopedName(name string) string {
	digest := sha256.Sum256([]byte(sim.WorkloadScope() + "\x00" + name))
	return "sockerless-memorystore-" + hex.EncodeToString(digest[:8])
}

// msRedisNetworkName is the network every Memorystore engine of this
// simulator joins.
func msRedisNetworkName() string {
	digest := sha256.Sum256([]byte(sim.WorkloadScope()))
	return "sockerless-memorystore-" + hex.EncodeToString(digest[:6])
}

func msRedisNodeDir(node int) string {
	return fmt.Sprintf("%s/node-%d", msRedisDataPath, node)
}

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

// msRedisNewPlane builds the plane serving a resource from its record.
func msRedisNewPlane(name string, spec msRedisPlaneSpec, record msRedisPlaneRecord) (*msRedisPlane, error) {
	plane := &msRedisPlane{
		name:        name,
		volume:      msRedisScopedName(name),
		cluster:     spec.Cluster,
		labels:      map[string]string{"sockerless-memorystore": name},
		image:       spec.Image,
		configs:     spec.Configs,
		persistence: spec.Persistence,
		primary:     record.Primary,
		shards:      record.Shards,
		replicas:    record.Replicas,
		nodes:       map[int]*msRedisNode{},
		listeners:   map[string]net.Listener{},
		iamAuth:     spec.IAMAuth,
		tokenAuth:   spec.TokenAuth,
		aclPolicy:   spec.AclPolicy,
	}
	plane.password = record.AuthString
	if spec.Cluster {
		plane.password = record.Secret
	}
	announce := map[int]string{}
	for _, endpoint := range record.Endpoints {
		if endpoint.Role == msRedisNodeEndpoint {
			announce[endpoint.Node] = endpoint.Address
		}
	}
	for _, index := range record.Nodes {
		plane.nodes[index] = &msRedisNode{index: index, announce: announce[index]}
	}
	plane.endpoints = append(plane.endpoints, record.Endpoints...)
	if spec.CA != "" {
		config, err := msRedisServerTLSConfig(spec.CA)
		if err != nil {
			return nil, err
		}
		plane.tlsConfig = config
	}
	msRedisPlanes.Store(name, plane)
	plane.scheduleSnapshots()
	return plane, nil
}

// record is the plane's part of the resource's record as it now stands.
func (p *msRedisPlane) record(base msRedisPlaneRecord) msRedisPlaneRecord {
	p.mu.RLock()
	defer p.mu.RUnlock()
	base.Endpoints = append([]msRedisEndpointRecord(nil), p.endpoints...)
	base.Nodes = p.nodeOrderLocked()
	base.Primary = p.primary
	base.Shards, base.Replicas = p.shards, p.replicas
	return base
}

func (p *msRedisPlane) nodeOrderLocked() []int {
	order := make([]int, 0, len(p.nodes))
	for index := range p.nodes {
		order = append(order, index)
	}
	sort.Ints(order)
	return order
}

func (p *msRedisPlane) nodeOrder() []int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.nodeOrderLocked()
}

// nextNodeIndexLocked is the index a new node takes: one past every index in
// use.
func (p *msRedisPlane) nextNodeIndexLocked() int {
	next := 0
	for index := range p.nodes {
		if index >= next {
			next = index + 1
		}
	}
	return next
}

func (p *msRedisPlane) alias(node int) string {
	return p.volume + "-node-" + strconv.Itoa(node)
}

// script is a node container's command. A replica of an instance starts
// empty, since it resynchronises from its primary; a cluster node keeps its
// nodes.conf, which is its identity in the cluster, and keeps only the data
// files the configured persistence writes.
func (p *msRedisPlane) script(node int) string {
	p.mu.RLock()
	primary, persistence := p.primary, p.persistence
	p.mu.RUnlock()
	dir := msRedisNodeDir(node)
	var b strings.Builder
	b.WriteString("set -e\n")
	switch {
	case !p.cluster && node != primary:
		fmt.Fprintf(&b, "rm -rf %s\n", dbengine.ShellQuote(dir))
	case p.cluster:
		if persistence.Mode != msRedisPersistenceAOF {
			fmt.Fprintf(&b, "rm -rf %s %s\n", dbengine.ShellQuote(dir+"/appendonlydir"), dbengine.ShellQuote(dir+"/appendonly.aof"))
		}
		if persistence.Mode == msRedisPersistenceDisabled {
			fmt.Fprintf(&b, "rm -f %s\n", dbengine.ShellQuote(dir+"/dump.rdb"))
		}
	}
	fmt.Fprintf(&b, "mkdir -p %s\n", dbengine.ShellQuote(dir))
	b.WriteString("exec ")
	b.WriteString(p.serverCommand(node))
	b.WriteString("\n")
	return b.String()
}

func (p *msRedisPlane) serverCommand(node int) string {
	p.mu.RLock()
	primary, password, configs, persistence := p.primary, p.password, p.configs, p.persistence
	announce := ""
	if n, ok := p.nodes[node]; ok {
		announce = n.announce
	}
	p.mu.RUnlock()
	args := []string{"redis-server",
		"--port", strconv.Itoa(msRedisPort),
		"--bind", "0.0.0.0",
		"--protected-mode", "no",
		"--dir", msRedisNodeDir(node),
		"--save", "",
	}
	args = append(args, persistence.engineArgs()...)
	if password != "" {
		args = append(args, "--requirepass", password, "--masterauth", password)
	}
	if p.cluster {
		args = append(args,
			"--cluster-enabled", "yes",
			"--cluster-config-file", msRedisNodeDir(node)+"/nodes.conf",
			"--cluster-announce-hostname", announce,
			"--cluster-preferred-endpoint-type", "hostname",
		)
	} else if node != primary {
		// slaveof is the spelling every supported version accepts.
		args = append(args, "--slaveof", p.alias(primary), strconv.Itoa(msRedisPort))
	}
	for _, directive := range configs {
		args = append(args, "--"+directive[0], directive[1])
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = dbengine.ShellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

// serve relays every connection listener accepts to the node target names.
func (p *msRedisPlane) serve(key string, listener net.Listener, target func() (int, bool)) {
	p.mu.Lock()
	p.listeners[key] = listener
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

// stopServing closes the listener serving key.
func (p *msRedisPlane) stopServing(key string) {
	p.mu.Lock()
	listener := p.listeners[key]
	delete(p.listeners, key)
	p.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
}

// Primary is the node that takes an instance's writes.
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
	order := p.nodeOrderLocked()
	p.mu.RUnlock()
	replicas := make([]int, 0, len(order))
	for _, node := range order {
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

func (p *msRedisPlane) nodeAddress(node int) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n, ok := p.nodes[node]
	if !ok || n.handle == nil {
		return "", fmt.Errorf("node %d of the Redis engine is not running", node)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(n.hostPort)), nil
}

func (p *msRedisPlane) containerID(node int) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n, ok := p.nodes[node]
	if !ok || n.handle == nil {
		return "", fmt.Errorf("node %d of the Redis engine is not running", node)
	}
	return n.handle.ContainerID, nil
}

func (p *msRedisPlane) nodeIP(node int) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n, ok := p.nodes[node]
	if !ok || n.ip == "" {
		return "", fmt.Errorf("node %d of the Redis engine has no network address", node)
	}
	return n.ip, nil
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

func (p *msRedisPlane) running() bool {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	return p.attempted && p.startErr == nil
}

func (p *msRedisPlane) bringUp() error {
	var missing []int
	for _, node := range p.nodeOrder() {
		p.mu.RLock()
		started := p.nodes[node].handle != nil
		p.mu.RUnlock()
		if !started {
			missing = append(missing, node)
		}
	}
	if err := p.startNodes(missing); err != nil {
		_ = p.stopEngine()
		return err
	}
	if err := p.settle(); err != nil {
		_ = p.stopEngine()
		return err
	}
	return nil
}

// startNodes starts a container for each node and waits until every one
// answers.
func (p *msRedisPlane) startNodes(nodes []int) error {
	if len(nodes) == 0 {
		return nil
	}
	if _, err := sim.EnsureDockerNetwork(msRedisNetworkName()); err != nil {
		return fmt.Errorf("start the Redis engine: %w", err)
	}
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if errs[i] = p.startNode(node); errs[i] == nil {
				errs[i] = p.awaitNode(node)
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (p *msRedisPlane) startNode(node int) error {
	network := msRedisNetworkName()
	p.mu.RLock()
	image := p.image
	p.mu.RUnlock()
	labels := map[string]string{"sockerless-memorystore-node": strconv.Itoa(node)}
	for key, value := range p.labels {
		labels[key] = value
	}
	handle, err := sim.StartContainerSyncContext(context.Background(), sim.ContainerConfig{
		CancelGracePeriod: msRedisStopGrace,
		Image:             image,
		Architecture:      msRedisEnginePlatform,
		Command:           []string{"/bin/sh", "-c", p.script(node)},
		PublishPorts:      []int{msRedisPort},
		Binds:             []string{p.volume + ":" + msRedisDataPath},
		Labels:            labels,
		Network:           network,
		NetworkAliases:    []string{p.alias(node)},
		Sandbox:           SandboxCloudRun,
	}, sim.NoopSink{})
	if err != nil {
		return fmt.Errorf("start node %d of the Redis engine: %w", node, err)
	}
	if err := p.attachNode(node, handle); err != nil {
		handle.Cancel()
		_ = handle.Wait()
		return fmt.Errorf("start node %d of the Redis engine: %w", node, err)
	}
	return nil
}

func (p *msRedisPlane) attachNode(node int, handle *sim.ContainerHandle) error {
	hostPort, err := handle.PublishedPort(context.Background(), msRedisPort)
	if err != nil {
		return err
	}
	ip := sim.ContainerIPv4(handle.ContainerID)
	if ip == "" {
		return fmt.Errorf("container %s has no network address", handle.ContainerID)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	n, ok := p.nodes[node]
	if !ok {
		return fmt.Errorf("node %d is not part of the engine", node)
	}
	n.handle, n.hostPort, n.ip = handle, hostPort, ip
	return nil
}

// awaitNode waits until the node answers PING with its dataset loaded. A node
// still loading a snapshot answers LOADING; one whose container stopped never
// will, so the wait ends there instead of at the budget.
func (p *msRedisPlane) awaitNode(node int) error {
	containerID, err := p.containerID(node)
	if err != nil {
		return err
	}
	address, err := p.nodeAddress(node)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(msRedisReadyBudget)
	nextLiveness := time.Now().Add(msRedisLivenessEvery)
	for !msRedisAnswers(address, p.currentPassword()) {
		now := time.Now()
		if !now.Before(nextLiveness) {
			if !sim.ContainerRunning(containerID) {
				return fmt.Errorf("node %d of the Redis engine stopped before accepting connections: container %s is not running", node, containerID)
			}
			nextLiveness = now.Add(msRedisLivenessEvery)
		}
		if !now.Before(deadline) {
			return fmt.Errorf("node %d of the Redis engine did not become ready within %s", node, msRedisReadyBudget)
		}
		time.Sleep(msRedisProbeInterval)
	}
	return nil
}

func (p *msRedisPlane) currentPassword() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.password
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
// replication for an instance, slots, replicas and users for a cluster. An
// instance without persistence then removes a snapshot staged for its primary
// to load, so a later restart does not bring that dataset back.
func (p *msRedisPlane) settle() error {
	if p.cluster {
		if err := p.formCluster(); err != nil {
			return fmt.Errorf("form the Redis cluster: %w", err)
		}
		return p.applyUsers(p.nodeOrder())
	}
	if err := p.awaitReplication(); err != nil {
		return err
	}
	p.mu.RLock()
	primary, persistence := p.primary, p.persistence
	p.mu.RUnlock()
	if persistence.Mode != msRedisPersistenceDisabled {
		return nil
	}
	_, err := p.exec(primary, []string{"rm", "-f", msRedisNodeDir(primary) + "/dump.rdb"}, nil)
	return err
}

// awaitReplication waits until every replica's link to the primary is up and
// its initial synchronisation is over.
func (p *msRedisPlane) awaitReplication() error {
	primary := p.Primary()
	for _, node := range p.nodeOrder() {
		if node == primary {
			continue
		}
		if err := p.awaitSynchronised(node); err != nil {
			return err
		}
	}
	return nil
}

func (p *msRedisPlane) awaitSynchronised(node int) error {
	if err := p.awaitInfo(node, "replication", func(info map[string]string) bool {
		return info["master_link_status"] == "up" && info["master_sync_in_progress"] == "0"
	}); err != nil {
		return fmt.Errorf("replica %d did not synchronise: %w", node, err)
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
// attaches each replica to its primary. Nodes that kept their identity across
// a restart already hold their slots and roles; meeting each at its current
// address is all they need.
func (p *msRedisPlane) formCluster() error {
	order := p.nodeOrder()
	p.mu.RLock()
	shards, replicas := p.shards, p.replicas
	p.mu.RUnlock()
	if len(order) != shards*(replicas+1) {
		return fmt.Errorf("%d nodes cannot form %d shards of %d replicas", len(order), shards, replicas)
	}
	reply, err := p.command(order[0], "CLUSTER", "INFO")
	if err != nil {
		return err
	}
	formed := msRedisParseInfo(fmt.Sprint(reply))["cluster_slots_assigned"] == strconv.Itoa(msRedisClusterSlots)
	for _, node := range order {
		if err := p.meet(node, order); err != nil {
			return err
		}
	}
	if formed {
		return p.awaitClusterState(shards, replicas)
	}
	ids := make(map[int]string, len(order))
	for _, node := range order {
		id, err := p.nodeID(node)
		if err != nil {
			return err
		}
		ids[node] = id
	}
	primaries := order[:shards]
	for shard, node := range primaries {
		first, last := msRedisSlotRange(shard, shards)
		if _, err := p.command(node, "CLUSTER", "ADDSLOTSRANGE", strconv.Itoa(first), strconv.Itoa(last)); err != nil {
			return err
		}
	}
	for i, node := range order[shards:] {
		if err := p.follow(node, ids[primaries[i/max(replicas, 1)]]); err != nil {
			return err
		}
	}
	return p.awaitClusterState(shards, replicas)
}

// msRedisSlotRange is the hash-slot range shard s of n owns, the slots split
// evenly.
func msRedisSlotRange(shard, shards int) (first, last int) {
	first = shard * msRedisClusterSlots / shards
	last = (shard+1)*msRedisClusterSlots/shards - 1
	return first, last
}

// meet introduces nodes to via at their current network addresses. Meeting
// a node from every other one, rather than leaving gossip to spread it, has
// every node know it at once.
func (p *msRedisPlane) meet(via int, nodes []int) error {
	for _, node := range nodes {
		if node == via {
			continue
		}
		ip, err := p.nodeIP(node)
		if err != nil {
			return err
		}
		if _, err := p.command(via, "CLUSTER", "MEET", ip, strconv.Itoa(msRedisPort)); err != nil {
			return err
		}
	}
	return nil
}

func (p *msRedisPlane) nodeID(node int) (string, error) {
	reply, err := p.command(node, "CLUSTER", "MYID")
	if err != nil {
		return "", err
	}
	return fmt.Sprint(reply), nil
}

// follow makes node a replica of the primary with primaryID, once node has
// learned of that primary through the cluster bus.
func (p *msRedisPlane) follow(node int, primaryID string) error {
	deadline := time.Now().Add(msRedisCommandDeadline)
	for {
		view, err := p.clusterView(node)
		if err != nil {
			return err
		}
		if known, ok := view.byID(primaryID); ok && known.primary() {
			break
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("node %d did not learn of primary %s within %s", node, primaryID, msRedisCommandDeadline)
		}
		time.Sleep(msRedisProbeInterval)
	}
	_, err := p.command(node, "CLUSTER", "REPLICATE", primaryID)
	return err
}

// awaitClusterState waits until every node reports the cluster ok and sees
// every primary and replica in its role, so the topology a client reads from
// any node is the whole one, and every replica has synchronised.
func (p *msRedisPlane) awaitClusterState(shards, replicas int) error {
	order := p.nodeOrder()
	want := strconv.Itoa(len(order))
	var followers []int
	for _, node := range order {
		deadline := time.Now().Add(msRedisCommandDeadline)
		for {
			reply, err := p.command(node, "CLUSTER", "INFO")
			if err != nil {
				return err
			}
			info := msRedisParseInfo(fmt.Sprint(reply))
			view, err := p.clusterView(node)
			if err != nil {
				return err
			}
			primaries, replicaCount := view.countRoles()
			if info["cluster_state"] == "ok" && info["cluster_known_nodes"] == want &&
				primaries == shards && replicaCount == shards*replicas {
				if self, ok := view.self(); ok && !self.primary() {
					followers = append(followers, node)
				}
				break
			}
			if !time.Now().Before(deadline) {
				return fmt.Errorf("node %d reported cluster_state %q with %s known nodes, %d primaries and %d replicas after %s",
					node, info["cluster_state"], info["cluster_known_nodes"], primaries, replicaCount, msRedisCommandDeadline)
			}
			time.Sleep(msRedisProbeInterval)
		}
	}
	for _, node := range followers {
		if err := p.awaitSynchronised(node); err != nil {
			return err
		}
	}
	return nil
}

// msRedisClusterNode is one line of a CLUSTER NODES reply.
type msRedisClusterNode struct {
	ID        string
	IP        string
	Hostname  string
	Flags     []string
	PrimaryID string
	Connected bool
	Slots     []string
}

func (n msRedisClusterNode) has(flag string) bool {
	for _, f := range n.Flags {
		if f == flag {
			return true
		}
	}
	return false
}

func (n msRedisClusterNode) primary() bool { return n.has("master") }

type msRedisClusterView []msRedisClusterNode

func msRedisParseClusterNodes(text string) msRedisClusterView {
	var view msRedisClusterView
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
		node := msRedisClusterNode{
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

func (v msRedisClusterView) byID(id string) (msRedisClusterNode, bool) {
	for _, node := range v {
		if node.ID == id {
			return node, true
		}
	}
	return msRedisClusterNode{}, false
}

func (v msRedisClusterView) self() (msRedisClusterNode, bool) {
	for _, node := range v {
		if node.has("myself") {
			return node, true
		}
	}
	return msRedisClusterNode{}, false
}

// countRoles counts the connected primaries and replicas.
func (v msRedisClusterView) countRoles() (primaries, replicas int) {
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

func (p *msRedisPlane) clusterView(node int) (msRedisClusterView, error) {
	reply, err := p.command(node, "CLUSTER", "NODES")
	if err != nil {
		return nil, err
	}
	return msRedisParseClusterNodes(fmt.Sprint(reply)), nil
}

// shardMap is the cluster's shards as node indices: each primary, ordered by
// the first slot it owns, and the replicas following each.
func (p *msRedisPlane) shardMap() (primaries []int, followers map[int][]int, err error) {
	order := p.nodeOrder()
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
		if entry.primary() {
			primaries = append(primaries, node)
			firstSlot[node] = msRedisClusterSlots
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

// command runs one Redis command on a node through its published port.
func (p *msRedisPlane) command(node int, args ...string) (any, error) {
	address, err := p.nodeAddress(node)
	if err != nil {
		return nil, err
	}
	conn, err := msRedisDial(address, p.currentPassword(), 5*time.Second)
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

// everyNode runs one command on every node.
func (p *msRedisPlane) everyNode(args ...string) error {
	for _, node := range p.nodeOrder() {
		if _, err := p.command(node, args...); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot has node write its dataset as an RDB file with SAVE and returns the
// file's bytes. Without RDB persistence the file does not outlive the call.
func (p *msRedisPlane) Snapshot(node int) ([]byte, error) {
	if _, err := p.command(node, "SAVE"); err != nil {
		return nil, err
	}
	path := msRedisNodeDir(node) + "/dump.rdb"
	data, err := p.readFile(node, path)
	if err != nil {
		return nil, err
	}
	p.mu.RLock()
	keep := p.persistence.Mode == msRedisPersistenceRDB
	p.mu.RUnlock()
	if !keep {
		if _, err := p.exec(node, []string{"rm", "-f", path}, nil); err != nil {
			return nil, err
		}
	}
	return data, nil
}

// Restart replaces an instance's running engine with a fresh one on the same
// volume. With rdb, the primary loads it as its dataset; without, it saves its
// own first, so the new engine — on image, when one is named — holds what the
// old one held.
func (p *msRedisPlane) Restart(rdb []byte, image string) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	primary := p.Primary()
	path := msRedisNodeDir(primary) + "/dump.rdb"
	if rdb != nil {
		if err := p.writeFile(primary, path, rdb); err != nil {
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

// Failover promotes an instance's node to primary: it stops replicating, and
// every other node, the old primary included, replicates from it.
func (p *msRedisPlane) Failover(node int) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	if _, err := p.command(node, "SLAVEOF", "NO", "ONE"); err != nil {
		return err
	}
	for _, other := range p.nodeOrder() {
		if other == node {
			continue
		}
		if _, err := p.command(other, "SLAVEOF", p.alias(node), strconv.Itoa(msRedisPort)); err != nil {
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
	p.mu.Unlock()
	if !p.running() {
		return nil
	}
	for _, directive := range directives {
		if err := p.everyNode("CONFIG", "SET", directive[0], directive[1]); err != nil {
			return err
		}
	}
	return nil
}

// SetPassword changes the AUTH string every node requires and replicates
// with.
func (p *msRedisPlane) SetPassword(password string) error {
	if p.running() {
		for _, node := range p.nodeOrder() {
			if _, err := p.command(node, "CONFIG", "SET", "masterauth", password); err != nil {
				return err
			}
			if _, err := p.command(node, "CONFIG", "SET", "requirepass", password); err != nil {
				return err
			}
		}
	}
	p.mu.Lock()
	p.password = password
	p.mu.Unlock()
	return nil
}

func (p *msRedisPlane) readFile(node int, path string) ([]byte, error) {
	containerID, err := p.containerID(node)
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

func (p *msRedisPlane) writeFile(node int, path string, data []byte) error {
	containerID, err := p.containerID(node)
	if err != nil {
		return err
	}
	dir, file := path[:strings.LastIndex(path, "/")], path[strings.LastIndex(path, "/")+1:]
	if _, err := p.exec(node, []string{"mkdir", "-p", dir}, nil); err != nil {
		return err
	}
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

// exec runs a command in a node's container and returns its output.
func (p *msRedisPlane) exec(node int, command []string, env []string) (string, error) {
	containerID, err := p.containerID(node)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), msRedisReshardDeadline)
	defer cancel()
	docker := sim.DockerClient()
	created, err := docker.ExecCreate(ctx, containerID, dockerclient.ExecCreateOptions{
		Cmd: command, Env: env, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("create engine command: %w", err)
	}
	attached, err := docker.ExecAttach(ctx, created.ID, dockerclient.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("attach engine command: %w", err)
	}
	var output bytes.Buffer
	_, readErr := stdcopy.StdCopy(&output, &output, attached.Reader)
	attached.Close()
	if readErr != nil {
		return "", fmt.Errorf("read engine command output: %w", readErr)
	}
	inspected, err := docker.ExecInspect(ctx, created.ID, dockerclient.ExecInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("inspect engine command: %w", err)
	}
	if inspected.ExitCode != 0 {
		return output.String(), fmt.Errorf("%s exited %d: %s", command[0], inspected.ExitCode, strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}

// Adopt picks up the node containers an earlier control-plane process left
// for this resource. A node whose container stopped starts afresh at the next
// Ensure, from the configuration the resource has now.
func (p *msRedisPlane) Adopt() error {
	existing, err := sim.FindExistingContainers(p.labels)
	if err != nil {
		return err
	}
	for _, found := range existing {
		node, err := strconv.Atoi(found.Labels["sockerless-memorystore-node"])
		p.mu.RLock()
		_, known := p.nodes[node]
		p.mu.RUnlock()
		if err != nil || !known || !found.Running {
			if err := sim.RemoveExistingContainer(found.ID); err != nil {
				return fmt.Errorf("remove Redis engine container %s: %w", found.ID, err)
			}
			continue
		}
		handle, err := sim.AdoptContainer(found.ID, sim.ContainerConfig{CancelGracePeriod: msRedisStopGrace}, sim.NoopSink{})
		if err != nil {
			return err
		}
		if err := p.attachNode(node, handle); err != nil {
			handle.Cancel()
			_ = handle.Wait()
			return fmt.Errorf("adopt the Redis engine container %s: %w", found.ID, err)
		}
	}
	return nil
}

// Stop stops the engine and forgets the last start's outcome, so the next
// client starts a fresh engine on the same volume. It returns once every
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
	var errs []error
	for _, node := range p.nodeOrder() {
		errs = append(errs, p.stopNode(node))
	}
	return errors.Join(errs...)
}

func (p *msRedisPlane) stopNode(node int) error {
	p.mu.Lock()
	var handle *sim.ContainerHandle
	if n, ok := p.nodes[node]; ok {
		handle = n.handle
		n.handle, n.hostPort, n.ip = nil, 0, ""
	}
	p.mu.Unlock()
	if handle == nil {
		return nil
	}
	handle.Cancel()
	_ = handle.Wait()
	return sim.WaitContainerRemoved(handle.ContainerID, msRedisRemovalTimeout)
}

// Close stops accepting clients, stops snapshots and stops the engine; the
// volume stays.
func (p *msRedisPlane) Close() error {
	p.mu.Lock()
	listeners := p.listeners
	p.listeners = map[string]net.Listener{}
	if p.snapshots != nil {
		p.snapshots.Stop()
	}
	p.closed = true
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
	if _, err := c.Write(msRedisEncodeCommand(args)); err != nil {
		return nil, err
	}
	return msRedisReadReply(c.reader)
}

func msRedisEncodeCommand(args []string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(arg), arg)
	}
	return b.Bytes()
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
