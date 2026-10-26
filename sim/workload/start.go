package workload

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// Member is one container of a group that starts in dependency order: a
// Cloud Run task or instance, an Amazon ECS task, a Container Apps replica.
type Member struct {
	Container
	// DependsOn names the members that must have started, and passed their
	// startup check, before this one starts.
	DependsOn []string
	// Startup, when set, returns once the member passed its startup check:
	// a probe, a health check. owner is the member whose network namespace
	// the group shares.
	Startup func(ctx context.Context, member, owner placement.Handle) error
}

// StartOptions are what a group's start reports and writes to.
type StartOptions struct {
	// Sink is where member i writes its output.
	Sink func(i int) sim.LogSink
	// Placed runs as each member lands, before its startup check, so a caller
	// that stops the group while it starts holds every member placed so far.
	Placed func(i int, h placement.Handle)
	// Owner configures the member that owns the group's network namespace,
	// the first in start order: the network it joins, its address, the
	// ports it publishes.
	Owner func(cfg *sim.ContainerConfig)
}

// StartOrder orders the members so each comes after every member its
// DependsOn names, keeping the declared order among members free to start.
// It refuses a dependency on no other member of the group, and a cycle.
func StartOrder(members []Member) ([]int, error) {
	index := make(map[string]int, len(members))
	for i, m := range members {
		if m.Name != "" {
			index[m.Name] = i
		}
	}
	waiting := make([]int, len(members))
	dependents := make([][]int, len(members))
	for i, m := range members {
		for _, dep := range m.DependsOn {
			j, ok := index[dep]
			if !ok || j == i {
				return nil, fmt.Errorf("containers[%d].dependsOn names %q, which is not another container", i, dep)
			}
			waiting[i]++
			dependents[j] = append(dependents[j], i)
		}
	}
	order := make([]int, 0, len(members))
	started := make([]bool, len(members))
	for len(order) < len(members) {
		next := -1
		for i := range members {
			if !started[i] && waiting[i] == 0 {
				next = i
				break
			}
		}
		if next < 0 {
			return nil, errors.New("the containers' dependsOn form a cycle")
		}
		started[next] = true
		order = append(order, next)
		for _, d := range dependents[next] {
			waiting[d]--
		}
	}
	return order, nil
}

// StartError is a group that did not start: StartGroup cancelled the members
// it placed, and Stopped closes once every one of them has exited, for a
// caller that holds what they used, an address or a mount, until then.
type StartError struct {
	Err     error
	stopped chan struct{}
}

func (e *StartError) Error() string { return e.Err.Error() }
func (e *StartError) Unwrap() error { return e.Err }

// Stopped closes once every member the failed start placed has exited.
func (e *StartError) Stopped() <-chan struct{} { return e.stopped }

// StartGroup starts members on backend in their dependency order: the first
// to start owns the network namespace and is placed on its own, every other
// joins it, and each starts once every member it depends on has started and
// passed its startup check. It returns once every member passed its check.
// The returned group's Main is the first declared member and its Sidecars
// the rest, in declared order. When a member fails to start or its check
// fails, or ctx ends, StartGroup cancels every member placed and returns a
// *StartError.
func StartGroup(ctx context.Context, backend placement.Backend, members []Member, opts StartOptions) (*Group, error) {
	if len(members) == 0 {
		return nil, errors.New("the group has no containers")
	}
	order, err := StartOrder(members)
	if err != nil {
		return nil, err
	}
	sink := func(i int) sim.LogSink {
		if opts.Sink == nil {
			return sim.NoopSink{}
		}
		return opts.Sink(i)
	}
	handles := make([]placement.Handle, len(members))
	var mu sync.Mutex
	placed := func(i int, h placement.Handle) {
		mu.Lock()
		handles[i] = h
		mu.Unlock()
		if opts.Placed != nil {
			opts.Placed(i, h)
		}
	}
	fail := func(err error) (*Group, error) {
		mu.Lock()
		var started []placement.Handle
		for _, h := range handles {
			if h != nil {
				h.Cancel()
				started = append(started, h)
			}
		}
		mu.Unlock()
		stopped := make(chan struct{})
		go func() {
			for _, h := range started {
				<-h.Exited()
			}
			close(stopped)
		}()
		return nil, &StartError{Err: err, stopped: stopped}
	}

	owner := order[0]
	ownerContainer := members[owner].Container
	if opts.Owner != nil {
		opts.Owner(&ownerContainer.Config)
	}
	ownerHandle, err := Place(ctx, backend, "main", ownerContainer, sink(owner))
	if err != nil {
		return fail(err)
	}
	placed(owner, ownerHandle)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	index := make(map[string]int, len(members))
	for i, m := range members {
		index[m.Name] = i
	}
	ready := make([]chan struct{}, len(members))
	for i := range ready {
		ready[i] = make(chan struct{})
	}
	errs := make(chan error, len(members))
	var wg sync.WaitGroup
	for _, i := range order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, dep := range members[i].DependsOn {
				select {
				case <-ready[index[dep]]:
				case <-ctx.Done():
					return
				}
			}
			handle := ownerHandle
			if i != owner {
				started, err := PlaceMember(ctx, backend, ownerHandle.ID(), "sidecar", members[i].Container, sink(i))
				if err != nil {
					errs <- err
					cancel()
					return
				}
				placed(i, started)
				handle = started
			}
			if members[i].Startup != nil {
				if err := members[i].Startup(ctx, handle, ownerHandle); err != nil {
					errs <- fmt.Errorf("container %q: %w", members[i].Name, err)
					cancel()
					return
				}
			}
			close(ready[i])
		}()
	}
	wg.Wait()
	select {
	case err := <-errs:
		return fail(err)
	default:
	}
	if err := ctx.Err(); err != nil {
		return fail(fmt.Errorf("the group stopped while its containers started: %w", err))
	}
	return &Group{Main: handles[0], Sidecars: handles[1:]}, nil
}
