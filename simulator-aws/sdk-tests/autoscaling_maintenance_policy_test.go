package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutoScalingInstanceMaintenancePolicySDK expects a group to store its
// InstanceMaintenancePolicy, refuse thresholds outside the documented ranges,
// clear them with -1, and lend them to an instance refresh whose preferences
// omit them.
func TestAutoScalingInstanceMaintenancePolicySDK(t *testing.T) {
	t.Parallel()
	c := autoScalingClient()
	const lc, group = "sdk-maint-lc", "sdk-maint-asg"
	_, err := c.CreateLaunchConfiguration(ctx, &autoscaling.CreateLaunchConfigurationInput{
		LaunchConfigurationName: aws.String(lc), ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: aws.String("t3.micro"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteLaunchConfiguration(ctx, &autoscaling.DeleteLaunchConfigurationInput{LaunchConfigurationName: aws.String(lc)})
	})

	_, err = c.CreateAutoScalingGroup(ctx, &autoscaling.CreateAutoScalingGroupInput{
		AutoScalingGroupName: aws.String(group), LaunchConfigurationName: aws.String(lc),
		MinSize: aws.Int32(0), MaxSize: aws.Int32(2), DesiredCapacity: aws.Int32(0),
		InstanceMaintenancePolicy: &types.InstanceMaintenancePolicy{MinHealthyPercentage: aws.Int32(50), MaxHealthyPercentage: aws.Int32(250)},
	})
	var apiErr smithy.APIError
	require.ErrorAs(t, err, &apiErr, "MaxHealthyPercentage above 200")
	assert.Equal(t, "ValidationError", apiErr.ErrorCode())

	_, err = c.CreateAutoScalingGroup(ctx, &autoscaling.CreateAutoScalingGroupInput{
		AutoScalingGroupName: aws.String(group), LaunchConfigurationName: aws.String(lc),
		MinSize: aws.Int32(0), MaxSize: aws.Int32(2), DesiredCapacity: aws.Int32(0),
		InstanceMaintenancePolicy: &types.InstanceMaintenancePolicy{MinHealthyPercentage: aws.Int32(80), MaxHealthyPercentage: aws.Int32(150)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteAutoScalingGroup(ctx, &autoscaling.DeleteAutoScalingGroupInput{AutoScalingGroupName: aws.String(group), ForceDelete: aws.Bool(true)})
	})
	describe := func() *types.InstanceMaintenancePolicy {
		out, err := c.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{AutoScalingGroupNames: []string{group}})
		require.NoError(t, err)
		require.Len(t, out.AutoScalingGroups, 1)
		return out.AutoScalingGroups[0].InstanceMaintenancePolicy
	}
	policy := describe()
	require.NotNil(t, policy)
	assert.Equal(t, int32(80), aws.ToInt32(policy.MinHealthyPercentage))
	assert.Equal(t, int32(150), aws.ToInt32(policy.MaxHealthyPercentage))

	refresh, err := c.StartInstanceRefresh(ctx, &autoscaling.StartInstanceRefreshInput{AutoScalingGroupName: aws.String(group)})
	require.NoError(t, err)
	refreshes, err := c.DescribeInstanceRefreshes(ctx, &autoscaling.DescribeInstanceRefreshesInput{
		AutoScalingGroupName: aws.String(group), InstanceRefreshIds: []string{aws.ToString(refresh.InstanceRefreshId)},
	})
	require.NoError(t, err)
	require.Len(t, refreshes.InstanceRefreshes, 1)
	prefs := refreshes.InstanceRefreshes[0].Preferences
	require.NotNil(t, prefs)
	assert.Equal(t, int32(80), aws.ToInt32(prefs.MinHealthyPercentage), "an omitted MinHealthyPercentage takes the group's policy")
	assert.Equal(t, int32(150), aws.ToInt32(prefs.MaxHealthyPercentage), "an omitted MaxHealthyPercentage takes the group's policy")

	_, err = c.UpdateAutoScalingGroup(ctx, &autoscaling.UpdateAutoScalingGroupInput{
		AutoScalingGroupName:      aws.String(group),
		InstanceMaintenancePolicy: &types.InstanceMaintenancePolicy{MinHealthyPercentage: aws.Int32(-1), MaxHealthyPercentage: aws.Int32(-1)},
	})
	require.NoError(t, err)
	assert.Nil(t, describe(), "-1 clears the policy")
}
