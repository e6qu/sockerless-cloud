package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// DeleteTaskDefinitions permanently removes (up to 10) task-definition
// revisions. Real ECS requires each revision to be INACTIVE (deregistered)
// first; an ACTIVE revision is returned as a failure with reason
// "Revision is in ACTIVE status." A deleted revision transitions to
// DELETE_IN_PROGRESS and is no longer retrievable.
func handleECSDeleteTaskDefinitions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskDefinitions []string `json:"taskDefinitions"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.TaskDefinitions) == 0 {
		AWSError(w, "InvalidParameterException", "taskDefinitions cannot be empty", http.StatusBadRequest)
		return
	}
	if len(req.TaskDefinitions) > 10 {
		AWSError(w, "InvalidParameterException",
			"taskDefinitions cannot have more than 10 elements", http.StatusBadRequest)
		return
	}

	var deleted []ECSTaskDefinition
	var failures []map[string]string
	for _, ref := range req.TaskDefinitions {
		key := ref
		if strings.HasPrefix(key, "arn:") {
			parts := strings.Split(key, "/")
			key = parts[len(parts)-1]
		}
		td, ok := ecsTaskDefinitions.Get(key)
		if !ok {
			failures = append(failures, map[string]string{
				"arn":    ecsArn("task-definition", key),
				"reason": "MISSING",
			})
			continue
		}
		if td.Status == "ACTIVE" {
			failures = append(failures, map[string]string{
				"arn":    td.TaskDefinitionArn,
				"reason": "Revision is in ACTIVE status.",
			})
			continue
		}
		td.Status = "DELETE_IN_PROGRESS"
		ecsTaskDefinitions.Delete(key)
		deleted = append(deleted, td)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"taskDefinitions": deleted,
		"failures":        failures,
	})
}
