package aws_cli_test

import (
	"strings"
	"testing"
)

func TestECSCLI_ContainerDependencies(t *testing.T) {
	t.Parallel()
	runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-dependencies",
		"--container-definitions", `[{"name":"setup","image":"mirror.gcr.io/library/busybox:latest","memory":64,"essential":false,"startTimeout":30},`+
			`{"name":"app","image":"mirror.gcr.io/library/busybox:latest","memory":64,"dependsOn":[{"containerName":"setup","condition":"SUCCESS"}]}]`))

	dependency := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-task-definition",
		"--task-definition", "cli-dependencies",
		"--query", "taskDefinition.containerDefinitions[1].dependsOn[0].[containerName,condition]", "--output", "text")))
	if dependency != "setup\tSUCCESS" {
		t.Fatalf("dependsOn[0] = %q, want setup SUCCESS", dependency)
	}
	startTimeout := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-task-definition",
		"--task-definition", "cli-dependencies",
		"--query", "taskDefinition.containerDefinitions[0].startTimeout", "--output", "text")))
	if startTimeout != "30" {
		t.Fatalf("startTimeout = %q, want 30", startTimeout)
	}

	out := runCLIExpectError(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-dependencies-unknown",
		"--container-definitions", `[{"name":"app","image":"mirror.gcr.io/library/busybox:latest","memory":64,"dependsOn":[{"containerName":"missing","condition":"START"}]}]`))
	if !strings.Contains(out, "ClientException") {
		t.Fatalf("register-task-definition with an unknown dependency = %q, want a ClientException", out)
	}
}
