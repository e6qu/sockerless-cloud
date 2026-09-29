package cron

import (
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func mustNext(t *testing.T, expr string, d Dialect, loc *time.Location, after time.Time) time.Time {
	t.Helper()
	s, err := Parse(expr, d, loc)
	if err != nil {
		t.Fatalf("Parse(%q): %v", expr, err)
	}
	next, ok := s.Next(after)
	if !ok {
		t.Fatalf("Next(%q) found no occurrence", expr)
	}
	return next
}

func TestAWSDialectOccurrences(t *testing.T) {
	// Wednesday 2026-06-10 12:34 UTC.
	base := time.Date(2026, 6, 10, 12, 34, 0, 0, time.UTC)
	for _, c := range []struct {
		expr string
		want time.Time
	}{
		{"0 2 * * ? *", time.Date(2026, 6, 11, 2, 0, 0, 0, time.UTC)},
		{"*/15 * * * ? *", time.Date(2026, 6, 10, 12, 45, 0, 0, time.UTC)},
		{"30 14 ? * MON-FRI *", time.Date(2026, 6, 10, 14, 30, 0, 0, time.UTC)},
		{"0,30 * * * ? *", time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC)},
		{"0/5 * * * ? *", time.Date(2026, 6, 10, 12, 35, 0, 0, time.UTC)},
		{"2/10 * * * ? *", time.Date(2026, 6, 10, 12, 42, 0, 0, time.UTC)},
		{"0 0 1 JAN ? *", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"0 9 15 * ? *", time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)},
		{"0 0 L * ? *", time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)},
		{"0 0 LW * ? *", time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)},
		{"0 0 15W * ? *", time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)},
		{"0 0 1W 8 ? *", time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)},
		{"0 0 ? * 6#3 *", time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)},
		{"0 0 ? * 6L *", time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)},
		{"0 0 ? * L *", time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC)},
		{"0 0 1 1 ? 2030", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		if got := mustNext(t, c.expr, AWS, nil, base); !got.Equal(c.want) {
			t.Errorf("Next(%q) = %s, want %s", c.expr, got, c.want)
		}
	}
}

func TestAWSDialectRejects(t *testing.T) {
	for _, expr := range []string{
		"0 2 * * ?",     // five fields
		"99 2 * * ? *",  // minute out of range
		"0 2 ? * 8#1 *", // day-of-week 8
		"0 2 ? * 2#9 *", // ninth occurrence
		"0 2 32W * ? *", // day 32
		"0 2 * * * *",   // neither day field is ?
		"0 2 ? * ? *",   // both day fields are ?
		"? 2 * * ? *",   // ? outside the day fields
		"0/0 * * * ? *", // zero step
		"0 0 1 1 ? 1969",
	} {
		if _, err := Parse(expr, AWS, nil); err == nil {
			t.Errorf("Parse(%q) accepted it", expr)
		}
	}
	s, err := Parse("0 0 1 1 ? 2020", AWS, nil)
	if err != nil {
		t.Fatal(err)
	}
	if next, ok := s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); ok {
		t.Errorf("a year list that has passed produced %s", next)
	}
}

func TestVixieDialectDayRule(t *testing.T) {
	// Wednesday 2026-03-04 09:17 UTC.
	base := time.Date(2026, 3, 4, 9, 17, 0, 0, time.UTC)
	for _, c := range []struct {
		expr string
		want time.Time
	}{
		{"0 2 * * *", time.Date(2026, 3, 5, 2, 0, 0, 0, time.UTC)},
		{"30 */6 * * 1-5", time.Date(2026, 3, 4, 12, 30, 0, 0, time.UTC)},
		{"0 0 * * 0", time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)},
		{"0 0 * * 7", time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)},
		{"0 0 * * SAT", time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)},
		{"0 0 1 * *", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		// Both day fields restricted: the 15th OR any Friday.
		{"0 0 15 * FRI", time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC)},
		// A day field that begins with * keeps the AND rule: days 1, 11, 21, 31 that are Fridays.
		{"0 0 */10 * FRI", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)},
		{"0 0 29 FEB *", time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},
	} {
		if got := mustNext(t, c.expr, Vixie, nil, base); !got.Equal(c.want) {
			t.Errorf("Next(%q) = %s, want %s", c.expr, got, c.want)
		}
	}
	for _, bad := range []string{"", "0 2 * *", "not a crontab", "60 2 * * *", "0 2 * * 9", "0 2 ? * *"} {
		if _, err := Parse(bad, Vixie, nil); err == nil {
			t.Errorf("Parse(%q) accepted it", bad)
		}
	}
}

func TestOccurrencesFollowTheTimeZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 09:00 in New York is 13:00 UTC in summer and 14:00 UTC in winter.
	summer := mustNext(t, "0 9 * * ? *", AWS, ny, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC); !summer.Equal(want) {
		t.Errorf("summer 09:00 New York = %s, want %s", summer.UTC(), want)
	}
	winter := mustNext(t, "0 9 * * ? *", AWS, ny, time.Date(2026, 12, 10, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 12, 10, 14, 0, 0, 0, time.UTC); !winter.Equal(want) {
		t.Errorf("winter 09:00 New York = %s, want %s", winter.UTC(), want)
	}
	// 2026-03-08 02:30 does not exist in New York; the next occurrence is a day later.
	skipped := mustNext(t, "30 2 * * ? *", AWS, ny, time.Date(2026, 3, 8, 0, 0, 0, 0, ny))
	if want := time.Date(2026, 3, 9, 2, 30, 0, 0, ny); !skipped.Equal(want) {
		t.Errorf("02:30 across the spring jump = %s, want %s", skipped, want)
	}
	// 2026-11-01 01:30 happens twice in New York; it fires at the first.
	repeated := mustNext(t, "30 1 * * ? *", AWS, ny, time.Date(2026, 11, 1, 0, 0, 0, 0, ny))
	if want := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC); !repeated.Equal(want) {
		t.Errorf("01:30 across the autumn repeat = %s, want %s", repeated.UTC(), want)
	}
	again := mustNext(t, "30 1 * * ? *", AWS, ny, repeated)
	if want := time.Date(2026, 11, 2, 1, 30, 0, 0, ny); !again.Equal(want) {
		t.Errorf("after the first 01:30 = %s, want the next day's %s", again, want)
	}
}

func TestTickerFiresPersistsAndForgets(t *testing.T) {
	store := sim.NewStateStore[Record]()
	hourly, err := Parse("0 * * * *", Vixie, nil)
	if err != nil {
		t.Fatal(err)
	}
	var fired []time.Time
	var oneShot []time.Time
	entries := []Entry{
		{Key: "hourly", Spec: "0 * * * *", Next: hourly.Next, Fire: func(at time.Time) { fired = append(fired, at) }},
		{Key: "once", Spec: "at", First: func(time.Time) (time.Time, bool) {
			return time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC), true
		}, Fire: func(at time.Time) { oneShot = append(oneShot, at) }},
	}
	ticker := NewTicker(store, func() []Entry { return entries })
	start := time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
	ticker.Tick(start)
	if len(fired)+len(oneShot) != 0 {
		t.Fatal("nothing is due at 00:10")
	}
	if rec, _ := store.Get("hourly"); !rec.Next.Equal(time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("persisted next = %s", rec.Next)
	}
	ticker.Tick(time.Date(2026, 1, 1, 1, 0, 5, 0, time.UTC))
	if len(fired) != 1 || !fired[0].Equal(time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)) || len(oneShot) != 1 {
		t.Fatalf("fired %v, one-shot %v", fired, oneShot)
	}
	// Five hours pass with no tick: the missed occurrences collapse into the latest.
	ticker.Tick(time.Date(2026, 1, 1, 6, 0, 30, 0, time.UTC))
	if len(fired) != 2 || !fired[1].Equal(time.Date(2026, 1, 1, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("after a gap fired %v", fired)
	}
	if len(oneShot) != 1 {
		t.Fatal("a one-shot schedule fired twice")
	}
	// A changed spec restarts the schedule; a removed entry loses its record.
	entries[0].Spec = "changed"
	entries = entries[:1]
	ticker.Tick(time.Date(2026, 1, 1, 6, 10, 0, 0, time.UTC))
	if _, ok := store.Get("once"); ok {
		t.Fatal("the record of a removed schedule survived")
	}
	if rec, _ := store.Get("hourly"); rec.Spec != "changed" || !rec.Next.Equal(time.Date(2026, 1, 1, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("restarted record = %+v", rec)
	}
}

func TestTickerDropsOccurrencesOlderThanMaxLate(t *testing.T) {
	store := sim.NewStateStore[Record]()
	daily, err := Parse("0 2 * * *", Vixie, nil)
	if err != nil {
		t.Fatal(err)
	}
	var fired int
	store.Put("daily", Record{Spec: "d", Next: time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)})
	ticker := NewTicker(store, func() []Entry {
		return []Entry{{Key: "daily", Spec: "d", Next: daily.Next, MaxLate: time.Hour, Fire: func(time.Time) { fired++ }}}
	})
	ticker.Tick(time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC))
	if fired != 0 {
		t.Fatal("an occurrence seven hours late fired despite a one-hour bound")
	}
	if rec, _ := store.Get("daily"); !rec.Next.Equal(time.Date(2026, 3, 5, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("next = %s", rec.Next)
	}
}
