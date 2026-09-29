package cron

import (
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
)

// Record is what a Ticker persists per schedule: the next occurrence, so a
// restarted simulator resumes a schedule instead of recomputing it from the
// restart instant.
type Record struct {
	Spec string    `json:"spec"`
	Next time.Time `json:"next"`
	Done bool      `json:"done,omitempty"`
}

// Entry is one schedule a Ticker evaluates.
type Entry struct {
	Key string
	// Spec identifies what the occurrences derive from; a changed Spec
	// discards the persisted occurrence and starts the schedule afresh.
	Spec string
	// First is the first occurrence of a schedule the Ticker has not seen;
	// nil means Next(now).
	First func(now time.Time) (time.Time, bool)
	// Next is the occurrence after one that fired; nil makes the schedule
	// one-shot.
	Next func(after time.Time) (time.Time, bool)
	// MaxLate drops an occurrence the simulator was not running for when it is
	// older than this; zero fires it however late.
	MaxLate time.Duration
	// Paused keeps the persisted occurrence without firing it.
	Paused bool
	Fire   func(scheduled time.Time)
}

// Ticker fires every due Entry once per tick. Occurrences missed while the
// simulator was down collapse into the latest of them, which fires once.
type Ticker struct {
	store   sim.Store[Record]
	entries func() []Entry
}

func NewTicker(store sim.Store[Record], entries func() []Entry) *Ticker {
	return &Ticker{store: store, entries: entries}
}

// Start ticks every interval on the server's background lifecycle.
func (t *Ticker) Start(srv kvstore.Background, name string, interval time.Duration) {
	kvstore.StartSweeper(srv, name, interval, t.Tick)
}

// Tick fires what is due at now and forgets the records of schedules that no
// longer exist.
func (t *Ticker) Tick(now time.Time) {
	live := map[string]bool{}
	for _, entry := range t.entries() {
		live[entry.Key] = true
		t.tickEntry(entry, now)
	}
	for _, keyed := range t.store.ListPrefix("") {
		if !live[keyed.ID] {
			t.store.Delete(keyed.ID)
		}
	}
}

func (t *Ticker) tickEntry(entry Entry, now time.Time) {
	rec, ok := t.store.Get(entry.Key)
	if !ok || rec.Spec != entry.Spec {
		first := entry.First
		if first == nil {
			first = entry.Next
		}
		next, has := first(now)
		rec = Record{Spec: entry.Spec, Next: next, Done: !has}
		t.store.Put(entry.Key, rec)
	}
	if rec.Done || entry.Paused || now.Before(rec.Next) {
		return
	}
	scheduled := rec.Next
	if entry.Next != nil {
		for {
			later, has := entry.Next(scheduled)
			if !has || later.After(now) {
				break
			}
			scheduled = later
		}
	}
	if entry.MaxLate <= 0 || now.Sub(scheduled) <= entry.MaxLate {
		entry.Fire(scheduled)
	}
	rec.Done = true
	if entry.Next != nil {
		if next, has := entry.Next(scheduled); has {
			rec.Next, rec.Done = next, false
		}
	}
	if _, still := t.store.Get(entry.Key); still {
		t.store.Put(entry.Key, rec)
	}
}

// Forget drops a schedule's record, so the next tick starts it afresh.
func (t *Ticker) Forget(key string) { t.store.Delete(key) }
