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
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// ecsTaskLogSinks holds each container's awslogs sink, keyed by container
// name; a container without the awslogs driver writes nowhere.
type ecsTaskLogSinks map[string]sim.LogSink

func (s ecsTaskLogSinks) For(container string) sim.LogSink {
	if sink, ok := s[container]; ok {
		return sink
	}
	return discardLogSink{}
}

// Create each container's log stream, <prefix>/<container name>/<task ID>,
// before the containers start so it exists once the task is RUNNING. The
// awslogs driver creates the group only with awslogs-create-group "true"; a
// missing group fails the start with ResourceInitializationError.
func ecsTaskCloudWatchSink(td ECSTaskDefinition, taskID string) (ecsTaskLogSinks, error) {
	sinks := ecsTaskLogSinks{}
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

		logStreamName := fmt.Sprintf("%s/%s/%s", streamPrefix, cd.Name, taskID)
		key := cwEventsKey(logGroup, logStreamName)
		if _, exists := cwLogStreams.Get(key); !exists {
			// Event timestamps stay unset until the container writes; the sink stamps them
			// on the first line.
			cwLogStreams.Put(key, CWLogStream{
				LogStreamName:       logStreamName,
				LogGroupName:        logGroup,
				CreationTime:        nowMs,
				Arn:                 cwLogStreamArn(logGroup, logStreamName),
				UploadSequenceToken: "1",
			})
			cwLogEvents.Open(key)
		}
		sinks[cd.Name] = &cwLogSink{logGroup: logGroup, logStream: logStreamName}
	}
	return sinks, nil
}

// The service scheduler launches through the same path as RunTask.
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

// The overrides' roles, else the task definition's.
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

// A role must exist and trust ecs-tasks.amazonaws.com; Amazon ECS checks at
// launch, not at RegisterTaskDefinition.
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
	// RunTask launches at most 10 tasks per call.
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

// A task the host cannot fit comes back as a `failures[]` entry (never nil:
// the wire shape is `[]`) while the rest launch, as Amazon ECS places what it
// can. RunTask, StartTask and the service scheduler share this.
func runECSTasks(ctx context.Context, in ecsRunTaskInput) ([]ECSTask, []ecsFailure, *ecsRequestError) {
	if in.Cluster == "" {
		in.Cluster = "default"
	}

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

	tdKey := ecsTaskDefinitionKey(in.TaskDefinition)
	td, ok := ecsTaskDefinitions.Get(tdKey)
	if !ok {
		return nil, nil, &ecsRequestError{"ClientException",
			fmt.Sprintf("Unable to describe task definition: %s", in.TaskDefinition), http.StatusBadRequest}
	}

	// Security groups must exist.
	if in.NetworkConfiguration != nil && in.NetworkConfiguration.AwsvpcConfiguration != nil {
		for _, sgID := range in.NetworkConfiguration.AwsvpcConfiguration.SecurityGroups {
			if _, sgOK := ec2SecurityGroups.Get(sgID); !sgOK {
				return nil, nil, &ecsRequestError{"InvalidParameterException",
					fmt.Sprintf("The security group '%s' does not exist", sgID), http.StatusBadRequest}
			}
		}
	}

	// awsvpc requires networkConfiguration, which allocates the task's ENI; other
	// network modes share the instance's networking and refuse it.
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

	// An unknown subnet is an InvalidParameterException; its CIDR supplies the
	// task's address.
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

		// Nothing below is allocated for a task the host cannot fit.
		if short, committed, ceiling, fits := ecsReservePlacement(taskID, requested); !fits {
			failures = append(failures, ecsPlacementFailure(in, cluster.ClusterArn, short, requested, committed, ceiling))
			continue
		}
		// Return the reservation on every path that leaves without storing the task,
		// panics included.
		placed := false
		defer func(id string) {
			if !placed {
				ecsAbandonPlacement(id)
			}
		}(taskID)

		// Only awsvpc tasks get an ENI and per-container networkInterfaces.
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

		// Request tags win over those inherited from the task definition.
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

// RUNNING is reported only once container handles are stored, so
// ExecuteCommand and task metadata find the containers. It reads durable
// stores, so it runs under the server lifecycle and drains before SQLite
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

	ecsTasks.Update(id, func(t *ECSTask) {
		ecsResetTaskContainers(t)
		for j := range t.Attachments {
			if t.Attachments[j].Type == "ElasticNetworkInterface" {
				t.Attachments[j].Status = "ATTACHING"
			}
		}
	})
	phases := newECSPhaseTimer(time.Now)

	// A hydration failure is a resource problem: stop the task with
	// TaskFailedToStart and a ResourceInitializationError reason, as Amazon ECS
	// does.
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

	// The log group and stream exist once the task reports RUNNING.
	sink, err := ecsTaskCloudWatchSink(td, id)
	phases.Mark("log-stream")

	var processes *ecsTaskProcesses
	if err == nil {
		processes, err = startECSTaskContainers(ctx, id, td, taskTags, overrides, taskVolumeHosts, sink, launchType, containerInstanceKey, phases)
	}
	if errors.Is(err, errECSTaskStartInterrupted) {
		ecsLogInterruptedStart(id)
		return
	}
	if errors.Is(err, errECSTaskStartPreempted) {
		return
	}
	if err != nil {
		// Log the cause; otherwise only StoppedReason records it.
		fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: container start failed: %v\n", id, err)
		ecsStopFailedStart(id, containerInstanceKey, "Container start failed: ", err)
		return
	}

	if ecsMarkTaskRunning(id, processes) {
		// The pull is the only phase the API exposes, and rarely the slow one.
		fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: RUNNING %s\n", id, phases.Summary())
		ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 1)
	}
	if task, ok := ecsTasks.Get(id); ok {
		ecsRequestServiceReconcileForTask(task)
	}
}

// Every container starts PENDING, with no exit code or health yet.
func ecsResetTaskContainers(t *ECSTask) {
	t.LastStatus = ECSTaskStatusPending
	for j := range t.Containers {
		t.Containers[j].LastStatus = "PENDING"
		t.Containers[j].ExitCode = nil
		t.Containers[j].HealthStatus = ""
	}
	t.HealthStatus = ""
}

// A task is RUNNING once each container has started or exited, as the agent
// reports a task at the earliest state of its containers. A task already
// stopping stays where it is, for its stop to finish.
func ecsMarkTaskRunning(id string, processes *ecsTaskProcesses) bool {
	var essential map[string]bool
	if processes != nil {
		essential = processes.Essential
	}
	now := ecsEpochSeconds()
	running := false
	ecsTasks.Update(id, func(t *ECSTask) {
		if ecsTaskStartPreempted(*t, essential) {
			return
		}
		running = true
		t.LastStatus = ECSTaskStatusRunning
		t.Connectivity = "CONNECTED"
		t.StartedAt = &now
		for j := range t.Attachments {
			t.Attachments[j].Status = "ATTACHED"
		}
	})
	return running
}

// A start that failed stops the task. Each container the start placed keeps
// how it exited; one that never started has no exit code, except after a
// placement failure, which marks every unplaced container -1.
func ecsStopFailedStart(id, containerInstanceKey, reasonPrefix string, err error) {
	stoppedAt := ecsEpochSeconds()
	var doomedVolumes []string
	var resourceErr *ecsResourceInitializationError
	resourceFailure := errors.As(err, &resourceErr)
	var startErr *ecsStartFailure
	errors.As(err, &startErr)
	var dependencyErr *ecsDependencyError
	dependencyFailure := errors.As(err, &dependencyErr)
	ecsTasks.Update(id, func(t *ECSTask) {
		t.LastStatus = ECSTaskStatusStopped
		t.DesiredStatus = ECSTaskStatusStopped
		t.StoppedAt = &stoppedAt
		t.StopCode = "EssentialContainerExited"
		t.StoppedReason = reasonPrefix + err.Error()
		if resourceFailure {
			t.StopCode = "TaskFailedToStart"
			t.StoppedReason = resourceErr.Error()
		}
		for j := range t.Containers {
			c := &t.Containers[j]
			c.LastStatus = "STOPPED"
			if c.ExitCode != nil || resourceFailure {
				continue
			}
			if startErr != nil {
				if code, ok := startErr.exitCodes[c.Name]; ok {
					c.ExitCode = &code
					continue
				}
			}
			if dependencyFailure {
				continue
			}
			exitCode := -1
			c.ExitCode = &exitCode
		}
		doomedVolumes = ecsReleaseTaskAttachments(t)
	})
	ecsDeleteManagedVolumes(doomedVolumes)
	ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 0)
	if task, ok := ecsTasks.Get(id); ok {
		ecsRequestServiceReconcileForTask(task)
	}
}

// Runs under the server's background-worker lifecycle when registered, so
// shutdown drains it; package tests without a server get a goroutine. Either
// way the start counts for the test drain barrier before hand-over.
func ecsScheduleTaskStart(
	start func(context.Context, string, ECSTaskDefinition, []ECSTag, *ECSTaskOverride, map[string]string, string, string),
	id string, td ECSTaskDefinition, taskTags []ECSTag, overrides *ECSTaskOverride,
	taskVolumeHosts map[string]string, launchType, containerInstanceKey string,
) {
	ecsHandOffTaskLifecycle(func(ctx context.Context) {
		start(ctx, id, td, taskTags, overrides, taskVolumeHosts, launchType, containerInstanceKey)
	}, ecsTaskStartBegins(id))
}

// Closes once the start settles: handles stored, or the start failed or was
// dropped.
var ecsTaskStarts sync.Map

// Returns what settles the start.
func ecsTaskStartBegins(id string) func() {
	settled := make(chan struct{})
	ecsTaskStarts.Store(id, settled)
	return func() {
		ecsTaskStarts.CompareAndDelete(id, settled)
		close(settled)
	}
}

// A container reading its metadata at once may arrive before its handle is
// stored; wait for that container alone, not the task's later containers.
func ecsAwaitContainerPlaced(ctx context.Context, taskID, container string) {
	declared := false
	for {
		placed := ecsContainerPlacements.Changed(taskID)
		if dockerID, _ := ecsTaskDockerInfo(taskID, container); dockerID != "" {
			return
		}
		v, ok := ecsTaskStarts.Load(taskID)
		if !ok {
			return
		}
		settled, ok := v.(chan struct{})
		if !ok {
			return
		}
		if !declared {
			sim.DeclareOpenEndedWait(ctx)
			declared = true
		}
		select {
		case <-placed:
		case <-settled:
			return
		case <-ctx.Done():
			return
		}
	}
}

// after runs whether the step ran, was refused or was dropped by a test drain,
// and must not read control-plane stores. work must return promptly once done;
// a step shutdown interrupts is left for the next process's recovery.
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

// Not a task failure: the task stays PENDING for the next process.
var errECSTaskStartInterrupted = errors.New("task start interrupted by simulator shutdown")

func ecsLogInterruptedStart(taskID string) {
	fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: start interrupted by shutdown; the task stays PENDING for the next process to resume\n", taskID)
}

func ecsWatchTaskProcesses(taskID, containerInstanceKey string, processes *ecsTaskProcesses) {
	for name, handle := range processes.named() {
		ecsWatchTaskContainer(taskID, containerInstanceKey, processes, name, handle)
	}
}

// A non-essential container that exits stops alone, keeping its exit code; an
// essential one stops the task. A container stopped by the task's own stop
// records nothing here: the stop does.
func ecsWatchTaskContainer(taskID, containerInstanceKey string, processes *ecsTaskProcesses, name string, handle placement.Handle) {
	var result sim.ProcessResult
	_ = bg.WatchThen(func() { result = handle.Wait() }, func() {
		exitCode := result.ExitCode
		ecsTasks.Update(taskID, func(t *ECSTask) {
			if t.LastStatus == ECSTaskStatusStopped || t.DesiredStatus == ECSTaskStatusStopped {
				return
			}
			for j := range t.Containers {
				if t.Containers[j].Name == name {
					t.Containers[j].LastStatus = "STOPPED"
					t.Containers[j].ExitCode = &exitCode
				}
			}
		})
		if processes.Essential[name] {
			ecsStopTaskForEssentialExit(taskID, containerInstanceKey)
		}
	})
}

func ecsStopTaskForEssentialExit(taskID, containerInstanceKey string) {
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
	prior, ok := ecsTasks.Get(taskID)
	if !ok || prior.LastStatus == ECSTaskStatusStopped {
		return
	}
	// A container exiting while the task stops does not change the stop's code
	// and reason.
	ecsTasks.Update(taskID, func(t *ECSTask) {
		if t.DesiredStatus != ECSTaskStatusStopped {
			t.DesiredStatus = ECSTaskStatusStopped
			t.StopCode = "EssentialContainerExited"
			t.StoppedReason = "Essential container in task exited"
		}
	})
	cleanupECSTaskProcesses(context.Background(), taskID, ownedProcesses)
	if ecsMarkTaskStopped(taskID, ownedProcesses) {
		ecsReleaseTaskCount(containerInstanceKey, prior.LastStatus)
		if task, ok := ecsTasks.Get(taskID); ok {
			ecsRequestServiceReconcileForTask(task)
		}
	}
}

// Every container stops. One that already exited keeps its exit code, one the
// stop ended reports how it exited, and one that never started has none.
// Call it once processes' containers have exited.
func ecsMarkTaskStopped(taskID string, processes *ecsTaskProcesses) bool {
	exitCodes := map[string]int{}
	for name, handle := range processes.named() {
		if handle != nil {
			exitCodes[name] = handle.Wait().ExitCode
		}
	}
	stoppedAt := ecsEpochSeconds()
	transitioned := false
	var doomedVolumes []string
	ecsTasks.Update(taskID, func(t *ECSTask) {
		if t.LastStatus == ECSTaskStatusStopped {
			return
		}
		transitioned = true
		t.LastStatus = ECSTaskStatusStopped
		t.DesiredStatus = ECSTaskStatusStopped
		t.StoppedAt = &stoppedAt
		for j := range t.Containers {
			c := &t.Containers[j]
			wasRunning := c.LastStatus == "RUNNING"
			c.LastStatus = "STOPPED"
			if c.ExitCode != nil {
				continue
			}
			if exitCode, ok := exitCodes[c.Name]; ok {
				c.ExitCode = &exitCode
				continue
			}
			// A running container whose handle this process does not hold was killed
			// after its stop timeout: 128+SIGKILL.
			if wasRunning {
				killed := 137
				c.ExitCode = &killed
			}
		}
		doomedVolumes = ecsReleaseTaskAttachments(t)
	})
	ecsDeleteManagedVolumes(doomedVolumes)
	return transitioned
}

// A stopped task leaves its container instance's pending or running count.
func ecsReleaseTaskCount(containerInstanceKey string, prior ECSTaskStatus) {
	switch prior {
	case ECSTaskStatusProvisioning, ECSTaskStatusPending:
		ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 0)
	case ECSTaskStatusRunning:
		ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, 0, -1)
	}
}
