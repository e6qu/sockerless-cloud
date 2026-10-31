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

// On the backend the task's launch type places on.
func ecsFindExistingWorkloads(task ECSTask, labels map[string]string) ([]placement.Existing, error) {
	backend, err := awsPlacement.For(ecsPlacementService(task.LaunchType))
	if err != nil {
		return nil, err
	}
	return backend.Find(context.Background(), labels)
}

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
	ecsTasks.Update(taskID, ecsResetTaskContainers)
	phases := newECSPhaseTimer(time.Now)
	sink, err := ecsTaskCloudWatchSink(definition, taskID)
	phases.Mark("log-stream")
	containerInstanceKey := ecsContainerInstanceKeyFromARN(task.ContainerInstanceArn)
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
			containerInstanceKey,
			phases,
		)
	}
	if errors.Is(err, errECSTaskStartInterrupted) {
		ecsLogInterruptedStart(taskID)
		return
	}
	if errors.Is(err, errECSTaskStartPreempted) {
		return
	}
	if err != nil {
		ecsStopFailedStart(taskID, containerInstanceKey, "Container start failed after control-plane restart: ", err)
		return
	}
	if ecsMarkTaskRunning(taskID, processes) {
		fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: RUNNING (resumed after restart) %s\n", taskID, phases.Summary())
		ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 1)
	}
	if current, ok := ecsTasks.Get(taskID); ok {
		ecsRequestServiceReconcileForTask(current)
	}
}

// A restart inside a stop timeout leaves a task desired STOPPED but not yet
// stopped; adopt what remains only so the stop can take it down.
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
	// The first container in definition order still running owns the namespace.
	adopted := map[string]bool{}
	for _, container := range existing {
		adopted[container.Labels["sockerless-sim-task-container"]] = true
	}
	mainName := definition.ContainerDefinitions[0].Name
	for _, cd := range definition.ContainerDefinitions {
		if adopted[cd.Name] {
			mainName = cd.Name
			break
		}
	}
	processes := newECSTaskProcesses(definition, mainName)
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
		handle, err := backend.Adopt(context.Background(), container.ID, sim.ContainerConfig{CancelGracePeriod: processes.StopGrace[name]}, sink.For(name))
		if err != nil {
			return fmt.Errorf("adopt Amazon ECS task %s container %s: %w", task.TaskArn, name, err)
		}
		processes.setHandle(name, handle)
		for _, cd := range definition.ContainerDefinitions {
			if cd.Name == name {
				ecsMonitorContainerHealth(taskID, cd, handle)
			}
		}
	}
	ecsProcessHandles.Store(taskID, processes)
	ecsWatchTaskProcesses(taskID, ecsContainerInstanceKeyFromARN(task.ContainerInstanceArn), processes)
	return nil
}
