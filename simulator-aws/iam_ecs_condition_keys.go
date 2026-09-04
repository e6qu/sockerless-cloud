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
// definition the request names, because that is where Amazon ECS reads them
// from when the request does not override them.

// iamPopulateECSConditionKeys adds the keys an Amazon ECS request settles.
func iamPopulateECSConditionKeys(r *http.Request, body []byte, ctx map[string][]string) {
	if len(body) == 0 {
		return
	}
	var request struct {
		TaskDefinition           string `json:"taskDefinition"`
		ServiceName              string `json:"serviceName"`
		Name                     string `json:"name"`
		EnableECSManagedTags     *bool  `json:"enableECSManagedTags"`
		EnableExecuteCommand     *bool  `json:"enableExecuteCommand"`
		CapacityProviderStrategy []struct {
			CapacityProvider string `json:"capacityProvider"`
		} `json:"capacityProviderStrategy"`
		NetworkConfiguration struct {
			AwsvpcConfiguration struct {
				Subnets []string `json:"subnets"`
			} `json:"awsvpcConfiguration"`
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

	if request.ServiceName != "" {
		ctx["ecs:service"] = []string{request.ServiceName}
	}
	if request.EnableECSManagedTags != nil {
		ctx["ecs:enable-ecs-managed-tags"] = []string{strconv.FormatBool(*request.EnableECSManagedTags)}
	}
	if request.EnableExecuteCommand != nil {
		ctx["ecs:enable-execute-command"] = []string{strconv.FormatBool(*request.EnableExecuteCommand)}
	}
	if len(request.VolumeConfigurations) > 0 {
		ctx["ecs:enable-ebs-volumes"] = []string{"true"}
	}
	if request.ServiceConnectConfiguration.Namespace != "" {
		ctx["ecs:namespace"] = []string{request.ServiceConnectConfiguration.Namespace}
	}
	if providers := request.CapacityProviderStrategy; len(providers) > 0 {
		names := make([]string, 0, len(providers))
		for _, item := range providers {
			if item.CapacityProvider != "" {
				names = append(names, item.CapacityProvider)
			}
		}
		if len(names) > 0 {
			ctx["ecs:capacity-provider"] = names
		}
	}
	if subnets := request.NetworkConfiguration.AwsvpcConfiguration.Subnets; len(subnets) > 0 {
		ctx["ecs:subnet"] = subnets
	}

	if request.TaskDefinition == "" {
		return
	}
	ctx["ecs:task-definition"] = []string{request.TaskDefinition}
	// The size is the task definition's, unless the request overrides it —
	// which is the order Amazon ECS resolves it in.
	cpu, memory := request.Overrides.Cpu, request.Overrides.Memory
	if definition, ok := ecsServiceTaskDefinition(request.TaskDefinition); ok {
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
