package main

import (
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A deployed simulator's stop waits for every background worker, so an Amazon
// ECS lifecycle step that waits on its task — here a stop queued behind the
// task's lifecycle lock, which a start still pulling its image holds — must
// return once the server stops.
func TestECSLifecycleStepReturnsWhenTheServerStops(t *testing.T) {
	srv, _, _ := buildConformanceSimulator(t)
	ecsClusters.Put("shutdown-cluster", ECSCluster{
		ClusterName: "shutdown-cluster",
		ClusterArn:  ecsArn("cluster", "shutdown-cluster"),
		Status:      "ACTIVE",
	})
	taskID := sim.NewUUID()
	ecsTasks.Put(taskID, ECSTask{
		TaskArn:       ecsArn("task", "shutdown-cluster/"+taskID),
		ClusterArn:    ecsArn("cluster", "shutdown-cluster"),
		LastStatus:    ECSTaskStatusRunning,
		DesiredStatus: ECSTaskStatusRunning,
		Containers:    []ECSTaskContainer{{Name: "app", LastStatus: "RUNNING"}},
	})

	lifecycle := ecsTaskLifecycleLock(taskID)
	lifecycle.Lock()
	t.Cleanup(lifecycle.Unlock)
	if _, ok := stopECSTask(taskID, "", "UserInitiated"); !ok {
		t.Fatal("StopTask did not accept the stop of a running task")
	}
	inFlight, ok := ecsTaskStopsInFlight.Load(taskID)
	if !ok {
		t.Fatal("StopTask left no stop in flight for a running task")
	}

	stopped := make(chan time.Duration, 1)
	go func() {
		started := time.Now()
		srv.StopBackground()
		stopped <- time.Since(started)
	}()
	select {
	case took := <-stopped:
		t.Logf("background workers returned in %s", took)
	case <-time.After(10 * time.Second):
		t.Fatal("the server's stop waited for an Amazon ECS lifecycle step that holds no work it can finish")
	}
	select {
	case <-inFlight.(chan struct{}):
	default:
		t.Fatal("the interrupted stop still counts as in flight")
	}

	task, ok := ecsTasks.Get(taskID)
	if !ok {
		t.Fatal("task missing after the server stopped")
	}
	if task.LastStatus != ECSTaskStatusRunning || task.DesiredStatus != ECSTaskStatusStopped {
		t.Fatalf("task = %s/%s, want RUNNING with desired status STOPPED for the next process to finish",
			task.LastStatus, task.DesiredStatus)
	}
}
