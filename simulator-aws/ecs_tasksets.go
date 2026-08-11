package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// ECS task sets — the blue/green deployment primitive within an EXTERNAL
// (or CODE_DEPLOY) service. A service can carry multiple task sets, each a
// distinct task-definition rollout with its own scale; one is the PRIMARY.
// Backed by aws_ecs_task_set. The sim models each task set as a control-plane
// object keyed by its service + id; it reaches a STEADY_STATE stability
// immediately so create/describe/update/delete round-trip.

// ECSTaskSet is the stored shape of a task set.
type ECSTaskSet struct {
	Id                       string          `json:"id"`
	TaskSetArn               string          `json:"taskSetArn"`
	ServiceArn               string          `json:"serviceArn"`
	ClusterArn               string          `json:"clusterArn"`
	StartedBy                string          `json:"startedBy,omitempty"`
	ExternalId               string          `json:"externalId,omitempty"`
	Status                   string          `json:"status"`
	TaskDefinition           string          `json:"taskDefinition"`
	ComputedDesiredCount     int             `json:"computedDesiredCount"`
	PendingCount             int             `json:"pendingCount"`
	RunningCount             int             `json:"runningCount"`
	CreatedAt                float64         `json:"createdAt"`
	UpdatedAt                float64         `json:"updatedAt"`
	LaunchType               string          `json:"launchType,omitempty"`
	CapacityProviderStrategy json.RawMessage `json:"capacityProviderStrategy,omitempty"`
	PlatformVersion          string          `json:"platformVersion,omitempty"`
	NetworkConfiguration     json.RawMessage `json:"networkConfiguration,omitempty"`
	LoadBalancers            json.RawMessage `json:"loadBalancers,omitempty"`
	ServiceRegistries        json.RawMessage `json:"serviceRegistries,omitempty"`
	Scale                    json.RawMessage `json:"scale,omitempty"`
	StabilityStatus          string          `json:"stabilityStatus"`
	StabilityStatusAt        float64         `json:"stabilityStatusAt"`
	Tags                     []ECSTag        `json:"tags,omitempty"`
}

var ecsTaskSets sim.Store[ECSTaskSet]

func registerECSTaskSets(r *sim.AWSRouter, srv *sim.Server) {
	ecsTaskSets = sim.MakeStore[ECSTaskSet](srv.DB(), "ecs_task_sets")

	r.Register("AmazonEC2ContainerServiceV20141113.CreateTaskSet", handleECSCreateTaskSet)
	r.Register("AmazonEC2ContainerServiceV20141113.DeleteTaskSet", handleECSDeleteTaskSet)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeTaskSets", handleECSDescribeTaskSets)
	r.Register("AmazonEC2ContainerServiceV20141113.UpdateTaskSet", handleECSUpdateTaskSet)
	r.Register("AmazonEC2ContainerServiceV20141113.UpdateServicePrimaryTaskSet", handleECSUpdateServicePrimaryTaskSet)
}

func ecsTaskSetKey(serviceArn, id string) string { return serviceArn + "/" + id }

// ecsResolveService resolves a cluster + service name-or-ARN to its stored
// service, returning the cluster name used as the store-key prefix.
func ecsResolveService(cluster, service string) (clusterName string, svc ECSService, ok bool) {
	clusterName = ecsClusterNameFromRef(cluster)
	if strings.HasPrefix(service, "arn:") {
		cn, _, s, found := ecsServiceFromARN(service)
		if found {
			return cn, s, true
		}
		return clusterName, ECSService{}, false
	}
	svc, ok = ecsServices.Get(ecsServiceKey(clusterName, service))
	return clusterName, svc, ok
}

func handleECSCreateTaskSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Service                  string          `json:"service"`
		Cluster                  string          `json:"cluster"`
		ExternalId               string          `json:"externalId"`
		TaskDefinition           string          `json:"taskDefinition"`
		NetworkConfiguration     json.RawMessage `json:"networkConfiguration"`
		LoadBalancers            json.RawMessage `json:"loadBalancers"`
		ServiceRegistries        json.RawMessage `json:"serviceRegistries"`
		LaunchType               string          `json:"launchType"`
		CapacityProviderStrategy json.RawMessage `json:"capacityProviderStrategy"`
		PlatformVersion          string          `json:"platformVersion"`
		Scale                    json.RawMessage `json:"scale"`
		Tags                     []ECSTag        `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Service == "" {
		sim.AWSError(w, "InvalidParameterException", "service is required", http.StatusBadRequest)
		return
	}
	if req.TaskDefinition == "" {
		sim.AWSError(w, "InvalidParameterException", "taskDefinition is required", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	_, svc, ok := ecsResolveService(req.Cluster, req.Service)
	if !ok {
		sim.AWSErrorf(w, "ServiceNotFoundException", http.StatusBadRequest,
			"The service %s does not exist.", req.Service)
		return
	}

	id := "ecs-svc/" + generateNumericID()
	now := float64(time.Now().Unix())
	ts := ECSTaskSet{
		Id:                       id,
		TaskSetArn:               svc.ServiceArn + "/" + id,
		ServiceArn:               svc.ServiceArn,
		ClusterArn:               svc.ClusterArn,
		ExternalId:               req.ExternalId,
		Status:                   "ACTIVE",
		TaskDefinition:           req.TaskDefinition,
		ComputedDesiredCount:     0,
		CreatedAt:                now,
		UpdatedAt:                now,
		LaunchType:               req.LaunchType,
		CapacityProviderStrategy: req.CapacityProviderStrategy,
		PlatformVersion:          req.PlatformVersion,
		NetworkConfiguration:     req.NetworkConfiguration,
		LoadBalancers:            req.LoadBalancers,
		ServiceRegistries:        req.ServiceRegistries,
		Scale:                    req.Scale,
		StabilityStatus:          "STEADY_STATE",
		StabilityStatusAt:        now,
		Tags:                     req.Tags,
	}
	ecsTaskSets.Put(ecsTaskSetKey(svc.ServiceArn, id), ts)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"taskSet": ts})
}

func handleECSDescribeTaskSets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster  string   `json:"cluster"`
		Service  string   `json:"service"`
		TaskSets []string `json:"taskSets"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	_, svc, ok := ecsResolveService(req.Cluster, req.Service)
	if !ok {
		sim.AWSErrorf(w, "ServiceNotFoundException", http.StatusBadRequest,
			"The service %s does not exist.", req.Service)
		return
	}

	want := map[string]bool{}
	for _, ref := range req.TaskSets {
		want[ecsTaskSetID(ref)] = true
	}
	var sets []ECSTaskSet
	for _, ts := range ecsTaskSets.List() {
		if ts.ServiceArn != svc.ServiceArn {
			continue
		}
		if len(want) > 0 && !want[ts.Id] {
			continue
		}
		sets = append(sets, ts)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"taskSets": sets,
		"failures": []any{},
	})
}

func handleECSUpdateTaskSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string          `json:"cluster"`
		Service string          `json:"service"`
		TaskSet string          `json:"taskSet"`
		Scale   json.RawMessage `json:"scale"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	ts, key, ok := ecsLookupTaskSet(req.Cluster, req.Service, req.TaskSet)
	if !ok {
		sim.AWSErrorf(w, "TaskSetNotFoundException", http.StatusBadRequest,
			"The specified task set does not exist.")
		return
	}
	if len(req.Scale) > 0 {
		ts.Scale = req.Scale
	}
	ts.UpdatedAt = float64(time.Now().Unix())
	ecsTaskSets.Put(key, ts)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"taskSet": ts})
}

func handleECSUpdateServicePrimaryTaskSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster        string `json:"cluster"`
		Service        string `json:"service"`
		PrimaryTaskSet string `json:"primaryTaskSet"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	ts, key, ok := ecsLookupTaskSet(req.Cluster, req.Service, req.PrimaryTaskSet)
	if !ok {
		sim.AWSErrorf(w, "TaskSetNotFoundException", http.StatusBadRequest,
			"The specified task set does not exist.")
		return
	}
	// Mark this set PRIMARY; demote any sibling that was PRIMARY to ACTIVE.
	for _, sib := range ecsTaskSets.List() {
		if sib.ServiceArn == ts.ServiceArn && sib.Id != ts.Id && sib.Status == "PRIMARY" {
			sib.Status = "ACTIVE"
			ecsTaskSets.Put(ecsTaskSetKey(sib.ServiceArn, sib.Id), sib)
		}
	}
	ts.Status = "PRIMARY"
	ts.UpdatedAt = float64(time.Now().Unix())
	ecsTaskSets.Put(key, ts)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"taskSet": ts})
}

func handleECSDeleteTaskSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string `json:"cluster"`
		Service string `json:"service"`
		TaskSet string `json:"taskSet"`
		Force   bool   `json:"force"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	ts, key, ok := ecsLookupTaskSet(req.Cluster, req.Service, req.TaskSet)
	if !ok {
		sim.AWSErrorf(w, "TaskSetNotFoundException", http.StatusBadRequest,
			"The specified task set does not exist.")
		return
	}
	ts.Status = "DRAINING"
	ecsTaskSets.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"taskSet": ts})
}

// ecsLookupTaskSet resolves a task set by cluster + service + id/ARN, returning
// the stored set and its store key.
func ecsLookupTaskSet(cluster, service, taskSet string) (ECSTaskSet, string, bool) {
	_, svc, ok := ecsResolveService(cluster, service)
	if !ok {
		return ECSTaskSet{}, "", false
	}
	id := ecsTaskSetID(taskSet)
	key := ecsTaskSetKey(svc.ServiceArn, id)
	ts, ok := ecsTaskSets.Get(key)
	return ts, key, ok
}

// ecsTaskSetID extracts the task-set id from an id or a taskSetArn
// (...:task-set/<cluster>/<service>/ecs-svc/<n>). The id form is
// "ecs-svc/<numeric>", so we keep the last two path segments.
func ecsTaskSetID(ref string) string {
	if !strings.HasPrefix(ref, "arn:") {
		return ref
	}
	parts := strings.Split(ref, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return ref
}

// generateNumericID returns a numeric-ish id for ECS task-set / deployment ids,
// matching the real "ecs-svc/<19-digit>" shape closely enough for round-trips.
func generateNumericID() string {
	return fmt.Sprintf("%019d", time.Now().UnixNano())
}
