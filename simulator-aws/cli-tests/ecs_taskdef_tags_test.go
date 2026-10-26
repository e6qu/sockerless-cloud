package aws_cli_test

import (
	"strings"
	"testing"
)

// TestECSCLI_TaskDefinitionTagsIncludePath expects describe-task-definition to
// return top-level tags only with --include TAGS, terraform-provider-aws's
// read path.
func TestECSCLI_TaskDefinitionTagsIncludePath(t *testing.T) {
	t.Parallel()
	runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-taskdef-tags",
		"--network-mode", "awsvpc",
		"--requires-compatibilities", "FARGATE",
		"--cpu", "256", "--memory", "512",
		"--container-definitions", `[{"name":"app","image":"mirror.gcr.io/library/nginx:latest","stopTimeout":2}]`,
		"--tags", "key=Name,value=test", "key=env,value=ci"))

	withTags := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-task-definition",
		"--task-definition", "cli-taskdef-tags", "--include", "TAGS",
		"--query", "tags[?key=='Name'].value | [0]", "--output", "text")))
	if withTags != "test" {
		t.Fatalf("describe --include TAGS Name tag = %q, want test", withTags)
	}

	noTags := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-task-definition",
		"--task-definition", "cli-taskdef-tags",
		"--query", "tags", "--output", "text")))
	if noTags != "None" {
		t.Fatalf("describe without --include TAGS tags = %q, want None", noTags)
	}

	arn := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-task-definition",
		"--task-definition", "cli-taskdef-tags",
		"--query", "taskDefinition.taskDefinitionArn", "--output", "text")))
	listed := strings.TrimSpace(runCLI(t, awsCLI("ecs", "list-tags-for-resource",
		"--resource-arn", arn, "--query", "tags[?key=='env'].value | [0]", "--output", "text")))
	if listed != "ci" {
		t.Fatalf("list-tags-for-resource env tag = %q, want ci", listed)
	}
}
