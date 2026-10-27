package main

import (
	"fmt"
	"sync"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

func TestDDBItemSnapshotIsIndependentUnderConcurrentMutation(t *testing.T) {
	// Let earlier background work finish before replacing its stores.
	bg.Await()
	ddbItems = sim.MakeStore[map[string]any](nil, "ddb_items")

	const itemKey = "snap/S#item"
	item := map[string]any{
		"pk":      map[string]any{"S": "item"},
		"version": map[string]any{"N": "0"},
		"payload": map[string]any{"M": map[string]any{
			"body": map[string]any{"S": "initial"},
		}},
	}
	ddbItems.Put(itemKey, item)

	snap, ok := ddbItemSnapshot(itemKey)
	if !ok {
		t.Fatal("snapshot missing stored item")
	}
	release := ddbLockItemKeys(true, itemKey)
	item["version"] = map[string]any{"N": "1"}
	item["payload"].(map[string]any)["M"].(map[string]any)["body"] = map[string]any{"S": "mutated"}
	release()
	if got := snap["version"].(map[string]any)["N"]; got != "0" {
		t.Fatalf("snapshot version changed with stored item: %v", got)
	}
	body := snap["payload"].(map[string]any)["M"].(map[string]any)["body"].(map[string]any)["S"]
	if body != "initial" {
		t.Fatalf("nested snapshot changed with stored item: %v", body)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for n := 0; n < 1000; n++ {
				s, ok := ddbItemSnapshot(itemKey)
				if !ok {
					t.Errorf("worker %d: snapshot missing at iteration %d", worker, n)
					return
				}
				_ = ddbReadUnits(s, false)
			}
		}(i)
	}
	for n := 0; n < 1000; n++ {
		writeRelease := ddbLockItemKeys(true, itemKey)
		item["version"] = map[string]any{"N": fmt.Sprintf("%d", n)}
		item[fmt.Sprintf("attr-%d", n%16)] = map[string]any{"S": fmt.Sprintf("value-%d", n)}
		writeRelease()
	}
	wg.Wait()
}

// An off-by-one at the refill boundary would make Scan drop or double-read
// rows.
func TestDDBBatchedSnapshotsSpanBatchBoundaries(t *testing.T) {
	// Let earlier background work finish before replacing its stores.
	bg.Await()
	ddbItems = sim.MakeStore[map[string]any](nil, "ddb_items")

	// More than two batches, with holes for absent keys.
	const total = ddbSnapshotBatch*2 + 37
	keys := make([]string, 0, total)
	stored := map[string]bool{}
	for i := 0; i < total; i++ {
		key := fmt.Sprintf("batch/S#item-%04d", i)
		keys = append(keys, key)
		if i%7 == 0 {
			continue // no stored item for this key
		}
		ddbItems.Put(key, map[string]any{
			"pk":    map[string]any{"S": fmt.Sprintf("item-%04d", i)},
			"index": map[string]any{"N": fmt.Sprintf("%d", i)},
		})
		stored[key] = true
	}

	snapshots := newDDBBatchedSnapshots(keys)
	seen := 0
	for i, key := range keys {
		item, ok := snapshots.at(i, key)
		if ok != stored[key] {
			t.Fatalf("key %d (%s): got found=%v, want %v", i, key, ok, stored[key])
		}
		if !ok {
			continue
		}
		seen++
		got := item["index"].(map[string]any)["N"]
		if want := fmt.Sprintf("%d", i); got != want {
			t.Fatalf("key %d: got index %v, want %v", i, got, want)
		}
	}
	if seen != len(stored) {
		t.Fatalf("visited %d stored items, want %d", seen, len(stored))
	}

	// A batched snapshot is a copy.
	first := keys[1]
	item, ok := newDDBBatchedSnapshots(keys).at(1, first)
	if !ok {
		t.Fatal("expected a stored item")
	}
	mutate := ddbLockItemKeys(true, first)
	live, _ := ddbItems.Get(first)
	live["index"] = map[string]any{"N": "999"}
	ddbItems.Put(first, live)
	mutate()
	if got := item["index"].(map[string]any)["N"]; got != "1" {
		t.Fatalf("snapshot observed a later mutation: %v", got)
	}
}
