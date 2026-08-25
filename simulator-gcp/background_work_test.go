package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// The drain is a barrier: work started before it completes inside it, and
// work requested while it runs is dropped rather than admitted — a barrier
// that kept admitting chained work would wait on a group that never empties.
func TestAwaitSimulatorBackgroundDrainsStartedWorkAndDropsWorkRequestedMidDrain(t *testing.T) {
	var completed atomic.Int32
	release := make(chan struct{})
	simGo(func() {
		<-release
		completed.Add(1)
	})

	drained := make(chan struct{})
	go func() {
		AwaitSimulatorBackground()
		close(drained)
	}()

	select {
	case <-drained:
		t.Fatal("the drain returned while started work was still running")
	case <-time.After(50 * time.Millisecond):
	}

	// Work requested mid-drain is dropped: nothing may touch the stores the
	// barrier protects once it has begun.
	var admittedMidDrain atomic.Int32
	simGo(func() { admittedMidDrain.Add(1) })

	close(release)
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain did not return after the started work completed")
	}
	if completed.Load() != 1 {
		t.Fatalf("started work must complete inside the drain; completed=%d", completed.Load())
	}
	if admittedMidDrain.Load() != 0 {
		t.Fatalf("work requested mid-drain must be dropped, not run; ran=%d", admittedMidDrain.Load())
	}

	// After the drain ends, new work is admitted again.
	post := make(chan struct{})
	simGo(func() { close(post) })
	select {
	case <-post:
	case <-time.After(5 * time.Second):
		t.Fatal("work requested after the drain must run")
	}
	AwaitSimulatorBackground()
}
