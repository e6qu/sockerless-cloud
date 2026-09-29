package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
)

func TestParseAWSScheduleForms(t *testing.T) {
	base := time.Date(2026, 6, 10, 12, 34, 0, 0, time.UTC)
	cases := []struct {
		expr, tz string
		want     time.Time
	}{
		{"cron(0 2 * * ? *)", "", time.Date(2026, 6, 11, 2, 0, 0, 0, time.UTC)},
		{"cron(30 14 ? * MON-FRI *)", "", time.Date(2026, 6, 10, 14, 30, 0, 0, time.UTC)},
		{"cron(0 0 ? * 6#3 *)", "", time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)},
		// The fields are read in ScheduleExpressionTimezone: 09:00 in Tokyo is 00:00 UTC.
		{"cron(0 9 * * ? *)", "Asia/Tokyo", time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)},
		{"rate(5 minutes)", "", base.Add(5 * time.Minute)},
		{"rate(1 hour)", "", base.Add(time.Hour)},
		{"rate(2 days)", "", base.Add(48 * time.Hour)},
		{"at(2026-07-01T08:00:00)", "", time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)},
		{"at(2026-07-01T08:00:00)", "Europe/Berlin", time.Date(2026, 7, 1, 6, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		plan, err := parseAWSSchedule(c.expr, c.tz, true)
		if err != nil {
			t.Fatalf("parseAWSSchedule(%q, %q): %v", c.expr, c.tz, err)
		}
		got, ok := plan.first(base)
		if !ok || !got.Equal(c.want) {
			t.Errorf("first(%q in %q) = %s (%v), want %s", c.expr, c.tz, got.UTC(), ok, c.want)
		}
	}
	for _, expr := range []string{
		"", "cron()", "cron(0 2 * * ?)", "cron(99 2 * * ? *)", "cron(0 2 * * * *)",
		"rate(0 minutes)", "rate(1 minutes)", "rate(5 minute)", "rate(5 weeks)",
		"at(not-a-date)", "every(5 minutes)",
	} {
		if _, err := parseAWSSchedule(expr, "", true); err == nil {
			t.Errorf("parseAWSSchedule(%q) accepted it", expr)
		}
	}
	if _, err := parseAWSSchedule("cron(0 2 * * ? *)", "Mars/Olympus_Mons", true); err == nil {
		t.Error("an unknown time zone was accepted")
	}
	if _, err := parseAWSSchedule("at(2026-07-01T08:00:00)", "", false); err == nil {
		t.Error("at() was accepted where only rate() and cron() are")
	}
}

// TestSchedulerFiresInItsTimeZoneWithinItsDates drives the firing loop at chosen
// instants: a cron schedule fires at its wall-clock time in its own time zone,
// never before StartDate and never after EndDate.
func TestSchedulerFiresInItsTimeZoneWithinItsDates(t *testing.T) {
	_, router, _ := buildConformanceSimulator(t)
	schedulesStore := sim.NewStateStore[Schedule]()
	records := sim.NewStateStore[cron.Record]()
	queueURL, queueARN := testSQSQueue(t, router, "scheduler-tz-queue")

	target, _ := json.Marshal(map[string]any{"Arn": queueARN, "Input": "tick"})
	start := float64(time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC).Unix())
	end := float64(time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC).Unix())
	schedulesStore.Put("default/tokyo", Schedule{
		Name: "tokyo", GroupName: "default", State: "ENABLED",
		ScheduleExpression: "cron(0 9 * * ? *)", ScheduleExpressionTimezone: "Asia/Tokyo",
		StartDate: &start, EndDate: &end, Target: target,
	})
	ticker := cron.NewTicker(records, func() []cron.Entry { return schedulerEntries(schedulesStore, records) })

	ticker.Tick(time.Date(2026, 6, 10, 23, 59, 0, 0, time.UTC))
	rec, _ := records.Get("default/tokyo")
	if want := time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC); !rec.Next.Equal(want) {
		t.Fatalf("first occurrence = %s, want 09:00 Tokyo on the first day after StartDate (%s)", rec.Next, want)
	}
	ticker.Tick(time.Date(2026, 6, 13, 0, 0, 1, 0, time.UTC))
	if got := awaitSQSMessage(t, router, queueURL, 5*time.Second); got.Body != "tick" {
		t.Fatalf("the occurrence delivered %q, want the target's Input", got.Body)
	}
	if rec, _ := records.Get("default/tokyo"); !rec.Done {
		t.Fatalf("the occurrence after EndDate was scheduled: %+v", rec)
	}
}
