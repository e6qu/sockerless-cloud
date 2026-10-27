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

// Amazon ECS distinguishes placements by launch type.
func ecsPlacementService(launchType string) placement.Service {
	return placement.Service{Name: "ecs", Qualifier: strings.ToUpper(launchType)}
}

// "" means "default". Amazon ECS rejects every cluster-scoped operation on an
// unknown cluster with ClusterNotFoundException.
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
	// `tasks` is required and non-empty.
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

// Closes when the requested stop finishes.
var ecsTaskStopsInFlight sync.Map // task ID -> chan struct{}

// Amazon ECS answers StopTask at once: desired STOPPED, with stopCode,
// stoppedReason and stoppingAt set, last status and containers unchanged. The
// containers then get SIGTERM and their stop timeout before SIGKILL. A
// stopped task is returned as is; a stopping one keeps the first request's
// code and reason. false means no such task.
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
	// The scheduler stops counting the task and deregisters it now, while its
	// containers stop.
	ecsRequestServiceReconcileForTask(task)
	ecsHandOffTaskLifecycle(func(ctx context.Context) { ecsFinishTaskStop(ctx, taskID) }, finished)
	return task, true
}

// Holds the lifecycle lock, so a start in progress finishes first and its
// containers are then stopped. An interrupted step is left for the next
// process's recovery.
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
		// A container killed after its stop timeout exits 137 (128+SIGKILL), as
		// Fargate reports.
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

// Amazon ECS drops stopped tasks from ListTasks about an hour after they stop.
const ecsStoppedTaskRetention = time.Hour

// A task not stopped, or with no recorded stop time, never expires.
func ecsTaskExpired(t ECSTask, now time.Time) bool {
	if t.LastStatus != ECSTaskStatusStopped || t.StoppedAt == nil {
		return false
	}
	return now.Sub(ecsEpochTime(*t.StoppedAt)) > ecsStoppedTaskRetention
}

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
