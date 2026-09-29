// Package cron parses the cron expressions the clouds accept and computes
// their occurrences in a time zone, and fires persisted schedules from a
// server's background loop (see Ticker).
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Dialect selects a cron grammar.
type Dialect int

const (
	// Vixie is the five-field crontab (minute hour day-of-month month
	// day-of-week) with day-of-week 0–7 (0 and 7 are Sunday), JAN–DEC and
	// SUN–SAT names, and Vixie cron's day rule: when both day fields are
	// restricted (neither begins with `*`) a day matching either fires.
	Vixie Dialect = iota
	// AWS is the six-field Amazon form (minutes hours day-of-month month
	// day-of-week year) with day-of-week 1–7 (1 is Sunday), the `L`, `W` and
	// `#` qualifiers, and `?` in exactly one of the two day fields.
	AWS
)

// Schedule is a parsed expression bound to the time zone its fields are read
// in.
type Schedule struct {
	minute, hour, month bits
	years               map[int]bool
	maxYear             int
	dom, dow            func(time.Time) bool
	domStar, dowStar    bool
	orDays              bool
	loc                 *time.Location
}

type bits uint64

func (b bits) has(v int) bool { return b&(1<<uint(v)) != 0 }

var monthNames = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

var vixieDays = map[string]int{"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6}

var awsDays = map[string]int{"SUN": 1, "MON": 2, "TUE": 3, "WED": 4, "THU": 5, "FRI": 6, "SAT": 7}

// Parse reads expr in dialect d; occurrences are wall-clock times in loc (UTC
// when loc is nil).
func Parse(expr string, d Dialect, loc *time.Location) (Schedule, error) {
	if loc == nil {
		loc = time.UTC
	}
	fields := strings.Fields(expr)
	want := 5
	if d == AWS {
		want = 6
	}
	if len(fields) != want {
		return Schedule{}, fmt.Errorf("cron expression %q has %d fields, want %d", expr, len(fields), want)
	}
	s := Schedule{loc: loc}
	var err error
	if s.minute, err = parseBits(fields[0], 0, 59, nil); err != nil {
		return Schedule{}, fmt.Errorf("minutes: %w", err)
	}
	if s.hour, err = parseBits(fields[1], 0, 23, nil); err != nil {
		return Schedule{}, fmt.Errorf("hours: %w", err)
	}
	if s.month, err = parseBits(fields[3], 1, 12, monthNames); err != nil {
		return Schedule{}, fmt.Errorf("month: %w", err)
	}
	switch d {
	case Vixie:
		err = s.parseVixieDays(fields[2], fields[4])
	case AWS:
		err = s.parseAWSDays(fields[2], fields[4])
		if err == nil {
			err = s.parseYears(fields[5])
		}
	default:
		err = fmt.Errorf("unknown cron dialect %d", d)
	}
	if err != nil {
		return Schedule{}, err
	}
	return s, nil
}

func (s *Schedule) parseVixieDays(dom, dow string) error {
	domBits, err := parseBits(dom, 1, 31, nil)
	if err != nil {
		return fmt.Errorf("day-of-month: %w", err)
	}
	dowBits, err := parseBits(dow, 0, 7, vixieDays)
	if err != nil {
		return fmt.Errorf("day-of-week: %w", err)
	}
	if dowBits.has(7) {
		dowBits |= 1
	}
	s.dom = func(t time.Time) bool { return domBits.has(t.Day()) }
	s.dow = func(t time.Time) bool { return dowBits.has(int(t.Weekday())) }
	s.domStar = strings.HasPrefix(dom, "*")
	s.dowStar = strings.HasPrefix(dow, "*")
	s.orDays = !s.domStar && !s.dowStar
	return nil
}

func (s *Schedule) parseAWSDays(dom, dow string) error {
	if (dom == "?") == (dow == "?") {
		return fmt.Errorf("exactly one of day-of-month and day-of-week must be ?")
	}
	var err error
	if s.dom, err = awsDayOfMonth(dom); err != nil {
		return fmt.Errorf("day-of-month: %w", err)
	}
	if s.dow, err = awsDayOfWeek(dow); err != nil {
		return fmt.Errorf("day-of-week: %w", err)
	}
	return nil
}

func (s *Schedule) parseYears(spec string) error {
	const lo, hi = 1970, 2199
	values, err := parseValues(spec, lo, hi, nil)
	if err != nil {
		return fmt.Errorf("year: %w", err)
	}
	s.years = map[int]bool{}
	for _, v := range values {
		s.years[v] = true
		if v > s.maxYear {
			s.maxYear = v
		}
	}
	return nil
}

// Next returns the first occurrence strictly after after, false when none
// exists (a year list that has passed, or a date no calendar holds). A
// wall-clock time a daylight-saving jump skips does not occur; one it repeats
// occurs once, at its first instant.
func (s Schedule) Next(after time.Time) (time.Time, bool) {
	local := after.In(s.loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	lastYear := local.Year() + 8
	if s.years != nil {
		lastYear = s.maxYear
	}
	for day.Year() <= lastYear {
		if s.years != nil && !s.years[day.Year()] {
			day = time.Date(day.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !s.month.has(int(day.Month())) {
			day = time.Date(day.Year(), day.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if s.dayMatches(day) {
			for h := 0; h < 24; h++ {
				if !s.hour.has(h) {
					continue
				}
				for m := 0; m < 60; m++ {
					if !s.minute.has(m) {
						continue
					}
					t := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, s.loc)
					if t.Day() != day.Day() || t.Hour() != h || t.Minute() != m {
						continue
					}
					if t.After(after) {
						return t, true
					}
				}
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return time.Time{}, false
}

// dayMatches judges a calendar date carried in a UTC time value, so the
// qualifier arithmetic never meets a zone transition.
func (s Schedule) dayMatches(day time.Time) bool {
	dom, dow := s.dom(day), s.dow(day)
	if s.orDays {
		return dom || dow
	}
	return dom && dow
}

func parseBits(spec string, lo, hi int, names map[string]int) (bits, error) {
	values, err := parseValues(spec, lo, hi, names)
	if err != nil {
		return 0, err
	}
	var b bits
	for _, v := range values {
		b |= 1 << uint(v)
	}
	return b, nil
}

// parseValues expands a list of `*`, values, ranges and `/` steps. A bare
// value with a step runs from that value to the field's maximum, as both
// grammars define it.
func parseValues(spec string, lo, hi int, names map[string]int) ([]int, error) {
	var out []int
	for _, part := range strings.Split(spec, ",") {
		if part == "" {
			return nil, fmt.Errorf("empty term in %q", spec)
		}
		rng, step, hasStep := part, 1, false
		if i := strings.IndexByte(part, '/'); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 || n > hi {
				return nil, fmt.Errorf("invalid step in %q", part)
			}
			rng, step, hasStep = part[:i], n, true
		}
		from, to := lo, hi
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			i := strings.IndexByte(rng, '-')
			a, errA := value(rng[:i], names)
			b, errB := value(rng[i+1:], names)
			if errA != nil || errB != nil {
				return nil, fmt.Errorf("invalid range %q", rng)
			}
			from, to = a, b
		default:
			v, err := value(rng, names)
			if err != nil {
				return nil, err
			}
			from, to = v, v
			if hasStep {
				to = hi
			}
		}
		if from < lo || to > hi || from > to {
			return nil, fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for v := from; v <= to; v += step {
			out = append(out, v)
		}
	}
	return out, nil
}

func value(s string, names map[string]int) (int, error) {
	if v, ok := names[strings.ToUpper(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	return v, nil
}

func lastDay(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// nearestWeekday is the Monday–Friday closest to day n of t's month without
// leaving the month.
func nearestWeekday(t time.Time, n int) int {
	last := lastDay(t)
	if n > last {
		n = last
	}
	switch time.Date(t.Year(), t.Month(), n, 0, 0, 0, 0, time.UTC).Weekday() {
	case time.Saturday:
		if n > 1 {
			return n - 1
		}
		return n + 2
	case time.Sunday:
		if n < last {
			return n + 1
		}
		return n - 2
	}
	return n
}

func awsDayOfMonth(spec string) (func(time.Time) bool, error) {
	switch {
	case spec == "?":
		return func(time.Time) bool { return true }, nil
	case spec == "L":
		return func(t time.Time) bool { return t.Day() == lastDay(t) }, nil
	case spec == "LW":
		return func(t time.Time) bool { return t.Day() == nearestWeekday(t, lastDay(t)) }, nil
	case strings.HasSuffix(spec, "W"):
		n, err := strconv.Atoi(strings.TrimSuffix(spec, "W"))
		if err != nil || n < 1 || n > 31 {
			return nil, fmt.Errorf("invalid nearest-weekday %q", spec)
		}
		return func(t time.Time) bool { return t.Day() == nearestWeekday(t, n) }, nil
	}
	b, err := parseBits(spec, 1, 31, nil)
	if err != nil {
		return nil, err
	}
	return func(t time.Time) bool { return b.has(t.Day()) }, nil
}

func awsWeekday(t time.Time) int { return int(t.Weekday()) + 1 }

func awsDayOfWeek(spec string) (func(time.Time) bool, error) {
	switch {
	case spec == "?":
		return func(time.Time) bool { return true }, nil
	case spec == "L":
		return func(t time.Time) bool { return awsWeekday(t) == 7 }, nil
	case strings.HasSuffix(spec, "L"):
		d, err := value(strings.TrimSuffix(spec, "L"), awsDays)
		if err != nil || d < 1 || d > 7 {
			return nil, fmt.Errorf("invalid last-weekday %q", spec)
		}
		return func(t time.Time) bool { return awsWeekday(t) == d && t.Day() > lastDay(t)-7 }, nil
	case strings.Contains(spec, "#"):
		day, nth, _ := strings.Cut(spec, "#")
		d, errD := value(day, awsDays)
		n, errN := strconv.Atoi(nth)
		if errD != nil || errN != nil || d < 1 || d > 7 || n < 1 || n > 5 {
			return nil, fmt.Errorf("invalid nth-weekday %q", spec)
		}
		return func(t time.Time) bool { return awsWeekday(t) == d && (t.Day()-1)/7+1 == n }, nil
	}
	b, err := parseBits(spec, 1, 7, awsDays)
	if err != nil {
		return nil, err
	}
	return func(t time.Time) bool { return b.has(awsWeekday(t)) }, nil
}
