package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func init() {
	registerIAMRequestConditionPopulator("ecs", iamPopulateECSRequestConditionKeys)
}

type ecsConditionRequest struct {
	Name                    string   `json:"name"`
	Cluster                 string   `json:"cluster"`
	Task                    string   `json:"task"`
	Container               string   `json:"container"`
	ContainerInstances      []string `json:"containerInstances"`
	RequiresCompatibilities []string `json:"requiresCompatibilities"`
	ContainerDefinitions    []struct {
		Privileged *bool `json:"privileged"`
	} `json:"containerDefinitions"`
	NetworkConfiguration struct {
		AwsvpcConfiguration struct {
			AssignPublicIp string `json:"assignPublicIp"`
		} `json:"awsvpcConfiguration"`
	} `json:"networkConfiguration"`
	ServiceConnectConfiguration struct {
		Enabled *bool `json:"enabled"`
	} `json:"serviceConnectConfiguration"`
	VpcLatticeConfigurations []json.RawMessage `json:"vpcLatticeConfigurations"`
	Configuration            struct {
		ManagedStorageConfiguration struct {
			FargateEphemeralStorageKmsKeyId string `json:"fargateEphemeralStorageKmsKeyId"`
		} `json:"managedStorageConfiguration"`
	} `json:"configuration"`
	ManagedInstancesProvider struct {
		InstanceLaunchTemplate struct {
			InstanceMetadataTagsPropagation *bool `json:"instanceMetadataTagsPropagation"`
		} `json:"instanceLaunchTemplate"`
	} `json:"managedInstancesProvider"`
}

// iamPopulateECSRequestConditionKeys adds the Amazon ECS keys that
// iamPopulateECSConditionKeys does not: account settings, task-definition
// shape, service networking, cluster storage encryption, and the targets of
// StartTask and ExecuteCommand.
func iamPopulateECSRequestConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	var request ecsConditionRequest
	if !iamDecodeJSONRequest(body, &request) {
		return
	}
	switch operation {
	case "PutAccountSetting", "PutAccountSettingDefault", "DeleteAccountSetting":
		iamSetConditionValues(ctx, "ecs:account-setting", request.Name)
	case "RegisterTaskDefinition", "RegisterDaemonTaskDefinition":
		if operation == "RegisterTaskDefinition" {
			iamSetConditionValues(ctx, "ecs:compute-compatibility", request.RequiresCompatibilities...)
		}
		iamSetConditionValues(ctx, "ecs:privileged", ecsRequestPrivileged(request)...)
	case "CreateService", "UpdateService":
		switch request.NetworkConfiguration.AwsvpcConfiguration.AssignPublicIp {
		case "ENABLED":
			iamSetConditionValues(ctx, "ecs:auto-assign-public-ip", "true")
		case "DISABLED":
			iamSetConditionValues(ctx, "ecs:auto-assign-public-ip", "false")
		}
		iamSetConditionBool(ctx, "ecs:enable-service-connect", request.ServiceConnectConfiguration.Enabled)
		// An empty list is how a request turns VPC Lattice off.
		if request.VpcLatticeConfigurations != nil {
			iamSetConditionValues(ctx, "ecs:enable-vpc-lattice",
				strconv.FormatBool(len(request.VpcLatticeConfigurations) > 0))
		}
	case "CreateCluster", "UpdateCluster":
		iamSetConditionValues(ctx, "ecs:fargate-ephemeral-storage-kms-key",
			request.Configuration.ManagedStorageConfiguration.FargateEphemeralStorageKmsKeyId)
	case "CreateCapacityProvider", "UpdateCapacityProvider":
		iamSetConditionBool(ctx, "ecs:instance-metadata-tags-propagation",
			request.ManagedInstancesProvider.InstanceLaunchTemplate.InstanceMetadataTagsPropagation)
	case "StartTask":
		cluster := ecsClusterNameFromRef(request.Cluster)
		arns := make([]string, 0, len(request.ContainerInstances))
		for _, ref := range request.ContainerInstances {
			if strings.HasPrefix(ref, "arn:") {
				arns = append(arns, ref)
				continue
			}
			arns = append(arns, ecsArn("container-instance", cluster+"/"+ref))
		}
		iamSetConditionValues(ctx, "ecs:container-instances", arns...)
	case "ExecuteCommand":
		iamSetConditionValues(ctx, "ecs:container-name", ecsExecRequestContainer(request))
	}
}

// ecsRequestPrivileged is "true" when any container definition asks for
// privileged mode, "false" when those that state it all decline, and nothing
// when none states it. A task is privileged if one of its containers is.
func ecsRequestPrivileged(request ecsConditionRequest) []string {
	stated := false
	for _, definition := range request.ContainerDefinitions {
		if definition.Privileged == nil {
			continue
		}
		if *definition.Privileged {
			return []string{"true"}
		}
		stated = true
	}
	if stated {
		return []string{"false"}
	}
	return nil
}

// ecsExecRequestContainer is the container an ExecuteCommand runs in: the one
// the request names, or the task's only container when it names none, which is
// the one Amazon ECS picks.
func ecsExecRequestContainer(request ecsConditionRequest) string {
	if request.Container != "" {
		return request.Container
	}
	taskID := request.Task
	if i := strings.LastIndex(taskID, "/"); i >= 0 {
		taskID = taskID[i+1:]
	}
	if taskID == "" {
		return ""
	}
	task, ok := ecsTasks.Get(taskID)
	if !ok {
		return ""
	}
	if container := ecsExecTargetContainer(task, ""); container != nil {
		return container.Name
	}
	return ""
}
