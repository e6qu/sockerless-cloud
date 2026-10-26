// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"strings"
	"time"
)

// Amazon ECS exposes only pullStartedAt and pullStoppedAt; this logs every step
// of PROVISIONING → RUNNING so a slow start can be attributed.
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

// Closes the phase since the previous mark, recording it as name.
func (p *ecsPhaseTimer) Mark(name string) {
	t := p.now()
	p.phases = append(p.phases, fmt.Sprintf("%s=%s", name, t.Sub(p.last).Round(time.Millisecond)))
	p.last = t
}

// The total, then every phase in order.
func (p *ecsPhaseTimer) Summary() string {
	return fmt.Sprintf("total=%s %s", p.now().Sub(p.start).Round(time.Millisecond), strings.Join(p.phases, " "))
}
