package aws_sdk_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each kind of alarm keeps the ActionConfiguration it was written with and
// returns it from DescribeAlarms.
func TestCloudWatch_AlarmsKeepTheirActionConfiguration(t *testing.T) {
	t.Parallel()
	client := cloudwatchClient()
	suffix := fmt.Sprint(time.Now().UnixNano())
	metric, composite, logAlarm := "ac-metric-"+suffix, "ac-composite-"+suffix, "ac-log-"+suffix
	t.Cleanup(func() {
		_, _ = client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{composite}})
		_, _ = client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{metric, logAlarm}})
	})

	_, err := client.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(metric),
		Namespace:          aws.String("ActionConfiguration"),
		MetricName:         aws.String("Errors"),
		Statistic:          cwtypes.StatisticSum,
		Period:             aws.Int32(60),
		EvaluationPeriods:  aws.Int32(1),
		Threshold:          aws.Float64(1),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		OKActions:          []string{"arn:aws:sns:us-east-1:000000000000:ok"},
		Tags:               []cwtypes.Tag{{Key: aws.String("team"), Value: aws.String("payments")}},
		ActionConfiguration: &cwtypes.ActionConfiguration{AlarmTagsPropagation: &cwtypes.AlarmTagsPropagation{
			Enabled: aws.Bool(true), IncludedTagKeys: []string{"team"}}},
	})
	require.NoError(t, err)
	_, err = client.PutCompositeAlarm(ctx, &cloudwatch.PutCompositeAlarmInput{
		AlarmName: aws.String(composite),
		AlarmRule: aws.String(fmt.Sprintf("ALARM(%q)", metric)),
		ActionConfiguration: &cwtypes.ActionConfiguration{AlarmTagsPropagation: &cwtypes.AlarmTagsPropagation{
			Enabled: aws.Bool(true), ExcludedTagKeys: []string{"internal"}}},
	})
	require.NoError(t, err)
	group := seedLogAlarmLogGroup(t, cwLogsClient(), "/sdk/action-configuration/"+suffix, nil)
	logInput := putLogAlarmInput(logAlarm, group, "count(*) as hits", 1, cwtypes.ComparisonOperatorGreaterThanThreshold)
	logInput.ActionConfiguration = &cwtypes.ActionConfiguration{AlarmTagsPropagation: &cwtypes.AlarmTagsPropagation{
		Enabled: aws.Bool(false)}}
	_, err = client.PutLogAlarm(ctx, logInput)
	require.NoError(t, err)

	out, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{
		AlarmNames: []string{metric, composite, logAlarm},
		AlarmTypes: []cwtypes.AlarmType{cwtypes.AlarmTypeMetricAlarm, cwtypes.AlarmTypeCompositeAlarm, cwtypes.AlarmTypeLogAlarm},
	})
	require.NoError(t, err)
	require.Len(t, out.MetricAlarms, 1)
	require.Len(t, out.CompositeAlarms, 1)
	require.Len(t, out.LogAlarms, 1)

	ma := out.MetricAlarms[0]
	assert.Equal(t, []string{"arn:aws:sns:us-east-1:000000000000:ok"}, ma.OKActions)
	require.NotNil(t, ma.ActionConfiguration)
	require.NotNil(t, ma.ActionConfiguration.AlarmTagsPropagation)
	assert.True(t, aws.ToBool(ma.ActionConfiguration.AlarmTagsPropagation.Enabled))
	assert.Equal(t, []string{"team"}, ma.ActionConfiguration.AlarmTagsPropagation.IncludedTagKeys)

	ca := out.CompositeAlarms[0]
	require.NotNil(t, ca.ActionConfiguration)
	require.NotNil(t, ca.ActionConfiguration.AlarmTagsPropagation)
	assert.Equal(t, []string{"internal"}, ca.ActionConfiguration.AlarmTagsPropagation.ExcludedTagKeys)

	la := out.LogAlarms[0]
	require.NotNil(t, la.ActionConfiguration)
	require.NotNil(t, la.ActionConfiguration.AlarmTagsPropagation)
	assert.False(t, aws.ToBool(la.ActionConfiguration.AlarmTagsPropagation.Enabled))

	// The two key lists exclude each other.
	_, err = client.PutCompositeAlarm(ctx, &cloudwatch.PutCompositeAlarmInput{
		AlarmName: aws.String(composite),
		AlarmRule: aws.String(fmt.Sprintf("ALARM(%q)", metric)),
		ActionConfiguration: &cwtypes.ActionConfiguration{AlarmTagsPropagation: &cwtypes.AlarmTagsPropagation{
			Enabled: aws.Bool(true), IncludedTagKeys: []string{"team"}, ExcludedTagKeys: []string{"internal"}}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidParameterCombination")
}

// Enabling tag propagation reads the alarm's tags, so the write also needs
// cloudwatch:ListTagsForResource on the alarm; a write that leaves it off does
// not.
func TestCloudWatch_EnablingAlarmTagsPropagationNeedsListTagsForResource(t *testing.T) {
	t.Parallel()
	iamc := iamClient()
	user := uniqueName("cw-tags-propagation")
	_, err := iamc.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = iamc.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })
	_, err = iamc.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:   aws.String(user),
		PolicyName: aws.String("put-alarms-only"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{
			"Effect":"Allow","Action":["cloudwatch:PutMetricAlarm","cloudwatch:DeleteAlarms"],"Resource":"*"}]}`),
	})
	require.NoError(t, err)
	key, err := iamc.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	restricted := cloudwatch.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(
			aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")},
		func(o *cloudwatch.Options) { o.BaseEndpoint = aws.String(baseURL) })

	name := "ac-iam-" + fmt.Sprint(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = cloudwatchClient().DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}})
	})
	put := func(enabled bool) error {
		_, err := restricted.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
			AlarmName:          aws.String(name),
			Namespace:          aws.String("ActionConfiguration"),
			MetricName:         aws.String("Errors"),
			Statistic:          cwtypes.StatisticSum,
			Period:             aws.Int32(60),
			EvaluationPeriods:  aws.Int32(1),
			Threshold:          aws.Float64(1),
			ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
			ActionConfiguration: &cwtypes.ActionConfiguration{AlarmTagsPropagation: &cwtypes.AlarmTagsPropagation{
				Enabled: aws.Bool(enabled)}},
		})
		return err
	}
	require.NoError(t, put(false), "a write that leaves tag propagation off needs only cloudwatch:PutMetricAlarm")
	err = put(true)
	require.Error(t, err, "enabling tag propagation without cloudwatch:ListTagsForResource is refused")
	assert.Contains(t, err.Error(), "cloudwatch:ListTagsForResource")
}
