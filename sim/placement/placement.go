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
// guest's first process under Env, on Network at IPv4Address.
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
	// Cancel stops the workload, within the grace its Workload named.
	Cancel()
	// Exec runs a command inside the workload and returns its output and exit
	// code.
	Exec(ctx context.Context, command []string) (ExecResult, error)
	// PublishedPort returns the 127.0.0.1 host port the backend bound for a
	// port the Workload listed in PublishPorts.
	PublishedPort(ctx context.Context, port int) (int, error)
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
	// Adopt takes over a workload a previous simulator process placed, found
	// by its backend ID, and streams its output into sink.
	Adopt(ctx context.Context, id string, w Workload, sink sim.LogSink) (Handle, error)
	// Restart boots a kept workload (one placed with KeepOnExit that has
	// exited or was cancelled) again on the same filesystem.
	Restart(ctx context.Context, id string, w Workload, sink sim.LogSink) (Handle, error)
	// Remove discards a kept workload.
	Remove(ctx context.Context, id string) error
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
