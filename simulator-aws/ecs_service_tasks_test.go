package main

import (
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// TestECSStopServiceTasks_DrainsNonStopped verifies the delete-time drain: given
// a service and a set of tasks in various statuses tagged with the service's
// group, ecsStopServiceTasks stops every non-STOPPED task (RUNNING and PENDING)
// and leaves already-STOPPED tasks untouched. Tasks that belong to a different
// group must not be drained.
func TestECSStopServiceTasks_DrainsNonStopped(t *testing.T) {
	// Isolate from any other package-level state. The drain's task-stop events
	// schedule an asynchronous service reconciliation that reads the scheduler,
	// deployment-record, and alarm stores, so they must be real stores even
	// though the drain itself never touches them.
	ecsClusters = sim.MakeStore[ECSCluster](nil, "ecs_clusters")
	ecsTaskDefinitions = sim.MakeStore[ECSTaskDefinition](nil, "ecs_task_definitions")
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")
	ecsServices = sim.MakeStore[ECSService](nil, "ecs_services")
	ecsServiceSchedulerStates = sim.MakeStore[ECSServiceSchedulerState](nil, "ecs_service_scheduler_states")
	ecsServiceDeployments = sim.MakeStore[ECSServiceDeploymentRec](nil, "ecs_service_deployments")
	cwAlarms = sim.MakeStore[CWAlarm](nil, "cw_alarms")
	ecsRevisions = map[string]int{}

	cluster := ECSCluster{
		ClusterName: "drain-cluster",
		ClusterArn:  ecsArn("cluster", "drain-cluster"),
		Status:      "ACTIVE",
	}
	ecsClusters.Put(cluster.ClusterName, cluster)

	svc := ECSService{
		ServiceArn:     ecsArn("service", "drain-cluster/drain-svc"),
		ServiceName:    "drain-svc",
		ClusterArn:     cluster.ClusterArn,
		TaskDefinition: "drain-task",
		DesiredCount:   2,
		Status:         "ACTIVE",
	}
	ecsServices.Put(ecsServiceKey("drain-cluster", svc.ServiceName), svc)

	group := ecsServiceTaskGroup("drain-svc")
	startedBy := "ecs-svc/drain-svc"

	running := makeECSTestTask(cluster.ClusterArn, group, startedBy, ECSTaskStatusRunning)
	pending := makeECSTestTask(cluster.ClusterArn, group, startedBy, ECSTaskStatusPending)
	alreadyStopped := makeECSTestTask(cluster.ClusterArn, group, startedBy, ECSTaskStatusStopped)
	// A task in a different group must be left alone.
	otherGroup := makeECSTestTask(cluster.ClusterArn, ecsServiceTaskGroup("other-svc"), "ecs-svc/other-svc", ECSTaskStatusRunning)
	for _, task := range []ECSTask{running, pending, alreadyStopped, otherGroup} {
		ecsTasks.Put(task.TaskID(), task)
	}

	ecsStopServiceTasks(svc)

	assertStatus := func(name, id string, want ECSTaskStatus) {
		got, ok := ecsTasks.Get(id)
		if !ok {
			t.Fatalf("%s task %s missing after drain", name, id)
		}
		if got.LastStatus != want {
			t.Fatalf("%s task: expected LastStatus=%s, got %s", name, want, got.LastStatus)
		}
	}
	assertStatus("running", running.TaskID(), ECSTaskStatusStopped)
	assertStatus("pending", pending.TaskID(), ECSTaskStatusStopped)
	assertStatus("already-stopped", alreadyStopped.TaskID(), ECSTaskStatusStopped)
	assertStatus("other-group", otherGroup.TaskID(), ECSTaskStatusRunning)
}

func makeECSTestTask(clusterArn, group, startedBy string, status ECSTaskStatus) ECSTask {
	taskID := generateUUID()
	return ECSTask{
		TaskArn:           ecsArn("task", clusterArn[strings.LastIndex(clusterArn, "/")+1:]+"/"+taskID),
		TaskDefinitionArn: ecsArn("task-definition", "drain-task:1"),
		ClusterArn:        clusterArn,
		LastStatus:        status,
		DesiredStatus:     ECSTaskStatusRunning,
		Group:             group,
		StartedBy:         startedBy,
	}
}
