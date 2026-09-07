// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"strings"
	"time"
)

// ecsPhaseTimer records how long each step of a task's PROVISIONING→RUNNING
// lifecycle took, so a slow start can be attributed from the simulator's own
// log instead of bisected from outside. Amazon ECS exposes only
// pullStartedAt/pullStoppedAt; everything before the pull (volume
// preparation, the pause container, the VPC namespace attach) was a silent
// window: on 2026-09-07 a task waited 168 s there with nothing logged
// (sockerless-cloud#139).
type ecsPhaseTimer struct {
	now    func() time.Time
	start  time.Time
	last   time.Time
	phases []string
}

func newECSPhaseTimer(now func() time.Time) *ecsPhaseTimer {
	t := now()
	return &ecsPhaseTimer{now: now, start: t, last: t}
}

// Mark closes the phase that began at the previous mark (or at construction)
// and records it under name.
func (p *ecsPhaseTimer) Mark(name string) {
	t := p.now()
	p.phases = append(p.phases, fmt.Sprintf("%s=%s", name, t.Sub(p.last).Round(time.Millisecond)))
	p.last = t
}

// Summary is one log-line fragment: the total since construction, then every
// phase in order.
func (p *ecsPhaseTimer) Summary() string {
	return fmt.Sprintf("total=%s %s", p.now().Sub(p.start).Round(time.Millisecond), strings.Join(p.phases, " "))
}
