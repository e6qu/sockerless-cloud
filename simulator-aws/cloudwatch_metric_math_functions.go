package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// The metric math functions, as the User Guide's "Functions supported for
// metric math" table defines each one's arguments and result.

func (ev *cwMetricEvaluator) call(n *cwMathNode, q CWMetricDataQuery) (cwMathValue, error) {
	if n.name != strings.ToUpper(n.name) {
		return cwMathValue{}, fmt.Errorf("the function %s must be written in uppercase letters", n.name)
	}
	switch n.name {
	case "ANOMALY_DETECTION_BAND":
		return ev.anomalyBand(n, q)
	case "METRICS":
		return ev.metricsFunction(n, q)
	case "SEARCH":
		return ev.search(n, q)
	case "INSIGHT_RULE_METRIC":
		return ev.insightRuleMetric(n, q)
	case "LAMBDA":
		return ev.lambdaDataSource(n, q)
	case "SERVICE_QUOTA":
		return cwMathValue{}, fmt.Errorf("SERVICE_QUOTA needs AWS usage metrics and Service Quotas, which this endpoint does not serve")
	case "DB_PERF_INSIGHTS":
		return cwMathValue{}, fmt.Errorf("DB_PERF_INSIGHTS needs Performance Insights, which this endpoint does not serve")
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
	case "PERIOD":
		if len(args) != 1 || !args[0].isSingle() {
			return cwMathValue{}, cwUnsupportedOperands(n.name, args, fmt.Errorf("PERIOD takes one time series"))
		}
		return cwScalar(float64(args[0].series[0].Period)), nil
	case "ABS", "CEIL", "FLOOR", "LOG", "LOG10":
		return cwMathElementwise(n.name, args)
	case "AVG", "MAX", "MIN", "SUM", "STDDEV":
		return cwMathAggregate(n.name, args)
	case "DATAPOINT_COUNT":
		return cwMathDatapointCount(args)
	case "DIFF", "DIFF_TIME", "RATE", "RUNNING_SUM":
		return cwMathSequential(n.name, args)
	case "FILL":
		return ev.fill(args)
	case "FIRST", "LAST":
		if len(args) != 1 || !args[0].isSeries() || !args[0].array {
			return cwMathValue{}, cwUnsupportedOperands(n.name, args, fmt.Errorf("%s takes an array of time series", n.name))
		}
		if len(args[0].series) == 0 {
			return cwMathValue{series: []cwMetricSeries{}}, nil
		}
		pick := args[0].series[0]
		if n.name == "LAST" {
			pick = args[0].series[len(args[0].series)-1]
		}
		pick.Edge = ""
		return cwSeriesValue(pick), nil
	case "IF":
		return cwMathIf(args)
	case "METRIC_COUNT":
		if len(args) != 1 || !args[0].isSeries() || !args[0].array {
			return cwMathValue{}, cwUnsupportedOperands(n.name, args, fmt.Errorf("METRIC_COUNT takes an array of time series"))
		}
		return cwScalar(float64(len(args[0].series))), nil
	case "MINUTE", "HOUR", "DAY", "DATE", "MONTH", "YEAR", "EPOCH":
		if len(args) != 1 || !args[0].isSingle() {
			return cwMathValue{}, cwUnsupportedOperands(n.name, args, fmt.Errorf("%s takes one time series", n.name))
		}
		return cwSeriesValue(ev.calendar(n.name, args[0].series[0])), nil
	case "REMOVE_EMPTY":
		if len(args) != 1 || !args[0].isSeries() || !args[0].array {
			return cwMathValue{}, cwUnsupportedOperands(n.name, args, fmt.Errorf("REMOVE_EMPTY takes an array of time series"))
		}
		var kept []cwMetricSeries
		for _, s := range args[0].series {
			if len(s.Points) > 0 {
				kept = append(kept, s)
			}
		}
		return cwArrayValue(kept), nil
	case "SLICE":
		return cwMathSlice(args)
	case "SORT":
		return cwMathSort(args)
	case "TIME_SERIES":
		if len(args) != 1 {
			return cwMathValue{}, cwUnsupportedOperands(n.name, args, fmt.Errorf("TIME_SERIES takes one number"))
		}
		if args[0].isSeries() && !args[0].array && len(args[0].series) == 1 && len(args[0].series[0].Points) == 0 {
			return args[0], nil
		}
		if args[0].scalar == nil {
			return cwMathValue{}, cwUnsupportedOperands(n.name, args, fmt.Errorf("TIME_SERIES takes one number"))
		}
		out := cwMetricSeries{Label: "TIME_SERIES", Period: ev.periodOf(q), Points: map[int64]float64{}}
		for _, t := range ev.grid(out.Period) {
			out.Points[t] = *args[0].scalar
		}
		return cwSeriesValue(out), nil
	}
	return cwMathValue{}, fmt.Errorf("unsupported function %s", n.name)
}

// METRICS() answers every metric in the request, in request order; with a
// string, those whose Id contains it.
func (ev *cwMetricEvaluator) metricsFunction(n *cwMathNode, q CWMetricDataQuery) (cwMathValue, error) {
	filter := ""
	switch {
	case len(n.args) == 0:
	case len(n.args) == 1 && n.args[0].kind == "str":
		filter = n.args[0].name
	default:
		return cwMathValue{}, fmt.Errorf("METRICS takes no argument or one string")
	}
	var series []cwMetricSeries
	for _, other := range ev.order {
		if other.MetricStat == nil || other.Id == q.Id || !strings.Contains(other.Id, filter) {
			continue
		}
		v, err := ev.value(other.Id)
		if err != nil {
			return cwMathValue{}, err
		}
		for _, s := range v.series {
			if other.Label != "" {
				s.Label = other.Label
			}
			series = append(series, s)
		}
	}
	return cwArrayValue(series), nil
}

func cwMathElementwise(name string, args []cwMathValue) (cwMathValue, error) {
	if len(args) != 1 || !args[0].isSeries() && args[0].scalar == nil {
		return cwMathValue{}, cwUnsupportedOperands(name, args, fmt.Errorf("%s takes one time series or an array of time series", name))
	}
	f := map[string]func(float64) float64{"ABS": math.Abs, "CEIL": math.Ceil, "FLOOR": math.Floor, "LOG": math.Log, "LOG10": math.Log10}[name]
	apply := func(v float64) (float64, bool) {
		r := f(v)
		return r, !math.IsNaN(r) && !math.IsInf(r, 0)
	}
	if args[0].scalar != nil {
		r, ok := apply(*args[0].scalar)
		if !ok {
			return cwMathValue{}, fmt.Errorf("the result of %s is undefined", name)
		}
		return cwScalar(r), nil
	}
	out := make([]cwMetricSeries, 0, len(args[0].series))
	for _, s := range args[0].series {
		out = append(out, cwMapSeries(s, apply))
	}
	return cwMathValue{series: out, array: args[0].array}, nil
}

func cwReduce(name string, vals []float64) float64 {
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

// On one time series an aggregate answers a scalar; on an array it answers
// one time series of the per-timestamp aggregate. AVG counts a series'
// missing value at a timestamp as 0.
func cwMathAggregate(name string, args []cwMathValue) (cwMathValue, error) {
	if len(args) != 1 || !args[0].isSeries() {
		return cwMathValue{}, cwUnsupportedOperands(name, args, fmt.Errorf("%s takes one time series or an array of time series", name))
	}
	series := args[0].series
	if !args[0].array {
		if len(series) != 1 || len(series[0].Points) == 0 {
			return cwMathValue{series: []cwMetricSeries{cwEmptyLike(series)}}, nil
		}
		return cwScalar(cwReduce(name, series[0].values())), nil
	}
	out := cwMetricSeries{Label: name, Points: map[int64]float64{}}
	if len(series) > 0 {
		out.Period = series[0].Period
	}
	for t := range cwUnionTimes(series...) {
		var vals []float64
		for _, s := range series {
			v, ok := s.Points[t]
			switch {
			case ok:
				vals = append(vals, v)
			case name == "AVG":
				vals = append(vals, 0)
			}
		}
		out.Points[t] = cwReduce(name, vals)
	}
	return cwSeriesValue(out), nil
}

func cwEmptyLike(series []cwMetricSeries) cwMetricSeries {
	if len(series) == 1 {
		return series[0].derive()
	}
	return cwMetricSeries{Points: map[int64]float64{}}
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

func cwMathDatapointCount(args []cwMathValue) (cwMathValue, error) {
	if len(args) != 1 || !args[0].isSeries() {
		return cwMathValue{}, cwUnsupportedOperands("DATAPOINT_COUNT", args, fmt.Errorf("DATAPOINT_COUNT takes one time series or an array of time series"))
	}
	if !args[0].array {
		if len(args[0].series) != 1 {
			return cwScalar(0), nil
		}
		return cwScalar(float64(len(args[0].series[0].Points))), nil
	}
	out := cwMetricSeries{Label: "DATAPOINT_COUNT", Points: map[int64]float64{}}
	for _, s := range args[0].series {
		out.Period = s.Period
		for t := range s.Points {
			out.Points[t]++
		}
	}
	return cwSeriesValue(out), nil
}

// DIFF, DIFF_TIME, RATE and RUNNING_SUM walk each series' datapoints in time
// order; the first datapoint has no predecessor to differ from.
func cwMathSequential(name string, args []cwMathValue) (cwMathValue, error) {
	if len(args) != 1 || !args[0].isSeries() {
		return cwMathValue{}, cwUnsupportedOperands(name, args, fmt.Errorf("%s takes one time series or an array of time series", name))
	}
	out := make([]cwMetricSeries, 0, len(args[0].series))
	for _, s := range args[0].series {
		r := s.derive()
		times := s.times(false)
		running := 0.0
		for i, t := range times {
			v := s.Points[t]
			if name == "RUNNING_SUM" {
				running += v
				r.Points[t] = running
				continue
			}
			if i == 0 {
				continue
			}
			prev := times[i-1]
			switch name {
			case "DIFF":
				r.Points[t] = v - s.Points[prev]
			case "DIFF_TIME":
				r.Points[t] = float64(t - prev)
			case "RATE":
				r.Points[t] = (v - s.Points[prev]) / float64(t-prev)
			}
		}
		out = append(out, r)
	}
	return cwMathValue{series: out, array: args[0].array}, nil
}

// fill gives each period of the request's range without a value the filler:
// a number, the filler series' value at that time, the latest earlier value
// (REPEAT), or the straight line between the values that bound the gap
// (LINEAR).
func (ev *cwMetricEvaluator) fill(args []cwMathValue) (cwMathValue, error) {
	if len(args) != 2 || !args[0].isSeries() {
		return cwMathValue{}, cwUnsupportedOperands("FILL", args, fmt.Errorf("FILL takes a time series or an array of time series and a filler"))
	}
	filler := args[1]
	switch {
	case filler.scalar != nil, filler.keyword == "REPEAT", filler.keyword == "LINEAR", filler.isSingle():
	default:
		return cwMathValue{}, cwUnsupportedOperands("FILL", args, fmt.Errorf("FILL fills with a number, a time series, REPEAT or LINEAR"))
	}
	out := make([]cwMetricSeries, 0, len(args[0].series))
	for _, s := range args[0].series {
		r := s.derive()
		for t, v := range s.Points {
			r.Points[t] = v
		}
		grid := ev.grid(s.Period)
		switch {
		case filler.scalar != nil:
			for _, t := range grid {
				if _, ok := r.Points[t]; !ok {
					r.Points[t] = *filler.scalar
				}
			}
		case filler.keyword == "REPEAT":
			last, seen := 0.0, false
			for _, t := range grid {
				if v, ok := s.Points[t]; ok {
					last, seen = v, true
					continue
				}
				if seen {
					r.Points[t] = last
				}
			}
		case filler.keyword == "LINEAR":
			known := s.times(false)
			for i := 1; i < len(known); i++ {
				a, b := known[i-1], known[i]
				for _, t := range grid {
					if t > a && t < b {
						frac := float64(t-a) / float64(b-a)
						r.Points[t] = s.Points[a] + frac*(s.Points[b]-s.Points[a])
					}
				}
			}
		default:
			for _, t := range grid {
				if _, ok := r.Points[t]; ok {
					continue
				}
				if v, ok := filler.series[0].Points[t]; ok {
					r.Points[t] = v
				}
			}
		}
		out = append(out, r)
	}
	return cwMathValue{series: out, array: args[0].array}, nil
}

// cwMathIf follows "Using IF expressions": a condition datapoint of 0 is
// false and any other value true, and the condition's missing datapoints
// are dropped.
func cwMathIf(args []cwMathValue) (cwMathValue, error) {
	if len(args) < 2 || len(args) > 3 {
		return cwMathValue{}, cwUnsupportedOperands("IF", args, fmt.Errorf("IF takes a condition, a true value and an optional false value"))
	}
	for _, a := range args {
		if a.scalar == nil && !a.isSingle() {
			return cwMathValue{}, cwUnsupportedOperands("IF", args, fmt.Errorf("IF takes numbers and single time series"))
		}
	}
	cond, yes := args[0], args[1]
	var no *cwMathValue
	if len(args) == 3 {
		no = &args[2]
	}
	if cond.scalar != nil {
		if !yes.isSingle() {
			return cwMathValue{}, fmt.Errorf("IF with a number condition takes time series values")
		}
		if *cond.scalar != 0 {
			return yes, nil
		}
		if no == nil {
			return cwMathValue{series: []cwMetricSeries{yes.series[0].derive()}}, nil
		}
		if !no.isSingle() {
			return cwMathValue{}, fmt.Errorf("IF with a number condition takes time series values")
		}
		return *no, nil
	}
	c := cond.series[0]
	out := c.derive()
	out.Label = ""
	if yes.isSingle() {
		out.Label = yes.series[0].Label
	}
	for t, cv := range c.Points {
		if cv != 0 {
			if yes.scalar != nil {
				out.Points[t] = *yes.scalar
			} else {
				out.Points[t] = yes.series[0].Points[t]
			}
			continue
		}
		switch {
		case no == nil:
		case no.scalar != nil:
			out.Points[t] = *no.scalar
		case yes.scalar != nil:
			if v, ok := no.series[0].Points[t]; ok {
				out.Points[t] = v
			}
		default:
			out.Points[t] = no.series[0].Points[t]
		}
	}
	return cwSeriesValue(out), nil
}

// calendar answers a non-sparse series over the request's range, each value
// a UTC part of its timestamp; DAY counts Monday as 1 and Sunday as 7.
func (ev *cwMetricEvaluator) calendar(name string, s cwMetricSeries) cwMetricSeries {
	out := cwMetricSeries{Label: s.Label, Period: s.Period, Points: map[int64]float64{}}
	for _, t := range ev.grid(s.Period) {
		at := time.Unix(t, 0).UTC()
		var v int
		switch name {
		case "MINUTE":
			v = at.Minute()
		case "HOUR":
			v = at.Hour()
		case "DAY":
			v = int(at.Weekday())
			if v == 0 {
				v = 7
			}
		case "DATE":
			v = at.Day()
		case "MONTH":
			v = int(at.Month())
		case "YEAR":
			v = at.Year()
		case "EPOCH":
			out.Points[t] = float64(t)
			continue
		}
		out.Points[t] = float64(v)
	}
	return out
}

func cwMathSlice(args []cwMathValue) (cwMathValue, error) {
	if len(args) < 2 || len(args) > 3 || !args[0].isSeries() || !args[0].array {
		return cwMathValue{}, cwUnsupportedOperands("SLICE", args, fmt.Errorf("SLICE takes an array of time series, a start and an optional end"))
	}
	for _, a := range args[1:] {
		if a.scalar == nil {
			return cwMathValue{}, cwUnsupportedOperands("SLICE", args, fmt.Errorf("SLICE takes numbers for its start and end"))
		}
	}
	series := args[0].series
	clamp := func(v float64) int { return max(0, min(len(series), int(v))) }
	start, end := clamp(*args[1].scalar), len(series)
	if len(args) == 3 {
		end = clamp(*args[2].scalar)
	}
	if end < start {
		end = start
	}
	return cwArrayValue(append([]cwMetricSeries(nil), series[start:end]...)), nil
}

// cwMathSort orders an array by each series' AVG, MIN, MAX or SUM; a series
// without datapoints sorts last.
func cwMathSort(args []cwMathValue) (cwMathValue, error) {
	if len(args) < 3 || len(args) > 4 || !args[0].isSeries() || !args[0].array {
		return cwMathValue{}, cwUnsupportedOperands("SORT", args, fmt.Errorf("SORT takes an array of time series, a function, a sort order and an optional limit"))
	}
	fn, order := args[1].keyword, args[2].keyword
	if fn != "AVG" && fn != "MIN" && fn != "MAX" && fn != "SUM" {
		return cwMathValue{}, fmt.Errorf("SORT sorts by AVG, MIN, MAX or SUM")
	}
	if order != "ASC" && order != "DESC" {
		return cwMathValue{}, fmt.Errorf("SORT orders ASC or DESC")
	}
	series := append([]cwMetricSeries(nil), args[0].series...)
	key := func(s cwMetricSeries) (float64, bool) {
		if len(s.Points) == 0 {
			return 0, false
		}
		return cwReduce(fn, s.values()), true
	}
	sort.SliceStable(series, func(i, j int) bool {
		a, okA := key(series[i])
		b, okB := key(series[j])
		if okA != okB {
			return okA
		}
		if order == "DESC" {
			return a > b
		}
		return a < b
	})
	if len(args) == 4 {
		if args[3].scalar == nil {
			return cwMathValue{}, fmt.Errorf("SORT takes a number for its limit")
		}
		series = series[:max(0, min(len(series), int(*args[3].scalar)))]
	}
	return cwArrayValue(series), nil
}

// INSIGHT_RULE_METRIC(ruleName, metricName) answers one of the rule's report
// metrics, each period of the request's range a datapoint.
func (ev *cwMetricEvaluator) insightRuleMetric(n *cwMathNode, q CWMetricDataQuery) (cwMathValue, error) {
	if len(n.args) != 2 || n.args[0].kind != "str" || n.args[1].kind != "str" {
		return cwMathValue{}, fmt.Errorf("INSIGHT_RULE_METRIC takes a rule name and a metric name")
	}
	rule, metric := n.args[0].name, n.args[1].name
	ir, ok := cwInsightRules.Get(rule)
	if !ok {
		return cwMathValue{}, fmt.Errorf("the Contributor Insights rule %s does not exist", rule)
	}
	if !cwStrIn(metric, cwInsightReportMetrics) {
		return cwMathValue{}, fmt.Errorf("the metric %s is not one of %s", metric, strings.Join(cwInsightReportMetrics, ", "))
	}
	period := ev.periodOf(q)
	report, err := cwBuildInsightReport(ir, ev.start, ev.end, period, 0, "", []string{metric})
	if err != nil {
		return cwMathValue{}, err
	}
	out := cwMetricSeries{Label: metric, Period: period, Points: map[int64]float64{}}
	for _, p := range report.MetricDatapoints {
		out.Points[p.Timestamp] = p.Values[metric]
	}
	return cwSeriesValue(out), nil
}

// ANOMALY_DETECTION_BAND(id, k) answers the lower and upper edges, in that
// order, of the trained model's expected range for the metric or expression
// id, k standard deviations wide (2 by default), at each of its periods.
func (ev *cwMetricEvaluator) anomalyBand(n *cwMathNode, q CWMetricDataQuery) (cwMathValue, error) {
	if len(n.args) < 1 || len(n.args) > 2 || n.args[0].kind != "id" {
		return cwMathValue{}, fmt.Errorf("ANOMALY_DETECTION_BAND takes the id of a metric or expression and an optional number of standard deviations")
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
	if !ok {
		return cwMathValue{}, fmt.Errorf("ID '%s' not found", n.args[0].name)
	}
	m, err := ev.value(target.Id)
	if err != nil {
		return cwMathValue{}, err
	}
	if !m.isSingle() {
		args := []cwMathValue{m}
		if len(n.args) == 2 {
			args = append(args, cwScalar(width))
		}
		return cwMathValue{}, cwUnsupportedOperands(n.name, args, fmt.Errorf("ANOMALY_DETECTION_BAND needs a single time series"))
	}
	series := m.series[0]
	label := target.Label
	if label == "" {
		label = series.Label
	}
	lower := cwMetricSeries{Label: label, Period: series.Period, Points: map[int64]float64{}, Edge: "lower"}
	upper := cwMetricSeries{Label: label, Period: series.Period, Points: map[int64]float64{}, Edge: "upper"}
	if d, ok := ev.detectorFor(target); ok {
		if model, ok := cwFitAnomalyModel(d, series.Period); ok {
			for t := range series.Points {
				expected, spread := model.at(t)
				upper.Points[t] = expected + width*spread
				lower.Points[t] = expected - width*spread
			}
		}
	}
	return cwArrayValue([]cwMetricSeries{lower, upper}), nil
}

// detectorFor answers the model behind a band: the single metric detector of
// a metric, or the metric math detector whose queries are the expression and
// everything it reads.
func (ev *cwMetricEvaluator) detectorFor(target CWMetricDataQuery) (CWAnomalyDetector, bool) {
	if ms := target.MetricStat; ms != nil {
		return cwAnomalyDetectors.Get(cwAnomalyKey(ms.Metric.Namespace, ms.Metric.MetricName, ms.Stat, ms.Metric.Dimensions))
	}
	return cwAnomalyDetectors.Get(cwMathAnomalyKey(cwQueryClosure(ev.order, target.Id)))
}
