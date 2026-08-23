package aws_sdk_test

import (
	"fmt"
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
	var lastErr error
	require.Eventually(t, func() bool {
		output, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster: aws.String(cluster), Services: []string{serviceName},
		})
		if err != nil || len(output.Services) != 1 {
			lastErr = err
			return false
		}
		lastErr = nil
		found = output.Services[0]
		return aws.ToString(found.TaskDefinition) == taskDefinition &&
			found.RunningCount == found.DesiredCount &&
			found.PendingCount == 0 &&
			len(found.Deployments) > 0 &&
			found.Deployments[0].RolloutState == ecstypes.DeploymentRolloutStateCompleted
	}, 30*time.Second, 100*time.Millisecond,
		// A bare "Condition never satisfied" says nothing about which of the
		// five conditions held, and this deadline is most often reached on a
		// machine whose container engine never ran the workload at all — which
		// looks identical from here unless the service is printed.
		"service %s never settled on task definition %s: %s",
		serviceName, taskDefinition, describeECSServiceForFailure(&found, &lastErr))
	return found
}

// describeECSServiceForFailure renders what the service last looked like, for
// a wait that timed out. It is called by require.Eventually only when the wait
// fails, so the closure reads whatever the final poll observed.
func describeECSServiceForFailure(service *ecstypes.Service, lastErr *error) fmt.Stringer {
	return ecsServiceFailureReport{service: service, lastErr: lastErr}
}

type ecsServiceFailureReport struct {
	service *ecstypes.Service
	lastErr *error
}

func (r ecsServiceFailureReport) String() string {
	if r.lastErr != nil && *r.lastErr != nil {
		return fmt.Sprintf("DescribeServices last failed with %v", *r.lastErr)
	}
	service := r.service
	if service == nil || service.ServiceArn == nil {
		return "DescribeServices never returned the service"
	}
	rollout := "none"
	if len(service.Deployments) > 0 {
		rollout = string(service.Deployments[0].RolloutState)
		if reason := aws.ToString(service.Deployments[0].RolloutStateReason); reason != "" {
			rollout += " (" + reason + ")"
		}
	}
	return fmt.Sprintf("task definition %s, running %d of %d desired, %d pending, rollout %s; events %v",
		aws.ToString(service.TaskDefinition), service.RunningCount, service.DesiredCount,
		service.PendingCount, rollout, serviceEventMessages(*service))
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

// serviceEventCount counts the service events carrying a fragment. The
// rollback completion event is written by the same store write that publishes
// the rollout as COMPLETED, so exactly one exists: a second would mean the
// completion path had begun appending it separately again, which is what left
// a window in which a client could see the rollout completed and the event
// missing.
func serviceEventCount(service ecstypes.Service, fragment string) int {
	count := 0
	for _, event := range service.Events {
		if strings.Contains(aws.ToString(event.Message), fragment) {
			count++
		}
	}
	return count
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
	// The response that first reports the rollout COMPLETED must already carry
	// the event recording the rollback, and carry it once.
	require.Equal(t, 1, serviceEventCount(rolledBack, "deployment rollback completed"),
		"the rollback completion event must be written exactly once, by the write "+
			"that publishes the rollout as COMPLETED: %v", serviceEventMessages(rolledBack))
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
