package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/signals"
)

const (
	ecsConditionStart    = "START"
	ecsConditionComplete = "COMPLETE"
	ecsConditionSuccess  = "SUCCESS"
	ecsConditionHealthy  = "HEALTHY"
)

// Announces a task's ID each time one of its containers is placed, for a
// metadata read that arrived before its container's handle was stored.
var ecsContainerPlacements = signals.New[string]()

// The ClientException message for a dependsOn Amazon ECS refuses, or "".
func ecsContainerDependenciesInvalid(defs []ECSContainerDefinition) string {
	names := make(map[string]bool, len(defs))
	for _, cd := range defs {
		names[cd.Name] = true
	}
	for _, cd := range defs {
		for _, dep := range cd.DependsOn {
			switch dep.Condition {
			case ecsConditionStart, ecsConditionComplete, ecsConditionSuccess, ecsConditionHealthy:
			default:
				return fmt.Sprintf("Container %s dependsOn condition %q is not one of START, COMPLETE, SUCCESS or HEALTHY.", cd.Name, dep.Condition)
			}
			if !names[dep.ContainerName] {
				return fmt.Sprintf("Container %s dependsOn container %s, which is not defined in the task definition.", cd.Name, dep.ContainerName)
			}
		}
	}
	return ""
}

// A dependency that can never be met: the container that names it never
// starts, and the task stops.
type ecsDependencyError struct {
	container  string
	dependency ECSContainerDependency
	cause      string
}

func (e *ecsDependencyError) Error() string {
	return fmt.Sprintf("container %s cannot start: dependency %s (%s) %s", e.container, e.dependency.ContainerName, e.dependency.Condition, e.cause)
}

// Reports whether every dependency of cd has reached its condition. While one
// has not, deadline is the earliest moment a dependency's startTimeout runs
// out, zero when none is running against one. The timeout belongs to the
// dependency, counts from its start, and bounds only COMPLETE, SUCCESS and
// HEALTHY, as the ECS developer guide's startTimeout states.
func ecsContainerDependenciesMet(td ECSTaskDefinition, cd ECSContainerDefinition, containers []ECSTaskContainer, started map[string]time.Time, now time.Time) (met bool, deadline time.Time, err error) {
	met = true
	for _, dep := range cd.DependsOn {
		startedAt, isStarted := started[dep.ContainerName]
		if !isStarted {
			met = false
			continue
		}
		var state ECSTaskContainer
		for _, c := range containers {
			if c.Name == dep.ContainerName {
				state = c
			}
		}
		exited := state.LastStatus == "STOPPED"
		switch dep.Condition {
		case ecsConditionStart:
			continue
		case ecsConditionComplete:
			if exited {
				continue
			}
		case ecsConditionSuccess:
			if exited && state.ExitCode != nil && *state.ExitCode == 0 {
				continue
			}
			if exited {
				code := "without an exit code"
				if state.ExitCode != nil {
					code = fmt.Sprintf("with exit code %d", *state.ExitCode)
				}
				return false, time.Time{}, &ecsDependencyError{container: cd.Name, dependency: dep, cause: "exited " + code}
			}
		case ecsConditionHealthy:
			if state.HealthStatus == ecsHealthHealthy {
				continue
			}
		}
		met = false
		timeout := ecsDependencyStartTimeout(td, dep.ContainerName)
		if timeout <= 0 {
			continue
		}
		expires := startedAt.Add(timeout)
		if !now.Before(expires) {
			return false, time.Time{}, &ecsDependencyError{container: cd.Name, dependency: dep, cause: fmt.Sprintf("was not met within its startTimeout of %s", timeout)}
		}
		if deadline.IsZero() || expires.Before(deadline) {
			deadline = expires
		}
	}
	return met, deadline, nil
}

// Zero when the definition sets no startTimeout: the guide bounds the wait
// only when one is specified.
func ecsDependencyStartTimeout(td ECSTaskDefinition, name string) time.Duration {
	for _, cd := range td.ContainerDefinitions {
		if cd.Name == name && cd.StartTimeout != nil && *cd.StartTimeout > 0 {
			return time.Duration(*cd.StartTimeout) * time.Second
		}
	}
	return 0
}

// The start stopped because the task is stopping: StopTask asked for it, or
// an essential container exited and its watch stops the task.
var errECSTaskStartPreempted = errors.New("task start preempted by the task stopping")

func ecsTaskStartPreempted(t ECSTask, essential map[string]bool) bool {
	if t.DesiredStatus == ECSTaskStatusStopped || t.LastStatus == ECSTaskStatusStopped {
		return true
	}
	for _, c := range t.Containers {
		if essential[c.Name] && c.LastStatus == "STOPPED" {
			return true
		}
	}
	return false
}
