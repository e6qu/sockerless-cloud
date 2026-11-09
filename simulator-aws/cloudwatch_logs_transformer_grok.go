package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// cwtGrokPattern is one of the guide's supported grok patterns. A template
// names other patterns as %{NAME}; %{NAME:field} inside a template is a
// sub-capture the guide shows the pattern emitting under that fixed name.
type cwtGrokPattern struct {
	template string
	limit    int
}

const cwtIPv4 = `(?:(?:25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])\.){3}(?:25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])`

const cwtH16 = `[0-9A-Fa-f]{1,4}`

var cwtIPv6 = `(?:` + strings.Join([]string{
	`(?:` + cwtH16 + `:){7}` + cwtH16,
	`(?:` + cwtH16 + `:){6}` + cwtIPv4,
	`(?:` + cwtH16 + `:){1,6}:` + cwtIPv4,
	`(?:` + cwtH16 + `:){1,5}(?::` + cwtH16 + `){1,2}`,
	`(?:` + cwtH16 + `:){1,4}(?::` + cwtH16 + `){1,3}`,
	`(?:` + cwtH16 + `:){1,3}(?::` + cwtH16 + `){1,4}`,
	`(?:` + cwtH16 + `:){1,2}(?::` + cwtH16 + `){1,5}`,
	cwtH16 + `:(?::` + cwtH16 + `){1,6}`,
	`(?:` + cwtH16 + `:){1,6}:` + cwtH16,
	`(?:` + cwtH16 + `:){1,7}:`,
	`::(?:[Ff]{4}(?::0{1,4})?:)?` + cwtIPv4,
	`:(?::` + cwtH16 + `){1,7}`,
	`::`,
}, "|") + `)`

var cwtGrokPatterns = map[string]cwtGrokPattern{
	"USERNAME":             {`[a-zA-Z0-9._-]+`, 20},
	"USER":                 {`%{USERNAME}`, 20},
	"INT":                  {`[+-]?[0-9]+`, 20},
	"BASE10NUM":            {`[+-]?(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)`, 20},
	"NUMBER":               {`%{BASE10NUM}`, 20},
	"BASE16NUM":            {`[+-]?(?:0[xX])?[0-9A-Fa-f]+`, 20},
	"POSINT":               {`[1-9][0-9]*`, 20},
	"NONNEGINT":            {`[0-9]+`, 20},
	"WORD":                 {`\b\w+\b`, 20},
	"NOTSPACE":             {`\S+`, 5},
	"SPACE":                {`\s*`, 5},
	"DATA":                 {`.*?`, 5},
	"GREEDYDATA":           {`.*`, 5},
	"GREEDYDATA_MULTILINE": {`(?s:.*)`, 1},
	"QUOTEDSTRING":         {`(?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')`, 20},
	"UUID":                 {`[A-Fa-f0-9]{8}-(?:[A-Fa-f0-9]{4}-){3}[A-Fa-f0-9]{12}`, 20},
	"URN":                  {`urn:[0-9A-Za-z][0-9A-Za-z-]{0,31}:(?:%[0-9a-fA-F]{2}|[0-9A-Za-z()+,.:=@;$_!*'/?#-])+`, 20},
	"ARN":                  {`arn:(?:aws|aws-cn|aws-us-gov):[A-Za-z0-9-]+:[A-Za-z0-9-]+:[0-9]{12}:[A-Za-z0-9_+=,.@-]+(?:[/:][A-Za-z0-9_+=,.@-]+){0,5}`, 5},
	"CISCOMAC":             {`(?:[A-Fa-f0-9]{4}\.){2}[A-Fa-f0-9]{4}`, 20},
	"WINDOWSMAC":           {`(?:[A-Fa-f0-9]{2}-){5}[A-Fa-f0-9]{2}`, 20},
	"COMMONMAC":            {`(?:[A-Fa-f0-9]{2}:){5}[A-Fa-f0-9]{2}`, 20},
	"MAC":                  {`(?:%{CISCOMAC}|%{WINDOWSMAC}|%{COMMONMAC})`, 20},
	"IPV6":                 {cwtIPv6, 5},
	"IPV4":                 {cwtIPv4, 20},
	"IP":                   {`(?:%{IPV6}|%{IPV4})`, 5},
	"HOSTNAME":             {`\b[0-9A-Za-z][0-9A-Za-z-]{0,62}(?:\.[0-9A-Za-z][0-9A-Za-z-]{0,62})*\.?\b`, 5},
	"HOST":                 {`%{HOSTNAME}`, 5},
	"IPORHOST":             {`(?:%{IP}|%{HOSTNAME})`, 5},
	"HOSTPORT":             {`%{IPORHOST}:%{POSINT:PORT}`, 5},
	"URIHOST":              {`%{IPORHOST}(?::%{POSINT:port})?`, 5},
	"UNIXPATH":             {`(?:/(?:[\w_%!$@:.,+~-]+|\\.)*)+(?:\?[\w_%!$@:.,+~=&/-]*)?`, 20},
	"WINPATH":              {`(?:[A-Za-z]+:|\\)(?:\\[^\\?*]*)+`, 5},
	"PATH":                 {`(?:%{UNIXPATH}|%{WINPATH})`, 5},
	"TTY":                  {`/dev/(?:pts|tty(?:[pq])?)(?:\w+)?/?(?:[0-9]+)`, 20},
	"URIPROTO":             {`[A-Za-z](?:[A-Za-z+]*)`, 20},
	"URIPATH":              {`(?:/[A-Za-z0-9$.+!*'(){},~:;=@#%&_\-]*)+`, 20},
	"URIPARAM":             {`\?[A-Za-z0-9$.+!*'|(){},~@#%&/=:;_?\-\[\]<>]*`, 5},
	"URIPATHPARAM":         {`%{URIPATH}(?:%{URIPARAM})?`, 5},
	"URI":                  {`%{URIPROTO}://(?:%{USER}(?::[^@]*)?@)?(?:%{URIHOST})?(?:%{URIPATHPARAM})?`, 5},
	"MONTH":                {`\b(?:[Jj]an(?:uary)?|[Ff]eb(?:ruary)?|[Mm]ar(?:ch)?|[Aa]pr(?:il)?|[Mm]ay|[Jj]un(?:e)?|[Jj]ul(?:y)?|[Aa]ug(?:ust)?|[Ss]ep(?:tember)?|[Oo]ct(?:ober)?|[Nn]ov(?:ember)?|[Dd]ec(?:ember)?)\b`, 20},
	"MONTHNUM":             {`(?:0?[1-9]|1[0-2])`, 20},
	"MONTHNUM2":            {`(?:0[1-9]|1[0-2])`, 20},
	"MONTHDAY":             {`(?:0[1-9]|[12][0-9]|3[01]|[1-9])`, 20},
	"YEAR":                 {`(?:\d\d){1,2}`, 20},
	"DAY":                  {`\b(?:[Mm]on(?:day)?|[Tt]ue(?:sday)?|[Ww]ed(?:nesday)?|[Tt]hu(?:rsday)?|[Ff]ri(?:day)?|[Ss]at(?:urday)?|[Ss]un(?:day)?)\b`, 20},
	"HOUR":                 {`(?:2[0123]|[01]?[0-9])`, 20},
	"MINUTE":               {`(?:[0-5][0-9])`, 20},
	"SECOND":               {`(?:(?:[0-5]?[0-9]|60)(?:[:.,][0-9]+)?)`, 20},
	"TIME":                 {`%{HOUR}:%{MINUTE}:%{SECOND}`, 20},
	"DATE_US":              {`%{MONTHNUM}[/-]%{MONTHDAY}[/-]%{YEAR}`, 20},
	"DATE_EU":              {`%{MONTHDAY}[./-]%{MONTHNUM}[./-]%{YEAR}`, 20},
	"ISO8601_TIMEZONE":     {`(?:Z|[+-]%{HOUR}(?::?%{MINUTE}))`, 20},
	"ISO8601_SECOND":       {`%{SECOND}`, 20},
	"TIMESTAMP_ISO8601":    {`%{YEAR}-%{MONTHNUM}-%{MONTHDAY}[T ]%{HOUR}:?%{MINUTE}(?::?%{SECOND})?%{ISO8601_TIMEZONE}?`, 20},
	"DATE":                 {`(?:%{DATE_US}|%{DATE_EU})`, 20},
	"DATESTAMP":            {`%{DATE}[- ]%{TIME}`, 20},
	"TZ":                   {`(?:[APMCE][SD]T|UTC)`, 20},
	"DATESTAMP_RFC822":     {`%{DAY} %{MONTH} %{MONTHDAY} %{YEAR} %{TIME} %{TZ}`, 20},
	"DATESTAMP_RFC2822":    {`%{DAY}, %{MONTHDAY} %{MONTH} %{YEAR} %{TIME} %{ISO8601_TIMEZONE}`, 20},
	"DATESTAMP_OTHER":      {`%{DAY} %{MONTH} %{MONTHDAY} %{TIME} %{TZ} %{YEAR}`, 20},
	"DATESTAMP_EVENTLOG":   {`%{YEAR}%{MONTHNUM2}%{MONTHDAY}%{HOUR}%{MINUTE}%{SECOND}`, 20},
	"LOGLEVEL":             {`(?:Alert|ALERT|Trace|TRACE|Debug|DEBUG|Notice|NOTICE|Info|INFO|Warn(?:ing)?|WARN(?:ING)?|Err(?:or)?|ERR(?:OR)?|Crit(?:ical)?|CRIT(?:ICAL)?|Fatal|FATAL|Severe|SEVERE|Emerg(?:ency)?|EMERG(?:ENCY)?)`, 20},
	"HTTPDATE":             {`%{MONTHDAY}/%{MONTH}/%{YEAR}:%{TIME} %{INT}`, 20},
	"SYSLOGTIMESTAMP":      {`%{MONTH} +%{MONTHDAY} %{TIME}`, 20},
	"PROG":                 {`[\w./%-]+`, 20},
	"SYSLOGPROG":           {`%{PROG:program}(?:\[%{POSINT:pid}\])?`, 20},
	"SYSLOGHOST":           {`%{IPORHOST}`, 5},
	"SYSLOGFACILITY":       {`<%{NONNEGINT:facility}.%{NONNEGINT:priority}>`, 20},
}

// The patterns the guide caps at five uses in any combination.
var cwtGrokSharedLimit = map[string]bool{
	"URI": true, "URIPARAM": true, "URIPATHPARAM": true, "SPACE": true,
	"DATA": true, "GREEDYDATA": true, "GREEDYDATA_MULTILINE": true,
}

const (
	cwtGrokMaxPatterns    = 20
	cwtGrokMaxSharedUses  = 5
	cwtGrokMaxMatchLength = 512
)

var cwtGrokRefRE = regexp.MustCompile(`%\{([A-Za-z0-9_]+)(?::([^:}]+))?(?::([^}]*))?\}`)

type cwtGrokCapture struct {
	group   string
	field   string
	convert cwtGrokConvert
}

// cwtGrokConvert turns a captured value into the value the field holds; keep
// false leaves the field out.
type cwtGrokConvert func(string) (v any, keep bool, err error)

type cwtGrokBuilder struct {
	n        int
	captures []cwtGrokCapture
}

func (b *cwtGrokBuilder) group(field string, convert cwtGrokConvert, inner string) string {
	b.n++
	name := "g" + strconv.Itoa(b.n)
	b.captures = append(b.captures, cwtGrokCapture{group: name, field: field, convert: convert})
	return "(?P<" + name + ">" + inner + ")"
}

// expand inlines a pattern template; sub-captures become groups only when the
// pattern is used directly in the match.
func (b *cwtGrokBuilder) expand(name string, subCaptures bool) string {
	p := cwtGrokPatterns[name]
	return cwtGrokRefRE.ReplaceAllStringFunc(p.template, func(ref string) string {
		m := cwtGrokRefRE.FindStringSubmatch(ref)
		inner := b.expand(m[1], false)
		if m[2] != "" && subCaptures {
			return b.group(m[2], nil, inner)
		}
		return "(?:" + inner + ")"
	})
}

type cwtGrokStep struct {
	source   string
	re       *regexp.Regexp
	captures []cwtGrokCapture
}

func cwtCompileGrok(c *cwtGrok) (cwtStep, error) {
	if c.Source != nil && *c.Source != "@message" {
		if err := cwtCheckKey("grok.source", *c.Source, 128); err != nil {
			return nil, err
		}
	}
	g, err := cwtCompileGrokMatch(c.Match)
	if err != nil {
		return nil, err
	}
	g.source = cwtStr(c.Source)
	return g, nil
}

func cwtCompileGrokMatch(match string) (*cwtGrokStep, error) {
	if err := cwtCheckLen("grok.match", match, 1, cwtGrokMaxMatchLength); err != nil {
		return nil, err
	}
	refs := cwtGrokRefRE.FindAllStringSubmatchIndex(match, -1)
	if len(refs) == 0 {
		return nil, cwtInvalid("grok.match must contain at least one grok pattern")
	}
	if len(refs) > cwtGrokMaxPatterns {
		return nil, cwtInvalid("grok.match can combine at most %d grok patterns", cwtGrokMaxPatterns)
	}
	if rest := cwtGrokRefRE.ReplaceAllString(match, ""); strings.Contains(rest, "%{") {
		return nil, cwtInvalid("grok.match contains a malformed grok pattern")
	}
	b := &cwtGrokBuilder{}
	var re strings.Builder
	re.WriteString("^")
	uses := map[string]int{}
	shared := 0
	prev := 0
	commonAt := -1
	for i, r := range refs {
		name := match[r[2]:r[3]]
		field := ""
		if r[4] >= 0 {
			field = match[r[4]:r[5]]
		}
		if r[6] >= 0 {
			return nil, cwtInvalid("grok patterns don't support type conversions: %s", match[r[0]:r[1]])
		}
		literal := match[prev:r[0]]
		prev = r[1]
		if common, ok := cwtGrokCommonPatterns[name]; ok {
			if i != 0 || literal != "" {
				return nil, cwtInvalid("%s must be the first pattern in grok.match", name)
			}
			if field != "" {
				return nil, cwtInvalid("%s does not take a field name", name)
			}
			commonAt = i
			re.WriteString(common.build(b))
			continue
		}
		p, ok := cwtGrokPatterns[name]
		if !ok {
			return nil, cwtInvalid("grok.match uses an unsupported grok pattern %q", name)
		}
		if commonAt >= 0 {
			if i != commonAt+1 || (name != "DATA" && name != "GREEDYDATA" && name != "GREEDYDATA_MULTILINE") {
				return nil, cwtInvalid("Only one DATA, GREEDYDATA or GREEDYDATA_MULTILINE pattern can follow a common log pattern")
			}
		}
		canonical := cwtGrokCanonical(name)
		uses[canonical]++
		if uses[canonical] > p.limit {
			return nil, cwtInvalid("grok pattern %s can be used at most %d times", name, p.limit)
		}
		if cwtGrokSharedLimit[name] {
			shared++
			if shared > cwtGrokMaxSharedUses {
				return nil, cwtInvalid("URI, URIPARAM, URIPATHPARAM, SPACE, DATA, GREEDYDATA and GREEDYDATA_MULTILINE can be used at most %d times in total", cwtGrokMaxSharedUses)
			}
		}
		re.WriteString(regexp.QuoteMeta(literal))
		inner := b.expand(name, true)
		switch {
		case field == "":
			re.WriteString("(?:" + inner + ")")
		case name == "QUOTEDSTRING":
			re.WriteString(b.group(field, cwtGrokUnquote, inner))
		default:
			re.WriteString(b.group(field, nil, inner))
		}
	}
	re.WriteString(regexp.QuoteMeta(match[prev:]))
	re.WriteString("$")
	compiled, err := regexp.Compile(re.String())
	if err != nil {
		return nil, cwtInvalid("grok.match %q does not compile: %v", match, err)
	}
	return &cwtGrokStep{re: compiled, captures: b.captures}, nil
}

func cwtGrokCanonical(name string) string {
	switch name {
	case "USER":
		return "USERNAME"
	case "HOST":
		return "HOSTNAME"
	}
	return name
}

func cwtGrokUnquote(s string) (any, bool, error) {
	return s[1 : len(s)-1], true, nil
}

func (g *cwtGrokStep) apply(ev *cwtEvent) error {
	raw, err := ev.sourceString(g.source)
	if err != nil {
		return err
	}
	m := g.re.FindStringSubmatchIndex(raw)
	if m == nil {
		return fmt.Errorf("grok: %q does not match the grok pattern", cwtOr(g.source, "@message"))
	}
	for _, c := range g.captures {
		idx := g.re.SubexpIndex(c.group)
		if m[2*idx] < 0 {
			continue
		}
		v := raw[m[2*idx]:m[2*idx+1]]
		var val any = v
		if c.convert != nil {
			conv, keep, err := c.convert(v)
			if err != nil {
				return fmt.Errorf("grok: %s: %v", c.field, err)
			}
			if !keep {
				continue
			}
			val = conv
		}
		ev.root.SetPath(c.field, val, true)
	}
	return nil
}

// cwtGrokCommon is a common log format pattern: its regex and how it emits
// each field.
type cwtGrokCommon struct {
	build func(b *cwtGrokBuilder) string
}

// A dash marks an absent value in the common log formats; the field is left
// out.
func cwtGrokDashAbsent(s string) (any, bool, error) { return s, s != "-", nil }

func cwtGrokNumber(s string) (any, bool, error) {
	if s == "-" {
		return nil, false, nil
	}
	return json.Number(s), true, nil
}

var cwtGrokHTTPDateRE = regexp.MustCompile(`^([0-9]{1,2})/([A-Za-z]+)/([0-9]{2}|[0-9]{4}):([0-9]{1,2}):([0-9]{2}):([0-9]{1,2}) ([+-])([0-9]{2})([0-9]{2})$`)

var cwtEnglishMonths = map[string]time.Month{
	"jan": 1, "january": 1, "feb": 2, "february": 2, "mar": 3, "march": 3, "apr": 4, "april": 4,
	"may": 5, "jun": 6, "june": 6, "jul": 7, "july": 7, "aug": 8, "august": 8,
	"sep": 9, "september": 9, "oct": 10, "october": 10, "nov": 11, "november": 11, "dec": 12, "december": 12,
}

// HTTPDATE becomes an ISO 8601 UTC timestamp.
func cwtGrokHTTPDate(s string) (any, bool, error) {
	m := cwtGrokHTTPDateRE.FindStringSubmatch(s)
	if m == nil {
		return nil, false, fmt.Errorf("timestamp %q has no +HHMM offset", s)
	}
	month, ok := cwtEnglishMonths[strings.ToLower(m[2])]
	if !ok {
		return nil, false, fmt.Errorf("timestamp %q names no month", s)
	}
	n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
	year := n(3)
	if len(m[3]) == 2 {
		year += 2000
	}
	offset := (n(8)*60 + n(9)) * 60
	if m[7] == "-" {
		offset = -offset
	}
	t := time.Date(year, month, n(1), n(4), n(5), n(6), 0, time.FixedZone("", offset))
	return t.UTC().Format("2006-01-02T15:04:05Z"), true, nil
}

func cwtGrokAccessLog(b *cwtGrokBuilder) string {
	var r strings.Builder
	r.WriteString(b.group("remote_host", nil, b.expand("IPORHOST", false)))
	r.WriteString(` \S+ `)
	r.WriteString(b.group("auth_user", cwtGrokDashAbsent, `\S+`))
	r.WriteString(` \[`)
	r.WriteString(b.group("timestamp", cwtGrokHTTPDate, b.expand("HTTPDATE", false)))
	r.WriteString(`\] "`)
	r.WriteString(b.group("http_method", nil, `\w+`))
	r.WriteString(` `)
	r.WriteString(b.group("request", nil, `\S+`))
	r.WriteString(` HTTP/`)
	r.WriteString(b.group("http_version", nil, `[0-9.]+`))
	r.WriteString(`" `)
	r.WriteString(b.group("status_code", cwtGrokNumber, `[0-9]{3}`))
	r.WriteString(` `)
	r.WriteString(b.group("response_size", cwtGrokNumber, `[0-9]+|-`))
	r.WriteString(`(?: "`)
	r.WriteString(b.group("referrer", cwtGrokDashAbsent, `[^"]*`))
	r.WriteString(`" "`)
	r.WriteString(b.group("agent", cwtGrokDashAbsent, `[^"]*`))
	r.WriteString(`")?`)
	return r.String()
}

func cwtGrokSyslog5424(b *cwtGrokBuilder) string {
	var r strings.Builder
	r.WriteString(`<`)
	r.WriteString(b.group("pri", cwtGrokNumber, `[0-9]{1,3}`))
	r.WriteString(`>`)
	r.WriteString(b.group("version", cwtGrokNumber, `[0-9]{1,2}`))
	for _, f := range []string{"timestamp", "hostname", "app", "proc_id", "msg_id"} {
		r.WriteString(` `)
		r.WriteString(b.group(f, cwtGrokDashAbsent, `\S+`))
	}
	r.WriteString(` (?:-|\[`)
	r.WriteString(b.group("structured_data", nil, `(?:[^\]\\]|\\.)*`))
	r.WriteString(`\]) ?`)
	r.WriteString(b.group("message", func(s string) (any, bool, error) { return s, s != "", nil }, `.*?`))
	return r.String()
}

var cwtGrokCommonPatterns = map[string]cwtGrokCommon{
	"APACHE_ACCESS_LOG": {build: cwtGrokAccessLog},
	"NGINX_ACCESS_LOG":  {build: cwtGrokAccessLog},
	"SYSLOG5424":        {build: cwtGrokSyslog5424},
}
