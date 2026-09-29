// Package kvstore holds the primitives the simulators' key-value and document
// stores share: provisioned-throughput token buckets, per-key read-write
// locks, the background expiry sweep, a change log, and entity-tag
// preconditions. A cloud's error shapes and capacity arithmetic stay with the
// cloud; these types only keep the state.
package kvstore

import (
	"math"
	"strings"
	"sync"
	"time"
)

// Limit is a provisioned rate: Rate units accrue per second, up to Burst
// units held unspent.
type Limit struct {
	Rate  float64
	Burst float64
}

// TokenBucket is the balance of one provisioned rate. Buckets starts every
// bucket full.
type TokenBucket struct {
	Limit
	tokens float64
	last   time.Time
}

func (b *TokenBucket) refill(now time.Time) {
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(b.Burst, b.tokens+b.Rate*elapsed)
		b.last = now
	}
}

// Take spends units when the balance covers them and reports whether it did;
// a refused request spends nothing.
func (b *TokenBucket) Take(units float64, now time.Time) bool {
	if units <= 0 {
		return true
	}
	b.refill(now)
	if b.tokens < units {
		return false
	}
	b.tokens -= units
	return true
}

// Admit reports whether a request whose cost is known only after it runs may
// start: it may while the balance is positive. When it may not, it returns how
// long until the balance is positive again.
func (b *TokenBucket) Admit(now time.Time) (bool, time.Duration) {
	b.refill(now)
	if b.tokens > 0 {
		return true, 0
	}
	if b.Rate <= 0 {
		return false, 0
	}
	wait := time.Duration(math.Floor(-b.tokens/b.Rate*1000)+1) * time.Millisecond
	return false, wait
}

// Charge spends units after the fact. The balance may go negative; Admit
// refuses until the refill has repaid the debt.
func (b *TokenBucket) Charge(units float64, now time.Time) {
	b.refill(now)
	b.tokens -= units
}

// Buckets holds one TokenBucket per key. A bucket starts full, and starts
// full again when the key's Limit changes.
type Buckets struct {
	mu sync.Mutex
	by map[string]*TokenBucket
}

func (s *Buckets) bucket(key string, limit Limit, now time.Time) *TokenBucket {
	if s.by == nil {
		s.by = map[string]*TokenBucket{}
	}
	b, ok := s.by[key]
	if !ok || b.Limit != limit {
		b = &TokenBucket{Limit: limit, tokens: limit.Burst, last: now}
		s.by[key] = b
	}
	return b
}

// Take is TokenBucket.Take on key's bucket.
func (s *Buckets) Take(key string, limit Limit, units float64, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bucket(key, limit, now).Take(units, now)
}

// Admit is TokenBucket.Admit on key's bucket.
func (s *Buckets) Admit(key string, limit Limit, now time.Time) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bucket(key, limit, now).Admit(now)
}

// Charge is TokenBucket.Charge on key's bucket.
func (s *Buckets) Charge(key string, limit Limit, units float64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bucket(key, limit, now).Charge(units, now)
}

// Forget drops every bucket whose key begins with prefix.
func (s *Buckets) Forget(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.by {
		if strings.HasPrefix(key, prefix) {
			delete(s.by, key)
		}
	}
}
