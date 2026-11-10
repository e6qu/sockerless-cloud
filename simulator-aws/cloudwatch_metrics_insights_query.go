package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// A Metrics Insights query, as "Query components and syntax in CloudWatch
// Metrics Insights" defines it:
//
//	SELECT FUNCTION(metricName)
//	FROM namespace | SCHEMA(namespace, labelKey, ...)
//	[ WHERE labelKey = | != 'labelValue' [ AND ... ] ]
//	[ GROUP BY labelKey [ , ... ] ]
//	[ ORDER BY FUNCTION() [ DESC | ASC ] ]
//	[ LIMIT number ]
//
// FUNCTION aggregates every observation of the matched metrics in each
// period. Keywords are case-insensitive, identifiers case-sensitive.

const (
	cwInsightsMaxMetrics   = 10000
	cwInsightsMaxSeries    = 500
	cwInsightsGraphWindow  = 14 * 24 * time.Hour
	cwInsightsAlarmWindow  = 3 * time.Hour
	cwInsightsMinPeriod    = 60
	cwInsightsAccountLabel = "AWS.AccountId"
)

var cwInsightsFunctions = []string{"AVG", "COUNT", "MAX", "MIN", "SUM"}

type cwInsightsCondition struct {
	key   string
	equal bool
	value string
}

type cwInsightsQuery struct {
	function   string
	metric     string
	namespace  string
	schema     bool
	schemaKeys []string
	where      []cwInsightsCondition
	groupBy    []string
	orderBy    string
	descending bool
	limit      int
}

func cwIsMetricsInsightsQuery(expression string) bool {
	trimmed := strings.TrimSpace(expression)
	return len(trimmed) >= 7 && strings.EqualFold(trimmed[:6], "SELECT") && unicode.IsSpace(rune(trimmed[6]))
}

type cwInsightsLexer struct {
	src []rune
	pos int
}

func (l *cwInsightsLexer) skip() {
	for l.pos < len(l.src) && unicode.IsSpace(l.src[l.pos]) {
		l.pos++
	}
}

func (l *cwInsightsLexer) done() bool {
	l.skip()
	return l.pos >= len(l.src)
}

func (l *cwInsightsLexer) keyword(word string) bool {
	l.skip()
	end := l.pos + len(word)
	if end > len(l.src) || !strings.EqualFold(string(l.src[l.pos:end]), word) {
		return false
	}
	if end < len(l.src) && (unicode.IsLetter(l.src[end]) || unicode.IsDigit(l.src[end]) || l.src[end] == '_') {
		return false
	}
	l.pos = end
	return true
}

func (l *cwInsightsLexer) symbol(s string) bool {
	l.skip()
	if strings.HasPrefix(string(l.src[l.pos:]), s) {
		l.pos += len([]rune(s))
		return true
	}
	return false
}

// identifier reads a bare name of letters, digits, underscores and dots, or
// a "double-quoted" one in which a backslash escapes a quotation mark.
func (l *cwInsightsLexer) identifier() (string, error) {
	l.skip()
	if l.pos < len(l.src) && l.src[l.pos] == '"' {
		return l.quoted('"')
	}
	start := l.pos
	for l.pos < len(l.src) && (unicode.IsLetter(l.src[l.pos]) || unicode.IsDigit(l.src[l.pos]) || l.src[l.pos] == '_' || l.src[l.pos] == '.') {
		l.pos++
	}
	if start == l.pos {
		return "", fmt.Errorf("expected a name at position %d", l.pos)
	}
	return string(l.src[start:l.pos]), nil
}

func (l *cwInsightsLexer) quoted(q rune) (string, error) {
	l.pos++
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\\' && l.pos+1 < len(l.src) && (l.src[l.pos+1] == '"' || l.src[l.pos+1] == '\''):
			b.WriteRune(l.src[l.pos+1])
			l.pos += 2
		case c == q:
			l.pos++
			return b.String(), nil
		default:
			b.WriteRune(c)
			l.pos++
		}
	}
	return "", fmt.Errorf("a quoted name or value is missing its closing quotation mark")
}

// function reads an aggregate function's name. CloudWatch refuses another
// name with "Invalid syntax. 'ANOMALY_DETECTION_BAND' is not a valid
// aggregate function".
func (l *cwInsightsLexer) function() (string, error) {
	l.skip()
	for _, f := range cwInsightsFunctions {
		if l.keyword(f) {
			return f, nil
		}
	}
	end := l.pos
	for end < len(l.src) && (unicode.IsLetter(l.src[end]) || unicode.IsDigit(l.src[end]) || l.src[end] == '_') {
		end++
	}
	if end > l.pos {
		return "", cwCloudText(fmt.Sprintf("Invalid syntax. '%s' is not a valid aggregate function", string(l.src[l.pos:end])))
	}
	return "", fmt.Errorf("the function must be one of %s", strings.Join(cwInsightsFunctions, ", "))
}

func cwParseMetricsInsights(src string) (cwInsightsQuery, error) {
	l := &cwInsightsLexer{src: []rune(src)}
	q := cwInsightsQuery{limit: cwInsightsMaxSeries}
	var err error
	if !l.keyword("SELECT") {
		return q, fmt.Errorf("a Metrics Insights query starts with SELECT")
	}
	if q.function, err = l.function(); err != nil {
		return q, err
	}
	if !l.symbol("(") {
		return q, fmt.Errorf("expected ( after %s", q.function)
	}
	if q.metric, err = l.identifier(); err != nil {
		return q, err
	}
	if !l.symbol(")") {
		return q, fmt.Errorf("expected ) after the metric name")
	}
	if !l.keyword("FROM") {
		return q, fmt.Errorf("expected FROM")
	}
	if l.keyword("SCHEMA") {
		q.schema = true
		if !l.symbol("(") {
			return q, fmt.Errorf("expected ( after SCHEMA")
		}
		if q.namespace, err = l.identifier(); err != nil {
			return q, err
		}
		for l.symbol(",") {
			key, err := l.identifier()
			if err != nil {
				return q, err
			}
			q.schemaKeys = append(q.schemaKeys, key)
		}
		if !l.symbol(")") {
			return q, fmt.Errorf("expected ) to close SCHEMA")
		}
	} else if q.namespace, err = l.identifier(); err != nil {
		return q, err
	}
	if l.keyword("WHERE") {
		for {
			c, err := l.condition()
			if err != nil {
				return q, err
			}
			q.where = append(q.where, c)
			if !l.keyword("AND") {
				break
			}
		}
	}
	if l.keyword("GROUP") {
		if !l.keyword("BY") {
			return q, fmt.Errorf("expected BY after GROUP")
		}
		for {
			key, err := l.identifier()
			if err != nil {
				return q, err
			}
			q.groupBy = append(q.groupBy, key)
			if !l.symbol(",") {
				break
			}
		}
	}
	if l.keyword("ORDER") {
		if !l.keyword("BY") {
			return q, fmt.Errorf("expected BY after ORDER")
		}
		if q.orderBy, err = l.function(); err != nil {
			return q, err
		}
		if !l.symbol("(") || !l.symbol(")") {
			return q, fmt.Errorf("expected () after %s in ORDER BY", q.orderBy)
		}
		if l.keyword("DESC") {
			q.descending = true
		} else {
			l.keyword("ASC")
		}
	}
	if l.keyword("LIMIT") {
		l.skip()
		start := l.pos
		for l.pos < len(l.src) && unicode.IsDigit(l.src[l.pos]) {
			l.pos++
		}
		n, err := strconv.Atoi(string(l.src[start:l.pos]))
		if err != nil || n < 1 || n > cwInsightsMaxSeries {
			return q, fmt.Errorf("LIMIT must be a number from 1 to %d", cwInsightsMaxSeries)
		}
		q.limit = n
	}
	if !l.done() {
		return q, fmt.Errorf("unexpected %q", string(l.src[l.pos:]))
	}
	for _, key := range append(append([]string{}, q.groupBy...), cwInsightsConditionKeys(q.where)...) {
		if strings.HasPrefix(key, "tag.") {
			return q, fmt.Errorf("%s filters and groups by resource tags, which this endpoint does not associate with metrics", key)
		}
	}
	return q, nil
}

func cwInsightsConditionKeys(conds []cwInsightsCondition) []string {
	out := make([]string, 0, len(conds))
	for _, c := range conds {
		out = append(out, c.key)
	}
	return out
}

func (l *cwInsightsLexer) condition() (cwInsightsCondition, error) {
	key, err := l.identifier()
	if err != nil {
		return cwInsightsCondition{}, err
	}
	c := cwInsightsCondition{key: key}
	switch {
	case l.symbol("!="):
	case l.symbol("="):
		c.equal = true
	default:
		return c, fmt.Errorf("expected = or != after %s", key)
	}
	l.skip()
	switch {
	case l.pos < len(l.src) && l.src[l.pos] == '\'':
		if c.value, err = l.quoted('\''); err != nil {
			return c, err
		}
	case key == cwInsightsAccountLabel && l.keyword("CURRENT_ACCOUNT_ID"):
		if !l.symbol("(") || !l.symbol(")") {
			return c, fmt.Errorf("expected () after CURRENT_ACCOUNT_ID")
		}
		c.value = awsAccountID()
	case key == cwInsightsAccountLabel:
		start := l.pos
		for l.pos < len(l.src) && unicode.IsDigit(l.src[l.pos]) {
			l.pos++
		}
		c.value = string(l.src[start:l.pos])
		if c.value == "" {
			return c, fmt.Errorf("expected an account ID after %s", key)
		}
	default:
		return c, fmt.Errorf("the value for %s must be in single quotation marks", key)
	}
	return c, nil
}

func cwInsightsLabelValue(key string, dims []CWDimension) (string, bool) {
	if key == cwInsightsAccountLabel {
		return awsAccountID(), true
	}
	for _, d := range dims {
		if d.Name == key {
			return d.Value, true
		}
	}
	return "", false
}

func (q cwInsightsQuery) admits(dims []CWDimension) bool {
	if q.schema {
		if len(dims) != len(q.schemaKeys) {
			return false
		}
		for _, key := range q.schemaKeys {
			if _, ok := cwInsightsLabelValue(key, dims); !ok {
				return false
			}
		}
	}
	for _, c := range q.where {
		v, ok := cwInsightsLabelValue(c.key, dims)
		if c.equal != (ok && v == c.value) {
			return false
		}
	}
	return true
}

func cwInsightsReduce(function string, vals []float64) float64 {
	switch function {
	case "COUNT":
		return float64(len(vals))
	case "SUM":
		return cwApplyStat("Sum", vals)
	case "MIN":
		return cwApplyStat("Minimum", vals)
	case "MAX":
		return cwApplyStat("Maximum", vals)
	}
	return cwApplyStat("Average", vals)
}

// metricsInsights runs the query over the request's range, no further back
// than two weeks (three hours for an alarm), in periods of at least a
// minute. Without GROUP BY it answers one time series; with it, one per
// group, ordered by the group's label values unless ORDER BY says
// otherwise, and a metric without a GROUP BY key falls in group Other.
func (ev *cwMetricEvaluator) metricsInsights(q CWMetricDataQuery) (cwMathValue, error) {
	query, err := cwParseMetricsInsights(q.Expression)
	if err != nil {
		return cwMathValue{}, err
	}
	period := max(ev.periodOf(q), cwInsightsMinPeriod)
	window := cwInsightsGraphWindow
	if ev.opts.alarm {
		window = cwInsightsAlarmWindow
	}
	start := max(ev.start, ev.opts.now.Add(-window).Unix())
	var matched [][]CWMetricDatum
	for _, data := range cwMetrics.List() {
		if len(data) == 0 || data[0].Namespace != query.namespace || data[0].MetricName != query.metric || !query.admits(data[0].Dimensions) {
			continue
		}
		matched = append(matched, data)
	}
	sort.Slice(matched, func(i, j int) bool {
		return metricsKey(matched[i][0].Namespace, matched[i][0].MetricName, matched[i][0].Dimensions) <
			metricsKey(matched[j][0].Namespace, matched[j][0].MetricName, matched[j][0].Dimensions)
	})
	if len(matched) > cwInsightsMaxMetrics {
		matched = matched[:cwInsightsMaxMetrics]
	}
	type group struct {
		labels  []string
		buckets map[int64][]float64
	}
	groups := map[string]*group{}
	p := int64(period)
	for _, data := range matched {
		labels := make([]string, 0, len(query.groupBy))
		for _, key := range query.groupBy {
			v, ok := cwInsightsLabelValue(key, data[0].Dimensions)
			if !ok {
				v = "Other"
			}
			labels = append(labels, v)
		}
		k := strings.Join(labels, "\x00")
		g := groups[k]
		if g == nil {
			g = &group{labels: labels, buckets: map[int64][]float64{}}
			groups[k] = g
		}
		for _, d := range data {
			ts := int64(d.Timestamp)
			if ts < start || ts >= ev.end {
				continue
			}
			g.buckets[(ts/p)*p] = append(g.buckets[(ts/p)*p], d.Value)
		}
	}
	var series []cwMetricSeries
	for _, g := range groups {
		s := cwMetricSeries{Label: strings.Join(g.labels, " "), Period: period, Points: map[int64]float64{}}
		if len(query.groupBy) == 0 {
			s.Label = query.metric
		}
		for t, vals := range g.buckets {
			s.Points[t] = cwInsightsReduce(query.function, vals)
		}
		series = append(series, s)
	}
	sort.SliceStable(series, func(i, j int) bool { return series[i].Label < series[j].Label })
	if query.orderBy != "" {
		key := func(s cwMetricSeries) float64 { return cwInsightsReduce(query.orderBy, s.values()) }
		sort.SliceStable(series, func(i, j int) bool {
			if query.descending {
				return key(series[i]) > key(series[j])
			}
			return key(series[i]) < key(series[j])
		})
	}
	if len(series) > query.limit {
		series = series[:query.limit]
	}
	if len(query.groupBy) == 0 {
		if len(series) == 0 {
			return cwSeriesValue(cwMetricSeries{Label: query.metric, Period: period, Points: map[int64]float64{}}), nil
		}
		return cwSeriesValue(series[0]), nil
	}
	return cwArrayValue(series), nil
}
