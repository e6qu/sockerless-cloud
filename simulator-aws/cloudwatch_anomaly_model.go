package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CloudWatch anomaly detection, as the User Guide's "Using CloudWatch anomaly
// detection" describes it: a model per metric and statistic, or per metric
// math expression, trained on up to two weeks of history less the excluded
// time ranges, that accounts for hourly, daily and weekly patterns and gives
// an expected value and a band k standard deviations wide around it. The
// trainer re-trains a detector whenever its training window gains or loses
// datapoints.

const cwAnomalyTrainingWindow = 14 * 24 * time.Hour

type CWAnomalyExcludedRange struct {
	StartTime int64 `json:"StartTime"`
	EndTime   int64 `json:"EndTime"`
}

type CWAnomalyConfiguration struct {
	ExcludedTimeRanges       []CWAnomalyExcludedRange `json:"ExcludedTimeRanges,omitempty"`
	MetricTimezone           string                   `json:"MetricTimezone,omitempty"`
	AdaptationSpeed          string                   `json:"AdaptationSpeed,omitempty"`
	OutlierFilteringStrength string                   `json:"OutlierFilteringStrength,omitempty"`
}

func (c CWAnomalyConfiguration) excludes(ts int64) bool {
	for _, r := range c.ExcludedTimeRanges {
		if ts >= r.StartTime && ts < r.EndTime {
			return true
		}
	}
	return false
}

func (c CWAnomalyConfiguration) location() *time.Location {
	if c.MetricTimezone != "" {
		if loc, err := time.LoadLocation(c.MetricTimezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// The outlier cut-off, in robust standard deviations from the residuals'
// median: STRICT excludes the most, MINIMAL only the most extreme.
func (c CWAnomalyConfiguration) outlierCutoff() float64 {
	switch c.OutlierFilteringStrength {
	case "MINIMAL":
		return 6
	case "STRICT":
		return 2.5
	}
	return 4
}

// The half-life over which the model's level follows a sustained change in
// the baseline: FAST tracks it within hours, SLOW over a week.
func (c CWAnomalyConfiguration) adaptationHalfLife() time.Duration {
	switch c.AdaptationSpeed {
	case "FAST":
		return time.Hour
	case "SLOW":
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

func startCWAnomalyTrainer(srv *sim.Server) {
	srv.StartBackground("CloudWatch anomaly detector trainer", func(ctx context.Context) {
		ticker := time.NewTicker(cwAlarmEvalInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				cwTrainAnomalyDetectors(ctx, now)
			}
		}
	})
}

func cwTrainAnomalyDetectors(ctx context.Context, now time.Time) {
	for _, d := range cwAnomalyDetectors.List() {
		cwTrainAnomalyDetector(ctx, d.key(), now)
	}
}

// cwAnomalyTrainingSeries answers what a detector learns from over
// [from, to): the metric's statistic at period, or the series its returned
// metric math query answers at its own period.
func cwAnomalyTrainingSeries(ctx context.Context, d CWAnomalyDetector, period int32, from, to int64) (cwMetricSeries, bool) {
	if d.kind() != cwAnomalyMetricMath {
		return cwStatSeries(CWMetricRef{Namespace: d.Namespace, MetricName: d.MetricName, Dimensions: d.Dimensions}, period, d.Stat, from, to), true
	}
	returned := cwMathDetectorReturned(d)
	results, err := cwEvaluateMetricQueries(cwMathOptions{ctx: ctx}, d.MathQueries, from, to)
	if err != nil {
		return cwMetricSeries{}, false
	}
	for _, r := range results {
		if r.Id == returned.Id {
			return r.Series, true
		}
	}
	return cwMetricSeries{}, false
}

// A detector with no history stays PENDING_TRAINING, as LocalStack's
// AWS-validated snapshot records it; one whose window holds
// fewer than two datapoints has no spread to learn and is
// TRAINED_INSUFFICIENT_DATA.
func cwTrainAnomalyDetector(ctx context.Context, key string, now time.Time) {
	d, ok := cwAnomalyDetectors.Get(key)
	if !ok {
		return
	}
	from := now.Add(-cwAnomalyTrainingWindow).Unix()
	to := now.Unix() + 1
	points := 0
	var newest int64
	if d.kind() == cwAnomalyMetricMath {
		series, ok := cwAnomalyTrainingSeries(ctx, d, 0, from, to)
		if !ok {
			return
		}
		for ts := range series.Points {
			if d.Configuration.excludes(ts) {
				continue
			}
			points++
			newest = max(newest, ts)
		}
	} else {
		data, _ := cwMetrics.Get(metricsKey(d.Namespace, d.MetricName, d.Dimensions))
		for _, p := range data {
			ts := int64(p.Timestamp)
			if ts < from || ts >= to || d.Configuration.excludes(ts) {
				continue
			}
			points++
			newest = max(newest, ts)
		}
	}
	if points == d.TrainedPoints && newest == d.TrainedNewest && d.StateValue != "" && d.Id != "" {
		return
	}
	cwAnomalyDetectors.Update(key, func(d *CWAnomalyDetector) {
		if d.Id == "" {
			d.Id, d.AccountId = sim.NewUUID(), awsAccountID()
		}
		d.TrainedPoints, d.TrainedNewest = points, newest
		switch {
		case points == 0:
			d.StateValue = "PENDING_TRAINING"
			return
		case points < 2:
			d.StateValue = "TRAINED_INSUFFICIENT_DATA"
		default:
			d.StateValue = "TRAINED"
		}
		d.TrainedFrom, d.TrainedTo = from, to
	})
}

func (d CWAnomalyDetector) state() string {
	if d.StateValue == "" {
		return "PENDING_TRAINING"
	}
	return d.StateValue
}

type cwAnomalyModel struct {
	loc        *time.Location
	global     float64
	hourOfWeek map[int]float64
	hourOfDay  map[int]float64
	level      float64
	spread     float64
	spikes     map[int]float64
}

type cwAnomalySample struct {
	ts int64
	v  float64
}

// cwSeasonalMedians fits the expected value: the median of the values seen
// at the same hour of the week (two or more weeks of it), else at the same
// hour of the day (two or more days), else overall.
func cwSeasonalMedians(m *cwAnomalyModel, samples []cwAnomalySample) {
	m.hourOfWeek, m.hourOfDay = map[int]float64{}, map[int]float64{}
	var all []float64
	weekVals, dayVals := map[int][]float64{}, map[int][]float64{}
	weeks, days := map[int]map[int]bool{}, map[int]map[int]bool{}
	for _, s := range samples {
		all = append(all, s.v)
		t := time.Unix(s.ts, 0).In(m.loc)
		how, hod := int(t.Weekday())*24+t.Hour(), t.Hour()
		year, week := t.ISOWeek()
		if weeks[how] == nil {
			weeks[how] = map[int]bool{}
		}
		if days[hod] == nil {
			days[hod] = map[int]bool{}
		}
		weeks[how][year*100+week] = true
		days[hod][t.YearDay()+1000*t.Year()] = true
		weekVals[how] = append(weekVals[how], s.v)
		dayVals[hod] = append(dayVals[hod], s.v)
	}
	m.global = cwMedian(all)
	for h, vals := range weekVals {
		if len(weeks[h]) >= 2 {
			m.hourOfWeek[h] = cwMedian(vals)
		}
	}
	for h, vals := range dayVals {
		if len(days[h]) >= 2 {
			m.hourOfDay[h] = cwMedian(vals)
		}
	}
}

// cwFitAnomalyModel fits the trained window at period. Seasonal medians give
// the expected shape; a level follows the residuals in time order with an
// exponential weight whose half-life AdaptationSpeed sets, so a sustained
// change in the baseline moves the expected value while one datapoint does
// not, as each step's innovation is clipped at the outlier cut-off. A
// datapoint whose innovation lies further from the innovations' median than
// OutlierFilteringStrength allows is an outlier, left out of the medians and
// the spread. With PeriodicSpikes, an outlier that recurs at the same hour of
// the day on two or more days is a periodic spike: it stays out of the
// medians but widens the band at that hour to cover it at two deviations.
func cwFitAnomalyModel(d CWAnomalyDetector, period int32) (cwAnomalyModel, bool) {
	if d.state() != "TRAINED" {
		return cwAnomalyModel{}, false
	}
	series, ok := cwAnomalyTrainingSeries(context.Background(), d, period, d.TrainedFrom, d.TrainedTo)
	if !ok {
		return cwAnomalyModel{}, false
	}
	m := cwAnomalyModel{loc: d.Configuration.location(), spikes: map[int]float64{}}
	var samples []cwAnomalySample
	for _, ts := range series.times(false) {
		if !d.Configuration.excludes(ts) {
			samples = append(samples, cwAnomalySample{ts, series.Points[ts]})
		}
	}
	if len(samples) < 2 {
		return cwAnomalyModel{}, false
	}
	cwSeasonalMedians(&m, samples)
	halfLife := d.Configuration.adaptationHalfLife().Seconds()
	innovations := func(residuals []float64, ts []int64, clip float64) ([]float64, float64) {
		out := make([]float64, len(residuals))
		level := 0.0
		for i, r := range residuals {
			e := r - level
			out[i] = e
			if clip > 0 {
				e = max(-clip, min(clip, e))
			}
			gap := ts[1] - ts[0]
			if i > 0 {
				gap = ts[i] - ts[i-1]
			}
			level += (1 - math.Pow(0.5, float64(gap)/halfLife)) * e
		}
		return out, level
	}
	residuals := make([]float64, len(samples))
	times := make([]int64, len(samples))
	for i, s := range samples {
		residuals[i], times[i] = s.v-m.seasonal(s.ts), s.ts
	}
	raw, _ := innovations(residuals, times, 0)
	center := cwMedian(raw)
	deviations := make([]float64, len(raw))
	for i, e := range raw {
		deviations[i] = math.Abs(e - center)
	}
	scale := 1.4826 * cwMedian(deviations)
	if scale == 0 {
		scale = cwStdDev(raw)
	}
	cutoff := d.Configuration.outlierCutoff() * scale
	clipped, _ := innovations(residuals, times, cutoff)
	outlier := make([]bool, len(samples))
	spikeDays := map[int]map[int]bool{}
	for i, e := range clipped {
		if scale > 0 && math.Abs(e-center) > cutoff {
			outlier[i] = true
			t := time.Unix(samples[i].ts, 0).In(m.loc)
			if spikeDays[t.Hour()] == nil {
				spikeDays[t.Hour()] = map[int]bool{}
			}
			spikeDays[t.Hour()][t.YearDay()+1000*t.Year()] = true
		}
	}
	periodic := d.PeriodicSpikes != nil && *d.PeriodicSpikes
	var kept []cwAnomalySample
	spikeSamples := map[int][]cwAnomalySample{}
	for i, s := range samples {
		if !outlier[i] {
			kept = append(kept, s)
			continue
		}
		hour := time.Unix(s.ts, 0).In(m.loc).Hour()
		if periodic && len(spikeDays[hour]) >= 2 {
			spikeSamples[hour] = append(spikeSamples[hour], s)
		}
	}
	if len(kept) < 2 {
		kept = samples
	}
	cwSeasonalMedians(&m, kept)
	for i, s := range samples {
		residuals[i] = s.v - m.seasonal(s.ts)
	}
	all, level := innovations(residuals, times, cutoff)
	keptInnovations := make([]float64, 0, len(kept))
	for i := range samples {
		if !outlier[i] || len(kept) == len(samples) {
			keptInnovations = append(keptInnovations, all[i])
		}
	}
	m.level = level
	m.spread = cwStdDev(keptInnovations)
	for hour, spikes := range spikeSamples {
		widest := 0.0
		for _, s := range spikes {
			widest = max(widest, math.Abs(s.v-m.seasonal(s.ts)-m.level))
		}
		m.spikes[hour] = widest/2 + m.spread
	}
	return m, true
}

func (m cwAnomalyModel) seasonal(ts int64) float64 {
	t := time.Unix(ts, 0).In(m.loc)
	if v, ok := m.hourOfWeek[int(t.Weekday())*24+t.Hour()]; ok {
		return v
	}
	if v, ok := m.hourOfDay[t.Hour()]; ok {
		return v
	}
	return m.global
}

func (m cwAnomalyModel) at(ts int64) (expected, spread float64) {
	spread = m.spread
	if s, ok := m.spikes[time.Unix(ts, 0).In(m.loc).Hour()]; ok {
		spread = max(spread, s)
	}
	return m.seasonal(ts) + m.level, spread
}

func cwMedian(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func cwIsBandOperator(op string) bool {
	switch op {
	case "LessThanLowerOrGreaterThanUpperThreshold", "LessThanLowerThreshold", "GreaterThanUpperThreshold":
		return true
	}
	return false
}

func cwBandBreaches(v, lower, upper float64, op string) bool {
	switch op {
	case "LessThanLowerOrGreaterThanUpperThreshold":
		return v < lower || v > upper
	case "LessThanLowerThreshold":
		return v < lower
	case "GreaterThanUpperThreshold":
		return v > upper
	}
	return false
}

// cwValidateAlarmMetrics checks a Metrics-based alarm: one watched series,
// and a ThresholdMetricId, with a band operator, that names an
// ANOMALY_DETECTION_BAND expression.
func cwValidateAlarmMetrics(metrics []CWMetricDataQuery, thresholdMetricId, op, metricName string) (code, msg string, ok bool) {
	if len(metrics) == 0 {
		if thresholdMetricId != "" || cwIsBandOperator(op) {
			return "ValidationError", "ComparisonOperators for ranges require ThresholdMetricId to be set", false
		}
		return "", "", true
	}
	if metricName != "" {
		return "InvalidParameterCombination", "MetricName and Metrics cannot be specified together.", false
	}
	if cwIsBandOperator(op) != (thresholdMetricId != "") {
		return "ValidationError", "ComparisonOperators for ranges require ThresholdMetricId to be set", false
	}
	watched := 0
	foundThreshold := false
	for _, q := range metrics {
		if (q.MetricStat == nil) == (q.Expression == "") {
			return "ValidationError", fmt.Sprintf("Exactly one of MetricStat and Expression must be set for query %s.", q.Id), false
		}
		if q.Id == thresholdMetricId {
			node, err := cwParseMetricExpression(q.Expression)
			if err != nil || node.kind != "call" || node.name != "ANOMALY_DETECTION_BAND" {
				return "ValidationError", "ThresholdMetricId must be the id of an ANOMALY_DETECTION_BAND expression.", false
			}
			foundThreshold = true
			continue
		}
		if q.Expression != "" && cwSearchUsed(q.Expression) {
			return "ValidationError", "You can't create an alarm based on a SEARCH expression.", false
		}
		if q.returns() {
			watched++
		}
	}
	if thresholdMetricId != "" && !foundThreshold {
		return "ValidationError", "ThresholdMetricId must be the id of an ANOMALY_DETECTION_BAND expression.", false
	}
	if watched != 1 {
		return "ValidationError", "Metrics list must contain exactly one metric matching the ReturnData parameter.", false
	}
	return "", "", true
}

// cwEnsureAlarmAnomalyDetectors creates the model behind each
// ANOMALY_DETECTION_BAND an alarm uses, on a metric or a metric math
// expression, as CloudWatch generates the band after the alarm is created.
func cwEnsureAlarmAnomalyDetectors(a CWAlarm) {
	byID := map[string]CWMetricDataQuery{}
	for _, q := range a.Metrics {
		byID[q.Id] = q
	}
	for _, q := range a.Metrics {
		if q.Id != a.ThresholdMetricId {
			continue
		}
		node, err := cwParseMetricExpression(q.Expression)
		if err != nil || len(node.args) == 0 || node.args[0].kind != "id" {
			continue
		}
		target, ok := byID[node.args[0].name]
		if !ok {
			continue
		}
		d := CWAnomalyDetector{Type: cwAnomalyMetricMath, MathQueries: cwQueryClosure(a.Metrics, target.Id)}
		if ms := target.MetricStat; ms != nil {
			d = CWAnomalyDetector{Type: cwAnomalySingleMetric, Namespace: ms.Metric.Namespace, MetricName: ms.Metric.MetricName, Stat: ms.Stat, Dimensions: ms.Metric.Dimensions}
		}
		if _, exists := cwAnomalyDetectors.Get(d.key()); !exists {
			cwPutAnomalyDetector(d)
		}
	}
}

// cwEvaluateMetricsAlarmStateAt evaluates an alarm on a Metrics array: the
// watched series over the latest EvaluationPeriods periods, against the
// static Threshold or, with a ThresholdMetricId, the anomaly band.
func cwEvaluateMetricsAlarmStateAt(a CWAlarm, at time.Time) (state, reason string) {
	var watched CWMetricDataQuery
	for _, q := range a.Metrics {
		if q.Id != a.ThresholdMetricId && q.returns() {
			watched = q
		}
	}
	period := cwQueryPeriod(watched, a.Metrics)
	evalPeriods := max(a.EvaluationPeriods, 1)
	now := at.UTC().Unix()
	start := (now/int64(period))*int64(period) - int64(evalPeriods)*int64(period)
	queries := make([]CWMetricDataQuery, 0, len(a.Metrics))
	yes := true
	for _, q := range a.Metrics {
		q.ReturnData = &yes
		queries = append(queries, q)
	}
	results, err := cwEvaluateMetricQueries(cwMathOptions{alarm: true, now: at}, queries, start, now+1)
	if err != nil {
		return "INSUFFICIENT_DATA", "Unable to evaluate the alarm's metric math: " + err.Error()
	}
	var series cwMetricSeries
	var upper, lower *cwMetricSeries
	for i := range results {
		switch results[i].Id {
		case watched.Id:
			series = results[i].Series
		case a.ThresholdMetricId:
			if results[i].Series.Edge == "upper" {
				upper = &results[i].Series
			} else {
				lower = &results[i].Series
			}
		}
	}
	times := series.times(true)
	evaluated, breaching := 0, 0
	for _, t := range times {
		if evaluated >= int(evalPeriods) {
			break
		}
		v := series.Points[t]
		if a.ThresholdMetricId != "" {
			hi, okHi := upper.Points[t]
			lo, okLo := lower.Points[t]
			if !okHi || !okLo {
				continue
			}
			evaluated++
			if cwBandBreaches(v, lo, hi, a.ComparisonOperator) {
				breaching++
			}
			continue
		}
		evaluated++
		if cwAlarmBreaches(v, a.Threshold, a.ComparisonOperator) {
			breaching++
		}
	}
	if evaluated == 0 {
		switch a.TreatMissingData {
		case "notBreaching":
			return "OK", "Threshold not crossed: missing data treated as not breaching"
		case "breaching":
			return "ALARM", "Threshold crossed: missing data treated as breaching"
		}
		return "INSUFFICIENT_DATA", "Insufficient Data: no datapoints were received"
	}
	if evaluated >= int(evalPeriods) && breaching == evaluated {
		if a.ThresholdMetricId != "" {
			return "ALARM", fmt.Sprintf("Thresholds Crossed: %d datapoints were %s the band", breaching, a.ComparisonOperator)
		}
		return "ALARM", fmt.Sprintf("Threshold Crossed: %d datapoints were %s the threshold (%g)", breaching, a.ComparisonOperator, a.Threshold)
	}
	return "OK", "Threshold not crossed"
}

// The watched series' period: its MetricStat's, its own Period, or that of
// the first metric it reads.
func cwQueryPeriod(q CWMetricDataQuery, all []CWMetricDataQuery) int32 {
	if q.MetricStat != nil && q.MetricStat.Period > 0 {
		return q.MetricStat.Period
	}
	if q.Period > 0 {
		return q.Period
	}
	for _, other := range all {
		if other.MetricStat != nil && other.MetricStat.Period > 0 {
			return other.MetricStat.Period
		}
	}
	return 60
}
