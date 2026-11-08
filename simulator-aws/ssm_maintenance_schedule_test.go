package main

import (
	"testing"
	"time"
)

func mustWindowPlan(t *testing.T, m SSMMaintenanceWindow) ssmWindowPlan {
	t.Helper()
	plan, err := ssmWindowPlanFor(m)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// The examples are ssm-maintenance-window-schedule-options.md.gz's.
func TestSSMWindowPlan_StartDateAndTimezone(t *testing.T) {
	plan := mustWindowPlan(t, SSMMaintenanceWindow{Schedule: "cron(0 09 ? * WED *)",
		ScheduleTimezone: "America/Los_Angeles", StartDate: "2021-01-01T00:00:00-08:00"})
	first, ok := plan.first(time.Date(2020, 12, 1, 0, 0, 0, 0, time.UTC))
	la, _ := time.LoadLocation("America/Los_Angeles")
	if want := time.Date(2021, 1, 6, 9, 0, 0, 0, la); !ok || !first.Equal(want) {
		t.Fatalf("first run = %v %v, want %v", first, ok, want)
	}
}

func TestSSMWindowPlan_RateBetweenStartAndEnd(t *testing.T) {
	m := SSMMaintenanceWindow{Schedule: "rate(7 days)", ScheduleTimezone: "Asia/Tokyo",
		StartDate: "2019-01-01T00:03:15+09:00", EndDate: "2019-06-30T00:06:15+09:00"}
	plan := mustWindowPlan(t, m)
	plan.anchor = plan.start
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	var last time.Time
	for run, ok := plan.first(time.Date(2018, 12, 1, 0, 0, 0, 0, time.UTC)); ok; run, ok = plan.next(run) {
		if run.In(tokyo).Weekday() != time.Tuesday || run.In(tokyo).Format("15:04:05") != "00:03:15" {
			t.Fatalf("run %v is not Tuesday 00:03:15 JST", run.In(tokyo))
		}
		last = run
	}
	if want := time.Date(2019, 6, 25, 0, 3, 15, 0, tokyo); !last.Equal(want) {
		t.Fatalf("last run = %v, want %v", last.In(tokyo), want)
	}
}

func TestSSMWindowPlan_OffsetAndSecondsField(t *testing.T) {
	plan := mustWindowPlan(t, SSMMaintenanceWindow{Schedule: "cron(0 30 23 ? * TUE#3 *)", ScheduleOffset: 2})
	run, ok := plan.next(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 11, 19, 23, 30, 0, 0, time.UTC); !ok || !run.Equal(want) {
		t.Fatalf("run = %v, want two days after the third Tuesday, %v", run, want)
	}
	seconds := mustWindowPlan(t, SSMMaintenanceWindow{Schedule: "cron(24 30 09 ? * * *)"})
	run, ok = seconds.next(time.Date(2026, 11, 1, 9, 30, 24, 0, time.UTC))
	if want := time.Date(2026, 11, 2, 9, 30, 24, 0, time.UTC); !ok || !run.Equal(want) {
		t.Fatalf("run = %v, want %v", run, want)
	}
}

func TestSSMWindowPlan_AtRunsOnce(t *testing.T) {
	plan := mustWindowPlan(t, SSMMaintenanceWindow{Schedule: "at(2030-07-07T15:55:00)"})
	run, ok := plan.first(time.Date(2030, 7, 1, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2030, 7, 7, 15, 55, 0, 0, time.UTC); !ok || !run.Equal(want) {
		t.Fatalf("run = %v, want %v", run, want)
	}
	if _, ok := plan.next(run); ok {
		t.Fatal("an at() window runs once")
	}
}
