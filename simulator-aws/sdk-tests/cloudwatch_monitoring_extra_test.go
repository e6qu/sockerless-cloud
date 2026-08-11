package aws_sdk_test

import (
	"bytes"
	"image/png"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_DatasetKmsKey covers the metrics-dataset KMS lifecycle:
// GetDataset materializes the dataset (id + ARN), AssociateDatasetKmsKey sets
// the CMK, GetDataset reads it back, and DisassociateDatasetKmsKey clears it.
func TestCloudWatch_DatasetKmsKey(t *testing.T) {
	client := cloudwatchClient()
	id := "sdk-dataset-1"
	kms := "arn:aws:kms:us-east-1:000000000000:key/11111111-2222-3333-4444-555555555555"

	got, err := client.GetDataset(ctx, &cloudwatch.GetDatasetInput{DatasetIdentifier: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(got.DatasetId))
	assert.Contains(t, aws.ToString(got.Arn), ":dataset/"+id)
	assert.Empty(t, aws.ToString(got.KmsKeyArn), "no key before association")

	_, err = client.AssociateDatasetKmsKey(ctx, &cloudwatch.AssociateDatasetKmsKeyInput{
		DatasetIdentifier: aws.String(id),
		KmsKeyArn:         aws.String(kms),
	})
	require.NoError(t, err)

	got, err = client.GetDataset(ctx, &cloudwatch.GetDatasetInput{DatasetIdentifier: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, kms, aws.ToString(got.KmsKeyArn), "associated CMK reads back")

	_, err = client.DisassociateDatasetKmsKey(ctx, &cloudwatch.DisassociateDatasetKmsKeyInput{
		DatasetIdentifier: aws.String(id),
	})
	require.NoError(t, err)

	got, err = client.GetDataset(ctx, &cloudwatch.GetDatasetInput{DatasetIdentifier: aws.String(id)})
	require.NoError(t, err)
	assert.Empty(t, aws.ToString(got.KmsKeyArn), "key cleared after disassociate")
}

// TestCloudWatch_OTelEnrichment covers the account-level OpenTelemetry
// enrichment state: Start flips the status to Running, Stop flips it to Stopped,
// and Get reads the current value.
func TestCloudWatch_OTelEnrichment(t *testing.T) {
	client := cloudwatchClient()

	_, err := client.StartOTelEnrichment(ctx, &cloudwatch.StartOTelEnrichmentInput{})
	require.NoError(t, err)
	got, err := client.GetOTelEnrichment(ctx, &cloudwatch.GetOTelEnrichmentInput{})
	require.NoError(t, err)
	assert.Equal(t, cwtypes.OTelEnrichmentStatusRunning, got.Status)

	_, err = client.StopOTelEnrichment(ctx, &cloudwatch.StopOTelEnrichmentInput{})
	require.NoError(t, err)
	got, err = client.GetOTelEnrichment(ctx, &cloudwatch.GetOTelEnrichmentInput{})
	require.NoError(t, err)
	assert.Equal(t, cwtypes.OTelEnrichmentStatusStopped, got.Status)
}

// TestCloudWatch_ManagedInsightRules covers PutManagedInsightRules creating a
// managed rule over a resource ARN and ListManagedInsightRules reading it back
// with its template name and rule state.
func TestCloudWatch_ManagedInsightRules(t *testing.T) {
	client := cloudwatchClient()
	resourceARN := "arn:aws:ec2:us-east-1:000000000000:vpc/vpc-abc123"

	_, err := client.PutManagedInsightRules(ctx, &cloudwatch.PutManagedInsightRulesInput{
		ManagedRules: []cwtypes.ManagedRule{
			{
				TemplateName: aws.String("VPC-Flow-Logs-By-Source"),
				ResourceARN:  aws.String(resourceARN),
				Tags:         []cwtypes.Tag{{Key: aws.String("team"), Value: aws.String("net")}},
			},
		},
	})
	require.NoError(t, err)

	out, err := client.ListManagedInsightRules(ctx, &cloudwatch.ListManagedInsightRulesInput{
		ResourceARN: aws.String(resourceARN),
	})
	require.NoError(t, err)
	require.Len(t, out.ManagedRules, 1)
	assert.Equal(t, "VPC-Flow-Logs-By-Source", aws.ToString(out.ManagedRules[0].TemplateName))
	assert.Equal(t, resourceARN, aws.ToString(out.ManagedRules[0].ResourceARN))
	require.NotNil(t, out.ManagedRules[0].RuleState)
	assert.Equal(t, "ENABLED", aws.ToString(out.ManagedRules[0].RuleState.State))
}

// TestCloudWatch_InsightRuleReport covers GetInsightRuleReport over an existing
// insight rule: the report has the real shape with honestly-empty contributors
// and per-period metric datapoints with zero counts (no contributor data is
// ingested by the sim).
func TestCloudWatch_InsightRuleReport(t *testing.T) {
	client := cloudwatchClient()
	name := "sdk-report-rule"
	def := `{"Schema":{"Name":"CloudWatchLogRule","Version":1},"LogGroupNames":["/aws/lambda/x"],"Contribution":{"Keys":["$.requestId"]},"AggregateOn":"Count"}`
	_, err := client.PutInsightRule(ctx, &cloudwatch.PutInsightRuleInput{
		RuleName:       aws.String(name),
		RuleDefinition: aws.String(def),
	})
	require.NoError(t, err)
	defer func() {
		_, _ = client.DeleteInsightRules(ctx, &cloudwatch.DeleteInsightRulesInput{RuleNames: []string{name}})
	}()

	end := time.Now().UTC().Truncate(time.Minute)
	start := end.Add(-5 * time.Minute)
	rep, err := client.GetInsightRuleReport(ctx, &cloudwatch.GetInsightRuleReportInput{
		RuleName:  aws.String(name),
		StartTime: aws.Time(start),
		EndTime:   aws.Time(end),
		Period:    aws.Int32(60),
		Metrics:   []string{"Sum", "Average"},
	})
	require.NoError(t, err)
	assert.Empty(t, rep.Contributors, "no ingested contributor data")
	assert.Equal(t, float64(0), aws.ToFloat64(rep.AggregateValue))
	assert.Equal(t, int64(0), aws.ToInt64(rep.ApproximateUniqueCount))
	require.Len(t, rep.MetricDatapoints, 5, "one datapoint per minute over the 5-minute window")

	// An unknown rule is a real not-found error, not an empty report.
	_, err = client.GetInsightRuleReport(ctx, &cloudwatch.GetInsightRuleReportInput{
		RuleName:  aws.String("does-not-exist-rule"),
		StartTime: aws.Time(start),
		EndTime:   aws.Time(end),
		Period:    aws.Int32(60),
	})
	require.Error(t, err)
}

// TestCloudWatch_AlarmContributors covers DescribeAlarmContributors over an
// existing alarm: the contributor list is honestly empty for a single-metric
// alarm, and an unknown alarm is a not-found error.
func TestCloudWatch_AlarmContributors(t *testing.T) {
	client := cloudwatchClient()
	name := "sdk-alarm-contributors"
	_, err := client.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(name),
		Namespace:          aws.String("Custom/ContribSDK"),
		MetricName:         aws.String("M"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Threshold:          aws.Float64(0),
		Statistic:          cwtypes.StatisticSum,
	})
	require.NoError(t, err)
	defer func() { _, _ = client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}}) }()

	out, err := client.DescribeAlarmContributors(ctx, &cloudwatch.DescribeAlarmContributorsInput{
		AlarmName: aws.String(name),
	})
	require.NoError(t, err)
	assert.Empty(t, out.AlarmContributors, "single-metric alarm has no contributor fan-out")

	_, err = client.DescribeAlarmContributors(ctx, &cloudwatch.DescribeAlarmContributorsInput{
		AlarmName: aws.String("no-such-alarm"),
	})
	require.Error(t, err)
}

// TestCloudWatch_MetricWidgetImage verifies GetMetricWidgetImage returns real,
// decodable PNG bytes sized from the widget definition's width/height.
func TestCloudWatch_MetricWidgetImage(t *testing.T) {
	client := cloudwatchClient()
	widget := `{"metrics":[["AWS/EC2","CPUUtilization","InstanceId","i-1234567890abcdef0"]],"width":300,"height":200,"period":300,"stat":"Average"}`

	out, err := client.GetMetricWidgetImage(ctx, &cloudwatch.GetMetricWidgetImageInput{
		MetricWidget: aws.String(widget),
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.MetricWidgetImage, "API returns image bytes")

	img, err := png.Decode(bytes.NewReader(out.MetricWidgetImage))
	require.NoError(t, err, "returned bytes are a valid PNG")
	assert.Equal(t, 300, img.Bounds().Dx(), "image width matches the widget")
	assert.Equal(t, 200, img.Bounds().Dy(), "image height matches the widget")

	// A malformed widget JSON is rejected, not silently rendered.
	_, err = client.GetMetricWidgetImage(ctx, &cloudwatch.GetMetricWidgetImageInput{
		MetricWidget: aws.String("{not json"),
	})
	require.Error(t, err)
}
