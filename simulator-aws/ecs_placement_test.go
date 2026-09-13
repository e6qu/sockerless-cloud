package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/stretchr/testify/require"
)

// A host that fits exactly one 2048 MiB / 1024 CPU-unit task beside a 2048 /
// 1024 one already running.
func placementHostForTest(t *testing.T) {
	t.Helper()
	AwaitSimulatorBackground()
	ecsClusters = sim.MakeStore[ECSCluster](nil, "ecs_clusters")
	ecsTaskDefinitions = sim.MakeStore[ECSTaskDefinition](nil, "ecs_task_definitions")
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")
	ecsPlacementMu.Lock()
	ecsPlacementInFlight = map[string]ecsTaskSize{}
	ecsPlacementMu.Unlock()
	previous := ecsPlacementCeiling
	ecsPlacementCeiling = func() ecsTaskSize { return ecsTaskSize{MemoryMiB: 4096, CPUUnits: 2048} }
	t.Cleanup(func() { ecsPlacementCeiling = previous })

	ecsClusters.Put("default", ECSCluster{ClusterArn: ecsArn("cluster", "default"), ClusterName: "default", Status: "ACTIVE"})
	ecsTaskDefinitions.Put("fits:1", ECSTaskDefinition{
		TaskDefinitionArn:    "arn:aws:ecs:us-east-1:000000000000:task-definition/fits:1",
		Family:               "fits",
		Revision:             1,
		Cpu:                  "1024",
		Memory:               "2048",
		NetworkMode:          "bridge",
		ContainerDefinitions: []ECSContainerDefinition{{Name: "app", Image: "busybox"}},
	})
	// Sized only per container, the way EC2 task definitions usually are.
	ecsTaskDefinitions.Put("percontainer:1", ECSTaskDefinition{
		TaskDefinitionArn: "arn:aws:ecs:us-east-1:000000000000:task-definition/percontainer:1",
		Family:            "percontainer",
		Revision:          1,
		NetworkMode:       "bridge",
		ContainerDefinitions: []ECSContainerDefinition{
			{Name: "app", Image: "busybox", Memory: 1536, Cpu: 512},
			{Name: "side", Image: "busybox", MemoryReservation: 512, Cpu: 512},
		},
	})
	ecsTaskDefinitions.Put("fargate:1", ECSTaskDefinition{
		TaskDefinitionArn:    "arn:aws:ecs:us-east-1:000000000000:task-definition/fargate:1",
		Family:               "fargate",
		Revision:             1,
		Cpu:                  "1024",
		Memory:               "2048",
		NetworkMode:          "awsvpc",
		ContainerDefinitions: []ECSContainerDefinition{{Name: "app", Image: "busybox"}},
	})
	ecsTasks.Put("held", ECSTask{
		TaskArn:           "arn:aws:ecs:us-east-1:000000000000:task/default/held",
		TaskDefinitionArn: "arn:aws:ecs:us-east-1:000000000000:task-definition/fits:1",
		ClusterArn:        ecsArn("cluster", "default"),
		LastStatus:        ECSTaskStatusRunning,
		DesiredStatus:     ECSTaskStatusRunning,
		Cpu:               "1024",
		Memory:            "2048",
	})
}

func TestRequestedTaskSizeSumsContainersWhenTheTaskHasNoSize(t *testing.T) {
	placementHostForTest(t)
	td, _ := ecsTaskDefinitions.Get("percontainer:1")
	require.Equal(t, ecsTaskSize{MemoryMiB: 2048, CPUUnits: 1024}, ecsRequestedTaskSize(td, nil))
	fits, _ := ecsTaskDefinitions.Get("fits:1")
	require.Equal(t, ecsTaskSize{MemoryMiB: 1024, CPUUnits: 256}, ecsRequestedTaskSize(fits, &ECSTaskOverride{Cpu: "256", Memory: "1024"}),
		"request overrides replace the task-level size")
}

func TestPlacementLedgerCountsStoredAndInFlightTasksUntilStopped(t *testing.T) {
	placementHostForTest(t)
	one := ecsTaskSize{MemoryMiB: 2048, CPUUnits: 1024}

	short, committed, ceiling, ok := ecsReservePlacement("second", one)
	require.True(t, ok, "the host has room for one more task beside the held one")
	require.Equal(t, "", short)
	require.Equal(t, one, committed, "the held task is the whole commitment before this one")
	require.Equal(t, ecsTaskSize{MemoryMiB: 4096, CPUUnits: 2048}, ceiling)

	short, committed, _, ok = ecsReservePlacement("third", one)
	require.False(t, ok, "a reservation that is in flight counts as much as a stored task")
	require.Equal(t, "MEMORY", short)
	require.Equal(t, ecsTaskSize{MemoryMiB: 4096, CPUUnits: 2048}, committed)

	ecsAbandonPlacement("second")
	_, _, _, ok = ecsReservePlacement("third", one)
	require.True(t, ok, "an abandoned reservation is given back")
	ecsCommitPlacement("third", ECSTask{TaskArn: "arn:third", LastStatus: ECSTaskStatusProvisioning, Cpu: "1024", Memory: "2048"})

	_, _, _, ok = ecsReservePlacement("fourth", ecsTaskSize{MemoryMiB: 1, CPUUnits: 1})
	require.False(t, ok, "a stored task holds its commitment while it is not STOPPED")

	ecsTasks.Update("held", func(task *ECSTask) { task.LastStatus = ECSTaskStatusStopped })
	_, _, _, ok = ecsReservePlacement("fourth", one)
	require.True(t, ok, "a STOPPED task releases what it held")

	// CPU runs out before memory does.
	ecsAbandonPlacement("fourth")
	ecsTasks.Update("third", func(task *ECSTask) { task.Cpu = "2048"; task.Memory = "1" })
	short, _, _, ok = ecsReservePlacement("fifth", ecsTaskSize{MemoryMiB: 1, CPUUnits: 1})
	require.False(t, ok)
	require.Equal(t, "CPU", short)
}

func TestRunTaskRefusesPlacementWithTheRealShape(t *testing.T) {
	placementHostForTest(t)
	// Fill the host: the held task plus one more.
	ecsTasks.Put("held2", ECSTask{TaskArn: "arn:held2", LastStatus: ECSTaskStatusPending, Cpu: "1024", Memory: "2048"})

	// A Fargate request needs awsvpc; the bridge definition above is refused
	// for that before placement, so drive the EC2 path through the handler and
	// the Fargate shape through runECSTasks directly.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"cluster":"default","taskDefinition":"fits:1","count":2}`))
	rec := httptest.NewRecorder()
	handleECSRunTask(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "placement refusal is a 200 with failures, not an error: %s", rec.Body.String())
	var out struct {
		Tasks    []json.RawMessage `json:"tasks"`
		Failures []ecsFailure      `json:"failures"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Empty(t, out.Tasks)
	require.Len(t, out.Failures, 2, "every task in the count that did not fit is a failure")
	require.Equal(t, ecsArn("cluster", "default"), out.Failures[0].Arn)
	require.Equal(t, "RESOURCE:MEMORY", out.Failures[0].Reason)
	require.Contains(t, out.Failures[0].Detail, "this task needs 2048 MiB and 1024 CPU units")

	// Placement is decided before the ENI is allocated, so a refused Fargate
	// task never needs the subnet to exist.
	fargate, failures, rerr := runECSTasks(context.Background(), ecsRunTaskInput{
		Cluster: "default", TaskDefinition: "fargate:1", Count: 1, LaunchType: "FARGATE",
		NetworkConfiguration: &ECSTaskNetworkConfig{AwsvpcConfiguration: &ECSTaskVpcConfig{Subnets: []string{"subnet-unplaced"}}},
	})
	require.Nil(t, rerr)
	require.Empty(t, fargate)
	require.Len(t, failures, 1)
	require.Equal(t, ecsFargateCapacityUnavailable, failures[0].Reason)
	require.Equal(t, ecsArn("cluster", "default"), failures[0].Arn)

	// An empty failures list is `[]` on the wire, as on Amazon ECS.
	ecsTasks.Update("held", func(task *ECSTask) { task.LastStatus = ECSTaskStatusStopped })
	ecsTasks.Update("held2", func(task *ECSTask) { task.LastStatus = ECSTaskStatusStopped })
	rec = httptest.NewRecorder()
	handleECSRunTask(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"cluster":"default","taskDefinition":"fits:1","count":1}`)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"failures":[]`)
	placed, _ := ecsTasks.Get(func() string {
		for _, task := range ecsTasks.List() {
			if task.LastStatus != ECSTaskStatusStopped {
				return strings.TrimPrefix(task.TaskArn, "arn:aws:ecs:"+awsRegion()+":"+awsAccountID()+":task/default/")
			}
		}
		return ""
	}())
	require.Equal(t, "2048", placed.Memory, "the placed task carries the commitment it holds")
	AwaitSimulatorBackground()
}
