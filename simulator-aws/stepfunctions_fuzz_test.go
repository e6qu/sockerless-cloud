package main

import (
	"strings"
	"testing"
	"time"
)

// sfnFuzzExecutionBudget is how long one execution may run before the fuzz
// target aborts it: generous for a parse-and-dispatch execution and far under
// -fuzztime.
const sfnFuzzExecutionBudget = 2 * time.Second

// FuzzSFNExecute drives the Amazon States Language interpreter with arbitrary
// definitions and input. A pathologically nested definition must stop at the
// depth guard instead of overflowing the goroutine stack.
func FuzzSFNExecute(f *testing.F) {
	seeds := []string{
		`{"StartAt":"A","States":{"A":{"Type":"Pass","Result":"x","End":true}}}`,
		`{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.x","NumericGreaterThan":5,"Next":"B"}],"Default":"B"},"B":{"Type":"Pass","End":true}}}`,
		`{"StartAt":"P","States":{"P":{"Type":"Parallel","End":true,"Branches":[{"StartAt":"A","States":{"A":{"Type":"Pass","End":true}}}]}}}`,
		`{"StartAt":"M","States":{"M":{"Type":"Map","End":true,"ItemsPath":"$.items","ItemProcessor":{"StartAt":"I","States":{"I":{"Type":"Pass","End":true}}}}}}`,
		`{`,
		``,
		// A deeply self-nested Parallel, which the depth guard stops.
		sfnNestedParallel(5000),
	}
	for _, s := range seeds {
		f.Add(s, `{"items":[1,2,3]}`)
	}
	f.Fuzz(func(t *testing.T, def, input string) {
		// Nothing bounds a Wait state's Seconds or Timestamp, so
		// {"Type":"Wait","Seconds":999999999} would park sfnExecute for
		// thirty-one years, and the fuzz coordinator would hide the hang until
		// the input was saved and replayed. Close cancel after the budget, as
		// an execution stop or the state machine's TimeoutSeconds does.
		cancel := make(chan struct{})
		timer := time.AfterFunc(sfnFuzzExecutionBudget, func() { close(cancel) })
		defer timer.Stop()
		_, _, _ = sfnExecute(t.Context(), def, input, cancel)
	})
}

// sfnNestedParallel builds a definition whose single Parallel branch is itself a
// state machine with a single Parallel branch, n levels deep.
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
