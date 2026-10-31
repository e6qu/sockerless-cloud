package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

const (
	ecsHealthHealthy   = "HEALTHY"
	ecsHealthUnhealthy = "UNHEALTHY"
	ecsHealthUnknown   = "UNKNOWN"
)

// The HealthCheck shape's documented defaults; startPeriod is off unless set.
const (
	ecsHealthDefaultInterval = 30 * time.Second
	ecsHealthDefaultTimeout  = 5 * time.Second
	ecsHealthDefaultRetries  = 3
)

type ecsHealthCheck struct {
	Command     []string `json:"command"`
	Interval    *int     `json:"interval,omitempty"`
	Timeout     *int     `json:"timeout,omitempty"`
	Retries     *int     `json:"retries,omitempty"`
	StartPeriod *int     `json:"startPeriod,omitempty"`
}

// nil when the definition declares no health check.
func ecsContainerHealthCheck(cd ECSContainerDefinition) *ecsHealthCheck {
	if len(cd.HealthCheck) == 0 || string(cd.HealthCheck) == "null" {
		return nil
	}
	var hc ecsHealthCheck
	if err := json.Unmarshal(cd.HealthCheck, &hc); err != nil || len(hc.Command) == 0 {
		return nil
	}
	return &hc
}

// What the container runs for each check: CMD runs its arguments directly,
// CMD-SHELL runs them with the container's default shell, and NONE disables
// the check, as Docker's HEALTHCHECK reads them.
func (hc *ecsHealthCheck) probeCommand() ([]string, bool) {
	switch hc.Command[0] {
	case "CMD":
		return hc.Command[1:], len(hc.Command) > 1
	case "CMD-SHELL":
		return []string{"/bin/sh", "-c", strings.Join(hc.Command[1:], " ")}, len(hc.Command) > 1
	default:
		return nil, false
	}
}

func ecsSecondsOr(value *int, fallback time.Duration) time.Duration {
	if value == nil || *value <= 0 {
		return fallback
	}
	return time.Duration(*value) * time.Second
}

// Runs a container's health check on the definition's own interval, as Docker
// runs a HEALTHCHECK: the first check one interval after the start, a check
// that outlasts timeout counts as failed, failures inside startPeriod count
// only once a check has passed, and retries consecutive failures make the
// container UNHEALTHY.
type ecsHealthMonitor struct {
	taskID      string
	container   string
	handle      placement.Handle
	command     []string
	interval    time.Duration
	timeout     time.Duration
	retries     int
	startPeriod time.Duration
	startedAt   time.Time

	mu       sync.Mutex
	passed   bool
	failures int
	status   string
}

func ecsMonitorContainerHealth(taskID string, cd ECSContainerDefinition, handle placement.Handle) {
	hc := ecsContainerHealthCheck(cd)
	if hc == nil {
		return
	}
	command, ok := hc.probeCommand()
	if !ok {
		return
	}
	retries := ecsHealthDefaultRetries
	if hc.Retries != nil && *hc.Retries > 0 {
		retries = *hc.Retries
	}
	m := &ecsHealthMonitor{
		taskID:      taskID,
		container:   cd.Name,
		handle:      handle,
		command:     command,
		interval:    ecsSecondsOr(hc.Interval, ecsHealthDefaultInterval),
		timeout:     ecsSecondsOr(hc.Timeout, ecsHealthDefaultTimeout),
		retries:     retries,
		startPeriod: ecsSecondsOr(hc.StartPeriod, 0),
		startedAt:   handle.StartedAt(),
		status:      ecsHealthUnknown,
	}
	m.arm()
}

func (m *ecsHealthMonitor) arm() {
	bg.AfterFunc(m.interval, m.check)
}

func (m *ecsHealthMonitor) exited() bool {
	select {
	case <-m.handle.Exited():
		return true
	default:
		return false
	}
}

func (m *ecsHealthMonitor) check() {
	if m.exited() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	result, err := m.handle.Exec(ctx, m.command)
	cancel()
	if m.exited() {
		return
	}
	if status, changed := m.record(err == nil && result.ExitCode == 0, time.Now()); changed {
		if !ecsSetContainerHealth(m.taskID, m.container, status) {
			return
		}
	}
	m.arm()
}

func (m *ecsHealthMonitor) record(passed bool, now time.Time) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	previous := m.status
	switch {
	case passed:
		m.passed = true
		m.failures = 0
		m.status = ecsHealthHealthy
	case !m.passed && now.Sub(m.startedAt) < m.startPeriod:
	default:
		m.failures++
		if m.failures >= m.retries {
			m.status = ecsHealthUnhealthy
		}
	}
	return m.status, m.status != previous
}

// Reports false once the task has stopped, which ends the monitor.
func ecsSetContainerHealth(taskID, container, status string) bool {
	task, ok := ecsTasks.Get(taskID)
	if !ok || task.LastStatus == ECSTaskStatusStopped {
		return false
	}
	td, ok := ecsTaskDefinitionByArn(task.TaskDefinitionArn)
	if !ok {
		return false
	}
	live := true
	ecsTasks.Update(taskID, func(t *ECSTask) {
		if t.LastStatus == ECSTaskStatusStopped {
			live = false
			return
		}
		for j := range t.Containers {
			if t.Containers[j].Name == container {
				t.Containers[j].HealthStatus = status
			}
		}
		t.HealthStatus = ecsTaskHealthStatus(td, t.Containers)
	})
	if live {
		fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: container %s is %s\n", taskID, container, status)
	}
	return live
}

// Only the essential containers whose definitions declare a health check
// decide the task's health: one UNHEALTHY makes it UNHEALTHY, else one
// UNKNOWN makes it UNKNOWN. A task with no health check reports none.
func ecsTaskHealthStatus(td ECSTaskDefinition, containers []ECSTaskContainer) string {
	declared := false
	considered := 0
	status := ecsHealthHealthy
	for _, cd := range td.ContainerDefinitions {
		if ecsContainerHealthCheck(cd) == nil {
			continue
		}
		declared = true
		if !ecsContainerEssential(cd) {
			continue
		}
		considered++
		containerStatus := ecsHealthUnknown
		for _, c := range containers {
			if c.Name == cd.Name && c.HealthStatus != "" {
				containerStatus = c.HealthStatus
			}
		}
		switch {
		case containerStatus == ecsHealthUnhealthy:
			return ecsHealthUnhealthy
		case containerStatus != ecsHealthHealthy:
			status = ecsHealthUnknown
		}
	}
	if !declared {
		return ""
	}
	if considered == 0 {
		return ecsHealthUnknown
	}
	return status
}
