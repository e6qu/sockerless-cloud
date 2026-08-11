package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/require"
)

func registerECSServiceDeploymentTask(
	t *testing.T,
	client *ecs.Client,
	family string,
	command []string,
) string {
	t.Helper()
	output, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:      aws.String("app"),
			Image:     aws.String(containerCommandImage),
			Command:   command,
			Essential: aws.Bool(true),
		}},
	})
	require.NoError(t, err)
	return aws.ToString(output.TaskDefinition.TaskDefinitionArn)
}

func waitForECSServiceTaskDefinition(
	t *testing.T,
	client *ecs.Client,
	cluster, serviceName, taskDefinition string,
) ecstypes.Service {
	t.Helper()
	var found ecstypes.Service
	require.Eventually(t, func() bool {
		output, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster: aws.String(cluster), Services: []string{serviceName},
		})
		if err != nil || len(output.Services) != 1 {
			return false
		}
		found = output.Services[0]
		return aws.ToString(found.TaskDefinition) == taskDefinition &&
			found.RunningCount == found.DesiredCount &&
			found.PendingCount == 0 &&
			len(found.Deployments) > 0 &&
			found.Deployments[0].RolloutState == ecstypes.DeploymentRolloutStateCompleted
	}, 30*time.Second, 100*time.Millisecond)
	return found
}

func createRollbackTestService(
	t *testing.T,
	client *ecs.Client,
	cluster, serviceName, family string,
) string {
	t.Helper()
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: aws.String(cluster), Service: aws.String(serviceName), Force: aws.Bool(true),
		})
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})
	stable := registerECSServiceDeploymentTask(t, client, family, []string{"hold"})
	_, err = client.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String(serviceName),
		TaskDefinition: aws.String(stable),
		DesiredCount:   aws.Int32(1),
	})
	require.NoError(t, err)
	waitForECSServiceTaskDefinition(t, client, cluster, serviceName, stable)
	return stable
}

func serviceEventsContain(service ecstypes.Service, fragment string) bool {
	for _, event := range service.Events {
		if strings.Contains(aws.ToString(event.Message), fragment) {
			return true
		}
	}
	return false
}

func serviceEventMessages(service ecstypes.Service) []string {
	messages := make([]string, 0, len(service.Events))
	for _, event := range service.Events {
		messages = append(messages, aws.ToString(event.Message))
	}
	return messages
}

// TestECS_ServiceDeploymentCircuitBreakerRollsBack proves that real task
// failures feed the persisted scheduler failure counter and restore the last
// completed task definition when the configured threshold is reached.
func TestECS_ServiceDeploymentCircuitBreakerRollsBack(t *testing.T) {
	client := ecsClient()
	const (
		cluster     = "circuit-breaker-cluster"
		serviceName = "circuit-breaker-service"
		family      = "circuit-breaker-task"
	)
	stable := createRollbackTestService(t, client, cluster, serviceName, family)
	failing := registerECSServiceDeploymentTask(t, client, family, []string{"not-a-supported-command"})

	_, err := client.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:        aws.String(cluster),
		Service:        aws.String(serviceName),
		TaskDefinition: aws.String(failing),
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			DeploymentCircuitBreaker: &ecstypes.DeploymentCircuitBreaker{
				Enable:   true,
				Rollback: true,
				ThresholdConfiguration: &ecstypes.ThresholdConfiguration{
					Type:  ecstypes.ThresholdTypeCount,
					Value: 1,
				},
			},
		},
	})
	require.NoError(t, err)

	rolledBack := waitForECSServiceTaskDefinition(t, client, cluster, serviceName, stable)
	require.True(t, serviceEventsContain(rolledBack, "was unable to place a task"))
	require.True(t, serviceEventsContain(rolledBack, "began rolling back"))
	require.True(t, serviceEventsContain(rolledBack, "deployment rollback completed"))
	require.GreaterOrEqual(t, len(rolledBack.Deployments), 2)
	require.Equal(t, ecstypes.DeploymentRolloutStateFailed, rolledBack.Deployments[1].RolloutState)
}

// TestECS_ServiceDeploymentCloudWatchAlarmRollsBack proves that the scheduler
// consumes the real CloudWatch alarm state, rather than a simulator-only flag,
// and rolls the deployment back through the same durable state machine.
func TestECS_ServiceDeploymentCloudWatchAlarmRollsBack(t *testing.T) {
	client := ecsClient()
	cloudWatch := cloudwatchClient()
	const (
		cluster     = "alarm-rollback-cluster"
		serviceName = "alarm-rollback-service"
		family      = "alarm-rollback-task"
		alarmName   = "alarm-rollback-deployment"
	)
	stable := createRollbackTestService(t, client, cluster, serviceName, family)
	next := registerECSServiceDeploymentTask(t, client, family, []string{"hold"})

	_, err := cloudWatch.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(alarmName),
		Namespace:          aws.String("Sockerless/ECSDeployment"),
		MetricName:         aws.String("DeploymentFailure"),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		Period:             aws.Int32(60),
		Statistic:          cwtypes.StatisticSum,
		Threshold:          aws.Float64(0),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cloudWatch.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{
			AlarmNames: []string{alarmName},
		})
	})
	_, err = cloudWatch.SetAlarmState(ctx, &cloudwatch.SetAlarmStateInput{
		AlarmName:   aws.String(alarmName),
		StateValue:  cwtypes.StateValueAlarm,
		StateReason: aws.String("external deployment health signal failed"),
	})
	require.NoError(t, err)

	_, err = client.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:        aws.String(cluster),
		Service:        aws.String(serviceName),
		TaskDefinition: aws.String(next),
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			Alarms: &ecstypes.DeploymentAlarms{
				AlarmNames: []string{alarmName},
				Enable:     true,
				Rollback:   true,
			},
		},
	})
	require.NoError(t, err)

	rolledBack := waitForECSServiceTaskDefinition(t, client, cluster, serviceName, stable)
	require.Truef(t, serviceEventsContain(rolledBack, "entered the ALARM state"),
		"service events: %#v", serviceEventMessages(rolledBack))
	require.Truef(t, serviceEventsContain(rolledBack, "began rolling back"),
		"service events: %#v", serviceEventMessages(rolledBack))
}
