package aws_sdk_test

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createVMImportRole creates the service role VM Import/Export assumes to
// read disk images from the bucket.
func createVMImportRole(t *testing.T, name, bucket string) {
	t.Helper()
	iamc := iamClient()
	_, err := iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String(name),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Principal":{"Service":"vmie.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	_, err = iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:   aws.String(name),
		PolicyName: aws.String("read-disk-images"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Action":["s3:GetObject","s3:GetBucketLocation","s3:ListBucket"],` +
			`"Resource":["arn:aws:s3:::` + bucket + `","arn:aws:s3:::` + bucket + `/*"]}]}`),
	})
	require.NoError(t, err)
}

// TestEC2_ImportSnapshotReadsTheDiskImage imports a RAW disk image from
// Amazon S3: the task completes behind the call, the SDK's SnapshotImported
// waiter observes it, and the snapshot is sized from the image.
func TestEC2_ImportSnapshotReadsTheDiskImage(t *testing.T) {
	c := ec2Client()
	s3c := s3Client()
	suffix := fmt.Sprint(time.Now().UnixNano())
	bucket := "import-snap-" + suffix
	role := "vmimport-" + suffix
	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	createVMImportRole(t, role, bucket)
	disk := bytes.Repeat([]byte("sockerless-disk-"), 3<<20/16)
	_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("disk.raw"), Body: bytes.NewReader(disk),
	})
	require.NoError(t, err)

	_, err = c.ImportSnapshot(ctx, &ec2.ImportSnapshotInput{
		DiskContainer: &ec2types.SnapshotDiskContainer{
			Format:     aws.String("RAW"),
			UserBucket: &ec2types.UserBucket{S3Bucket: aws.String(bucket), S3Key: aws.String("disk.raw")},
		},
		RoleName: aws.String("vmimport-missing-" + suffix),
	})
	requireAWSErrorCode(t, err, "InvalidParameter")

	started, err := c.ImportSnapshot(ctx, &ec2.ImportSnapshotInput{
		Description: aws.String("imported-snap"),
		DiskContainer: &ec2types.SnapshotDiskContainer{
			Format:     aws.String("RAW"),
			UserBucket: &ec2types.UserBucket{S3Bucket: aws.String(bucket), S3Key: aws.String("disk.raw")},
		},
		RoleName: aws.String(role),
	})
	require.NoError(t, err)
	taskID := aws.ToString(started.ImportTaskId)
	require.NotEmpty(t, taskID)
	require.NotNil(t, started.SnapshotTaskDetail)

	imported, err := ec2.NewSnapshotImportedWaiter(c, func(o *ec2.SnapshotImportedWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).WaitForOutput(ctx, &ec2.DescribeImportSnapshotTasksInput{ImportTaskIds: []string{taskID}}, time.Minute)
	require.NoError(t, err)
	require.Len(t, imported.ImportSnapshotTasks, 1)
	detail := imported.ImportSnapshotTasks[0].SnapshotTaskDetail
	assert.Equal(t, "completed", aws.ToString(detail.Status))
	assert.Equal(t, float64(len(disk)), aws.ToFloat64(detail.DiskImageSize))
	assert.Equal(t, bucket, aws.ToString(detail.UserBucket.S3Bucket))
	snapshotID := aws.ToString(detail.SnapshotId)
	require.NotEmpty(t, snapshotID)

	snapshots, err := c.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{snapshotID}})
	require.NoError(t, err)
	require.Len(t, snapshots.Snapshots, 1)
	assert.Equal(t, int32(1), aws.ToInt32(snapshots.Snapshots[0].VolumeSize), "a 3 MiB disk rounds up to a 1 GiB snapshot")
	assert.Equal(t, ec2types.SnapshotStateCompleted, snapshots.Snapshots[0].State)
	assert.Equal(t, "vol-ffffffff", aws.ToString(snapshots.Snapshots[0].VolumeId))
}

// TestEC2_ImportSnapshotRejectsAnImageNotInItsFormat declares raw bytes a VMDK:
// the task fails with the disk validation error, which the SDK's
// SnapshotImported waiter reports as its failure state.
func TestEC2_ImportSnapshotRejectsAnImageNotInItsFormat(t *testing.T) {
	c := ec2Client()
	s3c := s3Client()
	suffix := fmt.Sprint(time.Now().UnixNano())
	bucket := "import-bad-" + suffix
	role := "vmimport-bad-" + suffix
	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	createVMImportRole(t, role, bucket)
	_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("disk.vmdk"), Body: bytes.NewReader(bytes.Repeat([]byte{7}, 4096)),
	})
	require.NoError(t, err)

	started, err := c.ImportSnapshot(ctx, &ec2.ImportSnapshotInput{
		DiskContainer: &ec2types.SnapshotDiskContainer{
			Format: aws.String("VMDK"),
			Url:    aws.String("s3://" + bucket + "/disk.vmdk"),
		},
		RoleName: aws.String(role),
	})
	require.NoError(t, err)
	taskID := aws.ToString(started.ImportTaskId)
	_, err = ec2.NewSnapshotImportedWaiter(c, func(o *ec2.SnapshotImportedWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).WaitForOutput(ctx, &ec2.DescribeImportSnapshotTasksInput{ImportTaskIds: []string{taskID}}, time.Minute)
	require.ErrorContains(t, err, "waiter state transitioned to Failure")

	tasks, err := c.DescribeImportSnapshotTasks(ctx, &ec2.DescribeImportSnapshotTasksInput{ImportTaskIds: []string{taskID}})
	require.NoError(t, err)
	require.Len(t, tasks.ImportSnapshotTasks, 1)
	assert.Equal(t, "ClientError: Disk validation failed [Unsupported VMDK File Format]",
		aws.ToString(tasks.ImportSnapshotTasks[0].SnapshotTaskDetail.StatusMessage))
}
