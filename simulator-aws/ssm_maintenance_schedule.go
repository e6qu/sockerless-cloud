package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
)

// ssmWindowPlan is a maintenance window's schedule read as
// ssm-maintenance-window-schedule-options.md.gz describes it: the cron or rate
// expression in ScheduleTimezone, cron occurrences moved ScheduleOffset days
// later, and only occurrences inside StartDate and EndDate.
type ssmWindowPlan struct {
	schedule awsSchedule
	seconds  []int
	offset   int
	start    time.Time
	end      time.Time
	anchor   time.Time
}

// ssmParseWindowSchedule reads a window's schedule; the maintenance window
// cron form takes an optional leading seconds field
// (ssm-cron-and-rate-expressions.md.gz).
func ssmParseWindowSchedule(expr, timezone string) (awsSchedule, []int, error) {
	expr = strings.TrimSpace(expr)
	seconds := []int{0}
	if inner, ok := strings.CutPrefix(expr, "cron("); ok && strings.HasSuffix(inner, ")") {
		fields := strings.Fields(strings.TrimSuffix(inner, ")"))
		if len(fields) == 7 {
			var err error
			if seconds, err = ssmCronSeconds(fields[0]); err != nil {
				return awsSchedule{}, nil, err
			}
			expr = "cron(" + strings.Join(fields[1:], " ") + ")"
		}
	}
	schedule, err := parseAWSSchedule(expr, timezone, true)
	if err != nil {
		return awsSchedule{}, nil, err
	}
	return schedule, seconds, nil
}

// ssmCronSeconds lists the seconds a seconds field names by reading it in the
// minutes position of a one-hour expression, whose grammar it shares.
func ssmCronSeconds(field string) ([]int, error) {
	probe, err := cron.Parse(field+" 0 1 1 ? 2000", cron.AWS, time.UTC)
	if err != nil {
		return nil, fmt.Errorf("invalid seconds field %q", field)
	}
	var seconds []int
	at := time.Date(1999, 12, 31, 23, 59, 59, 0, time.UTC)
	for {
		next, ok := probe.Next(at)
		if !ok || next.Hour() != 0 {
			break
		}
		seconds = append(seconds, next.Minute())
		at = next
	}
	return seconds, nil
}

// ssmWindowDate reads a StartDate or EndDate, an ISO-8601 timestamp.
func ssmWindowDate(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04Z07:00", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid ISO-8601 date %q", value)
}

func ssmWindowPlanFor(m SSMMaintenanceWindow) (ssmWindowPlan, error) {
	schedule, seconds, err := ssmParseWindowSchedule(m.Schedule, m.ScheduleTimezone)
	if err != nil {
		return ssmWindowPlan{}, err
	}
	plan := ssmWindowPlan{schedule: schedule, seconds: seconds}
	if schedule.cron != nil {
		plan.offset = m.ScheduleOffset
	}
	if m.StartDate != "" {
		if plan.start, err = ssmWindowDate(m.StartDate); err != nil {
			return ssmWindowPlan{}, err
		}
	}
	if m.EndDate != "" {
		if plan.end, err = ssmWindowDate(m.EndDate); err != nil {
			return ssmWindowPlan{}, err
		}
	}
	return plan, nil
}

func (p ssmWindowPlan) within(t time.Time, ok bool) (time.Time, bool) {
	if !ok || (!p.end.IsZero() && t.After(p.end)) {
		return time.Time{}, false
	}
	return t, true
}

// next is the first run strictly after after.
func (p ssmWindowPlan) next(after time.Time) (time.Time, bool) {
	switch {
	case !p.schedule.at.IsZero():
		if p.schedule.at.After(after) {
			return p.within(p.schedule.at, true)
		}
		return time.Time{}, false
	case p.schedule.rate > 0:
		if after.Before(p.anchor) {
			return p.within(p.anchor.Add(p.schedule.rate), true)
		}
		steps := after.Sub(p.anchor)/p.schedule.rate + 1
		return p.within(p.anchor.Add(steps*p.schedule.rate), true)
	}
	from := after.AddDate(0, 0, -p.offset)
	minute := from.Truncate(time.Minute).Add(-time.Nanosecond)
	for {
		occurrence, ok := p.schedule.cron.Next(minute)
		if !ok {
			return time.Time{}, false
		}
		for _, second := range p.seconds {
			run := occurrence.Add(time.Duration(second)*time.Second).AddDate(0, 0, p.offset)
			if run.After(after) {
				return p.within(run, true)
			}
		}
		minute = occurrence
	}
}

// first is the first run of a window the ticker meets at now. A rate
// expression counts its intervals from StartDate, or from when the schedule was
// last set; a cron run whose offset date has passed waits for the next period.
func (p ssmWindowPlan) first(now time.Time) (time.Time, bool) {
	from := now
	if !p.start.IsZero() && p.start.After(from) {
		from = p.start
	}
	if p.schedule.rate > 0 {
		return p.next(now)
	}
	if p.offset > 0 {
		if cronRun, ok := p.schedule.cron.Next(from); ok {
			second := time.Duration(p.seconds[0]) * time.Second
			return p.within(cronRun.Add(second).AddDate(0, 0, p.offset), true)
		}
		return time.Time{}, false
	}
	return p.next(from.Add(-time.Nanosecond))
}

func ssmWindowARN(windowID string) string {
	return "arn:aws:ssm:" + awsRegion() + ":" + awsAccountID() + ":maintenancewindow/" + windowID
}

// ssmWindowTicker fires each enabled window on its schedule.
var ssmWindowTicker *cron.Ticker

func startSSMMaintenanceWindows(srv *sim.Server) {
	records := sim.MakeStore[cron.Record](srv.DB(), "ssm_maintenance_window_runs")
	ssmWindowTicker = cron.NewTicker(records, ssmWindowEntries)
	ssmWindowTicker.Start(srv, "AWS Systems Manager maintenance windows", time.Second)
}

func ssmWindowEntries() []cron.Entry {
	var entries []cron.Entry
	for _, m := range ssmWindows.List() {
		if !m.Enabled {
			continue
		}
		plan, err := ssmWindowPlanFor(m)
		if err != nil {
			continue
		}
		windowID := m.WindowId
		if plan.schedule.rate > 0 {
			plan.anchor = time.Unix(int64(m.ModifiedDate), 0)
			if !plan.start.IsZero() {
				plan.anchor = plan.start
			}
		}
		entry := cron.Entry{
			Key:     windowID,
			Spec:    fmt.Sprintf("%s|%s|%d|%s|%s", m.Schedule, m.ScheduleTimezone, m.ScheduleOffset, m.StartDate, m.EndDate),
			First:   plan.first,
			MaxLate: time.Duration(m.Duration-m.Cutoff) * time.Hour,
			Fire:    func(scheduled time.Time) { ssmStartWindowExecution(windowID, scheduled) },
		}
		if plan.schedule.recurring() {
			entry.Next = plan.next
		}
		entries = append(entries, entry)
	}
	return entries
}

// ssmWindowNextRun is the window's next run, for NextExecutionTime and
// DescribeMaintenanceWindowSchedule.
func ssmWindowNextRun(m SSMMaintenanceWindow, now time.Time) (time.Time, bool) {
	if !m.Enabled {
		return time.Time{}, false
	}
	plan, err := ssmWindowPlanFor(m)
	if err != nil {
		return time.Time{}, false
	}
	if plan.schedule.rate > 0 {
		plan.anchor = time.Unix(int64(m.ModifiedDate), 0)
		if !plan.start.IsZero() {
			plan.anchor = plan.start
		}
		if now.Before(plan.anchor) {
			now = plan.anchor
		}
	} else if !plan.start.IsZero() && plan.start.After(now) {
		now = plan.start
	}
	return plan.next(now)
}

func ssmWindowTimeWire(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
