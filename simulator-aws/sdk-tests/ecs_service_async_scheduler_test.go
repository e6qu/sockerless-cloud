package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ecsControlPlaneBudget is the ceiling for a single Amazon ECS control-plane
// call in this test. Every one of them is a control-plane record read or write;
// none of them waits for the service scheduler, so they answer in
// milliseconds. The budget is set far above that so it fails only on the
// regression it guards: a handler that runs a scheduler pass — task placement,
// Elastic Load Balancing health probes, AWS Cloud Map registration — on the
// request path, which costs seconds to tens of seconds per call.
const ecsControlPlaneBudget = 5 * time.Second

// TestECS_Service_ControlPlaneAnswersWhileTheSchedulerConverges proves the
// Amazon ECS control plane answers from recorded state while its scheduler
// converges the service underneath. CreateService reports the service as
// requested with runningCount 0 and an IN_PROGRESS PRIMARY deployment,
// DescribeServices stays answerable throughout the rollout, UpdateService
// records a new desired count and returns, and the service still reaches
// steady state through the scheduler — observed the way a client observes it,
// by polling DescribeServices.
func TestECS_Service_ControlPlaneAnswersWhileTheSchedulerConverges(t *testing.T) {
	client := ecsClient()
	const cluster = "async-scheduler-cluster"
	const serviceName = "async-scheduler-svc"

	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: aws.String(cluster), Service: aws.String(serviceName), Force: aws.Bool(true),
		})
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	registered, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("async-scheduler-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:      aws.String("app"),
			Image:     aws.String(containerCommandImage),
			Command:   []string{"hold"},
			Essential: aws.Bool(true),
		}},
	})
	require.NoError(t, err)
	taskDefinition := aws.ToString(registered.TaskDefinition.TaskDefinitionArn)

	createStarted := time.Now()
	created, err := client.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String(serviceName),
		TaskDefinition: aws.String(taskDefinition),
		DesiredCount:   aws.Int32(2),
	})
	createLatency := time.Since(createStarted)
	require.NoError(t, err)
	t.Logf("CreateService answered in %s", createLatency)
	require.Lessf(t, createLatency, ecsControlPlaneBudget,
		"CreateService took %s: it waited for the service scheduler", createLatency)

	require.NotNil(t, created.Service)
	assert.EqualValues(t, 2, created.Service.DesiredCount)
	assert.EqualValues(t, 0, created.Service.RunningCount,
		"CreateService reported placed tasks: it ran the scheduler on the request path")
	require.Len(t, created.Service.Deployments, 1)
	assert.EqualValues(t, 2, created.Service.Deployments[0].DesiredCount)
	assert.EqualValues(t, 0, created.Service.Deployments[0].RunningCount)
	assert.Equal(t, ecstypes.DeploymentRolloutStateInProgress,
		created.Service.Deployments[0].RolloutState)

	// Poll the way a client polls. Every describe is timed: while the scheduler
	// places tasks, probes their health and completes the deployment, the read
	// must keep answering promptly.
	describeSteady := func(desired int32) bool {
		t.Helper()
		started := time.Now()
		described, describeErr := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster: aws.String(cluster), Services: []string{serviceName},
		})
		elapsed := time.Since(started)
		require.NoError(t, describeErr)
		require.Lessf(t, elapsed, ecsControlPlaneBudget,
			"DescribeServices took %s: the read is running a service reconciliation", elapsed)
		if len(described.Services) != 1 {
			return false
		}
		service := described.Services[0]
		return service.DesiredCount == desired &&
			service.RunningCount == desired &&
			service.PendingCount == 0 &&
			len(service.Deployments) == 1 &&
			service.Deployments[0].RolloutState == ecstypes.DeploymentRolloutStateCompleted
	}
	require.Eventually(t, func() bool { return describeSteady(2) },
		60*time.Second, 100*time.Millisecond,
		"the service scheduler did not converge the created service")

	updateStarted := time.Now()
	updated, err := client.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(cluster), Service: aws.String(serviceName), DesiredCount: aws.Int32(1),
	})
	updateLatency := time.Since(updateStarted)
	require.NoError(t, err)
	t.Logf("UpdateService answered in %s", updateLatency)
	require.Lessf(t, updateLatency, ecsControlPlaneBudget,
		"UpdateService took %s: it waited for the service scheduler", updateLatency)
	require.NotNil(t, updated.Service)
	assert.EqualValues(t, 1, updated.Service.DesiredCount)

	require.Eventually(t, func() bool { return describeSteady(1) },
		60*time.Second, 100*time.Millisecond,
		"the service scheduler did not converge the scaled-in service")
}
