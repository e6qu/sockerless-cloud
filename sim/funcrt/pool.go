// Package funcrt runs a function's invocations on execution environments: the
// mechanism behind AWS Lambda's execution environments and the Azure
// Functions host an App Service site runs.
//
// A Pool keeps the environments of one function version. Acquire leases an
// environment for one invocation: an idle environment that already finished
// initialising serves it without initialising again, so whatever the
// function's static code set up stays set up; when none is idle the pool
// starts another, scaling out with the invocations in flight. An environment
// serves Options.PerEnvironment invocations at once (one for Lambda; zero
// lets one environment serve every invocation, as a Functions host does).
// Release hands the environment back for the next invocation; Discard resets
// it, stopping it for good, as a cloud does after a crash or a timeout.
//
// Invocations count against a Limit, which several pools may share: the
// versions of one function share that function's reserved concurrency. An
// invocation the limit has no room for is refused with a ThrottledError
// rather than queued, so the cloud answers it the way it answers a throttle.
//
// Provision keeps a number of environments initialised ahead of any
// invocation. They hold their share of the limit while allocated, and serve
// invocations only once every one of them has initialised; invocations past
// them run on on-demand environments. Retire stops the pool: idle
// Invocations count against a Limit, which several pools may share: the
// versions of one function share that function's reserved concurrency.
// Acquire refuses an invocation the limit has no room for with a
// ThrottledError rather than queuing it, so the cloud answers it as a throttle.
// The package holds no wire shapes. The cloud creates and initialises an
// environment through Hooks.Start, and the environment's own protocol — the
// Lambda Runtime API, the Functions host's HTTP listener — carries the
// invocation; the pool only decides which environment runs it.
package funcrt

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// ErrRetired refuses an invocation on a pool Retire stopped.
var ErrRetired = errors.New("funcrt: pool retired")

// Kind is how an environment came to be initialised.
type Kind int

const (
	// OnDemand environments start for an invocation that found none idle.
	OnDemand Kind = iota
	// Provisioned environments start ahead of invocations, from Provision.
	Provisioned
)

func (k Kind) String() string {
	if k == Provisioned {
		return "provisioned"
	}
	return "on-demand"
}

// Environment is one execution environment the cloud started.
type Environment interface {
	// Exited closes once the environment is gone on its own, its workload
	// having exited.
	Exited() <-chan struct{}
	// Stop ends the environment and returns once it is gone.
	Stop()
}

// Hooks are what the cloud does for a pool.
type Hooks[E Environment] struct {
	// Start creates an environment of kind k. For a provisioned environment
	// it returns once the environment has initialised; an on-demand one may
	// return as soon as it exists and finish initialising under the
	// invocation that started it.
	Start func(ctx context.Context, k Kind) (E, error)
	// Expires, when set, is the time after which env must take no further
	// invocation, such as when the credentials it was started with lapse.
	// The pool stops it once it is idle past that time.
	Expires func(env E) time.Time
}

type Options struct {
	// Limit is what the pool's invocations count against; nil is unbounded.
	Limit *Limit
	// PerEnvironment is how many invocations one environment serves at once;
	// zero shares one environment among every invocation.
	PerEnvironment int
}

// Provisioning is where a pool's provisioned environments stand.
type Provisioning struct {
	// Requested is the number Provision asked for.
	Requested int
	// Allocated is the number that have initialised.
	Allocated int
	// Ready reports that every requested environment has initialised and
	// serves invocations.
	Ready bool
	// Err is why an environment could not be provisioned; the pool stops
	// provisioning once one fails.
	Err error
}

type slotState int

const (
	starting slotState = iota
	ready
	gone
)

type slot[E Environment] struct {
	kind   Kind
	state  slotState
	env    E
	leases int
	// started closes once Start returned, with err set when it failed.
	started chan struct{}
	err     error
	// park closes when the environment stops being idle, waking the watch
	// that stops it should it exit or expire while idle.
	park chan struct{}
}

func (s *slot[E]) idle() bool { return s.state == ready && s.leases == 0 }

// Pool runs one function version's invocations.
type Pool[E Environment] struct {
	hooks  Hooks[E]
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	slots   []*slot[E]
	retired bool
	// changed closes on every change a waiting Acquire or a provisioning
	// observer may care about.
	changed chan struct{}

	provRequested int
	provHolder    *Limit
	provReserved  int
	provErr       error
}

// New returns an empty pool. Its background starts end when ctx does.
func New[E Environment](ctx context.Context, hooks Hooks[E], opts Options) *Pool[E] {
	ctx, cancel := context.WithCancel(ctx)
	return &Pool[E]{hooks: hooks, opts: opts, ctx: ctx, cancel: cancel, changed: make(chan struct{})}
}

// Lease is one invocation's hold on an environment.
type Lease[E Environment] struct {
	pool   *Pool[E]
	slot   *slot[E]
	holder *Limit
	// Warm reports that the environment had served an invocation or was
	// provisioned before this one, so it initialised before the lease.
	Warm bool
	once sync.Once
}

func (l *Lease[E]) Env() E { return l.slot.env }

// Kind is how the leased environment was initialised.
func (l *Lease[E]) Kind() Kind { return l.slot.kind }

func (p *Pool[E]) notifyLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// Changed returns a channel the pool's next change closes: an environment
// starting, becoming idle or going, or provisioning moving on.
func (p *Pool[E]) Changed() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.changed
}

func (p *Pool[E]) provisionedReadyLocked() bool {
	if p.provRequested == 0 || p.provErr != nil {
		return false
	}
	n := 0
	for _, s := range p.slots {
		if s.kind == Provisioned && s.state == ready {
			n++
		}
	}
	return n >= p.provRequested
}

func (p *Pool[E]) hasRoomLocked(s *slot[E]) bool {
	return p.opts.PerEnvironment == 0 || s.leases < p.opts.PerEnvironment
}

// pickLocked finds a ready environment with room, provisioned ones first
// once they all serve.
func (p *Pool[E]) pickLocked(now time.Time) *slot[E] {
	provReady := p.provisionedReadyLocked()
	var onDemand *slot[E]
	for _, s := range p.slots {
		if s.state != ready || !p.hasRoomLocked(s) || p.expiredLocked(s, now) {
			continue
		}
		if s.kind == Provisioned {
			if provReady {
				return s
			}
			continue
		}
		if onDemand == nil {
			onDemand = s
		}
	}
	return onDemand
}

func (p *Pool[E]) expiredLocked(s *slot[E], now time.Time) bool {
	if p.hooks.Expires == nil || s.state != ready {
		return false
	}
	at := p.hooks.Expires(s.env)
	return !at.IsZero() && !now.Before(at)
}

// startingWithRoomLocked is an environment still starting that another
// invocation may share, for a pool whose environments serve many at once.
func (p *Pool[E]) startingWithRoomLocked() *slot[E] {
	if p.opts.PerEnvironment == 1 {
		return nil
	}
	for _, s := range p.slots {
		if s.state == starting && s.kind == OnDemand && p.hasRoomLocked(s) {
			return s
		}
	}
	return nil
}

// Acquire leases an environment for one invocation: an idle one when there
// is one, else a new one the pool starts. It refuses with a ThrottledError
// when the limit has no room, and with ErrRetired once the pool is retired.
func (p *Pool[E]) Acquire(ctx context.Context) (*Lease[E], error) {
	p.mu.Lock()
	if p.retired {
		p.mu.Unlock()
		return nil, ErrRetired
	}
	p.stopExpiredLocked(time.Now())
	if s := p.pickLocked(time.Now()); s != nil {
		var holder *Limit
		if s.kind == OnDemand {
			var err error
			if holder, err = p.opts.Limit.take(); err != nil {
				p.mu.Unlock()
				return nil, err
			}
		}
		if s.leases == 0 {
			close(s.park)
		}
		s.leases++
		p.notifyLocked()
		p.mu.Unlock()
		return &Lease[E]{pool: p, slot: s, holder: holder, Warm: true}, nil
	}
	if s := p.startingWithRoomLocked(); s != nil {
		holder, err := p.opts.Limit.take()
		if err != nil {
			p.mu.Unlock()
			return nil, err
		}
		s.leases++
		started := s.started
		p.notifyLocked()
		p.mu.Unlock()
		select {
		case <-started:
		case <-ctx.Done():
			p.mu.Lock()
			s.leases--
			p.mu.Unlock()
			holder.release()
			return nil, ctx.Err()
		}
		p.mu.Lock()
		if s.err != nil {
			s.leases--
			p.mu.Unlock()
			holder.release()
			return nil, s.err
		}
		p.mu.Unlock()
		return &Lease[E]{pool: p, slot: s, holder: holder}, nil
	}
	holder, err := p.opts.Limit.take()
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	s := &slot[E]{kind: OnDemand, state: starting, leases: 1, started: make(chan struct{})}
	p.slots = append(p.slots, s)
	p.notifyLocked()
	p.mu.Unlock()

	env, err := p.hooks.Start(ctx, OnDemand)
	p.mu.Lock()
	if err != nil {
		s.err = err
		s.state = gone
		s.leases--
		p.removeLocked(s)
		close(s.started)
		p.notifyLocked()
		p.mu.Unlock()
		holder.release()
		return nil, err
	}
	// An environment that finished starting after Retire still serves
	// this invocation, and stops when released.
	s.env = env
	s.state = ready
	close(s.started)
	p.notifyLocked()
	p.mu.Unlock()
	return &Lease[E]{pool: p, slot: s, holder: holder}, nil
}

func (p *Pool[E]) removeLocked(s *slot[E]) {
	for i, cur := range p.slots {
		if cur == s {
			p.slots = append(p.slots[:i], p.slots[i+1:]...)
			return
		}
	}
}

// Release hands the environment back to serve the next invocation.
func (l *Lease[E]) Release() { l.end(false) }

// Discard resets the environment: it stops for good, and the next
// invocation starts another. A provisioned environment is replaced.
func (l *Lease[E]) Discard() { l.end(true) }

func (l *Lease[E]) end(discard bool) {
	l.once.Do(func() {
		p, s := l.pool, l.slot
		p.mu.Lock()
		s.leases--
		var stop bool
		switch {
		case s.state == gone:
			stop = s.leases == 0
		case discard, p.retired, p.excessProvisionedLocked(s), p.expiredLocked(s, time.Now()):
			s.state = gone
			p.removeLocked(s)
			stop = s.leases == 0
		case s.leases == 0:
			p.parkUnderLock(s)
		}
		replace := discard && s.kind == Provisioned && !p.retired
		// Free the unit only once the environment is idle again, so the next
		// invocation reuses it rather than starting another.
		l.holder.release()
		p.notifyLocked()
		p.mu.Unlock()
		if stop {
			s.env.Stop()
		}
		if replace {
			p.topUp()
		}
	})
}

// excessProvisionedLocked reports a provisioned environment past what
// Provision now asks for.
func (p *Pool[E]) excessProvisionedLocked(s *slot[E]) bool {
	if s.kind != Provisioned {
		return false
	}
	n := 0
	for _, cur := range p.slots {
		if cur.kind == Provisioned && cur.state != gone {
			n++
		}
	}
	return n > p.provRequested
}

// parkUnderLock makes s idle and watches it, stopping it should its workload
// exit or its time run out before an invocation takes it.
func (p *Pool[E]) parkUnderLock(s *slot[E]) {
	park := make(chan struct{})
	s.park = park
	exited := s.env.Exited()
	var expiry <-chan time.Time
	var timer *time.Timer
	if p.hooks.Expires != nil {
		if at := p.hooks.Expires(s.env); !at.IsZero() {
			timer = time.NewTimer(time.Until(at))
			expiry = timer.C
		}
	}
	var expired bool
	bg.WatchThen(func() {
		select {
		case <-park:
		case <-exited:
		case <-expiry:
			expired = true
		case <-p.ctx.Done():
		}
		if timer != nil {
			timer.Stop()
		}
	}, func() {
		p.mu.Lock()
		if s.park != park || !s.idle() {
			p.mu.Unlock()
			return
		}
		select {
		case <-exited:
		default:
			if !expired {
				p.mu.Unlock()
				return
			}
		}
		s.state = gone
		close(park)
		p.removeLocked(s)
		replace := s.kind == Provisioned && !p.retired
		p.notifyLocked()
		p.mu.Unlock()
		s.env.Stop()
		if replace {
			p.topUp()
		}
	})
}

func (p *Pool[E]) stopExpiredLocked(now time.Time) {
	for _, s := range append([]*slot[E](nil), p.slots...) {
		if s.idle() && p.expiredLocked(s, now) {
			s.state = gone
			close(s.park)
			p.removeLocked(s)
			env := s.env
			bg.JoinedGo(env.Stop)
		}
	}
}

// Provision keeps n environments initialised ahead of invocations, starting
// what is missing in the background and stopping what is past n. It
// refuses with a ThrottledError when the limit cannot hold n more.
func (p *Pool[E]) Provision(n int) error {
	p.mu.Lock()
	if p.retired {
		p.mu.Unlock()
		return ErrRetired
	}
	p.provHolder.unreserve(p.provReserved)
	p.provHolder, p.provReserved = nil, 0
	holder, err := p.opts.Limit.reserve(n)
	if err != nil {
		// Keep the earlier reservation rather than lose it to the refusal.
		if prev, perr := p.opts.Limit.reserve(p.provRequested); perr == nil {
			p.provHolder, p.provReserved = prev, p.provRequested
		}
		p.mu.Unlock()
		return err
	}
	p.provHolder, p.provReserved = holder, n
	if n != p.provRequested {
		p.provErr = nil
	}
	p.provRequested = n
	var stop []E
	excess := -n
	for _, s := range p.slots {
		if s.kind == Provisioned && s.state != gone {
			excess++
		}
	}
	for i := len(p.slots) - 1; i >= 0 && excess > 0; i-- {
		s := p.slots[i]
		if s.kind != Provisioned || !s.idle() {
			continue
		}
		s.state = gone
		close(s.park)
		p.removeLocked(s)
		stop = append(stop, s.env)
		excess--
	}
	p.notifyLocked()
	p.mu.Unlock()
	for _, env := range stop {
		env.Stop()
	}
	p.topUp()
	return nil
}

// topUp starts the provisioned environments the pool is short of.
func (p *Pool[E]) topUp() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.retired || p.provErr != nil {
		return
	}
	have := 0
	for _, s := range p.slots {
		if s.kind == Provisioned && s.state != gone {
			have++
		}
	}
	for ; have < p.provRequested; have++ {
		s := &slot[E]{kind: Provisioned, state: starting, started: make(chan struct{})}
		p.slots = append(p.slots, s)
		bg.Go(func() { p.startProvisioned(s) })
	}
	p.notifyLocked()
}

func (p *Pool[E]) startProvisioned(s *slot[E]) {
	env, err := p.hooks.Start(p.ctx, Provisioned)
	p.mu.Lock()
	if err != nil {
		s.err = err
		s.state = gone
		p.removeLocked(s)
		close(s.started)
		if p.ctx.Err() == nil && !p.retired {
			p.provErr = err
		}
		p.notifyLocked()
		p.mu.Unlock()
		return
	}
	s.env = env
	close(s.started)
	if p.retired || p.excessProvisionedAmongLocked(s) {
		s.state = gone
		p.removeLocked(s)
		p.notifyLocked()
		p.mu.Unlock()
		env.Stop()
		return
	}
	s.state = ready
	p.parkUnderLock(s)
	p.notifyLocked()
	p.mu.Unlock()
}

// excessProvisionedAmongLocked reports whether s, still starting, is past
// what Provision asks for now that it has finished.
func (p *Pool[E]) excessProvisionedAmongLocked(s *slot[E]) bool {
	n := 0
	for _, cur := range p.slots {
		if cur.kind == Provisioned && cur.state != gone {
			n++
		}
	}
	return n > p.provRequested
}

// Provisioning reports where the provisioned environments stand.
func (p *Pool[E]) Provisioning() Provisioning {
	p.mu.Lock()
	defer p.mu.Unlock()
	allocated := 0
	for _, s := range p.slots {
		if s.kind == Provisioned && s.state == ready {
			allocated++
		}
	}
	return Provisioning{
		Requested: p.provRequested,
		Allocated: min(allocated, p.provRequested),
		Ready:     p.provisionedReadyLocked(),
		Err:       p.provErr,
	}
}

type Stats struct {
	Starting, Idle, Busy int
}

// Stats counts the environments by what they are doing.
func (p *Pool[E]) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	var st Stats
	for _, s := range p.slots {
		switch {
		case s.state == starting:
			st.Starting++
		case s.idle():
			st.Idle++
		case s.state == ready:
			st.Busy++
		}
	}
	return st
}

// Retire stops the pool: idle environments stop now, busy ones once their
// invocation ends, provisioned capacity returns to the limit, and Acquire
// refuses from now on. It returns once the idle environments are gone.
func (p *Pool[E]) Retire() {
	p.mu.Lock()
	if p.retired {
		p.mu.Unlock()
		return
	}
	p.retired = true
	p.provHolder.unreserve(p.provReserved)
	p.provHolder, p.provReserved, p.provRequested = nil, 0, 0
	var stop []E
	for _, s := range append([]*slot[E](nil), p.slots...) {
		if s.idle() {
			s.state = gone
			close(s.park)
			p.removeLocked(s)
			stop = append(stop, s.env)
		}
	}
	p.notifyLocked()
	p.mu.Unlock()
	p.cancel()
	var wg sync.WaitGroup
	for _, env := range stop {
		wg.Add(1)
		bg.JoinedGo(func() {
			defer wg.Done()
			env.Stop()
		})
	}
	wg.Wait()
}
