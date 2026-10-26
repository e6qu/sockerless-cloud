package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
	"github.com/stretchr/testify/require"
)

// A completed rollout stays completed: a task failing a health check
// afterwards is replaced, "by starting replacement tasks first and then
// stopping the unhealthy tasks".

const ecsHealthTestTargetGroupArn = "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/svc-health-tg/0123456789abcdef"

// A target group is health-checked only when a listener forwards to it, so
// register both.
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

// Drives the checker to the given verdicts; the checker's own tests cover
// probing real targets.
func ecsHealthTestRecordTargetHealth(t *testing.T, containerPort int, verdicts map[string]string) {
	t.Helper()
	tg, ok := elbv2TargetGroups.Get(ecsHealthTestTargetGroupArn)
	require.True(t, ok)
	policy := elbv2HealthPolicy(tg)
	var targets []lbplane.HealthTarget[string]
	for address, state := range verdicts {
		var result error
		if state == elbv2TargetStateUnhealthy {
			result = errors.New("connection refused")
		}
		targets = append(targets, lbplane.HealthTarget[string]{
			Key:    elbv2TargetHealthKey(tg.Arn, ELBv2TargetDescription{ID: address, Port: containerPort}),
			Policy: policy,
			Probe:  func(context.Context) (int, error) { return 0, result },
		})
	}
	now := time.Now()
	for range max(policy.UnhealthyThreshold, policy.InitialHealthyThreshold) {
		elbv2TargetHealthTracker.Sweep(context.Background(), now, targets)
		now = now.Add(policy.Interval)
	}
	for address, state := range verdicts {
		require.Equal(t, state, elbv2TargetHealthFor(tg, ELBv2TargetDescription{ID: address, Port: containerPort}).State)
	}
}

// The rollout has completed and the tasks are registered.
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

// A chosen age controls which task the scheduler considers oldest.
func ecsHealthTestTaskAged(
	cluster ECSCluster,
	serviceName, taskDefinitionArn, containerIP string,
	age time.Duration,
) ECSTask {
	service, ok := ecsServices.Get(ecsServiceKey(cluster.ClusterName, serviceName))
	if !ok {
		panic("the health test task's service is not stored: " + serviceName)
	}
	startedAt := float64(time.Now().Add(-age).UnixMilli()) / 1000
	createdAt := float64(startedAt)
	taskID := sim.NewUUID()
	task := ECSTask{
		TaskArn:           ecsArn("task", cluster.ClusterName+"/"+taskID),
		TaskDefinitionArn: taskDefinitionArn,
		ClusterArn:        cluster.ClusterArn,
		LastStatus:        ECSTaskStatusRunning,
		DesiredStatus:     ECSTaskStatusRunning,
		Group:             ecsServiceTaskGroup(serviceName),
		StartedBy:         ecsServicePrimaryStartedBy(service),
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

// For each task first stored, how many of the service's other tasks were not
// STOPPED, leaving out the named ones.
type ecsLaunchRecorder struct {
	sim.Store[ECSTask]
	group  string
	ignore map[string]bool

	mu       sync.Mutex
	inFlight []int
}

func (r *ecsLaunchRecorder) Put(id string, task ECSTask) {
	if _, exists := r.Get(id); !exists && task.Group == r.group {
		others := 0
		for _, other := range r.Filter(func(other ECSTask) bool {
			return other.Group == r.group && other.LastStatus != ECSTaskStatusStopped
		}) {
			if !r.ignore[other.TaskArn] {
				others++
			}
		}
		r.mu.Lock()
		r.inFlight = append(r.inFlight, others)
		r.mu.Unlock()
	}
	r.Store.Put(id, task)
}

// Reopening a finished rollout as IN_PROGRESS is a state the service never
// reports. The replacement's start fails here, and a stopped replacement is
// itself replaced, so each launch records the replacements in flight: one per
// failed task means none.
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
	ecsHealthTestRecordTargetHealth(t, containerPort, map[string]string{taskAddress: elbv2TargetStateUnhealthy})
	launches := &ecsLaunchRecorder{
		Store:  ecsTasks,
		group:  ecsServiceTaskGroup(serviceName),
		ignore: map[string]bool{failing.TaskArn: true},
	}
	ecsTasks = launches

	ecsReconcileService(key)
	bg.Await()

	service, ok := ecsServices.Get(key)
	require.True(t, ok)
	require.Len(t, service.Deployments, 1)
	require.Equal(t, "COMPLETED", service.Deployments[0].RolloutState,
		"a failed health check reopened a finished rollout instead of replacing the task")

	launches.mu.Lock()
	inFlight := slices.Clone(launches.inFlight)
	launches.mu.Unlock()
	require.NotEmpty(t, inFlight,
		"the scheduler did not start a replacement for the task whose health check failed")
	for launch, replacements := range inFlight {
		require.Zerof(t, replacements,
			"launch %d started while %d replacements for the one failed task were still in flight", launch, replacements)
	}
}

// The replacement starts first; the unhealthy task, not the newest, stops once
// the replacement is in service.
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

	// The replacement is newer, so trimming by age would stop the wrong task.
	failing := ecsHealthTestTaskAged(cluster, serviceName, taskDefinitionArn, failingAddress, 10*time.Minute)
	replacement := ecsHealthTestTaskAged(cluster, serviceName, taskDefinitionArn, healthyAddress, time.Minute)
	ecsHealthTestRecordTargetHealth(t, containerPort, map[string]string{
		failingAddress: elbv2TargetStateUnhealthy,
		healthyAddress: elbv2TargetStateHealthy,
	})

	ecsReconcileService(key)

	// Like StopTask, the stop does not wait for containers.
	stopping, ok := ecsTasks.Get(failing.TaskID())
	require.True(t, ok)
	require.Equal(t, ECSTaskStatusStopped, stopping.DesiredStatus,
		"the task whose health check failed was not asked to stop")
	require.Equal(t, ecsUnhealthyTaskReplacedReason, stopping.StoppedReason)

	// The reconciliation the stop requests settles the deployment.
	awaitECSTaskStop(t, failing.TaskID())
	bg.Await()

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
