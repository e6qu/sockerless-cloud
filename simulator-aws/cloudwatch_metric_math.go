package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// GetMetricData, metric alarms, metric math anomaly detectors and metric
// widgets share these MetricDataQuery shapes and one evaluator. Expressions
// follow the User Guide's "Math expressions with metrics": arithmetic,
// comparison and logical operators over scalars, time series and arrays of
// time series, and every function its table lists; an expression that starts
// with SELECT is a Metrics Insights query.

type CWMetricRef struct {
	Namespace  string        `json:"Namespace" cbor:"Namespace"`
	MetricName string        `json:"MetricName" cbor:"MetricName"`
	Dimensions []CWDimension `json:"Dimensions,omitempty" cbor:"Dimensions,omitempty"`
}

type CWMetricStat struct {
	Metric CWMetricRef `json:"Metric" cbor:"Metric"`
	Period int32       `json:"Period" cbor:"Period"`
	Stat   string      `json:"Stat" cbor:"Stat"`
	Unit   string      `json:"Unit,omitempty" cbor:"Unit,omitempty"`
}

type CWMetricDataQuery struct {
	Id         string        `json:"Id" cbor:"Id"`
	MetricStat *CWMetricStat `json:"MetricStat,omitempty" cbor:"MetricStat,omitempty"`
	Expression string        `json:"Expression,omitempty" cbor:"Expression,omitempty"`
	Label      string        `json:"Label,omitempty" cbor:"Label,omitempty"`
	ReturnData *bool         `json:"ReturnData,omitempty" cbor:"ReturnData,omitempty"`
	Period     int32         `json:"Period,omitempty" cbor:"Period,omitempty"`
	AccountId  string        `json:"AccountId,omitempty" cbor:"AccountId,omitempty"`
}

func (q CWMetricDataQuery) returns() bool { return q.ReturnData == nil || *q.ReturnData }

type cwMessage struct {
	Code  string
	Value string
}

// cwMetricSeries maps a period's start, in epoch seconds, to its value.
// Status is the MetricDataResult StatusCode, empty for Complete.
type cwMetricSeries struct {
	Label    string
	Period   int32
	Points   map[int64]float64
	Status   string
	Messages []cwMessage
	Edge     string
	Unit     string
	// Metric and Stat name the stored metric a series reads, which a dynamic
	// label's ${PROP(...)} values describe; a computed series has none.
	Metric *CWMetricRef
	Stat   string
}

func (s cwMetricSeries) times(descending bool) []int64 {
	keys := make([]int64, 0, len(s.Points))
	for k := range s.Points {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if descending {
			return keys[i] > keys[j]
		}
		return keys[i] < keys[j]
	})
	return keys
}

func (s cwMetricSeries) values() []float64 {
	out := make([]float64, 0, len(s.Points))
	for _, t := range s.times(false) {
		out = append(out, s.Points[t])
	}
	return out
}

// derive answers an empty series that keeps s's label, period and status.
func (s cwMetricSeries) derive() cwMetricSeries {
	return cwMetricSeries{Label: s.Label, Period: s.Period, Points: map[int64]float64{}, Status: s.Status, Messages: s.Messages, Unit: s.Unit}
}

// A metric math value is a scalar (S), a time series (TS), an array of time
// series (TS[]), a string or a bare keyword such as REPEAT or DESC. An array
// literal that also holds numbers keeps its element types in mixed, and no
// function takes it.
type cwMathValue struct {
	scalar  *float64
	series  []cwMetricSeries
	array   bool
	mixed   []string
	text    *string
	keyword string
	boolean *bool
}

func cwScalar(v float64) cwMathValue { return cwMathValue{scalar: &v} }

func cwSeriesValue(s cwMetricSeries) cwMathValue { return cwMathValue{series: []cwMetricSeries{s}} }

func cwArrayValue(s []cwMetricSeries) cwMathValue {
	if s == nil {
		s = []cwMetricSeries{}
	}
	return cwMathValue{series: s, array: true}
}

func (v cwMathValue) isSeries() bool {
	return v.scalar == nil && v.text == nil && v.keyword == "" && v.boolean == nil && v.mixed == nil
}

// operandType names a value as CloudWatch's refusals do: Scalar, TimeSeries,
// Array[TimeSeries], or an array literal's element types, Array[TimeSeries,
// Scalar]. ok is false for a string, a keyword or a Boolean, which no capture
// names.
func (v cwMathValue) operandType() (string, bool) {
	switch {
	case v.mixed != nil:
		return "Array[" + strings.Join(v.mixed, ", ") + "]", true
	case v.scalar != nil:
		return "Scalar", true
	case !v.isSeries():
		return "", false
	case v.array:
		return "Array[TimeSeries]", true
	}
	return "TimeSeries", true
}

// cwUnsupportedOperands refuses a function's arguments as CloudWatch does:
// "Unsupported operand type(s) for PERIOD: '[Array[TimeSeries]]'", as the
// vendored Grafana community posts quote it. Where an argument has no
// captured type name, fallback stands.
func cwUnsupportedOperands(name string, args []cwMathValue, fallback error) error {
	types := make([]string, 0, len(args))
	for _, a := range args {
		t, ok := a.operandType()
		if !ok {
			return fallback
		}
		types = append(types, t)
	}
	return cwCloudText(fmt.Sprintf("Unsupported operand type(s) for %s: '[%s]'", name, strings.Join(types, ", ")))
}

// cwCloudText is a refusal in CloudWatch's own words, which start with a
// capital letter.
type cwCloudText string

func (t cwCloudText) Error() string { return string(t) }

func (v cwMathValue) isSingle() bool { return v.isSeries() && !v.array && len(v.series) == 1 }

// cwMetricResult is one MetricDataResult: a band answers two, lower first.
type cwMetricResult struct {
	Id     string
	Label  string
	Series cwMetricSeries
	// Template is the query's own Label, which may carry dynamic values, and
	// DefaultLabel the label the series takes without one: ${LABEL}.
	Template     string
	DefaultLabel string
	Expression   bool
}

type cwMetricMathError struct{ msg string }

func (e *cwMetricMathError) Error() string { return e.msg }

func cwMathErrorf(format string, args ...any) error {
	return &cwMetricMathError{msg: fmt.Sprintf(format, args...)}
}

// cwExpressionError names the expression a refusal arose in, "Error in
// expression 'e1': ID 'm9' not found", unless an expression it reads already
// named itself.
func cwExpressionError(id string, err error) error {
	var named *cwMetricMathError
	if errors.As(err, &named) {
		return err
	}
	return cwMathErrorf("Error in expression '%s': %s", id, err.Error())
}

// cwStatSeries aggregates a stored metric over [start, end) into period
// buckets with stat, which may be a percentile such as p99.
func cwStatSeries(ref CWMetricRef, period int32, stat string, start, end int64) cwMetricSeries {
	if period <= 0 {
		period = 60
	}
	metric := ref
	out := cwMetricSeries{Label: ref.MetricName, Period: period, Points: map[int64]float64{}, Metric: &metric, Stat: stat}
	data, _ := cwMetrics.Get(metricsKey(ref.Namespace, ref.MetricName, ref.Dimensions))
	buckets := map[int64][]float64{}
	p := int64(period)
	for _, d := range data {
		ts := int64(d.Timestamp)
		if ts < start || ts >= end {
			continue
		}
		if out.Unit == "" {
			out.Unit = d.Unit
		}
		b := (ts / p) * p
		buckets[b] = append(buckets[b], d.Value)
	}
	for b, vals := range buckets {
		out.Points[b] = cwStatValue(stat, vals)
	}
	return out
}

func cwStatValue(stat string, vals []float64) float64 {
	if p, ok := cwParsePercentile(stat); ok {
		return cwPercentile(vals, p)
	}
	return cwApplyStat(stat, vals)
}

// cwMathOptions carries what differs between callers: an alarm gives a
// LAMBDA data source 4.5 seconds and a Metrics Insights query three hours of
// data, a graph 10 seconds and two weeks.
type cwMathOptions struct {
	ctx   context.Context
	alarm bool
	now   time.Time
}

type cwMetricEvaluator struct {
	opts    cwMathOptions
	order   []CWMetricDataQuery
	queries map[string]CWMetricDataQuery
	values  map[string]cwMathValue
	active  map[string]bool
	start   int64
	end     int64
}

// cwEvaluateMetricQueries answers every query with ReturnData unset or true,
// in request order.
func cwEvaluateMetricQueries(opts cwMathOptions, queries []CWMetricDataQuery, start, end int64) ([]cwMetricResult, error) {
	if opts.ctx == nil {
		opts.ctx = context.Background()
	}
	if opts.now.IsZero() {
		opts.now = time.Now()
	}
	ev := &cwMetricEvaluator{opts: opts, order: queries, queries: map[string]CWMetricDataQuery{}, values: map[string]cwMathValue{}, active: map[string]bool{}, start: start, end: end}
	insights := 0
	for _, q := range queries {
		if q.Id == "" {
			return nil, cwMathErrorf("The parameter MetricDataQueries.member.Id is required.")
		}
		if _, dup := ev.queries[q.Id]; dup {
			return nil, cwMathErrorf("The values for parameter id in MetricDataQueries are not unique.")
		}
		if (q.MetricStat == nil) == (q.Expression == "") {
			return nil, cwMathErrorf("Exactly one of MetricStat and Expression must be set for query %s.", q.Id)
		}
		if cwIsMetricsInsightsQuery(q.Expression) {
			insights++
		}
		ev.queries[q.Id] = q
	}
	if insights > 1 {
		return nil, cwMathErrorf("A GetMetricData operation can include only one Metrics Insights query.")
	}
	var out []cwMetricResult
	for _, q := range queries {
		v, err := ev.value(q.Id)
		if err != nil {
			return nil, err
		}
		if !q.returns() {
			continue
		}
		if !v.isSeries() {
			return nil, cwMathErrorf("Error in expression '%s': The return value of the expression must be TimeSeries or Array[TimeSeries]", q.Id)
		}
		for _, s := range v.series {
			out = append(out, cwMetricResult{Id: q.Id, Label: cwResultLabel(q, v, s), Series: s,
				Template: q.Label, DefaultLabel: cwResultLabel(CWMetricDataQuery{}, v, s), Expression: q.Expression != ""})
		}
	}
	return out, nil
}

// cwResultLabel follows the graph legend: an expression that answers an
// array of time series labels each one "Expression-Label Metric-Label", and
// an empty expression label leaves the metric's own. A band's edges carry
// (lower) and (upper).
func cwResultLabel(q CWMetricDataQuery, v cwMathValue, s cwMetricSeries) string {
	if s.Edge != "" {
		base := s.Label
		if q.Label != "" {
			base = q.Label
		}
		return base + " (" + s.Edge + ")"
	}
	switch {
	case q.Label == "":
		return s.Label
	case v.array && s.Label != "":
		return q.Label + " " + s.Label
	}
	return q.Label
}

// periodOf answers the period an expression's own series take: its Period,
// else that of the first metric in the request, else one minute.
func (ev *cwMetricEvaluator) periodOf(q CWMetricDataQuery) int32 {
	if q.Period > 0 {
		return q.Period
	}
	for _, other := range ev.order {
		if other.MetricStat != nil && other.MetricStat.Period > 0 {
			return other.MetricStat.Period
		}
	}
	return 60
}

// grid answers the period starts that cover the request's time range.
func (ev *cwMetricEvaluator) grid(period int32) []int64 {
	if period <= 0 {
		period = 60
	}
	p := int64(period)
	var out []int64
	for t := (ev.start / p) * p; t < ev.end; t += p {
		out = append(out, t)
	}
	return out
}

func (ev *cwMetricEvaluator) value(id string) (cwMathValue, error) {
	if v, ok := ev.values[id]; ok {
		return v, nil
	}
	q, ok := ev.queries[id]
	if !ok {
		return cwMathValue{}, cwMathErrorf("ID '%s' not found", id)
	}
	if ev.active[id] {
		return cwMathValue{}, cwMathErrorf("Error in expression '%s': circular reference.", id)
	}
	ev.active[id] = true
	defer delete(ev.active, id)
	var v cwMathValue
	switch {
	case q.MetricStat != nil:
		ms := q.MetricStat
		v = cwSeriesValue(cwStatSeries(ms.Metric, ms.Period, ms.Stat, ev.start, ev.end))
	case cwIsMetricsInsightsQuery(q.Expression):
		var err error
		v, err = ev.metricsInsights(q)
		if err != nil {
			return cwMathValue{}, cwExpressionError(id, err)
		}
	default:
		node, err := cwParseMetricExpression(q.Expression)
		if err != nil {
			return cwMathValue{}, cwExpressionError(id, err)
		}
		v, err = ev.eval(node, q)
		if err != nil {
			return cwMathValue{}, cwExpressionError(id, err)
		}
	}
	ev.values[id] = v
	return v, nil
}

type cwMathNode struct {
	kind  string // num, str, id, op, neg, call, array
	num   float64
	name  string
	op    string
	left  *cwMathNode
	right *cwMathNode
	args  []*cwMathNode
}

type cwMathParser struct {
	src string
	pos int
}

func cwParseMetricExpression(src string) (*cwMathNode, error) {
	p := &cwMathParser{src: src}
	n, err := p.or()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(p.src) {
		return nil, fmt.Errorf("unexpected %q at position %d", p.src[p.pos:], p.pos)
	}
	return n, nil
}

func (p *cwMathParser) space() {
	for p.pos < len(p.src) && unicode.IsSpace(rune(p.src[p.pos])) {
		p.pos++
	}
}

func (p *cwMathParser) peek() byte {
	p.space()
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

// accept consumes tok when it comes next; a word operator must end there.
func (p *cwMathParser) accept(tok string) bool {
	p.space()
	if !strings.HasPrefix(p.src[p.pos:], tok) {
		return false
	}
	end := p.pos + len(tok)
	if unicode.IsLetter(rune(tok[0])) && end < len(p.src) && cwIdentByte(p.src[end]) {
		return false
	}
	p.pos = end
	return true
}

func cwIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func (p *cwMathParser) or() (*cwMathNode, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.accept("||") || p.accept("OR") {
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		left = &cwMathNode{kind: "op", op: "OR", left: left, right: right}
	}
	return left, nil
}

func (p *cwMathParser) and() (*cwMathNode, error) {
	left, err := p.comparison()
	if err != nil {
		return nil, err
	}
	for p.accept("&&") || p.accept("AND") {
		right, err := p.comparison()
		if err != nil {
			return nil, err
		}
		left = &cwMathNode{kind: "op", op: "AND", left: left, right: right}
	}
	return left, nil
}

func (p *cwMathParser) comparison() (*cwMathNode, error) {
	left, err := p.sum()
	if err != nil {
		return nil, err
	}
	for {
		var op string
		for _, candidate := range []string{"==", "!=", "<=", ">=", "<", ">"} {
			if p.accept(candidate) {
				op = candidate
				break
			}
		}
		if op == "" {
			return left, nil
		}
		right, err := p.sum()
		if err != nil {
			return nil, err
		}
		left = &cwMathNode{kind: "op", op: op, left: left, right: right}
	}
}

func (p *cwMathParser) sum() (*cwMathNode, error) {
	left, err := p.product()
	if err != nil {
		return nil, err
	}
	for c := p.peek(); c == '+' || c == '-'; c = p.peek() {
		p.pos++
		right, err := p.product()
		if err != nil {
			return nil, err
		}
		left = &cwMathNode{kind: "op", op: string(c), left: left, right: right}
	}
	return left, nil
}

func (p *cwMathParser) product() (*cwMathNode, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for c := p.peek(); c == '*' || c == '/'; c = p.peek() {
		p.pos++
		right, err := p.unary()
		if err != nil {
			return nil, err
		}
		left = &cwMathNode{kind: "op", op: string(c), left: left, right: right}
	}
	return left, nil
}

func (p *cwMathParser) unary() (*cwMathNode, error) {
	if p.peek() == '-' {
		p.pos++
		inner, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &cwMathNode{kind: "neg", left: inner}, nil
	}
	return p.power()
}

func (p *cwMathParser) power() (*cwMathNode, error) {
	base, err := p.primary()
	if err != nil {
		return nil, err
	}
	if p.peek() == '^' {
		p.pos++
		exp, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &cwMathNode{kind: "op", op: "^", left: base, right: exp}, nil
	}
	return base, nil
}

func (p *cwMathParser) list(closing byte) ([]*cwMathNode, error) {
	var out []*cwMathNode
	if p.peek() == closing {
		p.pos++
		return out, nil
	}
	for {
		arg, err := p.or()
		if err != nil {
			return nil, err
		}
		out = append(out, arg)
		switch p.peek() {
		case ',':
			p.pos++
		case closing:
			p.pos++
			return out, nil
		default:
			return nil, fmt.Errorf("expected %q at position %d", string(closing), p.pos)
		}
	}
}

func (p *cwMathParser) primary() (*cwMathNode, error) {
	c := p.peek()
	switch {
	case c == '(':
		p.pos++
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.peek() != ')' {
			return nil, fmt.Errorf("expected ')' at position %d", p.pos)
		}
		p.pos++
		return n, nil
	case c == '[':
		p.pos++
		items, err := p.list(']')
		if err != nil {
			return nil, err
		}
		return &cwMathNode{kind: "array", args: items}, nil
	case c == '\'' || c == '"':
		p.pos++
		var b strings.Builder
		for p.pos < len(p.src) && p.src[p.pos] != c {
			if p.src[p.pos] == '\\' && p.pos+1 < len(p.src) && p.src[p.pos+1] == c {
				p.pos++
			}
			b.WriteByte(p.src[p.pos])
			p.pos++
		}
		if p.pos >= len(p.src) {
			return nil, fmt.Errorf("unterminated string")
		}
		p.pos++
		return &cwMathNode{kind: "str", name: b.String()}, nil
	case c >= '0' && c <= '9' || c == '.':
		start := p.pos
		for p.pos < len(p.src) && (p.src[p.pos] >= '0' && p.src[p.pos] <= '9' || p.src[p.pos] == '.') {
			p.pos++
		}
		if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
			q := p.pos + 1
			if q < len(p.src) && (p.src[q] == '+' || p.src[q] == '-') {
				q++
			}
			if q < len(p.src) && p.src[q] >= '0' && p.src[q] <= '9' {
				for q < len(p.src) && p.src[q] >= '0' && p.src[q] <= '9' {
					q++
				}
				p.pos = q
			}
		}
		f, err := strconv.ParseFloat(p.src[start:p.pos], 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q", p.src[start:p.pos])
		}
		return &cwMathNode{kind: "num", num: f}, nil
	case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		start := p.pos
		for p.pos < len(p.src) && cwIdentByte(p.src[p.pos]) {
			p.pos++
		}
		name := p.src[start:p.pos]
		if p.peek() != '(' {
			return &cwMathNode{kind: "id", name: name}, nil
		}
		p.pos++
		args, err := p.list(')')
		if err != nil {
			return nil, err
		}
		return &cwMathNode{kind: "call", name: name, args: args}, nil
	}
	if c == 0 {
		return nil, fmt.Errorf("unexpected end of expression")
	}
	return nil, fmt.Errorf("unexpected %q at position %d", string(c), p.pos)
}

var cwMathKeywords = map[string]bool{"REPEAT": true, "LINEAR": true, "ASC": true, "DESC": true, "AVG": true, "MIN": true, "MAX": true, "SUM": true}

func (ev *cwMetricEvaluator) eval(n *cwMathNode, q CWMetricDataQuery) (cwMathValue, error) {
	switch n.kind {
	case "num":
		return cwScalar(n.num), nil
	case "str":
		s := n.name
		return cwMathValue{text: &s}, nil
	case "id":
		if n.name == q.Id {
			return cwMathValue{}, fmt.Errorf("the expression references itself")
		}
		if _, ok := ev.queries[n.name]; ok {
			return ev.value(n.name)
		}
		if cwMathKeywords[n.name] {
			return cwMathValue{keyword: n.name}, nil
		}
		if n.name == "true" || n.name == "false" {
			b := n.name == "true"
			return cwMathValue{boolean: &b}, nil
		}
		return cwMathValue{}, fmt.Errorf("ID '%s' not found", n.name)
	case "array":
		var all []cwMetricSeries
		var kinds []string
		mixed := false
		for _, item := range n.args {
			v, err := ev.eval(item, q)
			if err != nil {
				return cwMathValue{}, err
			}
			kind := "TimeSeries"
			switch {
			case v.scalar != nil:
				kind, mixed = "Scalar", true
			case !v.isSeries():
				return cwMathValue{}, fmt.Errorf("an array holds only time series")
			default:
				all = append(all, v.series...)
			}
			if !slices.Contains(kinds, kind) {
				kinds = append(kinds, kind)
			}
		}
		if mixed {
			return cwMathValue{array: true, mixed: kinds}, nil
		}
		return cwArrayValue(all), nil
	case "neg":
		v, err := ev.eval(n.left, q)
		if err != nil {
			return cwMathValue{}, err
		}
		return cwMathBinary("-", cwScalar(0), v)
	case "op":
		l, err := ev.eval(n.left, q)
		if err != nil {
			return cwMathValue{}, err
		}
		r, err := ev.eval(n.right, q)
		if err != nil {
			return cwMathValue{}, err
		}
		return cwMathBinary(n.op, l, r)
	case "call":
		return ev.call(n, q)
	}
	return cwMathValue{}, fmt.Errorf("unsupported expression")
}

func cwTruth(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func cwMathApply(op string, a, b float64) (float64, bool) {
	switch op {
	case "+":
		return a + b, true
	case "-":
		return a - b, true
	case "*":
		return a * b, true
	case "/":
		if b == 0 {
			return 0, false
		}
		return a / b, true
	case "^":
		r := math.Pow(a, b)
		return r, !math.IsNaN(r) && !math.IsInf(r, 0)
	case "==":
		return cwTruth(a == b), true
	case "!=":
		return cwTruth(a != b), true
	case "<":
		return cwTruth(a < b), true
	case "<=":
		return cwTruth(a <= b), true
	case ">":
		return cwTruth(a > b), true
	case ">=":
		return cwTruth(a >= b), true
	case "AND":
		return cwTruth(a != 0 && b != 0), true
	case "OR":
		return cwTruth(a != 0 || b != 0), true
	}
	return 0, false
}

// cwMathBinary applies an operator. Between two time series a timestamp only
// one of them has counts the other's missing value as 0; a division by zero
// drops the datapoint.
func cwMathBinary(op string, l, r cwMathValue) (cwMathValue, error) {
	if !l.isSeries() && l.scalar == nil || !r.isSeries() && r.scalar == nil {
		return cwMathValue{}, fmt.Errorf("the operator %s takes numbers and time series", op)
	}
	switch {
	case l.scalar != nil && r.scalar != nil:
		v, ok := cwMathApply(op, *l.scalar, *r.scalar)
		if !ok {
			return cwMathValue{}, fmt.Errorf("the result is undefined")
		}
		return cwScalar(v), nil
	case l.scalar != nil:
		out := make([]cwMetricSeries, 0, len(r.series))
		for _, s := range r.series {
			out = append(out, cwMapSeries(s, func(v float64) (float64, bool) { return cwMathApply(op, *l.scalar, v) }))
		}
		return cwMathValue{series: out, array: r.array}, nil
	case r.scalar != nil:
		out := make([]cwMetricSeries, 0, len(l.series))
		for _, s := range l.series {
			out = append(out, cwMapSeries(s, func(v float64) (float64, bool) { return cwMathApply(op, v, *r.scalar) }))
		}
		return cwMathValue{series: out, array: l.array}, nil
	}
	if l.array && r.array {
		return cwMathValue{}, fmt.Errorf("the operator %s takes at most one array of time series", op)
	}
	if l.array || r.array {
		arr, single, arrLeft := l, r, true
		if r.array {
			arr, single, arrLeft = r, l, false
		}
		if len(single.series) != 1 {
			return cwArrayValue(nil), nil
		}
		out := make([]cwMetricSeries, 0, len(arr.series))
		for _, s := range arr.series {
			if arrLeft {
				out = append(out, cwCombineSeries(op, s, single.series[0]))
			} else {
				out = append(out, cwCombineSeries(op, single.series[0], s))
			}
		}
		return cwArrayValue(out), nil
	}
	if len(l.series) != 1 || len(r.series) != 1 {
		return cwMathValue{series: []cwMetricSeries{}}, nil
	}
	return cwSeriesValue(cwCombineSeries(op, l.series[0], r.series[0])), nil
}

func cwCombineSeries(op string, a, b cwMetricSeries) cwMetricSeries {
	out := a.derive()
	if out.Status == "" {
		out.Status, out.Messages = b.Status, b.Messages
	}
	for t := range cwUnionTimes(a, b) {
		v, ok := cwMathApply(op, a.Points[t], b.Points[t])
		if ok {
			out.Points[t] = v
		}
	}
	return out
}

func cwUnionTimes(series ...cwMetricSeries) map[int64]bool {
	out := map[int64]bool{}
	for _, s := range series {
		for t := range s.Points {
			out[t] = true
		}
	}
	return out
}

func cwMapSeries(s cwMetricSeries, f func(float64) (float64, bool)) cwMetricSeries {
	out := s.derive()
	for t, v := range s.Points {
		if r, ok := f(v); ok {
			out.Points[t] = r
		}
	}
	return out
}
