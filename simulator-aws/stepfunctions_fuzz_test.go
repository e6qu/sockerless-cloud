package main

import (
	"strings"
	"testing"
)

// FuzzSFNExecute drives the ASL interpreter with an arbitrary state-machine
// definition and input. The interpreter recurses through Parallel/Map branches;
// a pathologically nested definition must not overflow the goroutine stack
// (a fatal, unrecoverable crash) — the depth guard caps it instead.
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
		cancel := make(chan struct{})
		// No timer-Wait runaway: cap by closing cancel immediately if needed
		// is unnecessary — the step limit + depth guard bound everything.
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
