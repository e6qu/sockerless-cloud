package cacheengine

import (
	"bufio"
	"context"
	"crypto/tls"
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
)

// MemcachedPort is the port every Memcached node listens on inside its
// workload.
const MemcachedPort = 11211

// memcachedProbeInterval is how often a starting node is asked for its
// version: memcached reports readiness on its output only at a verbosity that
// also logs every command a client sends.
const memcachedProbeInterval = 100 * time.Millisecond

// MemcachedConfig is how a resource's Memcached nodes run.
type MemcachedConfig struct {
	Name     string
	Owner    string
	Image    string
	Platform string
	// Labels identify the resource's workloads; NodeLabel is the label that
	// carries a node's index.
	Labels    map[string]string
	NodeLabel string
	Backend   func() (placement.Backend, error)
	Network   func(ctx context.Context) (string, []string, error)
	Sandbox   sim.SandboxProfile
	// MemoryMB is the item memory each node holds, memcached's -m.
	MemoryMB int
	// TLS terminates TLS at every endpoint; nil serves plaintext.
	TLS *tls.Config
}

type memcachedNode struct {
	handle   placement.Handle
	hostPort int
}

// Memcached is one resource's Memcached nodes and the endpoints in front of
// them. The nodes share nothing: a client spreads its keys over them, and the
// discovery endpoint reaches any one of them.
type Memcached struct {
	cfg MemcachedConfig

	mu        sync.RWMutex
	nodes     map[int]*memcachedNode
	endpoints []Endpoint
	listeners map[string]net.Listener

	startMu   sync.Mutex
	attempted bool
	startErr  error

	cursor atomic.Uint64
}

// NewMemcached builds the plane serving a resource from its record.
func NewMemcached(cfg MemcachedConfig, record Record) *Memcached {
	m := &Memcached{cfg: cfg, nodes: map[int]*memcachedNode{}, listeners: map[string]net.Listener{}}
	for _, index := range record.Nodes {
		m.nodes[index] = &memcachedNode{}
	}
	m.endpoints = append(m.endpoints, record.Endpoints...)
	return m
}

// Record is the plane's nodes and endpoints as they now stand.
func (m *Memcached) Record() Record {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Record{Endpoints: append([]Endpoint(nil), m.endpoints...), Nodes: m.orderLocked()}
}

// Endpoints are the endpoints the plane serves.
func (m *Memcached) Endpoints() []Endpoint {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Endpoint(nil), m.endpoints...)
}

func (m *Memcached) orderLocked() []int {
	order := make([]int, 0, len(m.nodes))
	for index := range m.nodes {
		order = append(order, index)
	}
	sort.Ints(order)
	return order
}

// Nodes are the indices of the plane's nodes in order.
func (m *Memcached) Nodes() []int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.orderLocked()
}

// AddEndpoints records endpoints as the plane's own.
func (m *Memcached) AddEndpoints(endpoints ...Endpoint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.endpoints = append(m.endpoints, endpoints...)
}

// ServeEndpoints serves each endpoint the plane records from the listener
// bound for it.
func (m *Memcached) ServeEndpoints(listeners map[string]net.Listener) {
	for _, endpoint := range m.Endpoints() {
		listener, ok := listeners[endpoint.Key()]
		if !ok {
			continue
		}
		target := m.anyNode
		if endpoint.Role == NodeEndpoint {
			target = fixedTarget(endpoint.Node)
		}
		m.serve(endpoint.Key(), listener, target)
	}
}

func (m *Memcached) anyNode() (int, bool) {
	order := m.Nodes()
	if len(order) == 0 {
		return 0, false
	}
	return order[int(m.cursor.Add(1)-1)%len(order)], true
}

func (m *Memcached) serve(key string, listener net.Listener, target func() (int, bool)) {
	m.mu.Lock()
	m.listeners[key] = listener
	m.mu.Unlock()
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go m.relayConnection(client, target)
		}
	}()
}

func (m *Memcached) stopServing(key string) {
	m.mu.Lock()
	listener := m.listeners[key]
	delete(m.listeners, key)
	m.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
}

func (m *Memcached) relayConnection(client net.Conn, target func() (int, bool)) {
	defer client.Close()
	if m.cfg.TLS != nil {
		secured, ok := terminateTLS(client, m.cfg.TLS, false)
		if !ok {
			return
		}
		client = secured
	}
	if err := m.Ensure(); err != nil {
		log.Printf("%s data plane: %v", m.cfg.Name, err)
		return
	}
	index, ok := target()
	if !ok {
		return
	}
	address, err := m.nodeAddress(index)
	if err != nil {
		log.Printf("%s data plane: %v", m.cfg.Name, err)
		return
	}
	backend, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		log.Printf("%s data plane: dial node %d: %v", m.cfg.Name, index, err)
		return
	}
	defer backend.Close()
	Relay(client, backend)
}

func (m *Memcached) nodeAddress(index int) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n, ok := m.nodes[index]
	if !ok || n.handle == nil {
		return "", fmt.Errorf("node %d of the Memcached engine is not running", index)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(n.hostPort)), nil
}

// Ensure starts every node and returns once each answers. The outcome holds
// until Stop.
func (m *Memcached) Ensure() error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if !m.attempted {
		m.attempted = true
		var missing []int
		m.mu.RLock()
		for _, index := range m.orderLocked() {
			if m.nodes[index].handle == nil {
				missing = append(missing, index)
			}
		}
		m.mu.RUnlock()
		if m.startErr = m.startNodes(missing); m.startErr != nil {
			_ = m.stopNodes(m.Nodes())
		}
	}
	return m.startErr
}

// Running reports whether the nodes came up and have not stopped since.
func (m *Memcached) Running() bool {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	return m.attempted && m.startErr == nil
}

func (m *Memcached) startNodes(nodes []int) error {
	if len(nodes) == 0 {
		return nil
	}
	network, dns, err := m.cfg.Network(context.Background())
	if err != nil {
		return fmt.Errorf("start the Memcached engine: %w", err)
	}
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i, index := range nodes {
		wg.Go(func() { errs[i] = m.startNode(index, network, dns) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (m *Memcached) startNode(index int, network string, dns []string) error {
	backend, err := m.cfg.Backend()
	if err != nil {
		return fmt.Errorf("start node %d of the Memcached engine: %w", index, err)
	}
	labels := map[string]string{m.cfg.NodeLabel: strconv.Itoa(index)}
	for key, value := range m.cfg.Labels {
		labels[key] = value
	}
	memory := m.cfg.MemoryMB
	if memory <= 0 {
		memory = 64
	}
	handle, err := backend.Place(context.Background(), placement.Workload{
		CancelGracePeriod: stopGrace,
		Image:             m.cfg.Image,
		Architecture:      m.cfg.Platform,
		Command:           []string{"memcached", "-p", strconv.Itoa(MemcachedPort), "-U", "0", "-m", strconv.Itoa(memory)},
		PublishPorts:      []int{MemcachedPort},
		Labels:            labels,
		Network:           network,
		DNS:               dns,
		Sandbox:           m.cfg.Sandbox,
	}, sim.NoopSink{})
	if err != nil {
		return fmt.Errorf("start node %d of the Memcached engine: %w", index, err)
	}
	if err := m.attach(index, handle); err != nil {
		handle.Cancel()
		_ = handle.Wait()
		return fmt.Errorf("start node %d of the Memcached engine: %w", index, err)
	}
	return m.awaitNode(index, handle)
}

func (m *Memcached) attach(index int, handle placement.Handle) error {
	ctx, cancel := context.WithTimeout(context.Background(), commandDeadline)
	defer cancel()
	hostPort, err := handle.PublishedPort(ctx, MemcachedPort)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[index]
	if !ok {
		return fmt.Errorf("node %d is not part of the engine", index)
	}
	n.handle, n.hostPort = handle, hostPort
	return nil
}

// awaitNode asks the node for its version until it answers, giving up once
// its workload exits or the budget runs out.
func (m *Memcached) awaitNode(index int, handle placement.Handle) error {
	address, err := m.nodeAddress(index)
	if err != nil {
		return err
	}
	deadline := time.NewTimer(readyBudget)
	defer deadline.Stop()
	for !memcachedAnswers(address) {
		select {
		case <-handle.Exited():
			result := handle.Wait()
			return fmt.Errorf("node %d of the Memcached engine stopped before accepting connections: workload %s exited with status %d (%v)", index, handle.ID(), result.ExitCode, result.Error)
		case <-deadline.C:
			return fmt.Errorf("node %d of the Memcached engine did not become ready within %s", index, readyBudget)
		case <-time.After(memcachedProbeInterval):
		}
	}
	return nil
}

func memcachedAnswers(address string) bool {
	conn, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := conn.Write([]byte("version\r\n")); err != nil {
		return false
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	return err == nil && strings.HasPrefix(line, "VERSION ")
}

// Resize runs count nodes: new nodes take the next indices, each served at an
// endpoint of its own on port, and the nodes beyond count go, highest index
// first.
func (m *Memcached) Resize(count, port int) error {
	if err := m.Ensure(); err != nil {
		return err
	}
	order := m.Nodes()
	if count < len(order) {
		removed := order[count:]
		if err := m.stopNodes(removed); err != nil {
			return err
		}
		m.mu.Lock()
		for _, index := range removed {
			delete(m.nodes, index)
		}
		kept := m.endpoints[:0]
		var released []Endpoint
		for _, endpoint := range m.endpoints {
			if endpoint.Role == NodeEndpoint && m.nodes[endpoint.Node] == nil {
				released = append(released, endpoint)
				continue
			}
			kept = append(kept, endpoint)
		}
		m.endpoints = kept
		m.mu.Unlock()
		for _, endpoint := range released {
			m.stopServing(endpoint.Key())
			dbengine.ReleaseLoopback(Identifier(m.cfg.Owner, endpoint))
		}
		return nil
	}
	var added []int
	m.mu.Lock()
	next := 0
	for index := range m.nodes {
		if index >= next {
			next = index + 1
		}
	}
	for i := len(order); i < count; i++ {
		m.nodes[next] = &memcachedNode{}
		added = append(added, next)
		next++
	}
	m.mu.Unlock()
	listeners := map[string]net.Listener{}
	for _, index := range added {
		endpoint := Endpoint{Role: NodeEndpoint, Port: port, Node: index}
		listener, address, err := Listen(Identifier(m.cfg.Owner, endpoint), port)
		if err != nil {
			CloseListeners(listeners)
			return err
		}
		endpoint.Address = address
		listeners[endpoint.Key()] = listener
		m.AddEndpoints(endpoint)
	}
	for key, listener := range listeners {
		index, _ := strconv.Atoi(strings.TrimPrefix(key, "node-"))
		m.serve(key, listener, fixedTarget(index))
	}
	return m.startNodes(added)
}

// Reboot restarts nodes, each coming back empty, as a Memcached node holds
// its items only in memory.
func (m *Memcached) Reboot(nodes []int) error {
	if err := m.Ensure(); err != nil {
		return err
	}
	if err := m.stopNodes(nodes); err != nil {
		return err
	}
	return m.startNodes(nodes)
}

// Adopt picks up the node workloads an earlier control-plane process left
// for this resource.
func (m *Memcached) Adopt() error {
	backend, err := m.cfg.Backend()
	if err != nil {
		return err
	}
	ctx := context.Background()
	existing, err := backend.Find(ctx, m.cfg.Labels)
	if err != nil {
		return err
	}
	for _, found := range existing {
		index, err := strconv.Atoi(found.Labels[m.cfg.NodeLabel])
		m.mu.RLock()
		_, known := m.nodes[index]
		m.mu.RUnlock()
		if err != nil || !known || !found.Running {
			if err := backend.Remove(ctx, found.ID); err != nil {
				return fmt.Errorf("remove Memcached engine workload %s: %w", found.ID, err)
			}
			continue
		}
		handle, err := backend.Adopt(ctx, found.ID, placement.Workload{CancelGracePeriod: stopGrace}, sim.NoopSink{})
		if err != nil {
			return err
		}
		if err := m.attach(index, handle); err != nil {
			handle.Cancel()
			_ = handle.Wait()
			return fmt.Errorf("adopt the Memcached engine workload %s: %w", found.ID, err)
		}
	}
	return nil
}

func (m *Memcached) stopNodes(nodes []int) error {
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i, index := range nodes {
		wg.Go(func() {
			m.mu.Lock()
			var handle placement.Handle
			if n, ok := m.nodes[index]; ok {
				handle = n.handle
				n.handle, n.hostPort = nil, 0
			}
			m.mu.Unlock()
			errs[i] = removeWorkload(m.cfg.Backend, handle)
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Stop stops every node and forgets the last start's outcome.
func (m *Memcached) Stop() error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	err := m.stopNodes(m.Nodes())
	m.attempted, m.startErr = false, nil
	return err
}

// Remove closes a deleted resource's endpoints, stops its nodes and gives up
// the loopback addresses its endpoints held.
func (m *Memcached) Remove() {
	m.mu.Lock()
	listeners := m.listeners
	m.listeners = map[string]net.Listener{}
	m.mu.Unlock()
	CloseListeners(listeners)
	if err := m.Stop(); err != nil {
		log.Printf("%s: stop the Memcached engine: %v", m.cfg.Name, err)
	}
	for _, endpoint := range m.Endpoints() {
		dbengine.ReleaseLoopback(Identifier(m.cfg.Owner, endpoint))
	}
}
