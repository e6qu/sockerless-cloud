package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// StartTask runs a task on the specific container instances the caller names,
// rather than letting the scheduler place it (RunTask). It is the EC2-launch-type
// placement primitive: the caller has its own ECS agents (RegisterContainerInstance)
// and assigns the task directly. The sim creates a task associated with the named
// instances. It launches each assigned task through the same real container,
// networking, logging, and lifecycle path as RunTask.

func registerECSStartTask(r *AWSRouter, srv *sim.Server) {
	r.Register("AmazonEC2ContainerServiceV20141113.StartTask", handleECSStartTask)
}

func handleECSStartTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster              string                `json:"cluster"`
		ContainerInstances   []string              `json:"containerInstances"`
		TaskDefinition       string                `json:"taskDefinition"`
		Group                string                `json:"group"`
		StartedBy            string                `json:"startedBy"`
		ReferenceId          string                `json:"referenceId"`
		EnableExecuteCommand bool                  `json:"enableExecuteCommand"`
		EnableECSManagedTags bool                  `json:"enableECSManagedTags"`
		PropagateTags        string                `json:"propagateTags"`
		Tags                 []ECSTag              `json:"tags"`
		Overrides            *ECSTaskOverride      `json:"overrides"`
		NetworkConfiguration *ECSTaskNetworkConfig `json:"networkConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.TaskDefinition == "" {
		AWSError(w, "InvalidParameterException", "taskDefinition is required", http.StatusBadRequest)
		return
	}
	if len(req.ContainerInstances) == 0 {
		AWSError(w, "InvalidParameterException", "containerInstances cannot be empty", http.StatusBadRequest)
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	_, ok := ecsClusters.Get(clusterName)
	if !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest, "Cluster not found: %s", req.Cluster)
		return
	}

	// Resolve task definition (latest revision when a bare family is given).
	tdKey := req.TaskDefinition
	if strings.HasPrefix(tdKey, "arn:") {
		parts := strings.Split(tdKey, "/")
		tdKey = parts[len(parts)-1]
	}
	if !strings.Contains(tdKey, ":") {
		ecsRevisionMu.Lock()
		rev, exists := ecsRevisions[tdKey]
		ecsRevisionMu.Unlock()
		if exists {
			tdKey = fmt.Sprintf("%s:%d", tdKey, rev)
		}
	}
	_, ok = ecsTaskDefinitions.Get(tdKey)
	if !ok {
		AWSErrorf(w, "ClientException", http.StatusBadRequest,
			"Unable to describe task definition: %s", req.TaskDefinition)
		return
	}

	var tasks []ECSTask
	var failures []map[string]string
	for _, instanceRef := range req.ContainerInstances {
		instID := ecsContainerInstanceID(instanceRef)
		instanceKey := ecsContainerInstanceKey(clusterName, instID)
		ci, ciOK := ecsContainerInstances.Get(instanceKey)
		if !ciOK {
			failures = append(failures, map[string]string{
				"arn":    ecsArn("container-instance", clusterName+"/"+instID),
				"reason": "MISSING",
			})
			continue
		}
		launched, requestError := runECSTasks(r.Context(), ecsRunTaskInput{
			Cluster:              req.Cluster,
			TaskDefinition:       req.TaskDefinition,
			Count:                1,
			Group:                req.Group,
			LaunchType:           "EC2",
			Tags:                 ecsStartTaskTags(req.Tags, req.EnableECSManagedTags, req.Cluster),
			PropagateTags:        req.PropagateTags,
			EnableExecuteCommand: req.EnableExecuteCommand,
			Overrides:            req.Overrides,
			NetworkConfiguration: req.NetworkConfiguration,
			StartedBy:            req.StartedBy,
			ContainerInstanceArn: ci.ContainerInstanceArn,
			ContainerInstanceKey: instanceKey,
		})
		if requestError != nil {
			AWSError(w, requestError.code, requestError.message, requestError.status)
			return
		}
		tasks = append(tasks, launched...)
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"tasks":    ecsTasksWire(tasks),
		"failures": failures,
	})
}

// ecsStartTaskTags applies the managed tags Amazon ECS adds when a caller asks
// for them. The service scheduler already did this for the tasks it launches;
// StartTask parsed the same flag and dropped it, so the identical request
// produced tagged tasks through a service and untagged ones directly. A task
// started outside a service has no service name, so only the cluster tag
// applies.
func ecsStartTaskTags(tags []ECSTag, managed bool, cluster string) []ECSTag {
	if !managed {
		return tags
	}
	return append(tags, ECSTag{
		Key:   "aws:ecs:clusterName",
		Value: ecsClusterNameFromRef(cluster),
	})
}
