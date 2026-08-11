package aws_cli_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pollECSTaskStopped polls describe-tasks until lastStatus is STOPPED or 60s
// elapses. Returns the final describe-tasks JSON output. A fixed sleep before
// the describe was the original approach, but it races on slow CI runners.
func pollECSTaskStopped(t *testing.T, cluster, taskArn string) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		out := runCLI(t, awsCLI("ecs", "describe-tasks",
			"--cluster", cluster,
			"--tasks", taskArn,
			"--output", "json",
		))
		var result struct {
			Tasks []struct {
				LastStatus string `json:"lastStatus"`
			} `json:"tasks"`
		}
		parseJSON(t, out, &result)
		if len(result.Tasks) > 0 && result.Tasks[0].LastStatus == "STOPPED" {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("ECS task %s did not reach STOPPED within 60s", taskArn)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func TestECS_CLI_ArithmeticEval(t *testing.T) {
	subnetID := createCLIECSTestSubnet(t, 140)

	// Create cluster
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", "cli-arith-cluster"))

	// Register task definition with eval-arithmetic command
	out := runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-arith-task",
		"--requires-compatibilities", "FARGATE",
		"--network-mode", "awsvpc",
		"--cpu", "256",
		"--memory", "512",
		"--container-definitions", fmt.Sprintf(`[{
			"name": "app",
			"image": %q,
			"command": ["(3 + 4) * 2"],
			"logConfiguration": {
				"logDriver": "awslogs",
				"options": {
					"awslogs-group": "/ecs/cli-arith-task",
					"awslogs-stream-prefix": "ecs"
				}
			}
		}]`, evalImageName),
		"--output", "json",
	))

	var tdResult struct {
		TaskDefinition struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"taskDefinition"`
	}
	parseJSON(t, out, &tdResult)
	require.NotEmpty(t, tdResult.TaskDefinition.TaskDefinitionArn)

	// Run task
	out = runCLI(t, awsCLI("ecs", "run-task",
		"--cluster", "cli-arith-cluster",
		"--task-definition", tdResult.TaskDefinition.TaskDefinitionArn,
		"--launch-type", "FARGATE",
		"--count", "1",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--output", "json",
	))

	var runResult struct {
		Tasks []struct {
			TaskArn string `json:"taskArn"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &runResult)
	require.Len(t, runResult.Tasks, 1)
	taskArn := runResult.Tasks[0].TaskArn

	// Poll until the task reaches STOPPED (or timeout).
	out = pollECSTaskStopped(t, "cli-arith-cluster", taskArn)

	var descResult struct {
		Tasks []struct {
			LastStatus string `json:"lastStatus"`
			Containers []struct {
				ExitCode *int `json:"exitCode"`
			} `json:"containers"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &descResult)
	require.Len(t, descResult.Tasks, 1)
	assert.Equal(t, "STOPPED", descResult.Tasks[0].LastStatus)
	require.NotEmpty(t, descResult.Tasks[0].Containers)
	require.NotNil(t, descResult.Tasks[0].Containers[0].ExitCode)
	assert.Equal(t, 0, *descResult.Tasks[0].Containers[0].ExitCode)

	// Verify CloudWatch logs contain the result
	out = runCLI(t, awsCLI("logs", "filter-log-events",
		"--log-group-name", "/ecs/cli-arith-task",
		"--output", "json",
	))

	var logResult struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	parseJSON(t, out, &logResult)
	require.NotEmpty(t, logResult.Events)

	found := false
	for _, e := range logResult.Events {
		if strings.Contains(e.Message, "14") {
			found = true
		}
	}
	assert.True(t, found, "expected '14' in CloudWatch logs")
}

func TestECS_CLI_ArithmeticInvalid(t *testing.T) {
	subnetID := createCLIECSTestSubnet(t, 141)

	// Create cluster
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", "cli-arith-fail-cluster"))

	// Register task definition with invalid expression
	out := runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-arith-fail-task",
		"--requires-compatibilities", "FARGATE",
		"--network-mode", "awsvpc",
		"--cpu", "256",
		"--memory", "512",
		"--container-definitions", fmt.Sprintf(`[{
			"name": "app",
			"image": %q,
			"command": ["3 +"],
			"logConfiguration": {
				"logDriver": "awslogs",
				"options": {
					"awslogs-group": "/ecs/cli-arith-fail-task",
					"awslogs-stream-prefix": "ecs"
				}
			}
		}]`, evalImageName),
		"--output", "json",
	))

	var tdResult struct {
		TaskDefinition struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"taskDefinition"`
	}
	parseJSON(t, out, &tdResult)

	// Run task
	out = runCLI(t, awsCLI("ecs", "run-task",
		"--cluster", "cli-arith-fail-cluster",
		"--task-definition", tdResult.TaskDefinition.TaskDefinitionArn,
		"--launch-type", "FARGATE",
		"--count", "1",
		"--network-configuration", `awsvpcConfiguration={subnets=[`+subnetID+`]}`,
		"--output", "json",
	))

	var runResult struct {
		Tasks []struct {
			TaskArn string `json:"taskArn"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &runResult)
	require.Len(t, runResult.Tasks, 1)
	taskArn := runResult.Tasks[0].TaskArn

	// Poll until the task reaches STOPPED (or timeout).
	out = pollECSTaskStopped(t, "cli-arith-fail-cluster", taskArn)

	var descResult struct {
		Tasks []struct {
			LastStatus string `json:"lastStatus"`
			Containers []struct {
				ExitCode *int `json:"exitCode"`
			} `json:"containers"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &descResult)
	require.Len(t, descResult.Tasks, 1)
	assert.Equal(t, "STOPPED", descResult.Tasks[0].LastStatus)
	require.NotEmpty(t, descResult.Tasks[0].Containers)
	require.NotNil(t, descResult.Tasks[0].Containers[0].ExitCode)
	assert.Equal(t, 1, *descResult.Tasks[0].Containers[0].ExitCode)
}

func createCLIECSTestSubnet(t *testing.T, startOctet int) string {
	t.Helper()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	octet := unusedDockerVPCOctet(t, startOctet, nil)
	vpcID, subnetID := mkVPCSubnet(t, q, vpcCIDR(octet), subnetCIDR(octet))
	t.Cleanup(func() {
		q("ec2", "delete-subnet", "--subnet-id", subnetID)
		q("ec2", "delete-vpc", "--vpc-id", vpcID)
		rmDockerNetworks(ecsVPCNet(vpcID), ecsVPCNet(vpcID)+"-egress")
	})
	return subnetID
}
