package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatchLogs_KmsKey covers CreateLogGroup persists kmsKeyId,
// and AssociateKmsKey / DisassociateKmsKey update it.
func TestCloudWatchLogs_KmsKey(t *testing.T) {
	c := cwLogsClient()
	name := "/probe/kms-test"
	kms1 := "arn:aws:kms:us-east-1:123456789012:key/d8ce7e2f-fc3e-4b45-0ff0-7d4b53ff3a40"

	_, err := c.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(name),
		KmsKeyId:     aws.String(kms1),
	})
	require.NoError(t, err)

	desc, err := c.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, desc.LogGroups, 1)
	assert.Equal(t, kms1, aws.ToString(desc.LogGroups[0].KmsKeyId), "kms key set at create must round-trip")

	// AssociateKmsKey swaps the key on the existing group.
	kms2 := "arn:aws:kms:us-east-1:123456789012:key/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	_, err = c.AssociateKmsKey(ctx, &cloudwatchlogs.AssociateKmsKeyInput{
		LogGroupName: aws.String(name),
		KmsKeyId:     aws.String(kms2),
	})
	require.NoError(t, err)
	desc, err = c.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, kms2, aws.ToString(desc.LogGroups[0].KmsKeyId))

	// DisassociateKmsKey clears it.
	_, err = c.DisassociateKmsKey(ctx, &cloudwatchlogs.DisassociateKmsKeyInput{LogGroupName: aws.String(name)})
	require.NoError(t, err)
	desc, err = c.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(name)})
	require.NoError(t, err)
	assert.Empty(t, aws.ToString(desc.LogGroups[0].KmsKeyId))
}
