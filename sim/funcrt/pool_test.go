package funcrt

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testEnv is an environment whose workload the test ends.
type testEnv struct {
	id      int
	kind    Kind
	exited  chan struct{}
	stopped atomic.Bool
	once    sync.Once
	expires time.Time
}

func (e *testEnv) Exited() <-chan struct{} { return e.exited }
func (e *testEnv) Stop() {
	e.stopped.Store(true)
	e.exit()
}
func (e *testEnv) exit() { e.once.Do(func() { close(e.exited) }) }

type starter struct {
	mu      sync.Mutex
	started []*testEnv
	// gate, when set, holds every Start until the test closes it.
	gate chan struct{}
	err  error
	// ttl, when set, makes each environment expire that long after it
	// started.
	ttl time.Duration
}

func (s *starter) start(ctx context.Context, k Kind) (*testEnv, error) {
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	env := &testEnv{id: len(s.started) + 1, kind: k, exited: make(chan struct{})}
	if s.ttl > 0 {
		env.expires = time.Now().Add(s.ttl)
	}
	s.started = append(s.started, env)
	return env, nil
}

func (s *starter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.started)
}

func newTestPool(t *testing.T, s *starter, opts Options) *Pool[*testEnv] {
	t.Helper()
	hooks := Hooks[*testEnv]{Start: s.start}
	if s.ttl > 0 {
		hooks.Expires = func(e *testEnv) time.Time { return e.expires }
	}
	p := New(context.Background(), hooks, opts)
	t.Cleanup(p.Retire)
	return p
}

func acquire(t *testing.T, p *Pool[*testEnv]) *Lease[*testEnv] {
	t.Helper()
	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	return l
}

// waitFor blocks until cond holds, waking on each pool change.
func waitFor(t *testing.T, p *Pool[*testEnv], cond func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		changed := p.Changed()
		if cond() {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("condition never held")
		}
	}
}

func TestReleasedEnvironmentServesTheNextInvocation(t *testing.T) {
	s := &starter{}
	p := newTestPool(t, s, Options{PerEnvironment: 1})
	first := acquire(t, p)
	if first.Warm {
		t.Fatal("the first invocation found a warm environment")
	}
	env := first.Env()
	first.Release()

	second := acquire(t, p)
	if second.Env() != env || !second.Warm {
		t.Fatalf("the second invocation ran on environment %d (warm %v), want the idle environment %d", second.Env().id, second.Warm, env.id)
	}
	second.Release()
	if s.count() != 1 {
		t.Fatalf("started %d environments, want 1", s.count())
	}
}

func TestConcurrentInvocationsScaleOut(t *testing.T) {
	s := &starter{}
	p := newTestPool(t, s, Options{PerEnvironment: 1})
	a, b := acquire(t, p), acquire(t, p)
	if a.Env() == b.Env() {
		t.Fatal("two invocations in flight share an environment that serves one at a time")
	}
	a.Release()
	b.Release()
	if st := p.Stats(); st.Idle != 2 {
		t.Fatalf("stats %+v, want two idle environments", st)
	}
}

func TestLimitThrottlesPastItsMax(t *testing.T) {
	s := &starter{}
	limit := NewLimit(nil, 1)
	p := newTestPool(t, s, Options{PerEnvironment: 1, Limit: limit})
	a := acquire(t, p)
	_, err := p.Acquire(context.Background())
	var throttled *ThrottledError
	if !errors.As(err, &throttled) || throttled.Limit != limit || !errors.Is(err, ErrThrottled) {
		t.Fatalf("second Acquire: %v, want a throttle by the limit", err)
	}
	a.Release()
	b := acquire(t, p)
	if b.Env() != a.Env() {
		t.Fatal("the invocation after the release did not reuse the idle environment")
	}
	b.Release()
}

func TestZeroLimitThrottlesEverything(t *testing.T) {
	p := newTestPool(t, &starter{}, Options{PerEnvironment: 1, Limit: NewLimit(nil, 0)})
	if _, err := p.Acquire(context.Background()); !errors.Is(err, ErrThrottled) {
		t.Fatalf("Acquire under a zero limit: %v", err)
	}
}

func TestPoolsShareTheirLimit(t *testing.T) {
	limit := NewLimit(nil, 1)
	v1 := newTestPool(t, &starter{}, Options{PerEnvironment: 1, Limit: limit})
	v2 := newTestPool(t, &starter{}, Options{PerEnvironment: 1, Limit: limit})
	a := acquire(t, v1)
	if _, err := v2.Acquire(context.Background()); !errors.Is(err, ErrThrottled) {
		t.Fatalf("a second version's invocation past the shared limit: %v", err)
	}
	a.Release()
	acquire(t, v2).Release()
}

func TestUnboundedLimitCountsAgainstItsParent(t *testing.T) {
	account := NewLimit(nil, 1)
	fn := NewLimit(account, -1)
	reserved := NewLimit(account, 1)
	p := newTestPool(t, &starter{}, Options{PerEnvironment: 1, Limit: fn})
	r := newTestPool(t, &starter{}, Options{PerEnvironment: 1, Limit: reserved})
	a := acquire(t, p)
	_, err := p.Acquire(context.Background())
	var throttled *ThrottledError
	if !errors.As(err, &throttled) || throttled.Limit != account {
		t.Fatalf("Acquire past the account's limit: %v", err)
	}
	// A bounded limit owns its capacity, so the account's being in use does
	// not throttle it.
	acquire(t, r).Release()
	a.Release()
}

func TestDiscardedEnvironmentStops(t *testing.T) {
	s := &starter{}
	p := newTestPool(t, s, Options{PerEnvironment: 1})
	a := acquire(t, p)
	env := a.Env()
	a.Discard()
	if !env.stopped.Load() {
		t.Fatal("a discarded environment kept running")
	}
	b := acquire(t, p)
	if b.Env() == env {
		t.Fatal("an invocation ran on a discarded environment")
	}
	b.Release()
}

func TestIdleEnvironmentThatExitsLeavesThePool(t *testing.T) {
	s := &starter{}
	p := newTestPool(t, s, Options{PerEnvironment: 1})
	a := acquire(t, p)
	env := a.Env()
	a.Release()
	env.exit()
	waitFor(t, p, func() bool { return p.Stats().Idle == 0 })
	b := acquire(t, p)
	if b.Env() == env {
		t.Fatal("an invocation ran on an environment whose workload exited")
	}
	b.Release()
}

func TestStartFailureReleasesTheLimit(t *testing.T) {
	s := &starter{err: errors.New("image pull failed")}
	limit := NewLimit(nil, 1)
	p := newTestPool(t, s, Options{PerEnvironment: 1, Limit: limit})
	if _, err := p.Acquire(context.Background()); err == nil || errors.Is(err, ErrThrottled) {
		t.Fatalf("Acquire with a failing start: %v", err)
	}
	if inUse, _ := limit.Usage(); inUse != 0 {
		t.Fatalf("a failed start holds %d units of the limit", inUse)
	}
}

func TestSharedEnvironmentServesConcurrentInvocations(t *testing.T) {
	s := &starter{gate: make(chan struct{})}
	p := newTestPool(t, s, Options{PerEnvironment: 0})
	leases := make(chan *Lease[*testEnv], 2)
	for range 2 {
		go func() {
			l, err := p.Acquire(context.Background())
			if err != nil {
				t.Error(err)
			}
			leases <- l
		}()
	}
	waitFor(t, p, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.slots) == 1 && p.slots[0].leases == 2
	})
	close(s.gate)
	a, b := <-leases, <-leases
	if a.Env() != b.Env() || s.count() != 1 {
		t.Fatalf("concurrent invocations of a shared pool started %d environments", s.count())
	}
	a.Release()
	b.Release()
}

func TestProvisionedEnvironmentsServeOnceAllAreReady(t *testing.T) {
	s := &starter{gate: make(chan struct{})}
	limit := NewLimit(nil, 3)
	p := newTestPool(t, s, Options{PerEnvironment: 1, Limit: limit})
	if err := p.Provision(2); err != nil {
		t.Fatal(err)
	}
	if got := p.Provisioning(); got.Requested != 2 || got.Allocated != 0 || got.Ready {
		t.Fatalf("provisioning before any environment started: %+v", got)
	}
	if _, reserved := limit.Usage(); reserved != 2 {
		t.Fatalf("provisioning reserved %d units, want 2", reserved)
	}
	close(s.gate)
	waitFor(t, p, func() bool { return p.Provisioning().Ready })
	if got := p.Provisioning(); got.Allocated != 2 {
		t.Fatalf("provisioning once ready: %+v", got)
	}
	a := acquire(t, p)
	if a.Kind() != Provisioned || !a.Warm {
		t.Fatalf("the first invocation ran on a %v environment, want a provisioned one", a.Kind())
	}
	b := acquire(t, p)
	// Two provisioned environments are busy and the limit's third unit is
	// all that is left for an on-demand one.
	c := acquire(t, p)
	if c.Kind() != OnDemand {
		t.Fatalf("the invocation past the provisioned environments ran on a %v one", c.Kind())
	}
	if _, err := p.Acquire(context.Background()); !errors.Is(err, ErrThrottled) {
		t.Fatalf("Acquire past provisioned and on-demand capacity: %v", err)
	}
	a.Release()
	b.Release()
	c.Release()
}

func TestProvisionedEnvironmentsLeaveNoRoomPastTheLimit(t *testing.T) {
	limit := NewLimit(nil, 1)
	p := newTestPool(t, &starter{}, Options{PerEnvironment: 1, Limit: limit})
	if err := p.Provision(2); !errors.Is(err, ErrThrottled) {
		t.Fatalf("provisioning past the limit: %v", err)
	}
	if err := p.Provision(1); err != nil {
		t.Fatal(err)
	}
	other := newTestPool(t, &starter{}, Options{PerEnvironment: 1, Limit: limit})
	if _, err := other.Acquire(context.Background()); !errors.Is(err, ErrThrottled) {
		t.Fatalf("an unprovisioned version's invocation with the limit all provisioned: %v", err)
	}
}

func TestDiscardedProvisionedEnvironmentIsReplaced(t *testing.T) {
	s := &starter{}
	p := newTestPool(t, s, Options{PerEnvironment: 1})
	if err := p.Provision(1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, p, func() bool { return p.Provisioning().Ready })
	a := acquire(t, p)
	first := a.Env()
	a.Discard()
	waitFor(t, p, func() bool { return p.Provisioning().Ready })
	b := acquire(t, p)
	if b.Env() == first || b.Kind() != Provisioned {
		t.Fatal("the reset provisioned environment was not replaced by another")
	}
	b.Release()
}

func TestFailedProvisioningReportsItsError(t *testing.T) {
	s := &starter{err: errors.New("init failed")}
	p := newTestPool(t, s, Options{PerEnvironment: 1})
	if err := p.Provision(1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, p, func() bool { return p.Provisioning().Err != nil })
	if got := p.Provisioning(); got.Ready || got.Allocated != 0 {
		t.Fatalf("failed provisioning: %+v", got)
	}
}

func TestProvisionLowerStopsIdleProvisionedEnvironments(t *testing.T) {
	s := &starter{}
	limit := NewLimit(nil, 2)
	p := newTestPool(t, s, Options{PerEnvironment: 1, Limit: limit})
	if err := p.Provision(2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, p, func() bool { return p.Provisioning().Ready })
	if err := p.Provision(0); err != nil {
		t.Fatal(err)
	}
	if st := p.Stats(); st.Idle != 0 {
		t.Fatalf("stats after provisioning none: %+v", st)
	}
	if _, reserved := limit.Usage(); reserved != 0 {
		t.Fatalf("provisioning none still reserves %d units", reserved)
	}
}

func TestRetireStopsIdleNowAndBusyOnRelease(t *testing.T) {
	s := &starter{}
	p := newTestPool(t, s, Options{PerEnvironment: 1})
	idle := acquire(t, p)
	busy := acquire(t, p)
	idleEnv, busyEnv := idle.Env(), busy.Env()
	idle.Release()
	p.Retire()
	if !idleEnv.stopped.Load() {
		t.Fatal("Retire left an idle environment running")
	}
	if busyEnv.stopped.Load() {
		t.Fatal("Retire stopped an environment an invocation still runs on")
	}
	busy.Release()
	if !busyEnv.stopped.Load() {
		t.Fatal("a retired pool's environment kept running after its invocation")
	}
	if _, err := p.Acquire(context.Background()); !errors.Is(err, ErrRetired) {
		t.Fatalf("Acquire on a retired pool: %v", err)
	}
}

func TestExpiredEnvironmentTakesNoInvocation(t *testing.T) {
	s := &starter{ttl: time.Millisecond}
	p := newTestPool(t, s, Options{PerEnvironment: 1})
	a := acquire(t, p)
	env := a.Env()
	<-time.After(time.Until(env.expires))
	a.Release()
	if !env.stopped.Load() {
		t.Fatal("an environment released past its expiry kept running")
	}
	b := acquire(t, p)
	if b.Env() == env {
		t.Fatal("an invocation ran on an expired environment")
	}
	b.Release()
}

func TestIdleEnvironmentStopsAtItsExpiry(t *testing.T) {
	s := &starter{ttl: 50 * time.Millisecond}
	p := newTestPool(t, s, Options{PerEnvironment: 1})
	a := acquire(t, p)
	env := a.Env()
	a.Release()
	select {
	case <-env.Exited():
	case <-time.After(10 * time.Second):
		t.Fatal("an idle environment outlived its expiry")
	}
	waitFor(t, p, func() bool { return p.Stats().Idle == 0 })
}

func TestConcurrentAcquireReleaseIsConsistent(t *testing.T) {
	s := &starter{}
	limit := NewLimit(nil, 4)
	p := newTestPool(t, s, Options{PerEnvironment: 1, Limit: limit})
	var wg sync.WaitGroup
	var inFlight, peak atomic.Int32
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := p.Acquire(context.Background())
			if errors.Is(err, ErrThrottled) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			n := inFlight.Add(1)
			for {
				cur := peak.Load()
				if n <= cur || peak.CompareAndSwap(cur, n) {
					break
				}
			}
			inFlight.Add(-1)
			l.Release()
		}()
	}
	wg.Wait()
	if peak.Load() > 4 {
		t.Fatalf("%d invocations ran at once under a limit of 4", peak.Load())
	}
	if inUse, _ := limit.Usage(); inUse != 0 {
		t.Fatalf("%d units still held after every invocation ended", inUse)
	}
	if s.count() > 4 {
		t.Fatalf("started %d environments for at most 4 concurrent invocations", s.count())
	}
}
