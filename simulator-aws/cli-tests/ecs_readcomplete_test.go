package aws_cli_test

import (
	"strings"
	"testing"
)

// TestECSCLI_CapacityProvidersAndFamilies drives the two ECS read ops via the
// aws CLI.
func TestECSCLI_CapacityProvidersAndFamilies(t *testing.T) {
	status := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-capacity-providers",
		"--capacity-providers", "FARGATE",
		"--query", "capacityProviders[0].status", "--output", "text")))
	if status != "ACTIVE" {
		t.Fatalf("FARGATE capacity provider status = %q, want ACTIVE", status)
	}

	runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-rc-family",
		"--container-definitions", `[{"name":"app","image":"public.ecr.aws/docker/library/busybox:latest","memory":128}]`))

	families := runCLI(t, awsCLI("ecs", "list-task-definition-families", "--family-prefix", "cli-rc-"))
	if !strings.Contains(families, "cli-rc-family") {
		t.Fatalf("list-task-definition-families did not include cli-rc-family: %s", families)
	}
}
