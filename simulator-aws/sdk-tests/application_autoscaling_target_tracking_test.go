package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAppScaling_TargetTrackingScalesECSService proves a target-tracking
// policy actually adjusts capacity: with CPU pinned well above the target the
// service's DesiredCount grows toward MaxCapacity; with CPU dropped below the
// target it shrinks back toward MinCapacity. This is the real Application Auto
// Scaling engine (AnyScaleFrontendService) reading CloudWatch Metrics and
// driving ECS UpdateService — the runner-platform autoscaling flow end-to-end.
func TestAppScaling_TargetTrackingScalesECSService(t *testing.T) {
	ecsC := ecsClient()
	cwC := cloudwatchClient()
	asC := appAutoScalingClient()

	cluster := "as-track-cluster"
	svcName := "as-track-svc"
	resourceID := "service/" + cluster + "/" + svcName
	const (
		ns   = aastypes.ServiceNamespaceEcs
		dim  = aastypes.ScalableDimensionECSServiceDesiredCount
		minC = int32(1)
		maxC = int32(10)
	)

	_, err := ecsC.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)

	_, err = ecsC.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("as-track-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name: aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)

	_, err = ecsC.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String(svcName),
		TaskDefinition: aws.String("as-track-task"),
		DesiredCount:   aws.Int32(1),
	})
	require.NoError(t, err)
	cleanupECSService(t, ecsC, cluster, svcName)

	_, err = asC.RegisterScalableTarget(ctx, &applicationautoscaling.RegisterScalableTargetInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
		MinCapacity:       aws.Int32(minC),
		MaxCapacity:       aws.Int32(maxC),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = asC.DeregisterScalableTarget(ctx, &applicationautoscaling.DeregisterScalableTargetInput{
			ServiceNamespace: ns, ResourceId: aws.String(resourceID), ScalableDimension: dim,
		})
	})

	_, err = asC.PutScalingPolicy(ctx, &applicationautoscaling.PutScalingPolicyInput{
		PolicyName:        aws.String("cpu-50"),
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
		PolicyType:        aastypes.PolicyTypeTargetTrackingScaling,
		TargetTrackingScalingPolicyConfiguration: &aastypes.TargetTrackingScalingPolicyConfiguration{
			TargetValue: aws.Float64(50.0),
			PredefinedMetricSpecification: &aastypes.PredefinedMetricSpecification{
				PredefinedMetricType: aastypes.MetricTypeECSServiceAverageCPUUtilization,
			},
		},
	})
	require.NoError(t, err)

	putCPU := func(value float64) {
		_, err := cwC.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
			Namespace: aws.String("AWS/ECS"),
			MetricData: []cwtypes.MetricDatum{{
				MetricName: aws.String("CPUUtilization"),
				Dimensions: []cwtypes.Dimension{
					{Name: aws.String("ClusterName"), Value: aws.String(cluster)},
					{Name: aws.String("ServiceName"), Value: aws.String(svcName)},
				},
				Value:     aws.Float64(value),
				Timestamp: aws.Time(time.Now()),
			}},
		})
		require.NoError(t, err)
	}

	desiredCount := func() int32 {
		out, err := ecsC.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster:  aws.String(cluster),
			Services: []string{svcName},
		})
		require.NoError(t, err)
		require.Len(t, out.Services, 1)
		return out.Services[0].DesiredCount
	}

	// Scale-out: pin CPU at 90% (target 50%). round(1 * 90/50) = 2 on the
	// first tick; subsequent ticks keep climbing toward MaxCapacity as long as
	// the metric stays high. Wait for at least one increase above the floor.
	putCPU(90.0)
	require.Eventually(t, func() bool {
		// Keep feeding datapoints so the 5-minute lookback window never goes empty.
		putCPU(90.0)
		return desiredCount() > minC
	}, 30*time.Second, 3*time.Second, "DesiredCount must increase when CPU is above the target")

	// Confirm the autoscaler respects MaxCapacity: it must never exceed it.
	assert.LessOrEqual(t, desiredCount(), maxC)

	// Scale-in: drop CPU to 10% (target 50%). round(current * 10/50) shrinks
	// toward MinCapacity. Wait for the count to fall back toward the floor.
	putCPU(10.0)
	require.Eventually(t, func() bool {
		putCPU(10.0)
		return desiredCount() <= minC
	}, 30*time.Second, 3*time.Second, "DesiredCount must decrease toward MinCapacity when CPU is below the target")

	// MinCapacity is a hard floor — never below it.
	assert.GreaterOrEqual(t, desiredCount(), minC)

	// The capacity change must surface as a ScalingActivity.
	actOut, err := asC.DescribeScalingActivities(ctx, &applicationautoscaling.DescribeScalingActivitiesInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
	})
	require.NoError(t, err)
	require.NotEmpty(t, actOut.ScalingActivities, "a real capacity change must record a ScalingActivity")
}
