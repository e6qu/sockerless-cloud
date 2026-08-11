package aws_sdk_test

import (
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cttypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func autoScalingClient() *autoscaling.Client {
	return autoscaling.NewFromConfig(sdkConfig(), func(o *autoscaling.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func cloudTrailClient() *cloudtrail.Client {
	return cloudtrail.NewFromConfig(sdkConfig(), func(o *cloudtrail.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestAutoScalingGroupLifecycleSDK(t *testing.T) {
	asgClient := autoScalingClient()
	ec2Client := ec2Client()

	vpcOut, err := ec2Client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.90.0.0/16")})
	require.NoError(t, err)
	subnetOut, err := ec2Client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            vpcOut.Vpc.VpcId,
		CidrBlock:        aws.String("10.90.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)

	_, err = asgClient.CreateLaunchConfiguration(ctx, &autoscaling.CreateLaunchConfigurationInput{
		LaunchConfigurationName: aws.String("sdk-lc"),
		ImageId:                 aws.String("ami-asg1234"),
		InstanceType:            aws.String("t3.micro"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = asgClient.DeleteLaunchConfiguration(ctx, &autoscaling.DeleteLaunchConfigurationInput{
			LaunchConfigurationName: aws.String("sdk-lc"),
		})
	})

	lcOut, err := asgClient.DescribeLaunchConfigurations(ctx, &autoscaling.DescribeLaunchConfigurationsInput{
		LaunchConfigurationNames: []string{"sdk-lc"},
	})
	require.NoError(t, err)
	require.Len(t, lcOut.LaunchConfigurations, 1)
	assert.Equal(t, "ami-asg1234", aws.ToString(lcOut.LaunchConfigurations[0].ImageId))

	_, err = asgClient.CreateAutoScalingGroup(ctx, &autoscaling.CreateAutoScalingGroupInput{
		AutoScalingGroupName:    aws.String("sdk-asg"),
		LaunchConfigurationName: aws.String("sdk-lc"),
		MinSize:                 aws.Int32(1),
		MaxSize:                 aws.Int32(2),
		DesiredCapacity:         aws.Int32(1),
		VPCZoneIdentifier:       subnetOut.Subnet.SubnetId,
		Tags: []types.Tag{{
			Key:               aws.String("env"),
			Value:             aws.String("sdk"),
			PropagateAtLaunch: aws.Bool(true),
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = asgClient.DeleteAutoScalingGroup(ctx, &autoscaling.DeleteAutoScalingGroupInput{
			AutoScalingGroupName: aws.String("sdk-asg"),
			ForceDelete:          aws.Bool(true),
		})
	})

	groupsOut, err := asgClient.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{"sdk-asg"},
	})
	require.NoError(t, err)
	require.Len(t, groupsOut.AutoScalingGroups, 1)
	require.Len(t, groupsOut.AutoScalingGroups[0].Instances, 1)
	instanceID := aws.ToString(groupsOut.AutoScalingGroups[0].Instances[0].InstanceId)
	require.NotEmpty(t, instanceID)

	instOut, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	require.Len(t, instOut.Reservations, 1)
	assert.Equal(t, ec2types.InstanceStateNameRunning, instOut.Reservations[0].Instances[0].State.Name)

	_, err = asgClient.SetDesiredCapacity(ctx, &autoscaling.SetDesiredCapacityInput{
		AutoScalingGroupName: aws.String("sdk-asg"),
		DesiredCapacity:      aws.Int32(2),
	})
	require.NoError(t, err)

	groupsOut, err = asgClient.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{"sdk-asg"},
	})
	require.NoError(t, err)
	require.Len(t, groupsOut.AutoScalingGroups, 1)
	require.Len(t, groupsOut.AutoScalingGroups[0].Instances, 2)

	activitiesOut, err := asgClient.DescribeScalingActivities(ctx, &autoscaling.DescribeScalingActivitiesInput{
		AutoScalingGroupName: aws.String("sdk-asg"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, activitiesOut.Activities)

	// DescribeAutoScalingGroups Filters: the sim used to ignore Filters and
	// return every group. A tag:env=sdk filter must return only sdk-asg, and a
	// tag:env=nope filter must return nothing.
	matched, err := asgClient.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		Filters: []types.Filter{{Name: aws.String("tag:env"), Values: []string{"sdk"}}},
	})
	require.NoError(t, err)
	names := make([]string, 0)
	for _, g := range matched.AutoScalingGroups {
		names = append(names, aws.ToString(g.AutoScalingGroupName))
	}
	assert.Contains(t, names, "sdk-asg")

	none, err := asgClient.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		Filters: []types.Filter{{Name: aws.String("tag:env"), Values: []string{"nope"}}},
	})
	require.NoError(t, err)
	for _, g := range none.AutoScalingGroups {
		assert.NotEqual(t, "sdk-asg", aws.ToString(g.AutoScalingGroupName),
			"tag:env=nope must not match sdk-asg")
	}
}

// TestAutoScalingPoliciesAndHooksSDK exercises the scaling-policy,
// scheduled-action, lifecycle-hook, and per-instance verbs of EC2 Auto
// Scaling against a real group, asserting round-trip read-back.
func TestAutoScalingPoliciesAndHooksSDK(t *testing.T) {
	asgClient := autoScalingClient()
	ec2Client := ec2Client()

	vpcOut, err := ec2Client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.92.0.0/16")})
	require.NoError(t, err)
	subnetOut, err := ec2Client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            vpcOut.Vpc.VpcId,
		CidrBlock:        aws.String("10.92.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)

	_, err = asgClient.CreateLaunchConfiguration(ctx, &autoscaling.CreateLaunchConfigurationInput{
		LaunchConfigurationName: aws.String("pol-lc"),
		ImageId:                 aws.String("ami-pol1234"),
		InstanceType:            aws.String("t3.small"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = asgClient.DeleteLaunchConfiguration(ctx, &autoscaling.DeleteLaunchConfigurationInput{
			LaunchConfigurationName: aws.String("pol-lc"),
		})
	})

	_, err = asgClient.CreateAutoScalingGroup(ctx, &autoscaling.CreateAutoScalingGroupInput{
		AutoScalingGroupName:    aws.String("pol-asg"),
		LaunchConfigurationName: aws.String("pol-lc"),
		MinSize:                 aws.Int32(1),
		MaxSize:                 aws.Int32(4),
		DesiredCapacity:         aws.Int32(2),
		VPCZoneIdentifier:       subnetOut.Subnet.SubnetId,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = asgClient.DeleteAutoScalingGroup(ctx, &autoscaling.DeleteAutoScalingGroupInput{
			AutoScalingGroupName: aws.String("pol-asg"),
			ForceDelete:          aws.Bool(true),
		})
	})

	// Scaling policy round-trip.
	putPol, err := asgClient.PutScalingPolicy(ctx, &autoscaling.PutScalingPolicyInput{
		AutoScalingGroupName: aws.String("pol-asg"),
		PolicyName:           aws.String("scale-out"),
		PolicyType:           aws.String("SimpleScaling"),
		AdjustmentType:       aws.String("ChangeInCapacity"),
		ScalingAdjustment:    aws.Int32(1),
		Cooldown:             aws.Int32(120),
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(putPol.PolicyARN), ":scalingPolicy:")

	polOut, err := asgClient.DescribePolicies(ctx, &autoscaling.DescribePoliciesInput{
		AutoScalingGroupName: aws.String("pol-asg"),
	})
	require.NoError(t, err)
	require.Len(t, polOut.ScalingPolicies, 1)
	assert.Equal(t, "scale-out", aws.ToString(polOut.ScalingPolicies[0].PolicyName))
	assert.Equal(t, int32(1), aws.ToInt32(polOut.ScalingPolicies[0].ScalingAdjustment))

	// ExecutePolicy bumps DesiredCapacity by the scaling adjustment.
	_, err = asgClient.ExecutePolicy(ctx, &autoscaling.ExecutePolicyInput{
		AutoScalingGroupName: aws.String("pol-asg"),
		PolicyName:           aws.String("scale-out"),
	})
	require.NoError(t, err)
	grpOut, err := asgClient.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{"pol-asg"},
	})
	require.NoError(t, err)
	require.Len(t, grpOut.AutoScalingGroups, 1)
	assert.Equal(t, int32(3), aws.ToInt32(grpOut.AutoScalingGroups[0].DesiredCapacity))

	_, err = asgClient.DeletePolicy(ctx, &autoscaling.DeletePolicyInput{
		AutoScalingGroupName: aws.String("pol-asg"),
		PolicyName:           aws.String("scale-out"),
	})
	require.NoError(t, err)
	polOut, err = asgClient.DescribePolicies(ctx, &autoscaling.DescribePoliciesInput{
		AutoScalingGroupName: aws.String("pol-asg"),
	})
	require.NoError(t, err)
	assert.Empty(t, polOut.ScalingPolicies)

	// Scheduled action round-trip.
	_, err = asgClient.PutScheduledUpdateGroupAction(ctx, &autoscaling.PutScheduledUpdateGroupActionInput{
		AutoScalingGroupName: aws.String("pol-asg"),
		ScheduledActionName:  aws.String("nightly"),
		Recurrence:           aws.String("0 0 * * *"),
		MinSize:              aws.Int32(0),
		MaxSize:              aws.Int32(5),
		DesiredCapacity:      aws.Int32(1),
	})
	require.NoError(t, err)
	schedOut, err := asgClient.DescribeScheduledActions(ctx, &autoscaling.DescribeScheduledActionsInput{
		AutoScalingGroupName: aws.String("pol-asg"),
	})
	require.NoError(t, err)
	require.Len(t, schedOut.ScheduledUpdateGroupActions, 1)
	assert.Equal(t, "nightly", aws.ToString(schedOut.ScheduledUpdateGroupActions[0].ScheduledActionName))
	assert.Equal(t, "0 0 * * *", aws.ToString(schedOut.ScheduledUpdateGroupActions[0].Recurrence))

	_, err = asgClient.DeleteScheduledAction(ctx, &autoscaling.DeleteScheduledActionInput{
		AutoScalingGroupName: aws.String("pol-asg"),
		ScheduledActionName:  aws.String("nightly"),
	})
	require.NoError(t, err)

	// Lifecycle hook round-trip.
	_, err = asgClient.PutLifecycleHook(ctx, &autoscaling.PutLifecycleHookInput{
		AutoScalingGroupName: aws.String("pol-asg"),
		LifecycleHookName:    aws.String("drain"),
		LifecycleTransition:  aws.String("autoscaling:EC2_INSTANCE_TERMINATING"),
		HeartbeatTimeout:     aws.Int32(300),
		DefaultResult:        aws.String("CONTINUE"),
	})
	require.NoError(t, err)
	hookOut, err := asgClient.DescribeLifecycleHooks(ctx, &autoscaling.DescribeLifecycleHooksInput{
		AutoScalingGroupName: aws.String("pol-asg"),
	})
	require.NoError(t, err)
	require.Len(t, hookOut.LifecycleHooks, 1)
	assert.Equal(t, "drain", aws.ToString(hookOut.LifecycleHooks[0].LifecycleHookName))
	assert.Equal(t, "CONTINUE", aws.ToString(hookOut.LifecycleHooks[0].DefaultResult))

	_, err = asgClient.DeleteLifecycleHook(ctx, &autoscaling.DeleteLifecycleHookInput{
		AutoScalingGroupName: aws.String("pol-asg"),
		LifecycleHookName:    aws.String("drain"),
	})
	require.NoError(t, err)

	// Per-instance verbs.
	asgInstances, err := asgClient.DescribeAutoScalingInstances(ctx, &autoscaling.DescribeAutoScalingInstancesInput{})
	require.NoError(t, err)
	var instanceID string
	for _, d := range asgInstances.AutoScalingInstances {
		if aws.ToString(d.AutoScalingGroupName) == "pol-asg" {
			instanceID = aws.ToString(d.InstanceId)
			break
		}
	}
	require.NotEmpty(t, instanceID, "DescribeAutoScalingInstances must list pol-asg's instances")

	termOut, err := asgClient.TerminateInstanceInAutoScalingGroup(ctx, &autoscaling.TerminateInstanceInAutoScalingGroupInput{
		InstanceId:                     aws.String(instanceID),
		ShouldDecrementDesiredCapacity: aws.Bool(true),
	})
	require.NoError(t, err)
	require.NotNil(t, termOut.Activity)
	assert.Equal(t, "pol-asg", aws.ToString(termOut.Activity.AutoScalingGroupName))

	// SetInstanceHealth on a remaining instance is accepted.
	grpOut, err = asgClient.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{"pol-asg"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, grpOut.AutoScalingGroups[0].Instances)
	healthyID := aws.ToString(grpOut.AutoScalingGroups[0].Instances[0].InstanceId)
	_, err = asgClient.SetInstanceHealth(ctx, &autoscaling.SetInstanceHealthInput{
		InstanceId:   aws.String(healthyID),
		HealthStatus: aws.String("Healthy"),
	})
	require.NoError(t, err)
}

func TestCloudTrailRecordsAPICallsToS3SDK(t *testing.T) {
	ctClient := cloudTrailClient()
	s3Client := s3Client()
	ec2Client := ec2Client()
	bucket := "sdk-cloudtrail-bucket"

	_, err := s3Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	createOut, err := ctClient.CreateTrail(ctx, &cloudtrail.CreateTrailInput{
		Name:         aws.String("sdk-trail"),
		S3BucketName: aws.String(bucket),
		S3KeyPrefix:  aws.String("trail-logs"),
	})
	require.NoError(t, err)
	assert.Equal(t, "sdk-trail", aws.ToString(createOut.Name))
	assert.Contains(t, aws.ToString(createOut.TrailARN), ":trail/sdk-trail")

	statusOut, err := ctClient.GetTrailStatus(ctx, &cloudtrail.GetTrailStatusInput{Name: aws.String("sdk-trail")})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(statusOut.IsLogging))

	_, err = ctClient.StartLogging(ctx, &cloudtrail.StartLoggingInput{Name: aws.String("sdk-trail")})
	require.NoError(t, err)

	statusOut, err = ctClient.GetTrailStatus(ctx, &cloudtrail.GetTrailStatusInput{Name: aws.String("sdk-trail")})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(statusOut.IsLogging))

	_, err = ec2Client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.91.0.0/16")})
	require.NoError(t, err)

	eventsOut, err := ctClient.LookupEvents(ctx, &cloudtrail.LookupEventsInput{})
	require.NoError(t, err)
	foundCreateVpc := false
	for _, event := range eventsOut.Events {
		if aws.ToString(event.EventName) == "CreateVpc" {
			foundCreateVpc = true
			// EC2 is a query-protocol service; the recorded event must carry
			// its real CloudTrail eventSource so EventSource-keyed LookupEvents
			// filters work (not a generic aws.amazonaws.com).
			assert.Equal(t, "ec2.amazonaws.com", aws.ToString(event.EventSource))
			break
		}
	}
	assert.True(t, foundCreateVpc, "LookupEvents must include the recorded EC2 CreateVpc management event")

	// EventSource-keyed lookup must find the EC2 event (the filter real
	// consumers use to scope CloudTrail to one service).
	bySource, err := ctClient.LookupEvents(ctx, &cloudtrail.LookupEventsInput{
		LookupAttributes: []cttypes.LookupAttribute{{
			AttributeKey:   cttypes.LookupAttributeKeyEventSource,
			AttributeValue: aws.String("ec2.amazonaws.com"),
		}},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, bySource.Events, "LookupEvents filtered by EventSource=ec2.amazonaws.com must return the EC2 event")

	objectsOut, err := s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String("trail-logs/AWSLogs/123456789012/CloudTrail/us-east-1/"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, objectsOut.Contents)

	getOut, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    objectsOut.Contents[0].Key,
	})
	require.NoError(t, err)
	defer getOut.Body.Close()
	body, err := io.ReadAll(getOut.Body)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(body), "\x1f\x8b"), "CloudTrail log objects are gzip streams")

	_, err = ctClient.StopLogging(ctx, &cloudtrail.StopLoggingInput{Name: aws.String("sdk-trail")})
	require.NoError(t, err)
	_, err = ctClient.DeleteTrail(ctx, &cloudtrail.DeleteTrailInput{Name: aws.String("sdk-trail")})
	require.NoError(t, err)
}
