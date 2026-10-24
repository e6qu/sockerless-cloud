package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The format elements of FORMAT_TIMESTAMP, PARSE_TIMESTAMP and their DATE,
// DATETIME and TIME siblings, and of a load's dateFormat and timestampFormat
// options, as the vendored format-elements reference defines them
// (specs/cloud-api/gcp/bigquery-format-elements.md.gz).

var (
	bqWeekdayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	bqMonthNames   = []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
)

// bqFormatTime writes t in the format elements of format. Elements the
// reference marks for TIMESTAMP only (%Z, %z, %Ez) render the zone t carries.
func bqFormatTime(format string, t time.Time) (string, error) {
	var b strings.Builder
	rs := []rune(format)
	for i := 0; i < len(rs); i++ {
		if rs[i] != '%' || i+1 >= len(rs) {
			b.WriteRune(rs[i])
			continue
		}
		i++
		switch rs[i] {
		case 'A':
			b.WriteString(bqWeekdayNames[t.Weekday()])
		case 'a':
			b.WriteString(bqWeekdayNames[t.Weekday()][:3])
		case 'B':
			b.WriteString(bqMonthNames[t.Month()-1])
		case 'b', 'h':
			b.WriteString(bqMonthNames[t.Month()-1][:3])
		case 'C':
			fmt.Fprintf(&b, "%02d", t.Year()/100)
		case 'c':
			fmt.Fprintf(&b, "%s %s %2d %02d:%02d:%02d %d", bqWeekdayNames[t.Weekday()][:3], bqMonthNames[t.Month()-1][:3], t.Day(), t.Hour(), t.Minute(), t.Second(), t.Year())
		case 'D', 'x':
			fmt.Fprintf(&b, "%02d/%02d/%02d", int(t.Month()), t.Day(), t.Year()%100)
		case 'd':
			fmt.Fprintf(&b, "%02d", t.Day())
		case 'e':
			fmt.Fprintf(&b, "%2d", t.Day())
		case 'F':
			fmt.Fprintf(&b, "%d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
		case 'G':
			y, _ := t.ISOWeek()
			fmt.Fprintf(&b, "%d", y)
		case 'g':
			y, _ := t.ISOWeek()
			fmt.Fprintf(&b, "%02d", y%100)
		case 'H':
			fmt.Fprintf(&b, "%02d", t.Hour())
		case 'I':
			fmt.Fprintf(&b, "%02d", bqClockHour12(t.Hour()))
		case 'J':
			fmt.Fprintf(&b, "%03d", bqISODayOfYear(t))
		case 'j':
			fmt.Fprintf(&b, "%03d", t.YearDay())
		case 'k':
			fmt.Fprintf(&b, "%2d", t.Hour())
		case 'l':
			fmt.Fprintf(&b, "%2d", bqClockHour12(t.Hour()))
		case 'M':
			fmt.Fprintf(&b, "%02d", t.Minute())
		case 'm':
			fmt.Fprintf(&b, "%02d", int(t.Month()))
		case 'n':
			b.WriteByte('\n')
		case 'P':
			if t.Hour() < 12 {
				b.WriteString("am")
			} else {
				b.WriteString("pm")
			}
		case 'p':
			if t.Hour() < 12 {
				b.WriteString("AM")
			} else {
				b.WriteString("PM")
			}
		case 'Q':
			fmt.Fprintf(&b, "%d", (int(t.Month())+2)/3)
		case 'R':
			fmt.Fprintf(&b, "%02d:%02d", t.Hour(), t.Minute())
		case 'S':
			fmt.Fprintf(&b, "%02d", t.Second())
		case 's':
			fmt.Fprintf(&b, "%d", t.Unix())
		case 'T', 'X':
			fmt.Fprintf(&b, "%02d:%02d:%02d", t.Hour(), t.Minute(), t.Second())
		case 't':
			b.WriteByte('\t')
		case 'U':
			fmt.Fprintf(&b, "%02d", bqWeekOfYear(t, time.Sunday))
		case 'u':
			wd := int(t.Weekday())
			if wd == 0 {
				wd = 7
			}
			fmt.Fprintf(&b, "%d", wd)
		case 'V':
			_, w := t.ISOWeek()
			fmt.Fprintf(&b, "%02d", w)
		case 'W':
			fmt.Fprintf(&b, "%02d", bqWeekOfYear(t, time.Monday))
		case 'w':
			fmt.Fprintf(&b, "%d", int(t.Weekday()))
		case 'Y':
			fmt.Fprintf(&b, "%d", t.Year())
		case 'y':
			fmt.Fprintf(&b, "%02d", t.Year()%100)
		case 'Z':
			name, offset := t.Zone()
			if name == "" || name == "UTC" || offset == 0 {
				b.WriteString("UTC")
			} else {
				b.WriteString(name)
			}
		case 'z':
			b.WriteString(bqZoneOffsetText(t, false))
		case '%':
			b.WriteByte('%')
		case 'E':
			rest := string(rs[i+1:])
			switch {
			case strings.HasPrefix(rest, "z"):
				b.WriteString(bqZoneOffsetText(t, true))
				i++
			case strings.HasPrefix(rest, "4Y"):
				fmt.Fprintf(&b, "%04d", t.Year())
				i += 2
			case strings.HasPrefix(rest, "*S"):
				b.WriteString(bqFormatClock(t)[6:])
				i += 2
			default:
				j := 0
				for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
					j++
				}
				if j == 0 || j >= len(rest) || rest[j] != 'S' {
					return "", bqQueryErr("Invalid format element in format string: %%E%s", rest)
				}
				digits, _ := strconv.Atoi(rest[:j])
				fmt.Fprintf(&b, "%02d", t.Second())
				if digits > 0 {
					frac := fmt.Sprintf("%09d", t.Nanosecond())
					for len(frac) < digits {
						frac += "0"
					}
					b.WriteByte('.')
					b.WriteString(frac[:digits])
				}
				i += j + 1
			}
		default:
			return "", bqQueryErr("Invalid format element in format string: %%%c", rs[i])
		}
	}
	return b.String(), nil
}

func bqClockHour12(h int) int {
	h %= 12
	if h == 0 {
		return 12
	}
	return h
}

// bqWeekOfYear is the week number in [0, 53] with weeks starting on start;
// days before the year's first start weekday fall in week 0.
func bqWeekOfYear(t time.Time, start time.Weekday) int {
	offset := (int(t.Weekday()) - int(start) + 7) % 7
	return (t.YearDay() - 1 + 7 - offset) / 7
}

// bqISODayOfYear counts days from the Monday that begins the ISO year.
func bqISODayOfYear(t time.Time) int {
	y, _ := t.ISOWeek()
	start := bqISOYearStart(y)
	return int(t.Sub(start).Hours()/24) + 1
}

// bqISOYearStart is the Monday of the first ISO week of year: the Monday on or
// before January 4th.
func bqISOYearStart(year int) time.Time {
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	return jan4.AddDate(0, 0, -((int(jan4.Weekday()) + 6) % 7))
}

func bqZoneOffsetText(t time.Time, colon bool) string {
	_, offset := t.Zone()
	sign := '+'
	if offset < 0 {
		sign, offset = '-', -offset
	}
	if colon {
		return fmt.Sprintf("%c%02d:%02d", sign, offset/3600, offset%3600/60)
	}
	return fmt.Sprintf("%c%02d%02d", sign, offset/3600, offset%3600/60)
}

// bqParsedTime is what the format elements of a parsed string fixed; a field
// no element names keeps its 1970-01-01 00:00:00 initial value.
type bqParsedTime struct {
	year, month, day           int
	hour, minute, second, nano int
	pm                         int
	century                    int
	yearInCentury              int
	epochSeconds               *int64
	zone                       *time.Location
	isoYear, isoDay            int
}

// bqParseTime reads text under the format elements of format in the zone
// loc, the way PARSE_TIMESTAMP, PARSE_DATE, PARSE_DATETIME and PARSE_TIME read
// their input.
func bqParseTime(format, text string, loc *time.Location) (time.Time, error) {
	p := &bqParsedTime{year: 1970, month: 1, day: 1, pm: -1, century: -1, yearInCentury: -1}
	rs := []rune(format)
	in := []rune(strings.TrimSpace(text))
	pos := 0
	fail := func() (time.Time, error) {
		return time.Time{}, bqQueryErr("Failed to parse input string %q", text)
	}
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if unicode.IsSpace(c) {
			for pos < len(in) && unicode.IsSpace(in[pos]) {
				pos++
			}
			continue
		}
		if c != '%' || i+1 >= len(rs) {
			if pos >= len(in) || in[pos] != c {
				return fail()
			}
			pos++
			continue
		}
		i++
		element := string(rs[i])
		if rs[i] == 'E' {
			rest := string(rs[i+1:])
			j := 0
			for j < len(rest) && (rest[j] >= '0' && rest[j] <= '9' || rest[j] == '*') {
				j++
			}
			if j < len(rest) && (rest[j] == 'S' || rest[j] == 'Y' || rest[j] == 'z') {
				element = "E" + rest[:j+1]
				i += j + 1
			}
		}
		next, ok := p.consume(element, in, pos)
		if !ok {
			return fail()
		}
		pos = next
	}
	for pos < len(in) && unicode.IsSpace(in[pos]) {
		pos++
	}
	if pos != len(in) {
		return fail()
	}
	return p.time(loc)
}

func (p *bqParsedTime) consume(element string, in []rune, pos int) (int, bool) {
	number := func(maxDigits int) (int, int, bool) {
		j := pos
		neg := false
		if j < len(in) && (in[j] == '-' || in[j] == '+') {
			neg = in[j] == '-'
			j++
		}
		start := j
		for j < len(in) && j-start < maxDigits && in[j] >= '0' && in[j] <= '9' {
			j++
		}
		if j == start {
			return 0, pos, false
		}
		n, _ := strconv.Atoi(string(in[start:j]))
		if neg {
			n = -n
		}
		return n, j, true
	}
	name := func(names []string, short bool) (int, int, bool) {
		rest := strings.ToLower(string(in[pos:]))
		for i, n := range names {
			candidates := []string{strings.ToLower(n)}
			if short {
				candidates = []string{strings.ToLower(n[:3])}
			}
			for _, c := range candidates {
				if strings.HasPrefix(rest, c) {
					return i, pos + len([]rune(c)), true
				}
			}
		}
		return 0, pos, false
	}
	switch element {
	case "A", "a":
		_, next, ok := name(bqWeekdayNames, element == "a")
		return next, ok
	case "B", "b", "h":
		m, next, ok := name(bqMonthNames, element != "B")
		p.month = m + 1
		return next, ok
	case "C":
		n, next, ok := number(2)
		p.century = n
		return next, ok
	case "c":
		return p.consumeAll([]string{"a", " ", "b", " ", "e", " ", "H", ":", "M", ":", "S", " ", "Y"}, in, pos)
	case "D", "x":
		return p.consumeAll([]string{"m", "/", "d", "/", "y"}, in, pos)
	case "d", "e":
		for element == "e" && pos < len(in) && in[pos] == ' ' {
			pos++
		}
		n, next, ok := number(2)
		p.day = n
		return next, ok
	case "F":
		return p.consumeAll([]string{"Y", "-", "m", "-", "d"}, in, pos)
	case "G":
		n, next, ok := number(10)
		p.isoYear = n
		return next, ok
	case "g":
		n, next, ok := number(2)
		p.isoYear = 2000 + n
		if n >= 69 {
			p.isoYear = 1900 + n
		}
		return next, ok
	case "H", "k":
		for element == "k" && pos < len(in) && in[pos] == ' ' {
			pos++
		}
		n, next, ok := number(2)
		p.hour = n
		return next, ok
	case "I", "l":
		for element == "l" && pos < len(in) && in[pos] == ' ' {
			pos++
		}
		n, next, ok := number(2)
		p.hour = n % 12
		return next, ok
	case "J":
		n, next, ok := number(3)
		p.isoDay = n
		return next, ok
	case "j":
		n, next, ok := number(3)
		p.month, p.day = 1, n
		return next, ok
	case "M":
		n, next, ok := number(2)
		p.minute = n
		return next, ok
	case "m":
		n, next, ok := number(2)
		p.month = n
		return next, ok
	case "n", "t":
		for pos < len(in) && unicode.IsSpace(in[pos]) {
			pos++
		}
		return pos, true
	case "p":
		rest := strings.ToLower(string(in[pos:]))
		switch {
		case strings.HasPrefix(rest, "am"):
			p.pm = 0
		case strings.HasPrefix(rest, "pm"):
			p.pm = 1
		default:
			return pos, false
		}
		return pos + 2, true
	case "Q":
		n, next, ok := number(1)
		p.month = (n-1)*3 + 1
		return next, ok
	case "R":
		return p.consumeAll([]string{"H", ":", "M"}, in, pos)
	case "S":
		n, next, ok := number(2)
		p.second = n
		return next, ok
	case "s":
		j := pos
		if j < len(in) && in[j] == '-' {
			j++
		}
		start := j
		for j < len(in) && in[j] >= '0' && in[j] <= '9' {
			j++
		}
		if j == start {
			return pos, false
		}
		n, err := strconv.ParseInt(string(in[pos:j]), 10, 64)
		if err != nil {
			return pos, false
		}
		p.epochSeconds = &n
		return j, true
	case "T", "X":
		return p.consumeAll([]string{"H", ":", "M", ":", "S"}, in, pos)
	case "U", "W", "V", "u", "w":
		_, next, ok := number(2)
		return next, ok
	case "Y", "E4Y":
		n, next, ok := number(10)
		p.year = n
		return next, ok
	case "y":
		n, next, ok := number(2)
		p.yearInCentury = n
		return next, ok
	case "Z":
		j := pos
		for j < len(in) && !unicode.IsSpace(in[j]) {
			j++
		}
		loc, err := bqLoadZone(string(in[pos:j]))
		if err != nil {
			return pos, false
		}
		p.zone = loc
		return j, true
	case "z", "Ez":
		j := pos
		if j < len(in) && (in[j] == '+' || in[j] == '-') {
			j++
		}
		for j < len(in) && (in[j] >= '0' && in[j] <= '9' || in[j] == ':') {
			j++
		}
		loc, err := bqLoadZone(string(in[pos:j]))
		if err != nil {
			return pos, false
		}
		p.zone = loc
		return j, true
	case "%":
		if pos < len(in) && in[pos] == '%' {
			return pos + 1, true
		}
		return pos, false
	}
	if strings.HasPrefix(element, "E") && strings.HasSuffix(element, "S") {
		n, next, ok := number(2)
		if !ok {
			return pos, false
		}
		p.second = n
		if next < len(in) && in[next] == '.' {
			j := next + 1
			for j < len(in) && in[j] >= '0' && in[j] <= '9' {
				j++
			}
			frac := string(in[next+1 : j])
			if len(frac) > 9 {
				frac = frac[:9]
			}
			for len(frac) < 9 {
				frac += "0"
			}
			p.nano, _ = strconv.Atoi(frac)
			next = j
		}
		return next, true
	}
	return pos, false
}

func (p *bqParsedTime) consumeAll(elements []string, in []rune, pos int) (int, bool) {
	for _, e := range elements {
		if e == " " || e == ":" || e == "/" || e == "-" {
			if e == " " {
				for pos < len(in) && unicode.IsSpace(in[pos]) {
					pos++
				}
				continue
			}
			if pos >= len(in) || string(in[pos]) != e {
				return pos, false
			}
			pos++
			continue
		}
		next, ok := p.consume(e, in, pos)
		if !ok {
			return pos, false
		}
		pos = next
	}
	return pos, true
}

func (p *bqParsedTime) time(loc *time.Location) (time.Time, error) {
	if p.zone != nil {
		loc = p.zone
	}
	if p.epochSeconds != nil {
		return time.Unix(*p.epochSeconds, 0).UTC(), nil
	}
	year := p.year
	if p.yearInCentury >= 0 {
		switch {
		case p.century >= 0:
			year = p.century*100 + p.yearInCentury
		case p.yearInCentury <= 68:
			year = 2000 + p.yearInCentury
		default:
			year = 1900 + p.yearInCentury
		}
	} else if p.century >= 0 {
		year = p.century * 100
	}
	hour := p.hour
	if p.pm == 1 && hour < 12 {
		hour += 12
	}
	if p.isoYear != 0 {
		day := p.isoDay
		if day == 0 {
			day = 1
		}
		start := bqISOYearStart(p.isoYear).AddDate(0, 0, day-1)
		return time.Date(start.Year(), start.Month(), start.Day(), hour, p.minute, p.second, p.nano, loc), nil
	}
	if p.month < 1 || p.month > 12 || p.day < 1 || p.day > 31 || hour > 23 || p.minute > 59 || p.second > 60 {
		return time.Time{}, bqQueryErr("Failed to parse input string: a field is out of range")
	}
	t := time.Date(year, time.Month(p.month), p.day, hour, p.minute, p.second, p.nano, loc)
	if t.Day() != p.day {
		return time.Time{}, bqQueryErr("Failed to parse input string: day %d is out of range for the month", p.day)
	}
	return t, nil
}

// bqLoadZone reads a time zone the way the TIMESTAMP functions take one: a
// tz database name, an offset from UTC such as -08, +05:30 or -0800, or Z.
func bqLoadZone(name string) (*time.Location, error) {
	switch name {
	case "", "Z", "z", "UTC", "utc":
		return time.UTC, nil
	}
	if name[0] == '+' || name[0] == '-' {
		offset, err := bqZoneOffset(name)
		if err != nil {
			return nil, bqQueryErr("Invalid time zone: %s", name)
		}
		return time.FixedZone(name, offset), nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, bqQueryErr("Invalid time zone: %s", name)
	}
	return loc, nil
}
