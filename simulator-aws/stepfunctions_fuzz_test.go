package main

import (
	"strings"
	"testing"
	"time"
)

// FuzzSFNExecute drives the ASL interpreter with an arbitrary state-machine
// definition and input. The interpreter recurses through Parallel/Map branches;
// a pathologically nested definition must not overflow the goroutine stack
// (a fatal, unrecoverable crash) — the depth guard caps it instead.
// sfnFuzzExecutionBudget is how long one execution may run before the fuzz
// target aborts it. Generous next to a parse-and-dispatch execution, and far
// under the per-target -fuzztime, so a bounded run never trips it.
const sfnFuzzExecutionBudget = 2 * time.Second

func FuzzSFNExecute(f *testing.F) {
	seeds := []string{
		`{"StartAt":"A","States":{"A":{"Type":"Pass","Result":"x","End":true}}}`,
		`{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.x","NumericGreaterThan":5,"Next":"B"}],"Default":"B"},"B":{"Type":"Pass","End":true}}}`,
		`{"StartAt":"P","States":{"P":{"Type":"Parallel","End":true,"Branches":[{"StartAt":"A","States":{"A":{"Type":"Pass","End":true}}}]}}}`,
		`{"StartAt":"M","States":{"M":{"Type":"Map","End":true,"ItemsPath":"$.items","ItemProcessor":{"StartAt":"I","States":{"I":{"Type":"Pass","End":true}}}}}}`,
		`{`,
		``,
		// A deeply self-nested Parallel — the crash case the depth guard catches.
		sfnNestedParallel(5000),
	}
	for _, s := range seeds {
		f.Add(s, `{"items":[1,2,3]}`)
	}
	f.Fuzz(func(t *testing.T, def, input string) {
		// A Wait state's duration is bounded by neither the step limit nor the
		// depth guard: Seconds and Timestamp are accepted as given, so
		// `{"Type":"Wait","Seconds":999999999}` parks sfnExecute on a timer for
		// thirty-one years. `cancel` is the only way out, and a channel that is
		// created and never closed is no way out at all. The worker then sits
		// idle rather than crashing, so the coordinator reports the run as
		// passing at the -fuzztime boundary and the hang is invisible — until
		// the input is saved as interesting and every later run replays it while
		// gathering baseline coverage, which is what killed the nightly job.
		//
		// Cancellation is what the production callers supply (an execution stop,
		// or the state machine's own TimeoutSeconds), so bounding it here
		// exercises the same path a real abort does.
		cancel := make(chan struct{})
		timer := time.AfterFunc(sfnFuzzExecutionBudget, func() { close(cancel) })
		defer timer.Stop()
		_, _, _ = sfnExecute(def, input, cancel)
	})
}

// sfnNestedParallel builds a definition whose single Parallel branch is itself a
// state machine with a single Parallel branch, n levels deep. Pre-guard this
// recursed n times and overflowed the stack.
func sfnNestedParallel(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(`{"StartAt":"P","States":{"P":{"Type":"Parallel","End":true,"Branches":[`)
	}
	b.WriteString(`{"StartAt":"L","States":{"L":{"Type":"Pass","End":true}}}`)
	for i := 0; i < n; i++ {
		b.WriteString(`]}}}`)
	}
	return b.String()
}
