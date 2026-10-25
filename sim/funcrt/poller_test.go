package funcrt

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/signals"
)

// background runs StartBackground workers until the test ends.
type background struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newBackground(t *testing.T) *background {
	ctx, cancel := context.WithCancel(context.Background())
	b := &background{ctx: ctx, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		b.wg.Wait()
	})
	return b
}

func (b *background) StartBackground(_ string, worker func(context.Context)) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		worker(b.ctx)
	}()
}

// fakeSource is an event source whose keys, batches and refusals the test
// sets.
type fakeSource struct {
	mu      sync.Mutex
	keys    map[string]bool
	batches map[string][]string
	refused map[string]bool
	handled map[string][]string
	ended   map[string]int

	keysChanged *signals.Keyed[string]
	wake        *signals.Keyed[string]
	arrived     *signals.Keyed[string]
	progress    *signals.Keyed[string]
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		keys: map[string]bool{}, batches: map[string][]string{}, refused: map[string]bool{}, handled: map[string][]string{}, ended: map[string]int{},
		keysChanged: signals.New[string](), wake: signals.New[string](), arrived: signals.New[string](), progress: signals.New[string](),
	}
}

func (f *fakeSource) source() Source[string] {
	return Source[string]{
		Keys: func() []string {
			f.mu.Lock()
			defer f.mu.Unlock()
			var out []string
			for k, on := range f.keys {
				if on {
					out = append(out, k)
				}
			}
			return out
		},
		KeysChanged: func() []<-chan struct{} { return []<-chan struct{}{f.keysChanged.Any()} },
		Wake:        func(k string) []<-chan struct{} { return []<-chan struct{}{f.wake.Changed(k)} },
		Poll: func(ctx context.Context, k string) Outcome {
			arrived := f.arrived.Changed(k)
			f.mu.Lock()
			if !f.keys[k] {
				f.ended[k]++
				f.mu.Unlock()
				f.progress.Announce(k)
				return Inactive
			}
			if f.refused[k] {
				f.mu.Unlock()
				f.progress.Announce(k)
				return Blocked
			}
			batch := f.batches[k]
			f.batches[k] = nil
			f.handled[k] = append(f.handled[k], batch...)
			f.mu.Unlock()
			f.progress.Announce(k)
			if len(batch) == 0 {
				select {
				case <-ctx.Done():
				case <-arrived:
				}
			}
			return Polled
		},
	}
}

func (f *fakeSource) setKey(k string, on bool) {
	f.mu.Lock()
	f.keys[k] = on
	f.mu.Unlock()
	f.keysChanged.Announce(k)
	f.arrived.Announce(k)
}

func (f *fakeSource) push(k, record string) {
	f.mu.Lock()
	f.batches[k] = append(f.batches[k], record)
	f.mu.Unlock()
	f.arrived.Announce(k)
}

func (f *fakeSource) await(t *testing.T, k string, cond func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		ch := f.progress.Changed(k)
		f.mu.Lock()
		ok := cond()
		f.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-ch:
		case <-deadline:
			t.Fatal("condition never held")
		}
	}
}

func TestPollerHandsEveryRecordToTheFunction(t *testing.T) {
	f := newFakeSource()
	StartPollers(newBackground(t), "test", f.source())
	f.setKey("m1", true)
	f.push("m1", "a")
	f.push("m1", "b")
	f.await(t, "m1", func() bool { return len(f.handled["m1"]) == 2 })
}

func TestBlockedPollerWaitsForItsWake(t *testing.T) {
	f := newFakeSource()
	f.refused["m1"] = true
	StartPollers(newBackground(t), "test", f.source())
	f.setKey("m1", true)
	f.push("m1", "a")
	f.await(t, "m1", func() bool { return f.refused["m1"] })
	f.mu.Lock()
	f.refused["m1"] = false
	f.mu.Unlock()
	f.wake.Announce("m1")
	f.await(t, "m1", func() bool { return len(f.handled["m1"]) == 1 })
}

func TestPollerStopsWhenItsKeyGoesAndRestartsWhenItReturns(t *testing.T) {
	f := newFakeSource()
	StartPollers(newBackground(t), "test", f.source())
	f.setKey("m1", true)
	f.push("m1", "a")
	f.await(t, "m1", func() bool { return len(f.handled["m1"]) == 1 })
	f.setKey("m1", false)
	f.await(t, "m1", func() bool { return f.ended["m1"] == 1 })
	f.setKey("m1", true)
	f.push("m1", "b")
	f.await(t, "m1", func() bool { return len(f.handled["m1"]) == 2 })
}
