package main

import (
	"fmt"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// SERVICE_QUOTA answers the quota a usage metric measures, applied to the
// account, at each of the metric's timestamps.
func (ev *cwMetricEvaluator) serviceQuota(n *cwMathNode, q CWMetricDataQuery) (cwMathValue, error) {
	if len(n.args) != 1 {
		return cwMathValue{}, fmt.Errorf("SERVICE_QUOTA takes one usage metric")
	}
	v, err := ev.eval(n.args[0], q)
	if err != nil {
		return cwMathValue{}, err
	}
	if !v.isSingle() || v.series[0].Metric == nil {
		return cwMathValue{}, fmt.Errorf("SERVICE_QUOTA takes a usage metric, not the result of an expression")
	}
	usage := v.series[0]
	quota, ok := sqQuotaForUsageMetric(*usage.Metric)
	if !ok {
		return cwMathValue{}, fmt.Errorf("%s %s is not a usage metric a service quota measures", usage.Metric.Namespace, usage.Metric.MetricName)
	}
	value := sqQuotaDefault(quota, awsRegion())
	if sqApplied != nil {
		value = sqAppliedQuota(awsAccountID(), awsRegion(), quota)
	}
	out := cwMetricSeries{Label: quota.QuotaName, Period: usage.Period, Points: map[int64]float64{}, Status: usage.Status, Messages: usage.Messages}
	for t := range usage.Points {
		out.Points[t] = value
	}
	return cwSeriesValue(out), nil
}

// DB_PERF_INSIGHTS reads Performance Insights metrics of a database by its
// resource ID, at the periods and within the data point counts the User
// Guide's metric math table states.
func (ev *cwMetricEvaluator) dbPerfInsights(n *cwMathNode, q CWMetricDataQuery) (cwMathValue, error) {
	if len(n.args) != 3 || n.args[0].kind != "str" || n.args[1].kind != "str" {
		return cwMathValue{}, fmt.Errorf("DB_PERF_INSIGHTS takes a service, a resource ID and a metric or an array of metrics, each in single quotes")
	}
	service, resource := n.args[0].name, n.args[1].name
	var metrics []string
	switch arg := n.args[2]; arg.kind {
	case "str":
		metrics = []string{arg.name}
	case "array":
		for _, item := range arg.args {
			if item.kind != "str" {
				return cwMathValue{}, fmt.Errorf("DB_PERF_INSIGHTS takes metric names in single quotes")
			}
			metrics = append(metrics, item.name)
		}
	default:
		return cwMathValue{}, fmt.Errorf("DB_PERF_INSIGHTS takes a metric or an array of metrics")
	}
	if service != "RDS" && service != "DOCDB" {
		return cwMathValue{}, fmt.Errorf("DB_PERF_INSIGHTS takes the service RDS or DOCDB, not %s", service)
	}
	instance, ok := piInstanceByResource(resource)
	if service != "RDS" || !ok || !instance.PerformanceInsightsEnabled {
		return cwMathValue{}, fmt.Errorf("%s does not name a database with Performance Insights turned on", resource)
	}
	engine, ok := rdsInstanceEngine(instance)
	if !ok {
		return cwMathValue{}, fmt.Errorf("the engine of %s has no Performance Insights", resource)
	}
	period := int64(ev.periodOf(q))
	limit := int64(1440)
	switch period {
	case 1, 10, 30:
		limit = 1080
	case 60, 300, 3600, 86400:
	default:
		return cwMathValue{}, fmt.Errorf("DB_PERF_INSIGHTS supports the periods 1, 10, 30, 60, 300, 3600 and 86400 seconds, not %d", period)
	}
	start := (ev.start / period) * period
	if (ev.end-start)/period > limit {
		return cwMathValue{}, fmt.Errorf("DB_PERF_INSIGHTS retrieves at most %d data points at a period of %d seconds", limit, period)
	}
	var series []cwMetricSeries
	for _, metric := range metrics {
		s, err := piMathSeries(instance, engine.Family, metric, time.Unix(start, 0), time.Unix(ev.end, 0), period)
		if err != nil {
			return cwMathValue{}, err
		}
		series = append(series, s)
	}
	if n.args[2].kind == "array" {
		return cwArrayValue(series), nil
	}
	return cwSeriesValue(series[0]), nil
}

// piMathSeries reads one Performance Insights metric into period buckets.
func piMathSeries(instance RDSInstance, family dbengine.Family, metric string, start, end time.Time, period int64) (cwMetricSeries, error) {
	out := cwMetricSeries{Label: metric, Period: int32(period), Points: map[int64]float64{}}
	if stat, isLoad := piLoadMetric(metric); isLoad {
		minutes := piSampledMinutes(instance.DbiResourceId, start, end)
		for b, bucket := range piLoadBuckets(minutes, start, end, period, nil, nil) {
			if bucket.seen > 0 {
				out.Points[start.Unix()+b*period] = piReduce(stat, bucket.counts[""], bucket.seen)
			}
		}
		return out, nil
	}
	name, stat, ok := piCounterStat(metric)
	counter, known := piFindCounter(family, name)
	if !ok || !known {
		return out, fmt.Errorf("%s is not a Performance Insights metric of %s", metric, instance.DbiResourceId)
	}
	if period < 60 {
		return out, fmt.Errorf("%s is collected once a minute, so its period is at least 60 seconds", metric)
	}
	for b, v := range piCounterBuckets(instance.DbiResourceId, counter, stat, start, end, period) {
		out.Points[start.Unix()+b*period] = v
	}
	return out, nil
}
