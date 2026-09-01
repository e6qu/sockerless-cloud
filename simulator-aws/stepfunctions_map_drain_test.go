package main

import (
	"testing"
	"time"
)

// TestSFN_MapCompletesWhileABackgroundDrainIsInProgress pins that a Map state's
// own fan-out is not the kind of work a drain may drop.
//
// simGo drops what it is handed once a drain has begun — correct for work that
// outlives the request that asked for it, and fatal for a fan-out the caller
// joins. A dropped worker leaves the feed blocked on a channel nobody reads; a
// dropped feed leaves the collector blocked on a channel nobody closes. Either
// way the Map never returns and its map run stays RUNNING for good, which is
// not a slow finish but no finish at all.
func TestSFN_MapCompletesWhileABackgroundDrainIsInProgress(t *testing.T) {
	simDraining.Store(true)
	t.Cleanup(func() { simDraining.Store(false) })

	definition := `{"StartAt":"M","States":{"M":{"Type":"Map","End":true,"ItemsPath":"$.items",
		"MaxConcurrency":2,
		"ItemProcessor":{"StartAt":"I","States":{"I":{"Type":"Pass","Result":"x","End":true}}}}}}`

	type result struct {
		output string
		status string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		out, status, err := sfnExecute(definition, `{"items":[1,2,3,4]}`, make(chan struct{}))
		done <- result{out, status, err}
	}()

	select {
	case got := <-done:
		if got.err != nil || got.status != "SUCCEEDED" || got.output != `["x","x","x","x"]` {
			t.Fatalf("map under a drain → %q %q %v", got.output, got.status, got.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the map never returned: its fan-out was dropped by the drain")
	}
}
