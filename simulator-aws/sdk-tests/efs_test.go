package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func efsClient() *efs.Client {
	return efs.NewFromConfig(sdkConfig(), func(o *efs.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestEFS_CreateAndDescribeFileSystem(t *testing.T) {
	client := efsClient()

	createOut, err := client.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken:   aws.String("test-fs-token"),
		PerformanceMode: efstypes.PerformanceModeGeneralPurpose,
		Tags: []efstypes.Tag{
			{Key: aws.String("Name"), Value: aws.String("test-fs")},
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, *createOut.FileSystemId)
	assert.Equal(t, efstypes.LifeCycleStateAvailable, createOut.LifeCycleState)
	fsID := *createOut.FileSystemId

	// Describe
	descOut, err := client.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)
	require.Len(t, descOut.FileSystems, 1)
	assert.Equal(t, fsID, *descOut.FileSystems[0].FileSystemId)
	assert.Equal(t, "test-fs", *descOut.FileSystems[0].Name)
	// CreationToken (a required FileSystemDescription member) must round-trip,
	// else terraform aws_efs_file_system drifts/ForceNew.
	assert.Equal(t, "test-fs-token", aws.ToString(descOut.FileSystems[0].CreationToken))

	// The CreationToken filter narrows the list to the matching file system.
	byToken, err := client.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{
		CreationToken: aws.String("test-fs-token"),
	})
	require.NoError(t, err)
	require.Len(t, byToken.FileSystems, 1)
	assert.Equal(t, fsID, *byToken.FileSystems[0].FileSystemId)

	// Cleanup
	_, err = client.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)

	// Verify deleted
	descOut2, err := client.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)
	assert.Empty(t, descOut2.FileSystems)
}

func TestEFS_CreateAndDescribeMountTargets(t *testing.T) {
	client := efsClient()

	// Create file system first
	createOut, err := client.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken: aws.String("mt-test-fs"),
	})
	require.NoError(t, err)
	fsID := *createOut.FileSystemId

	// Create mount target
	mtOut, err := client.CreateMountTarget(ctx, &efs.CreateMountTargetInput{
		FileSystemId:   aws.String(fsID),
		SubnetId:       aws.String("subnet-0123456789abcdef0"),
		SecurityGroups: []string{"sg-12345"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, *mtOut.MountTargetId)
	assert.Equal(t, fsID, *mtOut.FileSystemId)
	assert.Equal(t, efstypes.LifeCycleStateAvailable, mtOut.LifeCycleState)
	mtID := *mtOut.MountTargetId

	// Describe mount targets by filesystem
	descOut, err := client.DescribeMountTargets(ctx, &efs.DescribeMountTargetsInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)
	require.Len(t, descOut.MountTargets, 1)
	assert.Equal(t, mtID, *descOut.MountTargets[0].MountTargetId)

	// Verify file system now shows mount target count
	fsDesc, err := client.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)
	require.Len(t, fsDesc.FileSystems, 1)
	assert.Equal(t, int32(1), fsDesc.FileSystems[0].NumberOfMountTargets)

	// Cleanup
	_, err = client.DeleteMountTarget(ctx, &efs.DeleteMountTargetInput{
		MountTargetId: aws.String(mtID),
	})
	require.NoError(t, err)

	_, err = client.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)
}

func TestEFS_CreateAndDescribeAccessPoints(t *testing.T) {
	client := efsClient()

	// Create file system
	createOut, err := client.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken: aws.String("ap-test-fs"),
	})
	require.NoError(t, err)
	fsID := *createOut.FileSystemId

	// Create access point
	apOut, err := client.CreateAccessPoint(ctx, &efs.CreateAccessPointInput{
		FileSystemId: aws.String(fsID),
		PosixUser: &efstypes.PosixUser{
			Uid: aws.Int64(1000),
			Gid: aws.Int64(1000),
		},
		RootDirectory: &efstypes.RootDirectory{
			Path: aws.String("/data"),
			CreationInfo: &efstypes.CreationInfo{
				OwnerUid:    aws.Int64(1000),
				OwnerGid:    aws.Int64(1000),
				Permissions: aws.String("755"),
			},
		},
		Tags: []efstypes.Tag{
			{Key: aws.String("Name"), Value: aws.String("test-ap")},
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, *apOut.AccessPointId)
	assert.Equal(t, fsID, *apOut.FileSystemId)
	assert.Equal(t, efstypes.LifeCycleStateAvailable, apOut.LifeCycleState)
	apID := *apOut.AccessPointId

	// Describe access points by filesystem
	descOut, err := client.DescribeAccessPoints(ctx, &efs.DescribeAccessPointsInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)
	require.Len(t, descOut.AccessPoints, 1)
	assert.Equal(t, apID, *descOut.AccessPoints[0].AccessPointId)
	assert.Equal(t, int64(1000), *descOut.AccessPoints[0].PosixUser.Uid)
	assert.Equal(t, "/data", *descOut.AccessPoints[0].RootDirectory.Path)

	// Cleanup: delete in reverse order
	_, err = client.DeleteAccessPoint(ctx, &efs.DeleteAccessPointInput{
		AccessPointId: aws.String(apID),
	})
	require.NoError(t, err)

	// Verify deleted
	descOut2, err := client.DescribeAccessPoints(ctx, &efs.DescribeAccessPointsInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)
	assert.Empty(t, descOut2.AccessPoints)

	_, err = client.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)
}

func TestEFS_PoliciesAndBackup(t *testing.T) {
	client := efsClient()

	createOut, err := client.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken: aws.String("policy-test-fs"),
	})
	require.NoError(t, err)
	fsID := *createOut.FileSystemId
	t.Cleanup(func() {
		_, _ = client.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{FileSystemId: aws.String(fsID)})
	})

	// File system policy: describe before put -> PolicyNotFound.
	_, err = client.DescribeFileSystemPolicy(ctx, &efs.DescribeFileSystemPolicyInput{
		FileSystemId: aws.String(fsID),
	})
	require.Error(t, err)

	policyDoc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"elasticfilesystem:ClientMount","Resource":"*"}]}`
	putPol, err := client.PutFileSystemPolicy(ctx, &efs.PutFileSystemPolicyInput{
		FileSystemId: aws.String(fsID),
		Policy:       aws.String(policyDoc),
	})
	require.NoError(t, err)
	assert.Equal(t, fsID, aws.ToString(putPol.FileSystemId))
	assert.Equal(t, policyDoc, aws.ToString(putPol.Policy))

	descPol, err := client.DescribeFileSystemPolicy(ctx, &efs.DescribeFileSystemPolicyInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)
	assert.Equal(t, policyDoc, aws.ToString(descPol.Policy))

	_, err = client.DeleteFileSystemPolicy(ctx, &efs.DeleteFileSystemPolicyInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)
	_, err = client.DescribeFileSystemPolicy(ctx, &efs.DescribeFileSystemPolicyInput{
		FileSystemId: aws.String(fsID),
	})
	require.Error(t, err)

	// Backup policy: default DISABLED, then enable, then read back.
	descBak, err := client.DescribeBackupPolicy(ctx, &efs.DescribeBackupPolicyInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)
	assert.Equal(t, efstypes.StatusDisabled, descBak.BackupPolicy.Status)

	putBak, err := client.PutBackupPolicy(ctx, &efs.PutBackupPolicyInput{
		FileSystemId: aws.String(fsID),
		BackupPolicy: &efstypes.BackupPolicy{Status: efstypes.StatusEnabled},
	})
	require.NoError(t, err)
	assert.Equal(t, efstypes.StatusEnabled, putBak.BackupPolicy.Status)

	descBak2, err := client.DescribeBackupPolicy(ctx, &efs.DescribeBackupPolicyInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)
	assert.Equal(t, efstypes.StatusEnabled, descBak2.BackupPolicy.Status)
}

func TestEFS_Replication(t *testing.T) {
	client := efsClient()

	srcOut, err := client.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken: aws.String("repl-src-fs"),
	})
	require.NoError(t, err)
	srcID := *srcOut.FileSystemId
	t.Cleanup(func() {
		_, _ = client.DeleteReplicationConfiguration(ctx, &efs.DeleteReplicationConfigurationInput{SourceFileSystemId: aws.String(srcID)})
		_, _ = client.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{FileSystemId: aws.String(srcID)})
	})

	createRepl, err := client.CreateReplicationConfiguration(ctx, &efs.CreateReplicationConfigurationInput{
		SourceFileSystemId: aws.String(srcID),
		Destinations: []efstypes.DestinationToCreate{
			{Region: aws.String("us-west-2")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, srcID, aws.ToString(createRepl.SourceFileSystemId))
	require.Len(t, createRepl.Destinations, 1)
	destFsID := aws.ToString(createRepl.Destinations[0].FileSystemId)
	assert.NotEmpty(t, destFsID)
	t.Cleanup(func() {
		_, _ = client.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{FileSystemId: aws.String(destFsID)})
	})

	// Describe by source id.
	descRepl, err := client.DescribeReplicationConfigurations(ctx, &efs.DescribeReplicationConfigurationsInput{
		FileSystemId: aws.String(srcID),
	})
	require.NoError(t, err)
	require.Len(t, descRepl.Replications, 1)
	assert.Equal(t, srcID, aws.ToString(descRepl.Replications[0].SourceFileSystemId))
	assert.Equal(t, "us-west-2", aws.ToString(descRepl.Replications[0].Destinations[0].Region))

	_, err = client.DeleteReplicationConfiguration(ctx, &efs.DeleteReplicationConfigurationInput{
		SourceFileSystemId: aws.String(srcID),
	})
	require.NoError(t, err)

	// After deletion the source no longer has a replication configuration.
	descRepl2, err := client.DescribeReplicationConfigurations(ctx, &efs.DescribeReplicationConfigurationsInput{
		FileSystemId: aws.String(srcID),
	})
	require.NoError(t, err)
	assert.Empty(t, descRepl2.Replications)
}

func TestEFS_AccountPreferencesAndTagging(t *testing.T) {
	client := efsClient()

	// Account preferences round-trip.
	putPref, err := client.PutAccountPreferences(ctx, &efs.PutAccountPreferencesInput{
		ResourceIdType: efstypes.ResourceIdTypeLongId,
	})
	require.NoError(t, err)
	require.NotNil(t, putPref.ResourceIdPreference)
	assert.Equal(t, efstypes.ResourceIdTypeLongId, putPref.ResourceIdPreference.ResourceIdType)

	descPref, err := client.DescribeAccountPreferences(ctx, &efs.DescribeAccountPreferencesInput{})
	require.NoError(t, err)
	require.NotNil(t, descPref.ResourceIdPreference)
	assert.Equal(t, efstypes.ResourceIdTypeLongId, descPref.ResourceIdPreference.ResourceIdType)

	// Resource-ARN tagging API on a file system.
	fsOut, err := client.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken: aws.String("tag-test-fs"),
	})
	require.NoError(t, err)
	fsID := *fsOut.FileSystemId
	t.Cleanup(func() {
		_, _ = client.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{FileSystemId: aws.String(fsID)})
	})

	_, err = client.TagResource(ctx, &efs.TagResourceInput{
		ResourceId: aws.String(fsID),
		Tags: []efstypes.Tag{
			{Key: aws.String("env"), Value: aws.String("prod")},
			{Key: aws.String("team"), Value: aws.String("infra")},
		},
	})
	require.NoError(t, err)

	listTags, err := client.ListTagsForResource(ctx, &efs.ListTagsForResourceInput{
		ResourceId: aws.String(fsID),
	})
	require.NoError(t, err)
	got := map[string]string{}
	for _, tg := range listTags.Tags {
		got[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	assert.Equal(t, "prod", got["env"])
	assert.Equal(t, "infra", got["team"])

	_, err = client.UntagResource(ctx, &efs.UntagResourceInput{
		ResourceId: aws.String(fsID),
		TagKeys:    []string{"team"},
	})
	require.NoError(t, err)

	listTags2, err := client.ListTagsForResource(ctx, &efs.ListTagsForResourceInput{
		ResourceId: aws.String(fsID),
	})
	require.NoError(t, err)
	got2 := map[string]string{}
	for _, tg := range listTags2.Tags {
		got2[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	assert.Equal(t, "prod", got2["env"])
	_, hasTeam := got2["team"]
	assert.False(t, hasTeam)
}

func TestEFS_FullLifecycle(t *testing.T) {
	client := efsClient()

	// Create file system
	fsOut, err := client.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken: aws.String("lifecycle-test-fs"),
		Tags: []efstypes.Tag{
			{Key: aws.String("Name"), Value: aws.String("lifecycle-fs")},
		},
	})
	require.NoError(t, err)
	fsID := *fsOut.FileSystemId

	// Create mount target
	mtOut, err := client.CreateMountTarget(ctx, &efs.CreateMountTargetInput{
		FileSystemId: aws.String(fsID),
		SubnetId:     aws.String("subnet-0123456789abcdef0"),
	})
	require.NoError(t, err)
	mtID := *mtOut.MountTargetId

	// Create access point
	apOut, err := client.CreateAccessPoint(ctx, &efs.CreateAccessPointInput{
		FileSystemId: aws.String(fsID),
		PosixUser: &efstypes.PosixUser{
			Uid: aws.Int64(0),
			Gid: aws.Int64(0),
		},
		RootDirectory: &efstypes.RootDirectory{
			Path: aws.String("/"),
		},
	})
	require.NoError(t, err)
	apID := *apOut.AccessPointId

	// Delete in correct order: access point, mount target, file system
	_, err = client.DeleteAccessPoint(ctx, &efs.DeleteAccessPointInput{
		AccessPointId: aws.String(apID),
	})
	require.NoError(t, err)

	_, err = client.DeleteMountTarget(ctx, &efs.DeleteMountTargetInput{
		MountTargetId: aws.String(mtID),
	})
	require.NoError(t, err)

	_, err = client.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{
		FileSystemId: aws.String(fsID),
	})
	require.NoError(t, err)
}
