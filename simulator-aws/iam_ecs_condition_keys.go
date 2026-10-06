package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Amazon ECS's request-shape condition keys.
//
// These describe what a RunTask, CreateService or CreateCapacityProvider asks
// for — which capacity provider to place on, how much CPU and memory the task
// takes, which subnets it lands in, whether exec and managed tags are on. A
// policy uses them to hold callers to a shape: only Fargate Spot, only tasks
// under a size, only inside the private subnets, never with exec enabled.
//
// Every one is settled by the request. The two sizes come from the task
// definition or daemon task definition the request names, because that is
// where Amazon ECS reads them from when the request does not override them.

// iamPopulateECSConditionKeys adds the keys an Amazon ECS request settles.
func iamPopulateECSConditionKeys(r *http.Request, body []byte, ctx map[string][]string) {
	if len(body) == 0 {
		return
	}
	var request struct {
		TaskDefinition          string   `json:"taskDefinition"`
		TaskDefinitionArn       string   `json:"taskDefinitionArn"`
		DaemonTaskDefinitionArn string   `json:"daemonTaskDefinitionArn"`
		ServiceName             string   `json:"serviceName"`
		Service                 string   `json:"service"`
		Task                    string   `json:"task"`
		Namespace               string   `json:"namespace"`
		Name                    string   `json:"name"`
		Cpu                     string   `json:"cpu"`
		Memory                  string   `json:"memory"`
		CapacityProviders       []string `json:"capacityProviders"`
		CapacityProviderArns    []string `json:"capacityProviderArns"`
		DefaultCapacityProvider []struct {
			CapacityProvider string `json:"capacityProvider"`
		} `json:"defaultCapacityProviderStrategy"`
		ManagedInstancesProvider struct {
			PropagateTags string `json:"propagateTags"`
		} `json:"managedInstancesProvider"`
		EnableECSManagedTags     *bool `json:"enableECSManagedTags"`
		EnableExecuteCommand     *bool `json:"enableExecuteCommand"`
		CapacityProviderStrategy []struct {
			CapacityProvider string `json:"capacityProvider"`
		} `json:"capacityProviderStrategy"`
		NetworkConfiguration struct {
			AwsvpcConfiguration struct {
				Subnets []string `json:"subnets"`
			} `json:"awsvpcConfiguration"`
			// An Express Mode service names its subnets directly.
			Subnets []string `json:"subnets"`
		} `json:"networkConfiguration"`
		ServiceConnectConfiguration struct {
			Namespace string `json:"namespace"`
		} `json:"serviceConnectConfiguration"`
		VolumeConfigurations []struct {
			Name string `json:"name"`
		} `json:"volumeConfigurations"`
		Overrides struct {
			Cpu    string `json:"cpu"`
			Memory string `json:"memory"`
		} `json:"overrides"`
	}
	if json.Unmarshal(body, &request) != nil {
		return
	}

	// A task set, and a service's updates, name the service under `service`;
	// a create names the one it makes under `serviceName`.
	iamSetConditionValues(ctx, "ecs:service", request.ServiceName, request.Service)
	iamSetConditionValues(ctx, "ecs:task", request.Task)
	iamSetConditionValues(ctx, "ecs:daemon-task-definition", request.DaemonTaskDefinitionArn)
	iamSetConditionValues(ctx, "ecs:propagate-tags", request.ManagedInstancesProvider.PropagateTags)
	if request.EnableECSManagedTags != nil {
		ctx["ecs:enable-ecs-managed-tags"] = []string{strconv.FormatBool(*request.EnableECSManagedTags)}
	}
	if request.EnableExecuteCommand != nil {
		ctx["ecs:enable-execute-command"] = []string{strconv.FormatBool(*request.EnableExecuteCommand)}
	}
	if len(request.VolumeConfigurations) > 0 {
		ctx["ecs:enable-ebs-volumes"] = []string{"true"}
	}
	// ListServicesByNamespace names the namespace it lists at the top level.
	iamSetConditionValues(ctx, "ecs:namespace", request.ServiceConnectConfiguration.Namespace, request.Namespace)
	// The capacity providers the request places on: a placement strategy's,
	// the providers a cluster is given, and a cluster's default strategy.
	providers := append([]string{}, request.CapacityProviders...)
	providers = append(providers, request.CapacityProviderArns...)
	for _, item := range append(request.CapacityProviderStrategy, request.DefaultCapacityProvider...) {
		providers = append(providers, item.CapacityProvider)
	}
	iamSetConditionValues(ctx, "ecs:capacity-provider", providers...)
	iamSetConditionValues(ctx, "ecs:subnet", append(request.NetworkConfiguration.AwsvpcConfiguration.Subnets,
		request.NetworkConfiguration.Subnets...)...)

	// The size a request states for itself wins: a task definition's
	// registration, or an Express Mode service's own cpu and memory. Otherwise
	// it is the named task definition's, unless the request overrides it —
	// which is the order Amazon ECS resolves it in.
	definitionName := request.TaskDefinition
	if definitionName == "" {
		definitionName = request.TaskDefinitionArn
	}
	cpu, memory := request.Cpu, request.Memory
	if cpu == "" {
		cpu = request.Overrides.Cpu
	}
	if memory == "" {
		memory = request.Overrides.Memory
	}
	if definitionName != "" {
		ctx["ecs:task-definition"] = []string{definitionName}
	}
	if definition, ok := ecsServiceTaskDefinition(definitionName); ok && definitionName != "" {
		if cpu == "" {
			cpu = definition.Cpu
		}
		if memory == "" {
			memory = definition.Memory
		}
	}
	// A daemon runs its daemon task definition, whose size is the daemon's.
	if definition, ok := ecsDaemonTaskDefinitions.Get(ecsDaemonTDRefKey(request.DaemonTaskDefinitionArn)); ok &&
		request.DaemonTaskDefinitionArn != "" {
		if cpu == "" {
			cpu = definition.Cpu
		}
		if memory == "" {
			memory = definition.Memory
		}
	}
	if cpu != "" {
		ctx["ecs:task-cpu"] = []string{cpu}
	}
	if memory != "" {
		ctx["ecs:task-memory"] = []string{memory}
	}
}
