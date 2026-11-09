package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

// dateTimeConverter takes java.time.format.DateTimeFormatter patterns, time
// zone IDs and locales.

const cwtDefaultTargetFormat = "yyyy-MM-dd'T'HH:mm:ss.SSS'Z'"

type cwtDTTok struct {
	letter byte
	count  int
	lit    string
}

// Pattern letters DateTimeFormatter defines that the converter does not
// implement.
const cwtDTUnsupportedLetters = "QqYwWecFANvBOg"

func cwtParseDTPattern(p string) ([]cwtDTTok, error) {
	var toks []cwtDTTok
	for i := 0; i < len(p); {
		c := p[i]
		switch {
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z'):
			j := i
			for j < len(p) && p[j] == c {
				j++
			}
			toks = append(toks, cwtDTTok{letter: c, count: j - i})
			i = j
		case c == '\'':
			var lit strings.Builder
			j := i + 1
			closed := false
			for j < len(p) {
				if p[j] == '\'' {
					if j+1 < len(p) && p[j+1] == '\'' {
						lit.WriteByte('\'')
						j += 2
						continue
					}
					closed = true
					j++
					break
				}
				lit.WriteByte(p[j])
				j++
			}
			if !closed {
				return nil, fmt.Errorf("pattern ends with an incomplete string literal: %s", p)
			}
			if lit.Len() == 0 {
				lit.WriteByte('\'')
			}
			toks = append(toks, cwtDTTok{lit: lit.String()})
			i = j
		case c == '[' || c == ']':
			return nil, fmt.Errorf("optional sections are not supported: %s", p)
		case c == '{' || c == '}' || c == '#':
			return nil, fmt.Errorf("pattern includes reserved character %q: %s", c, p)
		default:
			toks = append(toks, cwtDTTok{lit: string(c)})
			i++
		}
	}
	for _, t := range toks {
		if t.letter == 0 {
			continue
		}
		if err := cwtCheckDTLetter(t); err != nil {
			return nil, err
		}
	}
	return toks, nil
}

func cwtCheckDTLetter(t cwtDTTok) error {
	maxCount := map[byte]int{
		'G': 3, 'u': 19, 'y': 19, 'M': 4, 'L': 4, 'd': 2, 'D': 3, 'E': 4, 'a': 1,
		'h': 2, 'K': 2, 'k': 2, 'H': 2, 'm': 2, 's': 2, 'S': 9, 'n': 9,
		'V': 2, 'z': 3, 'X': 5, 'x': 5, 'Z': 5,
	}
	if strings.IndexByte(cwtDTUnsupportedLetters, t.letter) >= 0 {
		return fmt.Errorf("pattern letter %q is not supported", t.letter)
	}
	limit, ok := maxCount[t.letter]
	if !ok {
		return fmt.Errorf("unknown pattern letter: %c", t.letter)
	}
	if t.count > limit || (t.letter == 'V' && t.count != 2) || (t.letter == 'Z' && t.count == 4) {
		return fmt.Errorf("too many pattern letters: %s", strings.Repeat(string(t.letter), t.count))
	}
	return nil
}

func cwtDTUsesText(toks []cwtDTTok) bool {
	for _, t := range toks {
		switch {
		case t.letter == 'G', t.letter == 'E', t.letter == 'a', t.letter == 'z':
			return true
		case (t.letter == 'M' || t.letter == 'L') && t.count >= 3:
			return true
		}
	}
	return false
}

type cwtDTLocale struct {
	months, shortMonths, days, shortDays []string
	ampm, eras                           [2]string
}

var cwtDTEnglish = &cwtDTLocale{
	months:      []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
	shortMonths: []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
	days:        []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
	shortDays:   []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	ampm:        [2]string{"AM", "PM"},
	eras:        [2]string{"BC", "AD"},
}

var cwtDTGerman = &cwtDTLocale{
	months:      []string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"},
	shortMonths: []string{"Jan.", "Feb.", "März", "Apr.", "Mai", "Juni", "Juli", "Aug.", "Sept.", "Okt.", "Nov.", "Dez."},
	days:        []string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"},
	shortDays:   []string{"So.", "Mo.", "Di.", "Mi.", "Do.", "Fr.", "Sa."},
	ampm:        [2]string{"AM", "PM"},
	eras:        [2]string{"v. Chr.", "n. Chr."},
}

var cwtLocaleRE = regexp.MustCompile(`^[A-Za-z]{2,8}(?:[_-][A-Za-z0-9]{1,8})*$`)

// cwtResolveLocale returns the text names for a locale tag, or nil when the
// converter has no names for its language. An omitted locale is Locale.ROOT.
func cwtResolveLocale(tag string) (*cwtDTLocale, bool, error) {
	if tag == "" || strings.EqualFold(tag, "root") {
		return cwtDTEnglish, true, nil
	}
	if !cwtLocaleRE.MatchString(tag) {
		return nil, false, fmt.Errorf("%q is not a locale", tag)
	}
	lang := strings.ToLower(strings.FieldsFunc(tag, func(r rune) bool { return r == '_' || r == '-' })[0])
	switch lang {
	case "en":
		return cwtDTEnglish, true, nil
	case "de":
		return cwtDTGerman, false, nil
	}
	return nil, false, nil
}

var cwtOffsetIDRE = regexp.MustCompile(`^([+-])(\d{1,2})(?::?(\d{2})(?::?(\d{2}))?)?$`)

// cwtResolveZone follows ZoneId.of: Z, an offset, UTC/GMT/UT with or without
// an offset, or a region ID.
func cwtResolveZone(id string) (*time.Location, error) {
	switch id {
	case "Z":
		return time.FixedZone("Z", 0), nil
	case "UTC", "GMT", "UT":
		return time.FixedZone(id, 0), nil
	}
	if id[0] == '+' || id[0] == '-' {
		secs, norm, err := cwtParseOffsetID(id)
		if err != nil {
			return nil, err
		}
		return time.FixedZone(norm, secs), nil
	}
	for _, prefix := range []string{"UTC", "GMT", "UT"} {
		if rest, ok := strings.CutPrefix(id, prefix); ok && rest != "" && (rest[0] == '+' || rest[0] == '-') {
			secs, norm, err := cwtParseOffsetID(rest)
			if err != nil {
				return nil, err
			}
			if secs == 0 {
				norm = ""
			}
			return time.FixedZone(prefix+norm, secs), nil
		}
	}
	if id == "Local" || strings.Contains(id, "..") {
		return nil, fmt.Errorf("unknown time-zone ID: %s", id)
	}
	loc, err := time.LoadLocation(id)
	if err != nil {
		return nil, fmt.Errorf("unknown time-zone ID: %s", id)
	}
	return loc, nil
}

func cwtParseOffsetID(s string) (int, string, error) {
	m := cwtOffsetIDRE.FindStringSubmatch(s)
	if m == nil {
		return 0, "", fmt.Errorf("invalid ID for ZoneOffset: %s", s)
	}
	h, _ := strconv.Atoi(m[2])
	mm, ss := 0, 0
	if m[3] != "" {
		mm, _ = strconv.Atoi(m[3])
	}
	if m[4] != "" {
		ss, _ = strconv.Atoi(m[4])
	}
	if h > 18 || mm > 59 || ss > 59 || h*3600+mm*60+ss > 18*3600 {
		return 0, "", fmt.Errorf("zone offset out of range: %s", s)
	}
	secs := h*3600 + mm*60 + ss
	if m[1] == "-" {
		secs = -secs
	}
	return secs, cwtOffsetString(secs, true, true, "Z"), nil
}

// cwtOffsetString writes an offset as ±HH[:]MM[[:]SS]; zero writes zeroText.
func cwtOffsetString(secs int, colon, withSeconds bool, zeroText string) string {
	if secs == 0 && zeroText != "" {
		return zeroText
	}
	sign := "+"
	if secs < 0 {
		sign = "-"
		secs = -secs
	}
	sep := ""
	if colon {
		sep = ":"
	}
	s := fmt.Sprintf("%s%02d%s%02d", sign, secs/3600, sep, secs/60%60)
	if withSeconds && secs%60 != 0 {
		s += fmt.Sprintf("%s%02d", sep, secs%60)
	}
	return s
}

type cwtDateTimeStep struct {
	source, target string
	patterns       [][]cwtDTTok
	patternREs     []*regexp.Regexp
	targetFormat   []cwtDTTok
	sourceZone     *time.Location
	targetZone     *time.Location
	names          *cwtDTLocale
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
	usesText := false
	for _, p := range c.MatchPatterns {
		if p == "" {
			return nil, cwtInvalid("dateTimeConverter.matchPatterns entries must not be empty")
		}
		toks, err := cwtParseDTPattern(p)
		if err != nil {
			return nil, cwtInvalid("dateTimeConverter.matchPatterns: %v", err)
		}
		for _, t := range toks {
			if t.letter == 'z' {
				return nil, cwtInvalid("dateTimeConverter.matchPatterns: parsing time-zone names (pattern letter z) is not supported; use VV or an offset")
			}
		}
		usesText = usesText || cwtDTUsesText(toks)
		step.patterns = append(step.patterns, toks)
	}
	format := cwtDefaultTargetFormat
	if c.TargetFormat != nil {
		if err := cwtCheckLen("dateTimeConverter.targetFormat", *c.TargetFormat, 1, 64); err != nil {
			return nil, err
		}
		format = *c.TargetFormat
	}
	toks, err := cwtParseDTPattern(format)
	if err != nil {
		return nil, cwtInvalid("dateTimeConverter.targetFormat: %v", err)
	}
	step.targetFormat = toks
	usesText = usesText || cwtDTUsesText(toks)
	for _, z := range []struct {
		field string
		id    *string
		dst   **time.Location
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
		loc, err := cwtResolveZone(id)
		if err != nil {
			return nil, cwtInvalid("dateTimeConverter.%s: %v", z.field, err)
		}
		*z.dst = loc
	}
	tag := ""
	if c.Locale != nil {
		if err := cwtCheckLen("dateTimeConverter.locale", *c.Locale, 1, 0); err != nil {
			return nil, err
		}
		tag = *c.Locale
	}
	names, english, err := cwtResolveLocale(tag)
	if err != nil {
		return nil, cwtInvalid("dateTimeConverter.locale: %v", err)
	}
	if usesText && names == nil {
		return nil, cwtInvalid("dateTimeConverter.locale %q: month, day, era and AM/PM names are available only for English and German locales", tag)
	}
	if !english {
		for _, t := range toks {
			if t.letter == 'z' {
				return nil, cwtInvalid("dateTimeConverter.targetFormat: time-zone names (pattern letter z) are available only for English locales")
			}
		}
	}
	step.names = names
	for _, p := range step.patterns {
		step.patternREs = append(step.patternREs, cwtDTRegex(p, names))
	}
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
	for i, p := range s.patterns {
		t, err := cwtDTParse(p, s.patternREs[i], raw, s.names, s.sourceZone)
		if err != nil {
			lastErr = err
			continue
		}
		ev.root.SetPath(s.target, cwtDTFormat(s.targetFormat, t.In(s.targetZone), s.names), true)
		return nil
	}
	return fmt.Errorf("dateTimeConverter: %q matches none of the match patterns: %v", raw, lastErr)
}

func cwtPad(n, width int) string {
	s := strconv.Itoa(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

func cwtDTFormat(toks []cwtDTTok, t time.Time, names *cwtDTLocale) string {
	var b strings.Builder
	for _, tok := range toks {
		if tok.letter == 0 {
			b.WriteString(tok.lit)
			continue
		}
		n := tok.count
		switch tok.letter {
		case 'G':
			if t.Year() > 0 {
				b.WriteString(names.eras[1])
			} else {
				b.WriteString(names.eras[0])
			}
		case 'y', 'u':
			year := t.Year()
			if tok.letter == 'y' && year <= 0 {
				year = 1 - year
			}
			if n == 2 {
				b.WriteString(cwtPad(((year%100)+100)%100, 2))
			} else {
				b.WriteString(cwtPad(year, n))
			}
		case 'M', 'L':
			switch n {
			case 3:
				b.WriteString(names.shortMonths[t.Month()-1])
			case 4:
				b.WriteString(names.months[t.Month()-1])
			default:
				b.WriteString(cwtPad(int(t.Month()), n))
			}
		case 'd':
			b.WriteString(cwtPad(t.Day(), n))
		case 'D':
			b.WriteString(cwtPad(t.YearDay(), n))
		case 'E':
			if n == 4 {
				b.WriteString(names.days[t.Weekday()])
			} else {
				b.WriteString(names.shortDays[t.Weekday()])
			}
		case 'a':
			b.WriteString(names.ampm[t.Hour()/12])
		case 'H':
			b.WriteString(cwtPad(t.Hour(), n))
		case 'k':
			h := t.Hour()
			if h == 0 {
				h = 24
			}
			b.WriteString(cwtPad(h, n))
		case 'K':
			b.WriteString(cwtPad(t.Hour()%12, n))
		case 'h':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			b.WriteString(cwtPad(h, n))
		case 'm':
			b.WriteString(cwtPad(t.Minute(), n))
		case 's':
			b.WriteString(cwtPad(t.Second(), n))
		case 'S':
			b.WriteString(cwtPad(t.Nanosecond(), 9)[:n])
		case 'n':
			b.WriteString(cwtPad(t.Nanosecond(), n))
		case 'V':
			b.WriteString(t.Location().String())
		case 'z':
			name, _ := t.Zone()
			b.WriteString(name)
		case 'X', 'x', 'Z':
			_, off := t.Zone()
			b.WriteString(cwtDTFormatOffset(tok, off))
		}
	}
	return b.String()
}

func cwtDTFormatOffset(tok cwtDTTok, off int) string {
	zero := ""
	if tok.letter == 'X' || (tok.letter == 'Z' && tok.count == 5) {
		zero = "Z"
	}
	if tok.letter == 'Z' {
		if tok.count == 5 {
			return cwtOffsetString(off, true, true, zero)
		}
		return cwtOffsetString(off, false, false, "")
	}
	if off == 0 && zero != "" {
		return zero
	}
	switch tok.count {
	case 1:
		s := cwtOffsetString(off, false, false, "")
		if off%3600 == 0 {
			return s[:3]
		}
		return s
	case 2:
		return cwtOffsetString(off, false, false, "")
	case 3:
		return cwtOffsetString(off, true, false, "")
	case 4:
		return cwtOffsetString(off, false, true, "")
	default:
		return cwtOffsetString(off, true, true, "")
	}
}

func cwtAlternation(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = regexp.QuoteMeta(w)
	}
	return "(" + strings.Join(q, "|") + ")"
}

func cwtIndexOf(words []string, w string) int {
	for i, x := range words {
		if x == w {
			return i
		}
	}
	return -1
}

// cwtDTParse parses text with a pattern the way DateTimeFormatter's SMART
// resolver does: case-sensitive names, fixed widths for doubled letters, a
// day-of-month past the month's end clamped to its last day, and a missing
// time of day at midnight.
func cwtDTRegex(toks []cwtDTTok, names *cwtDTLocale) *regexp.Regexp {
	var re strings.Builder
	re.WriteString("^")
	for _, tok := range toks {
		if tok.letter == 0 {
			re.WriteString(regexp.QuoteMeta(tok.lit))
			continue
		}
		n := tok.count
		var g string
		switch tok.letter {
		case 'G':
			g = cwtAlternation(names.eras[:])
		case 'y', 'u':
			switch n {
			case 2:
				g = `(\d{2})`
			case 1:
				g = `([+-]?\d{1,9})`
			default:
				g = `([+-]?\d{` + strconv.Itoa(n) + `,9})`
			}
		case 'M', 'L':
			switch n {
			case 3:
				g = cwtAlternation(names.shortMonths)
			case 4:
				g = cwtAlternation(names.months)
			case 2:
				g = `(\d{2})`
			default:
				g = `(\d{1,2})`
			}
		case 'd', 'H', 'h', 'K', 'k', 'm', 's':
			if n == 2 {
				g = `(\d{2})`
			} else {
				g = `(\d{1,2})`
			}
		case 'D':
			g = `(\d{` + strconv.Itoa(n) + `,3})`
		case 'E':
			if n == 4 {
				g = cwtAlternation(names.days)
			} else {
				g = cwtAlternation(names.shortDays)
			}
		case 'a':
			g = cwtAlternation(names.ampm[:])
		case 'S':
			g = `(\d{` + strconv.Itoa(n) + `})`
		case 'n':
			g = `(\d{1,9})`
		case 'V':
			g = `([A-Za-z][A-Za-z0-9~/._+-]+|Z|[+-][0-9:]+)`
		case 'X':
			g = `(Z|[+-]\d{2}(?::?\d{2}(?::?\d{2})?)?)`
		case 'x', 'Z':
			g = `([+-]\d{2}(?::?\d{2}(?::?\d{2})?)?|Z)`
		}
		re.WriteString(g)
	}
	re.WriteString("$")
	return regexp.MustCompile(re.String())
}

func cwtDTParse(toks []cwtDTTok, re *regexp.Regexp, text string, names *cwtDTLocale, zone *time.Location) (time.Time, error) {
	var slots []cwtDTTok
	for _, tok := range toks {
		if tok.letter != 0 {
			slots = append(slots, tok)
		}
	}
	m := re.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, fmt.Errorf("text %q could not be parsed", text)
	}
	year, month, day, yday := 0, 0, 0, 0
	haveYear, haveMonth, haveDay, haveYDay := false, false, false, false
	hour, minute, second, nano := 0, 0, 0, 0
	haveHour, ampmHour, pm, havePM := false, -1, false, false
	weekday := -1
	bce := false
	loc := zone
	for i, tok := range slots {
		v := m[i+1]
		num := func() int { n, _ := strconv.Atoi(strings.TrimPrefix(v, "+")); return n }
		switch tok.letter {
		case 'G':
			bce = v == names.eras[0]
		case 'y', 'u':
			year, haveYear = num(), true
			if tok.count == 2 {
				year += 2000
			}
			if tok.letter == 'u' {
				bce = false
			}
		case 'M', 'L':
			haveMonth = true
			switch tok.count {
			case 3:
				month = cwtIndexOf(names.shortMonths, v) + 1
			case 4:
				month = cwtIndexOf(names.months, v) + 1
			default:
				month = num()
			}
		case 'd':
			day, haveDay = num(), true
		case 'D':
			yday, haveYDay = num(), true
		case 'E':
			if tok.count == 4 {
				weekday = cwtIndexOf(names.days, v)
			} else {
				weekday = cwtIndexOf(names.shortDays, v)
			}
		case 'a':
			pm, havePM = v == names.ampm[1], true
		case 'H':
			hour, haveHour = num(), true
		case 'k':
			hour, haveHour = num()%24, true
			if num() < 1 || num() > 24 {
				return time.Time{}, fmt.Errorf("invalid value for ClockHourOfDay: %s", v)
			}
		case 'K':
			ampmHour = num()
		case 'h':
			if num() < 1 || num() > 12 {
				return time.Time{}, fmt.Errorf("invalid value for ClockHourOfAmPm: %s", v)
			}
			ampmHour = num() % 12
		case 'm':
			minute = num()
		case 's':
			second = num()
		case 'S':
			frac := v + strings.Repeat("0", 9-len(v))
			nano, _ = strconv.Atoi(frac)
		case 'n':
			nano = num()
		case 'V':
			l, err := cwtResolveZone(v)
			if err != nil {
				return time.Time{}, err
			}
			loc = l
		case 'X', 'x', 'Z':
			if v == "Z" {
				loc = time.FixedZone("Z", 0)
				continue
			}
			secs, norm, err := cwtParseOffsetID(v)
			if err != nil {
				return time.Time{}, err
			}
			loc = time.FixedZone(norm, secs)
		}
	}
	if !haveYear {
		return time.Time{}, fmt.Errorf("text %q has no year", text)
	}
	if bce {
		year = 1 - year
	}
	if ampmHour >= 0 {
		if !havePM {
			return time.Time{}, fmt.Errorf("text %q has an hour of AM/PM without AM or PM", text)
		}
		hour = ampmHour
		if pm {
			hour += 12
		}
	} else if havePM && haveHour && (hour < 12) == pm {
		return time.Time{}, fmt.Errorf("conflict found: hour %d does not agree with %s", hour, m[0])
	}
	if hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, fmt.Errorf("text %q has a time out of range", text)
	}
	var t time.Time
	switch {
	case haveMonth && haveDay:
		if month < 1 || month > 12 || day < 1 || day > 31 {
			return time.Time{}, fmt.Errorf("text %q has a date out of range", text)
		}
		last := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
		day = min(day, last)
		t = time.Date(year, time.Month(month), day, hour, minute, second, nano, loc)
	case haveYDay && !haveMonth:
		if yday < 1 || yday > time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC).YearDay() {
			return time.Time{}, fmt.Errorf("text %q has a day of year out of range", text)
		}
		t = time.Date(year, 1, yday, hour, minute, second, nano, loc)
	default:
		return time.Time{}, fmt.Errorf("text %q has no complete date", text)
	}
	if weekday >= 0 && int(t.Weekday()) != weekday {
		return time.Time{}, fmt.Errorf("conflict found: %s is not a %s", t.Format("2006-01-02"), names.days[weekday])
	}
	return t, nil
}
