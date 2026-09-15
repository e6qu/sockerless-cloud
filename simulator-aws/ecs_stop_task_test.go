package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// awaitECSTaskStop waits for a stop StopTask has already accepted to finish.
// A task with no stop in flight has finished stopping, or was never asked to.
func awaitECSTaskStop(t *testing.T, taskID string) {
	t.Helper()
	inFlight, ok := ecsTaskStopsInFlight.Load(taskID)
	if !ok {
		return
	}
	done, ok := inFlight.(chan struct{})
	if !ok {
		t.Fatalf("stop in flight for task %s is %T, not a channel", taskID, inFlight)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("the stop of task %s did not finish", taskID)
	}
}

// Amazon ECS answers StopTask at once. The task it returns is still running,
// with its desired status STOPPED and its stop code, reason and stoppingAt set;
// its containers are given their stop timeout afterwards. The simulator did all
// of that before answering, under the task's lifecycle lock, so a workspace
// stop on the Scaleway stack took 30.5 seconds.
//
// Holding the lifecycle lock stands in for whatever keeps a task's lifecycle
// busy — a start still pulling its image, or containers taking their stop
// timeout. StopTask must answer through it, and finish the stop once it is
// free.
func TestStopTaskAnswersWhileTheTaskIsStillStopping(t *testing.T) {
	buildConformanceSimulator(t)
	ecsClusters.Put("stop-cluster", ECSCluster{
		ClusterName: "stop-cluster",
		ClusterArn:  ecsArn("cluster", "stop-cluster"),
		Status:      "ACTIVE",
	})
	instanceKey := ecsContainerInstanceKey("stop-cluster", "instance-1")
	instanceArn := ecsArn("container-instance", "stop-cluster/instance-1")
	ecsContainerInstances.Put(instanceKey, ECSContainerInstance{
		ContainerInstanceArn: instanceArn,
		ClusterName:          "stop-cluster",
		RunningTasksCount:    1,
	})
	taskID := generateUUID()
	taskArn := ecsArn("task", "stop-cluster/"+taskID)
	ecsTasks.Put(taskID, ECSTask{
		TaskArn:              taskArn,
		ClusterArn:           ecsArn("cluster", "stop-cluster"),
		LastStatus:           ECSTaskStatusRunning,
		DesiredStatus:        ECSTaskStatusRunning,
		ContainerInstanceArn: instanceArn,
		Containers:           []ECSTaskContainer{{Name: "app", LastStatus: "RUNNING"}},
	})

	lifecycle := ecsTaskLifecycleLock(taskID)
	lifecycle.Lock()
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		handleECSStopTask(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
			`{"cluster":"stop-cluster","task":"`+taskArn+`","reason":"done for the day"}`,
		)))
		answered <- rec
	}()
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-answered:
	case <-time.After(10 * time.Second):
		lifecycle.Unlock()
		t.Fatal("StopTask waited for the task's lifecycle instead of answering")
	}
	if rec.Code != http.StatusOK {
		lifecycle.Unlock()
		t.Fatalf("StopTask status = %d, body %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Task struct {
			LastStatus    string   `json:"lastStatus"`
			DesiredStatus string   `json:"desiredStatus"`
			StopCode      string   `json:"stopCode"`
			StoppedReason string   `json:"stoppedReason"`
			StoppingAt    *float64 `json:"stoppingAt"`
			StoppedAt     *float64 `json:"stoppedAt"`
		} `json:"task"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		lifecycle.Unlock()
		t.Fatalf("decode StopTask response: %v", err)
	}
	got := response.Task
	if got.LastStatus != "RUNNING" || got.DesiredStatus != "STOPPED" ||
		got.StopCode != "UserInitiated" || got.StoppedReason != "done for the day" ||
		got.StoppingAt == nil || got.StoppedAt != nil {
		lifecycle.Unlock()
		t.Fatalf("StopTask answered with %s, want a running task asked to stop by the user", rec.Body.String())
	}

	// A second request while the task is stopping does not restate why it stopped.
	again, ok := stopECSTask(taskID, "service deleted", "ServiceSchedulerInitiated")
	if !ok || again.StopCode != "UserInitiated" || again.StoppedReason != "done for the day" {
		lifecycle.Unlock()
		t.Fatalf("a repeated stop changed the task to %+v", again)
	}

	inFlight, ok := ecsTaskStopsInFlight.Load(taskID)
	lifecycle.Unlock()
	if !ok {
		t.Fatal("StopTask left no stop in flight for a running task")
	}
	select {
	case <-inFlight.(chan struct{}):
	case <-time.After(10 * time.Second):
		t.Fatal("the stop did not finish once the task's lifecycle was free")
	}

	stopped, ok := ecsTasks.Get(taskID)
	if !ok {
		t.Fatal("task missing after its stop finished")
	}
	if stopped.LastStatus != ECSTaskStatusStopped || stopped.StoppedAt == nil || stopped.StoppingAt == nil ||
		stopped.StopCode != "UserInitiated" || stopped.StoppedReason != "done for the day" {
		t.Fatalf("stopped task = %+v", stopped)
	}
	if len(stopped.Containers) != 1 || stopped.Containers[0].LastStatus != "STOPPED" ||
		stopped.Containers[0].ExitCode == nil || *stopped.Containers[0].ExitCode != 137 {
		t.Fatalf("stopped task containers = %+v, want one STOPPED with exit code 137", stopped.Containers)
	}
	instance, _ := ecsContainerInstances.Get(instanceKey)
	if instance.RunningTasksCount != 0 {
		t.Fatalf("container instance running task count = %d, want 0", instance.RunningTasksCount)
	}

	// A stopped task is returned as it is.
	after, ok := stopECSTask(taskID, "later", "ServiceSchedulerInitiated")
	if !ok || after.StopCode != "UserInitiated" || *after.StoppedAt != *stopped.StoppedAt {
		t.Fatalf("stopping a stopped task changed it to %+v", after)
	}
}

// A task the scheduler has asked to stop still reads RUNNING until its
// containers have had their stop timeout. Counted among the service's tasks, it
// made the scheduler stop the service's remaining task for the same surplus.
func TestServiceSchedulerDoesNotStopAnotherTaskForAStopInProgress(t *testing.T) {
	buildConformanceSimulator(t)
	clusterArn := ecsArn("cluster", "surplus-cluster")
	ecsClusters.Put("surplus-cluster", ECSCluster{ClusterName: "surplus-cluster", ClusterArn: clusterArn, Status: "ACTIVE"})
	definition := ECSTaskDefinition{
		TaskDefinitionArn:    ecsArn("task-definition", "surplus:1"),
		Family:               "surplus",
		Revision:             1,
		ContainerDefinitions: []ECSContainerDefinition{{Name: "app"}},
		Status:               "ACTIVE",
	}
	ecsTaskDefinitions.Put("surplus:1", definition)
	service := ECSService{
		ServiceArn:     ecsArn("service", "surplus-cluster/surplus-svc"),
		ServiceName:    "surplus-svc",
		ClusterArn:     clusterArn,
		TaskDefinition: definition.TaskDefinitionArn,
		DesiredCount:   1,
		Status:         "ACTIVE",
	}
	key := ecsServiceKey("surplus-cluster", service.ServiceName)
	ecsServices.Put(key, service)

	group := ecsServiceTaskGroup(service.ServiceName)
	longAgo := float64(time.Now().Add(-time.Hour).Unix())
	task := func(createdAt float64, desired ECSTaskStatus) ECSTask {
		task := makeECSTestTask(clusterArn, group, "ecs-svc/surplus-svc", ECSTaskStatusRunning)
		task.TaskDefinitionArn = definition.TaskDefinitionArn
		task.DesiredStatus = desired
		task.CreatedAt = &createdAt
		task.StartedAt = &longAgo
		ecsTasks.Put(task.TaskID(), task)
		return task
	}
	stopping := task(longAgo, ECSTaskStatusStopped)
	// The newer task is the one a surplus stop picks first.
	remaining := task(longAgo+60, ECSTaskStatusRunning)
	if unhealthy := ecsUnhealthyServiceTasks(service, []ECSTask{remaining}); len(unhealthy) != 0 {
		t.Fatalf("the remaining task is unhealthy, so the test would not show a surplus: %+v", unhealthy)
	}

	ecsReconcileService(key)

	got, ok := ecsTasks.Get(remaining.TaskID())
	if !ok {
		t.Fatal("remaining task missing after reconciliation")
	}
	if got.DesiredStatus != ECSTaskStatusRunning {
		t.Fatalf("the scheduler stopped the service's only remaining task (%s: %s) while %s was already stopping",
			got.StopCode, got.StoppedReason, stopping.TaskID())
	}
	AwaitSimulatorBackground()
}

// StopTask answers before the containers have stopped, so a restart inside a
// container's stop timeout leaves a task asked to stop that has not. Recovery
// resumed a pending one as if nobody had asked, and stopped a running one as
// though its workload had vanished.
func TestRecoverECSTasksFinishesAStopThatWasInProgress(t *testing.T) {
	AwaitSimulatorBackground()
	ecsTaskDefinitions = sim.MakeStore[ECSTaskDefinition](nil, "ecs_task_definitions")
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")
	ecsContainerInstances = sim.MakeStore[ECSContainerInstance](nil, "ecs_container_instances")
	ec2Volumes = sim.MakeStore[EC2Volume](nil, "ec2_volumes")

	definition := ECSTaskDefinition{
		TaskDefinitionArn:    ecsArn("task-definition", "desktop:1"),
		Family:               "desktop",
		Revision:             1,
		ContainerDefinitions: []ECSContainerDefinition{{Name: "desktop"}},
		Status:               "ACTIVE",
	}
	ecsTaskDefinitions.Put("desktop:1", definition)
	instanceKey := ecsContainerInstanceKey("desktop-cluster", "instance-1")
	instanceArn := ecsArn("container-instance", "desktop-cluster/instance-1")
	ecsContainerInstances.Put(instanceKey, ECSContainerInstance{
		ContainerInstanceArn: instanceArn,
		ClusterName:          "desktop-cluster",
		RunningTasksCount:    1,
		PendingTasksCount:    1,
	})
	stoppingAt := ecsEpochSeconds()
	lastStatuses := map[string]ECSTaskStatus{
		"stopping-running": ECSTaskStatusRunning,
		"stopping-pending": ECSTaskStatusPending,
	}
	for taskID, lastStatus := range lastStatuses {
		ecsTasks.Put(taskID, ECSTask{
			TaskArn:              ecsArn("task", "desktop-cluster/"+taskID),
			TaskDefinitionArn:    definition.TaskDefinitionArn,
			LastStatus:           lastStatus,
			DesiredStatus:        ECSTaskStatusStopped,
			StoppingAt:           &stoppingAt,
			StopCode:             "UserInitiated",
			StoppedReason:        "edd: workspace lifecycle stop",
			ContainerInstanceArn: instanceArn,
			Containers:           []ECSTaskContainer{{Name: "desktop", LastStatus: string(lastStatus)}},
		})
	}

	err := recoverECSTasksWithContainerFinder(func(map[string]string) ([]sim.ExistingContainer, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("recover ECS tasks: %v", err)
	}
	for taskID := range lastStatuses {
		awaitECSTaskStop(t, taskID)
		task, ok := ecsTasks.Get(taskID)
		if !ok {
			t.Fatalf("task %s missing after recovery", taskID)
		}
		if task.LastStatus != ECSTaskStatusStopped || task.StoppedAt == nil {
			t.Errorf("task %s = %s (stoppedAt %v), want STOPPED", taskID, task.LastStatus, task.StoppedAt)
		}
		if task.StopCode != "UserInitiated" || task.StoppedReason != "edd: workspace lifecycle stop" {
			t.Errorf("task %s stopped with %s: %q, want the stop request's code and reason", taskID, task.StopCode, task.StoppedReason)
		}
	}
	instance, _ := ecsContainerInstances.Get(instanceKey)
	if instance.RunningTasksCount != 0 || instance.PendingTasksCount != 0 {
		t.Errorf("container instance counts = running %d pending %d, want 0 and 0",
			instance.RunningTasksCount, instance.PendingTasksCount)
	}
}
