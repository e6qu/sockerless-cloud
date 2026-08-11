package aws_cli_test

import (
	"strings"
	"testing"
)

// TestECSTaskDefFidelityCLI covers the full container-definition round-trip and
// the top-level task-def knobs (runtime_platform / ephemeral_storage / pid_mode
// / ipc_mode / placement_constraints) via the aws CLI.
func TestECSTaskDefFidelityCLI(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	containerDefs := `[
	  {"name":"app","image":"nginx:latest","essential":true,"user":"1000:1000",
	   "workingDirectory":"/srv","readonlyRootFilesystem":true,"startTimeout":30,"stopTimeout":10,
	   "dockerLabels":{"com.example.team":"platform"},
	   "ulimits":[{"name":"nofile","softLimit":1024,"hardLimit":2048}],
	   "linuxParameters":{"initProcessEnabled":true}}
	]`

	arn := q("ecs", "register-task-definition",
		"--family", "cli-fidelity-taskdef",
		"--network-mode", "awsvpc",
		"--requires-compatibilities", "FARGATE",
		"--cpu", "256", "--memory", "512",
		"--pid-mode", "task", "--ipc-mode", "task",
		"--runtime-platform", "cpuArchitecture=ARM64,operatingSystemFamily=LINUX",
		"--ephemeral-storage", "sizeInGiB=30",
		"--placement-constraints", "type=memberOf,expression=attribute:ecs.os-type == linux",
		"--container-definitions", containerDefs,
		"--query", "taskDefinition.taskDefinitionArn", "--output", "text")
	if !strings.Contains(arn, ":task-definition/cli-fidelity-taskdef:") {
		t.Fatalf("unexpected task-def ARN: %q", arn)
	}

	// Top-level knobs round-trip.
	out := q("ecs", "describe-task-definition", "--task-definition", arn,
		"--query", "taskDefinition.[runtimePlatform.cpuArchitecture,ephemeralStorage.sizeInGiB,pidMode,ipcMode]", "--output", "text")
	if f := strings.Fields(out); len(f) != 4 || f[0] != "ARM64" || f[1] != "30" || f[2] != "task" || f[3] != "task" {
		t.Fatalf("top-level knobs round-trip: got %q", out)
	}
	if pc := q("ecs", "describe-task-definition", "--task-definition", arn,
		"--query", "taskDefinition.placementConstraints[0].expression", "--output", "text"); pc != "attribute:ecs.os-type == linux" {
		t.Fatalf("placement constraint: got %q", pc)
	}

	// Container-def fields that were dropped before.
	cd := q("ecs", "describe-task-definition", "--task-definition", arn,
		"--query", "taskDefinition.containerDefinitions[0].[user,workingDirectory,readonlyRootFilesystem,ulimits[0].hardLimit,linuxParameters.initProcessEnabled]", "--output", "text")
	if f := strings.Fields(cd); len(f) != 5 || f[0] != "1000:1000" || f[1] != "/srv" || f[2] != "True" || f[3] != "2048" || f[4] != "True" {
		t.Fatalf("container-def round-trip: got %q", cd)
	}

	// Compatibilities: awsvpc + Fargate ⇒ EC2 + FARGATE.
	compat := q("ecs", "describe-task-definition", "--task-definition", arn,
		"--query", "taskDefinition.compatibilities", "--output", "text")
	if !strings.Contains(compat, "EC2") || !strings.Contains(compat, "FARGATE") {
		t.Fatalf("compatibilities: got %q, want EC2 + FARGATE", compat)
	}
}
