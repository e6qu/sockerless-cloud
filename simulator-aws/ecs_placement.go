package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Amazon ECS decides at RunTask whether a task fits; when it does not, RunTask
// still answers 200, with a `failures[]` entry (`RESOURCE:MEMORY`,
// `RESOURCE:CPU`, or Fargate's capacity message) instead of the task. That
// differs from a placed task failing to start (STOPPED, TaskFailedToStart).
//
// The simulator's host is finite: each placed task commits the memory and CPU
// its definition declares, the same its cgroups are bounded to. The ledger is
// the task store (until STOPPED) plus in-flight reservations, so two
// concurrent RunTasks cannot share the last slot.

// 1024 CPU units = 1 vCPU.
type ecsTaskSize struct {
	MemoryMiB int
	CPUUnits  int
}

type ecsFailure struct {
	Arn    string `json:"arn"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Fargate's reason, verbatim.
const ecsFargateCapacityUnavailable = "Capacity is unavailable at this time. Please try again later or in a different availability zone"

var (
	ecsPlacementMu sync.Mutex
	// Tasks granted placement but not yet stored.
	ecsPlacementInFlight = map[string]ecsTaskSize{}
	// Read once from the simulator's cgroup, or the machine when unbounded; tests
	// substitute a small host.
	ecsPlacementCeiling = sync.OnceValue(ecsHostCeiling)
)

// The task-level size with overrides, or for an EC2 definition sized per
// container the summed hard memory limits (reservations without one) and CPU
// units, as Amazon ECS reserves instance resources.
func ecsRequestedTaskSize(td ECSTaskDefinition, overrides *ECSTaskOverride) ecsTaskSize {
	size := ecsTaskSize{}
	if m, err := strconv.Atoi(ecsTaskMemory(td, overrides)); err == nil {
		size.MemoryMiB = m
	}
	if c, err := strconv.Atoi(ecsTaskCPU(td, overrides)); err == nil {
		size.CPUUnits = c
	}
	if size.MemoryMiB == 0 && size.CPUUnits == 0 {
		size = ecsContainerSummedSize(td)
	}
	return size
}

func ecsContainerSummedSize(td ECSTaskDefinition) ecsTaskSize {
	size := ecsTaskSize{}
	for _, cd := range td.ContainerDefinitions {
		if cd.Memory > 0 {
			size.MemoryMiB += cd.Memory
		} else {
			size.MemoryMiB += cd.MemoryReservation
		}
		size.CPUUnits += cd.Cpu
	}
	return size
}

// The task-level size, else the definition's container sum.
func ecsCommittedTaskSize(task ECSTask) ecsTaskSize {
	size := ecsTaskSize{}
	if m, err := strconv.Atoi(task.Memory); err == nil {
		size.MemoryMiB = m
	}
	if c, err := strconv.Atoi(task.Cpu); err == nil {
		size.CPUUnits = c
	}
	if size.MemoryMiB != 0 || size.CPUUnits != 0 {
		return size
	}
	key := task.TaskDefinitionArn
	if i := strings.LastIndex(key, "/"); i >= 0 {
		key = key[i+1:]
	}
	if td, ok := ecsTaskDefinitions.Get(key); ok {
		return ecsContainerSummedSize(td)
	}
	return size
}

// Non-STOPPED stored tasks plus in-flight placements. Caller holds
// ecsPlacementMu.
func ecsCommittedTotal() ecsTaskSize {
	total := ecsTaskSize{}
	for _, task := range ecsTasks.Filter(func(t ECSTask) bool { return t.LastStatus != ECSTaskStatusStopped }) {
		size := ecsCommittedTaskSize(task)
		total.MemoryMiB += size.MemoryMiB
		total.CPUUnits += size.CPUUnits
	}
	for _, size := range ecsPlacementInFlight {
		total.MemoryMiB += size.MemoryMiB
		total.CPUUnits += size.CPUUnits
	}
	return total
}

// A granted reservation is held until ecsCommitPlacement or
// ecsAbandonPlacement; a refusal returns the exhausted resource ("MEMORY" or
// "CPU") and the ledger.
func ecsReservePlacement(taskID string, size ecsTaskSize) (short string, committed, ceiling ecsTaskSize, ok bool) {
	ecsPlacementMu.Lock()
	defer ecsPlacementMu.Unlock()
	ceiling = ecsPlacementCeiling()
	committed = ecsCommittedTotal()
	switch {
	case committed.MemoryMiB+size.MemoryMiB > ceiling.MemoryMiB:
		return "MEMORY", committed, ceiling, false
	case committed.CPUUnits+size.CPUUnits > ceiling.CPUUnits:
		return "CPU", committed, ceiling, false
	}
	ecsPlacementInFlight[taskID] = size
	return "", committed, ceiling, true
}

// In one step, so no decision sees the task both in flight and stored.
func ecsCommitPlacement(taskID string, task ECSTask) {
	ecsPlacementMu.Lock()
	defer ecsPlacementMu.Unlock()
	ecsTasks.Put(taskID, task)
	delete(ecsPlacementInFlight, taskID)
}

// For a task refused for another reason after placement.
func ecsAbandonPlacement(taskID string) {
	ecsPlacementMu.Lock()
	defer ecsPlacementMu.Unlock()
	delete(ecsPlacementInFlight, taskID)
}

// Fargate names the cluster with its capacity message; EC2 names the
// container instance (or cluster) and `RESOURCE:MEMORY` / `RESOURCE:CPU`. The
// detail carries the ledger.
func ecsPlacementFailure(in ecsRunTaskInput, clusterArn, short string, requested, committed, ceiling ecsTaskSize) ecsFailure {
	detail := fmt.Sprintf("the host can commit %d MiB and %d CPU units; %d MiB and %d CPU units are held by placed tasks and this task needs %d MiB and %d CPU units",
		ceiling.MemoryMiB, ceiling.CPUUnits, committed.MemoryMiB, committed.CPUUnits, requested.MemoryMiB, requested.CPUUnits)
	if strings.EqualFold(in.LaunchType, "FARGATE") {
		return ecsFailure{Arn: clusterArn, Reason: ecsFargateCapacityUnavailable, Detail: detail}
	}
	arn := in.ContainerInstanceArn
	if arn == "" {
		arn = clusterArn
	}
	return ecsFailure{Arn: arn, Reason: "RESOURCE:" + short, Detail: detail}
}
