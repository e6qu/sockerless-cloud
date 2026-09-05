package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ECS task scale-in protection — a task in a service can be marked protected so
// the service scheduler won't terminate it during scale-in, optionally with an
// expiry. GetTaskProtection reads the flag; UpdateTaskProtection sets it. The
// sim stores the protection state keyed by task id (the task itself lives in
// ecsTasks).

// ECSTaskProtection is the stored protection state for one task.
type ECSTaskProtection struct {
	TaskArn           string   `json:"taskArn"`
	ProtectionEnabled bool     `json:"protectionEnabled"`
	ExpirationDate    *float64 `json:"expirationDate,omitempty"`
}

var ecsTaskProtections sim.Store[ECSTaskProtection]

func registerECSTaskProtection(r *AWSRouter, srv *sim.Server) {
	ecsTaskProtections = sim.MakeStore[ECSTaskProtection](srv.DB(), "ecs_task_protections")

	r.Register("AmazonEC2ContainerServiceV20141113.GetTaskProtection", handleECSGetTaskProtection)
	r.Register("AmazonEC2ContainerServiceV20141113.UpdateTaskProtection", handleECSUpdateTaskProtection)
}

func ecsTaskIDFromRef(ref string) string {
	if !strings.HasPrefix(ref, "arn:") {
		return ref
	}
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1]
}

func handleECSGetTaskProtection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string   `json:"cluster"`
		Tasks   []string `json:"tasks"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	var protected []ECSTaskProtection
	var failures []map[string]string
	for _, ref := range req.Tasks {
		id := ecsTaskIDFromRef(ref)
		task, ok := ecsTasks.Get(id)
		if !ok {
			failures = append(failures, map[string]string{
				"arn":    ref,
				"reason": "TASK_NOT_VALID",
			})
			continue
		}
		p, ok := ecsTaskProtections.Get(id)
		if !ok {
			p = ECSTaskProtection{TaskArn: task.TaskArn, ProtectionEnabled: false}
		}
		protected = append(protected, p)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"protectedTasks": protected,
		"failures":       failures,
	})
}

func handleECSUpdateTaskProtection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster           string   `json:"cluster"`
		Tasks             []string `json:"tasks"`
		ProtectionEnabled bool     `json:"protectionEnabled"`
		ExpiresInMinutes  *int     `json:"expiresInMinutes"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	var protected []ECSTaskProtection
	var failures []map[string]string
	for _, ref := range req.Tasks {
		id := ecsTaskIDFromRef(ref)
		task, ok := ecsTasks.Get(id)
		if !ok {
			failures = append(failures, map[string]string{
				"arn":    ref,
				"reason": "TASK_NOT_VALID",
			})
			continue
		}
		p := ECSTaskProtection{TaskArn: task.TaskArn, ProtectionEnabled: req.ProtectionEnabled}
		if req.ProtectionEnabled {
			mins := 2880 // ECS default protection period: 48 hours.
			if req.ExpiresInMinutes != nil {
				mins = *req.ExpiresInMinutes
			}
			exp := float64(time.Now().Add(time.Duration(mins) * time.Minute).Unix())
			p.ExpirationDate = &exp
			ecsTaskProtections.Put(id, p)
		} else {
			ecsTaskProtections.Delete(id)
		}
		protected = append(protected, p)
		ecsRequestServiceReconcileForTask(task)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"protectedTasks": protected,
		"failures":       failures,
	})
}
