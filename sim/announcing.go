package sim

import "github.com/e6qu/sockerless-cloud/sim/signals"

// Announcing returns store with every write announced to changes under the
// id it wrote, so a waiter on a resource sleeps until something about it
// changed and then reads it through the service's API, as the service's own
// clients do, instead of reading the store on a timer. A Prune names no ids
// and wakes every waiter.
func Announcing[T any](store Store[T], changes *signals.Keyed[string]) Store[T] {
	return announcingStore[T]{Store: store, changes: changes}
}

type announcingStore[T any] struct {
	Store[T]
	changes *signals.Keyed[string]
}

func (s announcingStore[T]) Put(id string, item T) {
	s.Store.Put(id, item)
	s.changes.Announce(id)
}

func (s announcingStore[T]) Delete(id string) bool {
	deleted := s.Store.Delete(id)
	if deleted {
		s.changes.Announce(id)
	}
	return deleted
}

func (s announcingStore[T]) Update(id string, fn func(*T)) bool {
	updated := s.Store.Update(id, fn)
	if updated {
		s.changes.Announce(id)
	}
	return updated
}

func (s announcingStore[T]) Upsert(id string, fn func(*T)) {
	s.Store.Upsert(id, fn)
	s.changes.Announce(id)
}

func (s announcingStore[T]) Prune(expired func(T) bool) int {
	pruned := s.Store.Prune(expired)
	if pruned > 0 {
		s.changes.AnnounceAll()
	}
	return pruned
}
