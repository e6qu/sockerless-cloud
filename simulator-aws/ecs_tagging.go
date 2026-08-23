package main

import (
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Tagging for every Amazon ECS resource type AWS declares taggable.
//
// The service reference lists nine for ecs:TagResource — capacity provider,
// cluster, container instance, daemon, daemon task definition, service, task,
// task definition and task set — and the three tagging operations had covered
// four of them, answering the other five with "tag-target type not implemented
// in sim". Each of those five is a resource this simulator already holds, so
// the refusal was a gap rather than a limit.
//
// The three operations resolve the target the same way, through one function,
// so a type cannot be taggable through TagResource and invisible to
// ListTagsForResource.

// ecsTaggable is a resolved tag target: the tags it carries, and the write
// that puts them back where they came from.
type ecsTaggable struct {
	tags    []ECSTag
	replace func([]ECSTag)
}

// ecsTagFault is the error a resolution failed with, in the service's own
// vocabulary — each ECS resource type has its own not-found exception.
type ecsTagFault struct {
	code    string
	message string
}

// ecsResolveTaggable resolves the resource an ARN names. A nil fault with a nil
// resource cannot happen: either the ARN names a resource that exists, or the
// fault says why not.
func ecsResolveTaggable(resourceArn string) (*ecsTaggable, *ecsTagFault) {
	notFound := func(code, kind string) (*ecsTaggable, *ecsTagFault) {
		return nil, &ecsTagFault{code: code, message: kind + " not found: " + resourceArn}
	}
	switch {
	// Checked before ":task-definition/" and ":service/" would match: a daemon
	// task definition's type ends in the same word, and a task set's ARN names
	// the service it belongs to.
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
		// task-set/<cluster>/<service>/<id>: the cluster and service scope the
		// lookup, and ecsLookupTaskSet reduces the ARN to its id itself.
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

// ecsRejectTaggingAStoppedTask reproduces the one rule that is about the
// operation rather than the resource: real Amazon ECS refuses to tag or untag
// a task that has stopped, and says so with InvalidParameterException. Reading
// a stopped task's tags is still allowed, so ListTagsForResource does not ask.
func ecsRejectTaggingAStoppedTask(resourceArn string) *ecsTagFault {
	if !strings.Contains(resourceArn, ":task/") {
		return nil
	}
	parts := strings.Split(resourceArn, "/")
	task, ok := ecsTasks.Get(parts[len(parts)-1])
	if !ok {
		return nil // absence is the resolver's to report, in its own words
	}
	if task.LastStatus == ECSTaskStatusStopped || task.LastStatus == ECSTaskStatusDeprovisioning {
		return &ecsTagFault{
			code:    "InvalidParameterException",
			message: "The specified task is not in a state to be tagged: " + string(task.LastStatus),
		}
	}
	return nil
}

// ecsWriteTagFault answers with the fault's own exception, which is what a
// client's error handling switches on.
func ecsWriteTagFault(w http.ResponseWriter, fault *ecsTagFault) {
	sim.AWSError(w, fault.code, fault.message, http.StatusBadRequest)
}
