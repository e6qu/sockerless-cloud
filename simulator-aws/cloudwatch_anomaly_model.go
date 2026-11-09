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
// detection" describes it: a model per metric and statistic, trained on up to
// two weeks of the metric's history less the excluded time ranges, that
// accounts for hourly, daily and weekly patterns and gives an expected value
// and a band k standard deviations wide around it. The trainer re-trains a
// detector whenever its training window gains or loses datapoints.

const cwAnomalyTrainingWindow = 14 * 24 * time.Hour

type CWAnomalyExcludedRange struct {
	StartTime int64 `json:"StartTime"`
	EndTime   int64 `json:"EndTime"`
}

type CWAnomalyConfiguration struct {
	ExcludedTimeRanges []CWAnomalyExcludedRange `json:"ExcludedTimeRanges,omitempty"`
	MetricTimezone     string                   `json:"MetricTimezone,omitempty"`
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

func startCWAnomalyTrainer(srv *sim.Server) {
	srv.StartBackground("CloudWatch anomaly detector trainer", func(ctx context.Context) {
		ticker := time.NewTicker(cwAlarmEvalInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				cwTrainAnomalyDetectors(now)
			}
		}
	})
}

func cwTrainAnomalyDetectors(now time.Time) {
	for _, d := range cwAnomalyDetectors.List() {
		cwTrainAnomalyDetector(cwAnomalyKey(d.Namespace, d.MetricName, d.Stat, d.Dimensions), now)
	}
}

// A detector with no history stays PENDING_TRAINING; one whose window holds
// fewer than two datapoints has no spread to learn and is
// TRAINED_INSUFFICIENT_DATA.
func cwTrainAnomalyDetector(key string, now time.Time) {
	d, ok := cwAnomalyDetectors.Get(key)
	if !ok {
		return
	}
	from := now.Add(-cwAnomalyTrainingWindow).Unix()
	to := now.Unix() + 1
	data, _ := cwMetrics.Get(metricsKey(d.Namespace, d.MetricName, d.Dimensions))
	points := 0
	var newest int64
	for _, p := range data {
		ts := int64(p.Timestamp)
		if ts < from || ts >= to || d.Configuration.excludes(ts) {
			continue
		}
		points++
		newest = max(newest, ts)
	}
	if points == d.TrainedPoints && newest == d.TrainedNewest && d.StateValue != "" {
		return
	}
	cwAnomalyDetectors.Update(key, func(d *CWAnomalyDetector) {
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
	spread     float64
}

// cwFitAnomalyModel fits the trained window at period: the median of the
// values seen at the same hour of the week (two or more weeks of it), else
// at the same hour of the day (two or more days), else overall, with the
// residuals' median absolute deviation, scaled to a standard deviation, as
// the spread. The medians filter outliers out of the training data.
func cwFitAnomalyModel(d CWAnomalyDetector, period int32) (cwAnomalyModel, bool) {
	if d.state() != "TRAINED" {
		return cwAnomalyModel{}, false
	}
	series := cwStatSeries(CWMetricRef{Namespace: d.Namespace, MetricName: d.MetricName, Dimensions: d.Dimensions}, period, d.Stat, d.TrainedFrom, d.TrainedTo)
	m := cwAnomalyModel{loc: d.Configuration.location(), hourOfWeek: map[int]float64{}, hourOfDay: map[int]float64{}}
	type sample struct {
		ts int64
		v  float64
	}
	var samples []sample
	var all []float64
	weekVals, dayVals := map[int][]float64{}, map[int][]float64{}
	weeks, days := map[int]map[int]bool{}, map[int]map[int]bool{}
	for ts, v := range series.Points {
		if d.Configuration.excludes(ts) {
			continue
		}
		samples = append(samples, sample{ts, v})
		all = append(all, v)
		t := time.Unix(ts, 0).In(m.loc)
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
		weekVals[how] = append(weekVals[how], v)
		dayVals[hod] = append(dayVals[hod], v)
	}
	if len(samples) < 2 {
		return cwAnomalyModel{}, false
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
	residuals := make([]float64, 0, len(samples))
	for _, s := range samples {
		expected, _ := m.at(s.ts)
		residuals = append(residuals, s.v-expected)
	}
	center := cwMedian(residuals)
	deviations := make([]float64, 0, len(residuals))
	for _, r := range residuals {
		deviations = append(deviations, math.Abs(r-center))
	}
	m.spread = 1.4826 * cwMedian(deviations)
	if m.spread == 0 {
		m.spread = cwStdDev(residuals)
	}
	return m, true
}

func (m cwAnomalyModel) at(ts int64) (expected, spread float64) {
	t := time.Unix(ts, 0).In(m.loc)
	if v, ok := m.hourOfWeek[int(t.Weekday())*24+t.Hour()]; ok {
		return v, m.spread
	}
	if v, ok := m.hourOfDay[t.Hour()]; ok {
		return v, m.spread
	}
	return m.global, m.spread
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
// ANOMALY_DETECTION_BAND an alarm uses, as CloudWatch generates the band
// after the alarm is created.
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
		if !ok || target.MetricStat == nil {
			continue
		}
		ms := target.MetricStat
		key := cwAnomalyKey(ms.Metric.Namespace, ms.Metric.MetricName, ms.Stat, ms.Metric.Dimensions)
		if _, exists := cwAnomalyDetectors.Get(key); !exists {
			cwAnomalyDetectors.Put(key, CWAnomalyDetector{Namespace: ms.Metric.Namespace, MetricName: ms.Metric.MetricName, Stat: ms.Stat, Dimensions: ms.Metric.Dimensions})
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
	results, err := cwEvaluateMetricQueries(queries, start, now+1)
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
			if upper == nil {
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
