// Package signals wakes goroutines that wait for something keyed to change:
// an operation to finish, a queue to gain a message, a record to be written.
// A waiter sleeps until the writer announces the key and then reads the state
// it cares about again, instead of reading that state on a timer.
//
// The contract has one rule a waiter must follow: take the channel before
// reading the state it guards. Changed(k) returns a channel the next
// Announce(k) closes, so a write that lands between the waiter's read and its
// wait has already closed the channel the waiter holds, and the wait returns
// at once. A waiter that reads first and takes the channel after can sleep
// through the write it was waiting for.
//
// An announcement wakes only the waiters that hold a channel when it is made;
// it is not remembered for a waiter that arrives later, which reads the
// current state instead. Waiters may wake when nothing they care about
// changed, so they always re-read and decide again.
package signals

import (
	"context"
	"sync"
)

// Keyed wakes the waiters of each key. The zero value is ready to use.
type Keyed[K comparable] struct {
	mu      sync.Mutex
	waiters map[K]chan struct{}
	// any wakes the waiters of every key; nil while nobody holds it.
	any chan struct{}
}

// New returns an empty Keyed.
func New[K comparable]() *Keyed[K] { return &Keyed[K]{} }

// Changed returns a channel that closes on the next announcement of k, or of
// every key.
func (s *Keyed[K]) Changed(k K) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.waiters[k]
	if !ok {
		if s.waiters == nil {
			s.waiters = map[K]chan struct{}{}
		}
		ch = make(chan struct{})
		s.waiters[k] = ch
	}
	return ch
}

// Any returns a channel that closes on the next announcement of any key.
func (s *Keyed[K]) Any() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.any == nil {
		s.any = make(chan struct{})
	}
	return s.any
}

// Announce wakes the waiters of k and the waiters of any key.
func (s *Keyed[K]) Announce(k K) {
	s.mu.Lock()
	ch, ok := s.waiters[k]
	delete(s.waiters, k)
	anyCh := s.any
	s.any = nil
	s.mu.Unlock()
	if ok {
		close(ch)
	}
	if anyCh != nil {
		close(anyCh)
	}
}

// AnnounceAll wakes every waiter, for a change that names no single key.
func (s *Keyed[K]) AnnounceAll() {
	s.mu.Lock()
	waiters := s.waiters
	s.waiters = nil
	anyCh := s.any
	s.any = nil
	s.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
	if anyCh != nil {
		close(anyCh)
	}
}

// Await calls ready until it reports true, waiting for an announcement of k
// between calls. It returns ctx's error when ctx ends first.
func (s *Keyed[K]) Await(ctx context.Context, k K, ready func() bool) error {
	for {
		changed := s.Changed(k)
		if ready() {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
