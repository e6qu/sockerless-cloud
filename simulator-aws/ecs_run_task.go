package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// ecsTaskCloudWatchSink prepares CloudWatch Logs resources for a task and
// returns a log sink for containers using the awslogs driver. The log stream is
// created before the container starts so it is observable as soon as the task
// reports RUNNING. The awslogs driver creates the log group only when
// awslogs-create-group is "true"; otherwise a missing group fails the start with
// the ResourceInitializationError Amazon ECS reports.
func ecsTaskCloudWatchSink(td ECSTaskDefinition, taskID string) (sim.LogSink, error) {
	var sink sim.LogSink = discardLogSink{}
	for _, cd := range td.ContainerDefinitions {
		if cd.LogConfiguration == nil || cd.LogConfiguration.LogDriver != "awslogs" {
			continue
		}
		logGroup := cd.LogConfiguration.Options["awslogs-group"]
		streamPrefix := cd.LogConfiguration.Options["awslogs-stream-prefix"]
		if logGroup == "" || streamPrefix == "" {
			continue
		}
		nowMs := time.Now().UnixMilli()
		if _, exists := cwLogGroups.Get(logGroup); !exists {
			if cd.LogConfiguration.Options["awslogs-create-group"] != "true" {
				return nil, &ecsResourceInitializationError{err: errors.New("failed to validate logger args: create stream has been retried 1 times: failed to create Cloudwatch log stream: ResourceNotFoundException: The specified log group does not exist. : exit status 1")}
			}
			cwLogGroups.Put(logGroup, CWLogGroup{
				LogGroupName: logGroup,
				Arn:          cwLogGroupArn(logGroup),
				CreationTime: nowMs,
			})
		}
		if _, ok := sink.(discardLogSink); !ok {
			continue
		}

		logStreamName := fmt.Sprintf("%s/%s/%s", streamPrefix, cd.Name, taskID)
		key := cwEventsKey(logGroup, logStreamName)
		if _, exists := cwLogStreams.Get(key); !exists {
			// First/last event timestamps stay unset until the container
			// writes; the sink stamps them on its first line, as CloudWatch
			// does. Amazon ECS writes nothing to the stream until the
			// container itself writes.
			cwLogStreams.Put(key, CWLogStream{
				LogStreamName:       logStreamName,
				LogGroupName:        logGroup,
				CreationTime:        nowMs,
				Arn:                 cwLogStreamArn(logGroup, logStreamName),
				UploadSequenceToken: "1",
			})
			cwLogEvents.Open(key)
		}
		sink = &cwLogSink{logGroup: logGroup, logStream: logStreamName}
	}
	return sink, nil
}

// ecsRunTaskInput is the parsed RunTask request — extracted so the in-process
// service scheduler can launch tasks through the same code path as the
// RunTask API handler.
type ecsRunTaskInput struct {
	Cluster              string
	TaskDefinition       string
	Count                int
	Group                string
	LaunchType           string
	Tags                 []ECSTag
	PropagateTags        string
	EnableExecuteCommand bool
	Overrides            *ECSTaskOverride
	VolumeConfigurations []ECSTaskVolumeConfiguration
	NetworkConfiguration *ECSTaskNetworkConfig
	StartedBy            string
	ContainerInstanceArn string
	ContainerInstanceKey string
}

func ecsTaskArn(clusterName, taskID string) string {
	return "arn:aws:ecs:" + awsRegion() + ":" + awsAccountID() + ":task/" + clusterName + "/" + taskID
}

// ecsTaskRoles returns the task role and the execution role a task runs with:
// its overrides', else its task definition's.
func ecsTaskRoles(td ECSTaskDefinition, overrides *ECSTaskOverride) (taskRole, executionRole string) {
	taskRole, executionRole = td.TaskRoleArn, td.ExecutionRoleArn
	if overrides != nil {
		if overrides.TaskRoleArn != "" {
			taskRole = overrides.TaskRoleArn
		}
		if overrides.ExecutionRoleArn != "" {
			executionRole = overrides.ExecutionRoleArn
		}
	}
	return taskRole, executionRole
}

// ecsRefuseUnassumableTaskRoles refuses to launch a task whose task role or
// execution role names no role or does not trust ecs-tasks.amazonaws.com.
// Amazon ECS checks when it launches the task, not at RegisterTaskDefinition.
func ecsRefuseUnassumableTaskRoles(td ECSTaskDefinition, overrides *ECSTaskOverride, taskArn string) *ecsRequestError {
	taskRole, executionRole := ecsTaskRoles(td, overrides)
	for _, role := range []string{taskRole, executionRole} {
		if role == "" {
			continue
		}
		if _, err := iamServiceTrustedRole(role, "ecs-tasks.amazonaws.com", taskArn); err != nil {
			return &ecsRequestError{"ClientException", fmt.Sprintf("ECS was unable to assume the role '%s' that was provided for this task. "+
				"Please verify that the role being passed has the proper trust relationship and permissions "+
				"and that your IAM user has permissions to pass this role.", role), http.StatusBadRequest}
		}
	}
	return nil
}

func ecsUpdateContainerInstanceTaskCounts(key string, pendingDelta, runningDelta int) {
	if key == "" {
		return
	}
	ecsContainerInstances.Update(key, func(instance *ECSContainerInstance) {
		instance.PendingTasksCount += pendingDelta
		instance.RunningTasksCount += runningDelta
		if instance.PendingTasksCount < 0 {
			instance.PendingTasksCount = 0
		}
		if instance.RunningTasksCount < 0 {
			instance.RunningTasksCount = 0
		}
	})
}

func ecsContainerInstanceKeyFromARN(arn string) string {
	resource := arn
	if i := strings.LastIndex(resource, ":"); i >= 0 {
		resource = resource[i+1:]
	}
	parts := strings.Split(resource, "/")
	if len(parts) < 3 {
		return ""
	}
	return ecsContainerInstanceKey(parts[len(parts)-2], parts[len(parts)-1])
}

func handleECSRunTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster              string                       `json:"cluster"`
		TaskDefinition       string                       `json:"taskDefinition"`
		Count                int                          `json:"count"`
		LaunchType           string                       `json:"launchType"`
		Group                string                       `json:"group"`
		StartedBy            string                       `json:"startedBy"`
		Tags                 []ECSTag                     `json:"tags,omitempty"`
		PropagateTags        string                       `json:"propagateTags,omitempty"`
		EnableExecuteCommand bool                         `json:"enableExecuteCommand,omitempty"`
		Overrides            *ECSTaskOverride             `json:"overrides,omitempty"`
		VolumeConfigurations []ECSTaskVolumeConfiguration `json:"volumeConfigurations,omitempty"`
		NetworkConfiguration *ECSTaskNetworkConfig        `json:"networkConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.TaskDefinition == "" {
		AWSError(w, "InvalidParameterException", "taskDefinition is required", http.StatusBadRequest)
		return
	}
	if req.Count == 0 {
		req.Count = 1
	}
	// RunTask accepts up to 10 tasks per call (documented max).
	if req.Count > 10 {
		AWSError(w, "InvalidParameterException",
			"count cannot be greater than 10.", http.StatusBadRequest)
		return
	}
	in := ecsRunTaskInput{
		Cluster:              req.Cluster,
		TaskDefinition:       req.TaskDefinition,
		Count:                req.Count,
		Group:                req.Group,
		LaunchType:           req.LaunchType,
		StartedBy:            req.StartedBy,
		Tags:                 req.Tags,
		PropagateTags:        req.PropagateTags,
		EnableExecuteCommand: req.EnableExecuteCommand,
		Overrides:            req.Overrides,
		VolumeConfigurations: req.VolumeConfigurations,
		NetworkConfiguration: req.NetworkConfiguration,
	}
	tasks, failures, rerr := runECSTasks(r.Context(), in)
	if rerr != nil {
		AWSError(w, rerr.code, rerr.message, rerr.status)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"tasks":    ecsTasksWire(tasks),
		"failures": failures,
	})
}

// runECSTasks launches `in.Count` tasks for the named cluster / task
// definition, performing the same validation, placement, ENI allocation, and
// async PROVISIONING → PENDING → RUNNING lifecycle transitions as the RunTask
// API. A task the host cannot fit is not launched: it comes back as a
// `failures[]` entry (never nil — the wire shape is `[]`), while the tasks
// that did fit are launched, exactly as Amazon ECS places what it can. Used by
// handleECSRunTask, StartTask and the in-process ECS service scheduler.
func runECSTasks(ctx context.Context, in ecsRunTaskInput) ([]ECSTask, []ecsFailure, *ecsRequestError) {
	if in.Cluster == "" {
		in.Cluster = "default"
	}

	// Resolve cluster name
	clusterName := in.Cluster
	if strings.HasPrefix(clusterName, "arn:") {
		parts := strings.Split(clusterName, "/")
		if len(parts) > 1 {
			clusterName = parts[len(parts)-1]
		}
	}

	cluster, ok := ecsClusters.Get(clusterName)
	if !ok {
		return nil, nil, &ecsRequestError{"ClusterNotFoundException",
			fmt.Sprintf("Cluster not found: %s", in.Cluster), http.StatusBadRequest}
	}

	// Resolve task definition
	tdKey := ecsTaskDefinitionKey(in.TaskDefinition)
	td, ok := ecsTaskDefinitions.Get(tdKey)
	if !ok {
		return nil, nil, &ecsRequestError{"ClientException",
			fmt.Sprintf("Unable to describe task definition: %s", in.TaskDefinition), http.StatusBadRequest}
	}

	// Validate security groups exist
	if in.NetworkConfiguration != nil && in.NetworkConfiguration.AwsvpcConfiguration != nil {
		for _, sgID := range in.NetworkConfiguration.AwsvpcConfiguration.SecurityGroups {
			if _, sgOK := ec2SecurityGroups.Get(sgID); !sgOK {
				return nil, nil, &ecsRequestError{"InvalidParameterException",
					fmt.Sprintf("The security group '%s' does not exist", sgID), http.StatusBadRequest}
			}
		}
	}

	// networkConfiguration is required for awsvpc task definitions — that is
	// what allocates the task its own elastic network interface — and is not
	// supported for any other network mode, where the task shares the container
	// instance's networking. Rejecting both mismatches keeps a caller from
	// silently getting networking it did not ask for.
	networkMode := ecsEffectiveNetworkMode(td)
	if strings.EqualFold(in.LaunchType, "FARGATE") && networkMode != ecsNetworkModeAwsvpc {
		return nil, nil, &ecsRequestError{"ClientException",
			"Fargate tasks require the awsvpc network mode.", http.StatusBadRequest}
	}
	hasAwsvpcConfig := in.NetworkConfiguration != nil &&
		in.NetworkConfiguration.AwsvpcConfiguration != nil &&
		len(in.NetworkConfiguration.AwsvpcConfiguration.Subnets) > 0
	switch {
	case networkMode == ecsNetworkModeAwsvpc && !hasAwsvpcConfig:
		return nil, nil, &ecsRequestError{"InvalidParameterException",
			"Network Configuration must be provided when networking mode is awsvpc.", http.StatusBadRequest}
	case networkMode != ecsNetworkModeAwsvpc && in.NetworkConfiguration != nil:
		return nil, nil, &ecsRequestError{"InvalidParameterException",
			fmt.Sprintf("Network Configuration is not supported when networking mode is %s.", networkMode),
			http.StatusBadRequest}
	}

	// Real ECS validates the subnet exists in EC2 and uses its CIDR for
	// task IP assignment. Pull the requested subnet up front; surface a
	// clean InvalidParameterException when the caller passes one we
	// don't know about (matches real AWS InvalidSubnetID.NotFound).
	var requestedSubnet string
	if hasAwsvpcConfig {
		requestedSubnet = in.NetworkConfiguration.AwsvpcConfiguration.Subnets[0]
	}

	taskIDs := make([]string, in.Count)
	for i := range taskIDs {
		taskIDs[i] = sim.NewUUID()
	}
	if len(taskIDs) > 0 {
		if rerr := ecsRefuseUnassumableTaskRoles(td, in.Overrides, ecsTaskArn(clusterName, taskIDs[0])); rerr != nil {
			return nil, nil, rerr
		}
	}

	tasks := []ECSTask{}
	failures := []ecsFailure{}
	requested := ecsRequestedTaskSize(td, in.Overrides)
	for _, taskID := range taskIDs {
		taskArn := ecsTaskArn(clusterName, taskID)

		// Placement comes first: nothing below — the ENI, the managed volume,
		// the stored record — is allocated for a task the host cannot fit.
		if short, committed, ceiling, fits := ecsReservePlacement(taskID, requested); !fits {
			failures = append(failures, ecsPlacementFailure(in, cluster.ClusterArn, short, requested, committed, ceiling))
			continue
		}
		// A reservation is given back on every path that leaves without
		// storing the task — an allocation error below, or a panic — so a task
		// that never existed can never hold capacity.
		placed := false
		defer func(id string) {
			if !placed {
				ecsAbandonPlacement(id)
			}
		}(taskID)

		// Only an awsvpc task is allocated an elastic network interface. A
		// bridge/host/none task shares the container instance's networking and
		// carries no ENI attachment and no per-container networkInterfaces.
		eniID := sim.NewUUID()
		var privateIP, subnetID string
		if networkMode == ecsNetworkModeAwsvpc {
			ip, ipErr := AllocateSubnetIP(requestedSubnet)
			if ipErr != nil {
				return nil, nil, &ecsRequestError{"InvalidParameterException", ipErr.Error(), http.StatusBadRequest}
			}
			privateIP = ip
			subnetID = requestedSubnet
		}
		createdAt := ecsEpochSeconds()

		var containers []ECSTaskContainer
		for _, cd := range td.ContainerDefinitions {
			c := ECSTaskContainer{
				ContainerArn: fmt.Sprintf("arn:aws:ecs:"+awsRegion()+":"+awsAccountID()+":container/%s", sim.NewUUID()),
				TaskArn:      taskArn,
				Name:         cd.Name,
				Image:        cd.Image,
				LastStatus:   "PROVISIONING",
			}
			ecsApplyContainerSizing(&c, cd, in.Overrides)
			if networkMode == ecsNetworkModeAwsvpc {
				c.NetworkInterfaces = []ECSNetworkInterface{{
					AttachmentId:       eniID,
					PrivateIpv4Address: privateIP,
				}}
			}
			containers = append(containers, c)
		}

		// Merge tags: request tags take priority, then inherited from task def
		var taskTags []ECSTag
		if in.PropagateTags == "TASK_DEFINITION" && len(td.Tags) > 0 {
			taskTags = append(taskTags, td.Tags...)
		}
		taskTags = append(taskTags, in.Tags...)

		taskVolumeHosts, ebsAttachments, pendingRestores, ebsErr := ecsPrepareManagedEBSVolumes(ctx, td, in.VolumeConfigurations, taskArn, requestedSubnet)
		if ebsErr != nil {
			return nil, nil, ebsErr
		}

		task := ECSTask{
			TaskArn:              taskArn,
			TaskDefinitionArn:    td.TaskDefinitionArn,
			ClusterArn:           cluster.ClusterArn,
			LastStatus:           ECSTaskStatusProvisioning,
			DesiredStatus:        ECSTaskStatusRunning,
			Containers:           containers,
			CreatedAt:            &createdAt,
			Tags:                 taskTags,
			LaunchType:           in.LaunchType,
			Cpu:                  ecsTaskCPU(td, in.Overrides),
			Memory:               ecsTaskMemory(td, in.Overrides),
			Group:                ecsTaskGroup(in.Group, td),
			Overrides:            in.Overrides,
			EnableExecuteCommand: in.EnableExecuteCommand,
			StartedBy:            in.StartedBy,
			ContainerInstanceArn: in.ContainerInstanceArn,
			VolumeHosts:          taskVolumeHosts,
		}
		if networkMode == ecsNetworkModeAwsvpc {
			attachment, eniErr := ecsCreateTaskNetworkInterface(taskID, eniID, subnetID, privateIP, ecsTaskSecurityGroups(subnetID, in.NetworkConfiguration.AwsvpcConfiguration.SecurityGroups))
			if eniErr != nil {
				return nil, nil, &ecsRequestError{"InvalidParameterException", eniErr.Error(), http.StatusBadRequest}
			}
			task.Attachments = append(task.Attachments, attachment)
		}
		task.Attachments = append(task.Attachments, ebsAttachments...)

		// Store VPC network configuration from request
		if in.NetworkConfiguration != nil && in.NetworkConfiguration.AwsvpcConfiguration != nil {
			vpc := in.NetworkConfiguration.AwsvpcConfiguration
			task.NetworkConfiguration = &ECSTaskNetworkConfig{
				AwsvpcConfiguration: &ECSTaskVpcConfig{
					Subnets:        vpc.Subnets,
					SecurityGroups: vpc.SecurityGroups,
					AssignPublicIp: vpc.AssignPublicIp,
				},
			}
		}

		ecsCommitPlacement(taskID, task)
		placed = true
		if in.ContainerInstanceKey != "" {
			ecsContainerInstances.Update(in.ContainerInstanceKey, func(instance *ECSContainerInstance) {
				instance.PendingTasksCount++
			})
		}
		tasks = append(tasks, task)

		start := func(ctx context.Context, id string, td ECSTaskDefinition, taskTags []ECSTag, overrides *ECSTaskOverride, taskVolumeHosts map[string]string, launchType, containerInstanceKey string) {
			ecsStartTask(ctx, id, td, taskTags, overrides, taskVolumeHosts, launchType, containerInstanceKey, pendingRestores)
		}
		ecsScheduleTaskStart(start, taskID, td, taskTags, in.Overrides, taskVolumeHosts, in.LaunchType, in.ContainerInstanceKey)
	}

	return tasks, failures, nil
}

// ecsStartTask runs a task's PROVISIONING → PENDING → RUNNING lifecycle. It
// reports RUNNING only once the task's containers have started and their
// handles are stored, so a caller that sees RUNNING (ExecuteCommand, task
// metadata) finds the containers. It reads durable stores while it provisions
// them, so it runs under the server lifecycle and is drained before SQLite
// closes.
func ecsStartTask(ctx context.Context, id string, td ECSTaskDefinition, taskTags []ECSTag, overrides *ECSTaskOverride, taskVolumeHosts map[string]string, launchType, containerInstanceKey string, pendingRestores []ecsPendingEBSRestore) {
	lifecycleLock := ecsTaskLifecycleLock(id)
	if !lifecycleLock.LockContext(ctx) {
		return
	}
	defer lifecycleLock.Unlock()
	current, exists := ecsTasks.Get(id)
	if !exists || current.DesiredStatus == ECSTaskStatusStopped {
		return
	}

	// PROVISIONING → PENDING
	ecsTasks.Update(id, func(t *ECSTask) {
		t.LastStatus = ECSTaskStatusPending
		for j := range t.Containers {
			t.Containers[j].LastStatus = "PENDING"
		}
		for j := range t.Attachments {
			if t.Attachments[j].Type == "ElasticNetworkInterface" {
				t.Attachments[j].Status = "ATTACHING"
			}
		}
	})
	phases := newECSPhaseTimer(time.Now)

	// Hydrate any snapshot-backed managed-EBS volume before starting the
	// containers that mount it. A failure here is a resource problem, not
	// an exited container, so it stops the task the way real ECS does —
	// with TaskFailedToStart and a ResourceInitializationError reason the
	// caller can read from DescribeTasks.
	if len(pendingRestores) > 0 {
		if err := ecsRunPendingEBSRestores(ctx, id, pendingRestores); err != nil {
			if ctx.Err() != nil {
				ecsLogInterruptedStart(id)
				return
			}
			fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: managed EBS volume preparation failed: %v\n", id, err)
			stoppedAt := ecsEpochSeconds()
			var doomedVolumes []string
			ecsTasks.Update(id, func(t *ECSTask) {
				t.LastStatus = ECSTaskStatusStopped
				t.DesiredStatus = ECSTaskStatusStopped
				t.StoppedAt = &stoppedAt
				t.StopCode = "TaskFailedToStart"
				t.StoppedReason = fmt.Sprintf("ResourceInitializationError: unable to prepare managed EBS volume: %v", err)
				for j := range t.Containers {
					t.Containers[j].LastStatus = "STOPPED"
				}
				doomedVolumes = ecsReleaseTaskAttachments(t)
			})
			ecsDeleteManagedVolumes(doomedVolumes)
			ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 0)
			if task, ok := ecsTasks.Get(id); ok {
				ecsRequestServiceReconcileForTask(task)
			}
			return
		}
	}

	phases.Mark("ebs-restore")

	// Prepare CloudWatch logs before the container starts, so the log
	// group/stream are observable as soon as the task reports RUNNING.
	sink, err := ecsTaskCloudWatchSink(td, id)
	phases.Mark("log-stream")

	// Start containers. This is the real work that RUNNING must wait for.
	var processes *ecsTaskProcesses
	if err == nil {
		processes, err = startECSTaskContainers(ctx, id, td, taskTags, overrides, taskVolumeHosts, sink, launchType, phases)
	}
	if errors.Is(err, errECSTaskStartInterrupted) {
		ecsLogInterruptedStart(id)
		return
	}
	if err != nil {
		// Surface the start failure: it's otherwise only recorded in
		// the task's StoppedReason, so an intermittent VPC network or
		// image-pull failure reads as an opaque container ExitCode -1
		// with no diagnosable cause in logs.
		fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: container start failed: %v\n", id, err)
		stoppedAt := ecsEpochSeconds()
		var doomedVolumes []string
		var resourceErr *ecsResourceInitializationError
		resourceFailure := errors.As(err, &resourceErr)
		ecsTasks.Update(id, func(t *ECSTask) {
			t.LastStatus = ECSTaskStatusStopped
			t.DesiredStatus = ECSTaskStatusStopped
			t.StoppedAt = &stoppedAt
			t.StopCode = "EssentialContainerExited"
			t.StoppedReason = fmt.Sprintf("Container start failed: %v", err)
			if resourceFailure {
				t.StopCode = "TaskFailedToStart"
				t.StoppedReason = resourceErr.Error()
			}
			exitCode := -1
			for j := range t.Containers {
				t.Containers[j].LastStatus = "STOPPED"
				if !resourceFailure {
					t.Containers[j].ExitCode = &exitCode
				}
			}
			doomedVolumes = ecsReleaseTaskAttachments(t)
		})
		ecsDeleteManagedVolumes(doomedVolumes)
		ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 0)
		if task, ok := ecsTasks.Get(id); ok {
			ecsRequestServiceReconcileForTask(task)
		}
		return
	}

	// Containers are actually running. Report RUNNING and wire up
	// lifecycle waits. Store handles before marking RUNNING so any
	// concurrent observer that sees RUNNING also sees handles.
	if processes != nil {
		ecsProcessHandles.Store(id, processes)
	}
	now := ecsEpochSeconds()
	ecsTasks.Update(id, func(t *ECSTask) {
		t.LastStatus = ECSTaskStatusRunning
		t.Connectivity = "CONNECTED"
		t.StartedAt = &now
		for j := range t.Containers {
			t.Containers[j].LastStatus = "RUNNING"
		}
		for j := range t.Attachments {
			t.Attachments[j].Status = "ATTACHED"
		}
	})
	// One line per task naming where its start went: the pull is the
	// only phase the ECS API exposes, and it is rarely the slow one.
	fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: RUNNING %s\n", id, phases.Summary())
	ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 1)
	if processes != nil {
		ecsWatchTaskProcesses(id, containerInstanceKey, processes)
	}
	if task, ok := ecsTasks.Get(id); ok {
		ecsRequestServiceReconcileForTask(task)
	}
}

// ecsScheduleTaskStart runs one task's PROVISIONING→RUNNING lifecycle under the
// server's background-worker lifecycle when the ECS service has registered one,
// so orderly shutdown drains it before SQLite is closed. Package tests that call
// runECSTasks without building a server run it on a goroutine of its own.
//
// Either way the start is counted for the test drain barrier before it is
// handed over. The goroutine of its own was uncounted altogether, and the
// server's was counted only once it ran; both let a drain return while a start
// was about to read the control-plane stores the next test rebuilt.
func ecsScheduleTaskStart(
	start func(context.Context, string, ECSTaskDefinition, []ECSTag, *ECSTaskOverride, map[string]string, string, string),
	id string, td ECSTaskDefinition, taskTags []ECSTag, overrides *ECSTaskOverride,
	taskVolumeHosts map[string]string, launchType, containerInstanceKey string,
) {
	ecsHandOffTaskLifecycle(func(ctx context.Context) {
		start(ctx, id, td, taskTags, overrides, taskVolumeHosts, launchType, containerInstanceKey)
	}, ecsTaskStartBegins(id))
}

// ecsTaskStarts holds, for each task whose start is under way, a channel that
// closes once the start settles: its containers' handles are stored, or the
// start failed or was dropped.
var ecsTaskStarts sync.Map

// ecsTaskStartBegins records that the task's start is under way and returns
// what settles it.
func ecsTaskStartBegins(id string) func() {
	settled := make(chan struct{})
	ecsTaskStarts.Store(id, settled)
	return func() {
		ecsTaskStarts.CompareAndDelete(id, settled)
		close(settled)
	}
}

// ecsAwaitTaskStart waits for the task's start under way, if any, to settle,
// so a container that reads its metadata at once finds its task's containers.
func ecsAwaitTaskStart(ctx context.Context, id string) {
	v, ok := ecsTaskStarts.Load(id)
	if !ok {
		return
	}
	settled, ok := v.(chan struct{})
	if !ok {
		return
	}
	sim.DeclareOpenEndedWait(ctx)
	select {
	case <-settled:
	case <-ctx.Done():
	}
}

// ecsHandOffTaskLifecycle runs one step of a task's lifecycle off the request
// path, the way ecsScheduleTaskStart describes, and then calls after whether
// the step ran, was refused, or was dropped by a test drain. after must not
// read the control-plane stores: it can run once a drain has returned.
//
// work receives the server's background context and must return promptly once
// it is done, because a stopping simulator waits for every step. A step the
// shutdown interrupts leaves the task where the next process's recovery picks
// it up.
func ecsHandOffTaskLifecycle(work func(context.Context), after func()) {
	server := ecsBackgroundServer
	var workerCtx context.Context
	run, ok := bg.Handoff(func() { work(workerCtx) })
	if !ok {
		if after != nil {
			after()
		}
		return
	}
	if after != nil {
		handedOff := run
		run = func() {
			defer after()
			handedOff()
		}
	}
	if server == nil {
		workerCtx = context.Background()
		go run()
		return
	}
	server.StartBackground("ECS task lifecycle step", func(ctx context.Context) {
		workerCtx = ctx
		run()
	})
}

// errECSTaskStartInterrupted is a task start the simulator's shutdown cut
// short. It is not a task failure: the task stays PENDING for the next process.
var errECSTaskStartInterrupted = errors.New("task start interrupted by simulator shutdown")

func ecsLogInterruptedStart(taskID string) {
	fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: start interrupted by shutdown; the task stays PENDING for the next process to resume\n", taskID)
}

func ecsWatchTaskProcesses(taskID, containerInstanceKey string, processes *ecsTaskProcesses) {
	for _, handle := range processes.Handles {
		var result sim.ProcessResult
		_ = bg.WatchThen(func() { result = handle.Wait() }, func() {
			lifecycleLock := ecsTaskLifecycleLock(taskID)
			lifecycleLock.Lock()
			defer lifecycleLock.Unlock()
			owned, ownsLifecycle := ecsProcessHandles.LoadAndDelete(taskID)
			if !ownsLifecycle {
				return
			}
			ownedProcesses, ok := owned.(*ecsTaskProcesses)
			if !ok {
				panic("Amazon ECS task process registry contained a non-process value")
			}
			cleanupECSTaskProcesses(context.Background(), taskID, ownedProcesses)
			stoppedAt := ecsEpochSeconds()
			transitioned := false
			var doomedVolumes []string
			ecsTasks.Update(taskID, func(t *ECSTask) {
				if t.LastStatus == ECSTaskStatusStopped {
					return
				}
				transitioned = true
				t.LastStatus = ECSTaskStatusStopped
				t.StoppedAt = &stoppedAt
				// A container that exits while the task is already stopping
				// does not change why the task stopped: the stop request's
				// code and reason stand, as they do on Amazon ECS.
				if t.DesiredStatus != ECSTaskStatusStopped {
					t.DesiredStatus = ECSTaskStatusStopped
					t.StopCode = "EssentialContainerExited"
					t.StoppedReason = "Essential container in task exited"
				}
				exitCode := result.ExitCode
				for j := range t.Containers {
					t.Containers[j].LastStatus = "STOPPED"
					t.Containers[j].ExitCode = &exitCode
				}
				doomedVolumes = ecsReleaseTaskAttachments(t)
			})
			ecsDeleteManagedVolumes(doomedVolumes)
			if transitioned {
				ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, 0, -1)
				if task, ok := ecsTasks.Get(taskID); ok {
					ecsRequestServiceReconcileForTask(task)
				}
			}
		})
	}
}
