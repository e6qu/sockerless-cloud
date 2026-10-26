// Package cacheengine runs the data plane of a managed in-memory cache: real
// Redis-protocol engines (Redis OSS, or Valkey through the same command
// names) and Memcached engines, one workload per node on a placement backend,
// behind endpoint listeners the simulator owns.
//
// A Redis plane runs one of two topologies: a primary with zero or more
// replicas following it by network alias, or a Redis Cluster of shards whose
// nodes announce the endpoint the simulator serves them at, so a cluster
// client follows redirections to addresses it can reach. Redis itself answers
// AUTH, replication and cluster redirections; an endpoint terminates TLS when
// the resource encrypts in transit and, through a hook, can exchange a
// credential the cloud verified for the engine's own. A Memcached plane runs
// independent nodes, each served at an endpoint of its own.
//
// Which image runs, which network the nodes join, what the endpoints present
// as certificates, which users the engine holds and what the control plane
// records stay in each cloud's module and reach the plane through its Config.
package cacheengine

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/signals"
)

const (
	// Port is the port every Redis node listens on inside its workload.
	Port            = 6379
	dataPath        = "/data"
	readyBudget     = 10 * time.Minute
	stopGrace       = 5 * time.Second
	removalTimeout  = 30 * time.Second
	commandDeadline = 2 * time.Minute
	reshardDeadline = 10 * time.Minute
)

// Role says which node an endpoint relays to.
type Role string

const (
	PrimaryEndpoint   Role = "primary"
	ReadEndpoint      Role = "read"
	DiscoveryEndpoint Role = "discovery"
	NodeEndpoint      Role = "node"
)

// Endpoint is one address the plane serves.
type Endpoint struct {
	Role    Role   `json:"role"`
	Address string `json:"address"`
	Port    int    `json:"port,omitempty"`
	Node    int    `json:"node,omitempty"`
	// Plaintext serves the endpoint without TLS on a plane that terminates
	// TLS at its other endpoints.
	Plaintext bool `json:"plaintext,omitempty"`
}

// PortOrDefault is the endpoint's port, Redis's own when it records none.
func (e Endpoint) PortOrDefault() int {
	if e.Port == 0 {
		return Port
	}
	return e.Port
}

// Key names the endpoint among the plane's listeners.
func (e Endpoint) Key() string {
	key := string(e.Role)
	if e.Role == NodeEndpoint {
		key = "node-" + strconv.Itoa(e.Node)
	}
	if e.Plaintext {
		key += "-plaintext"
	}
	return key
}

// Record is what a control-plane restart needs to serve a resource's
// endpoints again: the addresses it reported, the nodes the engine runs, its
// shape and the node that is primary.
type Record struct {
	Endpoints []Endpoint `json:"endpoints,omitempty"`
	Nodes     []int      `json:"nodes,omitempty"`
	Shards    int        `json:"shards,omitempty"`
	Replicas  int        `json:"replicas,omitempty"`
	Primary   int        `json:"primary"`
}

// Persistence is what the engine keeps of its dataset across a restart.
type Persistence struct {
	// AOF runs the append-only file, fsynced as AppendFsync ("no",
	// "everysec" or "always") says.
	AOF         bool
	AppendFsync string
	// RDB keeps the RDB file a snapshot writes.
	RDB bool
}

func (p Persistence) disabled() bool { return !p.AOF && !p.RDB }

// engineArgs are the redis-server directives the persistence runs with. RDB
// snapshots are the service's own schedule, not the engine's save points.
func (p Persistence) engineArgs() []string {
	if !p.AOF {
		return []string{"--appendonly", "no"}
	}
	return []string{"--appendonly", "yes", "--appendfsync", p.AppendFsync}
}

// Config is how a resource's Redis engine runs.
type Config struct {
	// Name prefixes the plane's log lines, e.g. "Memorystore projects/p/…",
	// and LogTag the framework's, as "[sim-<tag>]".
	Name   string
	LogTag string
	// Owner holds the loopback addresses of the plane's endpoints, through
	// Identifier.
	Owner   string
	Cluster bool
	Image   string
	// Platform is the platform the engine runs on whatever the host is.
	Platform string
	// Volume holds every node's directory; it also prefixes the nodes'
	// network aliases.
	Volume string
	// Labels identify the resource's workloads; NodeLabel is the label that
	// carries a node's index.
	Labels    map[string]string
	NodeLabel string
	Backend   func() (placement.Backend, error)
	// Network ensures the engine network the nodes join and returns its name
	// and the resolvers the nodes ask.
	Network func(ctx context.Context) (string, []string, error)
	Sandbox sim.SandboxProfile
	// Password is what every node requires of clients and replicates with.
	Password    string
	Configs     [][2]string
	Persistence Persistence
	// TLS terminates TLS at every endpoint but a Plaintext one; nil serves
	// plaintext. TLSPreferred accepts a client that opens without TLS as well.
	TLS          *tls.Config
	TLSPreferred bool
	// ReadFromPrimaryWithoutReplicas has the read endpoint of an instance
	// that runs no replica reach its primary.
	ReadFromPrimaryWithoutReplicas bool
	// ExchangeCredential, when set, has the endpoints read every command and
	// rewrite an AUTH, or a HELLO that authenticates, whose password it
	// accepts into one for the engine user it names carrying Password.
	ExchangeCredential func(password string) (user string, ok bool)
	// Users are the engine users every node runs, the default user among
	// them when one is named so; UsersApplied runs once every node holds
	// them. Nil leaves the engine's users as the engine starts them.
	Users        func() ([]User, error)
	UsersApplied func([]User)
	// Started runs each time the engine comes up and settles.
	Started func()
}

type node struct {
	index    int
	announce string
	handle   placement.Handle
	output   *nodeOutput
	hostPort int
	ip       string
	// seed is a dataset the node loads at its next start.
	seed []byte
}

// nodeOutput reads what a node's redis-server writes: each line announces the
// node on its plane's changes, and the line redis-server writes once its
// dataset is loaded and it accepts clients marks it ready. Every supported
// version spells that line with "ready to accept connections".
type nodeOutput struct {
	changes *signals.Keyed[int]
	node    int
	ready   atomic.Bool
}

func (o *nodeOutput) WriteLog(line sim.LogLine) {
	if strings.Contains(strings.ToLower(line.Text), "ready to accept connections") {
		o.ready.Store(true)
	}
	o.changes.Announce(o.node)
}

// Plane is one resource's Redis engine and the endpoints in front of it.
type Plane struct {
	cfg Config

	mu    sync.RWMutex
	image string
	// network and dns are the engine network the nodes join and the
	// resolvers they ask.
	network     string
	dns         []string
	password    string
	configs     [][2]string
	persistence Persistence
	primary     int
	shards      int
	replicas    int
	nodes       map[int]*node
	endpoints   []Endpoint
	listeners   map[string]net.Listener
	tlsConfig   *tls.Config

	startMu   sync.Mutex
	attempted bool
	startErr  error

	readCursor atomic.Uint64

	// changes announces a node each time its engine writes a line;
	// redis-server logs every change of its replication link and of the
	// cluster's state.
	changes signals.Keyed[int]
}

// New builds the plane serving a resource from its record.
func New(cfg Config, record Record) *Plane {
	p := &Plane{
		cfg:         cfg,
		image:       cfg.Image,
		password:    cfg.Password,
		configs:     cfg.Configs,
		persistence: cfg.Persistence,
		primary:     record.Primary,
		shards:      record.Shards,
		replicas:    record.Replicas,
		nodes:       map[int]*node{},
		listeners:   map[string]net.Listener{},
		tlsConfig:   cfg.TLS,
	}
	announce := map[int]string{}
	for _, endpoint := range record.Endpoints {
		if endpoint.Role == NodeEndpoint {
			announce[endpoint.Node] = endpoint.Address
		}
	}
	for _, index := range record.Nodes {
		p.nodes[index] = &node{index: index, announce: announce[index]}
	}
	p.endpoints = append(p.endpoints, record.Endpoints...)
	return p
}

// Volume is the volume that holds the plane's data.
func (p *Plane) Volume() string { return p.cfg.Volume }

// Record is the plane's topology as it now stands.
func (p *Plane) Record() Record {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return Record{
		Endpoints: append([]Endpoint(nil), p.endpoints...),
		Nodes:     p.nodeOrderLocked(),
		Primary:   p.primary,
		Shards:    p.shards,
		Replicas:  p.replicas,
	}
}

// Endpoints are the endpoints the plane serves.
func (p *Plane) Endpoints() []Endpoint {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]Endpoint(nil), p.endpoints...)
}

func (p *Plane) nodeOrderLocked() []int {
	order := make([]int, 0, len(p.nodes))
	for index := range p.nodes {
		order = append(order, index)
	}
	sort.Ints(order)
	return order
}

// Nodes are the indices of the plane's nodes in order.
func (p *Plane) Nodes() []int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.nodeOrderLocked()
}

// nextNodeIndexLocked is the index a new node takes: one past every index in
// use.
func (p *Plane) nextNodeIndexLocked() int {
	next := 0
	for index := range p.nodes {
		if index >= next {
			next = index + 1
		}
	}
	return next
}

func (p *Plane) alias(node int) string {
	return p.cfg.Volume + "-node-" + strconv.Itoa(node)
}

func nodeDir(node int) string {
	return fmt.Sprintf("%s/node-%d", dataPath, node)
}

// script is a node container's command. A replica of an instance starts
// empty, since it resynchronises from its primary; a cluster node keeps its
// nodes.conf, which is its identity in the cluster, and keeps only the data
// files the configured persistence writes.
func (p *Plane) script(node int) string {
	p.mu.RLock()
	primary, persistence := p.primary, p.persistence
	p.mu.RUnlock()
	dir := nodeDir(node)
	var b strings.Builder
	b.WriteString("set -e\n")
	switch {
	case !p.cfg.Cluster && node != primary:
		fmt.Fprintf(&b, "rm -rf %s\n", dbengine.ShellQuote(dir))
	case p.cfg.Cluster:
		if !persistence.AOF {
			fmt.Fprintf(&b, "rm -rf %s %s\n", dbengine.ShellQuote(dir+"/appendonlydir"), dbengine.ShellQuote(dir+"/appendonly.aof"))
		}
		if persistence.disabled() {
			fmt.Fprintf(&b, "rm -f %s\n", dbengine.ShellQuote(dir+"/dump.rdb"))
		}
	}
	fmt.Fprintf(&b, "mkdir -p %s\n", dbengine.ShellQuote(dir))
	b.WriteString("exec ")
	b.WriteString(p.serverCommand(node))
	b.WriteString("\n")
	return b.String()
}

func (p *Plane) serverCommand(index int) string {
	p.mu.RLock()
	primary, password, configs, persistence := p.primary, p.password, p.configs, p.persistence
	announce := ""
	if n, ok := p.nodes[index]; ok {
		announce = n.announce
	}
	p.mu.RUnlock()
	args := []string{"redis-server",
		"--port", strconv.Itoa(Port),
		"--bind", "0.0.0.0",
		"--protected-mode", "no",
		"--dir", nodeDir(index),
		"--save", "",
		// Start a replica's full synchronisation when the replica asks,
		// not five seconds later in case another replica joins the transfer:
		// the service attaches its replicas itself and knows of no other.
		"--repl-diskless-sync-delay", "0",
	}
	args = append(args, persistence.engineArgs()...)
	if password != "" {
		args = append(args, "--requirepass", password, "--masterauth", password)
	}
	if p.cfg.Cluster {
		args = append(args,
			"--cluster-enabled", "yes",
			"--cluster-config-file", nodeDir(index)+"/nodes.conf",
			"--cluster-announce-hostname", announce,
			"--cluster-preferred-endpoint-type", "hostname",
		)
	} else if index != primary {
		// slaveof is the spelling every supported version accepts.
		args = append(args, "--slaveof", p.alias(primary), strconv.Itoa(Port))
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

// Primary is the node that takes an instance's writes.
func (p *Plane) Primary() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.primary
}

// Replicas are an instance's nodes other than its primary, in order.
func (p *Plane) Replicas() []int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var replicas []int
	for _, index := range p.nodeOrderLocked() {
		if index != p.primary {
			replicas = append(replicas, index)
		}
	}
	return replicas
}

func (p *Plane) nodeAddress(index int) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n, ok := p.nodes[index]
	if !ok || n.handle == nil {
		return "", fmt.Errorf("node %d of the Redis engine is not running", index)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(n.hostPort)), nil
}

func (p *Plane) workload(index int) (placement.Handle, *nodeOutput, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n, ok := p.nodes[index]
	if !ok || n.handle == nil {
		return nil, nil, fmt.Errorf("node %d of the Redis engine is not running", index)
	}
	return n.handle, n.output, nil
}

// NodeIP is a running node's address on the engine network.
func (p *Plane) NodeIP(index int) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n, ok := p.nodes[index]
	if !ok || n.ip == "" {
		return "", fmt.Errorf("node %d of the Redis engine has no network address", index)
	}
	return n.ip, nil
}

// Ensure brings the engine up and returns once every node answers and the
// topology is in place. The outcome holds until Stop.
func (p *Plane) Ensure() error {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if !p.attempted {
		p.attempted = true
		p.startErr = p.bringUp()
		if p.startErr == nil && p.cfg.Started != nil {
			p.cfg.Started()
		}
	}
	return p.startErr
}

// Running reports whether the engine came up and has not stopped since.
func (p *Plane) Running() bool {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	return p.attempted && p.startErr == nil
}

func (p *Plane) bringUp() error {
	var missing []int
	for _, index := range p.Nodes() {
		p.mu.RLock()
		started := p.nodes[index].handle != nil
		p.mu.RUnlock()
		if !started {
			missing = append(missing, index)
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

func (p *Plane) prepareNetwork() error {
	network, dns, err := p.cfg.Network(context.Background())
	if err != nil {
		return fmt.Errorf("start the Redis engine: %w", err)
	}
	p.mu.Lock()
	p.network, p.dns = network, dns
	p.mu.Unlock()
	return nil
}

// startNodes starts a container for each node and waits until every one
// answers.
func (p *Plane) startNodes(nodes []int) error {
	if len(nodes) == 0 {
		return nil
	}
	if err := p.prepareNetwork(); err != nil {
		return err
	}
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i, index := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if errs[i] = p.startNode(index); errs[i] == nil {
				errs[i] = p.awaitNode(index)
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (p *Plane) startNode(index int) error {
	p.mu.RLock()
	image, network, dns := p.image, p.network, p.dns
	p.mu.RUnlock()
	labels := map[string]string{p.cfg.NodeLabel: strconv.Itoa(index)}
	for key, value := range p.cfg.Labels {
		labels[key] = value
	}
	backend, err := p.cfg.Backend()
	if err != nil {
		return fmt.Errorf("start node %d of the Redis engine: %w", index, err)
	}
	p.mu.Lock()
	var seed []sim.SeedFile
	if n, ok := p.nodes[index]; ok && n.seed != nil {
		seed = []sim.SeedFile{{Path: nodeDir(index) + "/dump.rdb", Data: n.seed}}
		n.seed = nil
	}
	p.mu.Unlock()
	output := &nodeOutput{changes: &p.changes, node: index}
	handle, err := backend.Place(context.Background(), placement.Workload{
		CancelGracePeriod: stopGrace,
		Image:             image,
		Architecture:      p.cfg.Platform,
		Command:           []string{"/bin/sh", "-c", p.script(index)},
		PublishPorts:      []int{Port},
		Binds:             []string{p.cfg.Volume + ":" + dataPath},
		Labels:            labels,
		Network:           network,
		NetworkAliases:    []string{p.alias(index)},
		DNS:               dns,
		Seed:              seed,
		Sandbox:           p.cfg.Sandbox,
	}, output)
	if err != nil {
		return fmt.Errorf("start node %d of the Redis engine: %w", index, err)
	}
	if err := p.attachNode(index, handle, output); err != nil {
		handle.Cancel()
		_ = handle.Wait()
		return fmt.Errorf("start node %d of the Redis engine: %w", index, err)
	}
	return nil
}

func (p *Plane) attachNode(index int, handle placement.Handle, output *nodeOutput) error {
	ctx, cancel := context.WithTimeout(context.Background(), commandDeadline)
	defer cancel()
	hostPort, err := handle.PublishedPort(ctx, Port)
	if err != nil {
		return err
	}
	ip, err := handle.Address(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	n, ok := p.nodes[index]
	if !ok {
		return fmt.Errorf("node %d is not part of the engine", index)
	}
	n.handle, n.output, n.hostPort, n.ip = handle, output, hostPort, ip
	return nil
}

// awaitNode waits until the node logs that it accepts clients, which
// redis-server does once its dataset is loaded, and then confirms that it
// answers PING. A node whose workload exits never will, so the wait ends
// there instead of at the budget.
func (p *Plane) awaitNode(index int) error {
	handle, output, err := p.workload(index)
	if err != nil {
		return err
	}
	address, err := p.nodeAddress(index)
	if err != nil {
		return err
	}
	ctx, cancel := waitContext(handle, readyBudget)
	defer cancel(nil)
	if err := p.changes.Await(ctx, index, output.ready.Load); err != nil {
		if errors.Is(context.Cause(ctx), errNodeExited) {
			result := handle.Wait()
			return fmt.Errorf("node %d of the Redis engine stopped before accepting connections: workload %s exited with status %d (%v)", index, handle.ID(), result.ExitCode, result.Error)
		}
		return fmt.Errorf("node %d of the Redis engine did not become ready within %s", index, readyBudget)
	}
	if !answers(address, p.Password()) {
		return fmt.Errorf("node %d of the Redis engine logged that it accepts connections but does not answer PING at %s", index, address)
	}
	return nil
}

var errNodeExited = errors.New("the node's workload exited")

// waitContext ends after budget, or once the node's workload exits, with
// errNodeExited as its cause.
func waitContext(handle placement.Handle, budget time.Duration) (context.Context, context.CancelCauseFunc) {
	ctx, cancel := context.WithCancelCause(context.Background())
	ctx, stop := context.WithTimeout(ctx, budget)
	go func() {
		select {
		case <-handle.Exited():
			cancel(errNodeExited)
		case <-ctx.Done():
		}
	}()
	return ctx, func(cause error) {
		stop()
		cancel(cause)
	}
}

// awaitEngine reads a node's state with satisfied until it reports true,
// again each time the node writes a line and, when cron is set, once per
// cron period as well. It gives up after commandDeadline or once the node's
// workload exits.
func (p *Plane) awaitEngine(index int, cron bool, satisfied func() (bool, error)) error {
	handle, _, err := p.workload(index)
	if err != nil {
		return err
	}
	var period time.Duration
	if cron {
		if period, err = p.cronPeriod(index); err != nil {
			return err
		}
	}
	ctx, cancel := waitContext(handle, commandDeadline)
	defer cancel(nil)
	for {
		changed := p.changes.Changed(index)
		ok, err := satisfied()
		if err != nil || ok {
			return err
		}
		var tick <-chan time.Time
		var timer *time.Timer
		if cron {
			timer = time.NewTimer(period)
			tick = timer.C
		}
		select {
		case <-changed:
		case <-tick:
		case <-ctx.Done():
		}
		if timer != nil {
			timer.Stop()
		}
		if ctx.Err() != nil {
			if errors.Is(context.Cause(ctx), errNodeExited) {
				return fmt.Errorf("node %d of the Redis engine stopped", index)
			}
			return fmt.Errorf("not settled within %s", commandDeadline)
		}
	}
}

// cronPeriod is how often the node runs its background tasks: once per
// period of its hz, which INFO reports as it currently applies. A cluster
// node pings its peers and acts on what they gossip from those tasks, and
// reports nothing when a peer's view changes, so a wait on the cluster's
// view rereads it at this period.
func (p *Plane) cronPeriod(index int) (time.Duration, error) {
	reply, err := p.Command(index, "INFO", "server")
	if err != nil {
		return 0, err
	}
	text, _ := reply.(string)
	hz, err := strconv.Atoi(ParseInfo(text)["hz"])
	if err != nil || hz <= 0 {
		return 0, fmt.Errorf("node %d reports no hz in INFO server", index)
	}
	return time.Second / time.Duration(hz), nil
}

// Password is the password every node currently requires.
func (p *Plane) Password() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.password
}

func answers(address, password string) bool {
	conn, err := Dial(address, password, 500*time.Millisecond)
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
func (p *Plane) settle() error {
	if p.cfg.Cluster {
		if err := p.formCluster(); err != nil {
			return fmt.Errorf("form the Redis cluster: %w", err)
		}
		return p.ApplyUsers(p.Nodes())
	}
	if err := p.awaitReplication(); err != nil {
		return err
	}
	if err := p.ApplyUsers(p.Nodes()); err != nil {
		return err
	}
	p.mu.RLock()
	primary, persistence := p.primary, p.persistence
	p.mu.RUnlock()
	if !persistence.disabled() {
		return nil
	}
	_, err := p.Exec(primary, []string{"rm", "-f", nodeDir(primary) + "/dump.rdb"}, nil)
	return err
}

// awaitReplication waits until every replica's link to the primary is up and
// its initial synchronisation is over.
func (p *Plane) awaitReplication() error {
	for _, index := range p.Replicas() {
		if err := p.awaitSynchronised(index); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plane) awaitSynchronised(index int) error {
	if err := p.awaitInfo(index, "replication", func(info map[string]string) bool {
		return info["master_link_status"] == "up" && info["master_sync_in_progress"] == "0"
	}); err != nil {
		return fmt.Errorf("replica %d did not synchronise: %w", index, err)
	}
	return nil
}

// awaitInfo waits until an INFO section of node satisfies a condition that
// only changes with a line the node logs: a replication link coming up or
// going down, or a synchronisation finishing.
func (p *Plane) awaitInfo(index int, section string, satisfied func(map[string]string) bool) error {
	err := p.awaitEngine(index, false, func() (bool, error) {
		reply, err := p.Command(index, "INFO", section)
		if err != nil {
			return false, err
		}
		text, _ := reply.(string)
		return satisfied(ParseInfo(text)), nil
	})
	if err != nil {
		return fmt.Errorf("INFO %s: %w", section, err)
	}
	return nil
}

// formCluster joins the nodes, gives each shard primary its slot range and
// attaches each replica to its primary. Nodes that kept their identity across
// a restart already hold their slots and roles; meeting each at its current
// address is all they need.
func (p *Plane) formCluster() error {
	order := p.Nodes()
	p.mu.RLock()
	shards, replicas := p.shards, p.replicas
	p.mu.RUnlock()
	if len(order) != shards*(replicas+1) {
		return fmt.Errorf("%d nodes cannot form %d shards of %d replicas", len(order), shards, replicas)
	}
	reply, err := p.Command(order[0], "CLUSTER", "INFO")
	if err != nil {
		return err
	}
	formed := ParseInfo(fmt.Sprint(reply))["cluster_slots_assigned"] == strconv.Itoa(ClusterSlots)
	for _, index := range order {
		if err := p.meet(index, order); err != nil {
			return err
		}
	}
	if formed {
		return p.awaitClusterState(shards, replicas)
	}
	ids := make(map[int]string, len(order))
	for _, index := range order {
		id, err := p.nodeID(index)
		if err != nil {
			return err
		}
		ids[index] = id
	}
	primaries := order[:shards]
	for shard, index := range primaries {
		first, last := SlotRange(shard, shards)
		if _, err := p.Command(index, "CLUSTER", "ADDSLOTSRANGE", strconv.Itoa(first), strconv.Itoa(last)); err != nil {
			return err
		}
	}
	for i, index := range order[shards:] {
		if err := p.follow(index, ids[primaries[i/max(replicas, 1)]]); err != nil {
			return err
		}
	}
	return p.awaitClusterState(shards, replicas)
}

// meet introduces nodes to via at their current network addresses. Meeting
// a node from every other one, rather than leaving gossip to spread it, has
// every node know it at once.
func (p *Plane) meet(via int, nodes []int) error {
	for _, index := range nodes {
		if index == via {
			continue
		}
		ip, err := p.NodeIP(index)
		if err != nil {
			return err
		}
		if _, err := p.Command(via, "CLUSTER", "MEET", ip, strconv.Itoa(Port)); err != nil {
			return err
		}
	}
	return nil
}

// follow makes node a replica of the primary with primaryID, once node has
// learned of that primary through the cluster bus.
func (p *Plane) follow(index int, primaryID string) error {
	if err := p.awaitEngine(index, true, func() (bool, error) {
		view, err := p.clusterView(index)
		if err != nil {
			return false, err
		}
		known, ok := view.byID(primaryID)
		return ok && known.Primary(), nil
	}); err != nil {
		return fmt.Errorf("node %d did not learn of primary %s: %w", index, primaryID, err)
	}
	_, err := p.Command(index, "CLUSTER", "REPLICATE", primaryID)
	return err
}

// awaitClusterState waits until every node reports the cluster ok and sees
// every primary and replica in its role, so the topology a client reads from
// any node is the whole one, and every replica has synchronised.
func (p *Plane) awaitClusterState(shards, replicas int) error {
	order := p.Nodes()
	// A role or hostname reaches a node in a packet from its owner, and
	// gossip picks the peer it pings at random, up to half the node timeout
	// apart. A MEET from every node to every other one delivers each now.
	for _, index := range order {
		if err := p.meet(index, order); err != nil {
			return err
		}
	}
	want := strconv.Itoa(len(order))
	var followers []int
	for _, index := range order {
		var info map[string]string
		var primaries, replicaCount int
		err := p.awaitEngine(index, true, func() (bool, error) {
			reply, err := p.Command(index, "CLUSTER", "INFO")
			if err != nil {
				return false, err
			}
			info = ParseInfo(fmt.Sprint(reply))
			view, err := p.clusterView(index)
			if err != nil {
				return false, err
			}
			primaries, replicaCount = view.countRoles()
			if info["cluster_state"] == "ok" && info["cluster_known_nodes"] == want &&
				primaries == shards && replicaCount == shards*replicas && view.hostnamesKnown() {
				if self, ok := view.self(); ok && !self.Primary() {
					followers = append(followers, index)
				}
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			return fmt.Errorf("node %d reported cluster_state %q with %s known nodes, %d primaries and %d replicas: %w",
				index, info["cluster_state"], info["cluster_known_nodes"], primaries, replicaCount, err)
		}
	}
	for _, index := range followers {
		if err := p.awaitSynchronised(index); err != nil {
			return err
		}
	}
	return nil
}

// Command runs one Redis command on a node through its published port.
func (p *Plane) Command(index int, args ...string) (any, error) {
	address, err := p.nodeAddress(index)
	if err != nil {
		return nil, err
	}
	conn, err := Dial(address, p.Password(), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to node %d: %w", index, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(commandDeadline)); err != nil {
		return nil, err
	}
	reply, err := conn.Do(args...)
	if err != nil {
		return nil, fmt.Errorf("%s on node %d: %w", strings.Join(args[:min(2, len(args))], " "), index, err)
	}
	return reply, nil
}

// everyNode runs one command on every node.
func (p *Plane) everyNode(args ...string) error {
	for _, index := range p.Nodes() {
		if _, err := p.Command(index, args...); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot has node write its dataset as an RDB file with SAVE and returns the
// file's bytes. Without RDB persistence the file does not outlive the call.
func (p *Plane) Snapshot(index int) ([]byte, error) {
	if _, err := p.Command(index, "SAVE"); err != nil {
		return nil, err
	}
	path := nodeDir(index) + "/dump.rdb"
	data, err := p.ReadFile(index, path)
	if err != nil {
		return nil, err
	}
	p.mu.RLock()
	keep := p.persistence.RDB
	p.mu.RUnlock()
	if !keep {
		if _, err := p.Exec(index, []string{"rm", "-f", path}, nil); err != nil {
			return nil, err
		}
	}
	return data, nil
}

// Restart replaces an instance's running engine with a fresh one on the same
// volume. With rdb, the primary loads it as its dataset; without, it saves its
// own first, so the new engine — on image, when one is named — holds what the
// old one held.
func (p *Plane) Restart(rdb []byte, image string) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	primary := p.Primary()
	if rdb == nil {
		if _, err := p.Command(primary, "SAVE"); err != nil {
			return err
		}
	}
	if image != "" {
		p.mu.Lock()
		p.image = image
		p.mu.Unlock()
	}
	if err := p.Stop(); err != nil {
		return err
	}
	if rdb != nil {
		p.mu.Lock()
		if n, ok := p.nodes[primary]; ok {
			n.seed = rdb
		}
		p.mu.Unlock()
	}
	return p.Ensure()
}

// Reboot restarts nodes in their workloads' place, each keeping only what the
// persistence keeps of its dataset, and returns once the topology has settled
// again.
func (p *Plane) Reboot(nodes []int) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	var errs []error
	for _, index := range nodes {
		errs = append(errs, p.stopNode(index))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if err := p.startNodes(nodes); err != nil {
		return err
	}
	return p.settle()
}

// Failover promotes an instance's node to primary: it stops replicating, and
// every other node, the old primary included, replicates from it.
func (p *Plane) Failover(index int) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	if _, err := p.Command(index, "SLAVEOF", "NO", "ONE"); err != nil {
		return err
	}
	for _, other := range p.Nodes() {
		if other == index {
			continue
		}
		if _, err := p.Command(other, "SLAVEOF", p.alias(index), strconv.Itoa(Port)); err != nil {
			return err
		}
	}
	p.mu.Lock()
	p.primary = index
	p.mu.Unlock()
	return p.awaitReplication()
}

// FailoverShard has a cluster replica take over its primary's slots with
// CLUSTER FAILOVER, which the old primary agrees to, and waits until every
// node sees the new roles.
func (p *Plane) FailoverShard(replica int) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	if _, err := p.Command(replica, "CLUSTER", "FAILOVER"); err != nil {
		return err
	}
	if err := p.awaitEngine(replica, true, func() (bool, error) {
		view, err := p.clusterView(replica)
		if err != nil {
			return false, err
		}
		self, ok := view.self()
		return ok && self.Primary(), nil
	}); err != nil {
		return fmt.Errorf("node %d did not take over its shard: %w", replica, err)
	}
	p.mu.RLock()
	shards, replicas := p.shards, p.replicas
	p.mu.RUnlock()
	return p.awaitClusterState(shards, replicas)
}

// Configure applies directives to every running node with CONFIG SET, as a
// live configuration change does. An engine that is not running takes them at
// its next start.
func (p *Plane) Configure(directives [][2]string) error {
	p.mu.Lock()
	p.configs = directives
	p.mu.Unlock()
	if !p.Running() {
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
func (p *Plane) SetPassword(password string) error {
	if p.Running() {
		for _, index := range p.Nodes() {
			if _, err := p.Command(index, "CONFIG", "SET", "masterauth", password); err != nil {
				return err
			}
			if _, err := p.Command(index, "CONFIG", "SET", "requirepass", password); err != nil {
				return err
			}
		}
	}
	p.mu.Lock()
	p.password = password
	p.mu.Unlock()
	return nil
}

// SetPersistence changes how the engine persists, live: the append-only file
// starts or stops at once.
func (p *Plane) SetPersistence(next Persistence) error {
	p.mu.Lock()
	previous := p.persistence
	p.persistence = next
	p.mu.Unlock()
	if !p.Running() {
		return nil
	}
	switch {
	case next.AOF:
		if err := p.everyNode("CONFIG", "SET", "appendfsync", next.AppendFsync); err != nil {
			return err
		}
		return p.everyNode("CONFIG", "SET", "appendonly", "yes")
	case previous.AOF:
		return p.everyNode("CONFIG", "SET", "appendonly", "no")
	}
	return nil
}

// BackgroundSave has every primary write its RDB snapshot.
func (p *Plane) BackgroundSave() error {
	primaries := []int{p.Primary()}
	if p.cfg.Cluster {
		var err error
		if primaries, _, err = p.ShardMap(); err != nil {
			return err
		}
	}
	for _, index := range primaries {
		if _, err := p.Command(index, "BGSAVE"); err != nil {
			return err
		}
	}
	return nil
}

// ReadFile reads a file of a node. A workload's command output reaches this
// process as text on every placement backend, so the file travels
// base64-encoded.
func (p *Plane) ReadFile(index int, path string) ([]byte, error) {
	encoded, err := p.Exec(index, []string{"base64", path}, nil)
	if err != nil {
		return nil, fmt.Errorf("read %s from the Redis engine: %w", path, err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(encoded), ""))
	if err != nil {
		return nil, fmt.Errorf("read %s from the Redis engine: %w", path, err)
	}
	return data, nil
}

// Exec runs a command in a node's workload and returns what it wrote to its
// standard output. env sets variables for the command alone.
func (p *Plane) Exec(index int, command []string, env []string) (string, error) {
	handle, _, err := p.workload(index)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), reshardDeadline)
	defer cancel()
	name := command[0]
	if len(env) > 0 {
		command = append(append([]string{"env"}, env...), command...)
	}
	result, err := handle.Exec(ctx, command)
	if err != nil {
		return "", fmt.Errorf("run %s on node %d of the Redis engine: %w", name, index, err)
	}
	if result.ExitCode != 0 {
		return result.Stdout, fmt.Errorf("%s exited %d: %s", name, result.ExitCode, strings.TrimSpace(result.Stdout+result.Stderr))
	}
	return result.Stdout, nil
}

// Adopt picks up the node workloads an earlier control-plane process left
// for this resource. A node whose workload stopped starts afresh at the next
// Ensure, from the configuration the resource has now.
func (p *Plane) Adopt() error {
	backend, err := p.cfg.Backend()
	if err != nil {
		return err
	}
	ctx := context.Background()
	existing, err := backend.Find(ctx, p.cfg.Labels)
	if err != nil {
		return err
	}
	for _, found := range existing {
		index, err := strconv.Atoi(found.Labels[p.cfg.NodeLabel])
		p.mu.RLock()
		_, known := p.nodes[index]
		p.mu.RUnlock()
		if err != nil || !known || !found.Running {
			if err := backend.Remove(ctx, found.ID); err != nil {
				return fmt.Errorf("remove Redis engine workload %s: %w", found.ID, err)
			}
			continue
		}
		output := &nodeOutput{changes: &p.changes, node: index}
		handle, err := backend.Adopt(ctx, found.ID, placement.Workload{CancelGracePeriod: stopGrace}, output)
		if err != nil {
			return err
		}
		if err := p.attachNode(index, handle, output); err != nil {
			handle.Cancel()
			_ = handle.Wait()
			return fmt.Errorf("adopt the Redis engine workload %s: %w", found.ID, err)
		}
	}
	return nil
}

// Stop stops the engine and forgets the last start's outcome, so the next
// client starts a fresh engine on the same volume. It returns once every
// container is gone.
func (p *Plane) Stop() error {
	firstErr := p.stopEngine()
	p.startMu.Lock()
	defer p.startMu.Unlock()
	err := p.stopEngine()
	p.attempted, p.startErr = false, nil
	return errors.Join(firstErr, err)
}

func (p *Plane) stopEngine() error {
	order := p.Nodes()
	errs := make([]error, len(order))
	var wg sync.WaitGroup
	for i, index := range order {
		wg.Go(func() { errs[i] = p.stopNode(index) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (p *Plane) stopNode(index int) error {
	p.mu.Lock()
	var handle placement.Handle
	if n, ok := p.nodes[index]; ok {
		handle = n.handle
		n.handle, n.output, n.hostPort, n.ip = nil, nil, 0, ""
	}
	p.mu.Unlock()
	return removeWorkload(p.cfg.Backend, handle)
}

// removeWorkload stops a workload and removes it. The workload's teardown
// removes it as it exits but reports no failure; Remove reports one, and finds
// nothing to do once the workload is gone, so a volume it held is free when
// removeWorkload returns.
func removeWorkload(backendOf func() (placement.Backend, error), handle placement.Handle) error {
	if handle == nil {
		return nil
	}
	handle.Cancel()
	_ = handle.Wait()
	backend, err := backendOf()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), removalTimeout)
	defer cancel()
	return backend.Remove(ctx, handle.ID())
}

// Close stops accepting clients and stops the engine; the volume stays.
func (p *Plane) Close() error {
	p.mu.Lock()
	listeners := p.listeners
	p.listeners = map[string]net.Listener{}
	p.mu.Unlock()
	for _, listener := range listeners {
		_ = listener.Close()
	}
	return p.Stop()
}

// Remove closes a deleted resource's endpoints, stops its engine, removes its
// volume and gives up the loopback addresses its endpoints held.
func (p *Plane) Remove() {
	if err := p.Close(); err != nil {
		log.Printf("%s: stop the Redis engine: %v", p.cfg.Name, err)
	}
	for _, endpoint := range p.Endpoints() {
		dbengine.ReleaseLoopback(Identifier(p.cfg.Owner, endpoint))
	}
	sim.RemoveVolumeSettled(p.cfg.Volume, p.cfg.LogTag)
}
