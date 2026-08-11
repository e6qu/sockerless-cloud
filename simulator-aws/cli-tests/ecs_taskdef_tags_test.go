package aws_cli_test

import (
	"strings"
	"testing"
)

// TestECSCLI_TaskDefinitionTagsIncludePath drives the task-def tag read paths:
// describe-task-definition --include TAGS surfaces top-level tags (the TF
// provider's path), while without --include TAGS there are none.
func TestECSCLI_TaskDefinitionTagsIncludePath(t *testing.T) {
	runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-taskdef-tags",
		"--network-mode", "awsvpc",
		"--requires-compatibilities", "FARGATE",
		"--cpu", "256", "--memory", "512",
		"--container-definitions", `[{"name":"app","image":"nginx"}]`,
		"--tags", "key=Name,value=test", "key=env,value=ci"))

	// --include TAGS surfaces the top-level tags.
	withTags := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-task-definition",
		"--task-definition", "cli-taskdef-tags", "--include", "TAGS",
		"--query", "tags[?key=='Name'].value | [0]", "--output", "text")))
	if withTags != "test" {
		t.Fatalf("describe --include TAGS Name tag = %q, want test", withTags)
	}

	// Without --include TAGS, no top-level tags.
	noTags := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-task-definition",
		"--task-definition", "cli-taskdef-tags",
		"--query", "tags", "--output", "text")))
	if noTags != "None" {
		t.Fatalf("describe without --include TAGS tags = %q, want None", noTags)
	}

	// ListTagsForResource path also returns them.
	arn := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-task-definition",
		"--task-definition", "cli-taskdef-tags",
		"--query", "taskDefinition.taskDefinitionArn", "--output", "text")))
	listed := strings.TrimSpace(runCLI(t, awsCLI("ecs", "list-tags-for-resource",
		"--resource-arn", arn, "--query", "tags[?key=='env'].value | [0]", "--output", "text")))
	if listed != "ci" {
		t.Fatalf("list-tags-for-resource env tag = %q, want ci", listed)
	}
}
