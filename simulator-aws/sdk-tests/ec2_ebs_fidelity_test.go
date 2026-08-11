package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func describeOneVolume(t *testing.T, c *ec2.Client, id string) types.Volume {
	t.Helper()
	out, err := c.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{id}})
	require.NoError(t, err)
	require.Len(t, out.Volumes, 1)
	return out.Volumes[0]
}

// TestEC2_EBSVolumeFidelitySDK covers the EBS Volume/Snapshot response fields
// (iops/throughput/kms/encrypted) and DescribeVolumesModifications — all
// previously dropped, so aws_ebs_volume drifted on non-default configs and
// volume resize updates errored (UnknownOperation).
func TestEC2_EBSVolumeFidelitySDK(t *testing.T) {
	c := ec2Client()

	// gp3 with no iops/throughput → AWS returns the defaults 3000/125.
	gp3, err := c.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(20), VolumeType: types.VolumeTypeGp3,
	})
	require.NoError(t, err)
	got := describeOneVolume(t, c, aws.ToString(gp3.VolumeId))
	assert.Equal(t, int32(3000), aws.ToInt32(got.Iops), "gp3 iops defaults to 3000")
	assert.Equal(t, int32(125), aws.ToInt32(got.Throughput), "gp3 throughput defaults to 125")

	// io1 with explicit iops + encryption with a CMK.
	io1, err := c.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(10), VolumeType: types.VolumeTypeIo1,
		Iops:      aws.Int32(1000),
		Encrypted: aws.Bool(true),
		KmsKeyId:  aws.String("arn:aws:kms:us-east-1:123456789012:key/abc"),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeVolume,
			Tags:         []types.Tag{{Key: aws.String("env"), Value: aws.String("prod")}},
		}},
	})
	require.NoError(t, err)
	io1ID := aws.ToString(io1.VolumeId)
	got = describeOneVolume(t, c, io1ID)
	assert.Equal(t, int32(1000), aws.ToInt32(got.Iops), "io1 iops must round-trip")
	assert.True(t, aws.ToBool(got.Encrypted))
	assert.Contains(t, aws.ToString(got.KmsKeyId), "key/abc")

	// DescribeVolumes filter by volume-type + tag.
	byType, err := c.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []types.Filter{
			{Name: aws.String("volume-type"), Values: []string{"io1"}},
			{Name: aws.String("tag:env"), Values: []string{"prod"}},
		},
	})
	require.NoError(t, err)
	require.Len(t, byType.Volumes, 1, "type+tag filter must scope to the one io1 prod volume")
	assert.Equal(t, io1ID, aws.ToString(byType.Volumes[0].VolumeId))

	// ModifyVolume → DescribeVolumesModifications returns the completed modification.
	_, err = c.ModifyVolume(ctx, &ec2.ModifyVolumeInput{
		VolumeId: aws.String(io1ID), Size: aws.Int32(40), Iops: aws.Int32(2000),
	})
	require.NoError(t, err)
	mods, err := c.DescribeVolumesModifications(ctx, &ec2.DescribeVolumesModificationsInput{VolumeIds: []string{io1ID}})
	require.NoError(t, err)
	require.Len(t, mods.VolumesModifications, 1, "DescribeVolumesModifications must return the modification")
	m := mods.VolumesModifications[0]
	assert.Equal(t, types.VolumeModificationStateCompleted, m.ModificationState)
	assert.Equal(t, int32(40), aws.ToInt32(m.TargetSize))
	assert.Equal(t, int32(2000), aws.ToInt32(m.TargetIops))
	assert.Equal(t, int32(10), aws.ToInt32(m.OriginalSize), "the io1 volume started at 10 GiB")
}

// TestEC2_EBSSnapshotFidelitySDK covers Snapshot.Encrypted/KmsKeyId (inherited
// from the source volume) and DescribeSnapshots filtering.
func TestEC2_EBSSnapshotFidelitySDK(t *testing.T) {
	c := ec2Client()
	vol, err := c.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(8), VolumeType: types.VolumeTypeGp3,
		Encrypted: aws.Bool(true), KmsKeyId: aws.String("arn:aws:kms:us-east-1:123456789012:key/snap"),
	})
	require.NoError(t, err)
	volID := aws.ToString(vol.VolumeId)

	snap, err := c.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{VolumeId: aws.String(volID)})
	require.NoError(t, err)
	snapID := aws.ToString(snap.SnapshotId)

	out, err := c.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{
		Filters: []types.Filter{{Name: aws.String("volume-id"), Values: []string{volID}}},
	})
	require.NoError(t, err)
	require.Len(t, out.Snapshots, 1, "volume-id filter must scope to the one snapshot")
	assert.Equal(t, snapID, aws.ToString(out.Snapshots[0].SnapshotId))
	assert.True(t, aws.ToBool(out.Snapshots[0].Encrypted), "snapshot inherits encryption from the source volume")
	assert.Contains(t, aws.ToString(out.Snapshots[0].KmsKeyId), "key/snap")
}

// TestEC2_CopySnapshotFidelitySDK covers CopySnapshot — the cross-region EBS DR
// primitive: a copy gets a fresh snapshot id, inherits the source's size +
// encryption, takes the supplied description, and lists via DescribeSnapshots.
func TestEC2_CopySnapshotFidelitySDK(t *testing.T) {
	c := ec2Client()
	vol, err := c.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(4), VolumeType: types.VolumeTypeGp3,
		Encrypted: aws.Bool(true), KmsKeyId: aws.String("arn:aws:kms:us-east-1:123456789012:key/copy"),
	})
	require.NoError(t, err)
	snap, err := c.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{VolumeId: vol.VolumeId})
	require.NoError(t, err)
	srcID := aws.ToString(snap.SnapshotId)

	cp, err := c.CopySnapshot(ctx, &ec2.CopySnapshotInput{
		SourceRegion:     aws.String("us-east-1"),
		SourceSnapshotId: aws.String(srcID),
		Description:      aws.String("DR copy"),
	})
	require.NoError(t, err)
	copyID := aws.ToString(cp.SnapshotId)
	require.NotEmpty(t, copyID)
	assert.NotEqual(t, srcID, copyID, "copy must get its own snapshot id")

	out, err := c.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{copyID}})
	require.NoError(t, err)
	require.Len(t, out.Snapshots, 1)
	assert.Equal(t, "DR copy", aws.ToString(out.Snapshots[0].Description))
	assert.True(t, aws.ToBool(out.Snapshots[0].Encrypted), "copy inherits encryption from the source")
	assert.Equal(t, int32(4), aws.ToInt32(out.Snapshots[0].VolumeSize))
}
