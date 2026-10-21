// Package replicas keeps a count of replicas of one specification running:
// the instances of a Cloud Run revision or worker pool, the replicas of a
// Container Apps revision, the tasks of an Amazon ECS service deployment. It
// starts the missing ones, stops the surplus newest first, restarts or
// replaces a replica whose main container exits on its own while the restart
// policy allows, takes over the replicas a restarted simulator finds, and
// picks a running replica for a front end to forward to. The cloud supplies
// how a replica starts and what its changes mean to it through Spec and
// Hooks.
package replicas

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/workload"
)

// Replica is one copy of the set's specification.
type Replica struct {
	ID      string
	created time.Time
	// ctx ends when the set stops the replica, ending a restart in progress.
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu    sync.Mutex
	group *workload.Group
	// release frees what the running group holds, an address or a mount,
	// once its members have exited.
	release  func()
	started  time.Time
	restarts int
	crashes  int
	stopped  bool
}

// Group is the replica's running containers; false while the replica waits
// to restart.
func (r *Replica) Group() (*workload.Group, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.group, r.group != nil
}

// Started is when the replica's containers last started.
func (r *Replica) Started() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

// Restarts counts the times the replica restarted in place.
func (r *Replica) Restarts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.restarts
}

// Done closes once the replica ended for good: the set stopped it, or it
// exited and was not restarted in place, and every member has exited.
func (r *Replica) Done() <-chan struct{} { return r.done }

// Restart is what the set does when a replica's main container exits on its
// own.
type Restart int

const (
	// Always restarts or replaces every replica that exits.
	Always Restart = iota
	// OnFailure restarts or replaces a replica that exits non-zero.
	OnFailure
	// Never leaves an exited replica gone.
	Never
)

// Spec is how the set's replicas start.
type Spec struct {
	// NewID names a replica the set is about to start.
	NewID func() string
	// Start starts replica id and returns once it is running, with what to
	// run once its members have exited.
	Start func(ctx context.Context, id string) (*workload.Group, func(), error)
	// Restart is what an exit of the replica's own leads to.
	Restart Restart
	// InPlace restarts a replica whose main container exits under its own
	// ID, keeping it in the set while it backs off, as Kubernetes restarts a
	// pod's containers; otherwise a new replica replaces it.
	InPlace bool
	// Backoff is how long the set waits before it restarts or replaces a
	// replica that exited or failed to start, given how many times in a row
	// that happened; nil restarts at once.
	Backoff func(consecutive int) time.Duration
	// Settled is how long a replica must run before its exit no longer
	// counts toward the consecutive exits Backoff reads.
	Settled time.Duration
}

// Hooks are what the cloud records as the set's replicas change. Every hook
// is optional and runs with no lock of the set held.
type Hooks struct {
	// Started runs once a replica is running, and again each time it
	// restarted in place.
	Started func(r *Replica)
	// Exited runs when a replica's main container exits on its own, with its
	// result.
	Exited func(r *Replica, result sim.ProcessResult)
	// Stopped runs once a replica the set stopped has ended.
	Stopped func(r *Replica)
	// Failed runs when a replica fails to start or to restart.
	Failed func(id string, err error)
}

// Set is the replicas of one specification.
type Set struct {
	spec  Spec
	hooks Hooks
	// ctx ends when the set is stopped, ending a backoff in progress.
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	want     int
	replicas []*Replica
	next     int
	closed   bool
	// crashes counts the replacements in a row whose replica exited before
	// it had settled, or failed to start.
	crashes int
}

// New is a set of spec's replicas, none running.
func New(spec Spec, hooks Hooks) *Set {
	ctx, cancel := context.WithCancel(context.Background())
	return &Set{spec: spec, hooks: hooks, ctx: ctx, cancel: cancel}
}

// ErrClosed is what Scale returns once the set was stopped.
var ErrClosed = errors.New("the replica set is stopped")

// Scale makes n replicas run: it starts the missing ones and returns once
// they run, or with the first start's error, and stops the surplus, newest
// first, and returns once they have exited or ctx ends.
func (s *Set) Scale(ctx context.Context, n int) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.want = n
	missing := n - len(s.replicas)
	var surplus []*Replica
	if missing < 0 {
		sort.SliceStable(s.replicas, func(i, j int) bool { return s.replicas[i].created.Before(s.replicas[j].created) })
		surplus = append(surplus, s.replicas[n:]...)
		s.replicas = s.replicas[:n]
	}
	s.mu.Unlock()
	for _, r := range surplus {
		s.stop(r)
	}
	for _, r := range surplus {
		select {
		case <-r.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.startMissing(ctx, missing)
}

// Raise makes at least n replicas run: it starts the missing ones and returns
// once they run, or with the first start's error, and keeps any beyond n.
func (s *Set) Raise(ctx context.Context, n int) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.want >= n {
		s.mu.Unlock()
		return nil
	}
	s.want = n
	missing := n - len(s.replicas)
	s.mu.Unlock()
	return s.startMissing(ctx, missing)
}

// Add starts one replica beyond the count the set keeps and returns it once
// it runs. A set that keeps limit already starts none and returns nil; a start
// that fails leaves the count as it was.
func (s *Set) Add(ctx context.Context, limit int) (*Replica, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	if s.want >= limit {
		s.mu.Unlock()
		return nil, nil
	}
	s.want++
	s.mu.Unlock()
	r, err := s.startOne(ctx)
	if err != nil {
		s.mu.Lock()
		s.want--
		s.mu.Unlock()
	}
	return r, err
}

// startMissing starts missing replicas at once and returns once they run, or
// with the first start's error.
func (s *Set) startMissing(ctx context.Context, missing int) error {
	if missing <= 0 {
		return nil
	}
	errs := make(chan error, missing)
	for range missing {
		go func() {
			_, err := s.startOne(ctx)
			errs <- err
		}()
	}
	var first error
	for range missing {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *Set) newReplica(id string, group *workload.Group, started time.Time, release func()) *Replica {
	ctx, cancel := context.WithCancel(s.ctx)
	return &Replica{ID: id, created: started, ctx: ctx, cancel: cancel, done: make(chan struct{}),
		group: group, release: release, started: started}
}

// startOne starts a replica and adds it to the set, unless the set no longer
// wants it by the time it runs, and returns the replica it kept.
func (s *Set) startOne(ctx context.Context) (*Replica, error) {
	id := s.spec.NewID()
	group, release, err := s.spec.Start(ctx, id)
	if err != nil {
		if s.hooks.Failed != nil {
			s.hooks.Failed(id, err)
		}
		return nil, fmt.Errorf("start replica %s: %w", id, err)
	}
	r := s.newReplica(id, group, time.Now(), release)
	s.mu.Lock()
	keep := s.short()
	if keep {
		s.replicas = append(s.replicas, r)
	}
	s.mu.Unlock()
	s.supervise(r)
	if !keep {
		s.stop(r)
		return nil, nil
	}
	if s.hooks.Started != nil {
		s.hooks.Started(r)
	}
	return r, nil
}

// short reports whether the set wants another replica; s.mu is held.
func (s *Set) short() bool { return !s.closed && len(s.replicas) < s.want }

// Adopt takes over a replica a previous simulator process started, as the
// set's own, and watches it from here on.
func (s *Set) Adopt(id string, group *workload.Group, started time.Time, release func()) *Replica {
	r := s.newReplica(id, group, started, release)
	s.mu.Lock()
	s.replicas = append(s.replicas, r)
	if len(s.replicas) > s.want {
		s.want = len(s.replicas)
	}
	s.mu.Unlock()
	s.supervise(r)
	return r
}

// supervise follows a replica to its end: a stop the set made is reported
// once every member exited; an exit of its own is reported and, as the
// restart policy allows, restarted in place or replaced. The replica's Done
// closes before the hook that reports its end runs.
func (s *Set) supervise(r *Replica) {
	bg.Go(func() {
		ended := false
		end := func() {
			if !ended {
				ended = true
				close(r.done)
			}
		}
		defer end()
		for {
			result, stopped := r.await()
			if stopped {
				end()
				s.reportStopped(r)
				return
			}
			s.mu.Lock()
			held := false
			for i, h := range s.replicas {
				if h == r {
					held = true
					if !s.spec.InPlace {
						s.replicas = append(s.replicas[:i], s.replicas[i+1:]...)
					}
					break
				}
			}
			restart := held && !s.closed && s.restarts(result)
			if s.spec.InPlace && held && !restart {
				s.drop(r)
			}
			crashes := s.crashed(r)
			s.mu.Unlock()
			if !restart || !s.spec.InPlace {
				end()
			}
			if s.hooks.Exited != nil {
				s.hooks.Exited(r, result)
			}
			if !restart {
				return
			}
			if !s.spec.InPlace {
				bg.Go(func() { s.replace(crashes) })
				return
			}
			if !s.restartInPlace(r, crashes) {
				end()
				s.reportStopped(r)
				return
			}
		}
	})
}

func (s *Set) reportStopped(r *Replica) {
	if s.hooks.Stopped != nil {
		s.hooks.Stopped(r)
	}
}

// drop removes r from the replicas the set holds; s.mu is held.
func (s *Set) drop(r *Replica) {
	for i, h := range s.replicas {
		if h == r {
			s.replicas = append(s.replicas[:i], s.replicas[i+1:]...)
			return
		}
	}
}

// await waits for the replica's main container to exit, stops the rest of
// its members, and frees what the group held once they exited.
func (r *Replica) await() (sim.ProcessResult, bool) {
	r.mu.Lock()
	group := r.group
	r.mu.Unlock()
	<-group.Main.Exited()
	result := group.Main.Wait()
	group.Stop(0)
	for _, h := range group.Sidecars {
		<-h.Exited()
	}
	r.mu.Lock()
	release := r.release
	r.group, r.release = nil, nil
	stopped := r.stopped
	r.mu.Unlock()
	if release != nil {
		release()
	}
	return result, stopped
}

// crashed counts an exit toward the backoff: the exits in a row before the
// replica settled, per replica when it restarts in place and across the set
// when replacements take its place; s.mu is held.
func (s *Set) crashed(r *Replica) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	settled := time.Since(r.started) >= s.spec.Settled
	if s.spec.InPlace {
		if settled {
			r.crashes = 0
		}
		r.crashes++
		return r.crashes
	}
	if settled {
		s.crashes = 0
	}
	s.crashes++
	return s.crashes
}

// backoff waits as long as consecutive exits call for, and reports false
// when ctx ends first.
func (s *Set) backoff(ctx context.Context, consecutive int) bool {
	var wait time.Duration
	if s.spec.Backoff != nil {
		wait = s.spec.Backoff(consecutive)
	}
	if wait <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// replace starts a replica in place of one that exited, backing off before
// each attempt, until one runs or the set no longer wants it.
func (s *Set) replace(crashes int) {
	for {
		if !s.backoff(s.ctx, crashes) {
			return
		}
		s.mu.Lock()
		wanted := s.short()
		s.mu.Unlock()
		if !wanted {
			return
		}
		if _, err := s.startOne(s.ctx); err == nil {
			return
		}
		s.mu.Lock()
		s.crashes++
		crashes = s.crashes
		s.mu.Unlock()
	}
}

// restartInPlace starts the replica's containers again under its ID, backing
// off before each attempt, and reports false when the set stopped the
// replica first.
func (s *Set) restartInPlace(r *Replica, crashes int) bool {
	for {
		if !s.backoff(r.ctx, crashes) {
			return false
		}
		group, release, err := s.spec.Start(r.ctx, r.ID)
		if err != nil {
			if s.hooks.Failed != nil {
				s.hooks.Failed(r.ID, err)
			}
			r.mu.Lock()
			r.crashes++
			crashes = r.crashes
			r.mu.Unlock()
			continue
		}
		r.mu.Lock()
		if r.stopped {
			r.mu.Unlock()
			group.Stop(0)
			<-group.Main.Exited()
			for _, h := range group.Sidecars {
				<-h.Exited()
			}
			if release != nil {
				release()
			}
			return false
		}
		r.group, r.release, r.started = group, release, time.Now()
		r.restarts++
		r.mu.Unlock()
		if s.hooks.Started != nil {
			s.hooks.Started(r)
		}
		return true
	}
}

func (s *Set) restarts(result sim.ProcessResult) bool {
	switch s.spec.Restart {
	case Always:
		return true
	case OnFailure:
		return result.ExitCode != 0
	default:
		return false
	}
}

// stop stops a replica the set no longer holds, giving each member its
// grace, in the background: a stop takes as long as the grace.
func (s *Set) stop(r *Replica) {
	r.mu.Lock()
	r.stopped = true
	group := r.group
	r.mu.Unlock()
	r.cancel()
	if group != nil {
		bg.Go(func() { group.Stop(0) })
	}
}

// Replicas lists the replicas the set holds, running or waiting to restart,
// oldest first.
func (s *Set) Replicas() []*Replica {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]*Replica(nil), s.replicas...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].created.Before(out[j].created) })
	return out
}

// Pick is the next running replica in turn that ready accepts, for a front
// end that spreads its requests across them; a nil ready accepts every
// running replica.
func (s *Set) Pick(ready func(*Replica) bool) (*Replica, *workload.Group, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for range s.replicas {
		r := s.replicas[s.next%len(s.replicas)]
		s.next++
		if group, ok := r.Group(); ok && (ready == nil || ready(r)) {
			return r, group, true
		}
	}
	return nil, nil, false
}

// Stop stops every replica and returns once each has ended or ctx ends; the
// set starts no replica after it.
func (s *Set) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	held := s.replicas
	s.replicas = nil
	s.mu.Unlock()
	s.cancel()
	for _, r := range held {
		s.stop(r)
	}
	for _, r := range held {
		select {
		case <-r.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
