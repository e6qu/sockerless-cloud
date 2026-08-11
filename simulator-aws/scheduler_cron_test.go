package main

import (
	"testing"
	"time"
)

func TestSchedulerCronNext(t *testing.T) {
	// A Wednesday, 2026-06-10 12:34:00 UTC.
	base := time.Date(2026, 6, 10, 12, 34, 0, 0, time.UTC)

	cases := []struct {
		name string
		expr string
		want time.Time
	}{
		{
			name: "daily at 02:00 next day",
			expr: "cron(0 2 * * ? *)",
			want: time.Date(2026, 6, 11, 2, 0, 0, 0, time.UTC),
		},
		{
			name: "every 15 minutes -> next quarter hour",
			expr: "cron(*/15 * * * ? *)",
			want: time.Date(2026, 6, 10, 12, 45, 0, 0, time.UTC),
		},
		{
			name: "weekdays 14:30 -> same Wednesday",
			expr: "cron(30 14 ? * MON-FRI *)",
			want: time.Date(2026, 6, 10, 14, 30, 0, 0, time.UTC),
		},
		{
			name: "specific minute list -> next listed minute",
			expr: "cron(0,30 * * * ? *)",
			want: time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC),
		},
		{
			// AWS start/step: 0/5 means minutes 0,5,…,55 (every 5 from 0), the
			// idiomatic clock-aligned form. Must not collapse to just minute 0.
			name: "start/step every 5 minutes (0/5)",
			expr: "cron(0/5 * * * ? *)",
			want: time.Date(2026, 6, 10, 12, 35, 0, 0, time.UTC),
		},
		{
			// Non-zero start: 2/10 means minutes 2,12,22,32,42,52.
			name: "start/step with offset (2/10)",
			expr: "cron(2/10 * * * ? *)",
			want: time.Date(2026, 6, 10, 12, 42, 0, 0, time.UTC),
		},
		{
			name: "Jan 1 midnight -> next year",
			expr: "cron(0 0 1 JAN ? *)",
			want: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "day-of-month restricted (15th 09:00)",
			expr: "cron(0 9 15 * ? *)",
			want: time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := schedulerCronNext(c.expr, base)
			if !ok {
				t.Fatalf("schedulerCronNext(%q) returned ok=false", c.expr)
			}
			if !got.Equal(c.want) {
				t.Fatalf("schedulerCronNext(%q) = %s, want %s", c.expr, got.Format(time.RFC3339), c.want.Format(time.RFC3339))
			}
		})
	}
}

// TestSchedulerCronWiring covers the firing-loop integration of cron: the loop
// must treat a cron schedule as recurring and compute a future first-fire time
// (the in-process dispatch itself is covered by TestScheduler_FiresECSTarget).
func TestSchedulerCronWiring(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s := Schedule{
		Name:               "cron-wire",
		GroupName:          "default",
		ScheduleExpression: "cron(0 2 * * ? *)",
		State:              "ENABLED",
		CreationDate:       float64(now.Unix()),
	}
	next, ok := schedulerFirstFire(s, now)
	if !ok || !next.After(now) {
		t.Fatalf("schedulerFirstFire(cron) = %s ok=%v, want a future time", next, ok)
	}
	if !schedulerRecurring(s.ScheduleExpression) {
		t.Fatal("cron(...) must be recurring so it re-fires and isn't auto-deleted")
	}
}

// TestSchedulerCronQualifiers covers the AWS L / W / # qualifiers, which real
// EventBridge fires correctly. base is Wednesday 2026-06-10 12:00:00 UTC.
func TestSchedulerCronQualifiers(t *testing.T) {
	base := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		expr string
		want time.Time
	}{
		{"L last day of month", "cron(0 0 L * ? *)", time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)},
		{"LW last weekday (Jun 30 Tue)", "cron(0 0 LW * ? *)", time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)},
		{"15W nearest weekday (Jun 15 Mon)", "cron(0 0 15W * ? *)", time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)},
		{"1W shifts Sat->Mon, no month cross (Aug 1 Sat -> Aug 3)", "cron(0 0 1W 8 ? *)", time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)},
		{"6#3 third Friday (Jun 19)", "cron(0 0 ? * 6#3 *)", time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)},
		{"6L last Friday (Jun 26)", "cron(0 0 ? * 6L *)", time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)},
		{"L day-of-week = Saturday (Jun 13)", "cron(0 0 ? * L *)", time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := schedulerCronNext(c.expr, base)
			if !ok {
				t.Fatalf("schedulerCronNext(%q) returned ok=false", c.expr)
			}
			if !got.Equal(c.want) {
				t.Fatalf("schedulerCronNext(%q) = %s, want %s", c.expr, got.Format(time.RFC3339), c.want.Format(time.RFC3339))
			}
		})
	}
}

func TestSchedulerCronNext_UnsupportedOrInvalid(t *testing.T) {
	base := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	for _, expr := range []string{
		"cron(0 2 * * ?)",     // only 5 fields (not 6)
		"rate(5 minutes)",     // not a cron expression
		"cron(99 2 * * ? *)",  // minute out of range
		"cron(0 2 ? * 8#1 *)", // day-of-week 8 invalid
		"cron(0 2 ? * 2#9 *)", // nth occurrence 9 invalid
		"cron(0 2 32W * ? *)", // day 32 invalid
	} {
		if _, ok := schedulerCronNext(expr, base); ok {
			t.Fatalf("schedulerCronNext(%q) returned ok=true, want false", expr)
		}
	}
}

func TestSchedulerCronValid(t *testing.T) {
	valid := []string{
		"cron(0 12 * * ? *)",
		"cron(0/5 * * * ? *)",
		"cron(0 0 L * ? *)",
		"cron(0 0 15W * ? *)",
		"cron(0 0 ? * 6#3 *)",
		"cron(0 0 ? * 6L *)",
	}
	for _, e := range valid {
		if !schedulerCronValid(e) {
			t.Errorf("schedulerCronValid(%q) = false, want true", e)
		}
	}
	invalid := []string{
		"cron(0 0 * * ?)",     // 5 fields
		"cron(99 0 * * ? *)",  // minute out of range
		"cron(0 0 ? * 8#1 *)", // dow 8 invalid
		"cron(0 0 32W * ? *)", // day 32 invalid
		"cron()",              // empty
	}
	for _, e := range invalid {
		if schedulerCronValid(e) {
			t.Errorf("schedulerCronValid(%q) = true, want false", e)
		}
	}
}
