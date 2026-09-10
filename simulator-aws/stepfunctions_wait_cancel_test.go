package main

import (
	"testing"
	"time"
)

// A Wait state's duration is whatever the definition says: Seconds and
// Timestamp are accepted as given, and neither the step limit nor the nesting
// depth guard bounds them. Cancellation is therefore the only thing that ends a
// long Wait early, and every caller that cannot wait years depends on it —
// including FuzzSFNExecute, which parked a worker on a thirty-one-year timer
// until it was given a cancel channel that actually closes.
func TestWaitStateHonoursCancellationPromptly(t *testing.T) {
	const def = `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":999999999,"End":true}}}`

	cancel := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_, _, _ = sfnExecute(def, `{}`, cancel)
		close(done)
	}()

	// Still running: nothing bounds the wait on its own.
	select {
	case <-done:
		t.Fatal("a Wait of 999999999 seconds returned on its own; this test no longer proves anything")
	case <-time.After(200 * time.Millisecond):
	}

	close(cancel)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("closing cancel did not end the Wait state")
	}
}
