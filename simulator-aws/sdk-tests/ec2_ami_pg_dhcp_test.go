package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_AMILifecycleSDK covers the user-AMI control plane: CreateImage from a
// running instance, RegisterImage from a block device mapping, CopyImage, the
// DescribeImages read-back of each, and DeregisterImage. A user-registered AMI
// must round-trip through DescribeImages (id / name / state / architecture /
// root device / backing snapshot), distinct from the synthesized vendor-AMI
// lookup the sim keeps for `data.aws_ami`.
func TestEC2_AMILifecycleSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.140.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.140.1.0/24")

	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String("ami-12345678"), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.NoError(t, err)
	instID := aws.ToString(run.Instances[0].InstanceId)

	// CreateImage from the instance.
	img, err := c.CreateImage(ctx, &ec2.CreateImageInput{
		InstanceId:  aws.String(instID),
		Name:        aws.String("my-golden-ami"),
		Description: aws.String("snapshotted from instance"),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeImage,
			Tags:         []types.Tag{{Key: aws.String("env"), Value: aws.String("prod")}},
		}},
	})
	require.NoError(t, err)
	amiID := aws.ToString(img.ImageId)
	require.NotEmpty(t, amiID)

	got := describeOneImage(t, c, amiID)
	assert.Equal(t, "my-golden-ami", aws.ToString(got.Name))
	assert.Equal(t, "snapshotted from instance", aws.ToString(got.Description))
	assert.Equal(t, types.ImageStateAvailable, got.State)
	assert.Equal(t, types.DeviceTypeEbs, got.RootDeviceType)
	require.Len(t, got.BlockDeviceMappings, 1, "CreateImage records the backing snapshot")
	require.NotNil(t, got.BlockDeviceMappings[0].Ebs)
	assert.NotEmpty(t, aws.ToString(got.BlockDeviceMappings[0].Ebs.SnapshotId))
	assert.Equal(t, aws.ToString(run.Instances[0].InstanceId), aws.ToString(got.SourceInstanceId))

	// Filter by name + tag must scope to the registered AMI.
	byName, err := c.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Filters: []types.Filter{
			{Name: aws.String("name"), Values: []string{"my-golden-ami"}},
			{Name: aws.String("tag:env"), Values: []string{"prod"}},
		},
	})
	require.NoError(t, err)
	require.Len(t, byName.Images, 1)
	assert.Equal(t, amiID, aws.ToString(byName.Images[0].ImageId))

	// RegisterImage from a block device mapping with an EBS snapshot.
	reg, err := c.RegisterImage(ctx, &ec2.RegisterImageInput{
		Name:           aws.String("registered-ami"),
		Architecture:   types.ArchitectureValuesArm64,
		RootDeviceName: aws.String("/dev/xvda"),
		BlockDeviceMappings: []types.BlockDeviceMapping{{
			DeviceName: aws.String("/dev/xvda"),
			Ebs:        &types.EbsBlockDevice{SnapshotId: aws.String("snap-0123456789abcdef0"), VolumeSize: aws.Int32(16)},
		}},
	})
	require.NoError(t, err)
	regID := aws.ToString(reg.ImageId)
	gotReg := describeOneImage(t, c, regID)
	assert.Equal(t, "registered-ami", aws.ToString(gotReg.Name))
	assert.Equal(t, types.ArchitectureValuesArm64, gotReg.Architecture)
	assert.Equal(t, "/dev/xvda", aws.ToString(gotReg.RootDeviceName))
	require.Len(t, gotReg.BlockDeviceMappings, 1)
	assert.Equal(t, int32(16), aws.ToInt32(gotReg.BlockDeviceMappings[0].Ebs.VolumeSize))

	// CopyImage gets its own id and preserves source metadata.
	cp, err := c.CopyImage(ctx, &ec2.CopyImageInput{
		SourceImageId: aws.String(amiID),
		SourceRegion:  aws.String("us-east-1"),
		Name:          aws.String("copied-ami"),
	})
	require.NoError(t, err)
	copyID := aws.ToString(cp.ImageId)
	require.NotEqual(t, amiID, copyID, "copy must get its own image id")
	gotCopy := describeOneImage(t, c, copyID)
	assert.Equal(t, "copied-ami", aws.ToString(gotCopy.Name))
	assert.Equal(t, got.Architecture, gotCopy.Architecture, "copy inherits source architecture")

	// DeregisterImage removes the AMI from the store. (DescribeImages by explicit
	// id can't confirm absence — the sim synthesizes an AMI for any requested id
	// to support terraform data.aws_ami lookups of public AMIs — so confirm the
	// deletion the store-faithful way: a second deregister now reports NotFound.)
	_, err = c.DeregisterImage(ctx, &ec2.DeregisterImageInput{ImageId: aws.String(amiID)})
	require.NoError(t, err)
	_, err = c.DeregisterImage(ctx, &ec2.DeregisterImageInput{ImageId: aws.String(amiID)})
	require.Error(t, err, "deregistering an already-removed AMI must fail")
}

func describeOneImage(t *testing.T, c *ec2.Client, id string) types.Image {
	t.Helper()
	out, err := c.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{id}})
	require.NoError(t, err)
	require.Len(t, out.Images, 1)
	return out.Images[0]
}

// TestEC2_PlacementGroupSDK covers CreatePlacementGroup / DescribePlacementGroups
// / DeletePlacementGroup, including the strategy + partition-count round-trip and
// a strategy filter.
func TestEC2_PlacementGroupSDK(t *testing.T) {
	c := ec2Client()

	_, err := c.CreatePlacementGroup(ctx, &ec2.CreatePlacementGroupInput{
		GroupName:      aws.String("sdk-pg-partition"),
		Strategy:       types.PlacementStrategyPartition,
		PartitionCount: aws.Int32(3),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypePlacementGroup,
			Tags:         []types.Tag{{Key: aws.String("team"), Value: aws.String("infra")}},
		}},
	})
	require.NoError(t, err)

	out, err := c.DescribePlacementGroups(ctx, &ec2.DescribePlacementGroupsInput{
		GroupNames: []string{"sdk-pg-partition"},
	})
	require.NoError(t, err)
	require.Len(t, out.PlacementGroups, 1)
	pg := out.PlacementGroups[0]
	assert.Equal(t, "sdk-pg-partition", aws.ToString(pg.GroupName))
	assert.Equal(t, types.PlacementGroupStateAvailable, pg.State)
	assert.Equal(t, types.PlacementStrategyPartition, pg.Strategy)
	assert.Equal(t, int32(3), aws.ToInt32(pg.PartitionCount))
	assert.NotEmpty(t, aws.ToString(pg.GroupId))
	assert.Contains(t, aws.ToString(pg.GroupArn), "placement-group/sdk-pg-partition")

	// Strategy filter must scope to the partition group.
	byStrategy, err := c.DescribePlacementGroups(ctx, &ec2.DescribePlacementGroupsInput{
		Filters: []types.Filter{{Name: aws.String("strategy"), Values: []string{"partition"}}},
	})
	require.NoError(t, err)
	found := false
	for _, g := range byStrategy.PlacementGroups {
		if aws.ToString(g.GroupName) == "sdk-pg-partition" {
			found = true
		}
	}
	assert.True(t, found, "strategy filter must include the partition group")

	_, err = c.DeletePlacementGroup(ctx, &ec2.DeletePlacementGroupInput{GroupName: aws.String("sdk-pg-partition")})
	require.NoError(t, err)
	gone, err := c.DescribePlacementGroups(ctx, &ec2.DescribePlacementGroupsInput{})
	require.NoError(t, err)
	for _, g := range gone.PlacementGroups {
		assert.NotEqual(t, "sdk-pg-partition", aws.ToString(g.GroupName), "deleted placement group must not appear")
	}
}

// TestEC2_DhcpOptionsSDK covers CreateDhcpOptions / DescribeDhcpOptions /
// AssociateDhcpOptions / DeleteDhcpOptions, including the dhcp-configuration
// key/value round-trip and the VPC association that updates dhcpOptionsId.
func TestEC2_DhcpOptionsSDK(t *testing.T) {
	c := ec2Client()

	created, err := c.CreateDhcpOptions(ctx, &ec2.CreateDhcpOptionsInput{
		DhcpConfigurations: []types.NewDhcpConfiguration{
			{Key: aws.String("domain-name"), Values: []string{"sockerless.internal"}},
			{Key: aws.String("domain-name-servers"), Values: []string{"10.0.0.2", "8.8.8.8"}},
		},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeDhcpOptions,
			Tags:         []types.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
		}},
	})
	require.NoError(t, err)
	optsID := aws.ToString(created.DhcpOptions.DhcpOptionsId)
	require.NotEmpty(t, optsID)

	out, err := c.DescribeDhcpOptions(ctx, &ec2.DescribeDhcpOptionsInput{DhcpOptionsIds: []string{optsID}})
	require.NoError(t, err)
	require.Len(t, out.DhcpOptions, 1)
	cfg := dhcpConfigMap(out.DhcpOptions[0].DhcpConfigurations)
	require.Contains(t, cfg, "domain-name")
	assert.Equal(t, []string{"sockerless.internal"}, cfg["domain-name"])
	require.Contains(t, cfg, "domain-name-servers")
	assert.Equal(t, []string{"10.0.0.2", "8.8.8.8"}, cfg["domain-name-servers"])

	// Associate with a VPC → DescribeVpcs reflects the new dhcpOptionsId.
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.141.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	_, err = c.AssociateDhcpOptions(ctx, &ec2.AssociateDhcpOptionsInput{
		DhcpOptionsId: aws.String(optsID), VpcId: aws.String(vpcID),
	})
	require.NoError(t, err)
	vout, err := c.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{vpcID}})
	require.NoError(t, err)
	require.Len(t, vout.Vpcs, 1)
	assert.Equal(t, optsID, aws.ToString(vout.Vpcs[0].DhcpOptionsId), "VPC must reflect the associated DHCP options set")

	// Deleting while associated must fail (DependencyViolation).
	_, err = c.DeleteDhcpOptions(ctx, &ec2.DeleteDhcpOptionsInput{DhcpOptionsId: aws.String(optsID)})
	require.Error(t, err, "cannot delete a DHCP options set still associated with a VPC")

	// Revert the VPC to the default set, then delete succeeds.
	_, err = c.AssociateDhcpOptions(ctx, &ec2.AssociateDhcpOptionsInput{
		DhcpOptionsId: aws.String("default"), VpcId: aws.String(vpcID),
	})
	require.NoError(t, err)
	_, err = c.DeleteDhcpOptions(ctx, &ec2.DeleteDhcpOptionsInput{DhcpOptionsId: aws.String(optsID)})
	require.NoError(t, err)
}

func dhcpConfigMap(cfgs []types.DhcpConfiguration) map[string][]string {
	m := map[string][]string{}
	for _, c := range cfgs {
		var vals []string
		for _, av := range c.Values {
			vals = append(vals, aws.ToString(av.Value))
		}
		m[aws.ToString(c.Key)] = vals
	}
	return m
}
