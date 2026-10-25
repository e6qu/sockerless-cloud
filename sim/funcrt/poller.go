package funcrt

import (
	"context"
	"reflect"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim/kvstore"
)

// Outcome is how one poll of an event source ended.
type Outcome int

const (
	// Polled means the poll handled a batch or found the source empty after
	// waiting on it; the poller polls again at once.
	Polled Outcome = iota
	// Blocked means the poll failed for a reason polling again cannot clear,
	// such as the function or the source refusing the poller; the poller
	// waits for one of its wake channels before it polls again.
	Blocked
	// Inactive means the poller should no longer run: its mapping is gone or
	// disabled, or the source it reads has ended.
	Inactive
)

// Source is an event source the cloud polls on a function's behalf: the
// pollers an event source mapping runs over a queue, or over each shard of a
// stream. The cloud keys each poller, and the set of keys can change at any
// time — a mapping created, enabled or deleted, a shard split.
type Source[K comparable] struct {
	// Keys lists the pollers that should run now.
	Keys func() []K
	// KeysChanged returns the channels the next change to Keys' answer
	// closes one of. The pollers take them before reading Keys.
	KeysChanged func() []<-chan struct{}
	// Wake returns the channels whose closing may clear what blocked k's
	// poller. The poller takes them before each poll, so a change that lands
	// during the poll still wakes it.
	Wake func(k K) []<-chan struct{}
	// Poll reads one batch for k, hands it to the function and settles it
	// with the source. It may wait for records to arrive, and must return
	// once ctx is done.
	Poll func(ctx context.Context, k K) Outcome
}

// Pollers runs one poller per key of a source for as long as the key stays
// listed, each on its own goroutine of srv's background work.
type Pollers[K comparable] struct {
	srv    kvstore.Background
	name   string
	source Source[K]

	mu     sync.Mutex
	active map[K]bool
	// ended wakes the loop when a poller ends, since its key may have been
	// listed again while it was ending.
	ended chan struct{}
}

// StartPollers runs the source's pollers until srv stops its background work.
func StartPollers[K comparable](srv kvstore.Background, name string, source Source[K]) *Pollers[K] {
	p := &Pollers[K]{srv: srv, name: name, source: source, active: map[K]bool{}, ended: make(chan struct{}, 1)}
	srv.StartBackground(name, func(ctx context.Context) {
		for {
			changed := source.KeysChanged()
			for _, k := range source.Keys() {
				if p.begin(k) {
					srv.StartBackground(name+" poller", func(ctx context.Context) { p.run(ctx, k) })
				}
			}
			if !waitAny(ctx, append(changed, p.ended)) {
				return
			}
		}
	})
	return p
}

func (p *Pollers[K]) begin(k K) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active[k] {
		return false
	}
	p.active[k] = true
	return true
}

// Running reports whether k's poller runs.
func (p *Pollers[K]) Running(k K) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active[k]
}

func (p *Pollers[K]) run(ctx context.Context, k K) {
	defer func() {
		p.mu.Lock()
		delete(p.active, k)
		p.mu.Unlock()
		select {
		case p.ended <- struct{}{}:
		default:
		}
	}()
	for ctx.Err() == nil {
		wake := p.source.Wake(k)
		switch p.source.Poll(ctx, k) {
		case Polled:
			continue
		case Inactive:
			return
		}
		if !waitAny(ctx, wake) {
			return
		}
	}
}

// waitAny blocks until ctx ends or one of chs closes, reporting false when
// ctx ended.
func waitAny(ctx context.Context, chs []<-chan struct{}) bool {
	cases := make([]reflect.SelectCase, 0, len(chs)+1)
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())})
	for _, ch := range chs {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ch)})
	}
	chosen, _, _ := reflect.Select(cases)
	return chosen != 0
}
