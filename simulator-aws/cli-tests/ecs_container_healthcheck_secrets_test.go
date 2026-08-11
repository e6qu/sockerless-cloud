package aws_cli_test

import (
	"strings"
	"testing"
)

// TestECSCLI_ContainerHealthCheckAndSecrets drives the healthCheck + secrets
// containerDefinitions round-trip via the aws CLI.
func TestECSCLI_ContainerHealthCheckAndSecrets(t *testing.T) {
	runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-hc-secrets",
		"--network-mode", "awsvpc",
		"--requires-compatibilities", "FARGATE",
		"--cpu", "256", "--memory", "512",
		"--container-definitions", `[{"name":"app","image":"nginx","healthCheck":{"command":["CMD-SHELL","curl -f http://localhost/ || exit 1"],"interval":30,"timeout":5,"retries":3,"startPeriod":30},"secrets":[{"name":"DB","valueFrom":"arn:aws:ssm:us-east-1:123456789012:parameter/db"}]}]`))

	retries := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-task-definition",
		"--task-definition", "cli-hc-secrets",
		"--query", "taskDefinition.containerDefinitions[0].healthCheck.retries", "--output", "text")))
	if retries != "3" {
		t.Fatalf("healthCheck.retries = %q, want 3", retries)
	}
	secret := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-task-definition",
		"--task-definition", "cli-hc-secrets",
		"--query", "taskDefinition.containerDefinitions[0].secrets[0].name", "--output", "text")))
	if secret != "DB" {
		t.Fatalf("secrets[0].name = %q, want DB", secret)
	}
}
