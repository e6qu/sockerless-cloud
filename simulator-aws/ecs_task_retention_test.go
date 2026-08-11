package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// Amazon ECS stops returning a stopped task from ListTasks about an hour after
// it stops, and keeps it behind DescribeTasks only briefly after that. A
// simulator that retains them forever diverges in a way that compounds: the
// reported cluster held 6,129 stopped tasks, the oldest a week old, so
// ListTasks paged through thousands of ARNs to answer an ordinary question and
// the cluster read at a glance like a crash loop (GitHub issue #908).

func ecsStoppedAt(t time.Time) *int64 {
	unix := t.Unix()
	return &unix
}

func TestStoppedTaskExpiresAfterTheRetentionWindow(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		task    ECSTask
		expired bool
	}{{
		name:    "a task stopped inside the window is still visible",
		task:    ECSTask{LastStatus: ECSTaskStatusStopped, StoppedAt: ecsStoppedAt(now.Add(-30 * time.Minute))},
		expired: false,
	}, {
		name:    "a task stopped beyond the window has aged out",
		task:    ECSTask{LastStatus: ECSTaskStatusStopped, StoppedAt: ecsStoppedAt(now.Add(-2 * time.Hour))},
		expired: true,
	}, {
		// The window is a property of being stopped. A running task is not old
		// enough to remove however long it has been running.
		name:    "a running task never ages out",
		task:    ECSTask{LastStatus: ECSTaskStatusRunning, StoppedAt: ecsStoppedAt(now.Add(-72 * time.Hour))},
		expired: false,
	}, {
		// Absence of a timestamp is not evidence of age; removing such a task
		// would delete something whose stop time is merely unrecorded.
		name:    "a stopped task with no stop time is kept",
		task:    ECSTask{LastStatus: ECSTaskStatusStopped},
		expired: false,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ecsTaskExpired(tc.task, now); got != tc.expired {
				t.Errorf("expired = %v, want %v", got, tc.expired)
			}
		})
	}
}

// The retention is a bound on what the simulator holds, not only on what it
// reports: a filter alone would leave the records accumulating behind it.
func TestSweepRemovesOnlyTheTasksThatAgedOut(t *testing.T) {
	buildConformanceSimulator(t)
	now := time.Now()

	aged := ECSTask{
		TaskArn:    ecsArn("task", "retention/aged"),
		ClusterArn: ecsArn("cluster", "retention"),
		LastStatus: ECSTaskStatusStopped,
		StoppedAt:  ecsStoppedAt(now.Add(-3 * time.Hour)),
	}
	recent := ECSTask{
		TaskArn:    ecsArn("task", "retention/recent"),
		ClusterArn: ecsArn("cluster", "retention"),
		LastStatus: ECSTaskStatusStopped,
		StoppedAt:  ecsStoppedAt(now.Add(-5 * time.Minute)),
	}
	running := ECSTask{
		TaskArn:    ecsArn("task", "retention/running"),
		ClusterArn: ecsArn("cluster", "retention"),
		LastStatus: ECSTaskStatusRunning,
	}
	for _, task := range []ECSTask{aged, recent, running} {
		ecsTasks.Put(task.TaskArn, task)
	}
	t.Cleanup(func() {
		for _, task := range []ECSTask{aged, recent, running} {
			ecsTasks.Delete(task.TaskArn)
		}
	})

	if swept := ecsSweepStoppedTasks(now); swept != 1 {
		t.Errorf("sweep removed %d task(s), want 1", swept)
	}
	if _, found := ecsTasks.Get(aged.TaskArn); found {
		t.Error("the aged-out task is still held")
	}
	if _, found := ecsTasks.Get(recent.TaskArn); !found {
		t.Error("a task stopped five minutes ago was removed")
	}
	if _, found := ecsTasks.Get(running.TaskArn); !found {
		t.Error("a running task was removed")
	}
}

// The window is what a caller sees: an aged-out task must be absent from
// ListTasks, which is the surface the accumulation actually hurt.
func TestListTasksOmitsTasksThatAgedOut(t *testing.T) {
	srv, _, _ := buildConformanceSimulator(t)
	now := time.Now()

	const cluster = "retention-list"
	clusterArn := ecsArn("cluster", cluster)
	ecsClusters.Put(cluster, ECSCluster{ClusterName: cluster, ClusterArn: clusterArn, Status: "ACTIVE"})

	aged := ECSTask{
		TaskArn: ecsArn("task", cluster+"/aged"), ClusterArn: clusterArn,
		LastStatus: ECSTaskStatusStopped, DesiredStatus: ECSTaskStatusStopped,
		StoppedAt: ecsStoppedAt(now.Add(-3 * time.Hour)),
	}
	recent := ECSTask{
		TaskArn: ecsArn("task", cluster+"/recent"), ClusterArn: clusterArn,
		LastStatus: ECSTaskStatusStopped, DesiredStatus: ECSTaskStatusStopped,
		StoppedAt: ecsStoppedAt(now.Add(-2 * time.Minute)),
	}
	ecsTasks.Put(aged.TaskArn, aged)
	ecsTasks.Put(recent.TaskArn, recent)
	t.Cleanup(func() {
		ecsTasks.Delete(aged.TaskArn)
		ecsTasks.Delete(recent.TaskArn)
		ecsClusters.Delete(cluster)
	})

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, jsonTargetReq("AmazonEC2ContainerServiceV20141113.ListTasks",
		`{"cluster":"`+cluster+`","desiredStatus":"STOPPED"}`))
	if rr.Code != 200 {
		t.Fatalf("ListTasks: status %d, body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		TaskArns []string `json:"taskArns"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode ListTasks: %v", err)
	}
	listed := map[string]bool{}
	for _, arn := range out.TaskArns {
		listed[arn] = true
	}
	if listed[aged.TaskArn] {
		t.Error("a task stopped three hours ago is still listed")
	}
	if !listed[recent.TaskArn] {
		t.Errorf("a task stopped two minutes ago is missing from %v", out.TaskArns)
	}
}
