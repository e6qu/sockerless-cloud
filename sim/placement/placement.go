// Package placement is the one interface through which a cloud service runs
// a workload — a container on the engine, or a Firecracker microVM on a node
// agent — chosen per service in the simulator's own configuration and never
// by anything a workload or a cloud API client sees.
package placement

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/netrules"
)

// Kind names a backend.
type Kind string

const (
	// Container runs the workload as a container on the engine.
	Container Kind = "container"
	// MicroVM runs the workload as a Firecracker guest on a node agent.
	MicroVM Kind = "microvm"
)

// Service names what asks for placement: a cloud service, and the attribute
// its cloud distinguishes placements by when the service has one (an Amazon
// ECS launch type, an Amazon RDS instance class family).
type Service struct {
	Name      string
	Qualifier string
}

func (s Service) String() string {
	if s.Qualifier == "" {
		return s.Name
	}
	return s.Name + ":" + s.Qualifier
}

// Workload is what a service wants to run, in the terms every backend
// understands. The container backend runs it as it is; the microVM backend
// builds a guest root filesystem from Image and runs Command and Args as the
// guest's first process under Env, on Network at IPv4Address. A member placed
// beside an owner with PlaceMember names no network of its own: it shares
// the owner's.
type Workload = sim.ContainerConfig

// Handle is a placed workload.
type Handle interface {
	// ID identifies the workload on its backend: the container ID, or the
	// guest ID the node agent assigned.
	ID() string
	// Kind is the backend the workload runs on.
	Kind() Kind
	// Wait blocks until the workload exits and returns how it exited.
	Wait() sim.ProcessResult
	// Exited closes once the workload has exited.
	Exited() <-chan struct{}
	// Cancel stops the workload, within the grace its Workload named, and
	// Wait reports the cancellation rather than an exit of the workload's
	// own.
	Cancel()
	// Stop sends the workload the stop signal and, once the grace its
	// Workload named has passed, kills it; Wait then reports how the
	// workload itself exited, as a cloud that stops an instance reads the
	// exit of its process.
	Stop()
	// Exec runs a command inside the workload and returns its output and exit
	// code.
	Exec(ctx context.Context, command []string) (ExecResult, error)
	// PublishedPort returns the 127.0.0.1 host port the backend bound for a
	// port the Workload listed in PublishPorts.
	PublishedPort(ctx context.Context, port int) (int, error)
	// Address is the workload's IPv4 address on its network, a member's
	// being its owner's.
	Address(ctx context.Context) (string, error)
	// SetIngress replaces what the workload may receive, as Workload.Ingress
	// spells it, on a backend that filters; the container backend and a
	// member, which receives through its owner, return ErrNoIngressFilter.
	SetIngress(ctx context.Context, stages [][]netrules.PacketRule) error
	// SetGateway replaces where the workload's default route goes, in the
	// terms of Workload.Gateway, or returns ErrNoRouteControl.
	SetGateway(ctx context.Context, gateway string) error
}

// ErrNoIngressFilter is returned by a handle whose backend cannot filter
// what the workload receives.
var ErrNoIngressFilter = errors.New("the backend does not filter what the workload receives")

// ErrNoRouteControl is returned by a backend that cannot choose where a
// workload's default route goes: a container's is the engine's.
var ErrNoRouteControl = errors.New("the backend does not route the workload")

// Existing is a workload a backend holds from an earlier simulator process:
// running, or exited with its filesystem kept. A member is listed beside
// its owner under its own ID.
type Existing struct {
	ID      string
	Running bool
	Labels  map[string]string
}

// ExecResult is what a command run inside a workload produced.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Backend places workloads.
type Backend interface {
	Kind() Kind
	// Place starts the workload and streams its output into sink.
	Place(ctx context.Context, w Workload, sink sim.LogSink) (Handle, error)
	// PlaceMember starts w in the network namespace of the placed workload
	// owner, so the two reach each other on localhost: a sidecar's shape. On
	// the container backend the member is a container joined to the owner's
	// namespace; on the microVM backend it is a process tree the owner's
	// guest runs from the member's own image. The member's lifetime is its
	// own: it outlives an owner that exits until it is cancelled or exits.
	PlaceMember(ctx context.Context, owner string, w Workload, sink sim.LogSink) (Handle, error)
	// Adopt takes over a workload a previous simulator process placed, found
	// by its backend ID, and streams its output into sink.
	Adopt(ctx context.Context, id string, w Workload, sink sim.LogSink) (Handle, error)
	// Restart boots a kept workload (one placed with KeepOnExit that has
	// exited or was cancelled) again on the same filesystem.
	Restart(ctx context.Context, id string, w Workload, sink sim.LogSink) (Handle, error)
	// Remove discards a kept workload.
	Remove(ctx context.Context, id string) error
	// Console is everything the workload wrote since it was placed, across
	// the restarts of a kept one: the machine's console as a cloud reads it.
	Console(ctx context.Context, id string) (string, error)
	// Capture stores the workload's filesystem as an engine image under
	// reference: the disk image a cloud makes of a machine, which another
	// workload boots from.
	Capture(ctx context.Context, id, reference string) error
	// Find lists the workloads of this simulator's persistent identity that
	// carry every label, running or kept, for a restarted process to adopt.
	Find(ctx context.Context, labels map[string]string) ([]Existing, error)
}

// Table maps services to backends from the simulator's configuration: a
// comma-separated list of `service[:qualifier]=container|microvm`, every
// service unnamed running on containers. A qualifier entry wins over the
// service's own.
type Table struct {
	rules    map[string]Kind
	backends map[Kind]Backend
}

// ParseTable reads the configuration; an unknown backend name or a malformed
// entry is an error naming it.
func ParseTable(spec string) (*Table, error) {
	t := &Table{rules: map[string]Kind{}, backends: map[Kind]Backend{}}
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		service, kind, ok := strings.Cut(entry, "=")
		service, kind = strings.TrimSpace(service), strings.TrimSpace(kind)
		if !ok || service == "" {
			return nil, fmt.Errorf("placement entry %q: want service[:qualifier]=container|microvm", entry)
		}
		switch Kind(kind) {
		case Container, MicroVM:
			t.rules[service] = Kind(kind)
		default:
			return nil, fmt.Errorf("placement entry %q: backend %q is not container or microvm", entry, kind)
		}
	}
	return t, nil
}

// Register makes a backend available to the table.
func (t *Table) Register(b Backend) { t.backends[b.Kind()] = b }

// KindFor is the backend kind configured for the service.
func (t *Table) KindFor(s Service) Kind {
	if s.Qualifier != "" {
		if kind, ok := t.rules[s.String()]; ok {
			return kind
		}
	}
	if kind, ok := t.rules[s.Name]; ok {
		return kind
	}
	return Container
}

// ErrOwnNetwork is wrapped when a member names a network, aliases or hosts
// of its own: it joins the owner's namespace and takes those from it.
var ErrOwnNetwork = errors.New("a member names its own network; it joins the owner's")

// memberShape checks that w names no network of its own.
func memberShape(w Workload) error {
	if w.NetworkMode != "" || w.Network != "" || w.IPv4Address != "" || len(w.NetworkAliases) > 0 || len(w.ExtraHosts) > 0 || len(w.PublishPorts) > 0 {
		return fmt.Errorf("%w: %s", ErrOwnNetwork, w.Name)
	}
	return nil
}

// ErrNoBackend is wrapped when a service is configured onto a backend the
// simulator has not been given.
var ErrNoBackend = errors.New("no backend of that kind is registered")

// For is the backend the service's workloads run on.
func (t *Table) For(s Service) (Backend, error) {
	kind := t.KindFor(s)
	b, ok := t.backends[kind]
	if !ok {
		return nil, fmt.Errorf("placement of %s on %s: %w", s, kind, ErrNoBackend)
	}
	return b, nil
}

// Services lists every service the configuration names, for a startup log.
func (t *Table) Services() []string {
	out := make([]string, 0, len(t.rules))
	for service, kind := range t.rules {
		out = append(out, service+"="+string(kind))
	}
	sort.Strings(out)
	return out
}

// MicroVMServices lists the services configured onto microVMs.
func (t *Table) MicroVMServices() []string {
	var out []string
	for service, kind := range t.rules {
		if kind == MicroVM {
			out = append(out, service)
		}
	}
	sort.Strings(out)
	return out
}
