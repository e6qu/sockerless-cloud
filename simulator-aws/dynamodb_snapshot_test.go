package main

import (
	"fmt"
	"sync"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestDDBItemSnapshotIsIndependentUnderConcurrentMutation(t *testing.T) {
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	ddbItems = sim.MakeStore[map[string]any](nil, "ddb_items")
	ddbResetItemLocks()

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

// Scan hands its key list to ddbBatchedSnapshots, which loads ddbSnapshotBatch
// items per acquisition of the table stripe instead of one. The refill boundary is the
// part worth pinning down: an off-by-one there silently drops or double-reads
// items, which surfaces as a Scan missing rows rather than as a crash.
func TestDDBBatchedSnapshotsSpanBatchBoundaries(t *testing.T) {
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	ddbItems = sim.MakeStore[map[string]any](nil, "ddb_items")
	ddbResetItemLocks()

	// Deliberately more than two batches, with holes so absent keys are covered.
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

	// A batched snapshot is still a copy: a later mutation of the stored item
	// must not be visible through one already handed out.
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
