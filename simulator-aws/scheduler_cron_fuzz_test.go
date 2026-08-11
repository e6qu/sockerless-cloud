package main

import (
	"testing"
	"time"
)

// FuzzSchedulerExpression fuzzes the EventBridge Scheduler expression validators and
// the cron "next fire" computation.
func FuzzSchedulerExpression(f *testing.F) {
	seeds := []string{
		"",
		"cron(0 12 * * ? *)",
		"cron(0/5 * * * ? *)",
		"cron(0 12 L * ? *)",
		"cron(0 12 LW * ? *)",
		"cron(0 12 15W * ? *)",
		"cron(0 12 ? * 2#1 *)",
		"cron(0 12 ? * MON-FRI *)",
		"cron(0 12 ? * 6L *)",
		"rate(5 minutes)",
		"at(2026-01-01T00:00:00)",
		"cron()",
		"cron(* * * * *)",
		"cron(* * * * * * *)",
		"cron(99 99 99 99 99 99)",
		"cron(0 0 1 1 1 99999999999999999999)",
		"cron(0/0 * * * ? *)",
		"cron(0 0 W * ? *)",
		"cron(0 0 ?# * ? *)",
		"cron(0 0 ? * #1 *)",
		"cron(0 0 -1 * ? *)",
		"cron(0 0 1-0 * ? *)",
		"cron(é * * * ? *)",
		"cron(\xff * * * ? *)",
		"cron(0 0 999999999999999999999W * ? *)",
		"at(not-a-date)",
		"rate(0 minutes)",
		"\\",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, expr string) {
		_ = schedulerExpressionValid(expr)
		_ = schedulerCronValid(expr)
		_, _ = schedulerCronNext(expr, after)
	})
}
