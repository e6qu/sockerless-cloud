package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func handleECSCreateCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClusterName                     string          `json:"clusterName"`
		Tags                            []ECSTag        `json:"tags"`
		CapacityProviders               []string        `json:"capacityProviders"`
		DefaultCapacityProviderStrategy json.RawMessage `json:"defaultCapacityProviderStrategy"`
		Settings                        json.RawMessage `json:"settings"`
		Configuration                   json.RawMessage `json:"configuration"`
		ServiceConnectDefaults          json.RawMessage `json:"serviceConnectDefaults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ClusterName == "" {
		req.ClusterName = "default"
	}

	cluster := ECSCluster{
		ClusterArn:                      ecsArn("cluster", req.ClusterName),
		ClusterName:                     req.ClusterName,
		Status:                          "ACTIVE",
		Tags:                            req.Tags,
		CapacityProviders:               req.CapacityProviders,
		DefaultCapacityProviderStrategy: req.DefaultCapacityProviderStrategy,
		Settings:                        req.Settings,
		Configuration:                   req.Configuration,
		ServiceConnectDefaults:          req.ServiceConnectDefaults,
	}
	ecsClusters.Put(req.ClusterName, cluster)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"cluster": cluster,
	})
}

func handleECSDescribeClusters(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Clusters []string `json:"clusters"`
		Include  []string `json:"include"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	includeSettings, includeConfig := false, false
	for _, inc := range req.Include {
		switch inc {
		case "SETTINGS":
			includeSettings = true
		case "CONFIGURATIONS":
			includeConfig = true
		}
	}

	clusters := []ECSCluster{}
	failures := []map[string]string{}

	for _, nameOrArn := range req.Clusters {
		// Extract cluster name from ARN if needed
		name := nameOrArn
		if strings.HasPrefix(nameOrArn, "arn:") {
			parts := strings.Split(nameOrArn, "/")
			if len(parts) > 1 {
				name = parts[len(parts)-1]
			}
		}

		cluster, ok := ecsClusters.Get(name)
		if ok {
			// Update running task count
			runningCount := 0
			for _, t := range ecsTasks.List() {
				if t.ClusterArn == cluster.ClusterArn && t.LastStatus == ECSTaskStatusRunning {
					runningCount++
				}
			}
			cluster.RunningTasksCount = runningCount
			activeServices := 0
			for _, s := range ecsServices.List() {
				if s.ClusterArn == cluster.ClusterArn && s.Status == "ACTIVE" {
					activeServices++
				}
			}
			cluster.ActiveServicesCount = activeServices
			// settings / configuration only surface when explicitly included,
			// matching real DescribeClusters.
			if !includeSettings {
				cluster.Settings = nil
			}
			if !includeConfig {
				cluster.Configuration = nil
			}
			clusters = append(clusters, cluster)
		} else {
			failures = append(failures, map[string]string{
				"arn":    ecsArn("cluster", name),
				"reason": "MISSING",
			})
		}
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"clusters": clusters,
		"failures": failures,
	})
}

// handleECSUpdateCluster updates a cluster's settings / configuration /
// serviceConnectDefaults in place. Without it, any change to
// aws_ecs_cluster.{setting,configuration,service_connect_defaults} forced
// recreation.
func handleECSUpdateCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster                string          `json:"cluster"`
		Settings               json.RawMessage `json:"settings"`
		Configuration          json.RawMessage `json:"configuration"`
		ServiceConnectDefaults json.RawMessage `json:"serviceConnectDefaults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name := ecsClusterNameFromRef(req.Cluster)
	cluster, ok := ecsClusters.Get(name)
	if !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest, "Cluster not found: %s", req.Cluster)
		return
	}
	if req.Settings != nil {
		cluster.Settings = req.Settings
	}
	if req.Configuration != nil {
		cluster.Configuration = req.Configuration
	}
	if req.ServiceConnectDefaults != nil {
		cluster.ServiceConnectDefaults = req.ServiceConnectDefaults
	}
	ecsClusters.Put(name, cluster)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"cluster": cluster})
}

// handleECSUpdateClusterSettings updates only the cluster settings
// (containerInsights). The provider uses it for aws_ecs_cluster.setting changes.
func handleECSUpdateClusterSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster  string          `json:"cluster"`
		Settings json.RawMessage `json:"settings"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name := ecsClusterNameFromRef(req.Cluster)
	cluster, ok := ecsClusters.Get(name)
	if !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest, "Cluster not found: %s", req.Cluster)
		return
	}
	if req.Settings != nil {
		cluster.Settings = req.Settings
	}
	ecsClusters.Put(name, cluster)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"cluster": cluster})
}

func handleECSDeleteCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string `json:"cluster"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Cluster == "" {
		AWSError(w, "InvalidParameterException", "cluster is required", http.StatusBadRequest)
		return
	}

	name := req.Cluster
	if strings.HasPrefix(name, "arn:") {
		parts := strings.Split(name, "/")
		if len(parts) > 1 {
			name = parts[len(parts)-1]
		}
	}

	cluster, ok := ecsClusters.Get(name)
	if !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest,
			"Cluster not found: %s", req.Cluster)
		return
	}

	for _, instance := range ecsContainerInstances.List() {
		if instance.Status != "INACTIVE" && strings.Contains(instance.ContainerInstanceArn, ":container-instance/"+name+"/") {
			AWSError(w, "ClusterContainsContainerInstancesException",
				"The Cluster cannot be deleted while Container Instances are active or draining.", http.StatusBadRequest)
			return
		}
	}
	for _, service := range ecsServices.List() {
		if service.ClusterArn == cluster.ClusterArn && service.Status != "INACTIVE" {
			AWSError(w, "ClusterContainsServicesException",
				"The Cluster cannot be deleted while Services are active.", http.StatusBadRequest)
			return
		}
	}
	for _, task := range ecsTasks.List() {
		if task.ClusterArn == cluster.ClusterArn && task.LastStatus != ECSTaskStatusStopped {
			AWSError(w, "ClusterContainsTasksException",
				"The Cluster cannot be deleted while Tasks are active.", http.StatusBadRequest)
			return
		}
	}

	cluster.Status = "INACTIVE"
	ecsClusters.Delete(name)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"cluster": cluster,
	})
}
