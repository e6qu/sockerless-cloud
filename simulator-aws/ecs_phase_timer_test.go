// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"
	"time"
)

func TestECSPhaseTimerAttributesEachPhase(t *testing.T) {
	clock := time.Date(2026, 9, 7, 19, 4, 58, 0, time.UTC)
	now := func() time.Time { return clock }
	timer := newECSPhaseTimer(now)

	clock = clock.Add(120 * time.Millisecond)
	timer.Mark("log-stream")
	clock = clock.Add(167 * time.Second)
	timer.Mark("vpc-netns")
	clock = clock.Add(16 * time.Millisecond)
	timer.Mark("image-pull")
	clock = clock.Add(400 * time.Millisecond)

	got := timer.Summary()
	want := "total=2m47.536s log-stream=120ms vpc-netns=2m47s image-pull=16ms"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}
