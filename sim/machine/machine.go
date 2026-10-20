// Package machine runs the lifecycle of a cloud's virtual machines on a
// placement backend: boot a machine and wait until its init is ready, watch
// it for an exit of its own, stop it keeping its root disk, start that disk
// again, discard it, take a running or kept machine over after the simulator
// restarts, capture its disk as an image and read its console. Amazon EC2,
// Compute Engine and Azure virtual machines each map their own states and
// verbs onto it; the transitions the machine causes itself reach the cloud
// through Hooks.
package machine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/vminstance"
)

// Hooks are what a cloud does when a machine changes on its own account.
// Every hook is optional and runs with no lock of the manager held.
type Hooks struct {
	// Booting runs before a machine is placed or restarted, so the cloud's
	// metadata service answers the machine from its first instruction.
	Booting func(key string)
	// Ready runs once a machine's init is ready.
	Ready func(key string)
	// Down runs once a machine is no longer running, however it stopped.
	Down func(key string)
	// GuestShutdown runs when a machine exits without being stopped: the
	// guest shut itself down, which the cloud records as its stopped state.
	GuestShutdown func(key string)
	// Forgotten runs once the manager no longer holds a machine for key.
	Forgotten func(key string)
	// Sink is where a machine's output streams while it runs; its console is
	// the backend's, so a cloud that streams nothing elsewhere leaves it nil.
	Sink func(key string) sim.LogSink
}

// ErrHalted is what Boot returns when the machine was stopped while booting.
var ErrHalted = errors.New("the machine was stopped while it booted")

// ErrNoMachine is what a call about a key the manager holds no machine for
// returns.
var ErrNoMachine = errors.New("no machine")

// NotReadyError is a machine that never became ready: its init exited, or
// the wait for it failed.
type NotReadyError struct {
	// ExitCode is the init's exit code when it exited, -1 otherwise.
	ExitCode int
	Err      error
}

func (e *NotReadyError) Error() string {
	if e.ExitCode >= 0 {
		return fmt.Sprintf("the machine's init exited %d before it was ready", e.ExitCode)
	}
	return "the machine never became ready: " + e.Err.Error()
}

func (e *NotReadyError) Unwrap() error { return e.Err }

// readyWait returns once the init raised its ready marker.
var readyWait = []string{"/bin/sh", "-c", "until [ -e " + vminstance.ReadyPath + " ]; do sleep 0.2; done"}

// Manager holds the machines of one cloud service.
type Manager struct {
	backend placement.Backend
	hooks   Hooks

	mu       sync.Mutex
	machines map[string]*machine
}

type machine struct {
	key      string
	workload placement.Workload

	mu     sync.Mutex
	handle placement.Handle
	// watched closes once the watch of handle has seen it exit and forgot
	// it, which is when a stop has finished.
	watched <-chan struct{}
	// id is the backend's identifier of the machine, which outlives the
	// handle of a stopped machine: its kept root disk.
	id string
	// halted marks a stop; a handle that lands afterwards is cancelled at
	// once, so a boot still placing the machine cannot leave it running.
	halted bool
	// cancelBoot ends the boot in flight, so a stop never waits one out;
	// booting closes once that boot has returned, which a stop waits for so
	// a machine the boot was still placing cannot outlive it.
	cancelBoot context.CancelFunc
	booting    chan struct{}
}

// New is a manager placing machines on backend.
func New(backend placement.Backend, hooks Hooks) *Manager {
	return &Manager{backend: backend, hooks: hooks, machines: map[string]*machine{}}
}

func (m *Manager) get(key string) *machine {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.machines[key]
}

func (m *Manager) sink(key string) sim.LogSink {
	if m.hooks.Sink != nil {
		if sink := m.hooks.Sink(key); sink != nil {
			return sink
		}
	}
	return sim.NoopSink{}
}

func call(hook func(string), key string) {
	if hook != nil {
		hook(key)
	}
}

// Boot brings key's machine up and returns once its init is ready: the kept
// root disk of a stopped machine boots again, any other is placed from
// workload, and a machine that runs already is left as it is. A machine that
// never becomes ready is stopped with its disk kept and Boot returns a
// *NotReadyError; whether to discard the disk is the cloud's decision.
func (m *Manager) Boot(ctx context.Context, key string, workload placement.Workload) error {
	m.mu.Lock()
	mc := m.machines[key]
	fresh := mc == nil
	if fresh {
		mc = &machine{key: key}
		m.machines[key] = mc
	}
	m.mu.Unlock()

	bootCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	booting := make(chan struct{})
	defer close(booting)
	mc.mu.Lock()
	if mc.handle != nil {
		mc.mu.Unlock()
		return nil
	}
	mc.halted = false
	mc.workload = workload
	mc.cancelBoot = cancel
	mc.booting = booting
	id := mc.id
	mc.mu.Unlock()

	call(m.hooks.Booting, key)
	var (
		handle placement.Handle
		err    error
	)
	if id != "" {
		handle, err = m.backend.Restart(bootCtx, id, workload, m.sink(key))
	} else {
		handle, err = m.backend.Place(bootCtx, workload, m.sink(key))
	}
	if err != nil {
		call(m.hooks.Down, key)
		if fresh {
			m.forget(mc)
		}
		if m.wasHalted(mc) {
			return ErrHalted
		}
		return err
	}
	return m.attach(bootCtx, mc, handle)
}

func (m *Manager) wasHalted(mc *machine) bool {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return mc.halted
}

// attach takes a placed handle, watches it and returns once the init is
// ready. A stop that arrived while the machine was placed cancels it here.
func (m *Manager) attach(ctx context.Context, mc *machine, handle placement.Handle) error {
	watched := m.track(mc, handle)
	mc.mu.Lock()
	mc.id = handle.ID()
	halted := mc.halted
	mc.mu.Unlock()
	if halted {
		handle.Cancel()
		<-watched
		return ErrHalted
	}
	if err := awaitReady(ctx, handle); err != nil {
		mc.mu.Lock()
		halted := mc.halted
		// The machine is stopped for never becoming ready, not shut down by
		// its guest.
		mc.halted = true
		mc.mu.Unlock()
		if halted {
			return ErrHalted
		}
		handle.Cancel()
		<-watched
		return err
	}
	mc.mu.Lock()
	mc.cancelBoot = nil
	mc.mu.Unlock()
	call(m.hooks.Ready, mc.key)
	return nil
}

func awaitReady(ctx context.Context, handle placement.Handle) error {
	done := make(chan error, 1)
	go func() {
		result, err := handle.Exec(ctx, readyWait)
		switch {
		case err != nil:
			done <- err
		case result.ExitCode != 0:
			done <- fmt.Errorf("the ready wait exited %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
		default:
			done <- nil
		}
	}()
	exited := func() error { return &NotReadyError{ExitCode: handle.Wait().ExitCode} }
	select {
	case err := <-done:
		select {
		case <-handle.Exited():
			return exited()
		default:
		}
		if err != nil {
			return &NotReadyError{ExitCode: -1, Err: err}
		}
		return nil
	case <-handle.Exited():
		return exited()
	case <-ctx.Done():
		return &NotReadyError{ExitCode: -1, Err: ctx.Err()}
	}
}

// track makes handle the machine's running one and watches it; the channel
// closes once the watch has seen it exit and forgot it.
func (m *Manager) track(mc *machine, handle placement.Handle) <-chan struct{} {
	watched := make(chan struct{})
	mc.mu.Lock()
	mc.handle = handle
	mc.watched = watched
	mc.mu.Unlock()
	bg.Go(func() {
		defer close(watched)
		m.watch(mc, handle)
	})
	return watched
}

// watch follows the handle to its exit. An exit nobody asked for is the guest
// shutting itself down.
func (m *Manager) watch(mc *machine, handle placement.Handle) {
	<-handle.Exited()
	mc.mu.Lock()
	current := mc.handle == handle
	if current {
		mc.handle = nil
	}
	halted := mc.halted
	mc.mu.Unlock()
	if !current {
		return
	}
	call(m.hooks.Down, mc.key)
	if !halted {
		call(m.hooks.GuestShutdown, mc.key)
	}
}

// Halt stops key's machine and keeps its root disk; a boot in flight ends
// with ErrHalted rather than being waited out.
func (m *Manager) Halt(ctx context.Context, key string) error {
	mc := m.get(key)
	if mc == nil {
		return nil
	}
	mc.mu.Lock()
	mc.halted = true
	cancelBoot, booting := mc.cancelBoot, mc.booting
	mc.cancelBoot = nil
	mc.mu.Unlock()
	if cancelBoot != nil {
		cancelBoot()
	}
	if booting != nil {
		select {
		case <-booting:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	mc.mu.Lock()
	handle, watched := mc.handle, mc.watched
	mc.mu.Unlock()
	if handle == nil {
		return nil
	}
	handle.Cancel()
	select {
	case <-watched:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Discard stops key's machine, removes its root disk and forgets it.
func (m *Manager) Discard(ctx context.Context, key string) error {
	mc := m.get(key)
	if mc == nil {
		return nil
	}
	if err := m.Halt(ctx, key); err != nil {
		return err
	}
	mc.mu.Lock()
	id := mc.id
	mc.mu.Unlock()
	m.forget(mc)
	if id == "" {
		return nil
	}
	return m.backend.Remove(ctx, id)
}

// Release forgets key's stopped machine and leaves its root disk in place,
// as a disk the cloud detaches outlives the machine.
func (m *Manager) Release(key string) {
	if mc := m.get(key); mc != nil {
		m.forget(mc)
	}
}

func (m *Manager) forget(mc *machine) {
	m.mu.Lock()
	forgotten := m.machines[mc.key] == mc
	if forgotten {
		delete(m.machines, mc.key)
	}
	m.mu.Unlock()
	if forgotten {
		call(m.hooks.Forgotten, mc.key)
	}
}

// Handle is the handle of key's running machine.
func (m *Manager) Handle(key string) (placement.Handle, bool) {
	mc := m.get(key)
	if mc == nil {
		return nil, false
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return mc.handle, mc.handle != nil
}

// DiskID is the backend's identifier of key's machine, running or kept.
func (m *Manager) DiskID(key string) (string, bool) {
	mc := m.get(key)
	if mc == nil {
		return "", false
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return mc.id, mc.id != ""
}

// RunningMachine is a machine that runs now.
type RunningMachine struct {
	Key    string
	Handle placement.Handle
}

// Running lists the machines that run now.
func (m *Manager) Running() []RunningMachine {
	m.mu.Lock()
	machines := make([]*machine, 0, len(m.machines))
	for _, mc := range m.machines {
		machines = append(machines, mc)
	}
	m.mu.Unlock()
	var out []RunningMachine
	for _, mc := range machines {
		mc.mu.Lock()
		if mc.handle != nil {
			out = append(out, RunningMachine{Key: mc.key, Handle: mc.handle})
		}
		mc.mu.Unlock()
	}
	return out
}

// Console is everything key's machine wrote to its console, across the
// boots of its root disk.
func (m *Manager) Console(ctx context.Context, key string) (string, error) {
	id, ok := m.DiskID(key)
	if !ok {
		return "", ErrNoMachine
	}
	return m.backend.Console(ctx, id)
}

// Capture stores key's root disk as an image under reference, flushing a
// running guest's writes first.
func (m *Manager) Capture(ctx context.Context, key, reference string) error {
	id, ok := m.DiskID(key)
	if !ok {
		return ErrNoMachine
	}
	if handle, running := m.Handle(key); running {
		if _, err := handle.Exec(ctx, []string{"sync"}); err != nil {
			return fmt.Errorf("flush the guest's writes: %w", err)
		}
	}
	return m.backend.Capture(ctx, id, reference)
}

// Found is what Adopt found of a machine an earlier simulator process
// placed.
type Found int

const (
	// Gone is a machine the backend no longer holds.
	Gone Found = iota
	// Kept is a stopped machine whose root disk the backend keeps.
	Kept
	// Up is a machine that still runs.
	Up
)

// Adopt takes over key's machine from what the backend holds of it,
// existing being the backend's listing for key, if any. A running machine is
// watched again with workload; Ready, when non-nil, reports once it is ready.
func (m *Manager) Adopt(ctx context.Context, key string, existing *placement.Existing, workload placement.Workload) (Found, <-chan error, error) {
	if existing == nil {
		return Gone, nil, nil
	}
	mc := &machine{key: key, workload: workload, id: existing.ID}
	m.mu.Lock()
	m.machines[key] = mc
	m.mu.Unlock()
	if !existing.Running {
		return Kept, nil, nil
	}
	handle, err := m.backend.Adopt(ctx, existing.ID, workload, m.sink(key))
	if err != nil {
		return Gone, nil, err
	}
	m.track(mc, handle)
	ready := make(chan error, 1)
	bg.Go(func() {
		err := awaitReady(context.WithoutCancel(ctx), handle)
		if err == nil {
			call(m.hooks.Ready, key)
		}
		ready <- err
	})
	return Up, ready, nil
}

// Find lists the machines the backend holds that carry every label, for a
// restarted simulator to adopt.
func (m *Manager) Find(ctx context.Context, labels map[string]string) ([]placement.Existing, error) {
	return m.backend.Find(ctx, labels)
}

// Remove discards a machine the backend holds that the manager does not:
// one a restarted simulator found no record for.
func (m *Manager) Remove(ctx context.Context, id string) error {
	return m.backend.Remove(ctx, id)
}
