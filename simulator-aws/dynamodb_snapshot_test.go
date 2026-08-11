package main

import (
	"fmt"
	"sync"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

func TestDDBItemSnapshotIsIndependentUnderConcurrentMutation(t *testing.T) {
	ddbItems = sim.MakeStore[map[string]any](nil, "ddb_items")
	ddbItemsMu = sync.Mutex{}

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
	ddbItemsMu.Lock()
	item["version"] = map[string]any{"N": "1"}
	item["payload"].(map[string]any)["M"].(map[string]any)["body"] = map[string]any{"S": "mutated"}
	ddbItemsMu.Unlock()
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
		ddbItemsMu.Lock()
		item["version"] = map[string]any{"N": fmt.Sprintf("%d", n)}
		item[fmt.Sprintf("attr-%d", n%16)] = map[string]any{"S": fmt.Sprintf("value-%d", n)}
		ddbItemsMu.Unlock()
	}
	wg.Wait()
}
