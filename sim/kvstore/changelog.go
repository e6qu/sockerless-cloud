package kvstore

import (
	"fmt"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ChangeOp is what a change did to its key.
type ChangeOp string

const (
	OpCreate  ChangeOp = "create"
	OpReplace ChangeOp = "replace"
	OpDelete  ChangeOp = "delete"
)

// Change is one write in a ChangeLog: the item after it (nothing for a
// delete), the item before it (nothing for a create), and the sequence
// number that orders it within its stream.
type Change[T any] struct {
	Seq      uint64    `json:"seq"`
	Key      string    `json:"key"`
	Op       ChangeOp  `json:"op"`
	Current  *T        `json:"current,omitempty"`
	Previous *T        `json:"previous,omitempty"`
	At       time.Time `json:"at"`
	// Expired marks a delete the store's time-to-live made rather than a
	// client.
	Expired bool `json:"expired,omitempty"`
}

// ChangeLog records every write to a set of streams — a container, a table —
// deletes included, and keeps each for Retention.
//
// The caller assigns sequence numbers and must append a stream's changes in
// sequence order, which it does by appending under the lock it already holds
// for the write. A reader resuming after sequence n then cannot miss a change
// numbered below one it has seen.
//
// Amazon DynamoDB Streams has the same shape: a table's stream is a ChangeLog
// keyed by table, OLD_IMAGE and NEW_IMAGE are Previous and Current, and its
// 24-hour retention is Retention; a shard iterator is the last sequence read.
type ChangeLog[T any] struct {
	store     sim.Store[Change[T]]
	Retention time.Duration
}

// NewChangeLog keeps its entries in store.
func NewChangeLog[T any](store sim.Store[Change[T]], retention time.Duration) *ChangeLog[T] {
	return &ChangeLog[T]{store: store, Retention: retention}
}

func changeID(stream string, seq uint64) string {
	return fmt.Sprintf("%s/%020d", stream, seq)
}

// Append records change in stream.
func (l *ChangeLog[T]) Append(stream string, change Change[T]) {
	l.store.Put(changeID(stream, change.Seq), change)
}

// Read returns up to max of stream's changes numbered after afterSeq, in
// sequence order; max <= 0 returns all of them.
func (l *ChangeLog[T]) Read(stream string, afterSeq uint64, max int) []Change[T] {
	var out []Change[T]
	for _, entry := range l.store.ListPrefix(stream + "/") {
		if entry.Item.Seq <= afterSeq {
			continue
		}
		out = append(out, entry.Item)
		if max > 0 && len(out) == max {
			break
		}
	}
	return out
}

// LastSeq is the highest sequence number the log holds in any stream, which a
// caller restoring persisted state raises its counter above.
func (l *ChangeLog[T]) LastSeq() uint64 {
	var last uint64
	for _, entry := range l.store.ListPrefix("") {
		last = max(last, entry.Item.Seq)
	}
	return last
}

// Prune deletes every change older than Retention as of now.
func (l *ChangeLog[T]) Prune(now time.Time) int {
	cutoff := now.Add(-l.Retention)
	return l.store.Prune(func(c Change[T]) bool { return c.At.Before(cutoff) })
}

// Drop deletes every change of every stream whose name begins with prefix,
// for a container or table that no longer exists.
func (l *ChangeLog[T]) Drop(prefix string) {
	for _, entry := range l.store.ListPrefix(prefix) {
		l.store.Delete(entry.ID)
	}
}
