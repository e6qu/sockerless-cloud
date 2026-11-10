package aws_sdk_test

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mathWindow is a run of whole minutes ending well in the past, so every
// datapoint is settled before the queries read it.
type mathWindow struct {
	t0 time.Time
	n  int
}

func newMathWindow(n int) mathWindow {
	return mathWindow{t0: time.Now().UTC().Truncate(time.Minute).Add(-time.Duration(n+10) * time.Minute), n: n}
}

func (w mathWindow) at(i int) time.Time { return w.t0.Add(time.Duration(i) * time.Minute) }

func (w mathWindow) end() time.Time { return w.at(w.n) }

// put records values[i] at minute i of the window; NaN leaves minute i
// without a datapoint, as the User Guide's tables mark with "-".
func (w mathWindow) put(t *testing.T, ns, metric string, dims []cwtypes.Dimension, values ...float64) {
	t.Helper()
	var data []cwtypes.MetricDatum
	for i, v := range values {
		if math.IsNaN(v) {
			continue
		}
		data = append(data, cwtypes.MetricDatum{MetricName: aws.String(metric), Dimensions: dims, Value: aws.Float64(v), Timestamp: aws.Time(w.at(i))})
	}
	_, err := cloudwatchClient().PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(ns), MetricData: data})
	require.NoError(t, err)
}

// slots lays a result's values on the window's minutes, NaN where it has no
// datapoint.
func (w mathWindow) slots(r cwtypes.MetricDataResult) []float64 {
	out := make([]float64, w.n)
	for i := range out {
		out[i] = math.NaN()
	}
	for i, ts := range r.Timestamps {
		k := int(ts.Sub(w.t0) / time.Minute)
		if k >= 0 && k < w.n {
			out[k] = r.Values[i]
		}
	}
	return out
}

func (w mathWindow) query(t *testing.T, queries ...cwtypes.MetricDataQuery) map[string][]cwtypes.MetricDataResult {
	t.Helper()
	out, err := cloudwatchClient().GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(w.t0), EndTime: aws.Time(w.end()), ScanBy: cwtypes.ScanByTimestampAscending, MetricDataQueries: queries,
	})
	require.NoError(t, err)
	byID := map[string][]cwtypes.MetricDataResult{}
	for _, r := range out.MetricDataResults {
		byID[aws.ToString(r.Id)] = append(byID[aws.ToString(r.Id)], r)
	}
	return byID
}

func metricQuery(id, ns, metric string, returnData bool) cwtypes.MetricDataQuery {
	return cwtypes.MetricDataQuery{Id: aws.String(id), ReturnData: aws.Bool(returnData), MetricStat: &cwtypes.MetricStat{
		Metric: &cwtypes.Metric{Namespace: aws.String(ns), MetricName: aws.String(metric)}, Period: aws.Int32(60), Stat: aws.String("Average"),
	}}
}

func exprQuery(id, expression string) cwtypes.MetricDataQuery {
	return cwtypes.MetricDataQuery{Id: aws.String(id), Expression: aws.String(expression)}
}

var dash = math.NaN()

func assertSlots(t *testing.T, want, got []float64, msg string) {
	t.Helper()
	require.Len(t, got, len(want), msg)
	for i := range want {
		if math.IsNaN(want[i]) {
			assert.True(t, math.IsNaN(got[i]), "%s: minute %d holds %v, want no datapoint", msg, i, got[i])
			continue
		}
		assert.InDelta(t, want[i], got[i], 1e-9, "%s: minute %d", msg, i)
	}
}

// TestCloudWatch_MetricMathComparisonOperators reproduces the User Guide's
// comparison and logical operator table: a timestamp only one series has
// counts the other's missing value as 0.
func TestCloudWatch_MetricMathComparisonOperators(t *testing.T) {
	t.Parallel()
	ns := uniquename.New("Custom/MathCompare")
	w := newMathWindow(4)
	w.put(t, ns, "One", nil, 30, 20, 0, 0)
	w.put(t, ns, "Two", nil, 20, dash, 20, dash)
	got := w.query(t,
		metricQuery("metric1", ns, "One", false), metricQuery("metric2", ns, "Two", false),
		exprQuery("lt", "(metric1 < metric2)"),
		exprQuery("ge", "(metric1 >= 30)"),
		exprQuery("both", "(metric1 > 15 AND metric2 > 15)"),
		exprQuery("either", "metric1 > 25 || metric2 > 15"),
	)
	assertSlots(t, []float64{0, 0, 1, 0}, w.slots(got["lt"][0]), "metric1 < metric2")
	assertSlots(t, []float64{1, 0, 0, 0}, w.slots(got["ge"][0]), "metric1 >= 30")
	assertSlots(t, []float64{1, 0, 0, 0}, w.slots(got["both"][0]), "metric1 > 15 AND metric2 > 15")
	assertSlots(t, []float64{1, 0, 1, 0}, w.slots(got["either"][0]), "metric1 > 25 || metric2 > 15")
}

// TestCloudWatch_MetricMathIf reproduces the User Guide's IF tables.
func TestCloudWatch_MetricMathIf(t *testing.T) {
	t.Parallel()
	ns := uniquename.New("Custom/MathIf")
	w := newMathWindow(5)
	w.put(t, ns, "One", nil, 1, 1, 0, 0, dash)
	w.put(t, ns, "Two", nil, 30, dash, 0, 0, 30)
	w.put(t, ns, "Three", nil, 0, 0, 20, dash, 20)
	got := w.query(t,
		metricQuery("metric1", ns, "One", false), metricQuery("metric2", ns, "Two", false), metricQuery("metric3", ns, "Three", false),
		exprQuery("tts", "IF(metric1, metric2, metric3)"),
		exprQuery("sts", "IF(metric1, 5, metric3)"),
		exprQuery("tss", "IF(metric1, metric2, 5)"),
		exprQuery("low", "IF(metric3 < 10, metric3)"),
		exprQuery("scalar", "IF(0, metric2, metric3)"),
	)
	assertSlots(t, []float64{30, 0, 20, 0, dash}, w.slots(got["tts"][0]), "IF(metric1, metric2, metric3)")
	assertSlots(t, []float64{5, 5, 20, dash, dash}, w.slots(got["sts"][0]), "IF(metric1, scalar2, metric3)")
	assertSlots(t, []float64{30, 0, 5, 5, dash}, w.slots(got["tss"][0]), "IF(metric1, metric2, scalar3)")
	assertSlots(t, []float64{0, 0, dash, dash, dash}, w.slots(got["low"][0]), "IF(metric3 < 10, metric3)")
	assertSlots(t, []float64{0, 0, 20, dash, 20}, w.slots(got["scalar"][0]), "IF(scalar1, metric2, metric3)")
}

// TestCloudWatch_MetricMathFunctions drives the functions of the User
// Guide's "Functions supported for metric math" table.
func TestCloudWatch_MetricMathFunctions(t *testing.T) {
	t.Parallel()
	ns := uniquename.New("Custom/MathFunctions")
	w := newMathWindow(6)
	w.put(t, ns, "Errors", nil, 1, 3, dash, dash, 9, 4)
	w.put(t, ns, "Requests", nil, 10, 20, 30, 40, 50, 60)
	errs, reqs := metricQuery("errors", ns, "Errors", false), metricQuery("requests", ns, "Requests", false)
	empty := cwtypes.MetricDataQuery{Id: aws.String("empty"), ReturnData: aws.Bool(false), MetricStat: &cwtypes.MetricStat{
		Metric: &cwtypes.Metric{Namespace: aws.String(ns), MetricName: aws.String("Absent")}, Period: aws.Int32(60), Stat: aws.String("Sum"),
	}}
	got := w.query(t, errs, reqs, empty,
		exprQuery("diff", "DIFF(errors)"),
		exprQuery("rate", "RATE(errors)"),
		exprQuery("difftime", "DIFF_TIME(errors)"),
		exprQuery("running", "RUNNING_SUM(errors)"),
		exprQuery("zero", "FILL(errors, 0)"),
		exprQuery("repeat", "FILL(errors, REPEAT)"),
		exprQuery("linear", "FILL(errors, LINEAR)"),
		exprQuery("fillts", "FILL(errors, requests)"),
		exprQuery("arith", "errors + requests"),
		exprQuery("avgarr", "AVG([errors, requests])"),
		exprQuery("maxarr", "MAX(METRICS())"),
		exprQuery("count", "DATAPOINT_COUNT(METRICS())"),
		exprQuery("rate2", "SUM(errors) / DATAPOINT_COUNT(errors) + 0 * requests"),
		exprQuery("period", "requests / PERIOD(requests)"),
		exprQuery("series", "TIME_SERIES(MAX(errors))"),
		exprQuery("minute", "MINUTE(requests)"),
		exprQuery("epoch", "EPOCH(requests)"),
		exprQuery("top", "FIRST(SORT(METRICS(), AVG, DESC))"),
		exprQuery("bottom", "LAST(SORT(REMOVE_EMPTY(METRICS()), MAX, DESC))"),
		exprQuery("sliced", "SLICE(SORT(METRICS(), SUM, DESC), 1)"),
		exprQuery("nonempty", "METRIC_COUNT(REMOVE_EMPTY(METRICS())) + 0 * requests"),
		exprQuery("named", "SUM(METRICS(\"err\"))"),
		exprQuery("log", "LOG10(requests)"),
		exprQuery("neg", "-requests ^ 2"),
	)
	assertSlots(t, []float64{dash, 2, dash, dash, 6, -5}, w.slots(got["diff"][0]), "DIFF")
	assertSlots(t, []float64{dash, 2.0 / 60, dash, dash, 6.0 / 180, -5.0 / 60}, w.slots(got["rate"][0]), "RATE")
	assertSlots(t, []float64{dash, 60, dash, dash, 180, 60}, w.slots(got["difftime"][0]), "DIFF_TIME")
	assertSlots(t, []float64{1, 4, dash, dash, 13, 17}, w.slots(got["running"][0]), "RUNNING_SUM")
	assertSlots(t, []float64{1, 3, 0, 0, 9, 4}, w.slots(got["zero"][0]), "FILL with a number")
	assertSlots(t, []float64{1, 3, 3, 3, 9, 4}, w.slots(got["repeat"][0]), "FILL with REPEAT")
	assertSlots(t, []float64{1, 3, 5, 7, 9, 4}, w.slots(got["linear"][0]), "FILL with LINEAR")
	assertSlots(t, []float64{1, 3, 30, 40, 9, 4}, w.slots(got["fillts"][0]), "FILL with a time series")
	assertSlots(t, []float64{11, 23, 30, 40, 59, 64}, w.slots(got["arith"][0]), "missing values count as 0")
	assertSlots(t, []float64{5.5, 11.5, 15, 20, 29.5, 32}, w.slots(got["avgarr"][0]), "AVG of an array counts missing values as 0")
	assertSlots(t, []float64{10, 20, 30, 40, 50, 60}, w.slots(got["maxarr"][0]), "MAX of an array")
	assertSlots(t, []float64{2, 2, 1, 1, 2, 2}, w.slots(got["count"][0]), "DATAPOINT_COUNT of an array")
	assertSlots(t, []float64{4.25, 4.25, 4.25, 4.25, 4.25, 4.25}, w.slots(got["rate2"][0]), "SUM / DATAPOINT_COUNT")
	assertSlots(t, []float64{10.0 / 60, 20.0 / 60, 30.0 / 60, 40.0 / 60, 50.0 / 60, 1}, w.slots(got["period"][0]), "PERIOD")
	assertSlots(t, []float64{9, 9, 9, 9, 9, 9}, w.slots(got["series"][0]), "TIME_SERIES")
	minutes := make([]float64, 6)
	epochs := make([]float64, 6)
	for i := range minutes {
		minutes[i] = float64(w.at(i).Minute())
		epochs[i] = float64(w.at(i).Unix())
	}
	assertSlots(t, minutes, w.slots(got["minute"][0]), "MINUTE")
	assertSlots(t, epochs, w.slots(got["epoch"][0]), "EPOCH")
	assertSlots(t, []float64{10, 20, 30, 40, 50, 60}, w.slots(got["top"][0]), "FIRST of SORT by AVG DESC")
	assertSlots(t, []float64{1, 3, dash, dash, 9, 4}, w.slots(got["bottom"][0]), "LAST of SORT by MAX DESC")
	require.Len(t, got["sliced"], 2, "SLICE from 1 drops the series with the highest SUM")
	assertSlots(t, []float64{1, 3, dash, dash, 9, 4}, w.slots(got["sliced"][0]), "SLICE")
	assertSlots(t, []float64{2, 2, 2, 2, 2, 2}, w.slots(got["nonempty"][0]), "REMOVE_EMPTY drops the series without datapoints")
	assertSlots(t, []float64{1, 3, dash, dash, 9, 4}, w.slots(got["named"][0]), "METRICS with a string")
	assertSlots(t, []float64{1, math.Log10(20), math.Log10(30), math.Log10(40), math.Log10(50), math.Log10(60)}, w.slots(got["log"][0]), "LOG10")
	assertSlots(t, []float64{-100, -400, -900, -1600, -2500, -3600}, w.slots(got["neg"][0]), "unary subtraction")

	_, err := cloudwatchClient().GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(w.t0), EndTime: aws.Time(w.end()),
		MetricDataQueries: []cwtypes.MetricDataQuery{errs, exprQuery("e1", "AVG(errors)")},
	})
	requireAWSErrorCode(t, err, "ValidationError")
	_, err = cloudwatchClient().GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(w.t0), EndTime: aws.Time(w.end()),
		MetricDataQueries: []cwtypes.MetricDataQuery{errs, exprQuery("e1", "abs(errors)")},
	})
	requireAWSErrorCode(t, err, "ValidationError")
}

// TestCloudWatch_MetricMathArrayLabels expects each series of an array
// labelled "Expression-Label Metric-Label".
func TestCloudWatch_MetricMathArrayLabels(t *testing.T) {
	t.Parallel()
	ns := uniquename.New("Custom/MathLabels")
	w := newMathWindow(2)
	w.put(t, ns, "Errors", nil, 1, dash)
	m1 := metricQuery("m1", ns, "Errors", false)
	m1.Label = aws.String("Errors")
	filled := exprQuery("e1", "FILL(METRICS(), 0)")
	filled.Label = aws.String("Filled With 0:")
	got := w.query(t, m1, filled)
	require.Len(t, got["e1"], 1)
	assert.Equal(t, "Filled With 0: Errors", aws.ToString(got["e1"][0].Label))
	assertSlots(t, []float64{1, 0}, w.slots(got["e1"][0]), "FILL(METRICS(), 0)")
}

// TestCloudWatch_MetricMathSearch follows "CloudWatch search expression
// syntax": tokenization, partial and exact matches, the metric schema,
// property designators and Boolean operators.
func TestCloudWatch_MetricMathSearch(t *testing.T) {
	t.Parallel()
	ns := uniquename.New("Custom/Search")
	w := newMathWindow(2)
	inst := func(id string) []cwtypes.Dimension {
		return []cwtypes.Dimension{{Name: aws.String("InstanceId"), Value: aws.String(id)}}
	}
	w.put(t, ns, "CustomCount1", inst("i-aaa"), 1, 2)
	w.put(t, ns, "CustomCount1", inst("i-bbb"), 10, 20)
	w.put(t, ns, "SDBFailure", inst("i-aaa"), 5, 5)
	w.put(t, ns, "Network-Errors-2", nil, 7, 7)
	search := func(term, stat string) []cwtypes.MetricDataResult {
		q := exprQuery("s1", fmt.Sprintf("SEARCH('%s', '%s')", term, stat))
		q.Period = aws.Int32(60)
		return w.query(t, q)["s1"]
	}
	schema := "{" + ns + ",InstanceId}"
	assert.Len(t, search(schema+` MetricName="CustomCount1"`, "Average"), 2)
	assert.Len(t, search(schema+" count", "Sum"), 2, "a single token matches case-insensitively")
	assert.Len(t, search(schema+" COUNT", "Sum"), 2)
	assert.Empty(t, search(schema+" couNT", "Sum"), "couNT tokenizes as cou and NT")
	assert.Len(t, search(schema+" CustomCount", "Sum"), 2, "a composite token matches case-sensitively")
	assert.Empty(t, search(schema+" customcount", "Sum"))
	assert.Len(t, search(schema+" sdb", "Sum"), 1, "SDBFailure tokenizes as sdb and failure")
	assert.Len(t, search(schema+` MetricName="CustomCount1" NOT i-aaa`, "Sum"), 1)
	assert.Len(t, search(schema+" (failure OR i-bbb)", "Sum"), 2)
	assert.Len(t, search("{"+ns+"} network/errors", "Sum"), 1, "delimiters in a term find composite tokens")
	assert.Empty(t, search(`{`+ns+`} "Network_Errors_2"`, "Sum"), "an exact match needs the exact characters")
	hits := search(schema+` InstanceId="i-bbb" MetricName="CustomCount1"`, "Sum")
	require.Len(t, hits, 1)
	assertSlots(t, []float64{10, 20}, w.slots(hits[0]), "SEARCH returns the statistic")

	sum := exprQuery("total", fmt.Sprintf(`SUM(SEARCH('%s MetricName="CustomCount1"', 'Sum'))`, schema))
	sum.Period = aws.Int32(60)
	assertSlots(t, []float64{11, 22}, w.slots(w.query(t, sum)["total"][0]), "SUM of a SEARCH")

	_, err := cloudwatchClient().PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(uniquename.New("sdk-search-alarm")),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Threshold:          aws.Float64(1),
		Metrics: []cwtypes.MetricDataQuery{{Id: aws.String("s1"), ReturnData: aws.Bool(true), Period: aws.Int32(60),
			Expression: aws.String(fmt.Sprintf("SUM(SEARCH('%s', 'Sum'))", schema))}},
	})
	requireAWSErrorCode(t, err, "ValidationError")
}

// TestCloudWatch_MetricMathRefusalMessages expects the refusals CloudWatch
// answers GetMetricData with, as Grafana users quoted them: an unknown id, a
// scalar result, and operand types a function does not take.
func TestCloudWatch_MetricMathRefusalMessages(t *testing.T) {
	t.Parallel()
	ns := uniquename.New("Custom/MathRefusals")
	w := newMathWindow(2)
	host := func(h string) []cwtypes.Dimension {
		return []cwtypes.Dimension{{Name: aws.String("Host"), Value: aws.String(h)}}
	}
	w.put(t, ns, "Requests", host("a"), 1, 2)
	w.put(t, ns, "Requests", host("b"), 3, 4)
	requests := metricQuery("requests", ns, "Requests", false)
	requests.MetricStat.Metric.Dimensions = host("a")
	search := exprQuery("hosts", fmt.Sprintf(`SEARCH('{%s,Host} MetricName="Requests"', 'Sum')`, ns))
	search.Period = aws.Int32(60)
	search.ReturnData = aws.Bool(false)
	refuse := func(message string, queries ...cwtypes.MetricDataQuery) {
		t.Helper()
		_, err := cloudwatchClient().GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
			StartTime: aws.Time(w.t0), EndTime: aws.Time(w.end()), MetricDataQueries: queries,
		})
		requireAWSError(t, err, "ValidationError", message)
	}
	refuse("Error in expression 'e1': ID 't5xx' not found", requests, exprQuery("e1", "FILL(t5xx, 0)"))
	refuse("Error in expression 'e1': The return value of the expression must be TimeSeries or Array[TimeSeries]", requests, exprQuery("e1", "SUM(requests)"))
	refuse("Error in expression 'e1': Unsupported operand type(s) for MAX: '[Array[TimeSeries, Scalar]]'", requests, exprQuery("e1", "MAX([requests, 1])"))
	refuse("Error in expression 'e1': Unsupported operand type(s) for PERIOD: '[Array[TimeSeries]]'", search, exprQuery("e1", "requests / PERIOD(hosts)"), requests)
	refuse("Error in expression 'e1': Unsupported operand type(s) for ANOMALY_DETECTION_BAND: '[Array[TimeSeries], Scalar]'", search, exprQuery("e1", "ANOMALY_DETECTION_BAND(hosts, 2)"))
	insights := exprQuery("q1", fmt.Sprintf(`SELECT ANOMALY_DETECTION_BAND(Requests) FROM "%s"`, ns))
	insights.Period = aws.Int32(60)
	refuse("Error in expression 'q1': Invalid syntax. 'ANOMALY_DETECTION_BAND' is not a valid aggregate function", insights)

	got := w.query(t, requests, exprQuery("e1", "requests / PERIOD(requests)"))
	assertSlots(t, []float64{1.0 / 60, 2.0 / 60}, w.slots(got["e1"][0]), "PERIOD of a metric")
}

// TestCloudWatch_MetricsInsightsQuery runs a Metrics Insights query with
// SCHEMA, WHERE, GROUP BY, ORDER BY and LIMIT, and feeds its result to a
// math expression.
func TestCloudWatch_MetricsInsightsQuery(t *testing.T) {
	t.Parallel()
	ns := uniquename.New("Custom/Insights")
	w := newMathWindow(2)
	dims := func(lb, az string) []cwtypes.Dimension {
		return []cwtypes.Dimension{{Name: aws.String("LoadBalancer"), Value: aws.String(lb)}, {Name: aws.String("AvailabilityZone"), Value: aws.String(az)}}
	}
	w.put(t, ns, "RequestCount", dims("app/lb-1", "a"), 1, 2)
	w.put(t, ns, "RequestCount", dims("app/lb-1", "b"), 10, 20)
	w.put(t, ns, "RequestCount", dims("app/lb-2", "a"), 100, 200)
	w.put(t, ns, "RequestCount", []cwtypes.Dimension{{Name: aws.String("LoadBalancer"), Value: aws.String("app/lb-1")}}, 1000, 1000)
	insights := func(sql string) []cwtypes.MetricDataResult {
		q := exprQuery("q1", sql)
		q.Period = aws.Int32(60)
		return w.query(t, q)["q1"]
	}
	from := fmt.Sprintf(`FROM SCHEMA("%s", LoadBalancer, AvailabilityZone)`, ns)
	all := insights("SELECT SUM(RequestCount) " + from)
	require.Len(t, all, 1, "without GROUP BY the query answers one time series")
	assertSlots(t, []float64{111, 222}, w.slots(all[0]), "SUM over the exact dimension set")

	byAZ := insights("SELECT SUM(RequestCount) " + from + " WHERE LoadBalancer = 'app/lb-1' GROUP BY AvailabilityZone ORDER BY MAX() DESC")
	require.Len(t, byAZ, 2)
	assert.Equal(t, "b", aws.ToString(byAZ[0].Label))
	assertSlots(t, []float64{10, 20}, w.slots(byAZ[0]), "the group with the highest MAX first")

	top := insights("select avg(RequestCount) " + from + " where LoadBalancer != 'app/lb-1' group by LoadBalancer limit 1")
	require.Len(t, top, 1)
	assertSlots(t, []float64{100, 200}, w.slots(top[0]), "keywords are case-insensitive")

	anyDims := insights(fmt.Sprintf(`SELECT COUNT(RequestCount) FROM "%s" GROUP BY AvailabilityZone`, ns))
	labels := map[string][]float64{}
	for _, r := range anyDims {
		labels[aws.ToString(r.Label)] = w.slots(r)
	}
	require.Len(t, labels, 3)
	assertSlots(t, []float64{2, 2}, labels["a"], "COUNT counts the observations of every matched metric")
	assertSlots(t, []float64{1, 1}, labels["Other"], "a metric without the GROUP BY key falls in Other")

	q := exprQuery("q1", "SELECT SUM(RequestCount) "+from)
	q.Period, q.ReturnData = aws.Int32(60), aws.Bool(false)
	got := w.query(t, q, exprQuery("half", "q1 / 2"))
	assertSlots(t, []float64{55.5, 111}, w.slots(got["half"][0]), "a math expression reads a Metrics Insights result")
}

// TestCloudWatch_MetricMathInsightRuleMetric reads a Contributor Insights
// rule's report through INSIGHT_RULE_METRIC.
func TestCloudWatch_MetricMathInsightRuleMetric(t *testing.T) {
	t.Parallel()
	client, logs := cloudwatchClient(), cwLogsClient()
	group := uniquename.New("/math/insight")
	rule := uniquename.New("sdk-math-rule")
	def := `{"Schema":{"Name":"CloudWatchLogRule","Version":1},"LogGroupNames":["` + group + `"],"LogFormat":"JSON",` +
		`"Contribution":{"Keys":["$.ip"],"ValueOf":"$.bytes"},"AggregateOn":"Sum"}`
	_, err := client.PutInsightRule(ctx, &cloudwatch.PutInsightRuleInput{RuleName: aws.String(rule), RuleDefinition: aws.String(def)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteInsightRules(ctx, &cloudwatch.DeleteInsightRulesInput{RuleNames: []string{rule}})
	})
	_, err = logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	})
	_, err = logs.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String("s")})
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = logs.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String("s"), LogEvents: []cwlogtypes.InputLogEvent{
		{Message: aws.String(`{"ip":"10.0.0.1","bytes":100}`), Timestamp: aws.Int64(now.UnixMilli())},
		{Message: aws.String(`{"ip":"10.0.0.2","bytes":50}`), Timestamp: aws.Int64(now.UnixMilli())},
	}})
	require.NoError(t, err)

	start := now.Truncate(5 * time.Minute)
	sum := exprQuery("e1", fmt.Sprintf(`INSIGHT_RULE_METRIC("%s", "Sum")`, rule))
	sum.Period = aws.Int32(300)
	unique := exprQuery("e2", fmt.Sprintf(`INSIGHT_RULE_METRIC("%s", "UniqueContributors")`, rule))
	unique.Period = aws.Int32(300)
	out, err := client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(start), EndTime: aws.Time(start.Add(5 * time.Minute)), MetricDataQueries: []cwtypes.MetricDataQuery{sum, unique},
	})
	require.NoError(t, err)
	require.Len(t, out.MetricDataResults, 2)
	assert.Equal(t, []float64{150}, out.MetricDataResults[0].Values)
	assert.Equal(t, []float64{2}, out.MetricDataResults[1].Values)
}

// TestCloudWatch_MetricMathLambdaDataSource queries a custom data source: the
// function receives the GetMetricData event and its MetricDataResults come
// back, datapoints off the requested period grid discarded.
func TestCloudWatch_MetricMathLambdaDataSource(t *testing.T) {
	t.Parallel()
	cw, lambdaAPI := cloudwatchClient(), lambdaClient()
	name := uniquename.New("cw-data-source")
	source := `exports.handler = async (event) => {
  if (event.EventType !== "GetMetricData") { return { Error: { Code: "Validation", Value: "unexpected " + event.EventType } }; }
  const r = event.GetMetricDataRequest;
  if (r.Arguments[0] === "deny") { return { Error: { Code: "Forbidden", Value: "no access" } }; }
  if (r.Arguments[0] === "bad") { return { Error: { Code: "Validation", Value: "Invalid Prometheus cluster" } }; }
  const ts = [], vs = [];
  for (let t = r.StartTime; t < r.EndTime; t += r.Period) { ts.push(t); vs.push(r.Arguments[1] * (ts.length)); }
  ts.push(r.StartTime + 7); vs.push(999);
  return { MetricDataResults: [{ StatusCode: "Complete", Label: r.Arguments[0], Timestamps: ts, Values: vs }] };
};`
	_, err := lambdaAPI.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(name),
		Role:         aws.String(lambdaRole(t)),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaNodeSourceZip(t, source)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lambdaAPI.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(name)})
	})

	w := newMathWindow(3)
	run := func(args string) (*cloudwatch.GetMetricDataOutput, error) {
		q := exprQuery("l1", fmt.Sprintf(`LAMBDA("%s", %s)`, name, args))
		q.Period = aws.Int32(60)
		return cw.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
			StartTime: aws.Time(w.t0), EndTime: aws.Time(w.end()), ScanBy: cwtypes.ScanByTimestampAscending, MetricDataQueries: []cwtypes.MetricDataQuery{q},
		})
	}
	out, err := run(`"query", 2`)
	require.NoError(t, err)
	require.Len(t, out.MetricDataResults, 1)
	assert.Equal(t, cwtypes.StatusCodeForbidden, out.MetricDataResults[0].StatusCode, "a function that does not grant the data source principal is not invoked")

	_, err = lambdaAPI.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName:  aws.String(name),
		StatementId:   aws.String("DataSource"),
		Action:        aws.String("lambda:InvokeFunction"),
		Principal:     aws.String("lambda.datasource.cloudwatch.amazonaws.com"),
		SourceAccount: aws.String("123456789012"),
	})
	require.NoError(t, err)
	out, err = run(`"query", 2`)
	require.NoError(t, err)
	require.Len(t, out.MetricDataResults, 1)
	r := out.MetricDataResults[0]
	assert.Equal(t, cwtypes.StatusCodeComplete, r.StatusCode)
	assert.Equal(t, "query", aws.ToString(r.Label))
	assertSlots(t, []float64{2, 4, 6}, w.slots(r), "the datapoint off the period grid is discarded")

	filled := exprQuery("f1", fmt.Sprintf(`FILL(LAMBDA("%s", "query", 3), 0) * 2`, name))
	filled.Period = aws.Int32(60)
	both, err := cw.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(w.t0), EndTime: aws.Time(w.end()), ScanBy: cwtypes.ScanByTimestampAscending, MetricDataQueries: []cwtypes.MetricDataQuery{filled},
	})
	require.NoError(t, err)
	assertSlots(t, []float64{6, 12, 18}, w.slots(both.MetricDataResults[0]), "LAMBDA combines with other functions")

	out, err = run(`"deny", 1`)
	require.NoError(t, err)
	assert.Equal(t, cwtypes.StatusCodeForbidden, out.MetricDataResults[0].StatusCode)
	require.NotEmpty(t, out.MetricDataResults[0].Messages)
	assert.Equal(t, "no access", aws.ToString(out.MetricDataResults[0].Messages[0].Value))

	_, err = run(`"bad", 1`)
	requireAWSErrorCode(t, err, "ValidationError")
	assert.Contains(t, err.Error(), "Invalid Prometheus cluster")
}

// TestCloudWatch_AnomalyBandEdgesOrder expects the band's lower edge first,
// so LAST(ANOMALY_DETECTION_BAND(m1)) is its upper bound.
func TestCloudWatch_AnomalyBandEdgesOrder(t *testing.T) {
	t.Parallel()
	client := cloudwatchClient()
	ns := uniquename.New("Custom/BandOrder")
	putBaseline(t, client, ns, "Latency", 30)
	_, err := client.PutAnomalyDetector(ctx, &cloudwatch.PutAnomalyDetectorInput{
		SingleMetricAnomalyDetector: &cwtypes.SingleMetricAnomalyDetector{Namespace: aws.String(ns), MetricName: aws.String("Latency"), Stat: aws.String("Average")},
	})
	require.NoError(t, err)
	waitDetectorState(t, client, ns, "Latency", cwtypes.AnomalyDetectorStateValueTrained)
	end := time.Now().UTC()
	out, err := client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(end.Add(-time.Hour)), EndTime: aws.Time(end),
		MetricDataQueries: []cwtypes.MetricDataQuery{
			metricQuery("m1", ns, "Latency", false),
			exprQuery("ad1", "ANOMALY_DETECTION_BAND(m1)"),
			exprQuery("hi", "LAST(ANOMALY_DETECTION_BAND(m1))"),
			exprQuery("lo", "FIRST(ANOMALY_DETECTION_BAND(m1))"),
		},
	})
	require.NoError(t, err)
	byID := map[string][]cwtypes.MetricDataResult{}
	for _, r := range out.MetricDataResults {
		byID[aws.ToString(r.Id)] = append(byID[aws.ToString(r.Id)], r)
	}
	require.Len(t, byID["ad1"], 2)
	assert.Equal(t, byID["ad1"][0].Values, byID["lo"][0].Values)
	assert.Equal(t, byID["ad1"][1].Values, byID["hi"][0].Values)
	for i := range byID["hi"][0].Values {
		assert.Greater(t, byID["hi"][0].Values[i], byID["lo"][0].Values[i])
	}
}

func mathDetectorQueries(ns string) []cwtypes.MetricDataQuery {
	return []cwtypes.MetricDataQuery{
		metricQuery("m1", ns, "Errors", false),
		metricQuery("m2", ns, "Requests", false),
		{Id: aws.String("e1"), Expression: aws.String("m1 / m2 * 100"), ReturnData: aws.Bool(true)},
	}
}

// TestCloudWatch_MetricMathAnomalyDetector creates, trains, bands, describes
// and deletes a MetricMathAnomalyDetector.
func TestCloudWatch_MetricMathAnomalyDetector(t *testing.T) {
	t.Parallel()
	client := cloudwatchClient()
	ns := uniquename.New("Custom/MathAnomaly")
	now := time.Now().UTC()
	var data []cwtypes.MetricDatum
	for i := range 30 {
		at := aws.Time(now.Add(-time.Duration(10+i) * time.Minute))
		data = append(data,
			cwtypes.MetricDatum{MetricName: aws.String("Errors"), Value: aws.Float64(float64(4 + i%3)), Timestamp: at},
			cwtypes.MetricDatum{MetricName: aws.String("Requests"), Value: aws.Float64(100), Timestamp: at})
	}
	_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(ns), MetricData: data})
	require.NoError(t, err)

	put, err := client.PutAnomalyDetector(ctx, &cloudwatch.PutAnomalyDetectorInput{
		MetricMathAnomalyDetector: &cwtypes.MetricMathAnomalyDetector{MetricDataQueries: mathDetectorQueries(ns)},
		Configuration: &cwtypes.AnomalyDetectorConfiguration{
			AdaptationSpeed:          cwtypes.AnomalyDetectorAdaptationSpeedSlow,
			OutlierFilteringStrength: cwtypes.AnomalyDetectorOutlierFilteringStrengthStrict,
		},
	})
	require.NoError(t, err)
	id := aws.ToString(put.AnomalyDetectorId)
	require.NotEmpty(t, id)

	var det cwtypes.AnomalyDetector
	require.Eventually(t, func() bool {
		out, err := client.DescribeAnomalyDetectors(ctx, &cloudwatch.DescribeAnomalyDetectorsInput{AnomalyDetectorIds: []string{id}})
		require.NoError(t, err)
		if len(out.AnomalyDetectors) != 1 {
			return false
		}
		det = out.AnomalyDetectors[0]
		return det.StateValue == cwtypes.AnomalyDetectorStateValueTrained
	}, 30*time.Second, 250*time.Millisecond)
	require.NotNil(t, det.MetricMathAnomalyDetector)
	assert.Len(t, det.MetricMathAnomalyDetector.MetricDataQueries, 3)
	assert.Nil(t, det.SingleMetricAnomalyDetector)
	assert.Equal(t, cwtypes.AnomalyDetectorAdaptationSpeedSlow, det.Configuration.AdaptationSpeed)
	assert.Equal(t, cwtypes.AnomalyDetectorOutlierFilteringStrengthStrict, det.Configuration.OutlierFilteringStrength)

	single, err := client.DescribeAnomalyDetectors(ctx, &cloudwatch.DescribeAnomalyDetectorsInput{Namespace: aws.String(ns)})
	require.NoError(t, err)
	assert.Empty(t, single.AnomalyDetectors, "AnomalyDetectorTypes defaults to SINGLE_METRIC")
	both, err := client.DescribeAnomalyDetectors(ctx, &cloudwatch.DescribeAnomalyDetectorsInput{AnomalyDetectorTypes: []cwtypes.AnomalyDetectorType{cwtypes.AnomalyDetectorTypeMetricMath}})
	require.NoError(t, err)
	found := false
	for _, d := range both.AnomalyDetectors {
		found = found || aws.ToString(d.AnomalyDetectorId) == id
	}
	assert.True(t, found)

	end := time.Now().UTC()
	queries := append(mathDetectorQueries(ns), exprQuery("ad1", "ANOMALY_DETECTION_BAND(e1, 2)"))
	out, err := client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{StartTime: aws.Time(end.Add(-time.Hour)), EndTime: aws.Time(end), MetricDataQueries: queries})
	require.NoError(t, err)
	var band []cwtypes.MetricDataResult
	for _, r := range out.MetricDataResults {
		if aws.ToString(r.Id) == "ad1" {
			band = append(band, r)
		}
	}
	require.Len(t, band, 2)
	require.Len(t, band[1].Values, 30, "the band covers the expression's datapoints")
	for i := range band[1].Values {
		assert.Greater(t, band[1].Values[i], 5.0)
		assert.Less(t, band[0].Values[i], 5.0)
	}

	_, err = client.PutAnomalyDetector(ctx, &cloudwatch.PutAnomalyDetectorInput{
		MetricMathAnomalyDetector: &cwtypes.MetricMathAnomalyDetector{MetricDataQueries: []cwtypes.MetricDataQuery{
			metricQuery("m1", ns, "Errors", false), {Id: aws.String("e1"), Expression: aws.String("SUM(METRICS())"), ReturnData: aws.Bool(true)},
		}},
	})
	requireAWSErrorCode(t, err, "InvalidParameterValue")
	_, err = client.PutAnomalyDetector(ctx, &cloudwatch.PutAnomalyDetectorInput{
		Namespace:                 aws.String(ns),
		MetricMathAnomalyDetector: &cwtypes.MetricMathAnomalyDetector{MetricDataQueries: mathDetectorQueries(ns)},
	})
	requireAWSErrorCode(t, err, "InvalidParameterCombination")

	_, err = client.DeleteAnomalyDetector(ctx, &cloudwatch.DeleteAnomalyDetectorInput{AnomalyDetectorId: aws.String(id)})
	require.NoError(t, err)
	gone, err := client.DescribeAnomalyDetectors(ctx, &cloudwatch.DescribeAnomalyDetectorsInput{AnomalyDetectorIds: []string{id}})
	require.NoError(t, err)
	assert.Empty(t, gone.AnomalyDetectors)
}

// TestCloudWatch_MetricMathAnomalyAlarm alarms on a band over an expression;
// the alarm generates the metric math model behind it.
func TestCloudWatch_MetricMathAnomalyAlarm(t *testing.T) {
	t.Parallel()
	client := cloudwatchClient()
	ns := uniquename.New("Custom/MathAnomalyAlarm")
	now := time.Now().UTC()
	var data []cwtypes.MetricDatum
	for i := range 30 {
		at := aws.Time(now.Add(-time.Duration(10+i) * time.Minute))
		data = append(data,
			cwtypes.MetricDatum{MetricName: aws.String("Errors"), Value: aws.Float64(float64(4 + i%3)), Timestamp: at},
			cwtypes.MetricDatum{MetricName: aws.String("Requests"), Value: aws.Float64(100), Timestamp: at})
	}
	_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(ns), MetricData: data})
	require.NoError(t, err)
	name := uniquename.New("sdk-math-band-alarm")
	_, err = client.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(name),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanUpperThreshold,
		EvaluationPeriods:  aws.Int32(1),
		ThresholdMetricId:  aws.String("ad1"),
		TreatMissingData:   aws.String("notBreaching"),
		Metrics:            append(mathDetectorQueries(ns), cwtypes.MetricDataQuery{Id: aws.String("ad1"), Expression: aws.String("ANOMALY_DETECTION_BAND(e1, 2)"), ReturnData: aws.Bool(true)}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}}) })

	require.Eventually(t, func() bool {
		out, err := client.DescribeAnomalyDetectors(ctx, &cloudwatch.DescribeAnomalyDetectorsInput{AnomalyDetectorTypes: []cwtypes.AnomalyDetectorType{cwtypes.AnomalyDetectorTypeMetricMath}})
		require.NoError(t, err)
		for _, d := range out.AnomalyDetectors {
			if d.MetricMathAnomalyDetector == nil {
				continue
			}
			for _, q := range d.MetricMathAnomalyDetector.MetricDataQueries {
				if q.MetricStat != nil && aws.ToString(q.MetricStat.Metric.Namespace) == ns && d.StateValue == cwtypes.AnomalyDetectorStateValueTrained {
					return true
				}
			}
		}
		return false
	}, 30*time.Second, 250*time.Millisecond, "the alarm generates the metric math model")

	_, err = client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(ns), MetricData: []cwtypes.MetricDatum{
		{MetricName: aws.String("Errors"), Value: aws.Float64(60)}, {MetricName: aws.String("Requests"), Value: aws.Float64(100)},
	}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		out, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{name}})
		require.NoError(t, err)
		return len(out.MetricAlarms) == 1 && out.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 30*time.Second, 250*time.Millisecond, "an error rate far above the band breaches it")
}

// bandWidths answers the width of the 2-deviation band at each of a
// detector's datapoints and the band's midpoint at the latest.
func bandShape(t *testing.T, ns, metric string, period int32, from, to time.Time) (widths []float64, latestMid float64) {
	t.Helper()
	q := metricQuery("m1", ns, metric, false)
	q.MetricStat.Period = aws.Int32(period)
	out, err := cloudwatchClient().GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(from), EndTime: aws.Time(to), ScanBy: cwtypes.ScanByTimestampAscending,
		MetricDataQueries: []cwtypes.MetricDataQuery{q, exprQuery("ad1", "ANOMALY_DETECTION_BAND(m1, 2)")},
	})
	require.NoError(t, err)
	require.Len(t, out.MetricDataResults, 2)
	lower, upper := out.MetricDataResults[0], out.MetricDataResults[1]
	require.NotEmpty(t, upper.Values)
	for i := range upper.Values {
		widths = append(widths, upper.Values[i]-lower.Values[i])
	}
	last := len(upper.Values) - 1
	return widths, (upper.Values[last] + lower.Values[last]) / 2
}

func putDetector(t *testing.T, ns, metric string, conf *cwtypes.AnomalyDetectorConfiguration, spikes *bool) {
	t.Helper()
	input := &cloudwatch.PutAnomalyDetectorInput{
		SingleMetricAnomalyDetector: &cwtypes.SingleMetricAnomalyDetector{Namespace: aws.String(ns), MetricName: aws.String(metric), Stat: aws.String("Average")},
		Configuration:               conf,
	}
	if spikes != nil {
		input.MetricCharacteristics = &cwtypes.MetricCharacteristics{PeriodicSpikes: spikes}
	}
	_, err := cloudwatchClient().PutAnomalyDetector(ctx, input)
	require.NoError(t, err)
	waitDetectorState(t, cloudwatchClient(), ns, metric, cwtypes.AnomalyDetectorStateValueTrained)
}

// TestCloudWatch_AnomalyModelSettings expects the documented direction of
// each setting: STRICT outlier filtering leaves a tighter band than MINIMAL,
// FAST adaptation follows a sustained change in the baseline sooner than
// SLOW, and PeriodicSpikes keeps recurring spikes inside the band.
func TestCloudWatch_AnomalyModelSettings(t *testing.T) {
	t.Parallel()
	client := cloudwatchClient()
	ns := uniquename.New("Custom/AnomalySettings")
	now := time.Now().UTC().Truncate(5 * time.Minute)
	series := func(metric string, n int, value func(i int) float64) {
		var data []cwtypes.MetricDatum
		for i := range n {
			data = append(data, cwtypes.MetricDatum{MetricName: aws.String(metric), Value: aws.Float64(value(i)), Timestamp: aws.Time(now.Add(-time.Duration(n-i) * 5 * time.Minute))})
		}
		_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(ns), MetricData: data})
		require.NoError(t, err)
	}
	withOutliers := func(i int) float64 {
		if i%10 == 5 {
			return 15
		}
		return float64(9 + 2*(i%2))
	}
	series("Strict", 40, withOutliers)
	series("Minimal", 40, withOutliers)
	putDetector(t, ns, "Strict", &cwtypes.AnomalyDetectorConfiguration{OutlierFilteringStrength: cwtypes.AnomalyDetectorOutlierFilteringStrengthStrict}, nil)
	putDetector(t, ns, "Minimal", &cwtypes.AnomalyDetectorConfiguration{OutlierFilteringStrength: cwtypes.AnomalyDetectorOutlierFilteringStrengthMinimal}, nil)
	from := now.Add(-4 * time.Hour)
	strict, _ := bandShape(t, ns, "Strict", 300, from, now)
	minimal, _ := bandShape(t, ns, "Minimal", 300, from, now)
	assert.Less(t, strict[0], minimal[0], "STRICT filtering excludes the outliers MINIMAL keeps")

	shifted := func(i int) float64 {
		if i >= 40 {
			return float64(19 + 2*(i%2))
		}
		return float64(9 + 2*(i%2))
	}
	series("Fast", 52, shifted)
	series("Slow", 52, shifted)
	putDetector(t, ns, "Fast", &cwtypes.AnomalyDetectorConfiguration{AdaptationSpeed: cwtypes.AnomalyDetectorAdaptationSpeedFast}, nil)
	putDetector(t, ns, "Slow", &cwtypes.AnomalyDetectorConfiguration{AdaptationSpeed: cwtypes.AnomalyDetectorAdaptationSpeedSlow}, nil)
	_, fastMid := bandShape(t, ns, "Fast", 300, from.Add(-2*time.Hour), now)
	_, slowMid := bandShape(t, ns, "Slow", 300, from.Add(-2*time.Hour), now)
	assert.Greater(t, fastMid, slowMid+1, "FAST adaptation moves the expected value toward the new baseline")

	day := now.Truncate(24 * time.Hour)
	spiky := func(metric string) {
		var data []cwtypes.MetricDatum
		for d := 1; d <= 3; d++ {
			base := day.Add(-time.Duration(d)*24*time.Hour + 12*time.Hour)
			for k := range 12 {
				v := float64(9 + 2*(k%2))
				if k == 6 {
					v = 50
				}
				data = append(data, cwtypes.MetricDatum{MetricName: aws.String(metric), Value: aws.Float64(v), Timestamp: aws.Time(base.Add(time.Duration(k) * 5 * time.Minute))})
			}
		}
		_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(ns), MetricData: data})
		require.NoError(t, err)
	}
	spiky("Periodic")
	spiky("Aperiodic")
	putDetector(t, ns, "Periodic", nil, aws.Bool(true))
	putDetector(t, ns, "Aperiodic", nil, aws.Bool(false))
	spikeAt := day.Add(-24*time.Hour + 12*time.Hour + 30*time.Minute)
	upperAt := func(metric string) float64 {
		q := metricQuery("m1", ns, metric, false)
		q.MetricStat.Period = aws.Int32(300)
		out, err := client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
			StartTime: aws.Time(spikeAt), EndTime: aws.Time(spikeAt.Add(5 * time.Minute)),
			MetricDataQueries: []cwtypes.MetricDataQuery{q, exprQuery("hi", "LAST(ANOMALY_DETECTION_BAND(m1, 2))")},
		})
		require.NoError(t, err)
		require.Len(t, out.MetricDataResults, 1)
		require.Len(t, out.MetricDataResults[0].Values, 1)
		return out.MetricDataResults[0].Values[0]
	}
	assert.GreaterOrEqual(t, upperAt("Periodic"), 50.0, "PeriodicSpikes expects the spike that recurs every day")
	assert.Less(t, upperAt("Aperiodic"), 50.0, "without PeriodicSpikes the spike stays an anomaly")
}

func countColor(img image.Image, want color.RGBA, region image.Rectangle) int {
	n := 0
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if uint8(r>>8) == want.R && uint8(g>>8) == want.G && uint8(b>>8) == want.B {
				n++
			}
		}
	}
	return n
}

func widgetImage(t *testing.T, widget string) image.Image {
	t.Helper()
	out, err := cloudwatchClient().GetMetricWidgetImage(ctx, &cloudwatch.GetMetricWidgetImageInput{MetricWidget: aws.String(widget)})
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(out.MetricWidgetImage))
	require.NoError(t, err)
	return img
}

// TestCloudWatch_MetricWidgetImageOptions draws the widget options the
// metric widget structure defines: vertical annotations and their fill, a
// metric on the right y-axis, a stacked graph, live data and the pie view.
func TestCloudWatch_MetricWidgetImageOptions(t *testing.T) {
	t.Parallel()
	client := cloudwatchClient()
	ns := uniquename.New("Custom/WidgetOptions")
	putBaseline(t, client, ns, "Load", 30)
	now := time.Now().UTC()
	var big []cwtypes.MetricDatum
	for i := range 30 {
		big = append(big, cwtypes.MetricDatum{MetricName: aws.String("Bytes"), Value: aws.Float64(10000 + float64(i)*100), Timestamp: aws.Time(now.Add(-time.Duration(10+i) * time.Minute))})
	}
	_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(ns), MetricData: big})
	require.NoError(t, err)
	purple := color.RGBA{R: 0x94, G: 0x67, B: 0xbd}
	red := color.RGBA{R: 0xd6, G: 0x27, B: 0x28}
	green := color.RGBA{R: 0x2c, G: 0xa0, B: 0x2c}

	deployed := now.Add(-20 * time.Minute).Format(time.RFC3339)
	widget := fmt.Sprintf(`{"metrics":[["%s","Load",{"color":"#d62728"}]],"period":60,"start":"-PT1H","width":400,"height":250,"legend":{"position":"hidden"},
		"annotations":{"vertical":[{"value":"%s","label":"deploy","color":"#9467bd","fill":"after"}]}}`, ns, deployed)
	img := widgetImage(t, widget)
	assert.Greater(t, countColor(img, purple, img.Bounds()), 100, "the vertical annotation is drawn")
	shaded := image.Rect(img.Bounds().Dx()*3/4, 60, img.Bounds().Dx()*3/4+20, 70)
	early := image.Rect(80, 60, 100, 70)
	tint := func(region image.Rectangle) int {
		n := 0
		for y := region.Min.Y; y < region.Max.Y; y++ {
			for x := region.Min.X; x < region.Max.X; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				if r != 0xffff || g != 0xffff || b != 0xffff {
					n++
				}
			}
		}
		return n
	}
	assert.Greater(t, tint(shaded), tint(early), "fill after shades the graph after the annotation")

	_, err = client.GetMetricWidgetImage(ctx, &cloudwatch.GetMetricWidgetImageInput{MetricWidget: aws.String(
		fmt.Sprintf(`{"metrics":[["%s","Load"]],"annotations":{"vertical":[{"value":"-PT1H"}]}}`, ns))})
	requireAWSErrorCode(t, err, "InvalidParameterValue")

	twoAxes := fmt.Sprintf(`{"metrics":[["%[1]s","Load",{"color":"#d62728"}],["%[1]s","Bytes",{"color":"#2ca02c","yAxis":"right"}]],"period":60,"start":"-PT1H","width":400,"height":250,"legend":{"position":"hidden"},"yAxis":{"right":{"label":"bytes"}}}`, ns)
	img = widgetImage(t, twoAxes)
	plotTop := image.Rect(56, 0, 344, 125)
	assert.Greater(t, countColor(img, red, plotTop), 50, "on its own left axis the small metric spans the plot")
	assert.Greater(t, countColor(img, green, plotTop), 50, "the right axis scales the large metric")

	sameAxis := fmt.Sprintf(`{"metrics":[["%[1]s","Load",{"color":"#d62728"}],["%[1]s","Bytes",{"color":"#2ca02c"}]],"period":60,"start":"-PT1H","width":400,"height":250,"legend":{"position":"hidden"}}`, ns)
	img = widgetImage(t, sameAxis)
	assert.Zero(t, countColor(img, red, plotTop), "sharing the large metric's axis flattens the small metric")

	stacked := fmt.Sprintf(`{"metrics":[["%[1]s","Load",{"color":"#d62728"}],["%[1]s","Load",{"color":"#2ca02c","stat":"Maximum"}]],"stacked":true,"period":60,"start":"-PT1H","width":400,"height":250,"legend":{"position":"hidden"}}`, ns)
	img = widgetImage(t, stacked)
	assert.Greater(t, countColor(img, green, img.Bounds()), 100, "the stacked series is drawn above the first")

	pie := fmt.Sprintf(`{"metrics":[["%[1]s","Load",{"color":"#d62728"}],["%[1]s","Bytes",{"color":"#2ca02c"}]],"view":"pie","period":60,"start":"-PT1H","width":300,"height":300,"legend":{"position":"hidden"}}`, ns)
	img = widgetImage(t, pie)
	assert.Greater(t, countColor(img, green, img.Bounds()), 10*countColor(img, red, img.Bounds()), "each slice is the series' share of the latest values")

	live := fmt.Sprintf(`{"metrics":[["%s","Fresh",{"color":"#9467bd"}]],"period":60,"start":"-PT10M","width":300,"height":200,"legend":{"position":"hidden"},"liveData":%%t}`, ns)
	_, err = client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(ns), MetricData: []cwtypes.MetricDatum{
		{MetricName: aws.String("Fresh"), Value: aws.Float64(1), Timestamp: aws.Time(time.Now().Add(-30 * time.Second))}, {MetricName: aws.String("Fresh"), Value: aws.Float64(1), Timestamp: aws.Time(now.Add(-5 * time.Minute))},
	}})
	require.NoError(t, err)
	settled := countColor(widgetImage(t, fmt.Sprintf(live, false)), purple, image.Rect(0, 0, 300, 200))
	withLive := countColor(widgetImage(t, fmt.Sprintf(live, true)), purple, image.Rect(0, 0, 300, 200))
	assert.Greater(t, withLive, settled, "live data draws the period still being aggregated")
}
