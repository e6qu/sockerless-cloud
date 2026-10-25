package uniquename

import (
	"regexp"
	"sync"
	"testing"
)

func TestNewIsDistinctAcrossGoroutines(t *testing.T) {
	const workers, perWorker = 8, 1000
	shape := regexp.MustCompile(`^bucket-[0-9a-f]{8}$`)
	var mu sync.Mutex
	seen := make(map[string]bool, workers*perWorker)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range perWorker {
				name := New("bucket")
				mu.Lock()
				dup := seen[name]
				seen[name] = true
				mu.Unlock()
				if dup {
					t.Errorf("New returned %s twice", name)
				}
				if !shape.MatchString(name) {
					t.Errorf("New returned %s, want bucket- and eight hex digits", name)
				}
			}
		})
	}
	wg.Wait()
}
