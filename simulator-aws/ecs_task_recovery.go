package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

func recoverECSTasks() error {
	return recoverECSTasksWithContainerFinder(ecsFindExistingWorkloads)
}

// ecsFindExistingWorkloads lists the workloads of a task an earlier process
// placed, on the backend the task's launch type places on.
func ecsFindExistingWorkloads(task ECSTask, labels map[string]string) ([]placement.Existing, error) {
	backend, err := awsPlacement.For(ecsPlacementService(task.LaunchType))
	if err != nil {
		return nil, err
	}
	return backend.Find(context.Background(), labels)
}

// ecsWorkloadFinder lists the workloads carrying every label that an earlier
// process placed for task.
type ecsWorkloadFinder func(task ECSTask, labels map[string]string) ([]placement.Existing, error)

func recoverECSTasksWithContainerFinder(findExistingContainers ecsWorkloadFinder) error {
	for _, task := range ecsTasks.List() {
		if task.LastStatus != ECSTaskStatusStopped && task.DesiredStatus == ECSTaskStatusStopped {
			if err := ecsRecoverStoppingTask(task, findExistingContainers); err != nil {
				return err
			}
			continue
		}
		switch task.LastStatus {
		case ECSTaskStatusProvisioning, ECSTaskStatusPending:
			definition, ok := ecsTaskDefinitionForARN(task.TaskDefinitionArn)
			if !ok {
				return fmt.Errorf("task %s references missing task definition %s", task.TaskArn, task.TaskDefinitionArn)
			}
			ecsHandOffTaskLifecycle(func(ctx context.Context) { ecsResumePendingTask(ctx, task, definition) }, nil)
		case ECSTaskStatusRunning:
			definition, ok := ecsTaskDefinitionForARN(task.TaskDefinitionArn)
			if !ok {
				return fmt.Errorf("task %s references missing task definition %s", task.TaskArn, task.TaskDefinitionArn)
			}
			if err := ecsRecoverRunningTask(task, definition, findExistingContainers); err != nil {
				return err
			}
		}
	}
	return nil
}

func ecsTaskDefinitionForARN(arn string) (ECSTaskDefinition, bool) {
	key := arn
	if separator := strings.LastIndexByte(key, '/'); separator >= 0 {
		key = key[separator+1:]
	}
	return ecsTaskDefinitions.Get(key)
}

func ecsResumePendingTask(ctx context.Context, task ECSTask, definition ECSTaskDefinition) {
	taskID := task.TaskID()
	lifecycleLock := ecsTaskLifecycleLock(taskID)
	if !lifecycleLock.LockContext(ctx) {
		return
	}
	defer lifecycleLock.Unlock()
	if current, exists := ecsTasks.Get(taskID); !exists || current.DesiredStatus == ECSTaskStatusStopped {
		return
	}
	ecsTasks.Update(taskID, func(current *ECSTask) {
		current.LastStatus = ECSTaskStatusPending
		for index := range current.Containers {
			current.Containers[index].LastStatus = "PENDING"
		}
	})
	phases := newECSPhaseTimer(time.Now)
	sink, err := ecsTaskCloudWatchSink(definition, taskID)
	phases.Mark("log-stream")
	var processes *ecsTaskProcesses
	if err == nil {
		processes, err = startECSTaskContainers(
			ctx,
			taskID,
			definition,
			task.Tags,
			task.Overrides,
			task.VolumeHosts,
			sink,
			task.LaunchType,
			phases,
		)
	}
	if errors.Is(err, errECSTaskStartInterrupted) {
		ecsLogInterruptedStart(taskID)
		return
	}
	containerInstanceKey := ecsContainerInstanceKeyFromARN(task.ContainerInstanceArn)
	if err != nil {
		stoppedAt := ecsEpochSeconds()
		var doomedVolumes []string
		var resourceErr *ecsResourceInitializationError
		resourceFailure := errors.As(err, &resourceErr)
		ecsTasks.Update(taskID, func(current *ECSTask) {
			current.LastStatus = ECSTaskStatusStopped
			current.DesiredStatus = ECSTaskStatusStopped
			current.StoppedAt = &stoppedAt
			current.StopCode = "EssentialContainerExited"
			current.StoppedReason = fmt.Sprintf("Container start failed after control-plane restart: %v", err)
			if resourceFailure {
				current.StopCode = "TaskFailedToStart"
				current.StoppedReason = resourceErr.Error()
			}
			exitCode := -1
			for index := range current.Containers {
				current.Containers[index].LastStatus = "STOPPED"
				if !resourceFailure {
					current.Containers[index].ExitCode = &exitCode
				}
			}
			doomedVolumes = ecsReleaseTaskAttachments(current)
		})
		ecsDeleteManagedVolumes(doomedVolumes)
		ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 0)
		if current, ok := ecsTasks.Get(taskID); ok {
			ecsRequestServiceReconcileForTask(current)
		}
		return
	}
	startedAt := ecsEpochSeconds()
	fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: RUNNING (resumed after restart) %s\n", taskID, phases.Summary())
	if processes != nil {
		ecsProcessHandles.Store(taskID, processes)
	}
	ecsTasks.Update(taskID, func(current *ECSTask) {
		current.LastStatus = ECSTaskStatusRunning
		current.Connectivity = "CONNECTED"
		current.StartedAt = &startedAt
		for index := range current.Containers {
			current.Containers[index].LastStatus = "RUNNING"
		}
		for index := range current.Attachments {
			current.Attachments[index].Status = "ATTACHED"
		}
	})
	ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 1)
	if processes != nil {
		ecsWatchTaskProcesses(taskID, containerInstanceKey, processes)
	}
	if current, ok := ecsTasks.Get(taskID); ok {
		ecsRequestServiceReconcileForTask(current)
	}
}

// ecsRecoverStoppingTask finishes a stop that the previous simulator process
// accepted and did not complete. StopTask answers before the task's containers
// have stopped, so a restart inside a container's stop timeout leaves a task
// whose desired status is STOPPED and whose last status is not. Resuming or
// adopting it as a running task would keep a workload the caller asked to
// stop; whatever of it is still on the host is adopted only so that the stop
// can take it down.
func ecsRecoverStoppingTask(task ECSTask, findExistingContainers ecsWorkloadFinder) error {
	taskID := task.TaskID()
	existing, err := findExistingContainers(task, map[string]string{"sockerless-sim-task": taskID})
	if err != nil {
		return fmt.Errorf("find Amazon ECS task %s containers: %w", task.TaskArn, err)
	}
	if len(existing) > 0 {
		definition, ok := ecsTaskDefinitionForARN(task.TaskDefinitionArn)
		if !ok {
			return fmt.Errorf("task %s references missing task definition %s", task.TaskArn, task.TaskDefinitionArn)
		}
		if len(definition.ContainerDefinitions) == 0 {
			return fmt.Errorf("task %s references an Amazon ECS task definition without containers", task.TaskArn)
		}
		if err := ecsAdoptRunningTask(task, definition, existing); err != nil {
			return err
		}
	}
	stopECSTask(taskID, task.StoppedReason, task.StopCode)
	return nil
}

func ecsRecoverRunningTask(task ECSTask, definition ECSTaskDefinition, findExistingContainers ecsWorkloadFinder) error {
	if len(definition.ContainerDefinitions) == 0 {
		return fmt.Errorf("task %s references an Amazon ECS task definition without containers", task.TaskArn)
	}
	taskID := task.TaskID()
	existing, err := findExistingContainers(task, map[string]string{"sockerless-sim-task": taskID})
	if err != nil {
		return fmt.Errorf("find Amazon ECS task %s containers: %w", task.TaskArn, err)
	}
	if len(existing) == 0 {
		ecsMarkMissingRunningTaskStopped(task)
		return nil
	}
	return ecsAdoptRunningTask(task, definition, existing)
}

func ecsMarkMissingRunningTaskStopped(task ECSTask) {
	taskID := task.TaskID()
	stoppedAt := ecsEpochSeconds()
	transitioned := false
	var doomedVolumes []string
	ecsTasks.Update(taskID, func(current *ECSTask) {
		if current.LastStatus != ECSTaskStatusRunning {
			return
		}
		transitioned = true
		current.LastStatus = ECSTaskStatusStopped
		current.DesiredStatus = ECSTaskStatusStopped
		current.Connectivity = ""
		current.StoppedAt = &stoppedAt
		current.StopCode = "EssentialContainerExited"
		current.StoppedReason = "Workload containers not found after control-plane restart"
		exitCode := -1
		for index := range current.Containers {
			current.Containers[index].LastStatus = "STOPPED"
			current.Containers[index].ExitCode = &exitCode
		}
		doomedVolumes = ecsReleaseTaskAttachments(current)
	})
	ecsDeleteManagedVolumes(doomedVolumes)
	if !transitioned {
		return
	}
	fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: workload containers not found after control-plane restart; transitioned task to STOPPED\n", taskID)
	ecsUpdateContainerInstanceTaskCounts(
		ecsContainerInstanceKeyFromARN(task.ContainerInstanceArn),
		0,
		-1,
	)
	ecsRequestServiceReconcileForTask(task)
}

func ecsAdoptRunningTask(task ECSTask, definition ECSTaskDefinition, existing []placement.Existing) error {
	taskID := task.TaskID()
	processes := &ecsTaskProcesses{
		MainContainerName: definition.ContainerDefinitions[0].Name,
		Handles:           make(map[string]placement.Handle, len(existing)),
		StopGrace:         ecsTaskStopGrace(definition),
	}
	sink, err := ecsTaskCloudWatchSink(definition, taskID)
	if err != nil {
		return fmt.Errorf("adopt Amazon ECS task %s: %w", task.TaskArn, err)
	}
	backend, err := awsPlacement.For(ecsPlacementService(task.LaunchType))
	if err != nil {
		return fmt.Errorf("adopt Amazon ECS task %s: %w", task.TaskArn, err)
	}
	for _, container := range existing {
		name := container.Labels["sockerless-sim-task-container"]
		if name == "" {
			return fmt.Errorf("task %s Amazon ECS container %s has no container identity label", task.TaskArn, container.ID)
		}
		handle, err := backend.Adopt(context.Background(), container.ID, sim.ContainerConfig{CancelGracePeriod: processes.StopGrace[name]}, sink)
		if err != nil {
			return fmt.Errorf("adopt Amazon ECS task %s container %s: %w", task.TaskArn, name, err)
		}
		processes.Handles[name] = handle
	}
	ecsProcessHandles.Store(taskID, processes)
	ecsWatchTaskProcesses(taskID, ecsContainerInstanceKeyFromARN(task.ContainerInstanceArn), processes)
	return nil
}
