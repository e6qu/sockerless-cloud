package funcrt

import (
	"errors"
	"fmt"
	"sync"
)

// ErrThrottled is what a ThrottledError matches with errors.Is.
var ErrThrottled = errors.New("funcrt: concurrency limit reached")

// ThrottledError refuses an invocation that no limit on its path has room
// for. Limit names the one that refused, so the cloud can say which: a
// function's own reserved concurrency, or the concurrency its account leaves
// unreserved.
type ThrottledError struct {
	Limit *Limit
}

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("funcrt: concurrency limit of %d reached", e.Limit.Max())
}

func (e *ThrottledError) Is(target error) bool { return target == ErrThrottled }

// Limit caps the invocations in flight on the pools that count against it.
//
// A bounded limit owns its capacity: reserving concurrency for a function
// takes it out of what the account leaves to everyone else, so the
// function's invocations count against its own limit and not the account's.
// An unbounded limit counts against its parent, as a function without
// reserved concurrency shares the account's unreserved concurrency.
//
// Capacity is taken two ways. An on-demand invocation holds a unit while it
// runs. Provisioned environments hold theirs for as long as they are
// allocated, busy or idle, which is how provisioned concurrency that adds up
// to a function's reserved concurrency leaves no room for anything else.
type Limit struct {
	parent *Limit

	mu       sync.Mutex
	max      int
	inUse    int
	reserved int
}

// NewLimit returns a limit of max units under parent; a negative max is
// unbounded, and parent may be nil.
func NewLimit(parent *Limit, max int) *Limit {
	return &Limit{parent: parent, max: max}
}

// SetMax changes the cap. Invocations already in flight past a lowered cap
// run to their end; the next one waits for the count to drop below it.
func (l *Limit) SetMax(max int) {
	l.mu.Lock()
	l.max = max
	l.mu.Unlock()
}

// Max is the cap, negative when unbounded.
func (l *Limit) Max() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.max
}

// Usage reports the units held by invocations in flight and by provisioned
// environments, counted on this limit itself.
func (l *Limit) Usage() (inUse, reserved int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inUse, l.reserved
}

// owner is the limit whose capacity l's invocations take: l itself when
// bounded, otherwise the nearest bounded ancestor, or nil when none is.
func (l *Limit) owner() *Limit {
	for cur := l; cur != nil; cur = cur.parent {
		cur.mu.Lock()
		bounded := cur.max >= 0
		cur.mu.Unlock()
		if bounded {
			return cur
		}
	}
	return nil
}

// take holds one unit for an invocation, returning the limit that holds it
// (nil when nothing bounds it) or the error naming the one that refused.
func (l *Limit) take() (*Limit, error) {
	if l == nil {
		return nil, nil
	}
	owner := l.owner()
	if owner == nil {
		return nil, nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.max >= 0 && owner.inUse+owner.reserved >= owner.max {
		return nil, &ThrottledError{Limit: owner}
	}
	owner.inUse++
	return owner, nil
}

// reserve holds n units for provisioned environments.
func (l *Limit) reserve(n int) (*Limit, error) {
	if l == nil || n == 0 {
		return nil, nil
	}
	owner := l.owner()
	if owner == nil {
		return nil, nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.max >= 0 && owner.inUse+owner.reserved+n > owner.max {
		return nil, &ThrottledError{Limit: owner}
	}
	owner.reserved += n
	return owner, nil
}

func (l *Limit) release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.inUse--
	l.mu.Unlock()
}

func (l *Limit) unreserve(n int) {
	if l == nil || n == 0 {
		return
	}
	l.mu.Lock()
	l.reserved -= n
	l.mu.Unlock()
}
