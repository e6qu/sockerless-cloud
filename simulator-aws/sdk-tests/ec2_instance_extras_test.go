package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// launchInstanceFor runs a single instance in a fresh VPC+subnet and returns
// its instance ID, for the instance-attribute ops to operate on.
func launchInstanceFor(t *testing.T, c *ec2.Client, cidr string) string {
	t.Helper()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String(cidr)})
	require.NoError(t, err)
	// Derive a /24 inside the VPC's /16: 10.x.0.0/16 -> 10.x.1.0/24.
	octets := strings.SplitN(cidr, ".", 3)
	subnetCidr := octets[0] + "." + octets[1] + ".1.0/24"
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, subnetCidr)
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String("ami-12345678"), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.NoError(t, err)
	require.Len(t, run.Instances, 1)
	return aws.ToString(run.Instances[0].InstanceId)
}

// TestEC2_IamInstanceProfileAssociation covers the IAM instance-profile
// association lifecycle: Associate over a running instance (state associated,
// iip-assoc ID, bound ARN), Describe read-back + instance-id filter, Replace
// (swaps the bound profile, same association ID flow), and Disassociate.
func TestEC2_IamInstanceProfileAssociation(t *testing.T) {
	c := ec2Client()
	instID := launchInstanceFor(t, c, "10.190.0.0/16")

	assoc, err := c.AssociateIamInstanceProfile(ctx, &ec2.AssociateIamInstanceProfileInput{
		InstanceId:         aws.String(instID),
		IamInstanceProfile: &types.IamInstanceProfileSpecification{Name: aws.String("my-role")},
	})
	require.NoError(t, err)
	a := assoc.IamInstanceProfileAssociation
	require.NotNil(t, a)
	assocID := aws.ToString(a.AssociationId)
	require.NotEmpty(t, assocID)
	assert.Equal(t, instID, aws.ToString(a.InstanceId))
	assert.Equal(t, types.IamInstanceProfileAssociationStateAssociated, a.State)
	require.NotNil(t, a.IamInstanceProfile)
	assert.Contains(t, aws.ToString(a.IamInstanceProfile.Arn), "instance-profile/my-role")

	desc, err := c.DescribeIamInstanceProfileAssociations(ctx, &ec2.DescribeIamInstanceProfileAssociationsInput{
		AssociationIds: []string{assocID},
	})
	require.NoError(t, err)
	require.Len(t, desc.IamInstanceProfileAssociations, 1)
	assert.Equal(t, instID, aws.ToString(desc.IamInstanceProfileAssociations[0].InstanceId))

	byInst, err := c.DescribeIamInstanceProfileAssociations(ctx, &ec2.DescribeIamInstanceProfileAssociationsInput{
		Filters: []types.Filter{{Name: aws.String("instance-id"), Values: []string{instID}}},
	})
	require.NoError(t, err)
	require.Len(t, byInst.IamInstanceProfileAssociations, 1)
	assert.Equal(t, assocID, aws.ToString(byInst.IamInstanceProfileAssociations[0].AssociationId))

	repl, err := c.ReplaceIamInstanceProfileAssociation(ctx, &ec2.ReplaceIamInstanceProfileAssociationInput{
		AssociationId:      aws.String(assocID),
		IamInstanceProfile: &types.IamInstanceProfileSpecification{Name: aws.String("other-role")},
	})
	require.NoError(t, err)
	require.NotNil(t, repl.IamInstanceProfileAssociation)
	assert.Contains(t, aws.ToString(repl.IamInstanceProfileAssociation.IamInstanceProfile.Arn), "instance-profile/other-role")

	dis, err := c.DisassociateIamInstanceProfile(ctx, &ec2.DisassociateIamInstanceProfileInput{
		AssociationId: aws.String(assocID),
	})
	require.NoError(t, err)
	require.NotNil(t, dis.IamInstanceProfileAssociation)

	gone, err := c.DescribeIamInstanceProfileAssociations(ctx, &ec2.DescribeIamInstanceProfileAssociationsInput{})
	require.NoError(t, err)
	for _, g := range gone.IamInstanceProfileAssociations {
		assert.NotEqual(t, assocID, aws.ToString(g.AssociationId), "disassociated association must be gone")
	}
}

// TestEC2_InstanceCreditSpecification covers the burstable-performance credit
// option: Modify to unlimited and read it back via Describe (defaulting to
// standard for an unset instance), plus the CPU-options modification.
func TestEC2_InstanceCreditSpecification(t *testing.T) {
	c := ec2Client()
	instID := launchInstanceFor(t, c, "10.191.0.0/16")

	// Default before any modification is standard.
	pre, err := c.DescribeInstanceCreditSpecifications(ctx, &ec2.DescribeInstanceCreditSpecificationsInput{
		InstanceIds: []string{instID},
	})
	require.NoError(t, err)
	require.Len(t, pre.InstanceCreditSpecifications, 1)
	assert.Equal(t, "standard", aws.ToString(pre.InstanceCreditSpecifications[0].CpuCredits))

	mod, err := c.ModifyInstanceCreditSpecification(ctx, &ec2.ModifyInstanceCreditSpecificationInput{
		InstanceCreditSpecifications: []types.InstanceCreditSpecificationRequest{
			{InstanceId: aws.String(instID), CpuCredits: aws.String("unlimited")},
		},
	})
	require.NoError(t, err)
	require.Len(t, mod.SuccessfulInstanceCreditSpecifications, 1)
	assert.Equal(t, instID, aws.ToString(mod.SuccessfulInstanceCreditSpecifications[0].InstanceId))

	post, err := c.DescribeInstanceCreditSpecifications(ctx, &ec2.DescribeInstanceCreditSpecificationsInput{
		InstanceIds: []string{instID},
	})
	require.NoError(t, err)
	require.Len(t, post.InstanceCreditSpecifications, 1)
	assert.Equal(t, "unlimited", aws.ToString(post.InstanceCreditSpecifications[0].CpuCredits))
}

// TestEC2_InstanceCpuOptions covers ModifyInstanceCpuOptions: the core/thread
// counts are echoed back after the update.
func TestEC2_InstanceCpuOptions(t *testing.T) {
	c := ec2Client()
	instID := launchInstanceFor(t, c, "10.192.0.0/16")

	out, err := c.ModifyInstanceCpuOptions(ctx, &ec2.ModifyInstanceCpuOptionsInput{
		InstanceId:     aws.String(instID),
		CoreCount:      aws.Int32(2),
		ThreadsPerCore: aws.Int32(1),
	})
	require.NoError(t, err)
	assert.Equal(t, instID, aws.ToString(out.InstanceId))
	assert.Equal(t, int32(2), aws.ToInt32(out.CoreCount))
	assert.Equal(t, int32(1), aws.ToInt32(out.ThreadsPerCore))
}

// TestEC2_InstancePlacement covers the placement / maintenance / network-
// performance / event-start-time modifications over a running instance.
func TestEC2_InstancePlacement(t *testing.T) {
	c := ec2Client()
	instID := launchInstanceFor(t, c, "10.193.0.0/16")

	place, err := c.ModifyInstancePlacement(ctx, &ec2.ModifyInstancePlacementInput{
		InstanceId: aws.String(instID),
		Tenancy:    types.HostTenancyDefault,
		GroupName:  aws.String("my-pg"),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(place.Return))

	maint, err := c.ModifyInstanceMaintenanceOptions(ctx, &ec2.ModifyInstanceMaintenanceOptionsInput{
		InstanceId:   aws.String(instID),
		AutoRecovery: types.InstanceAutoRecoveryStateDisabled,
	})
	require.NoError(t, err)
	assert.Equal(t, instID, aws.ToString(maint.InstanceId))
	assert.Equal(t, types.InstanceAutoRecoveryStateDisabled, maint.AutoRecovery)

	netperf, err := c.ModifyInstanceNetworkPerformanceOptions(ctx, &ec2.ModifyInstanceNetworkPerformanceOptionsInput{
		InstanceId:         aws.String(instID),
		BandwidthWeighting: types.InstanceBandwidthWeightingVpc1,
	})
	require.NoError(t, err)
	assert.Equal(t, instID, aws.ToString(netperf.InstanceId))
	assert.Equal(t, types.InstanceBandwidthWeightingVpc1, netperf.BandwidthWeighting)

	ev, err := c.ModifyInstanceEventStartTime(ctx, &ec2.ModifyInstanceEventStartTimeInput{
		InstanceId:      aws.String(instID),
		InstanceEventId: aws.String("instance-event-0abcd1234"),
		NotBefore:       aws.Time(time.Now()),
	})
	require.NoError(t, err)
	require.NotNil(t, ev.Event)
	assert.Equal(t, "instance-event-0abcd1234", aws.ToString(ev.Event.InstanceEventId))
}

// TestEC2_ConsoleOutput covers the console / TPM / UEFI reads, plus the
// password-data and instance-topology reads — every value is base64-shaped or
// id-bound and deterministic for the instance.
func TestEC2_ConsoleOutput(t *testing.T) {
	c := ec2Client()
	instID := launchInstanceFor(t, c, "10.194.0.0/16")

	con, err := c.GetConsoleOutput(ctx, &ec2.GetConsoleOutputInput{InstanceId: aws.String(instID)})
	require.NoError(t, err)
	assert.Equal(t, instID, aws.ToString(con.InstanceId))
	require.NotNil(t, con.Output)
	assert.NotEmpty(t, aws.ToString(con.Output))

	shot, err := c.GetConsoleScreenshot(ctx, &ec2.GetConsoleScreenshotInput{InstanceId: aws.String(instID)})
	require.NoError(t, err)
	assert.Equal(t, instID, aws.ToString(shot.InstanceId))
	assert.NotEmpty(t, aws.ToString(shot.ImageData))

	tpm, err := c.GetInstanceTpmEkPub(ctx, &ec2.GetInstanceTpmEkPubInput{
		InstanceId: aws.String(instID),
		KeyType:    types.EkPubKeyTypeRsa2048,
		KeyFormat:  types.EkPubKeyFormatDer,
	})
	require.NoError(t, err)
	assert.Equal(t, instID, aws.ToString(tpm.InstanceId))
	assert.NotEmpty(t, aws.ToString(tpm.KeyValue))

	uefi, err := c.GetInstanceUefiData(ctx, &ec2.GetInstanceUefiDataInput{InstanceId: aws.String(instID)})
	require.NoError(t, err)
	assert.Equal(t, instID, aws.ToString(uefi.InstanceId))
	assert.NotEmpty(t, aws.ToString(uefi.UefiData))
}

// TestEC2_PasswordData covers GetPasswordData (a Linux-derived instance has no
// Windows password, so the data is empty but the call is faithful).
func TestEC2_PasswordData(t *testing.T) {
	c := ec2Client()
	instID := launchInstanceFor(t, c, "10.195.0.0/16")

	pw, err := c.GetPasswordData(ctx, &ec2.GetPasswordDataInput{InstanceId: aws.String(instID)})
	require.NoError(t, err)
	assert.Equal(t, instID, aws.ToString(pw.InstanceId))
}

// TestEC2_InstanceTopology covers DescribeInstanceTopology + the
// DescribeInstanceImageMetadata read.
func TestEC2_InstanceTopology(t *testing.T) {
	c := ec2Client()
	instID := launchInstanceFor(t, c, "10.196.0.0/16")

	topo, err := c.DescribeInstanceTopology(ctx, &ec2.DescribeInstanceTopologyInput{
		InstanceIds: []string{instID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, topo.Instances)
	var found bool
	for _, it := range topo.Instances {
		if aws.ToString(it.InstanceId) == instID {
			found = true
			assert.NotEmpty(t, it.NetworkNodes)
		}
	}
	assert.True(t, found, "topology must include the launched instance")

	meta, err := c.DescribeInstanceImageMetadata(ctx, &ec2.DescribeInstanceImageMetadataInput{
		InstanceIds: []string{instID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, meta.InstanceImageMetadata)
	assert.Equal(t, instID, aws.ToString(meta.InstanceImageMetadata[0].InstanceId))
	require.NotNil(t, meta.InstanceImageMetadata[0].ImageMetadata)
	assert.Equal(t, "ami-12345678", aws.ToString(meta.InstanceImageMetadata[0].ImageMetadata.ImageId))
}

// TestEC2_BundleInstance covers BundleInstance + ConfirmProductInstance + the
// ImportInstance conversion task, each a real-shaped task/result record.
func TestEC2_BundleInstance(t *testing.T) {
	c := ec2Client()
	instID := launchInstanceFor(t, c, "10.197.0.0/16")

	bundle, err := c.BundleInstance(ctx, &ec2.BundleInstanceInput{
		InstanceId: aws.String(instID),
		Storage: &types.Storage{S3: &types.S3Storage{
			Bucket: aws.String("my-bucket"),
			Prefix: aws.String("ami/"),
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, bundle.BundleTask)
	assert.Equal(t, instID, aws.ToString(bundle.BundleTask.InstanceId))
	assert.NotEmpty(t, aws.ToString(bundle.BundleTask.BundleId))

	confirm, err := c.ConfirmProductInstance(ctx, &ec2.ConfirmProductInstanceInput{
		InstanceId:  aws.String(instID),
		ProductCode: aws.String("774F4FF8"),
	})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(confirm.Return))

	imp, err := c.ImportInstance(ctx, &ec2.ImportInstanceInput{
		Platform: types.PlatformValuesWindows,
	})
	require.NoError(t, err)
	require.NotNil(t, imp.ConversionTask)
	assert.NotEmpty(t, aws.ToString(imp.ConversionTask.ConversionTaskId))
}

// TestEC2_InstanceExportTask covers CreateInstanceExportTask.
func TestEC2_InstanceExportTask(t *testing.T) {
	c := ec2Client()
	instID := launchInstanceFor(t, c, "10.198.0.0/16")

	exp, err := c.CreateInstanceExportTask(ctx, &ec2.CreateInstanceExportTaskInput{
		InstanceId:        aws.String(instID),
		Description:       aws.String("export to vmware"),
		TargetEnvironment: types.ExportEnvironmentVmware,
		ExportToS3Task: &types.ExportToS3TaskSpecification{
			DiskImageFormat: types.DiskImageFormatVmdk,
			S3Bucket:        aws.String("export-bucket"),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, exp.ExportTask)
	assert.NotEmpty(t, aws.ToString(exp.ExportTask.ExportTaskId))
	require.NotNil(t, exp.ExportTask.InstanceExportDetails)
	assert.Equal(t, instID, aws.ToString(exp.ExportTask.InstanceExportDetails.InstanceId))
}

// TestEC2_InstanceSqlHa covers the SQL Server High Availability states: enable
// standby detection over a running instance, read the per-instance state back
// (DescribeInstanceSqlHaStates + history), then disable.
func TestEC2_InstanceSqlHa(t *testing.T) {
	c := ec2Client()
	instID := launchInstanceFor(t, c, "10.199.0.0/16")

	en, err := c.EnableInstanceSqlHaStandbyDetections(ctx, &ec2.EnableInstanceSqlHaStandbyDetectionsInput{
		InstanceIds: []string{instID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, en.Instances)
	assert.Equal(t, instID, aws.ToString(en.Instances[0].InstanceId))

	states, err := c.DescribeInstanceSqlHaStates(ctx, &ec2.DescribeInstanceSqlHaStatesInput{
		InstanceIds: []string{instID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, states.Instances)
	assert.Equal(t, instID, aws.ToString(states.Instances[0].InstanceId))

	hist, err := c.DescribeInstanceSqlHaHistoryStates(ctx, &ec2.DescribeInstanceSqlHaHistoryStatesInput{
		InstanceIds: []string{instID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, hist.Instances)

	dis, err := c.DisableInstanceSqlHaStandbyDetections(ctx, &ec2.DisableInstanceSqlHaStandbyDetectionsInput{
		InstanceIds: []string{instID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, dis.Instances)
}

// TestEC2_InstanceTypesFromRequirements covers
// GetInstanceTypesFromInstanceRequirements: the matcher returns instance types
// from the catalog satisfying the vCPU/memory/architecture constraints.
func TestEC2_InstanceTypesFromRequirements(t *testing.T) {
	c := ec2Client()

	out, err := c.GetInstanceTypesFromInstanceRequirements(ctx, &ec2.GetInstanceTypesFromInstanceRequirementsInput{
		ArchitectureTypes:   []types.ArchitectureType{types.ArchitectureTypeX8664},
		VirtualizationTypes: []types.VirtualizationType{types.VirtualizationTypeHvm},
		InstanceRequirements: &types.InstanceRequirementsRequest{
			VCpuCount: &types.VCpuCountRangeRequest{Min: aws.Int32(2), Max: aws.Int32(2)},
			MemoryMiB: &types.MemoryMiBRequest{Min: aws.Int32(1024), Max: aws.Int32(8192)},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.InstanceTypes, "requirements must match catalog instance types")
	for _, it := range out.InstanceTypes {
		assert.NotEmpty(t, aws.ToString(it.InstanceType))
	}
}
