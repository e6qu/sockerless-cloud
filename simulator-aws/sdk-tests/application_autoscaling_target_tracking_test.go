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
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
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

	policy, err := asC.PutScalingPolicy(ctx, &applicationautoscaling.PutScalingPolicyInput{
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

	// Application Auto Scaling manages two CloudWatch alarms for the policy:
	// AlarmHigh over three one-minute periods above the target, and AlarmLow
	// over fifteen below 90% of it.
	require.Len(t, policy.Alarms, 2)
	alarmNames := make([]string, 0, len(policy.Alarms))
	for _, alarm := range policy.Alarms {
		alarmNames = append(alarmNames, aws.ToString(alarm.AlarmName))
	}
	alarms, err := cwC.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: alarmNames})
	require.NoError(t, err)
	require.Len(t, alarms.MetricAlarms, 2)
	for _, alarm := range alarms.MetricAlarms {
		assert.Equal(t, int32(60), aws.ToInt32(alarm.Period))
		assert.Equal(t, []string{aws.ToString(policy.PolicyARN)}, alarm.AlarmActions)
		switch alarm.ComparisonOperator {
		case cwtypes.ComparisonOperatorGreaterThanThreshold:
			assert.Equal(t, int32(3), aws.ToInt32(alarm.EvaluationPeriods))
			assert.Equal(t, 50.0, aws.ToFloat64(alarm.Threshold))
		case cwtypes.ComparisonOperatorLessThanThreshold:
			assert.Equal(t, int32(15), aws.ToInt32(alarm.EvaluationPeriods))
			assert.Equal(t, 45.0, aws.ToFloat64(alarm.Threshold))
		default:
			t.Fatalf("unexpected alarm %s comparing %s", aws.ToString(alarm.AlarmName), alarm.ComparisonOperator)
		}
	}

	// putCPU records copies datapoints of value for each of the last minutes,
	// as the Amazon ECS agent reports one a minute.
	putCPU := func(value float64, minutes, copies int) {
		now := time.Now()
		var data []cwtypes.MetricDatum
		for minute := 0; minute < minutes; minute++ {
			for range copies {
				data = append(data, cwtypes.MetricDatum{
					MetricName: aws.String("CPUUtilization"),
					Dimensions: []cwtypes.Dimension{
						{Name: aws.String("ClusterName"), Value: aws.String(cluster)},
						{Name: aws.String("ServiceName"), Value: aws.String(svcName)},
					},
					Value:     aws.Float64(value),
					Timestamp: aws.Time(now.Add(-time.Duration(minute) * time.Minute)),
				})
			}
		}
		_, err := cwC.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String("AWS/ECS"), MetricData: data})
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

	// Scale-out: three minutes at 90% (target 50%) put AlarmHigh in ALARM,
	// and the policy sets round(1 * 90 / 50) = 2.
	putCPU(90.0, 3, 1)
	require.Eventually(t, func() bool {
		return desiredCount() > minC
	}, 30*time.Second, time.Second, "DesiredCount must increase once AlarmHigh is in ALARM")
	assert.LessOrEqual(t, desiredCount(), maxC)

	// Scale-in: fifteen minutes averaging well below 45% put AlarmLow in ALARM.
	putCPU(0.0, 15, 4)
	require.Eventually(t, func() bool {
		return desiredCount() <= minC
	}, 30*time.Second, time.Second, "DesiredCount must decrease toward MinCapacity once AlarmLow is in ALARM")
	assert.GreaterOrEqual(t, desiredCount(), minC)

	// Each capacity change surfaces as a ScalingActivity the alarm caused.
	actOut, err := asC.DescribeScalingActivities(ctx, &applicationautoscaling.DescribeScalingActivitiesInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
	})
	require.NoError(t, err)
	require.Len(t, actOut.ScalingActivities, 2)
	for _, activity := range actOut.ScalingActivities {
		assert.Contains(t, aws.ToString(activity.Cause), "TargetTracking-"+resourceID)
	}

	_, err = asC.DeleteScalingPolicy(ctx, &applicationautoscaling.DeleteScalingPolicyInput{
		PolicyName: aws.String("cpu-50"), ServiceNamespace: ns, ResourceId: aws.String(resourceID), ScalableDimension: dim,
	})
	require.NoError(t, err)
	alarms, err = cwC.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: alarmNames})
	require.NoError(t, err)
	assert.Empty(t, alarms.MetricAlarms, "deleting the policy deletes the alarms Application Auto Scaling created")
}
