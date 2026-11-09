package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// GetMetricData, metric alarms and metric widgets share these MetricDataQuery
// shapes and one evaluator. Expressions follow the User Guide's "Metric math
// syntax and functions": arithmetic over time series and scalars, the
// aggregate functions, and ANOMALY_DETECTION_BAND.

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

// cwMetricSeries maps a period's start, in epoch seconds, to its value.
type cwMetricSeries struct {
	Label  string
	Period int32
	Points map[int64]float64
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

// A metric math value is a scalar or one or more time series.
type cwMathValue struct {
	scalar   *float64
	series   []cwMetricSeries
	detector *CWAnomalyDetector
}

// cwMetricResult is one MetricDataResult: a band answers two, upper first.
type cwMetricResult struct {
	Id     string
	Label  string
	Series cwMetricSeries
}

type cwMetricMathError struct{ msg string }

func (e *cwMetricMathError) Error() string { return e.msg }

func cwMathErrorf(format string, args ...any) error {
	return &cwMetricMathError{msg: fmt.Sprintf(format, args...)}
}

// cwStatSeries aggregates a stored metric over [start, end) into period
// buckets with stat, which may be a percentile such as p99.
func cwStatSeries(ref CWMetricRef, period int32, stat string, start, end int64) cwMetricSeries {
	if period <= 0 {
		period = 60
	}
	out := cwMetricSeries{Label: ref.MetricName, Period: period, Points: map[int64]float64{}}
	data, _ := cwMetrics.Get(metricsKey(ref.Namespace, ref.MetricName, ref.Dimensions))
	buckets := map[int64][]float64{}
	p := int64(period)
	for _, d := range data {
		ts := int64(d.Timestamp)
		if ts < start || ts >= end {
			continue
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

type cwMetricEvaluator struct {
	queries map[string]CWMetricDataQuery
	values  map[string]cwMathValue
	active  map[string]bool
	start   int64
	end     int64
}

// cwEvaluateMetricQueries answers every query with ReturnData unset or true,
// in request order.
func cwEvaluateMetricQueries(queries []CWMetricDataQuery, start, end int64) ([]cwMetricResult, error) {
	ev := &cwMetricEvaluator{queries: map[string]CWMetricDataQuery{}, values: map[string]cwMathValue{}, active: map[string]bool{}, start: start, end: end}
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
		ev.queries[q.Id] = q
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
		if v.scalar != nil {
			return nil, cwMathErrorf("Error in expression %s: the expression returns a scalar, not a time series.", q.Id)
		}
		for _, s := range v.series {
			label := q.Label
			if label == "" {
				label = s.Label
			}
			out = append(out, cwMetricResult{Id: q.Id, Label: label, Series: s})
		}
	}
	return out, nil
}

func (ev *cwMetricEvaluator) value(id string) (cwMathValue, error) {
	if v, ok := ev.values[id]; ok {
		return v, nil
	}
	q, ok := ev.queries[id]
	if !ok {
		return cwMathValue{}, cwMathErrorf("Unrecognized metric or expression id %q.", id)
	}
	if ev.active[id] {
		return cwMathValue{}, cwMathErrorf("Error in expression %s: circular reference.", id)
	}
	ev.active[id] = true
	defer delete(ev.active, id)
	var v cwMathValue
	if q.MetricStat != nil {
		ms := q.MetricStat
		s := cwStatSeries(ms.Metric, ms.Period, ms.Stat, ev.start, ev.end)
		v = cwMathValue{series: []cwMetricSeries{s}}
		if d, ok := cwAnomalyDetectors.Get(cwAnomalyKey(ms.Metric.Namespace, ms.Metric.MetricName, ms.Stat, ms.Metric.Dimensions)); ok {
			v.detector = &d
		}
	} else {
		node, err := cwParseMetricExpression(q.Expression)
		if err != nil {
			return cwMathValue{}, cwMathErrorf("Error in expression %s: %s", id, err.Error())
		}
		v, err = ev.eval(node, q)
		if err != nil {
			return cwMathValue{}, cwMathErrorf("Error in expression %s: %s", id, err.Error())
		}
	}
	ev.values[id] = v
	return v, nil
}

type cwMathNode struct {
	kind  string // num, id, op, neg, call
	num   float64
	name  string
	op    byte
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
	n, err := p.sum()
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
		left = &cwMathNode{kind: "op", op: c, left: left, right: right}
	}
	return left, nil
}

func (p *cwMathParser) product() (*cwMathNode, error) {
	left, err := p.power()
	if err != nil {
		return nil, err
	}
	for c := p.peek(); c == '*' || c == '/'; c = p.peek() {
		p.pos++
		right, err := p.power()
		if err != nil {
			return nil, err
		}
		left = &cwMathNode{kind: "op", op: c, left: left, right: right}
	}
	return left, nil
}

func (p *cwMathParser) power() (*cwMathNode, error) {
	base, err := p.unary()
	if err != nil {
		return nil, err
	}
	if p.peek() == '^' {
		p.pos++
		exp, err := p.power()
		if err != nil {
			return nil, err
		}
		return &cwMathNode{kind: "op", op: '^', left: base, right: exp}, nil
	}
	return base, nil
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
	return p.primary()
}

func (p *cwMathParser) primary() (*cwMathNode, error) {
	c := p.peek()
	switch {
	case c == '(':
		p.pos++
		n, err := p.sum()
		if err != nil {
			return nil, err
		}
		if p.peek() != ')' {
			return nil, fmt.Errorf("expected ')' at position %d", p.pos)
		}
		p.pos++
		return n, nil
	case c >= '0' && c <= '9' || c == '.':
		start := p.pos
		for p.pos < len(p.src) && (p.src[p.pos] >= '0' && p.src[p.pos] <= '9' || p.src[p.pos] == '.') {
			p.pos++
		}
		f, err := strconv.ParseFloat(p.src[start:p.pos], 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q", p.src[start:p.pos])
		}
		return &cwMathNode{kind: "num", num: f}, nil
	case c == '_' || unicode.IsLetter(rune(c)):
		start := p.pos
		for p.pos < len(p.src) && (p.src[p.pos] == '_' || unicode.IsLetter(rune(p.src[p.pos])) || unicode.IsDigit(rune(p.src[p.pos]))) {
			p.pos++
		}
		name := p.src[start:p.pos]
		if p.peek() != '(' {
			return &cwMathNode{kind: "id", name: name}, nil
		}
		p.pos++
		call := &cwMathNode{kind: "call", name: strings.ToUpper(name)}
		if p.peek() == ')' {
			p.pos++
			return call, nil
		}
		for {
			arg, err := p.sum()
			if err != nil {
				return nil, err
			}
			call.args = append(call.args, arg)
			if p.peek() == ',' {
				p.pos++
				continue
			}
			if p.peek() != ')' {
				return nil, fmt.Errorf("expected ')' at position %d", p.pos)
			}
			p.pos++
			return call, nil
		}
	}
	return nil, fmt.Errorf("unexpected %q at position %d", string(c), p.pos)
}

func (ev *cwMetricEvaluator) eval(n *cwMathNode, q CWMetricDataQuery) (cwMathValue, error) {
	switch n.kind {
	case "num":
		f := n.num
		return cwMathValue{scalar: &f}, nil
	case "id":
		if n.name == q.Id {
			return cwMathValue{}, fmt.Errorf("the expression references itself")
		}
		return ev.value(n.name)
	case "neg":
		v, err := ev.eval(n.left, q)
		if err != nil {
			return cwMathValue{}, err
		}
		zero := 0.0
		return cwMathBinary('-', cwMathValue{scalar: &zero}, v)
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

func cwMathApply(op byte, a, b float64) (float64, bool) {
	switch op {
	case '+':
		return a + b, true
	case '-':
		return a - b, true
	case '*':
		return a * b, true
	case '/':
		if b == 0 {
			return 0, false
		}
		return a / b, true
	case '^':
		r := math.Pow(a, b)
		return r, !math.IsNaN(r) && !math.IsInf(r, 0)
	}
	return 0, false
}

// A division by zero or an undefined result drops the datapoint, as metric
// math leaves no value where none is defined.
func cwMathBinary(op byte, l, r cwMathValue) (cwMathValue, error) {
	switch {
	case l.scalar != nil && r.scalar != nil:
		v, ok := cwMathApply(op, *l.scalar, *r.scalar)
		if !ok {
			return cwMathValue{}, fmt.Errorf("the result is undefined")
		}
		return cwMathValue{scalar: &v}, nil
	case l.scalar != nil:
		out := make([]cwMetricSeries, 0, len(r.series))
		for _, s := range r.series {
			out = append(out, cwMapSeries(s, func(v float64) (float64, bool) { return cwMathApply(op, *l.scalar, v) }))
		}
		return cwMathValue{series: out}, nil
	case r.scalar != nil:
		out := make([]cwMetricSeries, 0, len(l.series))
		for _, s := range l.series {
			out = append(out, cwMapSeries(s, func(v float64) (float64, bool) { return cwMathApply(op, v, *r.scalar) }))
		}
		return cwMathValue{series: out}, nil
	}
	if len(l.series) == 0 || len(r.series) == 0 {
		return cwMathValue{series: []cwMetricSeries{}}, nil
	}
	if len(l.series) != 1 && len(r.series) != 1 && len(l.series) != len(r.series) {
		return cwMathValue{}, fmt.Errorf("the operands hold different numbers of time series")
	}
	n := max(len(l.series), len(r.series))
	out := make([]cwMetricSeries, 0, n)
	for i := 0; i < n; i++ {
		a, b := l.series[min(i, len(l.series)-1)], r.series[min(i, len(r.series)-1)]
		s := cwMetricSeries{Label: a.Label, Period: a.Period, Points: map[int64]float64{}}
		for t, av := range a.Points {
			if bv, ok := b.Points[t]; ok {
				if v, ok := cwMathApply(op, av, bv); ok {
					s.Points[t] = v
				}
			}
		}
		out = append(out, s)
	}
	return cwMathValue{series: out}, nil
}

func cwMapSeries(s cwMetricSeries, f func(float64) (float64, bool)) cwMetricSeries {
	out := cwMetricSeries{Label: s.Label, Period: s.Period, Points: map[int64]float64{}}
	for t, v := range s.Points {
		if r, ok := f(v); ok {
			out.Points[t] = r
		}
	}
	return out
}

func (ev *cwMetricEvaluator) call(n *cwMathNode, q CWMetricDataQuery) (cwMathValue, error) {
	if n.name == "ANOMALY_DETECTION_BAND" {
		return ev.anomalyBand(n, q)
	}
	if n.name == "METRICS" {
		var series []cwMetricSeries
		for id, other := range ev.queries {
			if other.MetricStat == nil || id == q.Id {
				continue
			}
			v, err := ev.value(id)
			if err != nil {
				return cwMathValue{}, err
			}
			series = append(series, v.series...)
		}
		sort.Slice(series, func(i, j int) bool { return series[i].Label < series[j].Label })
		return cwMathValue{series: series}, nil
	}
	args := make([]cwMathValue, 0, len(n.args))
	for _, a := range n.args {
		v, err := ev.eval(a, q)
		if err != nil {
			return cwMathValue{}, err
		}
		args = append(args, v)
	}
	switch n.name {
	case "ABS", "CEIL", "FLOOR", "LOG", "LOG10", "SQRT":
		if len(args) != 1 {
			return cwMathValue{}, fmt.Errorf("%s takes one argument", n.name)
		}
		f := map[string]func(float64) float64{"ABS": math.Abs, "CEIL": math.Ceil, "FLOOR": math.Floor, "LOG": math.Log, "LOG10": math.Log10, "SQRT": math.Sqrt}[n.name]
		apply := func(v float64) (float64, bool) {
			r := f(v)
			return r, !math.IsNaN(r) && !math.IsInf(r, 0)
		}
		if args[0].scalar != nil {
			r, ok := apply(*args[0].scalar)
			if !ok {
				return cwMathValue{}, fmt.Errorf("the result of %s is undefined", n.name)
			}
			return cwMathValue{scalar: &r}, nil
		}
		out := make([]cwMetricSeries, 0, len(args[0].series))
		for _, s := range args[0].series {
			out = append(out, cwMapSeries(s, apply))
		}
		return cwMathValue{series: out}, nil
	case "AVG", "MAX", "MIN", "SUM", "STDDEV":
		return cwMathAggregate(n.name, args)
	}
	return cwMathValue{}, fmt.Errorf("unsupported function %s", n.name)
}

// On one time series an aggregate answers a scalar; on several it answers
// one time series of the per-timestamp aggregate.
func cwMathAggregate(name string, args []cwMathValue) (cwMathValue, error) {
	if len(args) != 1 || args[0].scalar != nil {
		return cwMathValue{}, fmt.Errorf("%s takes one time series or an array of time series", name)
	}
	reduce := func(vals []float64) float64 {
		switch name {
		case "AVG":
			return cwApplyStat("Average", vals)
		case "MAX":
			return cwApplyStat("Maximum", vals)
		case "MIN":
			return cwApplyStat("Minimum", vals)
		case "SUM":
			return cwApplyStat("Sum", vals)
		}
		return cwStdDev(vals)
	}
	series := args[0].series
	if len(series) == 1 {
		vals := make([]float64, 0, len(series[0].Points))
		for _, v := range series[0].Points {
			vals = append(vals, v)
		}
		if len(vals) == 0 {
			return cwMathValue{series: []cwMetricSeries{{Label: series[0].Label, Period: series[0].Period, Points: map[int64]float64{}}}}, nil
		}
		r := reduce(vals)
		return cwMathValue{scalar: &r}, nil
	}
	byTime := map[int64][]float64{}
	var period int32
	for _, s := range series {
		period = s.Period
		for t, v := range s.Points {
			byTime[t] = append(byTime[t], v)
		}
	}
	out := cwMetricSeries{Label: name, Period: period, Points: map[int64]float64{}}
	for t, vals := range byTime {
		out.Points[t] = reduce(vals)
	}
	return cwMathValue{series: []cwMetricSeries{out}}, nil
}

func cwStdDev(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	mean := cwApplyStat("Average", vals)
	sum := 0.0
	for _, v := range vals {
		sum += (v - mean) * (v - mean)
	}
	return math.Sqrt(sum / float64(len(vals)))
}

// ANOMALY_DETECTION_BAND(m, k) answers the upper and lower edges of the
// trained model's expected range for m, k standard deviations wide (2 by
// default), at each of m's periods.
func (ev *cwMetricEvaluator) anomalyBand(n *cwMathNode, q CWMetricDataQuery) (cwMathValue, error) {
	if len(n.args) < 1 || len(n.args) > 2 || n.args[0].kind != "id" {
		return cwMathValue{}, fmt.Errorf("ANOMALY_DETECTION_BAND takes the id of a metric and an optional number of standard deviations")
	}
	width := 2.0
	if len(n.args) == 2 {
		w, err := ev.eval(n.args[1], q)
		if err != nil {
			return cwMathValue{}, err
		}
		if w.scalar == nil {
			return cwMathValue{}, fmt.Errorf("the band width of ANOMALY_DETECTION_BAND must be a number")
		}
		width = *w.scalar
	}
	target, ok := ev.queries[n.args[0].name]
	if !ok || target.MetricStat == nil {
		return cwMathValue{}, fmt.Errorf("ANOMALY_DETECTION_BAND needs the id of a metric with MetricStat")
	}
	m, err := ev.value(target.Id)
	if err != nil {
		return cwMathValue{}, err
	}
	label := target.Label
	if label == "" {
		label = target.MetricStat.Metric.MetricName
	}
	upper := cwMetricSeries{Label: label + " (upper)", Period: m.series[0].Period, Points: map[int64]float64{}}
	lower := cwMetricSeries{Label: label + " (lower)", Period: m.series[0].Period, Points: map[int64]float64{}}
	if m.detector != nil {
		model, ok := cwFitAnomalyModel(*m.detector, m.series[0].Period)
		if ok {
			for t := range m.series[0].Points {
				expected, spread := model.at(t)
				upper.Points[t] = expected + width*spread
				lower.Points[t] = expected - width*spread
			}
		}
	}
	return cwMathValue{series: []cwMetricSeries{upper, lower}}, nil
}
