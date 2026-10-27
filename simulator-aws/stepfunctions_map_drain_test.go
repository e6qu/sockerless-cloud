package main

import (
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// TestSFN_MapCompletesWhileABackgroundDrainIsInProgress pins that a drain never
// drops a Map state's own fan-out. bg.Go drops work once a drain begins, which
// suits work that outlives its request but not a fan-out the caller joins: a
// dropped worker or feed blocks the Map forever and leaves its map run RUNNING.
func TestSFN_MapCompletesWhileABackgroundDrainIsInProgress(t *testing.T) {
	hold := make(chan struct{})
	bg.Go(func() { <-hold })
	drained := make(chan struct{})
	go func() {
		bg.Await()
		close(drained)
	}()
	t.Cleanup(func() {
		close(hold)
		<-drained
	})
	for !bg.Draining() {
		time.Sleep(time.Millisecond)
	}

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
		out, status, err := sfnExecute(t.Context(), definition, `{"items":[1,2,3,4]}`, make(chan struct{}))
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
