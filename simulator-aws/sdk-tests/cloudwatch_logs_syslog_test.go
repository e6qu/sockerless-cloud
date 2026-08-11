package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloudWatchLogs_StorageTierAndSyslog_SDK(t *testing.T) {
	client := cwLogsStreamClient()
	group := "/sdk/syslog"
	_, err := client.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	})

	putPolicy, err := client.PutStorageTierPolicy(ctx, &cloudwatchlogs.PutStorageTierPolicyInput{
		StorageTier: cwltypes.StorageTierIntelligentTiering,
	})
	require.NoError(t, err)
	assert.Equal(t, cwltypes.StorageTierIntelligentTiering, putPolicy.StorageTier)
	require.NotZero(t, aws.ToInt64(putPolicy.LastUpdatedTime))

	getPolicy, err := client.GetStorageTierPolicy(ctx, &cloudwatchlogs.GetStorageTierPolicyInput{})
	require.NoError(t, err)
	assert.Equal(t, cwltypes.StorageTierIntelligentTiering, getPolicy.StorageTier)

	_, err = client.PutSyslogConfiguration(ctx, &cloudwatchlogs.PutSyslogConfigurationInput{
		LogGroupIdentifier: aws.String(group),
	})
	require.NoError(t, err)
	listed, err := client.ListSyslogConfigurations(ctx, &cloudwatchlogs.ListSyslogConfigurationsInput{
		LogGroupIdentifier: aws.String(group),
	})
	require.NoError(t, err)
	require.Len(t, listed.SyslogConfigurations, 1)
	assert.Equal(t, cwltypes.SyslogSourceTypeVpce, listed.SyslogConfigurations[0].SourceType)
	assert.Contains(t, aws.ToString(listed.SyslogConfigurations[0].LogGroupArn), group)

	_, err = client.DeleteSyslogConfiguration(ctx, &cloudwatchlogs.DeleteSyslogConfigurationInput{
		LogGroupIdentifier: aws.String(group),
	})
	require.NoError(t, err)
	listed, err = client.ListSyslogConfigurations(ctx, &cloudwatchlogs.ListSyslogConfigurationsInput{
		LogGroupIdentifier: aws.String(group),
	})
	require.NoError(t, err)
	assert.Empty(t, listed.SyslogConfigurations)
}
