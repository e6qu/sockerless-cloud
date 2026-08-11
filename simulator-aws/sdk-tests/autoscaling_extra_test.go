package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// asxSetupGroup creates a VPC, subnet, launch configuration and Auto Scaling
// group with one running instance, returning the group name and instance ID.
// It mirrors the coordinates a real consumer uses (no sim-only knobs), and
// registers tolerant cleanups.
func asxSetupGroup(t *testing.T, asg *autoscaling.Client, lcName, groupName string, desired int32) string {
	t.Helper()
	ec2c := ec2Client()

	vpcOut, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.91.0.0/16")})
	require.NoError(t, err)
	subnetOut, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            vpcOut.Vpc.VpcId,
		CidrBlock:        aws.String("10.91.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)

	_, err = asg.CreateLaunchConfiguration(ctx, &autoscaling.CreateLaunchConfigurationInput{
		LaunchConfigurationName: aws.String(lcName),
		ImageId:                 aws.String("ami-asx1234"),
		InstanceType:            aws.String("t3.micro"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = asg.DeleteLaunchConfiguration(ctx, &autoscaling.DeleteLaunchConfigurationInput{
			LaunchConfigurationName: aws.String(lcName),
		})
	})

	_, err = asg.CreateAutoScalingGroup(ctx, &autoscaling.CreateAutoScalingGroupInput{
		AutoScalingGroupName:    aws.String(groupName),
		LaunchConfigurationName: aws.String(lcName),
		MinSize:                 aws.Int32(0),
		MaxSize:                 aws.Int32(desired + 2),
		DesiredCapacity:         aws.Int32(desired),
		VPCZoneIdentifier:       subnetOut.Subnet.SubnetId,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = asg.DeleteAutoScalingGroup(ctx, &autoscaling.DeleteAutoScalingGroupInput{
			AutoScalingGroupName: aws.String(groupName),
			ForceDelete:          aws.Bool(true),
		})
	})

	if desired == 0 {
		return ""
	}
	groupsOut, err := asg.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{groupName},
	})
	require.NoError(t, err)
	require.Len(t, groupsOut.AutoScalingGroups, 1)
	require.NotEmpty(t, groupsOut.AutoScalingGroups[0].Instances)
	return aws.ToString(groupsOut.AutoScalingGroups[0].Instances[0].InstanceId)
}

func TestAutoScaling_LoadBalancersAndTargetGroups(t *testing.T) {
	c := autoScalingClient()
	const group = "asx-lb-grp"
	asxSetupGroup(t, c, "asx-lb-lc", group, 0)

	_, err := c.AttachLoadBalancers(ctx, &autoscaling.AttachLoadBalancersInput{
		AutoScalingGroupName: aws.String(group),
		LoadBalancerNames:    []string{"asx-clb"},
	})
	require.NoError(t, err)

	lbOut, err := c.DescribeLoadBalancers(ctx, &autoscaling.DescribeLoadBalancersInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.NoError(t, err)
	require.Len(t, lbOut.LoadBalancers, 1)
	assert.Equal(t, "asx-clb", aws.ToString(lbOut.LoadBalancers[0].LoadBalancerName))
	assert.Equal(t, "InService", aws.ToString(lbOut.LoadBalancers[0].State))

	_, err = c.DetachLoadBalancers(ctx, &autoscaling.DetachLoadBalancersInput{
		AutoScalingGroupName: aws.String(group),
		LoadBalancerNames:    []string{"asx-clb"},
	})
	require.NoError(t, err)
	lbOut, err = c.DescribeLoadBalancers(ctx, &autoscaling.DescribeLoadBalancersInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.NoError(t, err)
	assert.Empty(t, lbOut.LoadBalancers)

	const tgARN = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/asx-tg/abcdef0123456789"
	_, err = c.AttachLoadBalancerTargetGroups(ctx, &autoscaling.AttachLoadBalancerTargetGroupsInput{
		AutoScalingGroupName: aws.String(group),
		TargetGroupARNs:      []string{tgARN},
	})
	require.NoError(t, err)
	tgOut, err := c.DescribeLoadBalancerTargetGroups(ctx, &autoscaling.DescribeLoadBalancerTargetGroupsInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.NoError(t, err)
	require.Len(t, tgOut.LoadBalancerTargetGroups, 1)
	assert.Equal(t, tgARN, aws.ToString(tgOut.LoadBalancerTargetGroups[0].LoadBalancerTargetGroupARN))

	_, err = c.DetachLoadBalancerTargetGroups(ctx, &autoscaling.DetachLoadBalancerTargetGroupsInput{
		AutoScalingGroupName: aws.String(group),
		TargetGroupARNs:      []string{tgARN},
	})
	require.NoError(t, err)
}

func TestAutoScaling_TrafficSources(t *testing.T) {
	c := autoScalingClient()
	const group = "asx-ts-grp"
	asxSetupGroup(t, c, "asx-ts-lc", group, 0)

	const tgARN = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/asx-ts/0011223344556677"
	_, err := c.AttachTrafficSources(ctx, &autoscaling.AttachTrafficSourcesInput{
		AutoScalingGroupName: aws.String(group),
		TrafficSources: []astypes.TrafficSourceIdentifier{
			{Identifier: aws.String(tgARN), Type: aws.String("elbv2")},
		},
	})
	require.NoError(t, err)

	tsOut, err := c.DescribeTrafficSources(ctx, &autoscaling.DescribeTrafficSourcesInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.NoError(t, err)
	require.Len(t, tsOut.TrafficSources, 1)
	assert.Equal(t, tgARN, aws.ToString(tsOut.TrafficSources[0].Identifier))
	assert.Equal(t, "InService", aws.ToString(tsOut.TrafficSources[0].State))

	_, err = c.DetachTrafficSources(ctx, &autoscaling.DetachTrafficSourcesInput{
		AutoScalingGroupName: aws.String(group),
		TrafficSources: []astypes.TrafficSourceIdentifier{
			{Identifier: aws.String(tgARN), Type: aws.String("elbv2")},
		},
	})
	require.NoError(t, err)
	tsOut, err = c.DescribeTrafficSources(ctx, &autoscaling.DescribeTrafficSourcesInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.NoError(t, err)
	assert.Empty(t, tsOut.TrafficSources)
}

func TestAutoScaling_InstanceRefresh(t *testing.T) {
	c := autoScalingClient()
	const group = "asx-ir-grp"
	asxSetupGroup(t, c, "asx-ir-lc", group, 0)

	startOut, err := c.StartInstanceRefresh(ctx, &autoscaling.StartInstanceRefreshInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.NoError(t, err)
	refreshID := aws.ToString(startOut.InstanceRefreshId)
	require.NotEmpty(t, refreshID)

	descOut, err := c.DescribeInstanceRefreshes(ctx, &autoscaling.DescribeInstanceRefreshesInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.NoError(t, err)
	require.Len(t, descOut.InstanceRefreshes, 1)
	assert.Equal(t, refreshID, aws.ToString(descOut.InstanceRefreshes[0].InstanceRefreshId))
	assert.Equal(t, astypes.InstanceRefreshStatusSuccessful, descOut.InstanceRefreshes[0].Status)

	rbOut, err := c.RollbackInstanceRefresh(ctx, &autoscaling.RollbackInstanceRefreshInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.NoError(t, err)
	assert.Equal(t, refreshID, aws.ToString(rbOut.InstanceRefreshId))
}

func TestAutoScaling_WarmPool(t *testing.T) {
	c := autoScalingClient()
	const group = "asx-wp-grp"
	asxSetupGroup(t, c, "asx-wp-lc", group, 0)

	_, err := c.PutWarmPool(ctx, &autoscaling.PutWarmPoolInput{
		AutoScalingGroupName: aws.String(group),
		MinSize:              aws.Int32(2),
		PoolState:            astypes.WarmPoolStateStopped,
	})
	require.NoError(t, err)

	wpOut, err := c.DescribeWarmPool(ctx, &autoscaling.DescribeWarmPoolInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.NoError(t, err)
	require.NotNil(t, wpOut.WarmPoolConfiguration)
	assert.EqualValues(t, 2, aws.ToInt32(wpOut.WarmPoolConfiguration.MinSize))
	assert.Equal(t, astypes.WarmPoolStateStopped, wpOut.WarmPoolConfiguration.PoolState)

	_, err = c.DeleteWarmPool(ctx, &autoscaling.DeleteWarmPoolInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.NoError(t, err)
	wpOut, err = c.DescribeWarmPool(ctx, &autoscaling.DescribeWarmPoolInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.NoError(t, err)
	assert.Nil(t, wpOut.WarmPoolConfiguration)
}

func TestAutoScaling_NotificationsMetricsAndProcesses(t *testing.T) {
	c := autoScalingClient()
	const group = "asx-nmp-grp"
	asxSetupGroup(t, c, "asx-nmp-lc", group, 0)

	const topic = "arn:aws:sns:us-east-1:123456789012:asx-topic"
	_, err := c.PutNotificationConfiguration(ctx, &autoscaling.PutNotificationConfigurationInput{
		AutoScalingGroupName: aws.String(group),
		TopicARN:             aws.String(topic),
		NotificationTypes:    []string{"autoscaling:EC2_INSTANCE_LAUNCH", "autoscaling:EC2_INSTANCE_TERMINATE"},
	})
	require.NoError(t, err)

	ncOut, err := c.DescribeNotificationConfigurations(ctx, &autoscaling.DescribeNotificationConfigurationsInput{
		AutoScalingGroupNames: []string{group},
	})
	require.NoError(t, err)
	require.Len(t, ncOut.NotificationConfigurations, 2)
	assert.Equal(t, topic, aws.ToString(ncOut.NotificationConfigurations[0].TopicARN))

	_, err = c.DeleteNotificationConfiguration(ctx, &autoscaling.DeleteNotificationConfigurationInput{
		AutoScalingGroupName: aws.String(group),
		TopicARN:             aws.String(topic),
	})
	require.NoError(t, err)

	_, err = c.EnableMetricsCollection(ctx, &autoscaling.EnableMetricsCollectionInput{
		AutoScalingGroupName: aws.String(group),
		Granularity:          aws.String("1Minute"),
		Metrics:              []string{"GroupTotalInstances"},
	})
	require.NoError(t, err)
	_, err = c.DisableMetricsCollection(ctx, &autoscaling.DisableMetricsCollectionInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.NoError(t, err)

	_, err = c.SuspendProcesses(ctx, &autoscaling.SuspendProcessesInput{
		AutoScalingGroupName: aws.String(group),
		ScalingProcesses:     []string{"Terminate"},
	})
	require.NoError(t, err)
	_, err = c.ResumeProcesses(ctx, &autoscaling.ResumeProcessesInput{
		AutoScalingGroupName: aws.String(group),
		ScalingProcesses:     []string{"Terminate"},
	})
	require.NoError(t, err)
}

func TestAutoScaling_InstancesStandbyProtection(t *testing.T) {
	c := autoScalingClient()
	const group = "asx-inst-grp"
	instanceID := asxSetupGroup(t, c, "asx-inst-lc", group, 1)
	require.NotEmpty(t, instanceID)

	_, err := c.SetInstanceProtection(ctx, &autoscaling.SetInstanceProtectionInput{
		AutoScalingGroupName: aws.String(group),
		InstanceIds:          []string{instanceID},
		ProtectedFromScaleIn: aws.Bool(true),
	})
	require.NoError(t, err)

	standbyOut, err := c.EnterStandby(ctx, &autoscaling.EnterStandbyInput{
		AutoScalingGroupName:           aws.String(group),
		InstanceIds:                    []string{instanceID},
		ShouldDecrementDesiredCapacity: aws.Bool(true),
	})
	require.NoError(t, err)
	require.NotEmpty(t, standbyOut.Activities)

	exitOut, err := c.ExitStandby(ctx, &autoscaling.ExitStandbyInput{
		AutoScalingGroupName: aws.String(group),
		InstanceIds:          []string{instanceID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, exitOut.Activities)

	detachOut, err := c.DetachInstances(ctx, &autoscaling.DetachInstancesInput{
		AutoScalingGroupName:           aws.String(group),
		InstanceIds:                    []string{instanceID},
		ShouldDecrementDesiredCapacity: aws.Bool(true),
	})
	require.NoError(t, err)
	require.NotEmpty(t, detachOut.Activities)

	_, err = c.AttachInstances(ctx, &autoscaling.AttachInstancesInput{
		AutoScalingGroupName: aws.String(group),
		InstanceIds:          []string{instanceID},
	})
	require.NoError(t, err)
}

func TestAutoScaling_LifecycleActionAndBatch(t *testing.T) {
	c := autoScalingClient()
	const group = "asx-life-grp"
	asxSetupGroup(t, c, "asx-life-lc", group, 0)

	_, err := c.PutLifecycleHook(ctx, &autoscaling.PutLifecycleHookInput{
		AutoScalingGroupName: aws.String(group),
		LifecycleHookName:    aws.String("asx-hook"),
		LifecycleTransition:  aws.String("autoscaling:EC2_INSTANCE_LAUNCHING"),
	})
	require.NoError(t, err)

	_, err = c.RecordLifecycleActionHeartbeat(ctx, &autoscaling.RecordLifecycleActionHeartbeatInput{
		AutoScalingGroupName: aws.String(group),
		LifecycleHookName:    aws.String("asx-hook"),
		LifecycleActionToken: aws.String("00000000-0000-0000-0000-000000000abc"),
	})
	require.NoError(t, err)
	_, err = c.CompleteLifecycleAction(ctx, &autoscaling.CompleteLifecycleActionInput{
		AutoScalingGroupName:  aws.String(group),
		LifecycleHookName:     aws.String("asx-hook"),
		LifecycleActionToken:  aws.String("00000000-0000-0000-0000-000000000abc"),
		LifecycleActionResult: aws.String("CONTINUE"),
	})
	require.NoError(t, err)

	bpOut, err := c.BatchPutScheduledUpdateGroupAction(ctx, &autoscaling.BatchPutScheduledUpdateGroupActionInput{
		AutoScalingGroupName: aws.String(group),
		ScheduledUpdateGroupActions: []astypes.ScheduledUpdateGroupActionRequest{
			{ScheduledActionName: aws.String("asx-sched"), MinSize: aws.Int32(0), MaxSize: aws.Int32(3), DesiredCapacity: aws.Int32(1), Recurrence: aws.String("0 12 * * *")},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, bpOut.FailedScheduledUpdateGroupActions)

	schedOut, err := c.DescribeScheduledActions(ctx, &autoscaling.DescribeScheduledActionsInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.NoError(t, err)
	require.Len(t, schedOut.ScheduledUpdateGroupActions, 1)

	bdOut, err := c.BatchDeleteScheduledAction(ctx, &autoscaling.BatchDeleteScheduledActionInput{
		AutoScalingGroupName: aws.String(group),
		ScheduledActionNames: []string{"asx-sched"},
	})
	require.NoError(t, err)
	assert.Empty(t, bdOut.FailedScheduledActions)
}

func TestAutoScaling_PredictiveForecastAndLaunch(t *testing.T) {
	c := autoScalingClient()
	const group = "asx-fl-grp"
	asxSetupGroup(t, c, "asx-fl-lc", group, 0)

	fcOut, err := c.GetPredictiveScalingForecast(ctx, &autoscaling.GetPredictiveScalingForecastInput{
		AutoScalingGroupName: aws.String(group),
		PolicyName:           aws.String("asx-pred"),
		StartTime:            aws.Time(time.Now()),
		EndTime:              aws.Time(time.Now().Add(24 * time.Hour)),
	})
	require.NoError(t, err)
	require.NotNil(t, fcOut.LoadForecast)
	require.NotNil(t, fcOut.CapacityForecast)

	launchOut, err := c.LaunchInstances(ctx, &autoscaling.LaunchInstancesInput{
		AutoScalingGroupName: aws.String(group),
		RequestedCapacity:    aws.Int32(1),
	})
	require.NoError(t, err)
	assert.Equal(t, group, aws.ToString(launchOut.AutoScalingGroupName))
	require.Len(t, launchOut.Instances, 1)
	require.Len(t, launchOut.Instances[0].InstanceIds, 1)
}

func TestAutoScaling_StaticDescribes(t *testing.T) {
	c := autoScalingClient()

	limOut, err := c.DescribeAccountLimits(ctx, &autoscaling.DescribeAccountLimitsInput{})
	require.NoError(t, err)
	assert.EqualValues(t, 500, aws.ToInt32(limOut.MaxNumberOfAutoScalingGroups))
	assert.EqualValues(t, 200, aws.ToInt32(limOut.MaxNumberOfLaunchConfigurations))

	adjOut, err := c.DescribeAdjustmentTypes(ctx, &autoscaling.DescribeAdjustmentTypesInput{})
	require.NoError(t, err)
	adjs := make([]string, 0)
	for _, a := range adjOut.AdjustmentTypes {
		adjs = append(adjs, aws.ToString(a.AdjustmentType))
	}
	assert.ElementsMatch(t, []string{"ChangeInCapacity", "ExactCapacity", "PercentChangeInCapacity"}, adjs)

	notifOut, err := c.DescribeAutoScalingNotificationTypes(ctx, &autoscaling.DescribeAutoScalingNotificationTypesInput{})
	require.NoError(t, err)
	assert.Contains(t, notifOut.AutoScalingNotificationTypes, "autoscaling:EC2_INSTANCE_LAUNCH")

	hookOut, err := c.DescribeLifecycleHookTypes(ctx, &autoscaling.DescribeLifecycleHookTypesInput{})
	require.NoError(t, err)
	assert.Contains(t, hookOut.LifecycleHookTypes, "autoscaling:EC2_INSTANCE_LAUNCHING")

	metricOut, err := c.DescribeMetricCollectionTypes(ctx, &autoscaling.DescribeMetricCollectionTypesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, metricOut.Metrics)
	require.NotEmpty(t, metricOut.Granularities)
	assert.Equal(t, "1Minute", aws.ToString(metricOut.Granularities[0].Granularity))

	procOut, err := c.DescribeScalingProcessTypes(ctx, &autoscaling.DescribeScalingProcessTypesInput{})
	require.NoError(t, err)
	procs := make([]string, 0)
	for _, p := range procOut.Processes {
		procs = append(procs, aws.ToString(p.ProcessName))
	}
	assert.Contains(t, procs, "Launch")
	assert.Contains(t, procs, "Terminate")

	termOut, err := c.DescribeTerminationPolicyTypes(ctx, &autoscaling.DescribeTerminationPolicyTypesInput{})
	require.NoError(t, err)
	assert.Contains(t, termOut.TerminationPolicyTypes, "Default")
	assert.Contains(t, termOut.TerminationPolicyTypes, "OldestInstance")
}

func TestAutoScaling_CancelInstanceRefreshNotFound(t *testing.T) {
	c := autoScalingClient()
	const group = "asx-cir-grp"
	asxSetupGroup(t, c, "asx-cir-lc", group, 0)

	// CancelInstanceRefresh with no prior refresh must surface the real
	// ActiveInstanceRefreshNotFound error, not silently succeed.
	_, err := c.CancelInstanceRefresh(ctx, &autoscaling.CancelInstanceRefreshInput{
		AutoScalingGroupName: aws.String(group),
	})
	require.Error(t, err)
}
