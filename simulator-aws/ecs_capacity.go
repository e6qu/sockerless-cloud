package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The AWS-managed FARGATE and FARGATE_SPOT providers are implicit, always
// ACTIVE, and cannot be created or deleted.

type ECSCapacityProvider struct {
	CapacityProviderArn      string          `json:"capacityProviderArn"`
	Name                     string          `json:"name"`
	Status                   string          `json:"status"`
	AutoScalingGroupProvider json.RawMessage `json:"autoScalingGroupProvider,omitempty"`
	ManagedInstancesProvider json.RawMessage `json:"managedInstancesProvider,omitempty"`
	UpdateStatus             string          `json:"updateStatus,omitempty"`
	Tags                     []ECSTag        `json:"tags,omitempty"`
	Type                     string          `json:"type,omitempty"`
}

var ecsCapacityProviders sim.Store[ECSCapacityProvider]

func registerECSCapacity(r *AWSRouter, srv *sim.Server) {
	ecsCapacityProviders = sim.MakeStore[ECSCapacityProvider](srv.DB(), "ecs_capacity_providers")

	r.Register("AmazonEC2ContainerServiceV20141113.CreateCapacityProvider", handleECSCreateCapacityProvider)
	r.Register("AmazonEC2ContainerServiceV20141113.DeleteCapacityProvider", handleECSDeleteCapacityProvider)
	r.Register("AmazonEC2ContainerServiceV20141113.UpdateCapacityProvider", handleECSUpdateCapacityProvider)
}

func ecsCapacityProviderType(asg, mi json.RawMessage) string {
	if len(mi) > 0 {
		return "MANAGED_INSTANCES"
	}
	if len(asg) > 0 {
		return "EC2_AUTOSCALING"
	}
	return ""
}

func handleECSCreateCapacityProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                     string          `json:"name"`
		AutoScalingGroupProvider json.RawMessage `json:"autoScalingGroupProvider"`
		ManagedInstancesProvider json.RawMessage `json:"managedInstancesProvider"`
		Tags                     []ECSTag        `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		AWSError(w, "InvalidParameterException", "name is required", http.StatusBadRequest)
		return
	}
	for _, builtin := range ecsBuiltInCapacityProviders {
		if req.Name == builtin {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
				"The capacity provider name '%s' is reserved.", req.Name)
			return
		}
	}
	if _, exists := ecsCapacityProviders.Get(req.Name); exists {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"The specified capacity provider already exists. To change the configuration of an existing capacity provider, update the capacity provider.")
		return
	}
	cp := ECSCapacityProvider{
		CapacityProviderArn:      ecsArn("capacity-provider", req.Name),
		Name:                     req.Name,
		Status:                   "ACTIVE",
		AutoScalingGroupProvider: req.AutoScalingGroupProvider,
		ManagedInstancesProvider: req.ManagedInstancesProvider,
		Tags:                     req.Tags,
		Type:                     ecsCapacityProviderType(req.AutoScalingGroupProvider, req.ManagedInstancesProvider),
	}
	ecsCapacityProviders.Put(req.Name, cp)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"capacityProvider": cp})
}

func handleECSUpdateCapacityProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                     string          `json:"name"`
		AutoScalingGroupProvider json.RawMessage `json:"autoScalingGroupProvider"`
		ManagedInstancesProvider json.RawMessage `json:"managedInstancesProvider"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name := ecsCapacityProviderName(req.Name)
	cp, ok := ecsCapacityProviders.Get(name)
	if !ok {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"The specified capacity provider does not exist. Specify a valid name or ARN and try again.")
		return
	}
	// AutoScalingGroupProviderUpdate lacks autoScalingGroupArn, which cannot
	// change and the model requires, so merge the update over the stored members.
	cp.AutoScalingGroupProvider = ecsMergeProviderJSON(cp.AutoScalingGroupProvider, req.AutoScalingGroupProvider)
	cp.ManagedInstancesProvider = ecsMergeProviderJSON(cp.ManagedInstancesProvider, req.ManagedInstancesProvider)
	cp.UpdateStatus = "UPDATE_COMPLETE"
	ecsCapacityProviders.Put(name, cp)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"capacityProvider": cp})
}

// Members the update omits, the group ARN among them, stay as they were.
func ecsMergeProviderJSON(stored, update json.RawMessage) json.RawMessage {
	if len(update) == 0 {
		return stored
	}
	var into, from map[string]any
	if err := json.Unmarshal(update, &from); err != nil {
		return stored
	}
	if len(stored) > 0 {
		if err := json.Unmarshal(stored, &into); err != nil {
			into = nil
		}
	}
	if into == nil {
		return update
	}
	for k, v := range from {
		into[k] = v
	}
	merged, err := json.Marshal(into)
	if err != nil {
		return update
	}
	return merged
}

func handleECSDeleteCapacityProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CapacityProvider string `json:"capacityProvider"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name := ecsCapacityProviderName(req.CapacityProvider)
	// The AWS-managed providers are reserved.
	for _, builtin := range ecsBuiltInCapacityProviders {
		if name == builtin {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
				"The capacity provider '%s' is reserved and cannot be deleted.", name)
			return
		}
	}
	cp, ok := ecsCapacityProviders.Get(name)
	if !ok {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"The specified capacity provider does not exist. Specify a valid name or ARN and try again.")
		return
	}
	// A provider a cluster still names cannot be deleted; the caller must first
	// disassociate it with PutClusterCapacityProviders.
	if clusters := ecsClustersUsingCapacityProvider(name); len(clusters) > 0 {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"The capacity provider '%s' is in use by cluster(s) %s and cannot be deleted. "+
				"Disassociate it with PutClusterCapacityProviders and try again.",
			name, strings.Join(clusters, ", "))
		return
	}
	cp.Status = "INACTIVE"
	cp.UpdateStatus = "DELETE_COMPLETE"
	ecsCapacityProviders.Delete(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"capacityProvider": cp})
}

func ecsClustersUsingCapacityProvider(name string) []string {
	var clusters []string
	for _, cluster := range ecsClusters.List() {
		for _, associated := range cluster.CapacityProviders {
			if ecsCapacityProviderName(associated) != name {
				continue
			}
			clusters = append(clusters, cluster.ClusterName)
			break
		}
	}
	sort.Strings(clusters)
	return clusters
}
