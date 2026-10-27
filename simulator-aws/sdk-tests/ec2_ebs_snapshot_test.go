package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createSnapshotForEBS(t *testing.T, c *ec2.Client) string {
	t.Helper()
	vol, err := c.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(8),
	})
	require.NoError(t, err)
	snap, err := c.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{VolumeId: vol.VolumeId})
	require.NoError(t, err)
	return aws.ToString(snap.SnapshotId)
}

// TestEC2_EbsEncryptionByDefaultSDK covers the account-level encryption flag
// and default KMS key; Reset returns to the AWS managed alias/aws/ebs key.
func TestEC2_EbsEncryptionByDefaultSDK(t *testing.T) {
	c := ec2Client()

	en, err := c.EnableEbsEncryptionByDefault(ctx, &ec2.EnableEbsEncryptionByDefaultInput{})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(en.EbsEncryptionByDefault))

	got, err := c.GetEbsEncryptionByDefault(ctx, &ec2.GetEbsEncryptionByDefaultInput{})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(got.EbsEncryptionByDefault))

	dis, err := c.DisableEbsEncryptionByDefault(ctx, &ec2.DisableEbsEncryptionByDefaultInput{})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(dis.EbsEncryptionByDefault))

	got2, err := c.GetEbsEncryptionByDefault(ctx, &ec2.GetEbsEncryptionByDefaultInput{})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(got2.EbsEncryptionByDefault))

	const cmk = "arn:aws:kms:us-east-1:000000000000:key/abcd1234-1111-2222-3333-444455556666"
	mod, err := c.ModifyEbsDefaultKmsKeyId(ctx, &ec2.ModifyEbsDefaultKmsKeyIdInput{KmsKeyId: aws.String(cmk)})
	require.NoError(t, err)
	assert.Equal(t, cmk, aws.ToString(mod.KmsKeyId))

	gotKey, err := c.GetEbsDefaultKmsKeyId(ctx, &ec2.GetEbsDefaultKmsKeyIdInput{})
	require.NoError(t, err)
	assert.Equal(t, cmk, aws.ToString(gotKey.KmsKeyId))

	reset, err := c.ResetEbsDefaultKmsKeyId(ctx, &ec2.ResetEbsDefaultKmsKeyIdInput{})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(reset.KmsKeyId), "aws/ebs", "Reset reverts to the AWS-managed key")
}

// TestEC2_FastSnapshotRestoresSDK waits for fast snapshot restores to settle
// to enabled.
func TestEC2_FastSnapshotRestoresSDK(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	snapID := createSnapshotForEBS(t, c)

	en, err := c.EnableFastSnapshotRestores(ctx, &ec2.EnableFastSnapshotRestoresInput{
		SourceSnapshotIds: []string{snapID},
		AvailabilityZones: []string{"us-east-1a", "us-east-1b"},
	})
	require.NoError(t, err)
	require.Len(t, en.Successful, 2)
	assert.Empty(t, en.Unsuccessful)
	assert.Equal(t, snapID, aws.ToString(en.Successful[0].SnapshotId))
	assert.NotEmpty(t, string(en.Successful[0].State))

	badEn, err := c.EnableFastSnapshotRestores(ctx, &ec2.EnableFastSnapshotRestoresInput{
		SourceSnapshotIds: []string{"snap-ffffffffffffffff"},
		AvailabilityZones: []string{"us-east-1a"},
	})
	require.NoError(t, err)
	require.Len(t, badEn.Unsuccessful, 1)
	assert.Equal(t, "snap-ffffffffffffffff", aws.ToString(badEn.Unsuccessful[0].SnapshotId))
	require.Len(t, badEn.Unsuccessful[0].FastSnapshotRestoreStateErrors, 1)

	desc, err := c.DescribeFastSnapshotRestores(ctx, &ec2.DescribeFastSnapshotRestoresInput{
		Filters: []types.Filter{{Name: aws.String("snapshot-id"), Values: []string{snapID}}},
	})
	require.NoError(t, err)
	require.Len(t, desc.FastSnapshotRestores, 2)
	for _, f := range desc.FastSnapshotRestores {
		assert.Equal(t, snapID, aws.ToString(f.SnapshotId))
		assert.Equal(t, types.FastSnapshotRestoreStateCodeEnabled, f.State, "FSR settles to enabled on read")
	}

	dis, err := c.DisableFastSnapshotRestores(ctx, &ec2.DisableFastSnapshotRestoresInput{
		SourceSnapshotIds: []string{snapID},
		AvailabilityZones: []string{"us-east-1a"},
	})
	require.NoError(t, err)
	require.Len(t, dis.Successful, 1)
	assert.Equal(t, types.FastSnapshotRestoreStateCodeDisabling, dis.Successful[0].State)
}

// TestEC2_SnapshotTierSDK covers the tiering and restore start times and the
// temporary and permanent restore flags.
func TestEC2_SnapshotTierSDK(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	snapID := createSnapshotForEBS(t, c)

	mod, err := c.ModifySnapshotTier(ctx, &ec2.ModifySnapshotTierInput{
		SnapshotId: aws.String(snapID), StorageTier: types.TargetStorageTierArchive,
	})
	require.NoError(t, err)
	assert.Equal(t, snapID, aws.ToString(mod.SnapshotId))
	require.NotNil(t, mod.TieringStartTime)

	tmp, err := c.RestoreSnapshotTier(ctx, &ec2.RestoreSnapshotTierInput{
		SnapshotId: aws.String(snapID), TemporaryRestoreDays: aws.Int32(5),
	})
	require.NoError(t, err)
	assert.Equal(t, snapID, aws.ToString(tmp.SnapshotId))
	assert.False(t, aws.ToBool(tmp.IsPermanentRestore))
	assert.Equal(t, int32(5), aws.ToInt32(tmp.RestoreDuration))
	require.NotNil(t, tmp.RestoreStartTime)

	perm, err := c.RestoreSnapshotTier(ctx, &ec2.RestoreSnapshotTierInput{
		SnapshotId: aws.String(snapID), PermanentRestore: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(perm.IsPermanentRestore))

	// A live snapshot is not in the Recycle Bin, so the restore is rejected.
	_, err = c.RestoreSnapshotFromRecycleBin(ctx, &ec2.RestoreSnapshotFromRecycleBinInput{
		SnapshotId: aws.String(snapID),
	})
	require.Error(t, err, "a live snapshot is not in the Recycle Bin")
}

func TestEC2_SnapshotBlockPublicAccessSDK(t *testing.T) {
	c := ec2Client()

	en, err := c.EnableSnapshotBlockPublicAccess(ctx, &ec2.EnableSnapshotBlockPublicAccessInput{
		State: types.SnapshotBlockPublicAccessStateBlockAllSharing,
	})
	require.NoError(t, err)
	assert.Equal(t, types.SnapshotBlockPublicAccessStateBlockAllSharing, en.State)

	got, err := c.GetSnapshotBlockPublicAccessState(ctx, &ec2.GetSnapshotBlockPublicAccessStateInput{})
	require.NoError(t, err)
	assert.Equal(t, types.SnapshotBlockPublicAccessStateBlockAllSharing, got.State)
	assert.Equal(t, types.ManagedByAccount, got.ManagedBy)

	dis, err := c.DisableSnapshotBlockPublicAccess(ctx, &ec2.DisableSnapshotBlockPublicAccessInput{})
	require.NoError(t, err)
	assert.Equal(t, types.SnapshotBlockPublicAccessStateUnblocked, dis.State)

	got2, err := c.GetSnapshotBlockPublicAccessState(ctx, &ec2.GetSnapshotBlockPublicAccessStateInput{})
	require.NoError(t, err)
	assert.Equal(t, types.SnapshotBlockPublicAccessStateUnblocked, got2.State)
}

func TestEC2_VolumeAttributeSDK(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vol, err := c.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(8),
	})
	require.NoError(t, err)
	volID := aws.ToString(vol.VolumeId)

	got, err := c.DescribeVolumeAttribute(ctx, &ec2.DescribeVolumeAttributeInput{
		VolumeId: aws.String(volID), Attribute: types.VolumeAttributeNameAutoEnableIO,
	})
	require.NoError(t, err)
	require.NotNil(t, got.AutoEnableIO)
	assert.False(t, aws.ToBool(got.AutoEnableIO.Value), "autoEnableIO defaults to false")

	_, err = c.ModifyVolumeAttribute(ctx, &ec2.ModifyVolumeAttributeInput{
		VolumeId:     aws.String(volID),
		AutoEnableIO: &types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	require.NoError(t, err)

	got2, err := c.DescribeVolumeAttribute(ctx, &ec2.DescribeVolumeAttributeInput{
		VolumeId: aws.String(volID), Attribute: types.VolumeAttributeNameAutoEnableIO,
	})
	require.NoError(t, err)
	require.NotNil(t, got2.AutoEnableIO)
	assert.True(t, aws.ToBool(got2.AutoEnableIO.Value))
}
