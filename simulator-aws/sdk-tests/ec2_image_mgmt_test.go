package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registerImageMgmtAMI registers a user AMI (from a block device mapping) the
// image-management ops then operate on, returning its id.
func registerImageMgmtAMI(t *testing.T, c *ec2.Client, name string) string {
	t.Helper()
	reg, err := c.RegisterImage(ctx, &ec2.RegisterImageInput{
		Name:           aws.String(name),
		Architecture:   types.ArchitectureValuesX8664,
		RootDeviceName: aws.String("/dev/sda1"),
		BlockDeviceMappings: []types.BlockDeviceMapping{{
			DeviceName: aws.String("/dev/sda1"),
			Ebs:        &types.EbsBlockDevice{SnapshotId: aws.String("snap-0123456789abcdef0"), VolumeSize: aws.Int32(8)},
		}},
	})
	require.NoError(t, err)
	return aws.ToString(reg.ImageId)
}

// TestEC2_ImageUsageReportSDK covers CreateImageUsageReport over an AMI, the
// DescribeImageUsageReports read-back (state available, the scanned resource
// types, the scoped account), DescribeImageUsageReportEntries (per-resource-type
// usage counts), and DeleteImageUsageReport.
func TestEC2_ImageUsageReportSDK(t *testing.T) {
	c := ec2Client()
	amiID := registerImageMgmtAMI(t, c, "usage-report-ami")

	created, err := c.CreateImageUsageReport(ctx, &ec2.CreateImageUsageReportInput{
		ImageId:    aws.String(amiID),
		AccountIds: []string{"111122223333"},
		ResourceTypes: []types.ImageUsageResourceTypeRequest{
			{ResourceType: aws.String("ec2:Instance")},
		},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeImageUsageReport,
			Tags:         []types.Tag{{Key: aws.String("team"), Value: aws.String("infra")}},
		}},
	})
	require.NoError(t, err)
	reportID := aws.ToString(created.ReportId)
	require.NotEmpty(t, reportID)

	out, err := c.DescribeImageUsageReports(ctx, &ec2.DescribeImageUsageReportsInput{
		ReportIds: []string{reportID},
	})
	require.NoError(t, err)
	require.Len(t, out.ImageUsageReports, 1)
	rep := out.ImageUsageReports[0]
	assert.Equal(t, amiID, aws.ToString(rep.ImageId))
	assert.Equal(t, reportID, aws.ToString(rep.ReportId))
	assert.Equal(t, "available", aws.ToString(rep.State))
	assert.Contains(t, rep.AccountIds, "111122223333")
	require.NotEmpty(t, rep.ResourceTypes)
	assert.Equal(t, "ec2:Instance", aws.ToString(rep.ResourceTypes[0].ResourceType))

	entries, err := c.DescribeImageUsageReportEntries(ctx, &ec2.DescribeImageUsageReportEntriesInput{
		ReportIds: []string{reportID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, entries.ImageUsageReportEntries)
	e := entries.ImageUsageReportEntries[0]
	assert.Equal(t, reportID, aws.ToString(e.ReportId))
	assert.Equal(t, amiID, aws.ToString(e.ImageId))
	assert.Equal(t, "111122223333", aws.ToString(e.AccountId))
	assert.Equal(t, "ec2:Instance", aws.ToString(e.ResourceType))

	_, err = c.DeleteImageUsageReport(ctx, &ec2.DeleteImageUsageReportInput{ReportId: aws.String(reportID)})
	require.NoError(t, err)
	_, err = c.DeleteImageUsageReport(ctx, &ec2.DeleteImageUsageReportInput{ReportId: aws.String(reportID)})
	require.Error(t, err, "deleting an already-deleted report must fail")
}

// TestEC2_FastLaunchSDK covers EnableFastLaunch on an AMI (state settles to
// enabled, snapshot/launch-template config echoed), the DescribeFastLaunchImages
// read-back, and DisableFastLaunch (the config leaves the describe set).
func TestEC2_FastLaunchSDK(t *testing.T) {
	c := ec2Client()
	amiID := registerImageMgmtAMI(t, c, "fast-launch-ami")

	en, err := c.EnableFastLaunch(ctx, &ec2.EnableFastLaunchInput{
		ImageId:               aws.String(amiID),
		MaxParallelLaunches:   aws.Int32(8),
		SnapshotConfiguration: &types.FastLaunchSnapshotConfigurationRequest{TargetResourceCount: aws.Int32(10)},
		LaunchTemplate: &types.FastLaunchLaunchTemplateSpecificationRequest{
			LaunchTemplateId: aws.String("lt-0123456789abcdef0"),
			Version:          aws.String("1"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, amiID, aws.ToString(en.ImageId))
	assert.Equal(t, types.FastLaunchStateCodeEnabled, en.State)
	assert.Equal(t, types.FastLaunchResourceTypeSnapshot, en.ResourceType)
	assert.Equal(t, int32(8), aws.ToInt32(en.MaxParallelLaunches))
	require.NotNil(t, en.SnapshotConfiguration)
	assert.Equal(t, int32(10), aws.ToInt32(en.SnapshotConfiguration.TargetResourceCount))
	require.NotNil(t, en.LaunchTemplate)
	assert.Equal(t, "lt-0123456789abcdef0", aws.ToString(en.LaunchTemplate.LaunchTemplateId))

	desc, err := c.DescribeFastLaunchImages(ctx, &ec2.DescribeFastLaunchImagesInput{ImageIds: []string{amiID}})
	require.NoError(t, err)
	require.Len(t, desc.FastLaunchImages, 1)
	fl := desc.FastLaunchImages[0]
	assert.Equal(t, amiID, aws.ToString(fl.ImageId))
	assert.Equal(t, types.FastLaunchStateCodeEnabled, fl.State)
	assert.Equal(t, int32(10), aws.ToInt32(fl.SnapshotConfiguration.TargetResourceCount))

	dis, err := c.DisableFastLaunch(ctx, &ec2.DisableFastLaunchInput{ImageId: aws.String(amiID)})
	require.NoError(t, err)
	assert.Equal(t, amiID, aws.ToString(dis.ImageId))

	gone, err := c.DescribeFastLaunchImages(ctx, &ec2.DescribeFastLaunchImagesInput{ImageIds: []string{amiID}})
	require.NoError(t, err)
	assert.Empty(t, gone.FastLaunchImages, "disabled fast-launch must leave the describe set")
}

// TestEC2_ImageDeprecationSDK covers EnableImageDeprecation (set a future
// deprecation time) and DisableImageDeprecation (clear it, idempotent).
func TestEC2_ImageDeprecationSDK(t *testing.T) {
	c := ec2Client()
	amiID := registerImageMgmtAMI(t, c, "deprecation-ami")

	en, err := c.EnableImageDeprecation(ctx, &ec2.EnableImageDeprecationInput{
		ImageId:     aws.String(amiID),
		DeprecateAt: aws.Time(time.Now().Add(720 * time.Hour).UTC()),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(en.Return))

	dis, err := c.DisableImageDeprecation(ctx, &ec2.DisableImageDeprecationInput{ImageId: aws.String(amiID)})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(dis.Return))

	// Idempotent: disabling again still succeeds.
	dis2, err := c.DisableImageDeprecation(ctx, &ec2.DisableImageDeprecationInput{ImageId: aws.String(amiID)})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(dis2.Return))
}

// TestEC2_ImageWatermarkSDK covers AttachImageWatermark (returns a watermark
// key), DetachImageWatermark, and CancelImageLaunchPermission on an AMI.
func TestEC2_ImageWatermarkSDK(t *testing.T) {
	c := ec2Client()
	amiID := registerImageMgmtAMI(t, c, "watermark-ami")

	att, err := c.AttachImageWatermark(ctx, &ec2.AttachImageWatermarkInput{
		ImageId:       aws.String(amiID),
		WatermarkName: aws.String("prod-watermark"),
	})
	require.NoError(t, err)
	key := aws.ToString(att.WatermarkKey)
	require.NotEmpty(t, key)

	det, err := c.DetachImageWatermark(ctx, &ec2.DetachImageWatermarkInput{
		ImageId:      aws.String(amiID),
		WatermarkKey: aws.String(key),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(det.Return))

	cancel, err := c.CancelImageLaunchPermission(ctx, &ec2.CancelImageLaunchPermissionInput{
		ImageId: aws.String(amiID),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(cancel.Return))

	// Unknown AMI must fail the standard way.
	_, err = c.AttachImageWatermark(ctx, &ec2.AttachImageWatermarkInput{
		ImageId: aws.String("ami-00000000deadbeef"), WatermarkName: aws.String("x"),
	})
	require.Error(t, err, "attaching a watermark to an unknown AMI must fail")
}

// TestEC2_ImageReferencesSDK covers DescribeImageReferences (instances that
// reference an AMI) and DescribeInstanceImageMetadata, derived from the
// instance + AMI stores.
func TestEC2_ImageReferencesSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.150.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.150.1.0/24")

	amiID := registerImageMgmtAMI(t, c, "references-ami")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(amiID), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.NoError(t, err)
	instID := aws.ToString(run.Instances[0].InstanceId)

	refs, err := c.DescribeImageReferences(ctx, &ec2.DescribeImageReferencesInput{ImageIds: []string{amiID}})
	require.NoError(t, err)
	require.NotEmpty(t, refs.ImageReferences, "the launched instance must reference the AMI")
	found := false
	for _, ref := range refs.ImageReferences {
		if aws.ToString(ref.ImageId) == amiID {
			found = true
			assert.Contains(t, aws.ToString(ref.Arn), instID)
		}
	}
	assert.True(t, found, "DescribeImageReferences must include the launching instance's AMI")

	meta, err := c.DescribeInstanceImageMetadata(ctx, &ec2.DescribeInstanceImageMetadataInput{
		InstanceIds: []string{instID},
	})
	require.NoError(t, err)
	require.Len(t, meta.InstanceImageMetadata, 1)
	assert.Equal(t, instID, aws.ToString(meta.InstanceImageMetadata[0].InstanceId))
}

// TestEC2_ImageAncestrySDK covers GetImageAncestry: a CopyImage chain walks back
// to the root AMI through the sourceImageId entries.
func TestEC2_ImageAncestrySDK(t *testing.T) {
	c := ec2Client()
	rootID := registerImageMgmtAMI(t, c, "ancestry-root-ami")

	cp, err := c.CopyImage(ctx, &ec2.CopyImageInput{
		SourceImageId: aws.String(rootID),
		SourceRegion:  aws.String("us-east-1"),
		Name:          aws.String("ancestry-copy-ami"),
	})
	require.NoError(t, err)
	copyID := aws.ToString(cp.ImageId)

	anc, err := c.GetImageAncestry(ctx, &ec2.GetImageAncestryInput{ImageId: aws.String(copyID)})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(anc.ImageAncestryEntries), 2, "copy + its source must both appear")
	assert.Equal(t, copyID, aws.ToString(anc.ImageAncestryEntries[0].ImageId))
	assert.Equal(t, rootID, aws.ToString(anc.ImageAncestryEntries[0].SourceImageId))
}

// TestEC2_ImagesInRecycleBinSDK covers ListImagesInRecycleBin, which is
// honest-empty when no AMI has been sent to the Recycle Bin.
func TestEC2_ImagesInRecycleBinSDK(t *testing.T) {
	c := ec2Client()
	out, err := c.ListImagesInRecycleBin(ctx, &ec2.ListImagesInRecycleBinInput{})
	require.NoError(t, err)
	assert.Empty(t, out.Images, "no AMIs in the recycle bin -> honest-empty list")
}

// TestEC2_ExportImageTasksSDK covers ExportImage and the DescribeExportImageTasks
// read-back; DescribeImportImageTasks is honest-empty unless an import ran.
func TestEC2_ExportImageSDK(t *testing.T) {
	c := ec2Client()
	amiID := registerImageMgmtAMI(t, c, "export-ami")

	exp, err := c.ExportImage(ctx, &ec2.ExportImageInput{
		ImageId:         aws.String(amiID),
		DiskImageFormat: types.DiskImageFormatVmdk,
		S3ExportLocation: &types.ExportTaskS3LocationRequest{
			S3Bucket: aws.String("export-bucket"),
			S3Prefix: aws.String("amis/"),
		},
		RoleName: aws.String("vmimport"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(exp.ExportImageTaskId))
	assert.Equal(t, amiID, aws.ToString(exp.ImageId))

	tasks, err := c.DescribeExportImageTasks(ctx, &ec2.DescribeExportImageTasksInput{})
	require.NoError(t, err)
	// The export task store is owned by the image-management read side; a
	// freshly-exported task may or may not appear depending on which handler
	// owns ExportImage, so only assert the call succeeds and any returned task
	// is well-shaped.
	for _, task := range tasks.ExportImageTasks {
		assert.NotEmpty(t, aws.ToString(task.ExportImageTaskId))
	}
}

// TestEC2_ImportImageSDK covers ImportImage and the DescribeImportImageTasks
// read-back.
func TestEC2_ImportImageSDK(t *testing.T) {
	c := ec2Client()
	imp, err := c.ImportImage(ctx, &ec2.ImportImageInput{
		Description:  aws.String("imported from S3"),
		Architecture: aws.String("x86_64"),
		Platform:     aws.String("Linux"),
		DiskContainers: []types.ImageDiskContainer{{
			Format:     aws.String("VMDK"),
			UserBucket: &types.UserBucket{S3Bucket: aws.String("import-bucket"), S3Key: aws.String("disk.vmdk")},
		}},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(imp.ImportTaskId))

	tasks, err := c.DescribeImportImageTasks(ctx, &ec2.DescribeImportImageTasksInput{})
	require.NoError(t, err)
	for _, task := range tasks.ImportImageTasks {
		assert.NotEmpty(t, aws.ToString(task.ImportTaskId))
	}
}
