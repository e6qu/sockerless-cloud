package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Java SE 17 reads its locale data from CLDR 39 (cloudwatch_logs_transformer_cldr_gen.go)
// the way sun.util.cldr.CLDRLocaleProviderAdapter does: month, day, quarter,
// era and day period names; time zone names, with the JDK's COMPAT names
// filling the slots CLDR leaves empty; week definitions by region; and day
// period rules.

func cwdS(ref uint32) string {
	off := ref >> 8
	return cwdPool[off : off+ref&0xff]
}

// The TextStyle order the calendar tables keep: FULL, FULL_STANDALONE,
// SHORT, SHORT_STANDALONE, NARROW, NARROW_STANDALONE.
type cwdStyle int

const (
	cwdFull cwdStyle = iota
	cwdFullStandalone
	cwdShort
	cwdShortStandalone
	cwdNarrow
	cwdNarrowStandalone
)

func (s cwdStyle) standalone() bool { return s%2 == 1 }
func (s cwdStyle) base() cwdStyle   { return s &^ 1 }

const (
	cwdCalMonths   = 0
	cwdCalDays     = cwdCalMonths + 6*12
	cwdCalQuarters = cwdCalDays + 6*7
	cwdCalEras     = cwdCalQuarters + 6*4
	cwdCalPeriods  = cwdCalEras + 3*2
)

type cwdLocale struct {
	tag                  string
	lang, script, region string
	chain                []string
	compatChain          []string
	cal                  [cwdCalLen]string
	formats              [7]string
	firstDay, minDays    int
	dayPeriods           []cwdDayPeriod
}

var cwdLocaleTagRE = regexp.MustCompile(`^[A-Za-z]{2,8}(?:[_-][A-Za-z0-9]{1,8})*$`)

var (
	cwdLocaleCache sync.Map
)

// cwdLocaleOf builds a java.util.Locale from a tag: a language, then an
// optional four-letter script and a two-letter or three-digit region,
// separated by "-" or "_". An empty tag or "root" is Locale.ROOT.
func cwdLocaleOf(tag string) (*cwdLocale, error) {
	if v, ok := cwdLocaleCache.Load(tag); ok {
		if cached, isLocale := v.(*cwdLocale); isLocale {
			return cached, nil
		}
	}
	l := &cwdLocale{tag: tag}
	if tag != "" && !strings.EqualFold(tag, "root") {
		if !cwdLocaleTagRE.MatchString(tag) {
			return nil, fmt.Errorf("%q is not a locale", tag)
		}
		parts := strings.FieldsFunc(tag, func(r rune) bool { return r == '_' || r == '-' })
		l.lang = strings.ToLower(parts[0])
		for _, p := range parts[1:] {
			switch {
			case l.script == "" && l.region == "" && len(p) == 4 && cwdAlpha(p):
				l.script = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
			case l.region == "" && (len(p) == 2 && cwdAlpha(p) || len(p) == 3 && cwdDigits(p)):
				l.region = strings.ToUpper(p)
			}
		}
	}
	l.chain = l.cldrChain()
	for _, id := range l.chain {
		if c, ok := cwdCalendars[id]; ok {
			for i, r := range c {
				l.cal[i] = cwdS(r)
			}
			break
		}
	}
	for _, id := range l.chain {
		if f, ok := cwdZoneFormats[id]; ok {
			for i, r := range f {
				l.formats[i] = cwdS(r)
			}
			break
		}
	}
	region := l.region
	if region == "" {
		region = "US"
	}
	l.firstDay = cwdLookupWeek(cwdFirstDay, region)
	l.minDays = cwdLookupWeek(cwdMinDays, region)
	l.dayPeriods = cwdDayPeriodsOf(l.chain)
	l.compatChain = l.compat()
	cwdLocaleCache.Store(tag, l)
	return l, nil
}

func cwdAlpha(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

func cwdDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cldrChain follows ResourceBundle.Control's candidate list, which gives
// Chinese without a script the script its region implies, to the first CLDR
// locale that exists, then CLDR's parent locales to root.
func (l *cwdLocale) cldrChain() []string {
	script := l.script
	if l.lang == "zh" && script == "" {
		script = "Hans"
		switch l.region {
		case "TW", "HK", "MO":
			script = "Hant"
		}
	}
	var cands []string
	join := func(parts ...string) string {
		var keep []string
		for _, p := range parts {
			if p != "" {
				keep = append(keep, p)
			}
		}
		return strings.Join(keep, "-")
	}
	if l.lang != "" {
		if script != "" {
			cands = append(cands, join(l.lang, script, l.region), join(l.lang, script))
		}
		cands = append(cands, join(l.lang, l.region), l.lang)
	}
	start := "root"
	for _, c := range cands {
		if _, ok := cwdLocaleParent[c]; ok {
			start = c
			break
		}
	}
	var chain []string
	for id := start; id != ""; id = cwdLocaleParent[id] {
		chain = append(chain, id)
	}
	return chain
}

// compat lists the JDK's COMPAT time zone name bundles for the locale, most
// specific first, after mapping Chinese to zh_CN or zh_TW.
func (l *cwdLocale) compat() []string {
	lang, region := l.lang, l.region
	if lang == "zh" {
		switch {
		case l.script == "Hans":
			region = "CN"
		case l.script == "Hant":
			region = "TW"
		case region == "" || region == "CN" || region == "SG":
			region = "CN"
		case region == "HK" || region == "MO" || region == "TW":
			region = "TW"
		}
	}
	var out []string
	for _, c := range []string{lang + "_" + region, lang} {
		if c == "zh_HK" {
			c = "zh_TW"
		}
		if _, ok := cwdCompat()[c]; ok {
			out = append(out, c)
		}
	}
	return append(out, "")
}

func cwdLookupWeek(table map[string]int, region string) int {
	if v, ok := table[region]; ok {
		return v
	}
	return table["001"]
}

// The ISO day of week (Monday is 1) the locale's weeks start on.
func (l *cwdLocale) firstDayISO() int {
	return (l.firstDay+5)%7 + 1
}

// text returns a calendar name table: months (12), days from Sunday (7),
// quarters (4), eras BC/AD (2, FULL, SHORT or NARROW) or day periods (12).
func (l *cwdLocale) months(s cwdStyle) []string {
	return l.cal[cwdCalMonths+int(s)*12 : cwdCalMonths+int(s)*12+12]
}
func (l *cwdLocale) days(s cwdStyle) []string {
	return l.cal[cwdCalDays+int(s)*7 : cwdCalDays+int(s)*7+7]
}
func (l *cwdLocale) quarters(s cwdStyle) []string {
	return l.cal[cwdCalQuarters+int(s)*4 : cwdCalQuarters+int(s)*4+4]
}
func (l *cwdLocale) eras(s cwdStyle) []string {
	i := map[cwdStyle]int{cwdFull: 0, cwdShort: 1, cwdNarrow: 2}[s.base()]
	return l.cal[cwdCalEras+i*2 : cwdCalEras+i*2+2]
}
func (l *cwdLocale) periods(s cwdStyle) []string {
	i := map[cwdStyle]int{cwdFull: 0, cwdShort: 1, cwdNarrow: 2}[s.base()]
	return l.cal[cwdCalPeriods+i*12 : cwdCalPeriods+i*12+12]
}

// cwdDayPeriod is one CLDR day period rule, in minutes of the day.
type cwdDayPeriod struct {
	from, to, index int
}

func (p cwdDayPeriod) includes(mod int) bool {
	if p.from == 0 && p.to == 0 && mod == 1440 {
		return true
	}
	return p.from == mod && p.to == mod || p.from <= mod && mod < p.to || p.from > p.to && (p.from <= mod || p.to > mod)
}

func (p cwdDayPeriod) duration() int {
	if p.from > p.to {
		return 1440 - p.from + p.to
	}
	return p.to - p.from
}

func (p cwdDayPeriod) mid() int { return (p.from + p.duration()/2) % 1440 }

var cwdPeriodIndex = map[string]int{
	"am": 0, "pm": 1, "midnight": 2, "noon": 3, "morning1": 4, "morning2": 5,
	"afternoon1": 6, "afternoon2": 7, "evening1": 8, "evening2": 9, "night1": 10, "night2": 11,
}

var cwdPeriodRuleRE = regexp.MustCompile(`^([a-z12]+):(\d{2}):00(?:-(\d{2}):00)?$`)

// cwdDayPeriodsOf parses the first DayPeriodRules on the locale's chain and
// adds the am and pm periods the JDK falls back on.
func cwdDayPeriodsOf(chain []string) []cwdDayPeriod {
	var out []cwdDayPeriod
	seen := map[cwdDayPeriod]bool{}
	add := func(p cwdDayPeriod) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, id := range chain {
		rules, ok := cwdDayPeriodRules[id]
		if !ok {
			continue
		}
		for _, r := range strings.Split(rules, ";") {
			m := cwdPeriodRuleRE.FindStringSubmatch(r)
			if m == nil {
				continue
			}
			from, _ := strconv.Atoi(m[2])
			to := from
			if m[3] != "" {
				to, _ = strconv.Atoi(m[3])
			}
			add(cwdDayPeriod{from: from * 60, to: to * 60, index: cwdPeriodIndex[m[1]]})
		}
		break
	}
	add(cwdDayPeriod{0, 720, 0})
	add(cwdDayPeriod{720, 1440, 1})
	return out
}

// namedPeriods drops the periods the locale names in no FULL, SHORT or
// NARROW style, as DayPeriodPrinterParser's store removes them.
func (l *cwdLocale) namedPeriods() []cwdDayPeriod {
	var out []cwdDayPeriod
	for _, p := range l.dayPeriods {
		if l.periods(cwdFull)[p.index] != "" && l.periods(cwdShort)[p.index] != "" && l.periods(cwdNarrow)[p.index] != "" {
			out = append(out, p)
		}
	}
	return out
}

// periodFor picks the shortest named period that holds a minute of the day.
func (l *cwdLocale) periodFor(mod int) (cwdDayPeriod, bool) {
	var best cwdDayPeriod
	found := false
	for _, p := range l.namedPeriods() {
		if p.includes(mod) && (!found || p.duration() < best.duration()) {
			best, found = p, true
		}
	}
	return best, found
}

var (
	cwdZoneNamesOnce sync.Once
	cwdZoneNamesIdx  map[string]map[string][6]uint32
	cwdCompatOnce    sync.Once
	cwdCompatIdx     map[string]map[string][6]uint32
	cwdAliasOnce     sync.Once
)

func cwdParseNameLines(data string) map[string]map[string][6]uint32 {
	out := map[string]map[string][6]uint32{}
	for _, line := range strings.Split(data, "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		var refs [6]uint32
		for i, r := range strings.Split(parts[2], ",") {
			n, err := strconv.ParseUint(r, 36, 32)
			if err != nil || i >= 6 {
				panic(fmt.Sprintf("cloudwatch_logs_transformer_cldr_gen.go: bad name line %q", line))
			}
			refs[i] = uint32(n)
		}
		if out[parts[0]] == nil {
			out[parts[0]] = map[string][6]uint32{}
		}
		out[parts[0]][parts[1]] = refs
	}
	return out
}

func cwdZoneNamesTable() map[string]map[string][6]uint32 {
	cwdZoneNamesOnce.Do(func() { cwdZoneNamesIdx = cwdParseNameLines(cwdZoneNamesData) })
	return cwdZoneNamesIdx
}

func cwdCompat() map[string]map[string][6]uint32 {
	cwdCompatOnce.Do(func() { cwdCompatIdx = cwdParseNameLines(cwdCompatNamesData) })
	return cwdCompatIdx
}

// cwdCanonical maps a time zone ID to the ID CLDR files its names under.
var cwdCanonical map[string]string

func cwdCanonicalID(id string) string {
	cwdAliasOnce.Do(func() {
		cwdCanonical = map[string]string{}
		for _, line := range strings.Split(cwdZoneAliases, "\n") {
			ids := strings.Fields(line)
			for _, a := range ids[1:] {
				cwdCanonical[a] = ids[0]
			}
		}
	})
	if c, ok := cwdCanonical[id]; ok {
		return c
	}
	return id
}

func (l *cwdLocale) cldrNames(key string) ([6]string, bool) {
	t := cwdZoneNamesTable()
	for _, id := range l.chain {
		if refs, ok := t[id][key]; ok {
			var n [6]string
			for i, r := range refs {
				n[i] = cwdS(r)
			}
			return n, true
		}
	}
	return [6]string{}, false
}

const cwdNoInheritance = "∅∅∅"

// zoneNames is CLDRTimeZoneNameProviderImpl.getDisplayNameArray: the zone's
// long and short standard, daylight and generic names, from the zone's own
// CLDR names over its metazone's, each empty slot filled from the JDK's
// COMPAT names, the region format, the standard name, or the GMT format.
func (l *cwdLocale) zoneNames(id string) [6]string {
	canon := cwdCanonicalID(id)
	own, _ := l.cldrNames(canon)
	var mz [6]string
	if m, ok := cwdMetazone[canon]; ok {
		mz, _ = l.cldrNames("mz:" + m)
	}
	var names [6]string
	for i := range names {
		names[i] = own[i]
		if names[i] == "" {
			names[i] = mz[i]
		}
	}
	noDST := !cwdUsesDST(id)
	for i := range names {
		switch names[i] {
		case cwdNoInheritance:
			names[i] = l.gmtFormat(id, i == 2 || i == 3)
		case "":
			l.fallbackName(&names, i, id, noDST)
		}
	}
	return names
}

func (l *cwdLocale) fallbackName(names *[6]string, i int, id string, noDST bool) {
	compat := cwdCompat()
	for _, c := range l.compatChain {
		if n, ok := compat[c][id]; ok {
			for j := range names {
				if j == i || names[j] == "" {
					names[j] = cwdS(n[j])
				}
			}
			return
		}
	}
	if i%2 == 0 {
		city := ""
		if !strings.HasPrefix(id, "Etc") && !strings.HasPrefix(id, "SystemV") {
			if slash := strings.LastIndexByte(id, '/'); slash > 0 {
				city = strings.ReplaceAll(id[slash+1:], "_", " ")
			}
		}
		format := map[int]string{0: l.formats[4], 2: l.formats[5], 4: l.formats[3]}[i]
		if city != "" && format != "" {
			names[i] = strings.ReplaceAll(format, "{0}", city)
			return
		}
	}
	if noDST {
		gen, std := 5-i%2, 1-i%2
		if names[gen] != "" && !strings.HasPrefix(names[gen], "GMT") {
			names[i] = names[gen]
			return
		}
		if names[std] != "" && !strings.HasPrefix(names[std], "GMT") {
			names[i] = names[std]
			return
		}
	}
	names[i] = l.gmtFormat(id, i == 2 || i == 3)
}

// cwdZoneOffsets reads a zone's standard offset and daylight saving in
// the current year, as java.util.TimeZone's raw offset and DST savings.
func cwdZoneOffsets(id string) (raw, dst int, ok bool) {
	loc, err := time.LoadLocation(id)
	if err != nil {
		return 0, 0, false
	}
	y := time.Now().Year()
	_, jan := time.Date(y, 1, 1, 0, 0, 0, 0, loc).Zone()
	_, jul := time.Date(y, 7, 1, 0, 0, 0, 0, loc).Zone()
	raw, dst = min(jan, jul), 0
	if jan != jul {
		dst = max(jan, jul) - raw
	}
	return raw, dst, true
}

func cwdUsesDST(id string) bool {
	_, dst, ok := cwdZoneOffsets(id)
	return ok && dst != 0
}

// gmtFormat is the localized GMT format of a zone's standard or daylight
// offset.
func (l *cwdLocale) gmtFormat(id string, daylight bool) string {
	raw, dst, _ := cwdZoneOffsets(id)
	offset := raw
	if daylight {
		offset += dst
	}
	offset /= 60
	if offset == 0 {
		return l.formats[2]
	}
	hours := l.formats[0]
	if offset > 0 {
		hours = hours[:strings.IndexByte(hours, ';')]
	} else {
		hours = hours[strings.IndexByte(hours, ';')+1:]
		offset = -offset
	}
	hours = cwdFormatHM(hours, offset/60, offset%60)
	return strings.Replace(l.formats[1], "{0}", hours, 1)
}

var (
	cwdHoursRE   = regexp.MustCompile(`H+`)
	cwdMinutesRE = regexp.MustCompile(`m+`)
)

func cwdFormatHM(pattern string, h, m int) string {
	pattern = strings.Replace(pattern, cwdHoursRE.FindString(pattern), fmt.Sprintf("%02d", h), 1)
	return strings.Replace(pattern, cwdMinutesRE.FindString(pattern), fmt.Sprintf("%02d", m), 1)
}

// cwdZoneIDs lists the region IDs the simulator's time zone database
// resolves, as ZoneRulesProvider.getAvailableZoneIds does for Java.
var (
	cwdZoneIDsOnce sync.Once
	cwdZoneIDList  []string
)

func cwdZoneIDs() []string {
	cwdZoneIDsOnce.Do(func() {
		seen := map[string]bool{}
		add := func(id string) {
			if seen[id] || id == "" || !strings.Contains(id, "/") && !cwdLegacyRegionIDs[id] {
				return
			}
			if _, err := time.LoadLocation(id); err == nil {
				seen[id] = true
				cwdZoneIDList = append(cwdZoneIDList, id)
			}
		}
		for id := range cwdMetazone {
			add(id)
		}
		for id := range cwdCompat()[""] {
			add(id)
		}
		for a, c := range cwdCanonicalTable() {
			add(a)
			add(c)
		}
		sort.Strings(cwdZoneIDList)
	})
	return cwdZoneIDList
}

func cwdCanonicalTable() map[string]string {
	cwdCanonicalID("")
	return cwdCanonical
}

// Region IDs of the tz database without a slash.
var cwdLegacyRegionIDs = map[string]bool{
	"CET": true, "CST6CDT": true, "Cuba": true, "EET": true, "EST": true, "EST5EDT": true, "Egypt": true,
	"Eire": true, "GB": true, "GB-Eire": true, "GMT0": true, "Greenwich": true, "HST": true,
	"Hongkong": true, "Iceland": true, "Iran": true, "Israel": true, "Jamaica": true, "Japan": true,
	"Kwajalein": true, "Libya": true, "MET": true, "MST": true, "MST7MDT": true, "NZ": true,
	"NZ-CHAT": true, "Navajo": true, "PRC": true, "PST8PDT": true, "Poland": true, "Portugal": true,
	"ROC": true, "ROK": true, "Singapore": true, "Turkey": true, "UCT": true, "UTC": true,
	"Universal": true, "W-SU": true, "WET": true, "Zulu": true,
}

// cwdPreferredZone is java.time.format.ZoneName.toZid: a zone's metazone's
// preferred zone for the locale's region, or for the world.
func (l *cwdLocale) preferredZone(id string) string {
	mz, ok := cwdMetazone[cwdCanonicalID(id)]
	if !ok {
		return id
	}
	if z, ok := cwdMetazonePreferred[mz+"|"+l.region]; ok {
		return z
	}
	if z, ok := cwdMetazonePreferred[mz+"|001"]; ok {
		return z
	}
	return id
}
