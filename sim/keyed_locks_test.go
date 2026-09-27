package sim

import (
	"sync"
	"testing"
	"time"
)

func TestKeyedLocksSerializeAKeyAndForgetItAfterward(t *testing.T) {
	locks := NewKeyedLocks()
	var wg sync.WaitGroup
	var mu sync.Mutex
	inside, most := 0, 0
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := locks.Lock("object")
			mu.Lock()
			inside++
			most = max(most, inside)
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			inside--
			mu.Unlock()
			release()
		}()
	}
	wg.Wait()
	if most != 1 {
		t.Fatalf("%d holders of one key at once, want 1", most)
	}
	if locks.Len() != 0 {
		t.Fatalf("%d entries remain after every holder released", locks.Len())
	}
}

func TestKeyedLocksLeaveOtherKeysFree(t *testing.T) {
	locks := NewKeyedLocks()
	release := locks.Lock("a")
	defer release()
	done := make(chan struct{})
	go func() {
		locks.Lock("b")()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a held key blocked a different one")
	}
}
