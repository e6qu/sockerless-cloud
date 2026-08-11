package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func describeOneInstance(t *testing.T, c *ec2.Client, id string) types.Instance {
	t.Helper()
	out, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	require.NoError(t, err)
	require.Len(t, out.Reservations, 1)
	require.Len(t, out.Reservations[0].Instances, 1)
	return out.Reservations[0].Instances[0]
}

// TestEC2_InstanceKnobFidelitySDK covers the RunInstances input knobs and the
// Instance response fields that aws_instance reads back — previously dropped, so
// metadata_options / iam_instance_profile / ebs_optimized / monitoring /
// key_name / cpu_options all drifted every plan.
func TestEC2_InstanceKnobFidelitySDK(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.130.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.130.1.0/24")

	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String("ami-12345678"), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
		KeyName:      aws.String("my-key"),
		EbsOptimized: aws.Bool(true),
		Monitoring:   &types.RunInstancesMonitoringEnabled{Enabled: aws.Bool(true)},
		IamInstanceProfile: &types.IamInstanceProfileSpecification{
			Name: aws.String("my-instance-profile"),
		},
		MetadataOptions: &types.InstanceMetadataOptionsRequest{
			HttpTokens:              types.HttpTokensStateRequired,
			HttpPutResponseHopLimit: aws.Int32(2),
		},
		CpuOptions: &types.CpuOptionsRequest{CoreCount: aws.Int32(1), ThreadsPerCore: aws.Int32(1)},
	})
	require.NoError(t, err)
	id := aws.ToString(run.Instances[0].InstanceId)

	got := describeOneInstance(t, c, id)
	assert.Equal(t, "my-key", aws.ToString(got.KeyName), "key_name must round-trip")
	assert.True(t, aws.ToBool(got.EbsOptimized), "ebs_optimized must round-trip")
	require.NotNil(t, got.Monitoring)
	assert.Equal(t, types.MonitoringStateEnabled, got.Monitoring.State, "monitoring must round-trip")
	require.NotNil(t, got.IamInstanceProfile)
	assert.Contains(t, aws.ToString(got.IamInstanceProfile.Arn), "instance-profile/my-instance-profile")
	require.NotNil(t, got.MetadataOptions)
	assert.Equal(t, types.HttpTokensStateRequired, got.MetadataOptions.HttpTokens, "metadata http_tokens must round-trip")
	assert.Equal(t, int32(2), aws.ToInt32(got.MetadataOptions.HttpPutResponseHopLimit), "metadata hop limit must round-trip")
	require.NotNil(t, got.CpuOptions)
	assert.Equal(t, int32(1), aws.ToInt32(got.CpuOptions.CoreCount))
	assert.True(t, aws.ToBool(got.SourceDestCheck), "source_dest_check defaults true")

	// ModifyInstanceAttribute must persist (was a no-op).
	_, err = c.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{
		InstanceId: aws.String(id), SourceDestCheck: &types.AttributeBooleanValue{Value: aws.Bool(false)},
	})
	require.NoError(t, err)
	_, err = c.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{
		InstanceId: aws.String(id), DisableApiTermination: &types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	require.NoError(t, err)

	got = describeOneInstance(t, c, id)
	assert.False(t, aws.ToBool(got.SourceDestCheck), "source_dest_check must persist as false (NAT-instance path)")

	attr, err := c.DescribeInstanceAttribute(ctx, &ec2.DescribeInstanceAttributeInput{
		InstanceId: aws.String(id), Attribute: types.InstanceAttributeNameDisableApiTermination,
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(attr.DisableApiTermination.Value), "disable_api_termination must persist")
}

// TestEC2_RunInstancesAppliesLaunchTemplateSDK covers that launch-template data
// (IAM profile, metadata options) is applied to the launched instance, not just
// image/type.
func TestEC2_RunInstancesAppliesLaunchTemplateSDK(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.131.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.131.1.0/24")

	lt, err := c.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String("knob-lt"),
		LaunchTemplateData: &types.RequestLaunchTemplateData{
			ImageId: aws.String("ami-12345678"), InstanceType: types.InstanceTypeT3Micro,
			EbsOptimized: aws.Bool(true),
			IamInstanceProfile: &types.LaunchTemplateIamInstanceProfileSpecificationRequest{
				Name: aws.String("lt-profile"),
			},
			MetadataOptions: &types.LaunchTemplateInstanceMetadataOptionsRequest{
				HttpTokens: types.LaunchTemplateHttpTokensStateRequired,
			},
		},
	})
	require.NoError(t, err)

	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
		LaunchTemplate: &types.LaunchTemplateSpecification{
			LaunchTemplateId: lt.LaunchTemplate.LaunchTemplateId, Version: aws.String("$Latest"),
		},
	})
	require.NoError(t, err)
	got := describeOneInstance(t, c, aws.ToString(run.Instances[0].InstanceId))
	assert.True(t, aws.ToBool(got.EbsOptimized), "LT ebs_optimized must apply to the instance")
	require.NotNil(t, got.IamInstanceProfile)
	assert.Contains(t, aws.ToString(got.IamInstanceProfile.Arn), "lt-profile", "LT iam profile must apply")
	require.NotNil(t, got.MetadataOptions)
	assert.Equal(t, types.HttpTokensStateRequired, got.MetadataOptions.HttpTokens, "LT metadata options must apply")
}
