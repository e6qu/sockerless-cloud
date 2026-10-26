package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ecs:TagResource covers ten resource types: capacity provider, cluster,
// container instance, daemon, daemon task definition, gateway, service, task,
// task definition and task set. All three tagging operations resolve through
// one function, so no type is taggable yet invisible to ListTagsForResource.

// The tags, and the write that puts them back.
type ecsTaggable struct {
	tags    []ECSTag
	replace func([]ECSTag)
}

// Each resource type has its own not-found exception.
type ecsTagFault struct {
	code    string
	message string
}

// Either the resource exists or the fault says why not.
func ecsResolveTaggable(resourceArn string) (*ecsTaggable, *ecsTagFault) {
	notFound := func(code, kind string) (*ecsTaggable, *ecsTagFault) {
		return nil, &ecsTagFault{code: code, message: kind + " not found: " + resourceArn}
	}
	switch {
	// Check before ":task-definition/" and ":service/": a daemon task definition
	// ends in the same word, and a task set's ARN names its service.
	case strings.Contains(resourceArn, ":daemon-task-definition/"):
		key := ecsDaemonTDRefKey(resourceArn)
		td, ok := ecsDaemonTaskDefinitions.Get(key)
		if !ok {
			return notFound("ClientException", "daemon task definition")
		}
		return &ecsTaggable{tags: td.Tags, replace: func(tags []ECSTag) {
			td.Tags = tags
			ecsDaemonTaskDefinitions.Put(key, td)
		}}, nil
	case strings.Contains(resourceArn, ":daemon/"):
		daemon, ok := ecsDaemons.Get(resourceArn)
		if !ok {
			return notFound("ClientException", "daemon")
		}
		return &ecsTaggable{tags: daemon.Tags, replace: func(tags []ECSTag) {
			daemon.Tags = tags
			ecsDaemons.Put(resourceArn, daemon)
		}}, nil
	case strings.Contains(resourceArn, ":task-set/"):
		// task-set/<cluster>/<service>/<id>; ecsLookupTaskSet reduces the ARN to its
		// id.
		rest := resourceArn[strings.Index(resourceArn, ":task-set/")+len(":task-set/"):]
		segments := strings.SplitN(rest, "/", 3)
		if len(segments) < 3 {
			return notFound("TaskSetNotFoundException", "task set")
		}
		ts, key, ok := ecsLookupTaskSet(segments[0], segments[1], resourceArn)
		if !ok {
			return notFound("TaskSetNotFoundException", "task set")
		}
		return &ecsTaggable{tags: ts.Tags, replace: func(tags []ECSTag) {
			ts.Tags = tags
			ecsTaskSets.Put(key, ts)
		}}, nil
	case strings.Contains(resourceArn, ":capacity-provider/"):
		name := resourceArn[strings.LastIndex(resourceArn, "/")+1:]
		cp, ok := ecsCapacityProviders.Get(name)
		if !ok {
			return notFound("ClientException", "capacity provider")
		}
		return &ecsTaggable{tags: cp.Tags, replace: func(tags []ECSTag) {
			cp.Tags = tags
			ecsCapacityProviders.Put(name, cp)
		}}, nil
	case strings.Contains(resourceArn, ":container-instance/"):
		key := ecsContainerInstanceKeyFromARN(resourceArn)
		ci, ok := ecsContainerInstances.Get(key)
		if !ok {
			return notFound("InvalidParameterException", "container instance")
		}
		return &ecsTaggable{tags: ci.Tags, replace: func(tags []ECSTag) {
			ci.Tags = tags
			ecsContainerInstances.Put(key, ci)
		}}, nil
	case strings.Contains(resourceArn, ":task-definition/"):
		key := extractTDKey(resourceArn)
		td, ok := ecsTaskDefinitions.Get(key)
		if !ok {
			return notFound("ClientException", "task definition")
		}
		return &ecsTaggable{tags: td.Tags, replace: func(tags []ECSTag) {
			td.Tags = tags
			ecsTaskDefinitions.Put(key, td)
		}}, nil
	case strings.Contains(resourceArn, ":task/"):
		parts := strings.Split(resourceArn, "/")
		id := parts[len(parts)-1]
		task, ok := ecsTasks.Get(id)
		if !ok {
			return notFound("InvalidParameterException", "task")
		}
		return &ecsTaggable{tags: task.Tags, replace: func(tags []ECSTag) {
			task.Tags = tags
			ecsTasks.Put(id, task)
		}}, nil
	case strings.Contains(resourceArn, ":gateway/"):
		gateway, ok := ecsGateways.Get(resourceArn)
		if !ok {
			return nil, &ecsTagFault{code: "GatewayNotFoundException", message: "The specified gateway wasn't found: " + resourceArn}
		}
		return &ecsTaggable{tags: gateway.Tags, replace: func(tags []ECSTag) {
			ecsGateways.Update(resourceArn, func(current *ECSGateway) { current.Tags = tags })
		}}, nil
	case strings.Contains(resourceArn, ":cluster/"):
		name := ecsClusterNameFromRef(resourceArn)
		cluster, ok := ecsClusters.Get(name)
		if !ok {
			return notFound("ClusterNotFoundException", "cluster")
		}
		return &ecsTaggable{tags: cluster.Tags, replace: func(tags []ECSTag) {
			cluster.Tags = tags
			ecsClusters.Put(name, cluster)
		}}, nil
	case strings.Contains(resourceArn, ":service/"):
		_, key, svc, ok := ecsServiceFromARN(resourceArn)
		if !ok {
			return notFound("ServiceNotFoundException", "service")
		}
		return &ecsTaggable{tags: svc.Tags, replace: func(tags []ECSTag) {
			svc.Tags = tags
			ecsServices.Put(key, svc)
		}}, nil
	}
	return nil, &ecsTagFault{
		code:    "InvalidParameterException",
		message: "Amazon ECS does not tag the resource type named by " + resourceArn,
	}
}

// Amazon ECS refuses to tag or untag a stopped task with
// InvalidParameterException; reading its tags is still allowed.
func ecsRejectTaggingAStoppedTask(resourceArn string) *ecsTagFault {
	if !strings.Contains(resourceArn, ":task/") {
		return nil
	}
	parts := strings.Split(resourceArn, "/")
	task, ok := ecsTasks.Get(parts[len(parts)-1])
	if !ok {
		return nil // The resolver reports absence in its own words.
	}
	if task.LastStatus == ECSTaskStatusStopped || task.LastStatus == ECSTaskStatusDeprovisioning {
		return &ecsTagFault{
			code:    "InvalidParameterException",
			message: "The specified task is not in a state to be tagged: " + string(task.LastStatus),
		}
	}
	return nil
}

// Clients switch on the exception.
func ecsWriteTagFault(w http.ResponseWriter, fault *ecsTagFault) {
	AWSError(w, fault.code, fault.message, http.StatusBadRequest)
}

// New keys are added, existing keys overwritten, others kept.
func handleECSTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string   `json:"resourceArn"`
		Tags        []ECSTag `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceArn == "" {
		AWSError(w, "InvalidParameterException", "resourceArn is required", http.StatusBadRequest)
		return
	}
	if fault := ecsRejectTaggingAStoppedTask(req.ResourceArn); fault != nil {
		ecsWriteTagFault(w, fault)
		return
	}
	target, fault := ecsResolveTaggable(req.ResourceArn)
	if fault != nil {
		ecsWriteTagFault(w, fault)
		return
	}
	target.replace(mergeECSTagsByKey(target.tags, req.Tags))
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleECSUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string   `json:"resourceArn"`
		TagKeys     []string `json:"tagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceArn == "" || len(req.TagKeys) == 0 {
		AWSError(w, "InvalidParameterException", "resourceArn and tagKeys are required", http.StatusBadRequest)
		return
	}
	if fault := ecsRejectTaggingAStoppedTask(req.ResourceArn); fault != nil {
		ecsWriteTagFault(w, fault)
		return
	}
	target, fault := ecsResolveTaggable(req.ResourceArn)
	if fault != nil {
		ecsWriteTagFault(w, fault)
		return
	}
	drop := make(map[string]struct{}, len(req.TagKeys))
	for _, key := range req.TagKeys {
		drop[key] = struct{}{}
	}
	// Build a fresh slice: filtering in place would edit the store's own copy.
	kept := make([]ECSTag, 0, len(target.tags))
	for _, tag := range target.tags {
		if _, gone := drop[tag.Key]; gone {
			continue
		}
		kept = append(kept, tag)
	}
	target.replace(kept)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// "If existing tags on a resource are not specified in the request
// parameters, they aren't changed."
func mergeECSTagsByKey(existing, incoming []ECSTag) []ECSTag {
	byKey := make(map[string]ECSTag, len(existing)+len(incoming))
	for _, t := range existing {
		byKey[t.Key] = t
	}
	for _, t := range incoming {
		byKey[t.Key] = t
	}
	out := make([]ECSTag, 0, len(byKey))
	for _, t := range byKey {
		out = append(out, t)
	}
	return out
}

func handleECSListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"resourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	// The same resolver as TagResource.
	target, fault := ecsResolveTaggable(req.ResourceArn)
	if fault != nil {
		ecsWriteTagFault(w, fault)
		return
	}
	tags := target.tags
	if tags == nil {
		tags = []ECSTag{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"tags": tags})
}
