package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Amazon ECS answers DescribeServices from its recorded service state (a
// service created with desiredCount 10 answers runningCount 0), and the
// scheduler converges it. A read must never take the scheduler lock or
// reconcile, and CreateService / UpdateService record, hand off and return.

// The scheduler reads the service, task, deployment, revision, alarm and
// target group stores, so all must be real.
func ecsSchedulerTestStores() {
	// Let earlier reconciliations finish before replacing their stores.
	bg.Await()
	// No server owns the lifecycle steps; an earlier test's server would hand them
	// a cancelled context.
	ecsBackgroundServer = nil
	ecsClusters = sim.MakeStore[ECSCluster](nil, "ecs_clusters")
	ecsTaskDefinitions = sim.MakeStore[ECSTaskDefinition](nil, "ecs_task_definitions")
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")
	ecsServices = sim.MakeStore[ECSService](nil, "ecs_services")
	ecsGatewayRoutes = sim.MakeStore[ECSGatewayRoute](nil, "ecs_gateway_routes")
	ecsServiceGatewayConnectors = sim.MakeStore[[]ecsGatewayConnector](nil, "ecs_service_gateway_connectors")
	ecsServiceSchedulerStates = sim.MakeStore[ECSServiceSchedulerState](nil, "ecs_service_scheduler_states")
	ecsServiceDeployments = sim.MakeStore[ECSServiceDeploymentRec](nil, "ecs_service_deployments")
	ecsServiceRevisions = sim.MakeStore[ECSServiceRevisionRec](nil, "ecs_service_revisions")
	ecsTaskProtections = sim.MakeStore[ECSTaskProtection](nil, "ecs_task_protections")
	elbv2TargetGroups = sim.MakeStore[ELBv2TargetGroup](nil, "elbv2_target_groups")
	// Whether a listener forwards to a target group decides whether its targets
	// are health-checked, so the scheduler reads these stores too.
	elbv2Listeners = sim.MakeStore[ELBv2Listener](nil, "elbv2_listeners")
	elbv2Rules = sim.MakeStore[ELBv2Rule](nil, "elbv2_rules")
	cwAlarms = sim.MakeStore[CWAlarm](nil, "cw_alarms")
	ecsRevisions = map[string]int{}
}

func ecsSchedulerTestCluster(clusterName, family string) (ECSCluster, string) {
	cluster := ECSCluster{
		ClusterName: clusterName,
		ClusterArn:  ecsArn("cluster", clusterName),
		Status:      "ACTIVE",
	}
	ecsClusters.Put(clusterName, cluster)
	definition := ECSTaskDefinition{
		TaskDefinitionArn:    ecsArn("task-definition", family+":1"),
		Family:               family,
		Revision:             1,
		Status:               "ACTIVE",
		ContainerDefinitions: []ECSContainerDefinition{{Name: "app"}},
	}
	ecsTaskDefinitions.Put(family+":1", definition)
	ecsRevisionMu.Lock()
	ecsRevisions[family] = 1
	ecsRevisionMu.Unlock()
	return cluster, definition.TaskDefinitionArn
}

// Started long enough ago to be past the steady-state window.
func ecsSchedulerTestRunningTask(cluster ECSCluster, serviceName, startedBy, taskDefinitionArn, containerIP string) ECSTask {
	startedAt := float64(time.Now().Add(-time.Minute).UnixMilli()) / 1000
	createdAt := float64(startedAt)
	taskID := sim.NewUUID()
	task := ECSTask{
		TaskArn:           ecsArn("task", cluster.ClusterName+"/"+taskID),
		TaskDefinitionArn: taskDefinitionArn,
		ClusterArn:        cluster.ClusterArn,
		LastStatus:        ECSTaskStatusRunning,
		DesiredStatus:     ECSTaskStatusRunning,
		Group:             ecsServiceTaskGroup(serviceName),
		StartedBy:         startedBy,
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

func ecsJSONRequest(t *testing.T, body any) *http.Request {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
	request.Header.Set("Content-Type", "application/x-amz-json-1.1")
	return request
}

type ecsHandlerResult struct {
	code    int
	body    string
	elapsed time.Duration
}

// A handler reconciling inline blocks on the lock for the whole scheduler
// pass.
func ecsCallWhileSchedulerHoldsLock(
	t *testing.T,
	key string,
	handler http.HandlerFunc,
	body any,
	budget time.Duration,
	whileHeld func(ecsHandlerResult),
) ecsHandlerResult {
	t.Helper()
	lock := ecsServiceLock(key)
	lock.Lock()
	release := sync.OnceFunc(lock.Unlock)
	defer release()

	answered := make(chan ecsHandlerResult, 1)
	go func() {
		recorder := httptest.NewRecorder()
		started := time.Now()
		handler(recorder, ecsJSONRequest(t, body))
		answered <- ecsHandlerResult{
			code:    recorder.Code,
			body:    recorder.Body.String(),
			elapsed: time.Since(started),
		}
	}()

	select {
	case result := <-answered:
		require.Equalf(t, http.StatusOK, result.code, "handler failed: %s", result.body)
		if whileHeld != nil {
			whileHeld(result)
		}
		return result
	case <-time.After(budget):
		release()
		blocked := <-answered
		t.Fatalf("handler did not answer within %s while the service scheduler held the "+
			"lock for %s — it answered only after the lock was released, in %s: "+
			"the request path is running a scheduler pass",
			budget, key, blocked.elapsed)
		return ecsHandlerResult{}
	}
}

// Never replies, so a health check runs its full timeout, like a target that
// is registered but not yet answering.
func hangingHealthCheckTarget(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	held := make(chan net.Conn, 16)
	done := make(chan struct{})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				close(done)
				return
			}
			held <- conn
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		close(held)
		for conn := range held {
			_ = conn.Close()
		}
	})
	return listener.Addr().(*net.TCPAddr).Port
}

func decodeECSServices(t *testing.T, body string) []ECSService {
	t.Helper()
	var response struct {
		Services []ECSService `json:"services"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	return response.Services
}

// Reconciling in the handler would queue the describe behind the running
// pass; Amazon ECS answers regardless of its scheduler.
func TestDescribeServicesAnswersWhileTheSchedulerHoldsTheServiceLock(t *testing.T) {
	ecsSchedulerTestStores()
	cluster, taskDefinitionArn := ecsSchedulerTestCluster("lock-cluster", "lock-task")
	const serviceName = "lock-svc"
	key := ecsServiceKey(cluster.ClusterName, serviceName)

	service := ECSService{
		ServiceArn:     ecsArn("service", cluster.ClusterName+"/"+serviceName),
		ServiceName:    serviceName,
		ClusterArn:     cluster.ClusterArn,
		TaskDefinition: taskDefinitionArn,
		DesiredCount:   1,
		RunningCount:   1,
		Status:         "ACTIVE",
	}
	service.Deployments = []ECSDeployment{ecsServiceDeployment(service, float64(time.Now().Unix()))}
	ecsServices.Put(key, service)
	ecsSchedulerTestRunningTask(cluster, serviceName, ecsServicePrimaryStartedBy(service), taskDefinitionArn, "10.0.0.7")

	result := ecsCallWhileSchedulerHoldsLock(t, key, handleECSDescribeServices, map[string]any{
		"cluster":  cluster.ClusterName,
		"services": []string{serviceName},
	}, time.Second, nil)
	t.Logf("DescribeServices answered in %s while the scheduler lock was held", result.elapsed)

	described := decodeECSServices(t, result.body)
	require.Len(t, described, 1)
	require.Equal(t, serviceName, described[0].ServiceName)
}

// The read reports recorded state instead of recomputing it.
func TestDescribeServicesReportsTheRecordedServiceCounts(t *testing.T) {
	ecsSchedulerTestStores()
	cluster, taskDefinitionArn := ecsSchedulerTestCluster("record-cluster", "record-task")
	const serviceName = "record-svc"
	key := ecsServiceKey(cluster.ClusterName, serviceName)

	now := float64(time.Now().Add(-time.Hour).Unix())
	service := ECSService{
		ServiceArn:     ecsArn("service", cluster.ClusterName+"/"+serviceName),
		ServiceName:    serviceName,
		ClusterArn:     cluster.ClusterArn,
		TaskDefinition: taskDefinitionArn,
		DesiredCount:   3,
		RunningCount:   3,
		PendingCount:   0,
		Status:         "ACTIVE",
	}
	service.Deployments = []ECSDeployment{ecsServiceDeployment(service, now)}
	ecsServices.Put(key, service)

	recorder := httptest.NewRecorder()
	handleECSDescribeServices(recorder, ecsJSONRequest(t, map[string]any{
		"cluster":  cluster.ClusterName,
		"services": []string{serviceName},
	}))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	described := decodeECSServices(t, recorder.Body.String())
	require.Len(t, described, 1)
	require.EqualValues(t, 3, described[0].RunningCount,
		"the read recomputed runningCount from tasks instead of reporting the service record")
	require.Len(t, described[0].Deployments, 1)
	require.EqualValues(t, now, described[0].Deployments[0].UpdatedAt,
		"the read rewrote the deployment record")

	stored, ok := ecsServices.Get(key)
	require.True(t, ok)
	require.EqualValues(t, 3, stored.RunningCount, "the read wrote to the service store")
	require.EqualValues(t, now, stored.Deployments[0].UpdatedAt, "the read wrote to the service store")
}

// A scheduler pass surveys target health, so a read driving one would cost a
// health-check timeout per target.
func TestDescribeServicesDoesNotProbeLoadBalancerTargets(t *testing.T) {
	ecsSchedulerTestStores()
	cluster, taskDefinitionArn := ecsSchedulerTestCluster("probe-cluster", "probe-task")
	const serviceName = "probe-svc"
	key := ecsServiceKey(cluster.ClusterName, serviceName)

	port := hangingHealthCheckTarget(t)
	const healthCheckTimeout = 5
	targetGroupArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/probe-tg/0123456789abcdef"
	elbv2TargetGroups.Put(targetGroupArn, ELBv2TargetGroup{
		Arn:                 targetGroupArn,
		Name:                "probe-tg",
		Protocol:            "HTTP",
		Port:                port,
		TargetType:          "ip",
		HealthCheckProtocol: "HTTP",
		HealthCheckPath:     "/",
		HealthCheckEnabled:  true,
		HealthCheckTimeout:  healthCheckTimeout,
	})

	loadBalancers, err := json.Marshal([]map[string]any{{
		"targetGroupArn": targetGroupArn,
		"containerName":  "app",
		"containerPort":  port,
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
	service.Deployments = []ECSDeployment{ecsServiceDeployment(service, float64(time.Now().Unix()))}
	ecsServices.Put(key, service)
	ecsSchedulerTestRunningTask(cluster, serviceName, ecsServicePrimaryStartedBy(service), taskDefinitionArn, "127.0.0.1")

	recorder := httptest.NewRecorder()
	started := time.Now()
	handleECSDescribeServices(recorder, ecsJSONRequest(t, map[string]any{
		"cluster":  cluster.ClusterName,
		"services": []string{serviceName},
	}))
	elapsed := time.Since(started)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	t.Logf("DescribeServices answered in %s against an unanswering health-check target "+
		"(health check timeout %ds)", elapsed, healthCheckTimeout)

	require.Lessf(t, elapsed, time.Second,
		"DescribeServices took %s: it probed the service's Elastic Load Balancing targets, "+
			"which costs one health-check timeout per target per pass", elapsed)
}

// The scheduler fills in the counts after the response.
func TestCreateServiceReturnsBeforeTheSchedulerConverges(t *testing.T) {
	ecsSchedulerTestStores()
	cluster, taskDefinitionArn := ecsSchedulerTestCluster("create-cluster", "create-task")
	const serviceName = "create-svc"
	key := ecsServiceKey(cluster.ClusterName, serviceName)

	// The deployment's task is already running, as if an earlier process placed
	// it; the scheduler must record it after the handler answers.
	result := ecsCallWhileSchedulerHoldsLock(t, key, handleECSCreateService, map[string]any{
		"cluster":        cluster.ClusterName,
		"serviceName":    serviceName,
		"taskDefinition": taskDefinitionArn,
		"desiredCount":   1,
	}, time.Second, func(result ecsHandlerResult) {
		var created struct {
			Service ECSService `json:"service"`
		}
		require.NoError(t, json.Unmarshal([]byte(result.body), &created))
		ecsSchedulerTestRunningTask(cluster, serviceName, ecsServicePrimaryStartedBy(created.Service), taskDefinitionArn, "10.0.0.9")
	})
	t.Logf("CreateService answered in %s while the scheduler lock was held", result.elapsed)

	var created struct {
		Service ECSService `json:"service"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.body), &created))
	require.EqualValues(t, 1, created.Service.DesiredCount)
	require.EqualValues(t, 0, created.Service.RunningCount,
		"CreateService reported a converged service: it ran the scheduler inline")
	require.Len(t, created.Service.Deployments, 1)
	require.Equal(t, "PRIMARY", created.Service.Deployments[0].Status)

	requireSchedulerRecordsRunningTask(t, key)
}

// Amazon ECS records the new desired state and answers; the scheduler acts
// afterwards.
func TestUpdateServiceReturnsBeforeTheSchedulerConverges(t *testing.T) {
	ecsSchedulerTestStores()
	cluster, taskDefinitionArn := ecsSchedulerTestCluster("update-cluster", "update-task")
	const serviceName = "update-svc"
	key := ecsServiceKey(cluster.ClusterName, serviceName)

	service := ECSService{
		ServiceArn:     ecsArn("service", cluster.ClusterName+"/"+serviceName),
		ServiceName:    serviceName,
		ClusterArn:     cluster.ClusterArn,
		TaskDefinition: taskDefinitionArn,
		DesiredCount:   1,
		RunningCount:   0,
		Status:         "ACTIVE",
	}
	service.Deployments = []ECSDeployment{ecsServiceDeployment(service, float64(time.Now().Unix()))}
	ecsServices.Put(key, service)
	ecsSchedulerTestRunningTask(cluster, serviceName, ecsServicePrimaryStartedBy(service), taskDefinitionArn, "10.0.0.11")

	result := ecsCallWhileSchedulerHoldsLock(t, key, handleECSUpdateService, map[string]any{
		"cluster":      cluster.ClusterName,
		"service":      serviceName,
		"desiredCount": 1,
	}, time.Second, nil)
	t.Logf("UpdateService answered in %s while the scheduler lock was held", result.elapsed)

	var updated struct {
		Service ECSService `json:"service"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.body), &updated))
	require.EqualValues(t, 1, updated.Service.DesiredCount)
	require.EqualValues(t, 0, updated.Service.RunningCount,
		"UpdateService reported a converged service: it ran the scheduler inline")

	requireSchedulerRecordsRunningTask(t, key)
}

// Proves the write scheduled convergence rather than dropping it.
func requireSchedulerRecordsRunningTask(t *testing.T, key string) {
	t.Helper()
	require.Eventuallyf(t, func() bool {
		service, ok := ecsServices.Get(key)
		return ok && service.RunningCount == 1 &&
			len(service.Deployments) == 1 &&
			service.Deployments[0].RolloutState == "COMPLETED"
	}, 10*time.Second, 20*time.Millisecond,
		"the service scheduler never converged %s after the write returned", key)

	// The pass holds the lock throughout, so acquiring it waits for the pass to
	// finish.
	lock := ecsServiceLock(key)
	lock.Lock()
	defer lock.Unlock()
}
