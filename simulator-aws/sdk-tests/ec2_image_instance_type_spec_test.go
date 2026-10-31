package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registerAMIForInstanceTypeSpec registers an AMI the caller owns, the only kind
// whose instance type specification may be set, over the Amazon Linux AMI's
// root filesystem so its instances boot.
func registerAMIForInstanceTypeSpec(t *testing.T, c *ec2.Client, name string) string {
	t.Helper()
	base, err := c.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{ec2AmazonLinuxAMI(t)}})
	require.NoError(t, err)
	require.Len(t, base.Images, 1)
	out, err := c.RegisterImage(ctx, &ec2.RegisterImageInput{
		Name:               aws.String(name),
		Architecture:       types.ArchitectureValuesX8664,
		RootDeviceName:     aws.String("/dev/xvda"),
		VirtualizationType: aws.String("hvm"),
		ImageLocation:      base.Images[0].ImageLocation,
	})
	require.NoError(t, err)
	return aws.ToString(out.ImageId)
}

// TestEC2_ReplaceImageInstanceTypeSpecificationSDK expects DescribeImages to
// report the specification, wildcard entries to match a family, and omitting it
// to remove it.
func TestEC2_ReplaceImageInstanceTypeSpecificationSDK(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	imageID := registerAMIForInstanceTypeSpec(t, c, "instance-type-spec-ami")

	// With no specification every type is allowed, and DescribeImages carries no
	// member for it.
	described, err := c.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{imageID}})
	require.NoError(t, err)
	require.Len(t, described.Images, 1)
	assert.Nil(t, described.Images[0].InstanceTypeSpecification)

	_, err = c.ReplaceImageInstanceTypeSpecification(ctx, &ec2.ReplaceImageInstanceTypeSpecificationInput{
		ImageId: aws.String(imageID),
		InstanceTypeSpecification: &types.InstanceTypeSpecificationRequest{
			SupportedInstanceTypes:   []string{"t3.*", "t2.micro"},
			UnsupportedInstanceTypes: []string{"t3.nano"},
		},
	})
	require.NoError(t, err)

	described, err = c.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{imageID}})
	require.NoError(t, err)
	require.Len(t, described.Images, 1)
	spec := described.Images[0].InstanceTypeSpecification
	require.NotNil(t, spec)
	// The response wraps each entry in an InstanceTypeItem, unlike the request.
	names := func(items []types.InstanceTypeItem) []string {
		out := make([]string, 0, len(items))
		for _, item := range items {
			out = append(out, aws.ToString(item.InstanceType))
		}
		return out
	}
	assert.ElementsMatch(t, []string{"t3.*", "t2.micro"}, names(spec.SupportedInstanceTypes))
	assert.ElementsMatch(t, []string{"t3.nano"}, names(spec.UnsupportedInstanceTypes))
}

// RunInstances enforces the specification; a stored one nothing enforced
// would report a restriction that does not apply.
func TestEC2_ImageInstanceTypeSpecificationGovernsRunInstances(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	imageID := registerAMIForInstanceTypeSpec(t, c, "instance-type-spec-launch-ami")

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.171.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.171.1.0/24")

	launch := func(instanceType types.InstanceType) error {
		run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
			ImageId: aws.String(imageID), InstanceType: instanceType,
			MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
		})
		terminateEC2InstancesOnCleanup(t, c, run)
		return err
	}

	_, err = c.ReplaceImageInstanceTypeSpecification(ctx, &ec2.ReplaceImageInstanceTypeSpecificationInput{
		ImageId: aws.String(imageID),
		InstanceTypeSpecification: &types.InstanceTypeSpecificationRequest{
			SupportedInstanceTypes:   []string{"t3.*"},
			UnsupportedInstanceTypes: []string{"t3.nano"},
		},
	})
	require.NoError(t, err)

	require.NoError(t, launch(types.InstanceTypeT3Micro))

	// An unsupported entry beats the wildcard that would allow it.
	err = launch(types.InstanceTypeT3Nano)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidParameterCombination")

	err = launch(types.InstanceTypeT2Micro)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidParameterCombination")

	_, err = c.ReplaceImageInstanceTypeSpecification(ctx, &ec2.ReplaceImageInstanceTypeSpecificationInput{
		ImageId: aws.String(imageID),
	})
	require.NoError(t, err)

	described, err := c.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{imageID}})
	require.NoError(t, err)
	require.Len(t, described.Images, 1)
	assert.Nil(t, described.Images[0].InstanceTypeSpecification)

	require.NoError(t, launch(types.InstanceTypeT2Micro))
}

// With only unsupported entries, everything launches except what they match.
func TestEC2_ImageInstanceTypeSpecificationUnsupportedOnly(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	imageID := registerAMIForInstanceTypeSpec(t, c, "instance-type-spec-deny-ami")

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.172.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.172.1.0/24")

	_, err = c.ReplaceImageInstanceTypeSpecification(ctx, &ec2.ReplaceImageInstanceTypeSpecificationInput{
		ImageId: aws.String(imageID),
		InstanceTypeSpecification: &types.InstanceTypeSpecificationRequest{
			UnsupportedInstanceTypes: []string{"t2.*"},
		},
	})
	require.NoError(t, err)

	launch := func(instanceType types.InstanceType) error {
		run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
			ImageId: aws.String(imageID), InstanceType: instanceType,
			MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
		})
		terminateEC2InstancesOnCleanup(t, c, run)
		return err
	}

	require.NoError(t, launch(types.InstanceTypeT3Micro))
	err = launch(types.InstanceTypeT2Micro)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidParameterCombination")
}

// An AMI that does not exist is reported rather than answered true.
func TestEC2_ReplaceImageInstanceTypeSpecificationRejectsAbsentImage(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	_, err := c.ReplaceImageInstanceTypeSpecification(ctx, &ec2.ReplaceImageInstanceTypeSpecificationInput{
		ImageId: aws.String("ami-0000000000000dead"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidAMIID.NotFound")
}
