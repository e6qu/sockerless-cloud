package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// ECS capacity providers — the control-plane resource behind
// aws_ecs_capacity_provider. A capacity provider names an Auto Scaling group
// (or AWS-managed Fargate) that ECS uses for task placement. The sim stores it
// as a named control-plane object so Create/Describe/List/Update/Delete
// round-trip; the two AWS-managed providers (FARGATE / FARGATE_SPOT) are
// implicit and always ACTIVE and cannot be created or deleted.

// ECSCapacityProvider is the stored shape of a custom capacity provider.
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

func registerECSCapacity(r *sim.AWSRouter, srv *sim.Server) {
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		sim.AWSError(w, "InvalidParameterException", "name is required", http.StatusBadRequest)
		return
	}
	for _, builtin := range ecsBuiltInCapacityProviders {
		if req.Name == builtin {
			sim.AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
				"The capacity provider name '%s' is reserved.", req.Name)
			return
		}
	}
	if _, exists := ecsCapacityProviders.Get(req.Name); exists {
		sim.AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name := ecsCapacityProviderName(req.Name)
	cp, ok := ecsCapacityProviders.Get(name)
	if !ok {
		sim.AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"The specified capacity provider does not exist. Specify a valid name or ARN and try again.")
		return
	}
	if len(req.AutoScalingGroupProvider) > 0 {
		cp.AutoScalingGroupProvider = req.AutoScalingGroupProvider
	}
	if len(req.ManagedInstancesProvider) > 0 {
		cp.ManagedInstancesProvider = req.ManagedInstancesProvider
	}
	cp.UpdateStatus = "UPDATE_COMPLETE"
	ecsCapacityProviders.Put(name, cp)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"capacityProvider": cp})
}

func handleECSDeleteCapacityProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CapacityProvider string `json:"capacityProvider"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name := ecsCapacityProviderName(req.CapacityProvider)
	// The AWS-managed providers are reserved. CreateCapacityProvider already
	// refused these names and the delete did not, so the two disagreed about
	// whether FARGATE exists — and this file's own comment says it cannot be
	// deleted.
	for _, builtin := range ecsBuiltInCapacityProviders {
		if name == builtin {
			sim.AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
				"The capacity provider '%s' is reserved and cannot be deleted.", name)
			return
		}
	}
	cp, ok := ecsCapacityProviders.Get(name)
	if !ok {
		sim.AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"The specified capacity provider does not exist. Specify a valid name or ARN and try again.")
		return
	}
	// A provider a cluster still names cannot be deleted; AWS asks the caller
	// to disassociate it with PutClusterCapacityProviders first. The cluster's
	// own list was never consulted, so deleting one left clusters naming a
	// provider that no longer existed.
	if clusters := ecsClustersUsingCapacityProvider(name); len(clusters) > 0 {
		sim.AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
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

// ecsClustersUsingCapacityProvider names the clusters whose capacity-provider
// list still holds this provider, which is what stops it being deleted.
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
