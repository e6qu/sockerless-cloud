package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A rollout that reached steady state is finished: "When a service deployment
// is started, it begins in an IN_PROGRESS state. When the service reaches a
// steady state, the deployment transitions to a COMPLETED state." A task
// failing a health check afterwards is not a rollout going backwards — it is a
// task the scheduler replaces: "The service scheduler also replaces tasks
// determined to be unhealthy after a container health check or a load balancer
// target group health check fails", "by starting replacement tasks first and
// then stopping the unhealthy tasks".

const ecsHealthTestTargetGroupArn = "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/svc-health-tg/0123456789abcdef"

// ecsHealthTestTargetGroup registers the target group an Amazon ECS service
// binds to, holding one target per task address on the container port, and the
// listener that forwards to it. An Amazon ECS service's target group is one a
// load balancer listener forwards to; that is what has its targets
// health-checked in the first place, and therefore what gives the scheduler a
// load balancer target group health verdict to react to.
func ecsHealthTestTargetGroup(t *testing.T, containerPort int, addresses ...string) {
	t.Helper()
	targets := make([]ELBv2TargetDescription, 0, len(addresses))
	for _, address := range addresses {
		targets = append(targets, ELBv2TargetDescription{ID: address, Port: containerPort})
	}
	elbv2TargetGroups.Put(ecsHealthTestTargetGroupArn, ELBv2TargetGroup{
		Arn:                     ecsHealthTestTargetGroupArn,
		Name:                    "svc-health-tg",
		Protocol:                "HTTP",
		Port:                    containerPort,
		TargetType:              "ip",
		HealthCheckProtocol:     "HTTP",
		HealthCheckPath:         "/",
		HealthCheckEnabled:      true,
		HealthCheckInterval:     5,
		HealthCheckTimeout:      2,
		HealthyThresholdCount:   5,
		UnhealthyThresholdCount: 2,
		Targets:                 targets,
	})
	const loadBalancerArn = "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/svc-health-lb/0123456789abcdef"
	const listenerArn = loadBalancerArn + "/fedcba9876543210"
	elbv2Listeners.Put(listenerArn, ELBv2Listener{
		Arn:             listenerArn,
		LoadBalancerArn: loadBalancerArn,
		Protocol:        "HTTP",
		Port:            80,
		DefaultActions: []ELBv2Action{{
			Type:           "forward",
			TargetGroupArn: ecsHealthTestTargetGroupArn,
		}},
	})
}

// ecsHealthTestRecordTargetHealth is the verdict the Elastic Load Balancing
// health checker reached for a target. The checker's own tests cover how it
// gets there; these tests are about what the Amazon ECS service scheduler does
// once it has.
func ecsHealthTestRecordTargetHealth(address string, containerPort int, state string) {
	key := elbv2TargetHealthKey(ecsHealthTestTargetGroupArn,
		ELBv2TargetDescription{ID: address, Port: containerPort})
	elbv2TargetHealthMu.Lock()
	defer elbv2TargetHealthMu.Unlock()
	record := &ELBv2TargetHealth{State: state}
	if state == elbv2TargetStateUnhealthy {
		record.Reason = elbv2ReasonFailedHealthChecks
		record.Description = elbv2DescriptionFailedHealthChecks
	}
	elbv2TargetHealthRecords[key] = record
}

// ecsHealthTestService stores an Amazon ECS service whose rollout has already
// completed and whose tasks are registered in the target group.
func ecsHealthTestService(
	t *testing.T,
	cluster ECSCluster,
	serviceName, taskDefinitionArn string,
	containerPort int,
) string {
	t.Helper()
	loadBalancers, err := json.Marshal([]map[string]any{{
		"targetGroupArn": ecsHealthTestTargetGroupArn,
		"containerName":  "app",
		"containerPort":  containerPort,
	}})
	require.NoError(t, err)

	service := ECSService{
		ServiceArn:     ecsArn("service", cluster.ClusterName+"/"+serviceName),
		ServiceName:    serviceName,
		ClusterArn:     cluster.ClusterArn,
		TaskDefinition: taskDefinitionArn,
		DesiredCount:   1,
		RunningCount:   1,
		Status:         "ACTIVE",
		LoadBalancers:  loadBalancers,
	}
	now := float64(time.Now().Add(-time.Hour).Unix())
	deployment := ecsServiceDeployment(service, now)
	deployment.RolloutState = "COMPLETED"
	deployment.RunningCount = 1
	service.Deployments = []ECSDeployment{deployment}

	key := ecsServiceKey(cluster.ClusterName, serviceName)
	ecsServices.Put(key, service)
	return key
}

// ecsHealthTestTaskAged stores one RUNNING service task with a chosen age, so a
// test can control which task the scheduler considers oldest.
func ecsHealthTestTaskAged(
	cluster ECSCluster,
	serviceName, taskDefinitionArn, containerIP string,
	age time.Duration,
) ECSTask {
	startedAt := time.Now().Add(-age).Unix()
	createdAt := float64(startedAt)
	taskID := generateUUID()
	task := ECSTask{
		TaskArn:           ecsArn("task", cluster.ClusterName+"/"+taskID),
		TaskDefinitionArn: taskDefinitionArn,
		ClusterArn:        cluster.ClusterArn,
		LastStatus:        ECSTaskStatusRunning,
		DesiredStatus:     ECSTaskStatusRunning,
		Group:             ecsServiceTaskGroup(serviceName),
		StartedBy:         "ecs-svc/" + serviceName,
		StartedAt:         &startedAt,
		CreatedAt:         &createdAt,
		Containers: []ECSTaskContainer{{
			Name:              "app",
			LastStatus:        "RUNNING",
			NetworkInterfaces: []ECSNetworkInterface{{PrivateIpv4Address: containerIP}},
		}},
	}
	ecsTasks.Put(taskID, task)
	return task
}

// TestCompletedDeploymentSurvivesAFailedTargetHealthCheck proves the scheduler
// answers a failed health check by replacing the task rather than by reopening
// a finished rollout. Reporting IN_PROGRESS again is a state the service never
// reports, and it hides the replacement the service is supposed to perform.
func TestCompletedDeploymentSurvivesAFailedTargetHealthCheck(t *testing.T) {
	ecsSchedulerTestStores()
	cluster, taskDefinitionArn := ecsSchedulerTestCluster("health-cluster", "health-task")
	const (
		serviceName   = "health-svc"
		containerPort = 8080
		taskAddress   = "10.0.0.5"
	)
	ecsHealthTestTargetGroup(t, containerPort, taskAddress)
	key := ecsHealthTestService(t, cluster, serviceName, taskDefinitionArn, containerPort)
	failing := ecsHealthTestTaskAged(cluster, serviceName, taskDefinitionArn, taskAddress, time.Minute)
	ecsHealthTestRecordTargetHealth(taskAddress, containerPort, elbv2TargetStateUnhealthy)

	ecsReconcileService(key)

	service, ok := ecsServices.Get(key)
	require.True(t, ok)
	require.Len(t, service.Deployments, 1)
	require.Equal(t, "COMPLETED", service.Deployments[0].RolloutState,
		"a failed health check reopened a finished rollout instead of replacing the task")

	group := ecsServiceTaskGroup(serviceName)
	replacements := 0
	for _, task := range ecsServiceTasksForGroup(cluster.ClusterArn, group) {
		if task.TaskArn != failing.TaskArn {
			replacements++
		}
	}
	require.Equalf(t, 1, replacements,
		"the scheduler did not start a replacement for the task whose health check failed")
}

// TestSchedulerStopsTheUnhealthyTaskOnceItsReplacementIsInService proves the
// second half of the replacement: the scheduler starts the replacement first
// and stops the unhealthy task once the replacement is in service, and it stops
// the unhealthy task rather than whichever task is newest.
func TestSchedulerStopsTheUnhealthyTaskOnceItsReplacementIsInService(t *testing.T) {
	ecsSchedulerTestStores()
	cluster, taskDefinitionArn := ecsSchedulerTestCluster("replace-cluster", "replace-task")
	const (
		serviceName    = "replace-svc"
		containerPort  = 8080
		failingAddress = "10.0.0.5"
		healthyAddress = "10.0.0.6"
	)
	ecsHealthTestTargetGroup(t, containerPort, failingAddress, healthyAddress)
	key := ecsHealthTestService(t, cluster, serviceName, taskDefinitionArn, containerPort)

	// The replacement is the newer task, so a scheduler that trims by age
	// instead of by health stops the wrong one.
	failing := ecsHealthTestTaskAged(cluster, serviceName, taskDefinitionArn, failingAddress, 10*time.Minute)
	replacement := ecsHealthTestTaskAged(cluster, serviceName, taskDefinitionArn, healthyAddress, time.Minute)
	ecsHealthTestRecordTargetHealth(failingAddress, containerPort, elbv2TargetStateUnhealthy)
	ecsHealthTestRecordTargetHealth(healthyAddress, containerPort, elbv2TargetStateHealthy)

	ecsReconcileService(key)

	stopped, ok := ecsTasks.Get(failing.TaskID())
	require.True(t, ok)
	require.Equal(t, ECSTaskStatusStopped, stopped.LastStatus,
		"the task whose health check failed was left running")
	require.Equal(t, ecsUnhealthyTaskReplacedReason, stopped.StoppedReason)

	kept, ok := ecsTasks.Get(replacement.TaskID())
	require.True(t, ok)
	require.Equal(t, ECSTaskStatusRunning, kept.LastStatus,
		"the scheduler stopped the healthy replacement instead of the unhealthy task")

	service, ok := ecsServices.Get(key)
	require.True(t, ok)
	require.Len(t, service.Deployments, 1)
	require.Equal(t, "COMPLETED", service.Deployments[0].RolloutState)
}
