package aws_sdk_test

import (
	"bytes"
	"fmt"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// putBaseline records n datapoints around 10, one a minute, ending ten
// minutes ago.
func putBaseline(t *testing.T, client *cloudwatch.Client, ns, metric string, n int) {
	t.Helper()
	now := time.Now().UTC()
	data := make([]cwtypes.MetricDatum, 0, n)
	for i := range n {
		data = append(data, cwtypes.MetricDatum{
			MetricName: aws.String(metric),
			Value:      aws.Float64(10 + float64(i%3) - 1),
			Timestamp:  aws.Time(now.Add(-time.Duration(10+i) * time.Minute)),
		})
	}
	_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(ns), MetricData: data})
	require.NoError(t, err)
}

// waitDetectorState polls DescribeAnomalyDetectors' StateValue, the only
// training signal CloudWatch exposes.
func waitDetectorState(t *testing.T, client *cloudwatch.Client, ns, metric string, want cwtypes.AnomalyDetectorStateValue) cwtypes.AnomalyDetector {
	t.Helper()
	var last cwtypes.AnomalyDetector
	require.Eventually(t, func() bool {
		out, err := client.DescribeAnomalyDetectors(ctx, &cloudwatch.DescribeAnomalyDetectorsInput{Namespace: aws.String(ns), MetricName: aws.String(metric)})
		require.NoError(t, err)
		if len(out.AnomalyDetectors) != 1 {
			return false
		}
		last = out.AnomalyDetectors[0]
		return last.StateValue == want
	}, 30*time.Second, 250*time.Millisecond, "detector state %s", last.StateValue)
	return last
}

func TestCloudWatch_AnomalyDetectorTrainsAndBands(t *testing.T) {
	t.Parallel()
	client := cloudwatchClient()
	ns := uniquename.New("Custom/AnomalyBand")
	putBaseline(t, client, ns, "Latency", 30)
	_, err := client.PutAnomalyDetector(ctx, &cloudwatch.PutAnomalyDetectorInput{
		SingleMetricAnomalyDetector: &cwtypes.SingleMetricAnomalyDetector{Namespace: aws.String(ns), MetricName: aws.String("Latency"), Stat: aws.String("Average")},
		Configuration:               &cwtypes.AnomalyDetectorConfiguration{MetricTimezone: aws.String("Europe/Berlin")},
		MetricCharacteristics:       &cwtypes.MetricCharacteristics{PeriodicSpikes: aws.Bool(false)},
	})
	require.NoError(t, err)

	det := waitDetectorState(t, client, ns, "Latency", cwtypes.AnomalyDetectorStateValueTrained)
	require.NotNil(t, det.Configuration)
	assert.Equal(t, "Europe/Berlin", aws.ToString(det.Configuration.MetricTimezone))
	require.NotNil(t, det.MetricCharacteristics)
	assert.False(t, aws.ToBool(det.MetricCharacteristics.PeriodicSpikes))

	end := time.Now().UTC()
	out, err := client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(end.Add(-time.Hour)),
		EndTime:   aws.Time(end),
		MetricDataQueries: []cwtypes.MetricDataQuery{
			{Id: aws.String("m1"), MetricStat: &cwtypes.MetricStat{Metric: &cwtypes.Metric{Namespace: aws.String(ns), MetricName: aws.String("Latency")}, Period: aws.Int32(60), Stat: aws.String("Average")}},
			{Id: aws.String("ad1"), Expression: aws.String("ANOMALY_DETECTION_BAND(m1, 2)")},
			{Id: aws.String("e1"), Expression: aws.String("m1 * 2 + 1"), Label: aws.String("doubled")},
		},
	})
	require.NoError(t, err)
	byID := map[string][]cwtypes.MetricDataResult{}
	for _, r := range out.MetricDataResults {
		byID[aws.ToString(r.Id)] = append(byID[aws.ToString(r.Id)], r)
	}
	require.Len(t, byID["m1"], 1)
	require.Len(t, byID["m1"][0].Values, 30)
	require.Len(t, byID["ad1"], 2, "a band answers its upper and lower edges")
	upper, lower := byID["ad1"][0], byID["ad1"][1]
	require.Len(t, upper.Values, 30)
	for i := range upper.Values {
		assert.Greater(t, upper.Values[i], 10.0)
		assert.Less(t, lower.Values[i], 10.0)
	}
	require.Len(t, byID["e1"], 1)
	assert.Equal(t, "doubled", aws.ToString(byID["e1"][0].Label))
	assert.Equal(t, byID["m1"][0].Values[0]*2+1, byID["e1"][0].Values[0])
}

func TestCloudWatch_AnomalyDetectorWithoutHistoryPendsTraining(t *testing.T) {
	t.Parallel()
	client := cloudwatchClient()
	ns := uniquename.New("Custom/AnomalyEmpty")
	_, err := client.PutAnomalyDetector(ctx, &cloudwatch.PutAnomalyDetectorInput{
		SingleMetricAnomalyDetector: &cwtypes.SingleMetricAnomalyDetector{Namespace: aws.String(ns), MetricName: aws.String("Idle"), Stat: aws.String("Sum")},
	})
	require.NoError(t, err)
	_, err = client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(ns), MetricData: []cwtypes.MetricDatum{{MetricName: aws.String("Idle"), Value: aws.Float64(1)}}})
	require.NoError(t, err)
	waitDetectorState(t, client, ns, "Idle", cwtypes.AnomalyDetectorStateValueTrainedInsufficientData)
}

func TestCloudWatch_AnomalyBandAlarm(t *testing.T) {
	t.Parallel()
	client := cloudwatchClient()
	ns := uniquename.New("Custom/AnomalyAlarm")
	putBaseline(t, client, ns, "Requests", 30)
	name := uniquename.New("sdk-anomaly-alarm")
	metrics := []cwtypes.MetricDataQuery{
		{Id: aws.String("m1"), ReturnData: aws.Bool(true), MetricStat: &cwtypes.MetricStat{Metric: &cwtypes.Metric{Namespace: aws.String(ns), MetricName: aws.String("Requests")}, Period: aws.Int32(60), Stat: aws.String("Average")}},
		{Id: aws.String("ad1"), ReturnData: aws.Bool(true), Expression: aws.String("ANOMALY_DETECTION_BAND(m1, 2)")},
	}
	_, err := client.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(name),
		ComparisonOperator: cwtypes.ComparisonOperatorLessThanLowerOrGreaterThanUpperThreshold,
		EvaluationPeriods:  aws.Int32(1),
		ThresholdMetricId:  aws.String("ad1"),
		Metrics:            metrics,
		TreatMissingData:   aws.String("notBreaching"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}}) })

	described, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{name}})
	require.NoError(t, err)
	require.Len(t, described.MetricAlarms, 1)
	assert.Equal(t, "ad1", aws.ToString(described.MetricAlarms[0].ThresholdMetricId))
	require.Len(t, described.MetricAlarms[0].Metrics, 2)
	assert.Equal(t, "ANOMALY_DETECTION_BAND(m1, 2)", aws.ToString(described.MetricAlarms[0].Metrics[1].Expression))

	// The alarm generates the model behind its band.
	waitDetectorState(t, client, ns, "Requests", cwtypes.AnomalyDetectorStateValueTrained)

	_, err = client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(ns), MetricData: []cwtypes.MetricDatum{{MetricName: aws.String("Requests"), Value: aws.Float64(100)}}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		out, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{name}})
		require.NoError(t, err)
		return len(out.MetricAlarms) == 1 && out.MetricAlarms[0].StateValue == cwtypes.StateValueAlarm
	}, 30*time.Second, 250*time.Millisecond, "a datapoint far above the band breaches it")

	_, err = client.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(name + "-static"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanUpperThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Metrics:            metrics,
	})
	requireAWSErrorCode(t, err, "ValidationError")
}

func TestCloudWatch_MetricMathRefusesUnknownFunction(t *testing.T) {
	t.Parallel()
	end := time.Now().UTC()
	_, err := cloudwatchClient().GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime:         aws.Time(end.Add(-time.Hour)),
		EndTime:           aws.Time(end),
		MetricDataQueries: []cwtypes.MetricDataQuery{{Id: aws.String("e1"), Expression: aws.String("NO_SUCH_FUNCTION(1)")}},
	})
	requireAWSErrorCode(t, err, "ValidationError")
}

// TestCloudWatch_MetricWidgetImageDrawsTheMetrics renders a widget over
// recorded datapoints and finds its line, in the color the widget names, and
// its title's pixels.
func TestCloudWatch_MetricWidgetImageDrawsTheMetrics(t *testing.T) {
	t.Parallel()
	client := cloudwatchClient()
	ns := uniquename.New("Custom/Widget")
	putBaseline(t, client, ns, "Load", 30)
	widget := fmt.Sprintf(`{"metrics":[["%s","Load",{"color":"#d62728","label":"load"}]],"period":60,"start":"-PT1H","title":"Load","width":400,"height":250,"annotations":{"horizontal":[{"value":15,"label":"limit","color":"#2ca02c"}]}}`, ns)
	out, err := client.GetMetricWidgetImage(ctx, &cloudwatch.GetMetricWidgetImageInput{MetricWidget: aws.String(widget), OutputFormat: aws.String("png")})
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(out.MetricWidgetImage))
	require.NoError(t, err)
	assert.Equal(t, 400, img.Bounds().Dx())
	assert.Equal(t, 250, img.Bounds().Dy())
	count := func(want color.RGBA) int {
		n := 0
		for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
			for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				if uint8(r>>8) == want.R && uint8(g>>8) == want.G && uint8(b>>8) == want.B {
					n++
				}
			}
		}
		return n
	}
	assert.Greater(t, count(color.RGBA{R: 0xd6, G: 0x27, B: 0x28}), 100, "the metric's line is drawn in its color")
	assert.Greater(t, count(color.RGBA{R: 0x2c, G: 0xa0, B: 0x2c}), 100, "the horizontal annotation is drawn")

	_, err = client.GetMetricWidgetImage(ctx, &cloudwatch.GetMetricWidgetImageInput{MetricWidget: aws.String(`{"metrics":[["A","B"]],"width":3000}`)})
	requireAWSErrorCode(t, err, "InvalidParameterValue")
}

// TestCloudWatch_InsightRuleReportCountsContributors feeds JSON log events to
// a Contributor Insights rule and reads the contributors back.
func TestCloudWatch_InsightRuleReportCountsContributors(t *testing.T) {
	t.Parallel()
	client := cloudwatchClient()
	logs := cwLogsClient()
	prefix := uniquename.New("/ci/access")
	group := prefix + "-api"
	name := uniquename.New("sdk-ci-rule")
	def := `{"Schema":{"Name":"CloudWatchLogRule","Version":1},"LogGroupNames":["` + prefix + `*"],"LogFormat":"JSON",` +
		`"Contribution":{"Keys":["$.ip"],"ValueOf":"$.bytes","Filters":[{"Match":"$.method","In":["PUT"]}]},"AggregateOn":"Sum"}`
	_, err := client.PutInsightRule(ctx, &cloudwatch.PutInsightRuleInput{RuleName: aws.String(name), RuleDefinition: aws.String(def)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteInsightRules(ctx, &cloudwatch.DeleteInsightRulesInput{RuleNames: []string{name}})
	})

	_, err = logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	})
	_, err = logs.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String("s")})
	require.NoError(t, err)
	now := time.Now().UTC()
	put := func(lines ...string) {
		events := make([]cwlogtypes.InputLogEvent, 0, len(lines))
		for _, l := range lines {
			events = append(events, cwlogtypes.InputLogEvent{Message: aws.String(l), Timestamp: aws.Int64(now.UnixMilli())})
		}
		_, err := logs.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String("s"), LogEvents: events})
		require.NoError(t, err)
	}
	put(`{"ip":"10.0.0.1","method":"PUT","bytes":100}`,
		`{"ip":"10.0.0.1","method":"PUT","bytes":50}`,
		`{"ip":"10.0.0.2","method":"PUT","bytes":70}`,
		`{"ip":"10.0.0.3","method":"GET","bytes":999}`)

	report := func() *cloudwatch.GetInsightRuleReportOutput {
		out, err := client.GetInsightRuleReport(ctx, &cloudwatch.GetInsightRuleReportInput{
			RuleName:  aws.String(name),
			StartTime: aws.Time(now.Add(-5 * time.Minute)),
			EndTime:   aws.Time(now.Add(5 * time.Minute)),
			Period:    aws.Int32(300),
			Metrics:   []string{"UniqueContributors", "Sum"},
		})
		require.NoError(t, err)
		return out
	}
	out := report()
	assert.Equal(t, []string{"$.ip"}, out.KeyLabels)
	assert.Equal(t, "Sum", aws.ToString(out.AggregationStatistic))
	assert.Equal(t, 220.0, aws.ToFloat64(out.AggregateValue))
	assert.Equal(t, int64(2), aws.ToInt64(out.ApproximateUniqueCount))
	require.Len(t, out.Contributors, 2)
	assert.Equal(t, []string{"10.0.0.1"}, out.Contributors[0].Keys)
	assert.Equal(t, 150.0, aws.ToFloat64(out.Contributors[0].ApproximateAggregateValue))
	var sum, unique float64
	for _, dp := range out.MetricDatapoints {
		sum += aws.ToFloat64(dp.Sum)
		unique = max(unique, aws.ToFloat64(dp.UniqueContributors))
	}
	assert.Equal(t, 220.0, sum)
	assert.Equal(t, 2.0, unique)

	// A DISABLED rule ignores what is ingested meanwhile.
	_, err = client.DisableInsightRules(ctx, &cloudwatch.DisableInsightRulesInput{RuleNames: []string{name}})
	require.NoError(t, err)
	put(`{"ip":"10.0.0.9","method":"PUT","bytes":1000}`)
	assert.Equal(t, 220.0, aws.ToFloat64(report().AggregateValue))

	_, err = client.PutInsightRule(ctx, &cloudwatch.PutInsightRuleInput{RuleName: aws.String(name + "-bad"), RuleDefinition: aws.String(`{"Schema":{"Name":"CloudWatchLogRule","Version":1}}`)})
	requireAWSErrorCode(t, err, "InvalidParameterValue")
}
