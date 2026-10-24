package signals

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestAnnounceClosesOnlyTheChannelsOfItsKey(t *testing.T) {
	var s Keyed[string]
	a, b := s.Changed("a"), s.Changed("b")
	if a != s.Changed("a") {
		t.Fatal("two waiters of one key hold different channels before an announcement")
	}
	s.Announce("a")
	if !closed(a) {
		t.Fatal("Announce(a) left a's channel open")
	}
	if closed(b) {
		t.Fatal("Announce(a) closed b's channel")
	}
	if closed(s.Changed("a")) {
		t.Fatal("a channel taken after the announcement is already closed")
	}
}

func TestAnnouncementIsNotRememberedForALaterWaiter(t *testing.T) {
	var s Keyed[int]
	s.Announce(1)
	if closed(s.Changed(1)) {
		t.Fatal("an announcement nobody waited for closed the next waiter's channel")
	}
}

func TestAnyWakesOnEveryKeyAndAnnounceAllWakesEveryone(t *testing.T) {
	var s Keyed[string]
	anyCh := s.Any()
	s.Announce("x")
	if !closed(anyCh) {
		t.Fatal("Announce did not close Any's channel")
	}
	a, b, anyCh := s.Changed("a"), s.Changed("b"), s.Any()
	s.AnnounceAll()
	if !closed(a) || !closed(b) || !closed(anyCh) {
		t.Fatal("AnnounceAll left a waiter asleep")
	}
}

// TestTakingTheChannelBeforeReadingNeverMissesAWrite races a writer against a
// waiter that follows the contract: whatever order the scheduler picks, the
// waiter either reads the written value or holds a channel the write closed.
func TestTakingTheChannelBeforeReadingNeverMissesAWrite(t *testing.T) {
	for range 2000 {
		var s Keyed[string]
		var value atomic.Int32
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			value.Store(1)
			s.Announce("k")
		}()
		changed := s.Changed("k")
		if value.Load() != 1 {
			<-changed
			if value.Load() != 1 {
				t.Fatal("woke on the announcement without seeing the write before it")
			}
		}
		wg.Wait()
	}
}

func TestAwaitReturnsOnceReadyAndHonoursTheContext(t *testing.T) {
	var s Keyed[string]
	var value atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- s.Await(context.Background(), "k", func() bool { return value.Load() == 3 })
	}()
	for i := int32(1); i <= 3; i++ {
		value.Store(i)
		s.Announce("k")
	}
	if err := <-done; err != nil {
		t.Fatalf("Await: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Await(ctx, "k", func() bool { return false }); err != context.Canceled {
		t.Fatalf("Await on a cancelled context returned %v", err)
	}
}
