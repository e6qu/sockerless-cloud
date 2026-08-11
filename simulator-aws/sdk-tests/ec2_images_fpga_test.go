package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_FpgaImageLifecycleSDK covers the Amazon FPGA Image (AFI) control
// plane: CreateFpgaImage, DescribeFpgaImages read-back (id / name / description
// / state), CopyFpgaImage into a new AFI, the attribute set
// (DescribeFpgaImageAttribute / ModifyFpgaImageAttribute loadPermission grant /
// ResetFpgaImageAttribute), and DeleteFpgaImage.
func TestEC2_FpgaImageLifecycleSDK(t *testing.T) {
	c := ec2Client()

	create, err := c.CreateFpgaImage(ctx, &ec2.CreateFpgaImageInput{
		Name:                 aws.String("my-afi"),
		Description:          aws.String("test fpga image"),
		InputStorageLocation: &types.StorageLocation{Bucket: aws.String("my-dcp-bucket"), Key: aws.String("dcp.tar")},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeFpgaImage,
			Tags:         []types.Tag{{Key: aws.String("team"), Value: aws.String("hw")}},
		}},
	})
	require.NoError(t, err)
	afiID := aws.ToString(create.FpgaImageId)
	require.NotEmpty(t, afiID)
	require.NotEmpty(t, aws.ToString(create.FpgaImageGlobalId))
	defer c.DeleteFpgaImage(ctx, &ec2.DeleteFpgaImageInput{FpgaImageId: aws.String(afiID)})

	desc, err := c.DescribeFpgaImages(ctx, &ec2.DescribeFpgaImagesInput{
		FpgaImageIds: []string{afiID},
	})
	require.NoError(t, err)
	require.Len(t, desc.FpgaImages, 1)
	got := desc.FpgaImages[0]
	assert.Equal(t, "my-afi", aws.ToString(got.Name))
	assert.Equal(t, "test fpga image", aws.ToString(got.Description))
	assert.Equal(t, types.FpgaImageStateCodeAvailable, got.State.Code)
	require.Len(t, got.Tags, 1)
	assert.Equal(t, "team", aws.ToString(got.Tags[0].Key))

	// Filter by state must scope to the AFI.
	byState, err := c.DescribeFpgaImages(ctx, &ec2.DescribeFpgaImagesInput{
		FpgaImageIds: []string{afiID},
		Filters:      []types.Filter{{Name: aws.String("state"), Values: []string{"available"}}},
	})
	require.NoError(t, err)
	require.Len(t, byState.FpgaImages, 1)

	// CopyFpgaImage into a fresh AFI.
	cp, err := c.CopyFpgaImage(ctx, &ec2.CopyFpgaImageInput{
		SourceFpgaImageId: aws.String(afiID),
		Name:              aws.String("my-afi-copy"),
		SourceRegion:      aws.String("us-east-1"),
	})
	require.NoError(t, err)
	copyID := aws.ToString(cp.FpgaImageId)
	require.NotEmpty(t, copyID)
	require.NotEqual(t, afiID, copyID)
	defer c.DeleteFpgaImage(ctx, &ec2.DeleteFpgaImageInput{FpgaImageId: aws.String(copyID)})

	cpDesc, err := c.DescribeFpgaImages(ctx, &ec2.DescribeFpgaImagesInput{FpgaImageIds: []string{copyID}})
	require.NoError(t, err)
	require.Len(t, cpDesc.FpgaImages, 1)
	assert.Equal(t, "my-afi-copy", aws.ToString(cpDesc.FpgaImages[0].Name))

	// Attribute set: describe name/description, modify loadPermission, reset.
	attr, err := c.DescribeFpgaImageAttribute(ctx, &ec2.DescribeFpgaImageAttributeInput{
		FpgaImageId: aws.String(afiID),
		Attribute:   types.FpgaImageAttributeNameName,
	})
	require.NoError(t, err)
	require.NotNil(t, attr.FpgaImageAttribute)
	assert.Equal(t, "my-afi", aws.ToString(attr.FpgaImageAttribute.Name))

	mod, err := c.ModifyFpgaImageAttribute(ctx, &ec2.ModifyFpgaImageAttributeInput{
		FpgaImageId:   aws.String(afiID),
		Attribute:     types.FpgaImageAttributeNameLoadPermission,
		OperationType: types.OperationTypeAdd,
		UserIds:       []string{"123456789012"},
	})
	require.NoError(t, err)
	require.NotNil(t, mod.FpgaImageAttribute)
	require.Len(t, mod.FpgaImageAttribute.LoadPermissions, 1)
	assert.Equal(t, "123456789012", aws.ToString(mod.FpgaImageAttribute.LoadPermissions[0].UserId))

	loadAttr, err := c.DescribeFpgaImageAttribute(ctx, &ec2.DescribeFpgaImageAttributeInput{
		FpgaImageId: aws.String(afiID),
		Attribute:   types.FpgaImageAttributeNameLoadPermission,
	})
	require.NoError(t, err)
	require.Len(t, loadAttr.FpgaImageAttribute.LoadPermissions, 1)

	_, err = c.ResetFpgaImageAttribute(ctx, &ec2.ResetFpgaImageAttributeInput{
		FpgaImageId: aws.String(afiID),
		Attribute:   types.ResetFpgaImageAttributeNameLoadPermission,
	})
	require.NoError(t, err)

	resetAttr, err := c.DescribeFpgaImageAttribute(ctx, &ec2.DescribeFpgaImageAttributeInput{
		FpgaImageId: aws.String(afiID),
		Attribute:   types.FpgaImageAttributeNameLoadPermission,
	})
	require.NoError(t, err)
	assert.Empty(t, resetAttr.FpgaImageAttribute.LoadPermissions)

	del, err := c.DeleteFpgaImage(ctx, &ec2.DeleteFpgaImageInput{FpgaImageId: aws.String(afiID)})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(del.Return))
}

// TestEC2_AllowedImagesSettingsSDK covers the account-level Allowed AMIs
// settings: Enable (audit-mode), Get read-back, ReplaceImageCriteria, Get
// again, and Disable.
func TestEC2_AllowedImagesSettingsSDK(t *testing.T) {
	c := ec2Client()
	t.Cleanup(func() {
		c.DisableAllowedImagesSettings(ctx, &ec2.DisableAllowedImagesSettingsInput{})
	})

	en, err := c.EnableAllowedImagesSettings(ctx, &ec2.EnableAllowedImagesSettingsInput{
		AllowedImagesSettingsState: types.AllowedImagesSettingsEnabledStateAuditMode,
	})
	require.NoError(t, err)
	assert.Equal(t, types.AllowedImagesSettingsEnabledStateAuditMode, en.AllowedImagesSettingsState)

	get, err := c.GetAllowedImagesSettings(ctx, &ec2.GetAllowedImagesSettingsInput{})
	require.NoError(t, err)
	assert.Equal(t, "audit-mode", aws.ToString(get.State))

	_, err = c.ReplaceImageCriteriaInAllowedImagesSettings(ctx, &ec2.ReplaceImageCriteriaInAllowedImagesSettingsInput{
		ImageCriteria: []types.ImageCriterionRequest{{
			ImageProviders: []string{"amazon", "123456789012"},
		}},
	})
	require.NoError(t, err)

	get2, err := c.GetAllowedImagesSettings(ctx, &ec2.GetAllowedImagesSettingsInput{})
	require.NoError(t, err)
	require.Len(t, get2.ImageCriteria, 1)
	assert.ElementsMatch(t, []string{"amazon", "123456789012"}, get2.ImageCriteria[0].ImageProviders)

	dis, err := c.DisableAllowedImagesSettings(ctx, &ec2.DisableAllowedImagesSettingsInput{})
	require.NoError(t, err)
	assert.Equal(t, types.AllowedImagesSettingsDisabledStateDisabled, dis.AllowedImagesSettingsState)
}

// TestEC2_ImageBlockPublicAccessSDK covers the account-level image
// block-public-access state: Enable (block-new-sharing), Get read-back, Disable.
func TestEC2_ImageBlockPublicAccessSDK(t *testing.T) {
	c := ec2Client()
	t.Cleanup(func() {
		c.DisableImageBlockPublicAccess(ctx, &ec2.DisableImageBlockPublicAccessInput{})
	})

	en, err := c.EnableImageBlockPublicAccess(ctx, &ec2.EnableImageBlockPublicAccessInput{
		ImageBlockPublicAccessState: types.ImageBlockPublicAccessEnabledStateBlockNewSharing,
	})
	require.NoError(t, err)
	assert.Equal(t, types.ImageBlockPublicAccessEnabledStateBlockNewSharing, en.ImageBlockPublicAccessState)

	get, err := c.GetImageBlockPublicAccessState(ctx, &ec2.GetImageBlockPublicAccessStateInput{})
	require.NoError(t, err)
	assert.Equal(t, "block-new-sharing", aws.ToString(get.ImageBlockPublicAccessState))

	dis, err := c.DisableImageBlockPublicAccess(ctx, &ec2.DisableImageBlockPublicAccessInput{})
	require.NoError(t, err)
	assert.Equal(t, types.ImageBlockPublicAccessDisabledStateUnblocked, dis.ImageBlockPublicAccessState)
}

// TestEC2_ImageDeregistrationProtectionSDK covers per-AMI deregistration
// protection: Enable on a real registered AMI, then Disable.
func TestEC2_ImageDeregistrationProtectionSDK(t *testing.T) {
	c := ec2Client()
	amiID := ec2RegisterTestAMI(t, c)

	en, err := c.EnableImageDeregistrationProtection(ctx, &ec2.EnableImageDeregistrationProtectionInput{
		ImageId: aws.String(amiID),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(en.Return))

	dis, err := c.DisableImageDeregistrationProtection(ctx, &ec2.DisableImageDeregistrationProtectionInput{
		ImageId: aws.String(amiID),
	})
	require.NoError(t, err)
	assert.Equal(t, "disabled", aws.ToString(dis.Return))

	// Unknown AMI is rejected.
	_, err = c.EnableImageDeregistrationProtection(ctx, &ec2.EnableImageDeregistrationProtectionInput{
		ImageId: aws.String("ami-deadbeefdeadbeef"),
	})
	require.Error(t, err)
}

// TestEC2_BundleTasksSDK and TestEC2_ConversionTasksSDK assert the honest-empty
// task lists (no live bundling / import-export backend).
func TestEC2_BundleTasksSDK(t *testing.T) {
	c := ec2Client()
	bt, err := c.DescribeBundleTasks(ctx, &ec2.DescribeBundleTasksInput{})
	require.NoError(t, err)
	assert.Empty(t, bt.BundleTasks)

	_, err = c.CancelBundleTask(ctx, &ec2.CancelBundleTaskInput{BundleId: aws.String("bun-12345678")})
	require.Error(t, err, "no bundle tasks exist so cancel is rejected")
}

func TestEC2_ConversionTasksSDK(t *testing.T) {
	c := ec2Client()
	ct, err := c.DescribeConversionTasks(ctx, &ec2.DescribeConversionTasksInput{})
	require.NoError(t, err)
	assert.Empty(t, ct.ConversionTasks)

	// CancelConversionTask rejects an unknown task (the sim runs no live
	// conversion backend, so there is never a real task to cancel).
	_, err = c.CancelConversionTask(ctx, &ec2.CancelConversionTaskInput{ConversionTaskId: aws.String("import-i-ffffffff")})
	require.Error(t, err)
}

// TestEC2_StoreImageTaskSDK covers CreateStoreImageTask on a real AMI to an S3
// bucket and the DescribeStoreImageTasks read-back.
func TestEC2_StoreImageTaskSDK(t *testing.T) {
	c := ec2Client()
	amiID := ec2RegisterTestAMI(t, c)

	create, err := c.CreateStoreImageTask(ctx, &ec2.CreateStoreImageTaskInput{
		ImageId: aws.String(amiID),
		Bucket:  aws.String("my-ami-store-bucket"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(create.ObjectKey))

	desc, err := c.DescribeStoreImageTasks(ctx, &ec2.DescribeStoreImageTasksInput{
		ImageIds: []string{amiID},
	})
	require.NoError(t, err)
	require.Len(t, desc.StoreImageTaskResults, 1)
	got := desc.StoreImageTaskResults[0]
	assert.Equal(t, amiID, aws.ToString(got.AmiId))
	assert.Equal(t, "my-ami-store-bucket", aws.ToString(got.Bucket))
	assert.Equal(t, aws.ToString(create.ObjectKey), aws.ToString(got.S3objectKey))
	assert.Equal(t, "Completed", aws.ToString(got.StoreTaskState))

	// Unknown AMI is rejected.
	_, err = c.CreateStoreImageTask(ctx, &ec2.CreateStoreImageTaskInput{
		ImageId: aws.String("ami-deadbeefdeadbeef"),
		Bucket:  aws.String("b"),
	})
	require.Error(t, err)
}

// ec2RegisterTestAMI registers a minimal EBS-backed AMI and returns its id, for
// tests that need a real AMI in the image store (deregistration protection,
// store-image task).
func ec2RegisterTestAMI(t *testing.T, c *ec2.Client) string {
	t.Helper()
	reg, err := c.RegisterImage(ctx, &ec2.RegisterImageInput{
		Name:           aws.String("imf-test-ami"),
		Architecture:   types.ArchitectureValuesX8664,
		RootDeviceName: aws.String("/dev/sda1"),
		BlockDeviceMappings: []types.BlockDeviceMapping{{
			DeviceName: aws.String("/dev/sda1"),
			Ebs:        &types.EbsBlockDevice{SnapshotId: aws.String("snap-0123456789abcdef0"), VolumeSize: aws.Int32(8)},
		}},
	})
	require.NoError(t, err)
	amiID := aws.ToString(reg.ImageId)
	require.NotEmpty(t, amiID)
	t.Cleanup(func() {
		c.DeregisterImage(ctx, &ec2.DeregisterImageInput{ImageId: aws.String(amiID)})
	})
	return amiID
}
