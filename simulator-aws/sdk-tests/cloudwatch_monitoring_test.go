package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_AlarmActionsAndState covers EnableAlarmActions /
// DisableAlarmActions / SetAlarmState / DescribeAlarmHistory: a manual state set
// via SetAlarmState shows through DescribeAlarms, action toggles persist, and
// the state change is recorded in the alarm history.
func TestCloudWatch_AlarmActionsAndState(t *testing.T) {
	client := cloudwatchClient()
	name := "sdk-alarm-actions"

	_, err := client.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(name),
		Namespace:          aws.String("Custom/ActionsSDK"),
		MetricName:         aws.String("M"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(0),
		Statistic:          cwtypes.StatisticSum,
		TreatMissingData:   aws.String("notBreaching"),
	})
	require.NoError(t, err)
	defer func() { _, _ = client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}}) }()

	_, err = client.DisableAlarmActions(ctx, &cloudwatch.DisableAlarmActionsInput{AlarmNames: []string{name}})
	require.NoError(t, err)
	out, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{name}})
	require.NoError(t, err)
	require.Len(t, out.MetricAlarms, 1)
	assert.False(t, aws.ToBool(out.MetricAlarms[0].ActionsEnabled))

	_, err = client.EnableAlarmActions(ctx, &cloudwatch.EnableAlarmActionsInput{AlarmNames: []string{name}})
	require.NoError(t, err)
	out, err = client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{name}})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(out.MetricAlarms[0].ActionsEnabled))

	_, err = client.SetAlarmState(ctx, &cloudwatch.SetAlarmStateInput{
		AlarmName:   aws.String(name),
		StateValue:  cwtypes.StateValueAlarm,
		StateReason: aws.String("manual test override"),
	})
	require.NoError(t, err)
	out, err = client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{name}})
	require.NoError(t, err)
	assert.Equal(t, cwtypes.StateValueAlarm, out.MetricAlarms[0].StateValue, "manual state shows through DescribeAlarms")

	hist, err := client.DescribeAlarmHistory(ctx, &cloudwatch.DescribeAlarmHistoryInput{
		AlarmName:       aws.String(name),
		HistoryItemType: cwtypes.HistoryItemTypeStateUpdate,
	})
	require.NoError(t, err)
	require.NotEmpty(t, hist.AlarmHistoryItems, "SetAlarmState recorded a StateUpdate history item")
	assert.Equal(t, name, aws.ToString(hist.AlarmHistoryItems[0].AlarmName))
}

// TestCloudWatch_DescribeAlarmsForMetric verifies the metric→alarm reverse lookup.
func TestCloudWatch_DescribeAlarmsForMetric(t *testing.T) {
	client := cloudwatchClient()
	ns := "Custom/ReverseLookup"
	_, err := client.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String("sdk-reverse-alarm"),
		Namespace:          aws.String(ns),
		MetricName:         aws.String("Lat"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(1),
		Statistic:          cwtypes.StatisticAverage,
	})
	require.NoError(t, err)
	defer func() {
		_, _ = client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{"sdk-reverse-alarm"}})
	}()

	out, err := client.DescribeAlarmsForMetric(ctx, &cloudwatch.DescribeAlarmsForMetricInput{
		Namespace:  aws.String(ns),
		MetricName: aws.String("Lat"),
	})
	require.NoError(t, err)
	require.Len(t, out.MetricAlarms, 1)
	assert.Equal(t, "sdk-reverse-alarm", aws.ToString(out.MetricAlarms[0].AlarmName))
}

// TestCloudWatch_CompositeAlarm covers PutCompositeAlarm + tag round-trip via the
// cross-resource tagging API.
func TestCloudWatch_CompositeAlarm(t *testing.T) {
	client := cloudwatchClient()
	name := "sdk-composite"
	_, err := client.PutCompositeAlarm(ctx, &cloudwatch.PutCompositeAlarmInput{
		AlarmName: aws.String(name),
		AlarmRule: aws.String("ALARM(child-1) OR ALARM(child-2)"),
		Tags:      []cwtypes.Tag{{Key: aws.String("team"), Value: aws.String("sre")}},
	})
	require.NoError(t, err)
	defer func() { _, _ = client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}}) }()

	da, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{
		AlarmNames: []string{name},
		AlarmTypes: []cwtypes.AlarmType{cwtypes.AlarmTypeCompositeAlarm},
	})
	require.NoError(t, err)
	require.Len(t, da.CompositeAlarms, 1)
	arn := aws.ToString(da.CompositeAlarms[0].AlarmArn)
	tags, err := client.ListTagsForResource(ctx, &cloudwatch.ListTagsForResourceInput{ResourceARN: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "team", aws.ToString(tags.Tags[0].Key))
}

// TestCloudWatch_MetricStreams covers the full stream lifecycle: put, get, list,
// stop, start, delete — with the RUNNING/STOPPED state transitioning.
func TestCloudWatch_MetricStreams(t *testing.T) {
	client := cloudwatchClient()
	name := "sdk-stream"
	firehoseARN, _ := createImmediateFirehose(t, "sdk-stream-firehose")
	roleARN := createFirehoseServiceRole(t, "sdk-stream-cloudwatch-role",
		"streams.metrics.cloudwatch.amazonaws.com", []string{"firehose:PutRecord"}, []string{firehoseARN})
	_, err := client.PutMetricStream(ctx, &cloudwatch.PutMetricStreamInput{
		Name:         aws.String(name),
		FirehoseArn:  aws.String(firehoseARN),
		RoleArn:      aws.String(roleARN),
		OutputFormat: cwtypes.MetricStreamOutputFormatJson,
		IncludeFilters: []cwtypes.MetricStreamFilter{
			{Namespace: aws.String("AWS/EC2")},
		},
	})
	require.NoError(t, err)
	defer func() {
		_, _ = client.DeleteMetricStream(ctx, &cloudwatch.DeleteMetricStreamInput{Name: aws.String(name)})
	}()

	got, err := client.GetMetricStream(ctx, &cloudwatch.GetMetricStreamInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(got.Name))
	assert.Contains(t, aws.ToString(got.Arn), ":metric-stream/"+name)
	assert.Equal(t, "running", aws.ToString(got.State))
	require.Len(t, got.IncludeFilters, 1)
	assert.Equal(t, "AWS/EC2", aws.ToString(got.IncludeFilters[0].Namespace))

	_, err = client.StopMetricStreams(ctx, &cloudwatch.StopMetricStreamsInput{Names: []string{name}})
	require.NoError(t, err)
	got, err = client.GetMetricStream(ctx, &cloudwatch.GetMetricStreamInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "stopped", aws.ToString(got.State))

	_, err = client.StartMetricStreams(ctx, &cloudwatch.StartMetricStreamsInput{Names: []string{name}})
	require.NoError(t, err)

	list, err := client.ListMetricStreams(ctx, &cloudwatch.ListMetricStreamsInput{})
	require.NoError(t, err)
	found := false
	for _, e := range list.Entries {
		if aws.ToString(e.Name) == name {
			found = true
		}
	}
	assert.True(t, found, "stream appears in ListMetricStreams")
}

// TestCloudWatch_AnomalyDetector covers put / describe / delete of a
// single-metric anomaly detector.
func TestCloudWatch_AnomalyDetector(t *testing.T) {
	client := cloudwatchClient()
	ns := "Custom/AnomalySDK"
	put := &cloudwatch.PutAnomalyDetectorInput{
		SingleMetricAnomalyDetector: &cwtypes.SingleMetricAnomalyDetector{
			Namespace:  aws.String(ns),
			MetricName: aws.String("CPU"),
			Stat:       aws.String("Average"),
		},
	}
	_, err := client.PutAnomalyDetector(ctx, put)
	require.NoError(t, err)

	out, err := client.DescribeAnomalyDetectors(ctx, &cloudwatch.DescribeAnomalyDetectorsInput{
		Namespace:  aws.String(ns),
		MetricName: aws.String("CPU"),
	})
	require.NoError(t, err)
	require.Len(t, out.AnomalyDetectors, 1)
	assert.Equal(t, cwtypes.AnomalyDetectorStateValuePendingTraining, out.AnomalyDetectors[0].StateValue)

	_, err = client.DeleteAnomalyDetector(ctx, &cloudwatch.DeleteAnomalyDetectorInput{
		SingleMetricAnomalyDetector: &cwtypes.SingleMetricAnomalyDetector{
			Namespace:  aws.String(ns),
			MetricName: aws.String("CPU"),
			Stat:       aws.String("Average"),
		},
	})
	require.NoError(t, err)
	out, err = client.DescribeAnomalyDetectors(ctx, &cloudwatch.DescribeAnomalyDetectorsInput{
		Namespace:  aws.String(ns),
		MetricName: aws.String("CPU"),
	})
	require.NoError(t, err)
	assert.Empty(t, out.AnomalyDetectors)
}

// TestCloudWatch_InsightRules covers put / describe / disable / enable / delete.
func TestCloudWatch_InsightRules(t *testing.T) {
	client := cloudwatchClient()
	name := "sdk-insight"
	def := `{"Schema":{"Name":"CloudWatchLogRule","Version":1},"LogGroupNames":["/aws/lambda/x"],"Contribution":{"Keys":["$.requestId"]},"AggregateOn":"Count"}`
	_, err := client.PutInsightRule(ctx, &cloudwatch.PutInsightRuleInput{
		RuleName:       aws.String(name),
		RuleDefinition: aws.String(def),
	})
	require.NoError(t, err)
	defer func() {
		_, _ = client.DeleteInsightRules(ctx, &cloudwatch.DeleteInsightRulesInput{RuleNames: []string{name}})
	}()

	out, err := client.DescribeInsightRules(ctx, &cloudwatch.DescribeInsightRulesInput{})
	require.NoError(t, err)
	var rule *cwtypes.InsightRule
	for i := range out.InsightRules {
		if aws.ToString(out.InsightRules[i].Name) == name {
			rule = &out.InsightRules[i]
		}
	}
	require.NotNil(t, rule, "rule appears in DescribeInsightRules")
	assert.Equal(t, "ENABLED", aws.ToString(rule.State))

	_, err = client.DisableInsightRules(ctx, &cloudwatch.DisableInsightRulesInput{RuleNames: []string{name}})
	require.NoError(t, err)
	out, err = client.DescribeInsightRules(ctx, &cloudwatch.DescribeInsightRulesInput{})
	require.NoError(t, err)
	for i := range out.InsightRules {
		if aws.ToString(out.InsightRules[i].Name) == name {
			assert.Equal(t, "DISABLED", aws.ToString(out.InsightRules[i].State))
		}
	}

	_, err = client.EnableInsightRules(ctx, &cloudwatch.EnableInsightRulesInput{RuleNames: []string{name}})
	require.NoError(t, err)
}

// TestCloudWatch_GetMetricData verifies GetMetricData evaluates MetricDataQueries
// against recorded metrics — real datapoints, and an empty result for a metric
// with no data (no fabricated points).
func TestCloudWatch_GetMetricData(t *testing.T) {
	client := cloudwatchClient()
	ns := "Custom/GetMetricDataSDK"
	base := time.Now().UTC().Truncate(time.Minute)
	_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(ns),
		MetricData: []cwtypes.MetricDatum{
			{MetricName: aws.String("Hits"), Value: aws.Float64(2), Timestamp: aws.Time(base.Add(1 * time.Second))},
			{MetricName: aws.String("Hits"), Value: aws.Float64(4), Timestamp: aws.Time(base.Add(2 * time.Second))},
		},
	})
	require.NoError(t, err)

	q := []cwtypes.MetricDataQuery{{
		Id: aws.String("q1"),
		MetricStat: &cwtypes.MetricStat{
			Metric: &cwtypes.Metric{Namespace: aws.String(ns), MetricName: aws.String("Hits")},
			Period: aws.Int32(60),
			Stat:   aws.String("Sum"),
		},
	}}
	out, err := client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		MetricDataQueries: q,
		StartTime:         aws.Time(base.Add(-time.Minute)),
		EndTime:           aws.Time(base.Add(time.Minute)),
	})
	require.NoError(t, err)
	require.Len(t, out.MetricDataResults, 1)
	require.Len(t, out.MetricDataResults[0].Values, 1)
	assert.InDelta(t, 6, out.MetricDataResults[0].Values[0], 0.001, "Sum of 2 and 4")

	// A metric with no data returns an empty value set, never a fabricated point.
	empty, err := client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		MetricDataQueries: []cwtypes.MetricDataQuery{{
			Id: aws.String("q2"),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{Namespace: aws.String(ns), MetricName: aws.String("NoData")},
				Period: aws.Int32(60),
				Stat:   aws.String("Sum"),
			},
		}},
		StartTime: aws.Time(base.Add(-time.Minute)),
		EndTime:   aws.Time(base.Add(time.Minute)),
	})
	require.NoError(t, err)
	require.Len(t, empty.MetricDataResults, 1)
	assert.Empty(t, empty.MetricDataResults[0].Values)
}

// TestCloudWatch_AlarmMuteRules covers put / get / list / delete of an alarm mute
// rule, including the derived Status and the Rule/MuteTargets round-trip.
func TestCloudWatch_AlarmMuteRules(t *testing.T) {
	client := cloudwatchClient()
	name := "sdk-mute"
	_, err := client.PutAlarmMuteRule(ctx, &cloudwatch.PutAlarmMuteRuleInput{
		Name:        aws.String(name),
		Description: aws.String("nightly maintenance"),
		Rule:        &cwtypes.Rule{Schedule: &cwtypes.Schedule{Expression: aws.String("cron(0 2 * * ? *)"), Duration: aws.String("PT1H")}},
		MuteTargets: &cwtypes.MuteTargets{AlarmNames: []string{"some-alarm"}},
	})
	require.NoError(t, err)
	defer func() {
		_, _ = client.DeleteAlarmMuteRule(ctx, &cloudwatch.DeleteAlarmMuteRuleInput{AlarmMuteRuleName: aws.String(name)})
	}()

	got, err := client.GetAlarmMuteRule(ctx, &cloudwatch.GetAlarmMuteRuleInput{AlarmMuteRuleName: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(got.Name))
	require.NotNil(t, got.Rule)
	require.NotNil(t, got.Rule.Schedule)
	assert.Equal(t, "cron(0 2 * * ? *)", aws.ToString(got.Rule.Schedule.Expression))
	require.NotNil(t, got.MuteTargets)
	assert.Equal(t, []string{"some-alarm"}, got.MuteTargets.AlarmNames)
	assert.Equal(t, cwtypes.AlarmMuteRuleStatusScheduled, got.Status)

	list, err := client.ListAlarmMuteRules(ctx, &cloudwatch.ListAlarmMuteRulesInput{AlarmName: aws.String("some-alarm")})
	require.NoError(t, err)
	require.Len(t, list.AlarmMuteRuleSummaries, 1)
	assert.Contains(t, aws.ToString(list.AlarmMuteRuleSummaries[0].AlarmMuteRuleArn), ":alarm-mute-rule:"+name)

	_, err = client.DeleteAlarmMuteRule(ctx, &cloudwatch.DeleteAlarmMuteRuleInput{AlarmMuteRuleName: aws.String(name)})
	require.NoError(t, err)
	_, err = client.GetAlarmMuteRule(ctx, &cloudwatch.GetAlarmMuteRuleInput{AlarmMuteRuleName: aws.String(name)})
	require.Error(t, err, "deleted mute rule no longer exists")
}

// TestCloudWatch_TagResource covers the cross-resource tagging API over a metric
// stream ARN: tag, list, untag.
func TestCloudWatch_TagResource(t *testing.T) {
	client := cloudwatchClient()
	name := "sdk-stream-tagged"
	firehoseARN, _ := createImmediateFirehose(t, "sdk-stream-tagged-firehose")
	roleARN := createFirehoseServiceRole(t, "sdk-stream-tagged-cloudwatch-role",
		"streams.metrics.cloudwatch.amazonaws.com", []string{"firehose:PutRecord"}, []string{firehoseARN})
	_, err := client.PutMetricStream(ctx, &cloudwatch.PutMetricStreamInput{
		Name:         aws.String(name),
		FirehoseArn:  aws.String(firehoseARN),
		RoleArn:      aws.String(roleARN),
		OutputFormat: cwtypes.MetricStreamOutputFormatJson,
	})
	require.NoError(t, err)
	defer func() {
		_, _ = client.DeleteMetricStream(ctx, &cloudwatch.DeleteMetricStreamInput{Name: aws.String(name)})
	}()
	got, err := client.GetMetricStream(ctx, &cloudwatch.GetMetricStreamInput{Name: aws.String(name)})
	require.NoError(t, err)
	arn := aws.ToString(got.Arn)

	_, err = client.TagResource(ctx, &cloudwatch.TagResourceInput{
		ResourceARN: aws.String(arn),
		Tags:        []cwtypes.Tag{{Key: aws.String("env"), Value: aws.String("prod")}, {Key: aws.String("owner"), Value: aws.String("a")}},
	})
	require.NoError(t, err)
	tags, err := client.ListTagsForResource(ctx, &cloudwatch.ListTagsForResourceInput{ResourceARN: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 2)

	_, err = client.UntagResource(ctx, &cloudwatch.UntagResourceInput{ResourceARN: aws.String(arn), TagKeys: []string{"owner"}})
	require.NoError(t, err)
	tags, err = client.ListTagsForResource(ctx, &cloudwatch.ListTagsForResourceInput{ResourceARN: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "env", aws.ToString(tags.Tags[0].Key))
}
