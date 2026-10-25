package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// ecsPlacementService names the placement of a task: Amazon ECS distinguishes
// placements by launch type.
func ecsPlacementService(launchType string) placement.Service {
	return placement.Service{Name: "ecs", Qualifier: strings.ToUpper(launchType)}
}

// ecsRequireCluster resolves a cluster ref (name or ARN; "" → "default") and,
// if it doesn't exist, writes a ClusterNotFoundException and returns false. Real
// ECS rejects every cluster-scoped operation against an unknown cluster, so a
// deleted/unknown cluster is distinguishable from an empty cluster or a missing
// task.
func ecsRequireCluster(w http.ResponseWriter, ref string) bool {
	name := ref
	if name == "" {
		name = "default"
	}
	if strings.HasPrefix(name, "arn:") {
		parts := strings.Split(name, "/")
		if len(parts) > 1 {
			name = parts[len(parts)-1]
		}
	}
	if _, ok := ecsClusters.Get(name); !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest, "Cluster not found: %s", ref)
		return false
	}
	return true
}

func handleECSDescribeTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string   `json:"cluster"`
		Tasks   []string `json:"tasks"`
		Include []string `json:"include"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// The `tasks` list is required and must be non-empty.
	if len(req.Tasks) == 0 {
		AWSError(w, "InvalidParameterException", "Tasks cannot be empty.", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}

	tasks := []ECSTask{}
	failures := []map[string]string{}

	for _, taskRef := range req.Tasks {
		// Extract task ID from ARN
		taskID := taskRef
		if strings.HasPrefix(taskRef, "arn:") {
			parts := strings.Split(taskRef, "/")
			if len(parts) > 0 {
				taskID = parts[len(parts)-1]
			}
		}

		task, ok := ecsTasks.Get(taskID)
		if ok {
			tasks = append(tasks, task)
		} else {
			failures = append(failures, map[string]string{
				"arn":    taskRef,
				"reason": "MISSING",
			})
		}
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"tasks":    ecsTasksWireInclude(tasks, ecsIncludeHasTags(req.Include)),
		"failures": failures,
	})
}

func handleECSStopTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string `json:"cluster"`
		Task    string `json:"task"`
		Reason  string `json:"reason"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	if req.Task == "" {
		AWSError(w, "InvalidParameterException", "task is required", http.StatusBadRequest)
		return
	}

	task, ok := stopECSTask(ecsTaskIDFromRef(req.Task), req.Reason, "UserInitiated")
	if !ok {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Task not found: %s", req.Task)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"task": ecsTaskWire{ECSTask: task, includeTags: true},
	})
}

// ecsTaskStopsInFlight holds, for each task whose stop has been requested and
// has not finished, a channel that closes when it has.
var ecsTaskStopsInFlight sync.Map // task ID -> chan struct{}

// stopECSTask asks a task to stop and returns the task as the request left it,
// without waiting for its containers to stop. It reports false when there is
// no such task.
//
// Amazon ECS answers StopTask at once. The task it returns has a desired status
// of STOPPED, a stopCode, a stoppedReason and a stoppingAt, and its last status
// and its containers are still what they were; the containers then get the
// stop signal and their stop timeout, 30 seconds unless the container
// definition sets another, before SIGKILL, and only then does the task reach
// STOPPED. The simulator used to do all of that before it answered, holding
// the task's lifecycle lock, so every StopTask on a container that did not exit
// on SIGTERM took the full timeout: ecs-dev-desktop's workspace stops took
// 30.5 seconds, and a service scheduler reconciliation that stopped tasks was
// held for as long.
//
// A task already stopped is returned as it is, and a task already stopping
// keeps the code and reason of the request that stopped it.
func stopECSTask(taskID, reason, code string) (ECSTask, bool) {
	done := make(chan struct{})
	if _, stopping := ecsTaskStopsInFlight.LoadOrStore(taskID, done); stopping {
		return ecsTasks.Get(taskID)
	}
	finished := func() {
		ecsTaskStopsInFlight.CompareAndDelete(taskID, done)
		close(done)
	}

	requestedAt := ecsEpochSeconds()
	stopping := false
	ecsTasks.Update(taskID, func(t *ECSTask) {
		if t.LastStatus == ECSTaskStatusStopped {
			return
		}
		stopping = true
		if t.DesiredStatus == ECSTaskStatusStopped {
			return
		}
		t.DesiredStatus = ECSTaskStatusStopped
		t.StoppingAt = &requestedAt
		t.StopCode = code
		switch {
		case reason != "":
			t.StoppedReason = reason
		case code == "UserInitiated":
			t.StoppedReason = "Task stopped by user"
		default:
			t.StoppedReason = ""
		}
	})
	task, ok := ecsTasks.Get(taskID)
	if !ok || !stopping {
		finished()
		return task, ok
	}
	// The service scheduler stops counting the task and takes it out of its
	// load balancers and service registries now, while its containers stop.
	ecsRequestServiceReconcileForTask(task)
	ecsHandOffTaskLifecycle(func(ctx context.Context) { ecsFinishTaskStop(ctx, taskID) }, finished)
	return task, true
}

// ecsFinishTaskStop stops a stopping task's containers and records it STOPPED.
// It holds the task's lifecycle lock, so a start still in progress finishes
// first and its containers are then stopped rather than left running.
//
// A shutdown that interrupts the step leaves the task stopping: the next
// process's recovery finishes the stop.
func ecsFinishTaskStop(ctx context.Context, taskID string) {
	lifecycleLock := ecsTaskLifecycleLock(taskID)
	if !lifecycleLock.LockContext(ctx) {
		return
	}
	defer lifecycleLock.Unlock()

	existing, ok := ecsTasks.Get(taskID)
	if !ok || existing.LastStatus == ECSTaskStatusStopped {
		return
	}
	if v, ok := ecsProcessHandles.LoadAndDelete(taskID); ok {
		if procs, ok := v.(*ecsTaskProcesses); ok {
			cleanupECSTaskProcesses(ctx, taskID, procs)
		}
	}
	if ctx.Err() != nil {
		fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: stop interrupted by shutdown; the next process finishes it\n", taskID)
		return
	}

	now := ecsEpochSeconds()
	transitioned := false
	var doomedVolumes []string
	ecsTasks.Update(taskID, func(t *ECSTask) {
		if t.LastStatus == ECSTaskStatusStopped {
			return
		}
		transitioned = true
		t.LastStatus = ECSTaskStatusStopped
		t.DesiredStatus = ECSTaskStatusStopped
		t.StoppedAt = &now
		// A stopped container is SIGKILLed once its stop timeout runs out; the
		// faithful exit code is 137 (128+SIGKILL), what real Fargate reports —
		// not a clean-exit 0.
		exitCode := 137
		for j := range t.Containers {
			t.Containers[j].LastStatus = "STOPPED"
			t.Containers[j].ExitCode = &exitCode
		}
		doomedVolumes = ecsReleaseTaskAttachments(t)
	})
	ecsDeleteManagedVolumes(doomedVolumes)
	if !transitioned {
		return
	}
	instanceKey := ecsContainerInstanceKeyFromARN(existing.ContainerInstanceArn)
	switch existing.LastStatus {
	case ECSTaskStatusProvisioning, ECSTaskStatusPending:
		ecsUpdateContainerInstanceTaskCounts(instanceKey, -1, 0)
	case ECSTaskStatusRunning:
		ecsUpdateContainerInstanceTaskCounts(instanceKey, 0, -1)
	}
	if task, ok := ecsTasks.Get(taskID); ok {
		ecsRequestServiceReconcileForTask(task)
	}
}

// ecsStoppedTaskRetention is how long a stopped task remains visible. Amazon
// ECS stops returning stopped tasks from ListTasks about an hour after they
// stop and keeps them behind DescribeTasks only briefly after that, so a
// simulator that retains them forever diverges in a way that compounds: a
// cluster accumulates thousands of stopped tasks, ListTasks pages through all
// of them to answer "what happened to my task", and a cluster with one running
// task and thousands stopped reads at a glance like a crash loop.
const ecsStoppedTaskRetention = time.Hour

// ecsTaskExpired reports whether a stopped task has aged out of the window
// Amazon ECS keeps it visible for. A task that is not stopped never expires,
// and neither does one whose stop time was never recorded — the absence of a
// timestamp is not evidence of age.
func ecsTaskExpired(t ECSTask, now time.Time) bool {
	if t.LastStatus != ECSTaskStatusStopped || t.StoppedAt == nil {
		return false
	}
	return now.Sub(ecsEpochTime(*t.StoppedAt)) > ecsStoppedTaskRetention
}

// ecsSweepStoppedTasks deletes the tasks that have aged out.
func ecsSweepStoppedTasks(now time.Time) int {
	return ecsTasks.Prune(func(task ECSTask) bool { return ecsTaskExpired(task, now) })
}

func handleECSListTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster       string `json:"cluster"`
		Family        string `json:"family"`
		DesiredStatus string `json:"desiredStatus"`
		LaunchType    string `json:"launchType"`
		ServiceName   string `json:"serviceName"`
		StartedBy     string `json:"startedBy"`
		NextToken     string `json:"nextToken"`
		MaxResults    int    `json:"maxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}

	clusterName := req.Cluster
	if clusterName == "" {
		clusterName = "default"
	}
	if strings.HasPrefix(clusterName, "arn:") {
		parts := strings.Split(clusterName, "/")
		if len(parts) > 1 {
			clusterName = parts[len(parts)-1]
		}
	}

	clusterArn := ecsArn("cluster", clusterName)
	// "The default status filter is RUNNING."
	desiredStatus := req.DesiredStatus
	if desiredStatus == "" {
		desiredStatus = string(ECSTaskStatusRunning)
	}

	now := time.Now()
	tasks := ecsTasks.Filter(func(t ECSTask) bool {
		if t.ClusterArn != clusterArn {
			return false
		}
		if ecsTaskExpired(t, now) {
			return false
		}
		if req.Family != "" {
			td, ok := ecsTaskDefinitions.Get(extractTDKey(t.TaskDefinitionArn))
			if !ok || td.Family != req.Family {
				return false
			}
		}
		if string(t.DesiredStatus) != desiredStatus {
			return false
		}
		if req.LaunchType != "" && t.LaunchType != req.LaunchType {
			return false
		}
		if req.StartedBy != "" && t.StartedBy != req.StartedBy {
			return false
		}
		if req.ServiceName != "" {
			group := t.Group
			if !strings.HasPrefix(group, "service:") || group[len("service:"):] != req.ServiceName {
				return false
			}
		}
		return true
	})

	page, next, pageOK := awsKeyPage(w, ecsBadToken, tasks, func(it ECSTask) string { return it.TaskArn }, req.NextToken, req.MaxResults, 100)
	if !pageOK {
		return
	}

	taskArns := make([]string, 0, len(page))
	for _, t := range page {
		taskArns = append(taskArns, t.TaskArn)
	}

	out := map[string]any{"taskArns": taskArns}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}
