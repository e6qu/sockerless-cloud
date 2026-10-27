package main

import (
	"testing"
	"time"
)

// Neither the step limit nor the nesting-depth guard bounds a Wait state's
// Seconds or Timestamp, so only cancellation ends a long Wait early; every
// caller that cannot wait years, FuzzSFNExecute among them, depends on it.
func TestWaitStateHonoursCancellationPromptly(t *testing.T) {
	const def = `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":999999999,"End":true}}}`

	cancel := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_, _, _ = sfnExecute(t.Context(), def, `{}`, cancel)
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
