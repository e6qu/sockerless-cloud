package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutoScalingLaunchTemplateTagsMergeSDK expects an instance a group
// launches from a launch template to carry the template's instance tags merged
// with the group's propagated tags, the group's value winning on a shared key,
// as the vendored ec2-auto-scaling-tagging page states.
func TestAutoScalingLaunchTemplateTagsMergeSDK(t *testing.T) {
	t.Parallel()
	asgClient := autoScalingClient()
	ec2Client := ec2Client()

	vpcOut, err := ec2Client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.243.0.0/16")})
	require.NoError(t, err)
	subnetOut, err := ec2Client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            vpcOut.Vpc.VpcId,
		CidrBlock:        aws.String("10.243.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)

	const templateName, groupName = "sdk-asg-tags-lt", "sdk-asg-tags"
	_, err = ec2Client.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String(templateName),
		LaunchTemplateData: &ec2types.RequestLaunchTemplateData{
			ImageId:      aws.String(ec2AmazonLinuxAMI(t)),
			InstanceType: ec2types.InstanceTypeT3Micro,
			TagSpecifications: []ec2types.LaunchTemplateTagSpecificationRequest{{
				ResourceType: ec2types.ResourceTypeInstance,
				Tags: []ec2types.Tag{
					{Key: aws.String("env"), Value: aws.String("template")},
					{Key: aws.String("owner"), Value: aws.String("platform")},
				},
			}, {
				ResourceType: ec2types.ResourceTypeVolume,
				Tags:         []ec2types.Tag{{Key: aws.String("backup"), Value: aws.String("daily")}},
			}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ec2Client.DeleteLaunchTemplate(ctx, &ec2.DeleteLaunchTemplateInput{LaunchTemplateName: aws.String(templateName)})
	})

	_, err = asgClient.CreateAutoScalingGroup(ctx, &autoscaling.CreateAutoScalingGroupInput{
		AutoScalingGroupName: aws.String(groupName),
		LaunchTemplate:       &types.LaunchTemplateSpecification{LaunchTemplateName: aws.String(templateName), Version: aws.String("$Latest")},
		MinSize:              aws.Int32(1),
		MaxSize:              aws.Int32(1),
		DesiredCapacity:      aws.Int32(1),
		VPCZoneIdentifier:    subnetOut.Subnet.SubnetId,
		Tags: []types.Tag{
			{Key: aws.String("env"), Value: aws.String("group"), PropagateAtLaunch: aws.Bool(true)},
			{Key: aws.String("cost-center"), Value: aws.String("group-only"), PropagateAtLaunch: aws.Bool(false)},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = asgClient.DeleteAutoScalingGroup(ctx, &autoscaling.DeleteAutoScalingGroupInput{
			AutoScalingGroupName: aws.String(groupName), ForceDelete: aws.Bool(true),
		})
	})

	groupsOut, err := autoscaling.NewGroupInServiceWaiter(asgClient, func(o *autoscaling.GroupInServiceWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).WaitForOutput(ctx, &autoscaling.DescribeAutoScalingGroupsInput{AutoScalingGroupNames: []string{groupName}}, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, groupsOut.AutoScalingGroups[0].Instances, 1)
	instanceID := aws.ToString(groupsOut.AutoScalingGroups[0].Instances[0].InstanceId)

	instOut, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	tags := map[string]string{}
	for _, tag := range instOut.Reservations[0].Instances[0].Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, map[string]string{
		"aws:autoscaling:groupName": groupName,
		"env":                       "group",
		"owner":                     "platform",
	}, tags)
}
