package main

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
)

// dateTimeConverter takes java.time.format.DateTimeFormatter patterns, time
// zone IDs and locales. The formatter here follows Java SE 17's
// DateTimeFormatterBuilder (specs/cloud-api/aws/jdk17-DateTimeFormatterBuilder.java.gz):
// the same printer-parsers, adjacent value parsing, optional sections and
// SMART resolution (jdk17-Parsed.java.gz), with the locale data Java SE 17
// carries.

const cwtDefaultTargetFormat = "yyyy-MM-dd'T'HH:mm:ss.SSS'Z'"

type cwdField int

const (
	cwdEra cwdField = iota
	cwdYearOfEra
	cwdYear
	cwdQuarter
	cwdMonth
	cwdDayOfYear
	cwdDayOfMonth
	cwdAlignedDOWInMonth
	cwdDayOfWeek
	cwdAmPm
	cwdHourOfDay
	cwdClockHourOfDay
	cwdHourOfAmPm
	cwdClockHourOfAmPm
	cwdMinute
	cwdSecond
	cwdNano
	cwdMilliOfDay
	cwdNanoOfDay
	cwdModifiedJulianDay
	cwdOffsetSeconds
	cwdLocalDayOfWeek
	cwdWeekOfMonth
	cwdWeekOfWBY
	cwdWeekBasedYear
)

// cwdError carries a message java.time writes verbatim, capitalized as Java
// writes it.
type cwdError string

func (e cwdError) Error() string { return string(e) }

func cwdErr(format string, args ...any) error { return cwdError(fmt.Sprintf(format, args...)) }

// cwdRange is java.time.temporal.ValueRange.
type cwdRange struct{ min, largestMin, smallestMax, max int64 }

func (r cwdRange) String() string {
	s := strconv.FormatInt(r.min, 10)
	if r.min != r.largestMin {
		s += "/" + strconv.FormatInt(r.largestMin, 10)
	}
	s += " - " + strconv.FormatInt(r.smallestMax, 10)
	if r.smallestMax != r.max {
		s += "/" + strconv.FormatInt(r.max, 10)
	}
	return s
}

var cwdFieldInfo = map[cwdField]struct {
	name string
	r    cwdRange
	time bool
}{
	cwdEra:               {"Era", cwdRange{0, 0, 1, 1}, false},
	cwdYearOfEra:         {"YearOfEra", cwdRange{1, 1, 999999999, 1000000000}, false},
	cwdYear:              {"Year", cwdRange{-999999999, -999999999, 999999999, 999999999}, false},
	cwdQuarter:           {"QuarterOfYear", cwdRange{1, 1, 4, 4}, false},
	cwdMonth:             {"MonthOfYear", cwdRange{1, 1, 12, 12}, false},
	cwdDayOfYear:         {"DayOfYear", cwdRange{1, 1, 365, 366}, false},
	cwdDayOfMonth:        {"DayOfMonth", cwdRange{1, 1, 28, 31}, false},
	cwdAlignedDOWInMonth: {"AlignedDayOfWeekInMonth", cwdRange{1, 1, 7, 7}, false},
	cwdDayOfWeek:         {"DayOfWeek", cwdRange{1, 1, 7, 7}, false},
	cwdAmPm:              {"AmPmOfDay", cwdRange{0, 0, 1, 1}, true},
	cwdHourOfDay:         {"HourOfDay", cwdRange{0, 0, 23, 23}, true},
	cwdClockHourOfDay:    {"ClockHourOfDay", cwdRange{1, 1, 24, 24}, true},
	cwdHourOfAmPm:        {"HourOfAmPm", cwdRange{0, 0, 11, 11}, true},
	cwdClockHourOfAmPm:   {"ClockHourOfAmPm", cwdRange{1, 1, 12, 12}, true},
	cwdMinute:            {"MinuteOfHour", cwdRange{0, 0, 59, 59}, true},
	cwdSecond:            {"SecondOfMinute", cwdRange{0, 0, 59, 59}, true},
	cwdNano:              {"NanoOfSecond", cwdRange{0, 0, 999999999, 999999999}, true},
	cwdMilliOfDay:        {"MilliOfDay", cwdRange{0, 0, 86399999, 86399999}, true},
	cwdNanoOfDay:         {"NanoOfDay", cwdRange{0, 0, 86400*1000000000 - 1, 86400*1000000000 - 1}, true},
	cwdModifiedJulianDay: {"ModifiedJulianDay", cwdRange{-365243219162 + 40587, -365243219162 + 40587, 365241780471 + 40587, 365241780471 + 40587}, false},
	cwdOffsetSeconds:     {"OffsetSeconds", cwdRange{-64800, -64800, 64800, 64800}, false},
	cwdLocalDayOfWeek:    {"DayOfWeek", cwdRange{1, 1, 7, 7}, false},
	cwdWeekOfMonth:       {"WeekOfMonth", cwdRange{0, 1, 4, 6}, false},
	cwdWeekOfWBY:         {"WeekOfWeekBasedYear", cwdRange{1, 1, 52, 53}, false},
	cwdWeekBasedYear:     {"WeekBasedYear", cwdRange{-999999999, -999999999, 999999999, 999999999}, false},
}

func cwdCheck(f cwdField, v int64) error {
	info := cwdFieldInfo[f]
	if v < info.r.min || v > info.r.max {
		return cwdErr("Invalid value for %s (valid values %s): %d", info.name, info.r, v)
	}
	return nil
}

// cwdZone is a java.time.ZoneId: a ZoneOffset, or a region with rules.
// Only a region java.util.TimeZone knows has display names.
type cwdZone struct {
	id     string
	offset bool
	named  bool
	loc    *time.Location
}

var cwdRegionIDRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9~/._+-]+$`)

// cwdZoneOf is ZoneId.of: Z, an offset, UTC, GMT or UT with or without an
// offset, or a region ID of the time zone database.
func cwdZoneOf(id string) (*cwdZone, error) {
	if id == "Z" || strings.HasPrefix(id, "+") || strings.HasPrefix(id, "-") {
		secs, err := cwdOffsetOf(id)
		if err != nil {
			return nil, err
		}
		return cwdOffsetZone(secs), nil
	}
	if id == "UTC" || id == "GMT" || id == "UT" {
		return &cwdZone{id: id, named: id != "UT", loc: time.FixedZone(id, 0)}, nil
	}
	for _, prefix := range []string{"UTC", "GMT", "UT"} {
		if rest, ok := strings.CutPrefix(id, prefix); ok && (strings.HasPrefix(rest, "+") || strings.HasPrefix(rest, "-")) {
			secs, err := cwdOffsetOf(rest)
			if err != nil {
				return nil, err
			}
			return cwdPrefixedZone(prefix, secs), nil
		}
	}
	if !cwdRegionIDRE.MatchString(id) || id == "Local" || strings.Contains(id, "..") {
		return nil, cwdErr("Invalid ID for region-based ZoneId, invalid format: %s", id)
	}
	loc, err := time.LoadLocation(id)
	if err != nil {
		return nil, cwdErr("Unknown time-zone ID: %s", id)
	}
	return &cwdZone{id: id, named: true, loc: loc}, nil
}

func cwdOffsetZone(secs int) *cwdZone {
	id := cwdOffsetID(secs)
	return &cwdZone{id: id, offset: true, loc: time.FixedZone(id, secs)}
}

func cwdPrefixedZone(prefix string, secs int) *cwdZone {
	id := prefix
	if secs != 0 {
		id += cwdOffsetID(secs)
	}
	return &cwdZone{id: id, loc: time.FixedZone(id, secs)}
}

// cwdOffsetID is ZoneOffset.getId: Z, or ±HH:MM with :SS when non-zero.
func cwdOffsetID(secs int) string {
	if secs == 0 {
		return "Z"
	}
	sign := "+"
	if secs < 0 {
		sign, secs = "-", -secs
	}
	s := fmt.Sprintf("%s%02d:%02d", sign, secs/3600, secs/60%60)
	if secs%60 != 0 {
		s += fmt.Sprintf(":%02d", secs%60)
	}
	return s
}

// cwdOffsetOf is ZoneOffset.of: Z, ±h, ±hh, ±hh:mm, ±hhmm, ±hh:mm:ss or
// ±hhmmss.
func cwdOffsetOf(id string) (int, error) {
	if id == "Z" {
		return 0, nil
	}
	s := id
	num := func(pos int, colon bool) (int, error) {
		if colon && s[pos-1] != ':' {
			return 0, cwdErr("Invalid ID for ZoneOffset, colon not found when expected: %s", id)
		}
		c1, c2 := s[pos], s[pos+1]
		if c1 < '0' || c1 > '9' || c2 < '0' || c2 > '9' {
			return 0, cwdErr("Invalid ID for ZoneOffset, non numeric characters found: %s", id)
		}
		return int(c1-'0')*10 + int(c2-'0'), nil
	}
	var h, m, sec int
	var err error
	switch len(s) {
	case 2:
		s = s[:1] + "0" + s[1:]
		fallthrough
	case 3:
		h, err = num(1, false)
	case 5:
		if h, err = num(1, false); err == nil {
			m, err = num(3, false)
		}
	case 6:
		if h, err = num(1, false); err == nil {
			m, err = num(4, true)
		}
	case 7:
		if h, err = num(1, false); err == nil {
			if m, err = num(3, false); err == nil {
				sec, err = num(5, false)
			}
		}
	case 9:
		if h, err = num(1, false); err == nil {
			if m, err = num(4, true); err == nil {
				sec, err = num(7, true)
			}
		}
	default:
		return 0, cwdErr("Invalid ID for ZoneOffset, invalid format: %s", id)
	}
	if err != nil {
		return 0, err
	}
	if s[0] != '+' && s[0] != '-' {
		return 0, cwdErr("Invalid ID for ZoneOffset, plus/minus not found when expected: %s", id)
	}
	switch {
	case h > 18:
		return 0, cwdErr("Zone offset hours not in valid range: value %d is not in the range -18 to 18", h)
	case m > 59:
		return 0, cwdErr("Zone offset minutes not in valid range: value %d is not in the range -59 to 59", m)
	case sec > 59:
		return 0, cwdErr("Zone offset seconds not in valid range: value %d is not in the range -59 to 59", sec)
	case h == 18 && (m > 0 || sec > 0):
		return 0, cwdErr("Zone offset not in valid range: -18:00 to +18:00")
	}
	total := h*3600 + m*60 + sec
	if s[0] == '-' {
		total = -total
	}
	return total, nil
}

// cwdPrint is a DateTimePrintContext over a ZonedDateTime.
type cwdPrint struct {
	t    time.Time
	zone *cwdZone
	loc  *cwdLocale
}

func cwdFloorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func cwdFloorMod(a, b int64) int64 { return a - cwdFloorDiv(a, b)*b }

func cwdISODow(t time.Time) int {
	return (int(t.Weekday())+6)%7 + 1
}

func cwdYearLen(y int) int {
	if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
		return 366
	}
	return 365
}

func cwdEpochDay(y, m, d int) int64 {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC).Unix() / 86400
}

func cwdDateOfEpochDay(ed int64) time.Time {
	return time.Unix(ed*86400, 0).UTC()
}

// The localized week fields of java.time.temporal.WeekFields.
type cwdWeeks struct{ firstDay, minDays int }

func (w cwdWeeks) localDow(isoDow int) int {
	return int(cwdFloorMod(int64(isoDow-w.firstDay), 7)) + 1
}

func (w cwdWeeks) startOfWeekOffset(day, dow int) int {
	weekStart := int(cwdFloorMod(int64(day-dow), 7))
	offset := -weekStart
	if weekStart+1 > w.minDays {
		offset = 7 - weekStart
	}
	return offset
}

func cwdComputeWeek(offset, day int) int { return (7 + offset + (day - 1)) / 7 }

func (w cwdWeeks) weekOfMonth(d time.Time) int {
	dow := w.localDow(cwdISODow(d))
	return cwdComputeWeek(w.startOfWeekOffset(d.Day(), dow), d.Day())
}

func (w cwdWeeks) weekBasedYear(d time.Time) int {
	dow := w.localDow(cwdISODow(d))
	doy := d.YearDay()
	offset := w.startOfWeekOffset(doy, dow)
	week := cwdComputeWeek(offset, doy)
	if week == 0 {
		return d.Year() - 1
	}
	if week >= cwdComputeWeek(offset, cwdYearLen(d.Year())+w.minDays) {
		return d.Year() + 1
	}
	return d.Year()
}

func (w cwdWeeks) weekOfWeekBasedYear(d time.Time) int {
	dow := w.localDow(cwdISODow(d))
	doy := d.YearDay()
	offset := w.startOfWeekOffset(doy, dow)
	week := cwdComputeWeek(offset, doy)
	if week == 0 {
		return w.weekOfWeekBasedYear(d.AddDate(0, 0, -doy))
	}
	if week > 50 {
		newYearWeek := cwdComputeWeek(offset, cwdYearLen(d.Year())+w.minDays)
		if week >= newYearWeek {
			week = week - newYearWeek + 1
		}
	}
	return week
}

func (w cwdWeeks) ofWeekBasedYear(yowby, wowby, dow int) time.Time {
	date := time.Date(yowby, 1, 1, 0, 0, 0, 0, time.UTC)
	ldow := w.localDow(cwdISODow(date))
	offset := w.startOfWeekOffset(1, ldow)
	newYearWeek := cwdComputeWeek(offset, cwdYearLen(yowby)+w.minDays)
	wowby = min(wowby, newYearWeek-1)
	return date.AddDate(0, 0, -offset+(dow-1)+(wowby-1)*7)
}

func (l *cwdLocale) weeks() cwdWeeks { return cwdWeeks{firstDay: l.firstDayISO(), minDays: l.minDays} }

func (c *cwdPrint) value(f cwdField) int64 {
	t := c.t
	switch f {
	case cwdEra:
		if t.Year() >= 1 {
			return 1
		}
		return 0
	case cwdYearOfEra:
		if t.Year() >= 1 {
			return int64(t.Year())
		}
		return int64(1 - t.Year())
	case cwdYear:
		return int64(t.Year())
	case cwdQuarter:
		return int64((t.Month()-1)/3 + 1)
	case cwdMonth:
		return int64(t.Month())
	case cwdDayOfYear:
		return int64(t.YearDay())
	case cwdDayOfMonth:
		return int64(t.Day())
	case cwdAlignedDOWInMonth:
		return int64((t.Day()-1)%7 + 1)
	case cwdDayOfWeek:
		return int64(cwdISODow(t))
	case cwdAmPm:
		return int64(t.Hour() / 12)
	case cwdHourOfDay:
		return int64(t.Hour())
	case cwdClockHourOfDay:
		if t.Hour() == 0 {
			return 24
		}
		return int64(t.Hour())
	case cwdHourOfAmPm:
		return int64(t.Hour() % 12)
	case cwdClockHourOfAmPm:
		if t.Hour()%12 == 0 {
			return 12
		}
		return int64(t.Hour() % 12)
	case cwdMinute:
		return int64(t.Minute())
	case cwdSecond:
		return int64(t.Second())
	case cwdNano:
		return int64(t.Nanosecond())
	case cwdMilliOfDay:
		return int64(((t.Hour()*60+t.Minute())*60+t.Second())*1000 + t.Nanosecond()/1000000)
	case cwdNanoOfDay:
		return int64((t.Hour()*60+t.Minute())*60+t.Second())*1000000000 + int64(t.Nanosecond())
	case cwdModifiedJulianDay:
		return cwdEpochDay(t.Year(), int(t.Month()), t.Day()) + 40587
	case cwdOffsetSeconds:
		_, off := t.Zone()
		return int64(off)
	case cwdLocalDayOfWeek:
		return int64(c.loc.weeks().localDow(cwdISODow(t)))
	case cwdWeekOfMonth:
		return int64(c.loc.weeks().weekOfMonth(t))
	case cwdWeekOfWBY:
		return int64(c.loc.weeks().weekOfWeekBasedYear(t))
	case cwdWeekBasedYear:
		return int64(c.loc.weeks().weekBasedYear(t))
	}
	return 0
}

// cwdParsed is the field state of java.time.format.Parsed.
type cwdParsed struct {
	fields    map[cwdField]int64
	zone      *cwdZone
	dayPeriod *cwdDayPeriod
}

func (p *cwdParsed) copy() *cwdParsed {
	n := &cwdParsed{fields: make(map[cwdField]int64, len(p.fields)), zone: p.zone, dayPeriod: p.dayPeriod}
	for k, v := range p.fields {
		n.fields[k] = v
	}
	return n
}

type cwdParseCtx struct {
	loc   *cwdLocale
	stack []*cwdParsed
}

func (c *cwdParseCtx) cur() *cwdParsed { return c.stack[len(c.stack)-1] }

func (c *cwdParseCtx) set(f cwdField, v int64, errorPos, successPos int) int {
	old, had := c.cur().fields[f]
	c.cur().fields[f] = v
	if had && old != v {
		return ^errorPos
	}
	return successPos
}

func (c *cwdParseCtx) startOptional() { c.stack = append(c.stack, c.cur().copy()) }

func (c *cwdParseCtx) endOptional(ok bool) {
	if ok {
		c.stack = append(c.stack[:len(c.stack)-2], c.cur())
	} else {
		c.stack = c.stack[:len(c.stack)-1]
	}
}

func cwdRegionMatches(text []rune, pos int, s string) bool {
	rs := []rune(s)
	if pos < 0 || pos+len(rs) > len(text) {
		return false
	}
	for i, r := range rs {
		if text[pos+i] != r {
			return false
		}
	}
	return true
}

// cwdPP is a DateTimePrinterParser.
type cwdPP interface {
	format(c *cwdPrint, b *strings.Builder) (bool, error)
	parse(c *cwdParseCtx, text []rune, pos int) (int, error)
}

type cwdComposite struct {
	pps      []cwdPP
	optional bool
}

func (p *cwdComposite) format(c *cwdPrint, b *strings.Builder) (bool, error) {
	n := b.Len()
	for _, pp := range p.pps {
		ok, err := pp.format(c, b)
		if err != nil {
			return false, err
		}
		if !ok {
			s := b.String()[:n]
			b.Reset()
			b.WriteString(s)
			return true, nil
		}
	}
	return true, nil
}

func (p *cwdComposite) parse(c *cwdParseCtx, text []rune, pos int) (int, error) {
	if !p.optional {
		for _, pp := range p.pps {
			var err error
			if pos, err = pp.parse(c, text, pos); err != nil || pos < 0 {
				return pos, err
			}
		}
		return pos, nil
	}
	c.startOptional()
	cur := pos
	for _, pp := range p.pps {
		next, err := pp.parse(c, text, cur)
		if err != nil {
			return 0, err
		}
		if next < 0 {
			c.endOptional(false)
			return pos, nil
		}
		cur = next
	}
	c.endOptional(true)
	return cur, nil
}

type cwdPad struct {
	pp    cwdPP
	width int
	char  rune
}

func (p *cwdPad) format(c *cwdPrint, b *strings.Builder) (bool, error) {
	var inner strings.Builder
	ok, err := p.pp.format(c, &inner)
	if err != nil || !ok {
		return ok, err
	}
	n := len([]rune(inner.String()))
	if n > p.width {
		return false, cwdErr("Cannot print as output of %d characters exceeds pad width of %d", n, p.width)
	}
	b.WriteString(strings.Repeat(string(p.char), p.width-n))
	b.WriteString(inner.String())
	return true, nil
}

func (p *cwdPad) parse(c *cwdParseCtx, text []rune, position int) (int, error) {
	if position == len(text) {
		return ^position, nil
	}
	end := position + p.width
	if end > len(text) {
		return ^position, nil
	}
	pos := position
	for pos < end && text[pos] == p.char {
		pos++
	}
	res, err := p.pp.parse(c, text[:end], pos)
	if err != nil {
		return 0, err
	}
	if res != end {
		return ^(position + pos), nil
	}
	return res, nil
}

type cwdLiteral struct{ s string }

func (p *cwdLiteral) format(_ *cwdPrint, b *strings.Builder) (bool, error) {
	b.WriteString(p.s)
	return true, nil
}

func (p *cwdLiteral) parse(_ *cwdParseCtx, text []rune, pos int) (int, error) {
	if pos == len(text) || !cwdRegionMatches(text, pos, p.s) {
		return ^pos, nil
	}
	return pos + len([]rune(p.s)), nil
}

type cwdSign int

const (
	cwdSignNormal cwdSign = iota
	cwdSignAlways
	cwdSignNotNegative
	cwdSignExceedsPad
)

// cwdSignParse is SignStyle.parse for strict parsing.
func cwdSignParse(s cwdSign, positive bool) bool {
	switch s {
	case cwdSignNormal:
		return !positive
	case cwdSignAlways, cwdSignExceedsPad:
		return true
	}
	return false
}

type cwdNumKind int

const (
	cwdNumReduced cwdNumKind = iota + 1
	cwdNumFraction
)

var cwdExceedPoints = []int64{0, 10, 100, 1000, 10000, 100000, 1000000, 10000000, 100000000, 1000000000, 10000000000}

// cwdNumber is NumberPrinterParser and its ReducedPrinterParser and
// FractionPrinterParser subclasses.
type cwdNumber struct {
	field              cwdField
	minWidth, maxWidth int
	sign               cwdSign
	subsequentWidth    int
	kind               cwdNumKind
	weekLetter         rune
	weekCount          int
}

func (p *cwdNumber) withFixedWidth() *cwdNumber {
	if p.subsequentWidth == -1 {
		return p
	}
	n := *p
	n.subsequentWidth = -1
	return &n
}

func (p *cwdNumber) withSubsequentWidth(w int) *cwdNumber {
	n := *p
	n.subsequentWidth += w
	return &n
}

// resolveWeek is WeekBasedFieldPrinterParser.printerParser: the localized
// field and widths a week-based letter stands for.
func (p *cwdNumber) resolveWeek() *cwdNumber {
	if p.weekLetter == 0 {
		return p
	}
	n := &cwdNumber{minWidth: p.minWidth, maxWidth: p.maxWidth, sign: cwdSignNotNegative, subsequentWidth: p.subsequentWidth}
	switch p.weekLetter {
	case 'Y':
		n.field = cwdWeekBasedYear
		if p.weekCount == 2 {
			n.kind, n.minWidth, n.maxWidth = cwdNumReduced, 2, 2
		} else {
			n.minWidth, n.maxWidth = p.weekCount, 19
			n.sign = cwdSignNormal
			if p.weekCount >= 4 {
				n.sign = cwdSignExceedsPad
			}
		}
	case 'e', 'c':
		n.field = cwdLocalDayOfWeek
	case 'w':
		n.field = cwdWeekOfWBY
	case 'W':
		n.field = cwdWeekOfMonth
	}
	return n
}

func (p *cwdNumber) format(c *cwdPrint, b *strings.Builder) (bool, error) {
	p = p.resolveWeek()
	value := c.value(p.field)
	if p.kind == cwdNumFraction {
		return true, p.formatFraction(value, b)
	}
	if p.kind == cwdNumReduced {
		abs := value
		if abs < 0 {
			abs = -abs
		}
		base := int64(2000)
		if value >= base && value < base+cwdExceedPoints[p.minWidth] {
			value = abs % cwdExceedPoints[p.minWidth]
		} else {
			value = abs % cwdExceedPoints[p.maxWidth]
		}
	}
	abs := value
	if abs < 0 {
		abs = -abs
	}
	str := strconv.FormatInt(abs, 10)
	if len(str) > p.maxWidth {
		return false, cwdErr("Field %s cannot be printed as the value %d exceeds the maximum print width of %d", cwdFieldInfo[p.field].name, value, p.maxWidth)
	}
	if value >= 0 {
		switch p.sign {
		case cwdSignExceedsPad:
			if p.minWidth < 19 && value >= cwdExceedPoints[min(p.minWidth, 10)] {
				b.WriteByte('+')
			}
		case cwdSignAlways:
			b.WriteByte('+')
		}
	} else {
		switch p.sign {
		case cwdSignNormal, cwdSignExceedsPad, cwdSignAlways:
			b.WriteByte('-')
		case cwdSignNotNegative:
			return false, cwdErr("Field %s cannot be printed as the value %d cannot be negative according to the SignStyle", cwdFieldInfo[p.field].name, value)
		}
	}
	for i := 0; i < p.minWidth-len(str); i++ {
		b.WriteByte('0')
	}
	b.WriteString(str)
	return true, nil
}

func (p *cwdNumber) formatFraction(value int64, b *strings.Builder) error {
	digits := fmt.Sprintf("%09d", value)
	trimmed := strings.TrimRight(digits, "0")
	if trimmed == "" {
		b.WriteString(strings.Repeat("0", p.minWidth))
		return nil
	}
	scale := min(max(len(trimmed), p.minWidth), p.maxWidth)
	b.WriteString(digits[:scale])
	return nil
}

func (p *cwdNumber) parse(c *cwdParseCtx, text []rune, position int) (int, error) {
	p = p.resolveWeek()
	if p.kind == cwdNumFraction {
		return p.parseFraction(c, text, position)
	}
	length := len(text)
	if position == length {
		return ^position, nil
	}
	sign := text[position]
	negative, positive := false, false
	switch sign {
	case '+':
		if !cwdSignParse(p.sign, true) {
			return ^position, nil
		}
		positive = true
		position++
	case '-':
		if !cwdSignParse(p.sign, false) {
			return ^position, nil
		}
		negative = true
		position++
	default:
		if p.sign == cwdSignAlways {
			return ^position, nil
		}
	}
	effMin := p.minWidth
	minEnd := position + effMin
	if minEnd > length {
		return ^position, nil
	}
	effMax := p.maxWidth + max(p.subsequentWidth, 0)
	var total int64
	var big bool
	pos := position
	for pass := 0; pass < 2; pass++ {
		maxEnd := min(pos+effMax, length)
		for pos < maxEnd {
			ch := text[pos]
			pos++
			if ch < '0' || ch > '9' {
				pos--
				if pos < minEnd {
					return ^position, nil
				}
				break
			}
			if pos-position > 18 {
				big = true
			}
			total = total*10 + int64(ch-'0')
		}
		if p.subsequentWidth > 0 && pass == 0 {
			parseLen := pos - position
			effMax = max(effMin, parseLen-p.subsequentWidth)
			pos, total, big = position, 0, false
			continue
		}
		break
	}
	if big {
		return ^position, nil
	}
	if negative {
		if total == 0 {
			return ^(position - 1), nil
		}
		total = -total
	} else if p.sign == cwdSignExceedsPad {
		parseLen := pos - position
		if positive {
			if parseLen <= p.minWidth {
				return ^(position - 1), nil
			}
		} else if parseLen > p.minWidth {
			return ^position, nil
		}
	}
	if p.kind == cwdNumReduced {
		parseLen := pos - position
		if parseLen == p.minWidth && total >= 0 {
			base := int64(2000)
			rng := cwdExceedPoints[p.minWidth]
			basePart := base - base%rng
			total += basePart
			if total < base {
				total += rng
			}
		}
	}
	return c.set(p.field, total, position, pos), nil
}

func (p *cwdNumber) parseFraction(c *cwdParseCtx, text []rune, position int) (int, error) {
	length := len(text)
	if position == length {
		if p.minWidth > 0 {
			return ^position, nil
		}
		return position, nil
	}
	minEnd := position + p.minWidth
	if minEnd > length {
		return ^position, nil
	}
	maxEnd := min(position+p.maxWidth, length)
	total := 0
	pos := position
	for pos < maxEnd {
		ch := text[pos]
		pos++
		if ch < '0' || ch > '9' {
			if pos <= minEnd {
				return ^position, nil
			}
			pos--
			break
		}
		total = total*10 + int(ch-'0')
	}
	digits := pos - position
	value := int64(total) * int64(math.Pow10(9-digits))
	return c.set(cwdNano, value, position, pos), nil
}

type cwdText struct {
	field cwdField
	style cwdStyle
}

// names returns the locale's names for a text field and style by value,
// as DateTimeTextProvider's LocaleStore holds them.
func (p *cwdText) names(l *cwdLocale, s cwdStyle) map[int64]string {
	out := map[int64]string{}
	switch p.field {
	case cwdMonth:
		for i, n := range l.months(s) {
			if n != "" {
				out[int64(i+1)] = n
			}
		}
	case cwdDayOfWeek:
		for i, n := range l.days(s) {
			if n != "" {
				out[int64((i+6)%7+1)] = n
			}
		}
	case cwdQuarter:
		for i, n := range l.quarters(s) {
			if n != "" {
				out[int64(i+1)] = n
			}
		}
	case cwdEra:
		if s.standalone() {
			return nil
		}
		for i, n := range l.eras(s) {
			if n != "" {
				out[int64(i)] = n
			}
		}
	case cwdAmPm:
		if s.standalone() {
			return nil
		}
		ps := l.periods(cwdFull)
		if s == cwdNarrow {
			ps = l.periods(cwdNarrow)
		}
		for i, n := range ps[:2] {
			if n != "" {
				out[int64(i)] = n
			}
		}
	}
	return out
}

func (p *cwdText) format(c *cwdPrint, b *strings.Builder) (bool, error) {
	v := c.value(p.field)
	if n, ok := p.names(c.loc, p.style)[v]; ok {
		b.WriteString(n)
		return true, nil
	}
	return (&cwdNumber{field: p.field, minWidth: 1, maxWidth: 19}).format(c, b)
}

// cwdTextEntries orders a style's names longest first; of two values that
// share a name, the later one parses.
func cwdTextEntries(names map[int64]string) []cwdTextEntry {
	byName := map[string]int64{}
	var values []int64
	for v := range names {
		values = append(values, v)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	for _, v := range values {
		byName[names[v]] = v
	}
	var out []cwdTextEntry
	for n, v := range byName {
		out = append(out, cwdTextEntry{n, v})
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := len([]rune(out[i].text)), len([]rune(out[j].text))
		if li != lj {
			return li > lj
		}
		return out[i].value < out[j].value
	})
	return out
}

type cwdTextEntry struct {
	text  string
	value int64
}

func (p *cwdText) parse(c *cwdParseCtx, text []rune, pos int) (int, error) {
	names := p.names(c.loc, p.style)
	if len(names) == 0 {
		return (&cwdNumber{field: p.field, minWidth: 1, maxWidth: 19}).parse(c, text, pos)
	}
	for _, e := range cwdTextEntries(names) {
		if cwdRegionMatches(text, pos, e.text) {
			return c.set(p.field, e.value, pos, pos+len([]rune(e.text))), nil
		}
	}
	return ^pos, nil
}

type cwdDayPeriodText struct{ style cwdStyle }

func (p *cwdDayPeriodText) names(l *cwdLocale) map[int64]string {
	ps := l.periods(p.style)
	out := map[int64]string{}
	for _, dp := range l.namedPeriods() {
		if n := ps[dp.index]; n != "" {
			out[int64(dp.index)] = n
		}
	}
	return out
}

func (p *cwdDayPeriodText) format(c *cwdPrint, b *strings.Builder) (bool, error) {
	mod := c.t.Hour()*60 + c.t.Minute()
	idx := mod / 720
	if dp, ok := c.loc.periodFor(mod); ok {
		idx = dp.index
	}
	b.WriteString(p.names(c.loc)[int64(idx)])
	return true, nil
}

func (p *cwdDayPeriodText) parse(c *cwdParseCtx, text []rune, pos int) (int, error) {
	for _, e := range cwdTextEntries(p.names(c.loc)) {
		if cwdRegionMatches(text, pos, e.text) {
			for _, dp := range c.loc.namedPeriods() {
				if int64(dp.index) == e.value {
					dp := dp
					c.cur().dayPeriod = &dp
					break
				}
			}
			return pos + len([]rune(e.text)), nil
		}
	}
	return ^pos, nil
}

var cwdOffsetPatterns = []string{
	"+HH", "+HHmm", "+HH:mm", "+HHMM", "+HH:MM", "+HHMMss", "+HH:MM:ss", "+HHMMSS", "+HH:MM:SS", "+HHmmss", "+HH:mm:ss",
	"+H", "+Hmm", "+H:mm", "+HMM", "+H:MM", "+HMMss", "+H:MM:ss", "+HMMSS", "+H:MM:SS", "+Hmmss", "+H:mm:ss",
}

// cwdOffset is OffsetIdPrinterParser.
type cwdOffset struct {
	typ, style int
	noOffset   string
}

func newCWDOffset(pattern, noOffset string) *cwdOffset {
	for i, p := range cwdOffsetPatterns {
		if p == pattern {
			return &cwdOffset{typ: i, style: i % 11, noOffset: noOffset}
		}
	}
	panic("unknown offset pattern " + pattern)
}

func (p *cwdOffset) colon() bool { return p.style > 0 && p.style%2 == 0 }

func (p *cwdOffset) format(c *cwdPrint, b *strings.Builder) (bool, error) {
	total := int(c.value(cwdOffsetSeconds))
	if total == 0 {
		b.WriteString(p.noOffset)
		return true, nil
	}
	absH, absM, absS := cwdAbs(total/3600%100), cwdAbs(total/60%60), cwdAbs(total%60)
	var s strings.Builder
	if total < 0 {
		s.WriteByte('-')
	} else {
		s.WriteByte('+')
	}
	pad := func(colon bool, v int) {
		if colon {
			s.WriteByte(':')
		}
		fmt.Fprintf(&s, "%02d", v)
	}
	if p.typ < 11 || absH >= 10 {
		fmt.Fprintf(&s, "%02d", absH)
	} else {
		s.WriteByte(byte('0' + absH))
	}
	output := absH
	st := p.style
	if (st >= 3 && st <= 8) || (st >= 9 && absS > 0) || (st >= 1 && absM > 0) {
		pad(p.colon(), absM)
		output += absM
		if st == 7 || st == 8 || (st >= 5 && absS > 0) {
			pad(p.colon(), absS)
			output += absS
		}
	}
	if output == 0 {
		b.WriteString(p.noOffset)
	} else {
		b.WriteString(s.String())
	}
	return true, nil
}

func cwdAbs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (p *cwdOffset) parse(c *cwdParseCtx, text []rune, position int) (int, error) {
	length := len(text)
	noLen := len([]rune(p.noOffset))
	if noLen == 0 {
		if position == length {
			return c.set(cwdOffsetSeconds, 0, position, position), nil
		}
	} else {
		if position == length {
			return ^position, nil
		}
		if cwdRegionMatches(text, position, p.noOffset) {
			return c.set(cwdOffsetSeconds, 0, position, position+noLen), nil
		}
	}
	sign := text[position]
	if sign == '+' || sign == '-' {
		negative := 1
		if sign == '-' {
			negative = -1
		}
		colon, padded := p.colon(), p.typ < 11
		arr := [4]int{position + 1}
		digits := func(colon bool, idx int) bool {
			pos := arr[0]
			if pos < 0 {
				return true
			}
			if colon && idx != 1 {
				if pos+1 > length || text[pos] != ':' {
					return false
				}
				pos++
			}
			if pos+2 > length {
				return false
			}
			c1, c2 := text[pos], text[pos+1]
			if c1 < '0' || c1 > '9' || c2 < '0' || c2 > '9' {
				return false
			}
			v := int(c1-'0')*10 + int(c2-'0')
			if v > 59 {
				return false
			}
			arr[idx] = v
			arr[0] = pos + 2
			return true
		}
		variable := func(minD, maxD int) {
			pos := arr[0]
			var ch []int
			for i := 0; i < maxD && pos < length; i++ {
				r := text[pos]
				if r < '0' || r > '9' {
					break
				}
				ch = append(ch, int(r-'0'))
				pos++
			}
			if len(ch) < minD {
				arr[0] = ^arr[0]
				return
			}
			switch len(ch) {
			case 1:
				arr[1] = ch[0]
			case 2:
				arr[1] = ch[0]*10 + ch[1]
			case 3:
				arr[1], arr[2] = ch[0], ch[1]*10+ch[2]
			case 4:
				arr[1], arr[2] = ch[0]*10+ch[1], ch[2]*10+ch[3]
			case 5:
				arr[1], arr[2], arr[3] = ch[0], ch[1]*10+ch[2], ch[3]*10+ch[4]
			case 6:
				arr[1], arr[2], arr[3] = ch[0]*10+ch[1], ch[2]*10+ch[3], ch[4]*10+ch[5]
			}
			arr[0] = pos
		}
		hour := func() {
			if padded {
				if !digits(false, 1) {
					arr[0] = ^arr[0]
				}
			} else {
				variable(1, 2)
			}
		}
		part := func(idx int, mandatory bool) {
			if !digits(colon, idx) && mandatory {
				arr[0] = ^arr[0]
			}
		}
		switch p.typ {
		case 0, 11:
			hour()
		case 1, 2, 13:
			hour()
			part(2, false)
		case 3, 4, 15:
			hour()
			part(2, true)
		case 5, 6, 17:
			hour()
			part(2, true)
			part(3, false)
		case 7, 8, 19:
			hour()
			part(2, true)
			part(3, true)
		case 9, 10, 21:
			hour()
			if digits(colon, 2) {
				digits(colon, 3)
			}
		case 12:
			variable(1, 4)
		case 14:
			variable(3, 4)
		case 16:
			variable(3, 6)
		case 18:
			variable(5, 6)
		case 20:
			variable(1, 6)
		}
		if arr[0] > 0 {
			if arr[1] > 23 || arr[2] > 59 || arr[3] > 59 {
				return 0, cwdErr("Value out of range: Hour[0-23], Minute[0-59], Second[0-59]")
			}
			secs := int64(negative * (arr[1]*3600 + arr[2]*60 + arr[3]))
			return c.set(cwdOffsetSeconds, secs, position, arr[0]), nil
		}
	}
	if noLen == 0 {
		return c.set(cwdOffsetSeconds, 0, position, position), nil
	}
	return ^position, nil
}

// cwdLocalizedOffset is LocalizedOffsetIdPrinterParser, the O pattern.
type cwdLocalizedOffset struct{ full bool }

func (p *cwdLocalizedOffset) gmt(l *cwdLocale) string {
	if l.formats[2] != "" {
		return l.formats[2]
	}
	return "GMT"
}

func (p *cwdLocalizedOffset) format(c *cwdPrint, b *strings.Builder) (bool, error) {
	total := int(c.value(cwdOffsetSeconds))
	b.WriteString(p.gmt(c.loc))
	if total == 0 {
		return true, nil
	}
	h, m, s := cwdAbs(total/3600%100), cwdAbs(total/60%60), cwdAbs(total%60)
	if total < 0 {
		b.WriteByte('-')
	} else {
		b.WriteByte('+')
	}
	if p.full {
		fmt.Fprintf(b, "%02d:%02d", h, m)
		if s != 0 {
			fmt.Fprintf(b, ":%02d", s)
		}
		return true, nil
	}
	fmt.Fprintf(b, "%d", h)
	if m != 0 || s != 0 {
		fmt.Fprintf(b, ":%02d", m)
		if s != 0 {
			fmt.Fprintf(b, ":%02d", s)
		}
	}
	return true, nil
}

func (p *cwdLocalizedOffset) parse(c *cwdParseCtx, text []rune, position int) (int, error) {
	gmt := p.gmt(c.loc)
	if !cwdRegionMatches(text, position, gmt) {
		return ^position, nil
	}
	pos := position + len([]rune(gmt))
	end := len(text)
	if pos == end {
		return c.set(cwdOffsetSeconds, 0, position, pos), nil
	}
	var negative int
	switch text[pos] {
	case '+':
		negative = 1
	case '-':
		negative = -1
	default:
		return c.set(cwdOffsetSeconds, 0, position, pos), nil
	}
	pos++
	digit := func(i int) int {
		if i >= end || text[i] < '0' || text[i] > '9' {
			return -1
		}
		return int(text[i] - '0')
	}
	var h, m, s int
	if p.full {
		h1, h2 := digit(pos), digit(pos+1)
		if h1 < 0 || h2 < 0 || pos+2 >= end || text[pos+2] != ':' {
			return ^position, nil
		}
		h = h1*10 + h2
		pos += 3
		m1, m2 := digit(pos), digit(pos+1)
		if m1 < 0 || m2 < 0 {
			return ^position, nil
		}
		m = m1*10 + m2
		pos += 2
		if pos+2 < end && text[pos] == ':' {
			if s1, s2 := digit(pos+1), digit(pos+2); s1 >= 0 && s2 >= 0 {
				s = s1*10 + s2
				pos += 3
			}
		}
	} else {
		h = digit(pos)
		if h < 0 {
			return ^position, nil
		}
		pos++
		if pos < end {
			if h2 := digit(pos); h2 >= 0 {
				h = h*10 + h2
				pos++
			}
			if pos+2 < end && text[pos] == ':' {
				if m1, m2 := digit(pos+1), digit(pos+2); m1 >= 0 && m2 >= 0 {
					m = m1*10 + m2
					pos += 3
					if pos+2 < end && text[pos] == ':' {
						if s1, s2 := digit(pos+1), digit(pos+2); s1 >= 0 && s2 >= 0 {
							s = s1*10 + s2
							pos += 3
						}
					}
				}
			}
		}
	}
	return c.set(cwdOffsetSeconds, int64(negative*(h*3600+m*60+s)), position, pos), nil
}

// cwdZoneID is ZoneIdPrinterParser (VV) and, with a text style,
// ZoneTextPrinterParser (z and v).
type cwdZoneID struct {
	text    bool
	full    bool
	generic bool
}

func (p *cwdZoneID) format(c *cwdPrint, b *strings.Builder) (bool, error) {
	if !p.text || !c.zone.named {
		b.WriteString(c.zone.id)
		return true, nil
	}
	names := c.loc.zoneNames(c.zone.id)
	idx := 1
	if p.full {
		idx = 0
	}
	switch {
	case p.generic:
		idx += 4
	case c.t.IsDST():
		idx += 2
	}
	if n := names[idx]; n != "" {
		b.WriteString(n)
	} else {
		b.WriteString(c.zone.id)
	}
	return true, nil
}

var cwdZoneTrees sync.Map

// tree is the PrefixTree of the zone IDs and, for text, the locale's zone
// names, each name standing for its metazone's preferred zone.
func (p *cwdZoneID) tree(l *cwdLocale) map[string]string {
	key := fmt.Sprintf("%v|%v|%s", p.text, p.full, l.tag)
	if v, ok := cwdZoneTrees.Load(key); ok {
		if cached, isTree := v.(map[string]string); isTree {
			return cached
		}
	}
	t := map[string]string{}
	for _, id := range cwdZoneIDs() {
		t[id] = id
	}
	if p.text {
		first := 1
		if p.full {
			first = 0
		}
		for _, id := range cwdZoneIDs() {
			if strings.HasPrefix(id, "Etc/GMT") || strings.HasPrefix(id, "GMT") || strings.HasPrefix(id, "SystemV") {
				continue
			}
			names := l.zoneNames(id)
			zid := l.preferredZone(id)
			for i := first; i < 6; i += 2 {
				if names[i] != "" {
					t[names[i]] = zid
				}
			}
		}
	}
	cwdZoneTrees.Store(key, t)
	return t
}

func (p *cwdZoneID) parse(c *cwdParseCtx, text []rune, position int) (int, error) {
	length := len(text)
	if position == length {
		return ^position, nil
	}
	next := text[position]
	if next == '+' || next == '-' {
		return p.parseOffsetBased(c, text, position, position, newCWDOffset("+HH:MM:ss", "Z"))
	}
	if length >= position+2 {
		nn := text[position+1]
		if next == 'U' && nn == 'T' {
			if length >= position+3 && text[position+2] == 'C' {
				if !p.text {
					return p.parseOffsetBased(c, text, position, position+3, newCWDOffset("+HH:MM:ss", "0"))
				}
			} else {
				return p.parseOffsetBased(c, text, position, position+2, newCWDOffset("+HH:MM:ss", "0"))
			}
		} else if next == 'G' && length >= position+3 && nn == 'M' && text[position+2] == 'T' {
			if length >= position+4 && text[position+3] == '0' {
				z, err := cwdZoneOf("GMT0")
				if err != nil {
					return 0, err
				}
				c.cur().zone = z
				return position + 4, nil
			}
			return p.parseOffsetBased(c, text, position, position+3, newCWDOffset("+HH:MM:ss", "0"))
		}
	}
	tree := p.tree(c.loc)
	for end := length; end > position; end-- {
		if id, ok := tree[string(text[position:end])]; ok {
			z, err := cwdZoneOf(id)
			if err != nil {
				return 0, err
			}
			c.cur().zone = z
			return end, nil
		}
	}
	if next == 'Z' {
		c.cur().zone = cwdOffsetZone(0)
		return position + 1, nil
	}
	return ^position, nil
}

func (p *cwdZoneID) parseOffsetBased(c *cwdParseCtx, text []rune, prefixPos, position int, parser *cwdOffset) (int, error) {
	prefix := strings.ToUpper(string(text[prefixPos:position]))
	setPrefix := func() {
		if prefix == "" {
			c.cur().zone = cwdOffsetZone(0)
		} else {
			c.cur().zone = cwdPrefixedZone(prefix, 0)
		}
	}
	if position >= len(text) {
		setPrefix()
		return position, nil
	}
	if text[position] == '0' || text[position] == 'Z' {
		setPrefix()
		return position, nil
	}
	sub := &cwdParseCtx{loc: c.loc, stack: []*cwdParsed{{fields: map[cwdField]int64{}}}}
	end, err := parser.parse(sub, text, position)
	if err != nil {
		return ^prefixPos, nil
	}
	if end < 0 {
		if parser.noOffset == "Z" {
			return ^prefixPos, nil
		}
		setPrefix()
		return position, nil
	}
	secs := int(sub.cur().fields[cwdOffsetSeconds])
	if secs < -64800 || secs > 64800 {
		return ^prefixPos, nil
	}
	if prefix == "" {
		c.cur().zone = cwdOffsetZone(secs)
	} else {
		c.cur().zone = cwdPrefixedZone(prefix, secs)
	}
	return end, nil
}

// cwdBuilder is DateTimeFormatterBuilder for the pattern letters.
type cwdBuilder struct {
	parent      *cwdBuilder
	pps         []cwdPP
	valueParser int
	padWidth    int
	optional    bool
}

func (b *cwdBuilder) appendInternal(pp cwdPP) int {
	if b.padWidth > 0 {
		pp = &cwdPad{pp: pp, width: b.padWidth, char: ' '}
		b.padWidth = 0
	}
	b.pps = append(b.pps, pp)
	b.valueParser = -1
	return len(b.pps) - 1
}

func (b *cwdBuilder) appendValue(pp *cwdNumber) {
	base, isNumber := (*cwdNumber)(nil), false
	if b.valueParser >= 0 {
		base, isNumber = b.pps[b.valueParser].(*cwdNumber)
	}
	if isNumber {
		active := b.valueParser
		if pp.minWidth == pp.maxWidth && pp.sign == cwdSignNotNegative {
			base = base.withSubsequentWidth(pp.maxWidth)
			b.appendInternal(pp.withFixedWidth())
			b.valueParser = active
		} else {
			base = base.withFixedWidth()
			b.valueParser = b.appendInternal(pp)
		}
		b.pps[active] = base
		return
	}
	b.valueParser = b.appendInternal(pp)
}

func (b *cwdBuilder) value(f cwdField, minW, maxW int, sign cwdSign) {
	b.appendValue(&cwdNumber{field: f, minWidth: minW, maxWidth: maxW, sign: sign})
}

func (b *cwdBuilder) week(letter rune, count, minW, maxW int) {
	b.appendValue(&cwdNumber{weekLetter: letter, weekCount: count, minWidth: minW, maxWidth: maxW, sign: cwdSignNotNegative})
}

func (b *cwdBuilder) literal(s string) {
	if s != "" {
		b.appendInternal(&cwdLiteral{s})
	}
}

var cwdFieldOfLetter = map[byte]cwdField{
	'G': cwdEra, 'y': cwdYearOfEra, 'u': cwdYear, 'Q': cwdQuarter, 'q': cwdQuarter, 'M': cwdMonth, 'L': cwdMonth,
	'D': cwdDayOfYear, 'd': cwdDayOfMonth, 'F': cwdAlignedDOWInMonth, 'E': cwdDayOfWeek, 'c': cwdDayOfWeek,
	'e': cwdDayOfWeek, 'a': cwdAmPm, 'H': cwdHourOfDay, 'k': cwdClockHourOfDay, 'K': cwdHourOfAmPm,
	'h': cwdClockHourOfAmPm, 'm': cwdMinute, 's': cwdSecond, 'S': cwdNano, 'A': cwdMilliOfDay, 'n': cwdNano,
	'N': cwdNanoOfDay, 'g': cwdModifiedJulianDay,
}

// cwdCompilePattern is DateTimeFormatterBuilder.appendPattern then
// toFormatter.
func cwdCompilePattern(pattern string) (cwdPP, error) {
	root := &cwdBuilder{valueParser: -1}
	b := root
	for pos := 0; pos < len(pattern); pos++ {
		cur := pattern[pos]
		switch {
		case cur >= 'A' && cur <= 'Z' || cur >= 'a' && cur <= 'z':
			start := pos
			pos++
			for pos < len(pattern) && pattern[pos] == cur {
				pos++
			}
			count := pos - start
			if cur == 'p' {
				pad := 0
				if pos < len(pattern) {
					if c := pattern[pos]; c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
						pad = count
						cur = c
						start = pos
						pos++
						for pos < len(pattern) && pattern[pos] == cur {
							pos++
						}
						count = pos - start
					}
				}
				if pad == 0 {
					return nil, cwdErr("Pad letter 'p' must be followed by valid pad pattern: %s", pattern)
				}
				b.padWidth = pad
				b.valueParser = -1
			}
			if err := b.letter(cur, count); err != nil {
				return nil, err
			}
			pos--
		case cur == '\'':
			start := pos
			pos++
			for ; pos < len(pattern); pos++ {
				if pattern[pos] == '\'' {
					if pos+1 < len(pattern) && pattern[pos+1] == '\'' {
						pos++
					} else {
						break
					}
				}
			}
			if pos >= len(pattern) {
				return nil, cwdErr("Pattern ends with an incomplete string literal: %s", pattern)
			}
			str := pattern[start+1 : pos]
			if str == "" {
				b.literal("'")
			} else {
				b.literal(strings.ReplaceAll(str, "''", "'"))
			}
		case cur == '[':
			b.valueParser = -1
			b = &cwdBuilder{parent: b, valueParser: -1, optional: true}
		case cur == ']':
			if b.parent == nil {
				return nil, cwdErr("Pattern invalid as it contains ] without previous [")
			}
			b = b.close()
		case cur == '{' || cur == '}' || cur == '#':
			return nil, cwdErr("Pattern includes reserved character: '%c'", cur)
		default:
			r, size := []rune(pattern[pos:])[0], len(string([]rune(pattern[pos:])[0]))
			b.literal(string(r))
			pos += size - 1
		}
	}
	for b.parent != nil {
		b = b.close()
	}
	return &cwdComposite{pps: b.pps}, nil
}

func (b *cwdBuilder) close() *cwdBuilder {
	parent := b.parent
	if len(b.pps) > 0 {
		parent.appendInternal(&cwdComposite{pps: b.pps, optional: true})
	}
	return parent
}

func (b *cwdBuilder) letter(cur byte, count int) error {
	tooMany := cwdErr("Too many pattern letters: %c", cur)
	if f, ok := cwdFieldOfLetter[cur]; ok {
		return b.field(cur, count, f)
	}
	switch cur {
	case 'z':
		if count > 4 {
			return tooMany
		}
		b.appendInternal(&cwdZoneID{text: true, full: count == 4})
	case 'V':
		if count != 2 {
			return cwdErr("Pattern letter count must be 2: %c", cur)
		}
		b.appendInternal(&cwdZoneID{})
	case 'v':
		switch count {
		case 1:
			b.appendInternal(&cwdZoneID{text: true, generic: true})
		case 4:
			b.appendInternal(&cwdZoneID{text: true, generic: true, full: true})
		default:
			return cwdErr("Wrong number of pattern letters: %c", cur)
		}
	case 'Z':
		switch {
		case count < 4:
			b.appendInternal(newCWDOffset("+HHMM", "+0000"))
		case count == 4:
			b.appendInternal(&cwdLocalizedOffset{full: true})
		case count == 5:
			b.appendInternal(newCWDOffset("+HH:MM:ss", "Z"))
		default:
			return tooMany
		}
	case 'O':
		switch count {
		case 1:
			b.appendInternal(&cwdLocalizedOffset{})
		case 4:
			b.appendInternal(&cwdLocalizedOffset{full: true})
		default:
			return cwdErr("Pattern letter count must be 1 or 4: %c", cur)
		}
	case 'X', 'x':
		if count > 5 {
			return tooMany
		}
		i := count
		if count != 1 {
			i++
		}
		zero := "Z"
		if cur == 'x' {
			switch {
			case count == 1:
				zero = "+00"
			case count%2 == 0:
				zero = "+0000"
			default:
				zero = "+00:00"
			}
		}
		b.appendInternal(newCWDOffset(cwdOffsetPatterns[i], zero))
	case 'W':
		if count > 1 {
			return tooMany
		}
		b.week('W', count, count, count)
	case 'w':
		if count > 2 {
			return tooMany
		}
		b.week('w', count, count, 2)
	case 'Y':
		if count == 2 {
			b.week('Y', count, count, 2)
		} else {
			b.week('Y', count, count, 19)
		}
	case 'B':
		switch count {
		case 1:
			b.appendInternal(&cwdDayPeriodText{style: cwdShort})
		case 4:
			b.appendInternal(&cwdDayPeriodText{style: cwdFull})
		case 5:
			b.appendInternal(&cwdDayPeriodText{style: cwdNarrow})
		default:
			return cwdErr("Wrong number of pattern letters: %c", cur)
		}
	default:
		return cwdErr("Unknown pattern letter: %c", cur)
	}
	return nil
}

func (b *cwdBuilder) field(cur byte, count int, f cwdField) error {
	tooMany := cwdErr("Too many pattern letters: %c", cur)
	switch cur {
	case 'u', 'y':
		switch {
		case count == 2:
			b.appendValue(&cwdNumber{field: f, minWidth: 2, maxWidth: 2, sign: cwdSignNotNegative, kind: cwdNumReduced})
		case count < 4:
			b.value(f, count, 19, cwdSignNormal)
		default:
			b.value(f, count, 19, cwdSignExceedsPad)
		}
		if count > 19 {
			return cwdErr("The minimum width must be from 1 to 19 inclusive but was %d", count)
		}
	case 'c':
		if count == 1 {
			b.week('c', count, count, count)
			return nil
		}
		if count == 2 {
			return cwdErr(`Invalid pattern "cc"`)
		}
		return b.text(cur, count, f, true)
	case 'L', 'q':
		return b.text(cur, count, f, true)
	case 'M', 'Q', 'E', 'e':
		return b.text(cur, count, f, false)
	case 'a':
		if count != 1 {
			return tooMany
		}
		b.appendInternal(&cwdText{field: f, style: cwdShort})
	case 'G':
		switch count {
		case 1, 2, 3:
			b.appendInternal(&cwdText{field: f, style: cwdShort})
		case 4:
			b.appendInternal(&cwdText{field: f, style: cwdFull})
		case 5:
			b.appendInternal(&cwdText{field: f, style: cwdNarrow})
		default:
			return tooMany
		}
	case 'S':
		if count > 9 {
			return cwdErr("Maximum width must be from 1 to 9 inclusive but was %d", count)
		}
		b.appendValue(&cwdNumber{field: cwdNano, minWidth: count, maxWidth: count, sign: cwdSignNotNegative, kind: cwdNumFraction})
	case 'F':
		if count != 1 {
			return tooMany
		}
		b.value(f, 1, 19, cwdSignNormal)
	case 'd', 'h', 'H', 'k', 'K', 'm', 's':
		switch count {
		case 1:
			b.value(f, 1, 19, cwdSignNormal)
		case 2:
			b.value(f, 2, 2, cwdSignNotNegative)
		default:
			return tooMany
		}
	case 'D':
		switch count {
		case 1:
			b.value(f, 1, 19, cwdSignNormal)
		case 2, 3:
			b.value(f, count, 3, cwdSignNotNegative)
		default:
			return tooMany
		}
	case 'g':
		if count > 19 {
			return cwdErr("The minimum width must be from 1 to 19 inclusive but was %d", count)
		}
		b.value(f, count, 19, cwdSignNormal)
	case 'A', 'n', 'N':
		if count > 19 {
			return cwdErr("The minimum width must be from 1 to 19 inclusive but was %d", count)
		}
		b.value(f, count, 19, cwdSignNotNegative)
	}
	return nil
}

func (b *cwdBuilder) text(cur byte, count int, f cwdField, standalone bool) error {
	style := func(format, alone cwdStyle) cwdStyle {
		if standalone {
			return alone
		}
		return format
	}
	switch count {
	case 1, 2:
		switch {
		case cur == 'e' || cur == 'c':
			b.week(rune(cur), count, count, count)
		case cur == 'E':
			b.appendInternal(&cwdText{field: f, style: cwdShort})
		case count == 1:
			b.value(f, 1, 19, cwdSignNormal)
		default:
			b.value(f, 2, 2, cwdSignNotNegative)
		}
	case 3:
		b.appendInternal(&cwdText{field: f, style: style(cwdShort, cwdShortStandalone)})
	case 4:
		b.appendInternal(&cwdText{field: f, style: style(cwdFull, cwdFullStandalone)})
	case 5:
		b.appendInternal(&cwdText{field: f, style: style(cwdNarrow, cwdNarrowStandalone)})
	default:
		return cwdErr("Too many pattern letters: %c", cur)
	}
	return nil
}

// cwdFormatter is a compiled DateTimeFormatter pattern.
type cwdFormatter struct {
	pp cwdPP
}

func (f *cwdFormatter) format(t time.Time, zone *cwdZone, loc *cwdLocale) (string, error) {
	var b strings.Builder
	if _, err := f.pp.format(&cwdPrint{t: t, zone: zone, loc: loc}, &b); err != nil {
		return "", err
	}
	return b.String(), nil
}

// parse is DateTimeFormatter.parse with the SMART resolver, then the
// zoned date-time: the parsed zone, the parsed offset, or the source zone.
func (f *cwdFormatter) parse(s string, loc *cwdLocale, source *cwdZone) (time.Time, error) {
	text := []rune(s)
	abbr := s
	if len(text) > 64 {
		abbr = string(text[:64]) + "..."
	}
	ctx := &cwdParseCtx{loc: loc, stack: []*cwdParsed{{fields: map[cwdField]int64{}}}}
	pos, err := f.pp.parse(ctx, text, 0)
	if err != nil {
		return time.Time{}, cwdErr("Text '%s' could not be parsed: %v", abbr, err)
	}
	if pos < 0 {
		return time.Time{}, cwdErr("Text '%s' could not be parsed at index %d", abbr, ^pos)
	}
	if pos < len(text) {
		return time.Time{}, cwdErr("Text '%s' could not be parsed, unparsed text found at index %d", abbr, pos)
	}
	r := &cwdResolver{p: ctx.cur(), weeks: loc.weeks()}
	if err := r.resolve(); err != nil {
		return time.Time{}, cwdErr("Text '%s' could not be parsed: %v", abbr, err)
	}
	if r.date == nil {
		return time.Time{}, cwdErr("Text '%s' could not be parsed: the text has no date", abbr)
	}
	d := *r.date
	nod := int64(0)
	if r.time != nil {
		nod = *r.time
		if r.excessDays != 0 {
			d = d.AddDate(0, 0, r.excessDays)
		}
	}
	h, mi, se, ns := int(nod/3600e9), int(nod/60e9%60), int(nod/1e9%60), int(nod%1e9)
	zone := source
	if r.p.zone != nil {
		zone = r.p.zone
	}
	if off, ok := r.p.fields[cwdOffsetSeconds]; ok {
		instant := time.Date(d.Year(), d.Month(), d.Day(), h, mi, se, ns, time.UTC).Add(-time.Duration(off) * time.Second)
		if r.p.zone == nil {
			zone = cwdOffsetZone(int(off))
		}
		return instant.In(zone.loc), nil
	}
	return cwdOfLocal(d.Year(), d.Month(), d.Day(), h, mi, se, ns, zone.loc), nil
}

// cwdOfLocal is ZonedDateTime.ofLocal: in a gap the local time moves later
// by the gap's length; in an overlap it keeps the earlier offset.
func cwdOfLocal(y int, mo time.Month, d, h, mi, s, ns int, loc *time.Location) time.Time {
	local := time.Date(y, mo, d, h, mi, s, ns, time.UTC)
	var valid []int
	seen := map[int]bool{}
	for _, probe := range []time.Duration{-36 * time.Hour, -12 * time.Hour, 0, 12 * time.Hour, 36 * time.Hour} {
		_, off := local.Add(probe).In(loc).Zone()
		if seen[off] {
			continue
		}
		seen[off] = true
		if _, at := local.Add(-time.Duration(off) * time.Second).In(loc).Zone(); at == off {
			valid = append(valid, off)
		}
	}
	switch len(valid) {
	case 0:
		_, before := local.Add(-36 * time.Hour).In(loc).Zone()
		return local.Add(-time.Duration(before) * time.Second).In(loc)
	case 1:
		return local.Add(-time.Duration(valid[0]) * time.Second).In(loc)
	}
	sort.Ints(valid)
	return local.Add(-time.Duration(valid[len(valid)-1]) * time.Second).In(loc)
}

// cwdResolver is Parsed.resolve with ResolverStyle.SMART.
type cwdResolver struct {
	p          *cwdParsed
	weeks      cwdWeeks
	date       *time.Time
	time       *int64
	excessDays int
}

func (r *cwdResolver) has(f cwdField) bool {
	_, ok := r.p.fields[f]
	return ok
}

func (r *cwdResolver) take(f cwdField) int64 {
	v := r.p.fields[f]
	delete(r.p.fields, f)
	return v
}

func (r *cwdResolver) put(target, change cwdField, v int64) error {
	old, had := r.p.fields[change]
	r.p.fields[change] = v
	if had && old != v {
		return cwdErr("Conflict found: %s %d differs from %s %d while resolving  %s",
			cwdFieldInfo[change].name, old, cwdFieldInfo[change].name, v, cwdFieldInfo[target].name)
	}
	return nil
}

func (r *cwdResolver) addField(f cwdField, v int64) error {
	if old, had := r.p.fields[f]; had && old != v {
		return cwdErr("Conflict found: %s %d differs from %s %d", cwdFieldInfo[f].name, old, cwdFieldInfo[f].name, v)
	}
	r.p.fields[f] = v
	return nil
}

func cwdDateString(t time.Time) string {
	y := t.Year()
	if y > 9999 {
		return fmt.Sprintf("+%d-%02d-%02d", y, t.Month(), t.Day())
	}
	return fmt.Sprintf("%04d-%02d-%02d", y, t.Month(), t.Day())
}

func (r *cwdResolver) setDate(d *time.Time) error {
	if d == nil {
		return nil
	}
	if r.date != nil {
		if !r.date.Equal(*d) {
			return cwdErr("Conflict found: Fields resolved to two different dates: %s %s", cwdDateString(*r.date), cwdDateString(*d))
		}
		return nil
	}
	r.date = d
	return nil
}

func cwdTimeString(nod int64) string {
	h, m, s, ns := nod/3600e9, nod/60e9%60, nod/1e9%60, nod%1e9
	out := fmt.Sprintf("%02d:%02d", h, m)
	if s > 0 || ns > 0 {
		out += fmt.Sprintf(":%02d", s)
		switch {
		case ns == 0:
		case ns%1000000 == 0:
			out += fmt.Sprintf(".%03d", ns/1000000)
		case ns%1000 == 0:
			out += fmt.Sprintf(".%06d", ns/1000)
		default:
			out += fmt.Sprintf(".%09d", ns)
		}
	}
	return out
}

func (r *cwdResolver) setTime(nod int64, excess int) error {
	if r.time != nil {
		if *r.time != nod {
			return cwdErr("Conflict found: Fields resolved to different times: %s %s", cwdTimeString(*r.time), cwdTimeString(nod))
		}
		if r.excessDays != 0 && excess != 0 && r.excessDays != excess {
			return cwdErr("Conflict found: Fields resolved to different excess periods: P%dD P%dD", r.excessDays, excess)
		}
		r.excessDays = excess
		return nil
	}
	r.time, r.excessDays = &nod, excess
	return nil
}

func (r *cwdResolver) resolve() error {
	if err := r.resolveFields(); err != nil {
		return err
	}
	if err := r.resolveTimeLenient(); err != nil {
		return err
	}
	if err := r.crossCheck(); err != nil {
		return err
	}
	if r.date != nil && r.time != nil && r.excessDays != 0 {
		d := r.date.AddDate(0, 0, r.excessDays)
		r.date, r.excessDays = &d, 0
	}
	return nil
}

func (r *cwdResolver) resolveFields() error {
	if err := r.resolveDateFields(); err != nil {
		return err
	}
	if err := r.resolveTimeFields(); err != nil {
		return err
	}
	changed := 0
	for changed < 50 {
		progressed := false
		for _, f := range []cwdField{cwdLocalDayOfWeek, cwdWeekOfMonth, cwdWeekOfWBY, cwdWeekBasedYear, cwdModifiedJulianDay} {
			if !r.has(f) {
				continue
			}
			d, removed, err := r.resolveOther(f)
			if err != nil {
				return err
			}
			if d != nil {
				if err := r.setDate(d); err != nil {
					return err
				}
				progressed = true
				break
			}
			if removed {
				progressed = true
				break
			}
		}
		if !progressed {
			break
		}
		changed++
	}
	if changed > 0 {
		if err := r.resolveDateFields(); err != nil {
			return err
		}
		return r.resolveTimeFields()
	}
	return nil
}

// resolveOther is TemporalField.resolve for the fields that are not
// ChronoFields: the localized week fields and the Modified Julian Day.
func (r *cwdResolver) resolveOther(f cwdField) (*time.Time, bool, error) {
	value := r.p.fields[f]
	if f == cwdModifiedJulianDay {
		delete(r.p.fields, f)
		if err := cwdCheck(f, value); err != nil {
			return nil, true, err
		}
		d := cwdDateOfEpochDay(value - 40587)
		return &d, true, nil
	}
	if f == cwdLocalDayOfWeek {
		if err := cwdCheck(f, value); err != nil {
			return nil, false, err
		}
		iso := cwdFloorMod(int64(r.weeks.firstDay-1)+value-1, 7) + 1
		delete(r.p.fields, f)
		r.p.fields[cwdDayOfWeek] = iso
		return nil, true, nil
	}
	if !r.has(cwdDayOfWeek) {
		return nil, false, nil
	}
	isoDow := r.p.fields[cwdDayOfWeek]
	if err := cwdCheck(cwdDayOfWeek, isoDow); err != nil {
		return nil, false, err
	}
	dow := r.weeks.localDow(int(isoDow))
	if r.has(cwdYear) {
		year := r.p.fields[cwdYear]
		if err := cwdCheck(cwdYear, year); err != nil {
			return nil, false, err
		}
		if f == cwdWeekOfMonth && r.has(cwdMonth) {
			month := r.p.fields[cwdMonth]
			if err := cwdCheck(cwdMonth, month); err != nil {
				return nil, false, err
			}
			if err := cwdCheck(f, value); err != nil {
				return nil, false, err
			}
			date := time.Date(int(year), time.Month(month), 1, 0, 0, 0, 0, time.UTC)
			weeks := int(value) - r.weeks.weekOfMonth(date)
			days := dow - r.weeks.localDow(cwdISODow(date))
			date = date.AddDate(0, 0, weeks*7+days)
			for _, k := range []cwdField{f, cwdYear, cwdMonth, cwdDayOfWeek} {
				delete(r.p.fields, k)
			}
			return &date, true, nil
		}
	} else if (f == cwdWeekOfWBY || f == cwdWeekBasedYear) && r.has(cwdWeekBasedYear) && r.has(cwdWeekOfWBY) {
		yowby := r.p.fields[cwdWeekBasedYear]
		if err := cwdCheck(cwdWeekBasedYear, yowby); err != nil {
			return nil, false, err
		}
		wowby := r.p.fields[cwdWeekOfWBY]
		if err := cwdCheck(cwdWeekOfWBY, wowby); err != nil {
			return nil, false, err
		}
		date := r.weeks.ofWeekBasedYear(int(yowby), int(wowby), dow)
		for _, k := range []cwdField{f, cwdWeekBasedYear, cwdWeekOfWBY, cwdDayOfWeek} {
			delete(r.p.fields, k)
		}
		return &date, true, nil
	}
	return nil, false, nil
}

func (r *cwdResolver) resolveDateFields() error {
	if yoe, ok := r.p.fields[cwdYearOfEra]; ok {
		delete(r.p.fields, cwdYearOfEra)
		if err := cwdCheck(cwdYearOfEra, yoe); err != nil {
			return err
		}
		if era, ok := r.p.fields[cwdEra]; ok {
			delete(r.p.fields, cwdEra)
			switch era {
			case 1:
				if err := r.addField(cwdYear, yoe); err != nil {
					return err
				}
			case 0:
				if err := r.addField(cwdYear, 1-yoe); err != nil {
					return err
				}
			default:
				return cwdErr("Invalid value for era: %d", era)
			}
		} else {
			y, hasYear := r.p.fields[cwdYear]
			v := yoe
			if hasYear && y <= 0 {
				v = 1 - yoe
			}
			if err := r.addField(cwdYear, v); err != nil {
				return err
			}
		}
	} else if era, ok := r.p.fields[cwdEra]; ok {
		if err := cwdCheck(cwdEra, era); err != nil {
			return err
		}
	}
	if !r.has(cwdYear) {
		return nil
	}
	year := func() (int, error) {
		y := r.take(cwdYear)
		return int(y), cwdCheck(cwdYear, y)
	}
	if r.has(cwdMonth) && r.has(cwdDayOfMonth) {
		y, err := year()
		if err != nil {
			return err
		}
		moy, dom := r.take(cwdMonth), r.take(cwdDayOfMonth)
		if err := cwdCheck(cwdMonth, moy); err != nil {
			return err
		}
		if err := cwdCheck(cwdDayOfMonth, dom); err != nil {
			return err
		}
		switch moy {
		case 4, 6, 9, 11:
			dom = min(dom, 30)
		case 2:
			dom = min(dom, int64(cwdYearLen(y)-337))
		}
		d := time.Date(y, time.Month(moy), int(dom), 0, 0, 0, 0, time.UTC)
		return r.setDate(&d)
	}
	if r.has(cwdDayOfYear) {
		y, err := year()
		if err != nil {
			return err
		}
		doy := r.take(cwdDayOfYear)
		if err := cwdCheck(cwdDayOfYear, doy); err != nil {
			return err
		}
		if doy == 366 && cwdYearLen(y) == 365 {
			return cwdErr("Invalid date 'DayOfYear 366' as '%d' is not a leap year", y)
		}
		d := time.Date(y, 1, int(doy), 0, 0, 0, 0, time.UTC)
		return r.setDate(&d)
	}
	return nil
}

func (r *cwdResolver) resolveTimeFields() error {
	if r.has(cwdClockHourOfDay) {
		ch := r.take(cwdClockHourOfDay)
		if ch != 0 {
			if err := cwdCheck(cwdClockHourOfDay, ch); err != nil {
				return err
			}
		}
		if ch == 24 {
			ch = 0
		}
		if err := r.put(cwdClockHourOfDay, cwdHourOfDay, ch); err != nil {
			return err
		}
	}
	if r.has(cwdClockHourOfAmPm) {
		ch := r.take(cwdClockHourOfAmPm)
		if ch != 0 {
			if err := cwdCheck(cwdClockHourOfAmPm, ch); err != nil {
				return err
			}
		}
		if ch == 12 {
			ch = 0
		}
		if err := r.put(cwdClockHourOfAmPm, cwdHourOfAmPm, ch); err != nil {
			return err
		}
	}
	if r.has(cwdAmPm) && r.has(cwdHourOfAmPm) {
		ap, hap := r.take(cwdAmPm), r.take(cwdHourOfAmPm)
		if err := cwdCheck(cwdAmPm, ap); err != nil {
			return err
		}
		if err := cwdCheck(cwdHourOfAmPm, hap); err != nil {
			return err
		}
		if err := r.put(cwdAmPm, cwdHourOfDay, ap*12+hap); err != nil {
			return err
		}
	}
	if r.has(cwdNanoOfDay) {
		nod := r.take(cwdNanoOfDay)
		if err := cwdCheck(cwdNanoOfDay, nod); err != nil {
			return err
		}
		for _, kv := range []struct {
			f cwdField
			v int64
		}{{cwdHourOfDay, nod / 3600e9}, {cwdMinute, nod / 60e9 % 60}, {cwdSecond, nod / 1e9 % 60}, {cwdNano, nod % 1e9}} {
			if err := r.put(cwdNanoOfDay, kv.f, kv.v); err != nil {
				return err
			}
		}
	}
	if r.has(cwdMilliOfDay) {
		lod := r.take(cwdMilliOfDay)
		if err := cwdCheck(cwdMilliOfDay, lod); err != nil {
			return err
		}
		sod := lod / 1000
		if nos, ok := r.p.fields[cwdNano]; ok {
			if err := r.put(cwdMilliOfDay, cwdNano, lod%1000*1000000+nos%1000000); err != nil {
				return err
			}
		} else {
			r.p.fields[cwdNano] = lod % 1000 * 1000000
		}
		for _, kv := range []struct {
			f cwdField
			v int64
		}{{cwdHourOfDay, sod / 3600}, {cwdMinute, sod / 60 % 60}, {cwdSecond, sod % 60}} {
			if err := r.put(cwdMilliOfDay, kv.f, kv.v); err != nil {
				return err
			}
		}
	}
	if nos, ok := r.p.fields[cwdNano]; ok {
		if err := cwdCheck(cwdNano, nos); err != nil {
			return err
		}
	}
	if r.p.dayPeriod != nil && r.has(cwdHourOfAmPm) {
		hoap := r.take(cwdHourOfAmPm)
		if err := cwdCheck(cwdHourOfAmPm, hoap); err != nil {
			return err
		}
		moh := int64(0)
		if m, ok := r.p.fields[cwdMinute]; ok {
			moh = cwdFloorMod(m, 60)
		}
		excess := int64(0)
		if r.p.dayPeriod.includes(int((cwdFloorMod(hoap, 12)+12)*60 + moh)) {
			excess = 12
		}
		if err := r.put(cwdHourOfAmPm, cwdHourOfDay, hoap+excess); err != nil {
			return err
		}
		r.p.dayPeriod = nil
	}
	if r.has(cwdHourOfDay) && r.has(cwdMinute) && r.has(cwdSecond) && r.has(cwdNano) {
		return r.resolveTime(r.take(cwdHourOfDay), r.take(cwdMinute), r.take(cwdSecond), r.take(cwdNano))
	}
	return nil
}

func (r *cwdResolver) resolveTime(hod, moh, som, nos int64) error {
	if err := cwdCheck(cwdMinute, moh); err != nil {
		return err
	}
	if err := cwdCheck(cwdNano, nos); err != nil {
		return err
	}
	if hod == 24 && moh == 0 && som == 0 && nos == 0 {
		return r.setTime(0, 1)
	}
	if err := cwdCheck(cwdHourOfDay, hod); err != nil {
		return err
	}
	if err := cwdCheck(cwdSecond, som); err != nil {
		return err
	}
	return r.setTime(((hod*60+moh)*60+som)*1e9+nos, 0)
}

func (r *cwdResolver) resolveTimeLenient() error {
	if r.time == nil {
		if !r.has(cwdHourOfDay) && !r.has(cwdMinute) && !r.has(cwdSecond) && !r.has(cwdNano) {
			if r.p.dayPeriod != nil {
				mid := int64(r.p.dayPeriod.mid())
				if err := r.resolveTime(mid/60, mid%60, 0, 0); err != nil {
					return err
				}
				r.p.dayPeriod = nil
			} else if r.has(cwdAmPm) {
				ap := r.take(cwdAmPm)
				if err := cwdCheck(cwdAmPm, ap); err != nil {
					return err
				}
				if err := r.resolveTime(ap*12+6, 0, 0, 0); err != nil {
					return err
				}
			}
		}
		if hod, ok := r.p.fields[cwdHourOfDay]; ok {
			moh, hasM := r.p.fields[cwdMinute]
			som, hasS := r.p.fields[cwdSecond]
			nos, hasN := r.p.fields[cwdNano]
			if !hasM && (hasS || hasN) || hasM && !hasS && hasN {
				return r.checkTimeFields()
			}
			if r.p.dayPeriod != nil && !r.p.dayPeriod.includes(int(hod*60+moh)) {
				return cwdErr("Conflict found: Resolved time %02d:%02d conflicts with %s", hod, moh, r.p.dayPeriod)
			}
			if err := r.resolveTime(hod, moh, som, nos); err != nil {
				return err
			}
			for _, f := range []cwdField{cwdHourOfDay, cwdMinute, cwdSecond, cwdNano} {
				delete(r.p.fields, f)
			}
		}
	}
	return r.checkTimeFields()
}

func (p *cwdDayPeriod) String() string {
	s := fmt.Sprintf("DayPeriod(%02d:%02d", p.from/60, p.from%60)
	if p.from == p.to {
		return s + ")"
	}
	return s + fmt.Sprintf("-%02d:%02d)", p.to/60, p.to%60)
}

func (r *cwdResolver) checkTimeFields() error {
	keys := make([]cwdField, 0, len(r.p.fields))
	for f := range r.p.fields {
		keys = append(keys, f)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, f := range keys {
		if cwdFieldInfo[f].time {
			if err := cwdCheck(f, r.p.fields[f]); err != nil {
				return err
			}
		}
	}
	return nil
}

// crossCheck compares the fields left over with the resolved date and time.
func (r *cwdResolver) crossCheck() error {
	keys := make([]cwdField, 0, len(r.p.fields))
	for f := range r.p.fields {
		keys = append(keys, f)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	check := func(target string, value func(cwdField) (int64, bool)) error {
		for _, f := range keys {
			v2, present := r.p.fields[f]
			if !present {
				continue
			}
			v1, supported := value(f)
			if !supported {
				continue
			}
			if v1 != v2 {
				return cwdErr("Conflict found: Field %s %d differs from %s %d derived from %s", cwdFieldInfo[f].name, v1, cwdFieldInfo[f].name, v2, target)
			}
			delete(r.p.fields, f)
		}
		return nil
	}
	var dateValue, timeValue func(cwdField) (int64, bool)
	if r.date != nil {
		pc := &cwdPrint{t: *r.date, loc: &cwdLocale{}}
		dateValue = func(f cwdField) (int64, bool) {
			switch f {
			case cwdEra, cwdYearOfEra, cwdYear, cwdQuarter, cwdMonth, cwdDayOfYear, cwdDayOfMonth, cwdAlignedDOWInMonth, cwdDayOfWeek, cwdModifiedJulianDay:
				return pc.value(f), true
			case cwdLocalDayOfWeek:
				return int64(r.weeks.localDow(cwdISODow(*r.date))), true
			case cwdWeekOfMonth:
				return int64(r.weeks.weekOfMonth(*r.date)), true
			case cwdWeekOfWBY:
				return int64(r.weeks.weekOfWeekBasedYear(*r.date)), true
			case cwdWeekBasedYear:
				return int64(r.weeks.weekBasedYear(*r.date)), true
			}
			return 0, false
		}
		if err := check(cwdDateString(*r.date), dateValue); err != nil {
			return err
		}
	}
	if r.time != nil {
		nod := *r.time
		t := time.Date(2000, 1, 1, int(nod/3600e9), int(nod/60e9%60), int(nod/1e9%60), int(nod%1e9), time.UTC)
		pc := &cwdPrint{t: t, loc: &cwdLocale{}}
		timeValue = func(f cwdField) (int64, bool) {
			if cwdFieldInfo[f].time {
				return pc.value(f), true
			}
			return 0, false
		}
		if err := check(cwdTimeString(nod), timeValue); err != nil {
			return err
		}
		if r.date != nil && len(r.p.fields) > 0 {
			both := func(f cwdField) (int64, bool) {
				if v, ok := dateValue(f); ok {
					return v, true
				}
				return timeValue(f)
			}
			if err := check(cwdDateString(*r.date)+"T"+cwdTimeString(nod), both); err != nil {
				return err
			}
		}
	}
	return nil
}

type cwtDateTimeStep struct {
	source, target string
	patterns       []*cwdFormatter
	targetFormat   *cwdFormatter
	sourceZone     *cwdZone
	targetZone     *cwdZone
	locale         *cwdLocale
}

func cwtCompileDateTime(c *cwtDateTimeConverter) (cwtStep, error) {
	if err := cwtCheckKey("dateTimeConverter.source", c.Source, 128); err != nil {
		return nil, err
	}
	if err := cwtCheckKey("dateTimeConverter.target", c.Target, 128); err != nil {
		return nil, err
	}
	if err := cwtCheckEntries("dateTimeConverter.matchPatterns", len(c.MatchPatterns), 5); err != nil {
		return nil, err
	}
	step := &cwtDateTimeStep{source: c.Source, target: c.Target}
	for _, p := range c.MatchPatterns {
		if p == "" {
			return nil, cwtInvalid("dateTimeConverter.matchPatterns entries must not be empty")
		}
		pp, err := cwdCompilePattern(p)
		if err != nil {
			return nil, cwtInvalid("dateTimeConverter.matchPatterns: %v", err)
		}
		step.patterns = append(step.patterns, &cwdFormatter{pp: pp})
	}
	format := cwtDefaultTargetFormat
	if c.TargetFormat != nil {
		if err := cwtCheckLen("dateTimeConverter.targetFormat", *c.TargetFormat, 1, 64); err != nil {
			return nil, err
		}
		format = *c.TargetFormat
	}
	pp, err := cwdCompilePattern(format)
	if err != nil {
		return nil, cwtInvalid("dateTimeConverter.targetFormat: %v", err)
	}
	step.targetFormat = &cwdFormatter{pp: pp}
	for _, z := range []struct {
		field string
		id    *string
		dst   **cwdZone
	}{
		{"sourceTimezone", c.SourceTimezone, &step.sourceZone},
		{"targetTimezone", c.TargetTimezone, &step.targetZone},
	} {
		id := "UTC"
		if z.id != nil {
			if err := cwtCheckLen("dateTimeConverter."+z.field, *z.id, 1, 0); err != nil {
				return nil, err
			}
			id = *z.id
		}
		zone, err := cwdZoneOf(id)
		if err != nil {
			return nil, cwtInvalid("dateTimeConverter.%s: %v", z.field, err)
		}
		*z.dst = zone
	}
	tag := ""
	if c.Locale != nil {
		if err := cwtCheckLen("dateTimeConverter.locale", *c.Locale, 1, 0); err != nil {
			return nil, err
		}
		tag = *c.Locale
	}
	loc, err := cwdLocaleOf(tag)
	if err != nil {
		return nil, cwtInvalid("dateTimeConverter.locale: %v", err)
	}
	step.locale = loc
	return step, nil
}

func (s *cwtDateTimeStep) apply(ev *cwtEvent) error {
	v, ok := ev.root.GetPath(s.source)
	if !ok {
		return nil
	}
	raw, isStr := v.(string)
	if !isStr {
		return fmt.Errorf("dateTimeConverter: %s is not a string", s.source)
	}
	var lastErr error
	for _, p := range s.patterns {
		t, err := p.parse(raw, s.locale, s.sourceZone)
		if err != nil {
			lastErr = err
			continue
		}
		out, err := s.targetFormat.format(t.In(s.targetZone.loc), s.targetZone, s.locale)
		if err != nil {
			return fmt.Errorf("dateTimeConverter: %v", err)
		}
		ev.root.SetPath(s.target, out, true)
		return nil
	}
	return fmt.Errorf("dateTimeConverter: %q matches none of the match patterns: %v", raw, lastErr)
}
