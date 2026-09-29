package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/cron"
)

// awsSchedule is a parsed at(), rate() or cron() expression, the grammar
// Amazon EventBridge Scheduler, Amazon EventBridge rules and Application Auto
// Scaling scheduled actions share.
type awsSchedule struct {
	at   time.Time
	rate time.Duration
	cron *cron.Schedule
}

// parseAWSSchedule reads expr with its fields in the named IANA time zone (UTC
// when empty). allowAt admits the one-time at() form, which EventBridge rules
// do not accept.
func parseAWSSchedule(expr, timezone string, allowAt bool) (awsSchedule, error) {
	loc := time.UTC
	if timezone != "" {
		var err error
		if loc, err = time.LoadLocation(timezone); err != nil {
			return awsSchedule{}, fmt.Errorf("invalid time zone %q", timezone)
		}
	}
	expr = strings.TrimSpace(expr)
	open := strings.IndexByte(expr, '(')
	if open < 0 || !strings.HasSuffix(expr, ")") {
		return awsSchedule{}, fmt.Errorf("invalid schedule expression %q", expr)
	}
	inner := expr[open+1 : len(expr)-1]
	switch expr[:open] {
	case "at":
		if !allowAt {
			return awsSchedule{}, fmt.Errorf("at() expressions are not supported here")
		}
		at, err := time.ParseInLocation("2006-01-02T15:04:05", inner, loc)
		if err != nil {
			return awsSchedule{}, fmt.Errorf("invalid at() expression %q", expr)
		}
		return awsSchedule{at: at}, nil
	case "rate":
		interval, err := awsRateInterval(inner)
		if err != nil {
			return awsSchedule{}, err
		}
		return awsSchedule{rate: interval}, nil
	case "cron":
		schedule, err := cron.Parse(inner, cron.AWS, loc)
		if err != nil {
			return awsSchedule{}, fmt.Errorf("invalid cron() expression %q: %w", expr, err)
		}
		return awsSchedule{cron: &schedule}, nil
	}
	return awsSchedule{}, fmt.Errorf("invalid schedule expression %q", expr)
}

// awsRateInterval reads "value unit", where the unit is singular for a value
// of 1 and plural otherwise.
func awsRateInterval(inner string) (time.Duration, error) {
	fields := strings.Fields(inner)
	if len(fields) != 2 {
		return 0, fmt.Errorf("invalid rate() expression %q", inner)
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid rate() value %q", fields[0])
	}
	units := map[string]time.Duration{"minute": time.Minute, "hour": time.Hour, "day": 24 * time.Hour}
	unit := fields[1]
	if n != 1 {
		if !strings.HasSuffix(unit, "s") {
			return 0, fmt.Errorf("rate() unit %q must be plural for a value of %d", unit, n)
		}
		unit = strings.TrimSuffix(unit, "s")
	}
	per, ok := units[unit]
	if !ok {
		return 0, fmt.Errorf("invalid rate() unit %q", fields[1])
	}
	return time.Duration(n) * per, nil
}

func (s awsSchedule) recurring() bool { return s.at.IsZero() }

// first is the first occurrence of a schedule that starts at start: the at()
// instant, one interval after start, or the first cron() match after it.
func (s awsSchedule) first(start time.Time) (time.Time, bool) {
	if !s.at.IsZero() {
		return s.at, true
	}
	return s.next(start)
}

func (s awsSchedule) next(after time.Time) (time.Time, bool) {
	switch {
	case s.cron != nil:
		return s.cron.Next(after)
	case s.rate > 0:
		return after.Add(s.rate), true
	}
	return time.Time{}, false
}
