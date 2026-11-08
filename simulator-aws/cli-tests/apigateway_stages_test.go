package aws_cli_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type apigwCLIStage struct {
	DeploymentID        string            `json:"deploymentId"`
	Description         string            `json:"description"`
	CacheClusterEnabled bool              `json:"cacheClusterEnabled"`
	CacheClusterSize    string            `json:"cacheClusterSize"`
	CacheClusterStatus  string            `json:"cacheClusterStatus"`
	Variables           map[string]string `json:"variables"`
	TracingEnabled      bool              `json:"tracingEnabled"`
	Tags                map[string]string `json:"tags"`
	MethodSettings      map[string]struct {
		MetricsEnabled       bool   `json:"metricsEnabled"`
		LoggingLevel         string `json:"loggingLevel"`
		ThrottlingBurstLimit int    `json:"throttlingBurstLimit"`
		CacheTtlInSeconds    int    `json:"cacheTtlInSeconds"`
	} `json:"methodSettings"`
	AccessLogSettings *struct {
		DestinationArn string `json:"destinationArn"`
		Format         string `json:"format"`
	} `json:"accessLogSettings"`
	CanarySettings *struct {
		DeploymentID   string  `json:"deploymentId"`
		PercentTraffic float64 `json:"percentTraffic"`
	} `json:"canarySettings"`
}

// TestAPIGatewayCLIStages covers create-stage, get-stage, get-stages,
// update-stage and delete-stage, create-deployment naming a stage, a canary
// deployment, delete-deployment of a deployment a stage uses, and stage tags
// through tag-resource, get-tags and untag-resource.
func TestAPIGatewayCLIStages(t *testing.T) {
	t.Parallel()
	var api struct {
		ID             string `json:"id"`
		RootResourceID string `json:"rootResourceId"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "create-rest-api", "--name", "cli-stages"))), &api))
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigateway", "delete-rest-api", "--rest-api-id", api.ID)) })
	runCLI(t, awsCLI("apigateway", "put-method", "--rest-api-id", api.ID, "--resource-id", api.RootResourceID,
		"--http-method", "GET", "--authorization-type", "NONE"))
	runCLI(t, awsCLI("apigateway", "put-integration", "--rest-api-id", api.ID, "--resource-id", api.RootResourceID,
		"--http-method", "GET", "--type", "MOCK"))
	var dep struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "create-deployment", "--rest-api-id", api.ID))), &dep))

	var stage apigwCLIStage
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "create-stage",
		"--rest-api-id", api.ID, "--stage-name", "prod", "--deployment-id", dep.ID,
		"--description", "production", "--variables", "backend=blue", "--tracing-enabled",
		"--cache-cluster-enabled", "--cache-cluster-size", "1.6", "--tags", "env=prod"))), &stage))
	assert.Equal(t, "production", stage.Description)
	assert.Equal(t, map[string]string{"backend": "blue"}, stage.Variables)
	assert.True(t, stage.TracingEnabled)
	assert.Equal(t, "1.6", stage.CacheClusterSize)
	assert.Equal(t, map[string]string{"env": "prod"}, stage.Tags)
	assert.Contains(t, runCLIExpectError(t, awsCLI("apigateway", "create-stage",
		"--rest-api-id", api.ID, "--stage-name", "prod", "--deployment-id", dep.ID)), "ConflictException")

	require.Eventually(t, func() bool {
		var got apigwCLIStage
		out, err := awsCLI("apigateway", "get-stage", "--rest-api-id", api.ID, "--stage-name", "prod").Output()
		return err == nil && json.Unmarshal(out, &got) == nil && got.CacheClusterStatus == "AVAILABLE"
	}, 30*time.Second, 200*time.Millisecond, "the stage cache cluster becomes AVAILABLE")

	stageArn := "arn:aws:apigateway:us-east-1::/restapis/" + api.ID + "/stages/prod"
	runCLI(t, awsCLI("apigateway", "tag-resource", "--resource-arn", stageArn, "--tags", "team=api"))
	runCLI(t, awsCLI("apigateway", "untag-resource", "--resource-arn", stageArn, "--tag-keys", "env"))
	var tags struct {
		Tags map[string]string `json:"tags"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "get-tags", "--resource-arn", stageArn))), &tags))
	assert.Equal(t, map[string]string{"team": "api"}, tags.Tags)

	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "update-stage",
		"--rest-api-id", api.ID, "--stage-name", "prod", "--patch-operations", `[
			{"op":"replace","path":"/description","value":"live"},
			{"op":"replace","path":"/variables/backend","value":"green"},
			{"op":"replace","path":"/accessLogSettings/destinationArn","value":"arn:aws:logs:us-east-1:123456789012:log-group:access"},
			{"op":"replace","path":"/accessLogSettings/format","value":"$context.requestId"},
			{"op":"replace","path":"/*/*/metrics/enabled","value":"true"},
			{"op":"replace","path":"/*/*/logging/loglevel","value":"ERROR"},
			{"op":"replace","path":"/~1items/GET/throttling/burstLimit","value":"40"}
		]`))), &stage))
	assert.Equal(t, "live", stage.Description)
	assert.Equal(t, map[string]string{"backend": "green"}, stage.Variables)
	require.NotNil(t, stage.AccessLogSettings)
	assert.Equal(t, "$context.requestId", stage.AccessLogSettings.Format)
	assert.True(t, stage.MethodSettings["*/*"].MetricsEnabled)
	assert.Equal(t, "ERROR", stage.MethodSettings["*/*"].LoggingLevel)
	assert.Equal(t, 40, stage.MethodSettings["~1items/GET"].ThrottlingBurstLimit)
	assert.Equal(t, 300, stage.MethodSettings["~1items/GET"].CacheTtlInSeconds, "a new override starts from the documented 300-second TTL")
	assert.Contains(t, runCLIExpectError(t, awsCLI("apigateway", "update-stage",
		"--rest-api-id", api.ID, "--stage-name", "prod",
		"--patch-operations", "op=remove,path=/description")), "BadRequestException")

	var canary struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "create-deployment",
		"--rest-api-id", api.ID, "--stage-name", "prod", "--canary-settings", "percentTraffic=20"))), &canary))
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "get-stage",
		"--rest-api-id", api.ID, "--stage-name", "prod"))), &stage))
	assert.Equal(t, dep.ID, stage.DeploymentID)
	require.NotNil(t, stage.CanarySettings)
	assert.Equal(t, canary.ID, stage.CanarySettings.DeploymentID)
	assert.InDelta(t, 20.0, stage.CanarySettings.PercentTraffic, 0)
	assert.Contains(t, runCLIExpectError(t, awsCLI("apigateway", "delete-deployment",
		"--rest-api-id", api.ID, "--deployment-id", canary.ID)), "BadRequestException")

	var beta struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "create-deployment",
		"--rest-api-id", api.ID, "--stage-name", "beta", "--stage-description", "beta stage"))), &beta))
	var stages struct {
		Item []struct {
			StageName string `json:"stageName"`
		} `json:"item"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "get-stages",
		"--rest-api-id", api.ID, "--deployment-id", beta.ID))), &stages))
	require.Len(t, stages.Item, 1)
	assert.Equal(t, "beta", stages.Item[0].StageName)

	runCLI(t, awsCLI("apigateway", "delete-stage", "--rest-api-id", api.ID, "--stage-name", "beta"))
	assert.Contains(t, runCLIExpectError(t, awsCLI("apigateway", "get-stage",
		"--rest-api-id", api.ID, "--stage-name", "beta")), "NotFoundException")
}
