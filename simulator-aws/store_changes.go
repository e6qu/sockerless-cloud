package main

import (
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// storeChanges wakes whoever waits on an item of a store when the store
// writes it. A waiter that needs a resource to settle sleeps until something
// about it changed and then reads its state through the service's API, as
// the service's own clients do, instead of reading the store on a timer.
type storeChanges struct {
	mu      sync.Mutex
	waiters map[string]chan struct{}
}

func newStoreChanges() *storeChanges {
	return &storeChanges{waiters: map[string]chan struct{}{}}
}

// Changed returns a channel that closes on the next write to id. Take it
// before reading the state it guards, so a write between the read and the
// wait wakes the waiter.
func (c *storeChanges) Changed(id string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	waiter, ok := c.waiters[id]
	if !ok {
		waiter = make(chan struct{})
		c.waiters[id] = waiter
	}
	return waiter
}

func (c *storeChanges) announce(id string) {
	c.mu.Lock()
	waiter, ok := c.waiters[id]
	delete(c.waiters, id)
	c.mu.Unlock()
	if ok {
		close(waiter)
	}
}

func (c *storeChanges) announceAll() {
	c.mu.Lock()
	waiters := c.waiters
	c.waiters = map[string]chan struct{}{}
	c.mu.Unlock()
	for _, waiter := range waiters {
		close(waiter)
	}
}

// announcingStore reports every write to a store to changes.
type announcingStore[T any] struct {
	sim.Store[T]
	changes *storeChanges
}

var _ sim.Store[struct{}] = announcingStore[struct{}]{}

func announcing[T any](store sim.Store[T], changes *storeChanges) sim.Store[T] {
	return announcingStore[T]{Store: store, changes: changes}
}

func (s announcingStore[T]) Put(id string, item T) {
	s.Store.Put(id, item)
	s.changes.announce(id)
}

func (s announcingStore[T]) Delete(id string) bool {
	deleted := s.Store.Delete(id)
	s.changes.announce(id)
	return deleted
}

func (s announcingStore[T]) Update(id string, fn func(*T)) bool {
	updated := s.Store.Update(id, fn)
	if updated {
		s.changes.announce(id)
	}
	return updated
}

func (s announcingStore[T]) Upsert(id string, fn func(*T)) {
	s.Store.Upsert(id, fn)
	s.changes.announce(id)
}

// Prune names no ids, so every waiter wakes and reads the state again.
func (s announcingStore[T]) Prune(expired func(T) bool) int {
	pruned := s.Store.Prune(expired)
	if pruned > 0 {
		s.changes.announceAll()
	}
	return pruned
}
