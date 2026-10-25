package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An Amazon ECS service reports its CPUUtilization and MemoryUtilization to
// CloudWatch each minute, under AWS/ECS with its ClusterName and ServiceName:
// a task that keeps a core busy under a 512-unit task size uses all the CPU it
// reserves.
func TestECS_ServiceReportsItsUtilization(t *testing.T) {
	t.Parallel()
	ecsC, cwC := ecsClient(), cloudwatchClient()
	cluster, service, family := uniquename.New("metrics-cluster"), uniquename.New("metrics-svc"), uniquename.New("metrics-task")
	_, err := ecsC.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	_, err = ecsC.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family), Cpu: aws.String("512"), Memory: aws.String("1024"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"spin"},
		}},
	})
	require.NoError(t, err)
	started := time.Now()
	_, err = ecsC.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(service), TaskDefinition: aws.String(family), DesiredCount: aws.Int32(1),
	})
	require.NoError(t, err)
	cleanupECSService(t, ecsC, cluster, service)

	dims := []cwtypes.Dimension{
		{Name: aws.String("ClusterName"), Value: aws.String(cluster)},
		{Name: aws.String("ServiceName"), Value: aws.String(service)},
	}
	statistics := func(metric string) []cwtypes.Datapoint {
		out, err := cwC.GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
			Namespace: aws.String("AWS/ECS"), MetricName: aws.String(metric), Dimensions: dims,
			StartTime: aws.Time(started.Add(-time.Minute)), EndTime: aws.Time(time.Now().Add(time.Minute)),
			Period:     aws.Int32(60),
			Statistics: []cwtypes.Statistic{cwtypes.StatisticAverage, cwtypes.StatisticSampleCount},
		})
		require.NoError(t, err)
		return out.Datapoints
	}
	// Amazon ECS sends a minute's datapoint once the minute has passed, so
	// read the metric on its own one-minute cadence's scale.
	var cpu []cwtypes.Datapoint
	require.Eventually(t, func() bool {
		cpu = statistics("CPUUtilization")
		return len(cpu) > 0
	}, 3*time.Minute, 5*time.Second, "the service's first closed minute reaches CloudWatch")
	busiest := 0.0
	for _, point := range cpu {
		busiest = max(busiest, aws.ToFloat64(point.Average))
		assert.Equal(t, cwtypes.StandardUnitPercent, point.Unit)
	}
	assert.Greater(t, busiest, 50.0, "a task spinning at its CPU limit uses most of what it reserves: %+v", cpu)

	memory := statistics("MemoryUtilization")
	require.NotEmpty(t, memory)
	for _, point := range memory {
		assert.Greater(t, aws.ToFloat64(point.Average), 0.0)
		assert.Less(t, aws.ToFloat64(point.Average), 100.0)
	}
}
