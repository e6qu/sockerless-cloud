package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func appAutoScalingClient() *applicationautoscaling.Client {
	return applicationautoscaling.NewFromConfig(sdkConfig(), func(o *applicationautoscaling.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestAppAutoScaling_TargetAndPolicyLifecycle registers a scalable target for
// an Amazon ECS service, attaches a target-tracking policy, reads both back
// and tears them down.
func TestAppAutoScaling_TargetAndPolicyLifecycle(t *testing.T) {
	t.Parallel()
	c := appAutoScalingClient()
	const (
		ns         = aastypes.ServiceNamespaceEcs
		resourceID = "service/runner-cluster/runner-svc"
		dimension  = aastypes.ScalableDimensionECSServiceDesiredCount
	)

	regOut, err := c.RegisterScalableTarget(ctx, &applicationautoscaling.RegisterScalableTargetInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dimension,
		MinCapacity:       aws.Int32(1),
		MaxCapacity:       aws.Int32(4),
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(regOut.ScalableTargetARN), ":scalable-target/",
		"RegisterScalableTarget must return a scalable-target ARN")

	descOut, err := c.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{
		ServiceNamespace: ns,
		ResourceIds:      []string{resourceID},
	})
	require.NoError(t, err)
	require.Len(t, descOut.ScalableTargets, 1)
	tgt := descOut.ScalableTargets[0]
	assert.Equal(t, resourceID, aws.ToString(tgt.ResourceId))
	assert.EqualValues(t, 1, aws.ToInt32(tgt.MinCapacity))
	assert.EqualValues(t, 4, aws.ToInt32(tgt.MaxCapacity))
	assert.Equal(t, dimension, tgt.ScalableDimension)

	// Registering again with a higher MaxCapacity updates the target.
	_, err = c.RegisterScalableTarget(ctx, &applicationautoscaling.RegisterScalableTargetInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dimension,
		MinCapacity:       aws.Int32(2),
		MaxCapacity:       aws.Int32(8),
	})
	require.NoError(t, err)
	descOut, err = c.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{
		ServiceNamespace: ns,
		ResourceIds:      []string{resourceID},
	})
	require.NoError(t, err)
	require.Len(t, descOut.ScalableTargets, 1, "re-register must update in place, not duplicate")
	assert.EqualValues(t, 8, aws.ToInt32(descOut.ScalableTargets[0].MaxCapacity))

	putOut, err := c.PutScalingPolicy(ctx, &applicationautoscaling.PutScalingPolicyInput{
		PolicyName:        aws.String("cpu-target-tracking"),
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dimension,
		PolicyType:        aastypes.PolicyTypeTargetTrackingScaling,
		TargetTrackingScalingPolicyConfiguration: &aastypes.TargetTrackingScalingPolicyConfiguration{
			TargetValue: aws.Float64(60.0),
			PredefinedMetricSpecification: &aastypes.PredefinedMetricSpecification{
				PredefinedMetricType: aastypes.MetricTypeECSServiceAverageCPUUtilization,
			},
		},
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(putOut.PolicyARN), ":scalingPolicy:")

	polOut, err := c.DescribeScalingPolicies(ctx, &applicationautoscaling.DescribeScalingPoliciesInput{
		ServiceNamespace: ns,
		ResourceId:       aws.String(resourceID),
	})
	require.NoError(t, err)
	require.Len(t, polOut.ScalingPolicies, 1)
	pol := polOut.ScalingPolicies[0]
	assert.Equal(t, "cpu-target-tracking", aws.ToString(pol.PolicyName))
	assert.Equal(t, aastypes.PolicyTypeTargetTrackingScaling, pol.PolicyType)
	require.NotNil(t, pol.TargetTrackingScalingPolicyConfiguration, "target-tracking config must round-trip")
	assert.EqualValues(t, 60.0, aws.ToFloat64(pol.TargetTrackingScalingPolicyConfiguration.TargetValue))

	_, err = c.DeleteScalingPolicy(ctx, &applicationautoscaling.DeleteScalingPolicyInput{
		PolicyName:        aws.String("cpu-target-tracking"),
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dimension,
	})
	require.NoError(t, err)

	_, err = c.DeregisterScalableTarget(ctx, &applicationautoscaling.DeregisterScalableTargetInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dimension,
	})
	require.NoError(t, err)

	descOut, err = c.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{
		ServiceNamespace: ns,
		ResourceIds:      []string{resourceID},
	})
	require.NoError(t, err)
	assert.Empty(t, descOut.ScalableTargets, "deregistered target must no longer be described")
}

// TestAppAutoScaling_Tags covers the tags terraform-provider-aws reads through
// ListTagsForResource on every refresh of a tagged target.
func TestAppAutoScaling_Tags(t *testing.T) {
	t.Parallel()
	c := appAutoScalingClient()
	const (
		ns         = aastypes.ServiceNamespaceEcs
		resourceID = "service/tag-cluster/tag-svc"
		dimension  = aastypes.ScalableDimensionECSServiceDesiredCount
	)

	regOut, err := c.RegisterScalableTarget(ctx, &applicationautoscaling.RegisterScalableTargetInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dimension,
		MinCapacity:       aws.Int32(1),
		MaxCapacity:       aws.Int32(3),
		Tags:              map[string]string{"team": "runners"},
	})
	require.NoError(t, err)
	arn := aws.ToString(regOut.ScalableTargetARN)
	require.NotEmpty(t, arn)
	t.Cleanup(func() {
		c.DeregisterScalableTarget(ctx, &applicationautoscaling.DeregisterScalableTargetInput{
			ServiceNamespace: ns, ResourceId: aws.String(resourceID), ScalableDimension: dimension,
		})
	})

	listOut, err := c.ListTagsForResource(ctx, &applicationautoscaling.ListTagsForResourceInput{
		ResourceARN: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Equal(t, "runners", listOut.Tags["team"], "tags set at register time must be listable")

	_, err = c.TagResource(ctx, &applicationautoscaling.TagResourceInput{
		ResourceARN: aws.String(arn),
		Tags:        map[string]string{"env": "ci"},
	})
	require.NoError(t, err)
	listOut, err = c.ListTagsForResource(ctx, &applicationautoscaling.ListTagsForResourceInput{
		ResourceARN: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Equal(t, "ci", listOut.Tags["env"])
	assert.Equal(t, "runners", listOut.Tags["team"], "TagResource must merge, not replace")

	_, err = c.UntagResource(ctx, &applicationautoscaling.UntagResourceInput{
		ResourceARN: aws.String(arn),
		TagKeys:     []string{"env"},
	})
	require.NoError(t, err)
	listOut, err = c.ListTagsForResource(ctx, &applicationautoscaling.ListTagsForResourceInput{
		ResourceARN: aws.String(arn),
	})
	require.NoError(t, err)
	_, present := listOut.Tags["env"]
	assert.False(t, present, "UntagResource must remove the named key")
	assert.Equal(t, "runners", listOut.Tags["team"])
}

func TestAppAutoScaling_PolicyRequiresTarget(t *testing.T) {
	t.Parallel()
	c := appAutoScalingClient()
	_, err := c.PutScalingPolicy(ctx, &applicationautoscaling.PutScalingPolicyInput{
		PolicyName:        aws.String("orphan"),
		ServiceNamespace:  aastypes.ServiceNamespaceEcs,
		ResourceId:        aws.String("service/no-cluster/no-svc"),
		ScalableDimension: aastypes.ScalableDimensionECSServiceDesiredCount,
		PolicyType:        aastypes.PolicyTypeTargetTrackingScaling,
		TargetTrackingScalingPolicyConfiguration: &aastypes.TargetTrackingScalingPolicyConfiguration{
			TargetValue: aws.Float64(50.0),
			PredefinedMetricSpecification: &aastypes.PredefinedMetricSpecification{
				PredefinedMetricType: aastypes.MetricTypeECSServiceAverageCPUUtilization,
			},
		},
	})
	requireAWSErrorCode(t, err, "ObjectNotFoundException")
}
