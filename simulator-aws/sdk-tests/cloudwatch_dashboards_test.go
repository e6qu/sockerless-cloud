package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_Dashboards covers PutDashboard / GetDashboard / ListDashboards /
// DeleteDashboards (issue #608 — the API was unrouted → 404).
func TestCloudWatch_Dashboards(t *testing.T) {
	c := cloudwatchClient()
	body := `{"widgets":[{"type":"text","x":0,"y":0,"width":6,"height":2,"properties":{"markdown":"hi"}}]}`

	_, err := c.PutDashboard(ctx, &cloudwatch.PutDashboardInput{
		DashboardName: aws.String("ops"), DashboardBody: aws.String(body),
	})
	require.NoError(t, err)
	defer c.DeleteDashboards(ctx, &cloudwatch.DeleteDashboardsInput{DashboardNames: []string{"ops"}})

	get, err := c.GetDashboard(ctx, &cloudwatch.GetDashboardInput{DashboardName: aws.String("ops")})
	require.NoError(t, err)
	assert.JSONEq(t, body, aws.ToString(get.DashboardBody))
	assert.Contains(t, aws.ToString(get.DashboardArn), ":dashboard/ops")

	list, err := c.ListDashboards(ctx, &cloudwatch.ListDashboardsInput{})
	require.NoError(t, err)
	found := false
	for _, e := range list.DashboardEntries {
		if aws.ToString(e.DashboardName) == "ops" {
			found = true
		}
	}
	assert.True(t, found, "ListDashboards must include the created dashboard")

	_, err = c.DeleteDashboards(ctx, &cloudwatch.DeleteDashboardsInput{DashboardNames: []string{"ops"}})
	require.NoError(t, err)
	_, err = c.GetDashboard(ctx, &cloudwatch.GetDashboardInput{DashboardName: aws.String("ops")})
	require.Error(t, err, "a deleted dashboard must not be gettable")
	// The error must deserialize as a proper rpc-v2-cbor API error (it carries the
	// Smithy-Protocol header + a cbor __type/message body) — not a protocol-header
	// parse failure, which an awsJson-shaped error response would have produced.
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "want a deserialized cbor API error, got: %v", err)
	assert.Equal(t, "ResourceNotFound", apiErr.ErrorCode())
}

// TestCloudWatch_AlarmExtendedStatistic covers the percentile ExtendedStatistic
// round-trip (issue #609 — it was dropped → terraform perpetual diff). A
// percentile alarm returns ExtendedStatistic and no Statistic.
func TestCloudWatch_AlarmExtendedStatistic(t *testing.T) {
	c := cloudwatchClient()
	_, err := c.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String("p99-probe"),
		Namespace:          aws.String("edd/x"),
		MetricName:         aws.String("latency"),
		ExtendedStatistic:  aws.String("p99"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(300),
		Threshold:          aws.Float64(120000),
		TreatMissingData:   aws.String("notBreaching"),
	})
	require.NoError(t, err)
	defer c.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{"p99-probe"}})

	out, err := c.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{"p99-probe"}})
	require.NoError(t, err)
	require.Len(t, out.MetricAlarms, 1)
	assert.Equal(t, "p99", aws.ToString(out.MetricAlarms[0].ExtendedStatistic))
	assert.Empty(t, string(out.MetricAlarms[0].Statistic), "Statistic must be absent for a percentile alarm")
}
