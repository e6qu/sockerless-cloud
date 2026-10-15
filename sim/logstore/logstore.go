// Package logstore is the log entry store the simulators' logging services
// share: Amazon CloudWatch Logs streams, Cloud Logging logs and Azure Monitor
// tables.
//
// Each entry is a row of its own, keyed by its scope (a log stream, a log, a
// table) and the sequence number it arrived at, and each scope has a head row
// naming its next sequence number and the count it retains. An append writes
// the new rows and the head; a read gets one scope's rows by key prefix;
// retention deletes the rows it expires. Nothing rewrites a scope whole.
//
// Unlike a stream's records, log entries carry a time of their own, and
// retention is by that time: an entry written with an old timestamp expires on
// its timestamp, not on its arrival, so expiry deletes rows wherever they sit
// and a scope may hold gaps. Reads go by key prefix and never assume density.
package logstore

import (
	"fmt"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Entry is one stored entry. Time is the entry's own time and Ingested when
// it reached the store, both in Unix milliseconds.
type Entry[V any] struct {
	Seq      int64 `json:"seq"`
	Time     int64 `json:"time"`
	Ingested int64 `json:"ingested"`
	Value    V     `json:"value"`
}

// Item is an entry to append: its own time, and the value.
type Item[V any] struct {
	Time  int64
	Value V
}

// Head is a scope's bookkeeping: the sequence number the next append takes
// and how many entries the scope retains.
type Head struct {
	Next  int64 `json:"next"`
	Count int64 `json:"count"`
}

// Log is a set of scopes over two stores: entries and scope heads.
type Log[V any] struct {
	entries sim.Store[Entry[V]]
	heads   sim.Store[Head]
	locks   *sim.KeyedLocks
}

// New returns a Log over the given stores.
func New[V any](entries sim.Store[Entry[V]], heads sim.Store[Head]) *Log[V] {
	return &Log[V]{entries: entries, heads: heads, locks: sim.NewKeyedLocks()}
}

// The separator sorts below every printable byte, so one scope's rows never
// interleave with those of a scope its name is a prefix of.
func scopePrefix(scope string) string { return scope + "\x00" }

func entryKey(scope string, seq int64) string {
	return fmt.Sprintf("%s%020d", scopePrefix(scope), seq)
}

// Open creates scope with no entries if it does not exist, so a scope a
// service created before anything was written to it is listed.
func (l *Log[V]) Open(scope string) {
	defer l.locks.Lock(scope)()
	l.heads.Upsert(scope, func(*Head) {})
}

// Exists reports whether scope has been opened or written to.
func (l *Log[V]) Exists(scope string) bool {
	_, ok := l.heads.Get(scope)
	return ok
}

// Append adds items to scope in order, ingested at now, and returns them as
// entries.
func (l *Log[V]) Append(scope string, now time.Time, items ...Item[V]) []Entry[V] {
	if len(items) == 0 {
		return nil
	}
	defer l.locks.Lock(scope)()
	h, _ := l.heads.Get(scope)
	ingested := now.UnixMilli()
	out := make([]Entry[V], 0, len(items))
	for _, item := range items {
		e := Entry[V]{Seq: h.Next, Time: item.Time, Ingested: ingested, Value: item.Value}
		l.entries.Put(entryKey(scope, e.Seq), e)
		out = append(out, e)
		h.Next++
		h.Count++
	}
	l.heads.Put(scope, h)
	return out
}

// Import fills an empty scope with entries that keep their own times, for a
// migration from a layout that held a scope whole. Entries are numbered in the
// order given.
func (l *Log[V]) Import(scope string, entries []Entry[V]) error {
	defer l.locks.Lock(scope)()
	if h, ok := l.heads.Get(scope); ok && h.Next > 0 {
		return fmt.Errorf("scope %s already holds entries", scope)
	}
	h := Head{}
	for _, e := range entries {
		e.Seq = h.Next
		l.entries.Put(entryKey(scope, e.Seq), e)
		h.Next++
		h.Count++
	}
	l.heads.Put(scope, h)
	return nil
}

// Entries returns scope's retained entries in the order they arrived.
func (l *Log[V]) Entries(scope string) []Entry[V] {
	rows := l.entries.ListPrefix(scopePrefix(scope))
	out := make([]Entry[V], 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Item)
	}
	return out
}

// Values returns the values of scope's retained entries in arrival order.
func (l *Log[V]) Values(scope string) []V {
	rows := l.entries.ListPrefix(scopePrefix(scope))
	out := make([]V, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Item.Value)
	}
	return out
}

// Head returns scope's bookkeeping.
func (l *Log[V]) Head(scope string) Head {
	h, _ := l.heads.Get(scope)
	return h
}

// Scopes returns the names of the scopes that begin with prefix, in name
// order.
func (l *Log[V]) Scopes(prefix string) []string {
	rows := l.heads.ListPrefix(prefix)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

// Expire deletes scope's entries whose own time is before cutoff and returns
// how many it deleted.
func (l *Log[V]) Expire(scope string, cutoff time.Time) int {
	limit := cutoff.UnixMilli()
	return l.remove(scope, func(rows []sim.Keyed[Entry[V]]) []string {
		var doomed []string
		for _, row := range rows {
			if row.Item.Time < limit {
				doomed = append(doomed, row.ID)
			}
		}
		return doomed
	})
}

// Cap deletes scope's oldest-arrived entries beyond max and returns how many
// it deleted.
func (l *Log[V]) Cap(scope string, max int) int {
	if h := l.Head(scope); h.Count <= int64(max) {
		return 0
	}
	return l.remove(scope, func(rows []sim.Keyed[Entry[V]]) []string {
		if len(rows) <= max {
			return nil
		}
		doomed := make([]string, 0, len(rows)-max)
		for _, row := range rows[:len(rows)-max] {
			doomed = append(doomed, row.ID)
		}
		return doomed
	})
}

func (l *Log[V]) remove(scope string, pick func([]sim.Keyed[Entry[V]]) []string) int {
	defer l.locks.Lock(scope)()
	h, ok := l.heads.Get(scope)
	if !ok {
		return 0
	}
	doomed := pick(l.entries.ListPrefix(scopePrefix(scope)))
	for _, id := range doomed {
		l.entries.Delete(id)
	}
	if len(doomed) > 0 {
		h.Count -= int64(len(doomed))
		l.heads.Put(scope, h)
	}
	return len(doomed)
}

// Drop deletes scope and every entry it retains.
func (l *Log[V]) Drop(scope string) {
	defer l.locks.Lock(scope)()
	for _, row := range l.entries.ListPrefix(scopePrefix(scope)) {
		l.entries.Delete(row.ID)
	}
	l.heads.Delete(scope)
}

// DropPrefix deletes every scope whose name begins with prefix.
func (l *Log[V]) DropPrefix(prefix string) {
	for _, scope := range l.Scopes(prefix) {
		l.Drop(scope)
	}
}

// Edit passes each of scope's entries to f, in arrival order. f may change the
// entry's value; it returns whether to keep the entry and whether it changed
// it. Edit deletes the entries f drops, writes back the ones it changed, and
// returns how many it deleted.
func (l *Log[V]) Edit(scope string, f func(*Entry[V]) (keep, changed bool)) int {
	defer l.locks.Lock(scope)()
	h, ok := l.heads.Get(scope)
	if !ok {
		return 0
	}
	deleted := 0
	for _, row := range l.entries.ListPrefix(scopePrefix(scope)) {
		e := row.Item
		keep, changed := f(&e)
		switch {
		case !keep:
			l.entries.Delete(row.ID)
			deleted++
		case changed:
			l.entries.Put(row.ID, e)
		}
	}
	if deleted > 0 {
		h.Count -= int64(deleted)
		l.heads.Put(scope, h)
	}
	return deleted
}
