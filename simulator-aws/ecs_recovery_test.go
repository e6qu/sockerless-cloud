package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestRecoverECSTasksStopsLegacyRunningTasksWithoutWorkloadContainers(t *testing.T) {
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	ecsTaskDefinitions = sim.MakeStore[ECSTaskDefinition](nil, "ecs_task_definitions")
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")
	ecsContainerInstances = sim.MakeStore[ECSContainerInstance](nil, "ecs_container_instances")
	ec2Volumes = sim.MakeStore[EC2Volume](nil, "ec2_volumes")

	definition := ECSTaskDefinition{
		TaskDefinitionArn: ecsArn("task-definition", "desktop:1"),
		Family:            "desktop",
		Revision:          1,
		ContainerDefinitions: []ECSContainerDefinition{
			{Name: "desktop"},
		},
		Status: "ACTIVE",
	}
	ecsTaskDefinitions.Put("desktop:1", definition)

	containerInstanceARN := ecsArn("container-instance", "desktop-cluster/instance-1")
	instanceKey := ecsContainerInstanceKey("desktop-cluster", "instance-1")
	ecsContainerInstances.Put(instanceKey, ECSContainerInstance{
		ContainerInstanceArn: containerInstanceARN,
		ClusterName:          "desktop-cluster",
		RunningTasksCount:    2,
	})

	taskIDs := []string{"legacy-task-1", "legacy-task-2"}
	for _, taskID := range taskIDs {
		ecsTasks.Put(taskID, ECSTask{
			TaskArn:              ecsArn("task", "desktop-cluster/"+taskID),
			TaskDefinitionArn:    definition.TaskDefinitionArn,
			LastStatus:           ECSTaskStatusRunning,
			DesiredStatus:        ECSTaskStatusRunning,
			Connectivity:         "CONNECTED",
			ContainerInstanceArn: containerInstanceARN,
			Containers: []ECSTaskContainer{
				{Name: "desktop", LastStatus: "RUNNING"},
			},
		})
	}

	findCalls := 0
	err := recoverECSTasksWithContainerFinder(
		func(labels map[string]string) ([]sim.ExistingContainer, error) {
			findCalls++
			if labels["sockerless-sim-task"] == "" {
				t.Fatal("container lookup omitted the persisted task identity")
			}
			return nil, nil
		},
	)
	if err != nil {
		t.Fatalf("recover ECS tasks: %v", err)
	}
	if findCalls != len(taskIDs) {
		t.Fatalf("container lookups = %d, want %d", findCalls, len(taskIDs))
	}

	for _, taskID := range taskIDs {
		task, ok := ecsTasks.Get(taskID)
		if !ok {
			t.Fatalf("task %s missing after recovery", taskID)
		}
		if task.LastStatus != ECSTaskStatusStopped {
			t.Errorf("task %s LastStatus = %s, want STOPPED", taskID, task.LastStatus)
		}
		if task.DesiredStatus != ECSTaskStatusStopped {
			t.Errorf("task %s DesiredStatus = %s, want STOPPED", taskID, task.DesiredStatus)
		}
		if task.Connectivity != "" {
			t.Errorf("task %s Connectivity = %q, want empty", taskID, task.Connectivity)
		}
		if task.StopCode != "EssentialContainerExited" {
			t.Errorf("task %s StopCode = %q, want EssentialContainerExited", taskID, task.StopCode)
		}
		if task.StoppedReason != "Workload containers not found after control-plane restart" {
			t.Errorf("task %s StoppedReason = %q", taskID, task.StoppedReason)
		}
		if task.StoppedAt == nil {
			t.Errorf("task %s StoppedAt is nil", taskID)
		}
		if len(task.Containers) != 1 || task.Containers[0].LastStatus != "STOPPED" {
			t.Errorf("task %s containers = %#v, want one STOPPED container", taskID, task.Containers)
		} else if task.Containers[0].ExitCode == nil || *task.Containers[0].ExitCode != -1 {
			t.Errorf("task %s exit code = %v, want -1", taskID, task.Containers[0].ExitCode)
		}
	}

	instance, ok := ecsContainerInstances.Get(instanceKey)
	if !ok {
		t.Fatal("container instance missing after recovery")
	}
	if instance.RunningTasksCount != 0 {
		t.Errorf("container instance running task count = %d, want 0", instance.RunningTasksCount)
	}
}

func TestRecoverECSTasksFailsOnWorkloadDiscoveryError(t *testing.T) {
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	ecsTaskDefinitions = sim.MakeStore[ECSTaskDefinition](nil, "ecs_task_definitions")
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")

	definition := ECSTaskDefinition{
		TaskDefinitionArn: ecsArn("task-definition", "desktop:1"),
		ContainerDefinitions: []ECSContainerDefinition{
			{Name: "desktop"},
		},
	}
	ecsTaskDefinitions.Put("desktop:1", definition)
	taskID := "uninspectable-task"
	ecsTasks.Put(taskID, ECSTask{
		TaskArn:           ecsArn("task", "desktop-cluster/"+taskID),
		TaskDefinitionArn: definition.TaskDefinitionArn,
		LastStatus:        ECSTaskStatusRunning,
		DesiredStatus:     ECSTaskStatusRunning,
	})

	discoveryErr := errors.New("runtime unavailable")
	err := recoverECSTasksWithContainerFinder(
		func(map[string]string) ([]sim.ExistingContainer, error) {
			return nil, discoveryErr
		},
	)
	if err == nil {
		t.Fatal("recover ECS tasks succeeded after workload discovery failed")
	}
	if !errors.Is(err, discoveryErr) {
		t.Fatalf("recover ECS tasks error = %v, want wrapped discovery error", err)
	}
	if !strings.Contains(err.Error(), "find Amazon ECS task") {
		t.Fatalf("recover ECS tasks error = %q, want task discovery context", err)
	}

	task, ok := ecsTasks.Get(taskID)
	if !ok {
		t.Fatal("task missing after failed recovery")
	}
	if task.LastStatus != ECSTaskStatusRunning {
		t.Errorf("task LastStatus = %s, want RUNNING after discovery error", task.LastStatus)
	}
}
