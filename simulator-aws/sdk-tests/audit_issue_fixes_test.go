package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_CreateVolumeRequiresAZ expects MissingParameter rather than a
// defaulted Availability Zone.
func TestEC2_CreateVolumeRequiresAZ(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	_, err := c.CreateVolume(ctx, &ec2.CreateVolumeInput{Size: aws.Int32(8)})
	assertAWSAPIErrorCode(t, err, "MissingParameter")

	_, err = c.CreateVolume(ctx, &ec2.CreateVolumeInput{
		Size: aws.Int32(8), AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err, "with an AvailabilityZone the request succeeds")
}

// TestECS_UnknownClusterRaisesClusterNotFound expects ClusterNotFoundException
// from cluster-scoped operations, which tells an unknown cluster from an empty
// one or a missing task.
func TestECS_UnknownClusterRaisesClusterNotFound(t *testing.T) {
	t.Parallel()
	c := ecsClient()
	const missing = "edd-nonexistent-xyz"
	taskARN := "arn:aws:ecs:us-east-1:123456789012:task/" + missing + "/abc123"

	_, err := c.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(missing), Tasks: []string{taskARN},
	})
	assertAWSAPIErrorCode(t, err, "ClusterNotFoundException")

	_, err = c.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String(missing)})
	assertAWSAPIErrorCode(t, err, "ClusterNotFoundException")

	_, err = c.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(missing), Task: aws.String(taskARN),
	})
	assertAWSAPIErrorCode(t, err, "ClusterNotFoundException")
}

// TestEC2_DescribeSnapshotsPagination expects MaxResults to cap the page and a
// NextToken to retrieve the rest.
func TestEC2_DescribeSnapshotsPagination(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vol, err := c.CreateVolume(ctx, &ec2.CreateVolumeInput{
		Size: aws.Int32(1), AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	for i := 0; i < 6; i++ {
		_, err := c.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{VolumeId: vol.VolumeId})
		require.NoError(t, err)
	}

	page, err := c.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"}, MaxResults: aws.Int32(5),
	})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(page.Snapshots), 5, "first page is capped at MaxResults")
	require.NotNil(t, page.NextToken, "NextToken is present when results remain")

	next, err := c.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"}, MaxResults: aws.Int32(5), NextToken: page.NextToken,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, next.Snapshots, "the remainder is retrievable via NextToken")
}
