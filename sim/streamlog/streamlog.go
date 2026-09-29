// Package streamlog is the partitioned, append-only record log the
// simulators' streaming services share: Amazon Kinesis Data Streams shards
// and Azure Event Hubs partitions.
//
// Each record is a row of its own, keyed by partition and sequence number,
// and each partition has a head row naming its oldest retained and next
// sequence numbers. An append writes the new rows and the head; a read gets
// the rows it returns; trimming deletes the rows that aged out. Nothing
// rewrites a partition whole. Sequence numbers are dense from zero, so a read
// addresses rows directly. Each cloud keeps its own sequence-number and
// offset spelling on top.
package streamlog

import (
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"math/big"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Record is one appended record. Time is its arrival, in Unix milliseconds.
type Record[V any] struct {
	Seq   int64 `json:"seq"`
	Time  int64 `json:"time"`
	Value V     `json:"value"`
}

// Head bounds a partition's retained records: First is the oldest retained
// sequence number and Next the one the next append takes. First == Next when
// nothing is retained.
type Head struct {
	First int64 `json:"first"`
	Next  int64 `json:"next"`
}

// Log is a set of partitions over two stores: records and partition heads.
type Log[V any] struct {
	records sim.Store[Record[V]]
	heads   sim.Store[Head]
	locks   *sim.KeyedLocks
}

// New returns a Log over the given stores.
func New[V any](records sim.Store[Record[V]], heads sim.Store[Head]) *Log[V] {
	return &Log[V]{records: records, heads: heads, locks: sim.NewKeyedLocks()}
}

func recordKey(partition string, seq int64) string {
	return fmt.Sprintf("%s\x00%020d", partition, seq)
}

// Append adds values to a partition in order and returns them as records.
func (l *Log[V]) Append(partition string, now time.Time, values ...V) []Record[V] {
	defer l.locks.Lock(partition)()
	h, _ := l.heads.Get(partition)
	out := make([]Record[V], 0, len(values))
	for _, v := range values {
		rec := Record[V]{Seq: h.Next, Time: now.UnixMilli(), Value: v}
		l.records.Put(recordKey(partition, rec.Seq), rec)
		h.Next++
		out = append(out, rec)
	}
	l.heads.Put(partition, h)
	return out
}

// Import fills an empty partition with records that keep their own sequence
// numbers and times, as a migration from another layout needs. The records
// must be dense and ascending; next is the sequence number the next append
// takes, which may run past the last record when older ones aged out.
func (l *Log[V]) Import(partition string, next int64, recs []Record[V]) error {
	defer l.locks.Lock(partition)()
	if _, ok := l.heads.Get(partition); ok {
		return fmt.Errorf("partition %q already holds records", partition)
	}
	h := Head{First: next, Next: next}
	if len(recs) > 0 {
		h.First = recs[0].Seq
		for i, rec := range recs {
			if rec.Seq != h.First+int64(i) {
				return fmt.Errorf("partition %q: record %d has sequence %d, want %d", partition, i, rec.Seq, h.First+int64(i))
			}
		}
		if last := recs[len(recs)-1].Seq + 1; last > next {
			return fmt.Errorf("partition %q: next sequence %d precedes record %d", partition, next, last-1)
		}
	}
	for _, rec := range recs {
		l.records.Put(recordKey(partition, rec.Seq), rec)
	}
	l.heads.Put(partition, h)
	return nil
}

// Head returns a partition's bounds.
func (l *Log[V]) Head(partition string) Head {
	defer l.locks.Lock(partition)()
	h, _ := l.heads.Get(partition)
	return h
}

// Read returns up to limit records from sequence number from on, starting at
// the oldest retained record when from has aged out.
func (l *Log[V]) Read(partition string, from int64, limit int) []Record[V] {
	defer l.locks.Lock(partition)()
	h, _ := l.heads.Get(partition)
	if from < h.First {
		from = h.First
	}
	var out []Record[V]
	for seq := from; seq < h.Next && len(out) < limit; seq++ {
		rec, ok := l.records.Get(recordKey(partition, seq))
		if !ok {
			break
		}
		out = append(out, rec)
	}
	return out
}

// Last returns the newest retained record of a partition.
func (l *Log[V]) Last(partition string) (Record[V], bool) {
	defer l.locks.Lock(partition)()
	h, _ := l.heads.Get(partition)
	if h.Next == h.First {
		return Record[V]{}, false
	}
	return l.records.Get(recordKey(partition, h.Next-1))
}

// SeekTime returns the sequence number of the first retained record that
// arrived at or after t, or the partition's Next when none did.
func (l *Log[V]) SeekTime(partition string, t time.Time) int64 {
	defer l.locks.Lock(partition)()
	h, _ := l.heads.Get(partition)
	target := t.UnixMilli()
	lo, hi := h.First, h.Next
	for lo < hi {
		mid := lo + (hi-lo)/2
		rec, ok := l.records.Get(recordKey(partition, mid))
		if ok && rec.Time < target {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// Trim deletes the records of a partition that arrived before cutoff and
// returns how many it deleted. Records arrive in sequence order, so trimming
// stops at the first one it keeps.
func (l *Log[V]) Trim(partition string, cutoff time.Time) int {
	defer l.locks.Lock(partition)()
	h, ok := l.heads.Get(partition)
	if !ok {
		return 0
	}
	limit := cutoff.UnixMilli()
	n := 0
	for h.First < h.Next {
		rec, ok := l.records.Get(recordKey(partition, h.First))
		if ok && rec.Time >= limit {
			break
		}
		l.records.Delete(recordKey(partition, h.First))
		h.First++
		n++
	}
	if n > 0 {
		l.heads.Put(partition, h)
	}
	return n
}

// Drop deletes a partition and every record it retains.
func (l *Log[V]) Drop(partition string) {
	defer l.locks.Lock(partition)()
	h, ok := l.heads.Get(partition)
	if !ok {
		return
	}
	for seq := h.First; seq < h.Next; seq++ {
		l.records.Delete(recordKey(partition, seq))
	}
	l.heads.Delete(partition)
}

// Partitioner places a record by its partition key.
type Partitioner interface {
	Partition(key string) string
}

// HashRange is a partition that owns the 128-bit hash keys Start through End.
type HashRange struct {
	ID         string
	Start, End *big.Int
}

// HashRanges places a key by the MD5 hash of the key read as a 128-bit
// unsigned integer, the way Kinesis Data Streams maps a partition key to a
// shard.
type HashRanges []HashRange

// KeyHash is the 128-bit hash key HashRanges places key by.
func KeyHash(key string) *big.Int {
	sum := md5.Sum([]byte(key))
	return new(big.Int).SetBytes(sum[:])
}

func (r HashRanges) Partition(key string) string {
	return r.PartitionHash(KeyHash(key))
}

// PartitionHash places an explicit hash key; one outside every range goes to
// the first partition.
func (r HashRanges) PartitionHash(hash *big.Int) string {
	for _, p := range r {
		if hash.Cmp(p.Start) >= 0 && hash.Cmp(p.End) <= 0 {
			return p.ID
		}
	}
	if len(r) == 0 {
		return ""
	}
	return r[0].ID
}

// Modulo places a key by the first 64 bits of its MD5 hash modulo the number
// of partitions.
type Modulo []string

func (m Modulo) Partition(key string) string {
	if len(m) == 0 {
		return ""
	}
	sum := md5.Sum([]byte(key))
	return m[binary.BigEndian.Uint64(sum[:8])%uint64(len(m))]
}
