package main

import (
	"strconv"
	"strings"
	"time"
)

// AWS EventBridge Scheduler cron evaluation. The expression is
// cron(minutes hours day-of-month month day-of-week year) — six fields. Each
// field supports `*`, `?` (treated as `*`), single values, lists (`,`), ranges
// (`-`), steps (`/`), and named months (JAN–DEC) / days (SUN–SAT). Day-of-week
// is 1–7 with 1 = Sunday, matching AWS. The AWS qualifiers `L` (last day of
// month / week), `W` (nearest weekday), and `#` (nth weekday of month) are
// supported in the day-of-month and day-of-week fields.

var cronMonths = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

var cronDows = map[string]int{
	"SUN": 1, "MON": 2, "TUE": 3, "WED": 4, "THU": 5, "FRI": 6, "SAT": 7,
}

// schedulerCronNext returns the next fire time strictly after `after` for an AWS
// cron(...) expression, or false if the expression is unparseable / uses
// unsupported qualifiers / has no occurrence within four years.
func schedulerCronNext(expr string, after time.Time) (time.Time, bool) {
	expr = strings.TrimSpace(expr)
	if !strings.HasPrefix(expr, "cron(") || !strings.HasSuffix(expr, ")") {
		return time.Time{}, false
	}
	f := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(expr, "cron("), ")"))
	if len(f) != 6 {
		return time.Time{}, false
	}
	mins, ok1 := cronField(f[0], 0, 59, nil)
	hrs, ok2 := cronField(f[1], 0, 23, nil)
	domMatch, ok3 := cronDayOfMonth(f[2])
	mons, ok4 := cronField(f[3], 1, 12, cronMonths)
	dowMatch, ok5 := cronDayOfWeek(f[4])
	yrs, ok6 := cronField(f[5], 1970, 2199, nil)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
		return time.Time{}, false
	}

	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := after.AddDate(4, 0, 0)
	for t.Before(limit) {
		if yrs[t.Year()] && mons[int(t.Month())] && hrs[t.Hour()] && mins[t.Minute()] {
			if domMatch(t) && dowMatch(t) {
				return t, true
			}
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, false
}

// schedulerCronValid reports whether expr is a structurally valid AWS cron(...)
// expression (six parseable fields), independent of whether it has an upcoming
// occurrence. CreateSchedule uses it to reject malformed cron loudly instead of
// accepting a schedule that would silently never fire.
func schedulerCronValid(expr string) bool {
	expr = strings.TrimSpace(expr)
	if !strings.HasPrefix(expr, "cron(") || !strings.HasSuffix(expr, ")") {
		return false
	}
	f := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(expr, "cron("), ")"))
	if len(f) != 6 {
		return false
	}
	_, ok1 := cronField(f[0], 0, 59, nil)
	_, ok2 := cronField(f[1], 0, 23, nil)
	_, ok3 := cronDayOfMonth(f[2])
	_, ok4 := cronField(f[3], 1, 12, cronMonths)
	_, ok5 := cronDayOfWeek(f[4])
	_, ok6 := cronField(f[5], 1970, 2199, nil)
	return ok1 && ok2 && ok3 && ok4 && ok5 && ok6
}

// awsWeekday returns AWS day-of-week numbering (Sunday=1 … Saturday=7).
func awsWeekday(t time.Time) int { return int(t.Weekday()) + 1 }

// lastDayOfMonth returns the day number of the last day of t's month.
func lastDayOfMonth(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
}

// nearestWeekday returns the day-of-month of the nearest Mon–Fri to day n,
// without crossing the month boundary (AWS `W` semantics: Sat→Fri, Sun→Mon,
// except a boundary crossing flips to the other direction).
func nearestWeekday(t time.Time, n int) int {
	if last := lastDayOfMonth(t); n > last {
		n = last
	}
	day := time.Date(t.Year(), t.Month(), n, 0, 0, 0, 0, t.Location())
	switch day.Weekday() {
	case time.Saturday:
		if n-1 >= 1 {
			return n - 1
		}
		return n + 2
	case time.Sunday:
		if n+1 <= lastDayOfMonth(t) {
			return n + 1
		}
		return n - 2
	default:
		return n
	}
}

// cronDayOfMonth builds the day-of-month predicate, handling AWS qualifiers
// `L` (last day), `LW` (last weekday), and `nW` (nearest weekday to day n).
func cronDayOfMonth(spec string) (func(time.Time) bool, bool) {
	switch {
	case spec == "*" || spec == "?":
		return func(time.Time) bool { return true }, true
	case spec == "L":
		return func(t time.Time) bool { return t.Day() == lastDayOfMonth(t) }, true
	case spec == "LW":
		return func(t time.Time) bool {
			return t.Day() == nearestWeekday(t, lastDayOfMonth(t))
		}, true
	case strings.HasSuffix(spec, "W"):
		n, err := strconv.Atoi(strings.TrimSuffix(spec, "W"))
		if err != nil || n < 1 || n > 31 {
			return nil, false
		}
		return func(t time.Time) bool { return t.Day() == nearestWeekday(t, n) }, true
	default:
		set, ok := cronField(spec, 1, 31, nil)
		if !ok {
			return nil, false
		}
		return func(t time.Time) bool { return set[t.Day()] }, true
	}
}

// cronDayOfWeek builds the day-of-week predicate, handling AWS qualifiers `L`
// (last day of week = Saturday), `nL` (last weekday n of the month), and `d#n`
// (nth weekday d of the month). Day numbering is AWS 1=SUN … 7=SAT.
func cronDayOfWeek(spec string) (func(time.Time) bool, bool) {
	switch {
	case spec == "*" || spec == "?":
		return func(time.Time) bool { return true }, true
	case spec == "L":
		return func(t time.Time) bool { return awsWeekday(t) == 7 }, true
	case strings.HasSuffix(spec, "L"):
		d, ok := cronAtoi(strings.TrimSuffix(spec, "L"), cronDows)
		if !ok || d < 1 || d > 7 {
			return nil, false
		}
		return func(t time.Time) bool {
			return awsWeekday(t) == d && t.Day() > lastDayOfMonth(t)-7
		}, true
	case strings.Contains(spec, "#"):
		parts := strings.SplitN(spec, "#", 2)
		d, okD := cronAtoi(parts[0], cronDows)
		n, errN := strconv.Atoi(parts[1])
		if !okD || errN != nil || d < 1 || d > 7 || n < 1 || n > 5 {
			return nil, false
		}
		return func(t time.Time) bool {
			return awsWeekday(t) == d && (t.Day()-1)/7+1 == n
		}, true
	default:
		set, ok := cronField(spec, 1, 7, cronDows)
		if !ok {
			return nil, false
		}
		return func(t time.Time) bool { return set[awsWeekday(t)] }, true
	}
}

// cronField parses one cron field into the set of matching values in [min,max].
func cronField(spec string, min, max int, names map[string]int) (map[int]bool, bool) {
	out := make(map[int]bool)
	for _, part := range strings.Split(spec, ",") {
		step := 1
		hasStep := false
		rng := part
		if i := strings.Index(part, "/"); i >= 0 {
			rng = part[:i]
			s, err := strconv.Atoi(part[i+1:])
			if err != nil || s <= 0 {
				return nil, false
			}
			step = s
			hasStep = true
		}
		lo, hi := min, max
		switch {
		case rng == "*" || rng == "?":
			// full range (with optional step)
		case strings.Contains(rng, "-"):
			i := strings.Index(rng, "-")
			a, okA := cronAtoi(rng[:i], names)
			b, okB := cronAtoi(rng[i+1:], names)
			if !okA || !okB {
				return nil, false
			}
			lo, hi = a, b
		default:
			v, ok := cronAtoi(rng, names)
			if !ok {
				return nil, false
			}
			lo = v
			// AWS: a bare value with a step ("N/step") means "from N to the
			// field max, every step" (e.g. minutes 0/5 → 0,5,…,55). Without a
			// step it is the single value N.
			if hasStep {
				hi = max
			} else {
				hi = v
			}
		}
		if lo < min || hi > max || lo > hi {
			return nil, false
		}
		for v := lo; v <= hi; v += step {
			out[v] = true
		}
	}
	return out, len(out) > 0
}

func cronAtoi(s string, names map[string]int) (int, bool) {
	if names != nil {
		if v, ok := names[strings.ToUpper(s)]; ok {
			return v, true
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return v, true
}

// schedulerExpressionValid reports whether a ScheduleExpression is one of the
// three documented forms — at(yyyy-mm-ddThh:mm:ss), rate(value unit), or
// cron(...) — and is well-formed. Real EventBridge Scheduler rejects anything
// else with a ValidationException at create/update time.
func schedulerExpressionValid(expr string) bool {
	expr = strings.TrimSpace(expr)
	switch {
	case strings.HasPrefix(expr, "at(") && strings.HasSuffix(expr, ")"):
		inner := strings.TrimSuffix(strings.TrimPrefix(expr, "at("), ")")
		_, err := time.Parse("2006-01-02T15:04:05", inner)
		return err == nil
	case strings.HasPrefix(expr, "rate(") && strings.HasSuffix(expr, ")"):
		_, ok := schedulerRateInterval(expr)
		return ok
	case strings.HasPrefix(expr, "cron(") && strings.HasSuffix(expr, ")"):
		return schedulerCronValid(expr)
	}
	return false
}
