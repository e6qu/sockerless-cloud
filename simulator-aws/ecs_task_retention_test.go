package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// Amazon ECS drops a stopped task from ListTasks about an hour after it stops.

func ecsStoppedAt(t time.Time) *float64 {
	seconds := float64(t.UnixMilli()) / 1000
	return &seconds
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
		// A running task never ages out.
		name:    "a running task never ages out",
		task:    ECSTask{LastStatus: ECSTaskStatusRunning, StoppedAt: ecsStoppedAt(now.Add(-72 * time.Hour))},
		expired: false,
	}, {
		// No timestamp is no evidence of age.
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

// Retention bounds what the simulator holds, not just what it reports.
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
	// Under their ID, as RunTask stores them.
	for _, task := range []ECSTask{aged, recent, running} {
		ecsCommitPlacement(task.TaskID(), task)
	}
	t.Cleanup(func() {
		for _, task := range []ECSTask{aged, recent, running} {
			ecsTasks.Delete(task.TaskID())
		}
	})

	for _, task := range []ECSTask{aged, recent} {
		holder := roleHolder("ecs", task.TaskID())
		roleHeldSessions.Put(holder, roleHeldSession{Holder: holder, AccessKeyID: "ASIA" + task.TaskID()})
	}
	t.Cleanup(func() {
		roleHeldSessions.Delete(roleHolder("ecs", aged.TaskID()))
		roleHeldSessions.Delete(roleHolder("ecs", recent.TaskID()))
	})

	if swept := ecsSweepStoppedTasks(now); swept != 1 {
		t.Errorf("sweep removed %d task(s), want 1", swept)
	}
	sweepRoleCredentials(now)
	if _, found := ecsTasks.Get(aged.TaskID()); found {
		t.Error("the aged-out task is still held")
	}
	if _, found := ecsTasks.Get(recent.TaskID()); !found {
		t.Error("a task stopped five minutes ago was removed")
	}
	if _, found := ecsTasks.Get(running.TaskID()); !found {
		t.Error("a running task was removed")
	}
	if _, held := roleHeldSessions.Get(roleHolder("ecs", aged.TaskID())); held {
		t.Error("the swept task's credential is still held")
	}
	if _, held := roleHeldSessions.Get(roleHolder("ecs", recent.TaskID())); !held {
		t.Error("a retained task's credential was dropped")
	}
}

// An aged-out task is absent from ListTasks.
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
	ecsCommitPlacement(aged.TaskID(), aged)
	ecsCommitPlacement(recent.TaskID(), recent)
	t.Cleanup(func() {
		ecsTasks.Delete(aged.TaskID())
		ecsTasks.Delete(recent.TaskID())
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
