package main

import (
	"net/http"
	"strconv"
)

func init() {
	registerIAMRequestConditionPopulator("batch", iamPopulateBatchRequestConditionKeys)
}

// iamPopulateBatchRequestConditionKeys adds batch:SchedulingPriority, the
// scheduling priority a service job is submitted or updated with.
func iamPopulateBatchRequestConditionKeys(_ *http.Request, operation string, body []byte, ctx map[string][]string) {
	if operation != "SubmitServiceJob" && operation != "UpdateServiceJob" {
		return
	}
	var request struct {
		SchedulingPriority *int64 `json:"schedulingPriority"`
	}
	if iamDecodeJSONRequest(body, &request) {
		iamSetConditionInt(ctx, "batch:SchedulingPriority", request.SchedulingPriority)
	}
}

type batchConditionLogConfiguration struct {
	LogDriver string            `json:"logDriver"`
	Options   map[string]string `json:"options"`
}

type batchConditionContainer struct {
	Image            string                          `json:"image"`
	Privileged       *bool                           `json:"privileged"`
	User             string                          `json:"user"`
	LogConfiguration *batchConditionLogConfiguration `json:"logConfiguration"`
}

type batchConditionEKSContainer struct {
	Image           string `json:"image"`
	SecurityContext *struct {
		Privileged *bool  `json:"privileged"`
		RunAsUser  *int64 `json:"runAsUser"`
		RunAsGroup *int64 `json:"runAsGroup"`
	} `json:"securityContext"`
}

type batchConditionEKSProperties struct {
	PodProperties *struct {
		ServiceAccountName string `json:"serviceAccountName"`
		Metadata           *struct {
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Containers     []batchConditionEKSContainer `json:"containers"`
		InitContainers []batchConditionEKSContainer `json:"initContainers"`
	} `json:"podProperties"`
}

type batchConditionECSProperties struct {
	TaskProperties []struct {
		Containers []batchConditionContainer `json:"containers"`
	} `json:"taskProperties"`
}

// iamBatchJobDefinitionTargets authorizes a RegisterJobDefinition once for
// each container the job definition describes, with that container's
// condition keys: AWS Batch "evaluates condition keys on each container", so a
// statement whose conditions one container fails does not apply to that
// container, whatever the others carry (AWS Batch User Guide, "Deny action
// when all conditions match strings"). A job definition with no container is
// authorized once, with none of the keys.
func iamBatchJobDefinitionTargets(r *http.Request, resources []string) []iamAuthorizationTarget {
	var request struct {
		ContainerProperties *batchConditionContainer     `json:"containerProperties"`
		EcsProperties       *batchConditionECSProperties `json:"ecsProperties"`
		EksProperties       *batchConditionEKSProperties `json:"eksProperties"`
		NodeProperties      *struct {
			NodeRangeProperties []struct {
				Container     *batchConditionContainer     `json:"container"`
				EcsProperties *batchConditionECSProperties `json:"ecsProperties"`
				EksProperties *batchConditionEKSProperties `json:"eksProperties"`
			} `json:"nodeRangeProperties"`
		} `json:"nodeProperties"`
	}
	var contexts []map[string][]string
	if iamDecodeJSONRequest(iamRequestBody(r), &request) {
		contexts = append(contexts, batchContainerContexts(request.ContainerProperties, request.EcsProperties, request.EksProperties)...)
		if request.NodeProperties != nil {
			for _, nodeRange := range request.NodeProperties.NodeRangeProperties {
				contexts = append(contexts, batchContainerContexts(nodeRange.Container, nodeRange.EcsProperties, nodeRange.EksProperties)...)
			}
		}
	}
	if len(contexts) == 0 {
		contexts = []map[string][]string{nil}
	}
	targets := make([]iamAuthorizationTarget, 0, len(contexts)*len(resources))
	for _, ctx := range contexts {
		for _, arn := range resources {
			targets = append(targets, iamAuthorizationTarget{action: "batch:RegisterJobDefinition", resource: arn, context: ctx})
		}
	}
	return targets
}

func batchContainerContexts(container *batchConditionContainer, ecs *batchConditionECSProperties, eks *batchConditionEKSProperties) []map[string][]string {
	var contexts []map[string][]string
	if container != nil {
		contexts = append(contexts, batchContainerContext(*container))
	}
	if ecs != nil {
		for _, task := range ecs.TaskProperties {
			for _, c := range task.Containers {
				contexts = append(contexts, batchContainerContext(c))
			}
		}
	}
	if eks != nil && eks.PodProperties != nil {
		pod := eks.PodProperties
		namespace := ""
		if pod.Metadata != nil {
			namespace = pod.Metadata.Namespace
		}
		for _, c := range append(append([]batchConditionEKSContainer{}, pod.Containers...), pod.InitContainers...) {
			ctx := map[string][]string{}
			iamSetConditionValues(ctx, "batch:EKSImage", c.Image)
			iamSetConditionValues(ctx, "batch:EKSServiceAccountName", pod.ServiceAccountName)
			iamSetConditionValues(ctx, "batch:EKSNamespace", namespace)
			if security := c.SecurityContext; security != nil {
				iamSetConditionBool(ctx, "batch:EKSPrivileged", security.Privileged)
				iamSetConditionInt(ctx, "batch:EKSRunAsUser", security.RunAsUser)
				iamSetConditionInt(ctx, "batch:EKSRunAsGroup", security.RunAsGroup)
			}
			contexts = append(contexts, ctx)
		}
	}
	return contexts
}

// batchContainerContext reads one container's keys. The batch:AWSLogs* keys
// are the awslogs log driver's own options, so they describe a container that
// logs through awslogs.
func batchContainerContext(c batchConditionContainer) map[string][]string {
	ctx := map[string][]string{}
	iamSetConditionValues(ctx, "batch:Image", c.Image)
	iamSetConditionBool(ctx, "batch:Privileged", c.Privileged)
	iamSetConditionValues(ctx, "batch:User", c.User)
	if logs := c.LogConfiguration; logs != nil {
		iamSetConditionValues(ctx, "batch:LogDriver", logs.LogDriver)
		if logs.LogDriver == "awslogs" {
			iamSetConditionValues(ctx, "batch:AWSLogsGroup", logs.Options["awslogs-group"])
			iamSetConditionValues(ctx, "batch:AWSLogsRegion", logs.Options["awslogs-region"])
			iamSetConditionValues(ctx, "batch:AWSLogsStreamPrefix", logs.Options["awslogs-stream-prefix"])
			if create, err := strconv.ParseBool(logs.Options["awslogs-create-group"]); err == nil {
				iamSetConditionBool(ctx, "batch:AWSLogsCreateGroup", &create)
			}
		}
	}
	return ctx
}
