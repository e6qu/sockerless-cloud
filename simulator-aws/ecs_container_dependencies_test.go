package main

import (
	"errors"
	"testing"
	"time"
)

func TestECSContainerDependenciesMet(t *testing.T) {
	timeout := 10
	td := ECSTaskDefinition{ContainerDefinitions: []ECSContainerDefinition{
		{Name: "setup", StartTimeout: &timeout},
		{Name: "untimed"},
		{Name: "app"},
	}}
	start := time.Unix(1_000, 0)
	code := func(n int) *int { return &n }
	cases := []struct {
		name        string
		condition   string
		dependency  string
		state       ECSTaskContainer
		started     bool
		now         time.Time
		met         bool
		failed      bool
		hasDeadline bool
	}{
		{name: "not started waits with no deadline", condition: ecsConditionStart, dependency: "setup"},
		{name: "START is met once started", condition: ecsConditionStart, dependency: "setup", started: true, state: ECSTaskContainer{LastStatus: "RUNNING"}, met: true},
		{name: "COMPLETE waits for the exit", condition: ecsConditionComplete, dependency: "setup", started: true, state: ECSTaskContainer{LastStatus: "RUNNING"}, now: start.Add(time.Second), hasDeadline: true},
		{name: "COMPLETE takes any exit code", condition: ecsConditionComplete, dependency: "setup", started: true, state: ECSTaskContainer{LastStatus: "STOPPED", ExitCode: code(3)}, met: true},
		{name: "SUCCESS takes exit code 0", condition: ecsConditionSuccess, dependency: "setup", started: true, state: ECSTaskContainer{LastStatus: "STOPPED", ExitCode: code(0)}, met: true},
		{name: "SUCCESS fails on a non-zero exit", condition: ecsConditionSuccess, dependency: "setup", started: true, state: ECSTaskContainer{LastStatus: "STOPPED", ExitCode: code(1)}, failed: true},
		{name: "HEALTHY waits for the check", condition: ecsConditionHealthy, dependency: "setup", started: true, state: ECSTaskContainer{LastStatus: "RUNNING", HealthStatus: ecsHealthUnknown}, now: start.Add(time.Second), hasDeadline: true},
		{name: "HEALTHY is met once healthy", condition: ecsConditionHealthy, dependency: "setup", started: true, state: ECSTaskContainer{LastStatus: "RUNNING", HealthStatus: ecsHealthHealthy}, met: true},
		{name: "startTimeout fails the wait", condition: ecsConditionHealthy, dependency: "setup", started: true, state: ECSTaskContainer{LastStatus: "RUNNING"}, now: start.Add(10 * time.Second), failed: true},
		{name: "no startTimeout waits on", condition: ecsConditionHealthy, dependency: "untimed", started: true, state: ECSTaskContainer{LastStatus: "RUNNING"}, now: start.Add(time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := ECSContainerDefinition{Name: "app", DependsOn: []ECSContainerDependency{{ContainerName: tc.dependency, Condition: tc.condition}}}
			state := tc.state
			state.Name = tc.dependency
			started := map[string]time.Time{}
			if tc.started {
				started[tc.dependency] = start
			}
			met, deadline, err := ecsContainerDependenciesMet(td, app, []ECSTaskContainer{state}, started, tc.now)
			var dependencyErr *ecsDependencyError
			if tc.failed != errors.As(err, &dependencyErr) {
				t.Fatalf("err = %v, want failed %v", err, tc.failed)
			}
			if met != tc.met {
				t.Fatalf("met = %v, want %v", met, tc.met)
			}
			if !deadline.IsZero() != tc.hasDeadline {
				t.Fatalf("deadline = %v, want one: %v", deadline, tc.hasDeadline)
			}
			if tc.hasDeadline && !deadline.Equal(start.Add(10*time.Second)) {
				t.Fatalf("deadline = %v, want the dependency's start plus its startTimeout", deadline)
			}
		})
	}
}

func TestECSContainerDependenciesInvalid(t *testing.T) {
	defs := []ECSContainerDefinition{
		{Name: "setup"},
		{Name: "app", DependsOn: []ECSContainerDependency{{ContainerName: "setup", Condition: ecsConditionSuccess}}},
	}
	if message := ecsContainerDependenciesInvalid(defs); message != "" {
		t.Fatalf("a dependency on a defined container was refused: %s", message)
	}
	defs[1].DependsOn[0].ContainerName = "missing"
	if ecsContainerDependenciesInvalid(defs) == "" {
		t.Fatal("a dependency on an undefined container was accepted")
	}
	defs[1].DependsOn[0] = ECSContainerDependency{ContainerName: "setup", Condition: "READY"}
	if ecsContainerDependenciesInvalid(defs) == "" {
		t.Fatal("an unknown condition was accepted")
	}
}

// The task's health follows its essential containers that declare a check, in
// the healthcheck guide's order: UNHEALTHY, then UNKNOWN, then HEALTHY.
func TestECSTaskHealthStatus(t *testing.T) {
	check := []byte(`{"command":["CMD-SHELL","true"]}`)
	notEssential := false
	td := ECSTaskDefinition{ContainerDefinitions: []ECSContainerDefinition{
		{Name: "one", HealthCheck: check},
		{Name: "two", HealthCheck: check},
		{Name: "sidecar", HealthCheck: check, Essential: &notEssential},
		{Name: "unchecked"},
	}}
	containers := func(one, two, sidecar string) []ECSTaskContainer {
		return []ECSTaskContainer{{Name: "one", HealthStatus: one}, {Name: "two", HealthStatus: two}, {Name: "sidecar", HealthStatus: sidecar}, {Name: "unchecked"}}
	}
	for _, tc := range []struct{ one, two, sidecar, want string }{
		{ecsHealthUnhealthy, ecsHealthUnknown, ecsHealthHealthy, ecsHealthUnhealthy},
		{ecsHealthUnhealthy, ecsHealthHealthy, ecsHealthHealthy, ecsHealthUnhealthy},
		{ecsHealthHealthy, ecsHealthUnknown, ecsHealthHealthy, ecsHealthUnknown},
		{ecsHealthHealthy, ecsHealthHealthy, ecsHealthUnhealthy, ecsHealthHealthy},
	} {
		if got := ecsTaskHealthStatus(td, containers(tc.one, tc.two, tc.sidecar)); got != tc.want {
			t.Errorf("%s/%s/%s: task health %s, want %s", tc.one, tc.two, tc.sidecar, got, tc.want)
		}
	}
	if got := ecsTaskHealthStatus(ECSTaskDefinition{ContainerDefinitions: []ECSContainerDefinition{{Name: "app"}}}, nil); got != "" {
		t.Errorf("a task without health checks reports %q", got)
	}
}

// Failures inside startPeriod count only once a check has passed; retries
// consecutive failures make the container UNHEALTHY.
func TestECSHealthMonitorRecord(t *testing.T) {
	start := time.Unix(1_000, 0)
	m := &ecsHealthMonitor{retries: 2, startPeriod: 10 * time.Second, startedAt: start, status: ecsHealthUnknown}
	for range 3 {
		if status, _ := m.record(false, start.Add(time.Second)); status != ecsHealthUnknown {
			t.Fatalf("a failure inside startPeriod made the container %s", status)
		}
	}
	if status, changed := m.record(true, start.Add(2*time.Second)); status != ecsHealthHealthy || !changed {
		t.Fatalf("a passing check left the container %s", status)
	}
	if status, _ := m.record(false, start.Add(3*time.Second)); status != ecsHealthHealthy {
		t.Fatalf("one failure of two retries made the container %s", status)
	}
	if status, changed := m.record(false, start.Add(4*time.Second)); status != ecsHealthUnhealthy || !changed {
		t.Fatalf("retries consecutive failures left the container %s", status)
	}
}
