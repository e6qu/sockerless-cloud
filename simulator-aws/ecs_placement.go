package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Placement. Real ECS decides at RunTask time whether a task fits — on EC2
// against the container instances' registered resources, on Fargate against
// the capacity the service has in the zone — and when it does not, RunTask
// still answers HTTP 200, with the task missing from `tasks[]` and an entry in
// `failures[]` (`RESOURCE:MEMORY`, `RESOURCE:CPU`, or Fargate's capacity
// message). That is a different failure, with a different shape, from a task
// that was placed and then failed to start (STOPPED, `TaskFailedToStart`).
//
// This simulator runs real containers on one finite host, so a refusal can be
// grounded rather than invented: every placed task commits the memory and CPU
// its task definition declares (the same numbers its containers' cgroups are
// bounded to) against what the host can commit, and a task that would not fit
// is refused with the real shape. The ledger is the task store itself — a task
// holds its commitment until it is STOPPED — plus the reservations of tasks
// that are between "decided" and "stored", so two concurrent RunTasks cannot
// both be told they fit into the last slot.

// ecsTaskSize is a commitment in ECS units: MiB of memory and CPU units
// (1024 == 1 vCPU).
type ecsTaskSize struct {
	MemoryMiB int
	CPUUnits  int
}

// ecsFailure is the RunTask/StartTask `failures[]` entry.
type ecsFailure struct {
	Arn    string `json:"arn"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// ecsFargateCapacityUnavailable is the reason Fargate returns when it cannot
// place a task, verbatim.
const ecsFargateCapacityUnavailable = "Capacity is unavailable at this time. Please try again later or in a different availability zone"

var (
	ecsPlacementMu sync.Mutex
	// ecsPlacementInFlight holds the sizes of tasks that have been granted
	// placement but are not yet in the task store.
	ecsPlacementInFlight = map[string]ecsTaskSize{}
	// ecsPlacementCeiling answers what the host can commit. It is read once
	// from the simulator's own cgroup (or the machine, when the cgroup is
	// unbounded); tests substitute a small host.
	ecsPlacementCeiling = sync.OnceValue(ecsHostCeiling)
)

// ecsRequestedTaskSize is the commitment a RunTask asks for: the task-level
// size (with request overrides applied), or — for an EC2 task definition
// sized only per container — the containers' hard memory limits (their
// reservations when no hard limit is set) and CPU units summed, which is how
// Amazon ECS reserves container-instance resources.
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

// ecsCommittedTaskSize is what a stored task holds: its task-level size, or
// the container sum from its task definition when it has none.
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

// ecsCommittedTotal sums every commitment the host currently carries: stored
// tasks that are not STOPPED, and placements in flight. Caller holds
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

// ecsReservePlacement decides whether `size` fits beside everything already
// placed. On success the reservation is held under taskID until
// ecsCommitPlacement stores the task or ecsAbandonPlacement gives it back; on
// refusal it returns the resource that ran out ("MEMORY" or "CPU") and the
// ledger at the time of the decision.
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

// ecsCommitPlacement stores a placed task and retires its reservation in one
// step, so no concurrent decision sees the task both in flight and stored.
func ecsCommitPlacement(taskID string, task ECSTask) {
	ecsPlacementMu.Lock()
	defer ecsPlacementMu.Unlock()
	ecsTasks.Put(taskID, task)
	delete(ecsPlacementInFlight, taskID)
}

// ecsAbandonPlacement gives a reservation back when the task was refused for
// another reason after placement had been granted.
func ecsAbandonPlacement(taskID string) {
	ecsPlacementMu.Lock()
	defer ecsPlacementMu.Unlock()
	delete(ecsPlacementInFlight, taskID)
}

// ecsPlacementFailure is the `failures[]` entry for a task that did not fit.
// Fargate names the cluster and gives its capacity message; EC2 names the
// container instance (the cluster when the request did not target one) and
// the resource, `RESOURCE:MEMORY` / `RESOURCE:CPU`. The detail carries the
// ledger, so a caller can see why the host said no.
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
