//go:build ignore

// gen-cloudwatch-logs-cldr writes simulator-aws/cloudwatch_logs_transformer_cldr_gen.go
// from the vendored CLDR 39 data and JDK 17 sources: the locale data Java SE
// 17's java.time.format.DateTimeFormatter reads for the dateTimeConverter
// processor.
//
// Run from the repository root (scripts/fetch-cloudwatch-logs-transformer-sources.sh
// runs it after vendoring):
//
//	go run scripts/gen-cloudwatch-logs-cldr.go
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"go/format"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const specDir = "specs/cloud-api/aws/"

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func gunzip(name string) []byte {
	b, err := os.ReadFile(specDir + name)
	if err != nil {
		fail(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		fail(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		fail(err)
	}
	return out
}

func untar(name string) map[string][]byte {
	tr := tar.NewReader(bytes.NewReader(gunzip(name)))
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			fail(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			fail(err)
		}
		files[h.Name] = b
	}
}

// pool interns every string the tables reference; a reference packs the
// string's offset into the upper 24 bits and its length into the lower 8.
type pool struct {
	buf   strings.Builder
	index map[string]uint32
}

func (p *pool) ref(s string) uint32 {
	if s == "" {
		return 0
	}
	if r, ok := p.index[s]; ok {
		return r
	}
	if len(s) > 255 {
		fail(fmt.Errorf("string of %d bytes does not fit a reference: %q", len(s), s))
	}
	off := p.buf.Len()
	if off >= 1<<24 {
		fail(fmt.Errorf("string pool overflows 24-bit offsets"))
	}
	p.buf.WriteString(s)
	r := uint32(off)<<8 | uint32(len(s))
	p.index[s] = r
	return r
}

// The calendar names of one locale, in the order cwdCal* constants in
// simulator-aws/cloudwatch_logs_transformer_cldr.go read them: months, days,
// quarters (each in the six TextStyles' order: FULL, FULL_STANDALONE, SHORT,
// SHORT_STANDALONE, NARROW, NARROW_STANDALONE), eras (FULL, SHORT, NARROW)
// and the format day periods (FULL, SHORT, NARROW) in the JDK's AmPmMarkers
// order.
const calLen = 6*12 + 6*7 + 6*4 + 3*2 + 3*12

var (
	styles      = [][2]string{{"format", "wide"}, {"stand-alone", "wide"}, {"format", "abbreviated"}, {"stand-alone", "abbreviated"}, {"format", "narrow"}, {"stand-alone", "narrow"}}
	dayKeys     = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	periodKeys  = []string{"am", "pm", "midnight", "noon", "morning1", "morning2", "afternoon1", "afternoon2", "evening1", "evening2", "night1", "night2"}
	zoneKeys    = [][2]string{{"long", "standard"}, {"short", "standard"}, {"long", "daylight"}, {"short", "daylight"}, {"long", "generic"}, {"short", "generic"}}
	formatKeys  = []string{"hourFormat", "gmtFormat", "gmtZeroFormat", "regionFormat", "regionFormat-type-standard", "regionFormat-type-daylight", "fallbackFormat"}
	usShortMons = []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
)

type gregorian struct {
	Months     map[string]map[string]map[string]string `json:"months"`
	Days       map[string]map[string]map[string]string `json:"days"`
	Quarters   map[string]map[string]map[string]string `json:"quarters"`
	DayPeriods map[string]map[string]map[string]string `json:"dayPeriods"`
	Eras       map[string]map[string]string            `json:"eras"`
}

func calendar(loc string, raw []byte) [calLen]string {
	var doc struct {
		Main map[string]struct {
			Dates struct {
				Calendars struct {
					Gregorian gregorian `json:"gregorian"`
				} `json:"calendars"`
			} `json:"dates"`
		} `json:"main"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		fail(fmt.Errorf("%s ca-gregorian: %v", loc, err))
	}
	g := doc.Main[loc].Dates.Calendars.Gregorian
	var c [calLen]string
	i := 0
	for _, s := range styles {
		for m := 1; m <= 12; m++ {
			v := g.Months[s[0]][s[1]][strconv.Itoa(m)]
			// The JDK's CLDR converter replaces the root locale's month
			// names, which CLDR gives as M01 to M12, with
			// DateFormatSymbols(Locale.US).getShortMonths().
			if loc == "root" && s[1] != "narrow" {
				v = usShortMons[m-1]
			}
			c[i] = v
			i++
		}
	}
	for _, s := range styles {
		for _, d := range dayKeys {
			c[i] = g.Days[s[0]][s[1]][d]
			i++
		}
	}
	for _, s := range styles {
		for q := 1; q <= 4; q++ {
			c[i] = g.Quarters[s[0]][s[1]][strconv.Itoa(q)]
			i++
		}
	}
	for _, w := range []string{"eraNames", "eraAbbr", "eraNarrow"} {
		c[i], c[i+1] = g.Eras[w]["0"], g.Eras[w]["1"]
		i += 2
	}
	for _, w := range []string{"wide", "abbreviated", "narrow"} {
		for _, p := range periodKeys {
			c[i] = g.DayPeriods["format"][w][p]
			i++
		}
	}
	return c
}

type zoneData struct {
	names   map[string][6]string // "Zone/ID" or "mz:Metazone"
	formats [7]string
}

func zones(loc string, raw []byte) zoneData {
	var doc struct {
		Main map[string]struct {
			Dates struct {
				TimeZoneNames map[string]json.RawMessage `json:"timeZoneNames"`
			} `json:"dates"`
		} `json:"main"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		fail(fmt.Errorf("%s timeZoneNames: %v", loc, err))
	}
	tzn := doc.Main[loc].Dates.TimeZoneNames
	zd := zoneData{names: map[string][6]string{}}
	for i, k := range formatKeys {
		if v, ok := tzn[k]; ok {
			if err := json.Unmarshal(v, &zd.formats[i]); err != nil {
				fail(err)
			}
		}
	}
	names := func(node map[string]any) ([6]string, bool) {
		var n [6]string
		found := false
		for i, k := range zoneKeys {
			if w, ok := node[k[0]].(map[string]any); ok {
				if s, ok := w[k[1]].(string); ok {
					n[i] = s
					found = true
				}
			}
		}
		return n, found
	}
	var metazones map[string]map[string]any
	if raw, ok := tzn["metazone"]; ok {
		if err := json.Unmarshal(raw, &metazones); err != nil {
			fail(err)
		}
	}
	for mz, node := range metazones {
		if n, ok := names(node); ok {
			zd.names["mz:"+mz] = n
		}
	}
	var zoneTree map[string]any
	if raw, ok := tzn["zone"]; ok {
		if err := json.Unmarshal(raw, &zoneTree); err != nil {
			fail(err)
		}
	}
	var walk func(node map[string]any, path string)
	walk = func(node map[string]any, path string) {
		if n, ok := names(node); ok {
			zd.names[path] = n
		}
		for k, v := range node {
			if k == "long" || k == "short" || k == "exemplarCity" {
				continue
			}
			if child, ok := v.(map[string]any); ok {
				p := k
				if path != "" {
					p = path + "/" + k
				}
				walk(child, p)
			}
		}
	}
	walk(zoneTree, "")
	return zd
}

// The JDK's COMPAT time zone names: Java sources whose getContents defines
// String arrays and maps zone IDs to them.
var (
	javaArrayRE = regexp.MustCompile(`String\s+(\w+)\[\]\s*=\s*new\s+String\[\]\s*\{`)
	javaEntryRE = regexp.MustCompile(`\{\s*"([^"]+)"\s*,\s*(\w+|new\s+String\[\]\s*\{)`)
)

func javaStrings(src string, start int) ([]string, int) {
	var out []string
	i := start
	for i < len(src) {
		switch c := src[i]; {
		case c == '}':
			return out, i + 1
		case c == '"':
			var b strings.Builder
			j := i + 1
			for src[j] != '"' {
				if src[j] == '\\' {
					switch src[j+1] {
					case 'u':
						n, err := strconv.ParseUint(src[j+2:j+6], 16, 32)
						if err != nil {
							fail(err)
						}
						b.WriteRune(rune(n))
						j += 6
						continue
					case 'n':
						b.WriteByte('\n')
					default:
						b.WriteByte(src[j+1])
					}
					j += 2
					continue
				}
				b.WriteByte(src[j])
				j++
			}
			out = append(out, b.String())
			i = j + 1
		default:
			i++
		}
	}
	fail(fmt.Errorf("unterminated array"))
	return nil, 0
}

func compatNames(file string) map[string][6]string {
	raw := gunzip(file)
	if !utf8.Valid(raw) {
		fail(fmt.Errorf("%s is not UTF-8", file))
	}
	src := string(raw)
	arrays := map[string][]string{}
	for _, m := range javaArrayRE.FindAllStringSubmatchIndex(src, -1) {
		vals, _ := javaStrings(src, m[1])
		arrays[src[m[2]:m[3]]] = vals
	}
	body := src
	if i := strings.Index(src, "return new Object[][]"); i >= 0 {
		body = src[i:]
	}
	out := map[string][6]string{}
	for _, m := range javaEntryRE.FindAllStringSubmatchIndex(body, -1) {
		id, ref := body[m[2]:m[3]], body[m[4]:m[5]]
		var vals []string
		if strings.HasPrefix(ref, "new") {
			vals, _ = javaStrings(body, m[5])
		} else {
			v, ok := arrays[ref]
			if !ok {
				continue
			}
			vals = v
		}
		if len(vals) != 6 {
			fail(fmt.Errorf("%s: %s has %d names", file, id, len(vals)))
		}
		out[id] = [6]string(vals)
	}
	return out
}

func main() {
	var avail struct {
		AvailableLocales struct {
			Full []string `json:"full"`
		} `json:"availableLocales"`
	}
	if err := json.Unmarshal(gunzip("cldr-39.0.0-available-locales.json.gz"), &avail); err != nil {
		fail(err)
	}
	var parents struct {
		Supplemental struct {
			ParentLocales struct {
				ParentLocale map[string]string `json:"parentLocale"`
			} `json:"parentLocales"`
		} `json:"supplemental"`
	}
	if err := json.Unmarshal(gunzip("cldr-39.0.0-parent-locales.json.gz"), &parents); err != nil {
		fail(err)
	}
	locales := avail.AvailableLocales.Full
	sort.Strings(locales)
	available := map[string]bool{}
	for _, l := range locales {
		available[l] = true
	}
	parentOf := func(l string) string {
		if l == "root" {
			return ""
		}
		if p, ok := parents.Supplemental.ParentLocales.ParentLocale[l]; ok {
			return p
		}
		for i := strings.LastIndexByte(l, '-'); i > 0; i = strings.LastIndexByte(l, '-') {
			l = l[:i]
			if available[l] {
				return l
			}
		}
		return "root"
	}

	gregorianFiles := untar("cldr-39.0.0-dates-full-ca-gregorian.tar.gz")
	zoneFiles := untar("cldr-39.0.0-dates-full-timezonenames.tar.gz")
	cals := map[string][calLen]string{}
	zds := map[string]zoneData{}
	for _, l := range locales {
		g, ok := gregorianFiles["main/"+l+"/ca-gregorian.json"]
		if !ok {
			fail(fmt.Errorf("%s has no ca-gregorian.json", l))
		}
		cals[l] = calendar(l, g)
		z, ok := zoneFiles["main/"+l+"/timeZoneNames.json"]
		if !ok {
			fail(fmt.Errorf("%s has no timeZoneNames.json", l))
		}
		zds[l] = zones(l, z)
	}

	var metaDoc struct {
		Supplemental struct {
			MetaZones struct {
				MetazoneInfo struct {
					Timezone map[string]any `json:"timezone"`
				} `json:"metazoneInfo"`
				Metazones []struct {
					MapZone struct {
						Other     string `json:"_other"`
						Type      string `json:"_type"`
						Territory string `json:"_territory"`
					} `json:"mapZone"`
				} `json:"metazones"`
			} `json:"metaZones"`
		} `json:"supplemental"`
	}
	if err := json.Unmarshal(gunzip("cldr-39.0.0-meta-zones.json.gz"), &metaDoc); err != nil {
		fail(err)
	}
	metazone := map[string]string{}
	var walkMZ func(node map[string]any, path string)
	walkMZ = func(node map[string]any, path string) {
		for k, v := range node {
			p := k
			if path != "" {
				p = path + "/" + k
			}
			switch t := v.(type) {
			case []any:
				// The JDK's converter keeps the mapping in force when it
				// runs; a serviced Java SE 17 build runs after every
				// CLDR 39 period has begun, so the open-ended one holds.
				for _, e := range t {
					u, _ := e.(map[string]any)["usesMetazone"].(map[string]any)
					if _, ended := u["_to"]; !ended {
						metazone[p], _ = u["_mzone"].(string)
					}
				}
			case map[string]any:
				walkMZ(t, p)
			}
		}
	}
	walkMZ(metaDoc.Supplemental.MetaZones.MetazoneInfo.Timezone, "")

	preferred := map[string]string{}
	for _, m := range metaDoc.Supplemental.MetaZones.Metazones {
		preferred[m.MapZone.Other+"|"+m.MapZone.Territory] = m.MapZone.Type
	}
	var aliasLines []string
	for _, m := range regexp.MustCompile(`<type name="[a-z0-9]+"[^>]*\salias="([^"]+)"`).FindAllStringSubmatch(string(gunzip("cldr-39-bcp47-timezone.xml.gz")), -1) {
		if strings.Contains(m[1], " ") {
			aliasLines = append(aliasLines, m[1])
		}
	}
	sort.Strings(aliasLines)

	var week struct {
		Supplemental struct {
			WeekData struct {
				FirstDay map[string]string `json:"firstDay"`
				MinDays  map[string]string `json:"minDays"`
			} `json:"weekData"`
		} `json:"supplemental"`
	}
	if err := json.Unmarshal(gunzip("cldr-39.0.0-week-data.json.gz"), &week); err != nil {
		fail(err)
	}
	var periods struct {
		Supplemental struct {
			DayPeriodRuleSet map[string]map[string]map[string]string `json:"dayPeriodRuleSet"`
		} `json:"supplemental"`
	}
	if err := json.Unmarshal(gunzip("cldr-39.0.0-day-periods.json.gz"), &periods); err != nil {
		fail(err)
	}

	compat := map[string]map[string][6]string{"": compatNames("jdk17-TimeZoneNames.java.gz")}
	for _, l := range []string{"de", "es", "fr", "hi", "it", "ja", "ko", "pt_BR", "sv", "zh_CN", "zh_TW"} {
		compat[l] = compatNames("jdk17-TimeZoneNames_" + l + ".java.gz")
	}

	p := &pool{index: map[string]uint32{}}
	var out bytes.Buffer
	out.WriteString("// Code generated by scripts/gen-cloudwatch-logs-cldr.go from specs/cloud-api/aws/cldr-39.0.0-* and jdk17-*. DO NOT EDIT.\n\npackage main\n\n")
	out.WriteString("var cwdLocaleParent = map[string]string{\n")
	for _, l := range locales {
		fmt.Fprintf(&out, "%q: %q,\n", l, parentOf(l))
	}
	out.WriteString("}\n\n")

	writeRefs := func(vals []string) {
		out.WriteString("{")
		for i, v := range vals {
			if i > 0 {
				out.WriteString(",")
			}
			fmt.Fprintf(&out, "%d", p.ref(v))
		}
		out.WriteString("}")
	}
	fmt.Fprintf(&out, "const cwdCalLen = %d\n\n", calLen)
	out.WriteString("// cwdCalendars holds a locale's calendar names where they differ from its\n// parent's.\nvar cwdCalendars = map[string]*[cwdCalLen]uint32{\n")
	for _, l := range locales {
		c := cals[l]
		if par := parentOf(l); par != "" && cals[par] == c {
			continue
		}
		fmt.Fprintf(&out, "%q: ", l)
		writeRefs(c[:])
		out.WriteString(",\n")
	}
	out.WriteString("}\n\n")

	// Zone names and the COMPAT names are too many for map literals the
	// compiler handles quickly; they ship as lines of "locale|key|refs" that
	// the simulator indexes on first use.
	var zoneLines []string
	for _, l := range locales {
		zd := zds[l]
		par := parentOf(l)
		for _, k := range sortedKeys(zd.names) {
			n := zd.names[k]
			if par != "" {
				if pn, ok := zds[par].names[k]; ok && pn == n {
					continue
				}
			}
			zoneLines = append(zoneLines, l+"|"+k+"|"+refList(p, n[:]))
		}
	}
	out.WriteString("// cwdZoneNamesData holds each locale's zone and metazone names where they\n// differ from its parent's.\n")
	fmt.Fprintf(&out, "const cwdZoneNamesData = %q\n\n", strings.Join(zoneLines, "\n"))

	out.WriteString("var cwdZoneFormats = map[string][7]uint32{\n")
	for _, l := range locales {
		f := zds[l].formats
		if par := parentOf(l); par != "" && zds[par].formats == f {
			continue
		}
		fmt.Fprintf(&out, "%q: ", l)
		writeRefs(f[:])
		out.WriteString(",\n")
	}
	out.WriteString("}\n\n")

	out.WriteString("var cwdMetazone = map[string]string{\n")
	for _, z := range sortedKeys(metazone) {
		fmt.Fprintf(&out, "%q: %q,\n", z, metazone[z])
	}
	out.WriteString("}\n\n")

	var compatLines []string
	for _, l := range sortedKeys(compat) {
		for _, id := range sortedKeys(compat[l]) {
			n := compat[l][id]
			compatLines = append(compatLines, l+"|"+id+"|"+refList(p, n[:]))
		}
	}
	fmt.Fprintf(&out, "const cwdCompatNamesData = %q\n\n", strings.Join(compatLines, "\n"))

	out.WriteString("// cwdMetazonePreferred maps a metazone and a region to the zone that\n// stands for the metazone there.\nvar cwdMetazonePreferred = map[string]string{\n")
	for _, k := range sortedKeys(preferred) {
		fmt.Fprintf(&out, "%q: %q,\n", k, preferred[k])
	}
	out.WriteString("}\n\n")
	out.WriteString("// cwdZoneAliases lists CLDR's time zone IDs, each line the ID CLDR files\n// names under and then its aliases.\n")
	fmt.Fprintf(&out, "const cwdZoneAliases = %q\n\n", strings.Join(aliasLines, "\n"))

	dayNum := map[string]int{"sun": 1, "mon": 2, "tue": 3, "wed": 4, "thu": 5, "fri": 6, "sat": 7}
	out.WriteString("// cwdFirstDay is CLDR's first day of the week by region, as a\n// java.util.Calendar day (SUNDAY is 1).\nvar cwdFirstDay = map[string]int{\n")
	for _, r := range sortedKeys(week.Supplemental.WeekData.FirstDay) {
		if strings.Contains(r, "-") {
			continue
		}
		fmt.Fprintf(&out, "%q: %d,\n", r, dayNum[week.Supplemental.WeekData.FirstDay[r]])
	}
	out.WriteString("}\n\nvar cwdMinDays = map[string]int{\n")
	for _, r := range sortedKeys(week.Supplemental.WeekData.MinDays) {
		if strings.Contains(r, "-") {
			continue
		}
		fmt.Fprintf(&out, "%q: %s,\n", r, week.Supplemental.WeekData.MinDays[r])
	}
	out.WriteString("}\n\n")

	out.WriteString("// cwdDayPeriodRules holds each locale's CLDR day period rules as the JDK\n// writes its DayPeriodRules resource: type:HH:00[-HH:00] entries.\nvar cwdDayPeriodRules = map[string]string{\n")
	for _, l := range sortedKeys(periods.Supplemental.DayPeriodRuleSet) {
		set := periods.Supplemental.DayPeriodRuleSet[l]
		var rules []string
		for _, typ := range sortedKeys(set) {
			r := set[typ]
			switch {
			case r["_at"] != "":
				rules = append(rules, typ+":"+r["_at"])
			case r["_from"] != "":
				rules = append(rules, typ+":"+r["_from"]+"-"+r["_before"])
			}
		}
		fmt.Fprintf(&out, "%q: %q,\n", l, strings.Join(rules, ";"))
	}
	out.WriteString("}\n\n")

	fmt.Fprintf(&out, "const cwdPool = %q\n", p.buf.String())

	formatted, err := format.Source(out.Bytes())
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile("simulator-aws/cloudwatch_logs_transformer_cldr_gen.go", formatted, 0o644); err != nil {
		fail(err)
	}
}

func refList(p *pool, vals []string) string {
	refs := make([]string, len(vals))
	for i, v := range vals {
		refs[i] = strconv.FormatUint(uint64(p.ref(v)), 36)
	}
	return strings.Join(refs, ",")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
