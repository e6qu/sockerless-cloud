package aws_cli_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECSCLI_DeploymentConfiguration drives the deployment-configuration
// runtime via the AWS CLI: a real failed task trips a count-based circuit
// breaker, and the service returns to its last completed task definition.
func TestECSCLI_DeploymentConfiguration(t *testing.T) {
	cluster := "cli-deploycfg-cluster"
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	stable := strings.TrimSpace(runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-deploycfg-task",
		"--container-definitions", `[{"name":"app","image":"`+containerCommandImage+`","command":["hold"],"memory":128}]`,
		"--query", "taskDefinition.taskDefinitionArn", "--output", "text")))
	runCLI(t, awsCLI("ecs", "create-service",
		"--cluster", cluster, "--service-name", "cli-deploycfg-svc",
		"--task-definition", stable, "--desired-count", "1",
		"--deployment-configuration", `{"deploymentCircuitBreaker":{"enable":true,"rollback":true,"thresholdConfiguration":{"type":"COUNT","value":1}},"maximumPercent":200,"minimumHealthyPercent":100}`))
	cleanupCLIService(t, cluster, "cli-deploycfg-svc")

	require.Eventually(t, func() bool {
		out := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-services",
			"--cluster", cluster, "--services", "cli-deploycfg-svc",
			"--query", "services[0].[taskDefinition,deployments[0].rolloutState]",
			"--output", "text")))
		return strings.Contains(out, stable) && strings.Contains(out, "COMPLETED")
	}, time.Minute, 100*time.Millisecond)

	failing := strings.TrimSpace(runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-deploycfg-task",
		"--container-definitions", `[{"name":"app","image":"`+containerCommandImage+`","command":["not-a-supported-command"],"memory":128}]`,
		"--query", "taskDefinition.taskDefinitionArn", "--output", "text")))
	runCLI(t, awsCLI("ecs", "update-service",
		"--cluster", cluster, "--service", "cli-deploycfg-svc",
		"--task-definition", failing))

	var lastDescription string
	rolledBack := assert.Eventually(t, func() bool {
		out := runCLI(t, awsCLI("ecs", "describe-services",
			"--cluster", cluster, "--services", "cli-deploycfg-svc", "--output", "json"))
		lastDescription = out
		var response struct {
			Services []struct {
				TaskDefinition string `json:"taskDefinition"`
				Deployments    []struct {
					RolloutState string `json:"rolloutState"`
				} `json:"deployments"`
				Events []struct {
					Message string `json:"message"`
				} `json:"events"`
			} `json:"services"`
		}
		if json.Unmarshal([]byte(out), &response) != nil || len(response.Services) != 1 ||
			response.Services[0].TaskDefinition != stable ||
			len(response.Services[0].Deployments) < 2 ||
			response.Services[0].Deployments[0].RolloutState != "COMPLETED" {
			return false
		}
		for _, event := range response.Services[0].Events {
			if strings.Contains(event.Message, "deployment rollback completed") {
				return true
			}
		}
		return false
	}, time.Minute, 100*time.Millisecond)
	require.Truef(t, rolledBack, "last DescribeServices response: %s", lastDescription)
}
