package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigwtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIGateway_Stages covers CreateStage, GetStage, GetStages, UpdateStage,
// DeleteStage, a CreateDeployment that names a stage, a canary deployment,
// DeleteDeployment of a deployment a stage uses, and stage tags through
// TagResource, GetTags and UntagResource.
func TestAPIGateway_Stages(t *testing.T) {
	t.Parallel()
	c := apigwClient()

	api, err := c.CreateRestApi(ctx, &apigateway.CreateRestApiInput{Name: aws.String("sdk-stages")})
	require.NoError(t, err)
	apiId := api.Id
	t.Cleanup(func() { _, _ = c.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: apiId}) })
	_, err = c.PutMethod(ctx, &apigateway.PutMethodInput{
		RestApiId: apiId, ResourceId: api.RootResourceId, HttpMethod: aws.String("GET"), AuthorizationType: aws.String("NONE"),
	})
	require.NoError(t, err)
	_, err = c.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId: apiId, ResourceId: api.RootResourceId, HttpMethod: aws.String("GET"), Type: apigwtypes.IntegrationTypeMock,
	})
	require.NoError(t, err)

	dep, err := c.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{RestApiId: apiId})
	require.NoError(t, err)

	_, err = c.CreateStage(ctx, &apigateway.CreateStageInput{
		RestApiId: apiId, StageName: aws.String("prod"), DeploymentId: aws.String("absent0000"),
	})
	var notFound *apigwtypes.NotFoundException
	require.ErrorAs(t, err, &notFound, "CreateStage with an unknown deployment")

	stage, err := c.CreateStage(ctx, &apigateway.CreateStageInput{
		RestApiId:           apiId,
		StageName:           aws.String("prod"),
		DeploymentId:        dep.Id,
		Description:         aws.String("production"),
		Variables:           map[string]string{"backend": "blue"},
		TracingEnabled:      true,
		CacheClusterEnabled: true,
		Tags:                map[string]string{"env": "prod"},
	})
	require.NoError(t, err)
	assert.Equal(t, "production", aws.ToString(stage.Description))
	assert.Equal(t, map[string]string{"backend": "blue"}, stage.Variables)
	assert.True(t, stage.TracingEnabled)
	assert.True(t, stage.CacheClusterEnabled)
	assert.Equal(t, apigwtypes.CacheClusterSizeSize0Point5Gb, stage.CacheClusterSize)
	assert.Equal(t, map[string]string{"env": "prod"}, stage.Tags)
	assert.NotNil(t, stage.LastUpdatedDate)

	_, err = c.CreateStage(ctx, &apigateway.CreateStageInput{RestApiId: apiId, StageName: aws.String("prod"), DeploymentId: dep.Id})
	var conflict *apigwtypes.ConflictException
	require.ErrorAs(t, err, &conflict, "CreateStage of an existing stage")

	require.Eventually(t, func() bool {
		got, err := c.GetStage(ctx, &apigateway.GetStageInput{RestApiId: apiId, StageName: aws.String("prod")})
		return err == nil && got.CacheClusterStatus == apigwtypes.CacheClusterStatusAvailable
	}, 10*time.Second, 50*time.Millisecond, "the stage cache cluster becomes AVAILABLE")

	stageArn := "arn:aws:apigateway:us-east-1::/restapis/" + aws.ToString(apiId) + "/stages/prod"
	_, err = c.TagResource(ctx, &apigateway.TagResourceInput{ResourceArn: aws.String(stageArn), Tags: map[string]string{"team": "api"}})
	require.NoError(t, err)
	tags, err := c.GetTags(ctx, &apigateway.GetTagsInput{ResourceArn: aws.String(stageArn)})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"env": "prod", "team": "api"}, tags.Tags)
	_, err = c.UntagResource(ctx, &apigateway.UntagResourceInput{ResourceArn: aws.String(stageArn), TagKeys: []string{"env"}})
	require.NoError(t, err)
	tags, err = c.GetTags(ctx, &apigateway.GetTagsInput{ResourceArn: aws.String(stageArn)})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"team": "api"}, tags.Tags)
	apiTags, err := c.GetTags(ctx, &apigateway.GetTagsInput{ResourceArn: aws.String("arn:aws:apigateway:us-east-1::/restapis/" + aws.ToString(apiId))})
	require.NoError(t, err)
	assert.Empty(t, apiTags.Tags, "a stage tag stays off its REST API")

	updated, err := c.UpdateStage(ctx, &apigateway.UpdateStageInput{
		RestApiId: apiId, StageName: aws.String("prod"),
		PatchOperations: []apigwtypes.PatchOperation{
			apigwPatch("/description", "live"),
			apigwPatch("/variables/backend", "green"),
			{Op: apigwtypes.OpAdd, Path: aws.String("/variables/region"), Value: aws.String("east")},
			apigwPatch("/accessLogSettings/destinationArn", "arn:aws:logs:us-east-1:123456789012:log-group:access"),
			apigwPatch("/accessLogSettings/format", "$context.requestId"),
			apigwPatch("/*/*/metrics/enabled", "true"),
			apigwPatch("/*/*/logging/loglevel", "INFO"),
			apigwPatch("/~1items/GET/throttling/burstLimit", "50"),
			apigwPatch("/~1items/GET/throttling/rateLimit", "25.5"),
			apigwPatch("/cacheClusterEnabled", "false"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "live", aws.ToString(updated.Description))
	assert.Equal(t, map[string]string{"backend": "green", "region": "east"}, updated.Variables)
	require.NotNil(t, updated.AccessLogSettings)
	assert.Equal(t, "$context.requestId", aws.ToString(updated.AccessLogSettings.Format))
	assert.True(t, updated.MethodSettings["*/*"].MetricsEnabled)
	assert.Equal(t, "INFO", aws.ToString(updated.MethodSettings["*/*"].LoggingLevel))
	assert.Equal(t, int32(50), updated.MethodSettings["~1items/GET"].ThrottlingBurstLimit)
	assert.InDelta(t, 25.5, updated.MethodSettings["~1items/GET"].ThrottlingRateLimit, 0)
	assert.Equal(t, int32(300), updated.MethodSettings["~1items/GET"].CacheTtlInSeconds, "a new override starts from the documented 300-second TTL")
	assert.False(t, updated.CacheClusterEnabled)
	assert.Equal(t, apigwtypes.CacheClusterStatusDeleteInProgress, updated.CacheClusterStatus)

	updated, err = c.UpdateStage(ctx, &apigateway.UpdateStageInput{
		RestApiId: apiId, StageName: aws.String("prod"),
		PatchOperations: []apigwtypes.PatchOperation{
			{Op: apigwtypes.OpRemove, Path: aws.String("/variables/region")},
			{Op: apigwtypes.OpRemove, Path: aws.String("/~1items/GET")},
			{Op: apigwtypes.OpRemove, Path: aws.String("/accessLogSettings")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"backend": "green"}, updated.Variables)
	assert.NotContains(t, updated.MethodSettings, "~1items/GET")
	assert.Nil(t, updated.AccessLogSettings)

	_, err = c.UpdateStage(ctx, &apigateway.UpdateStageInput{
		RestApiId: apiId, StageName: aws.String("prod"),
		PatchOperations: []apigwtypes.PatchOperation{{Op: apigwtypes.OpRemove, Path: aws.String("/description")}},
	})
	var badRequest *apigwtypes.BadRequestException
	require.ErrorAs(t, err, &badRequest, "remove is not supported on /description")
	_, err = c.UpdateStage(ctx, &apigateway.UpdateStageInput{
		RestApiId: apiId, StageName: aws.String("prod"),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/cacheClusterSize", "2.0")},
	})
	require.ErrorAs(t, err, &badRequest, "2.0 is not a cache cluster size")

	canary, err := c.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{
		RestApiId: apiId, StageName: aws.String("prod"),
		CanarySettings: &apigwtypes.DeploymentCanarySettings{PercentTraffic: 10},
	})
	require.NoError(t, err)
	got, err := c.GetStage(ctx, &apigateway.GetStageInput{RestApiId: apiId, StageName: aws.String("prod")})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(dep.Id), aws.ToString(got.DeploymentId), "a canary deployment leaves the stage's deployment in place")
	require.NotNil(t, got.CanarySettings)
	assert.Equal(t, aws.ToString(canary.Id), aws.ToString(got.CanarySettings.DeploymentId))
	assert.InDelta(t, 10.0, got.CanarySettings.PercentTraffic, 0)

	promoted, err := c.UpdateStage(ctx, &apigateway.UpdateStageInput{
		RestApiId: apiId, StageName: aws.String("prod"),
		PatchOperations: []apigwtypes.PatchOperation{
			{Op: apigwtypes.OpCopy, From: aws.String("/canarySettings/deploymentId"), Path: aws.String("/deploymentId")},
			{Op: apigwtypes.OpRemove, Path: aws.String("/canarySettings")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(canary.Id), aws.ToString(promoted.DeploymentId))
	assert.Nil(t, promoted.CanarySettings)

	_, err = c.DeleteDeployment(ctx, &apigateway.DeleteDeploymentInput{RestApiId: apiId, DeploymentId: canary.Id})
	require.ErrorAs(t, err, &badRequest, "DeleteDeployment of a deployment a stage uses")
	_, err = c.DeleteDeployment(ctx, &apigateway.DeleteDeploymentInput{RestApiId: apiId, DeploymentId: dep.Id})
	require.NoError(t, err)

	beta, err := c.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{
		RestApiId: apiId, StageName: aws.String("beta"), StageDescription: aws.String("beta stage"),
		Variables: map[string]string{"backend": "beta"},
	})
	require.NoError(t, err)
	betaStage, err := c.GetStage(ctx, &apigateway.GetStageInput{RestApiId: apiId, StageName: aws.String("beta")})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(beta.Id), aws.ToString(betaStage.DeploymentId))
	assert.Equal(t, "beta stage", aws.ToString(betaStage.Description))
	assert.Equal(t, map[string]string{"backend": "beta"}, betaStage.Variables)

	byDeployment, err := c.GetStages(ctx, &apigateway.GetStagesInput{RestApiId: apiId, DeploymentId: beta.Id})
	require.NoError(t, err)
	require.Len(t, byDeployment.Item, 1)
	assert.Equal(t, "beta", aws.ToString(byDeployment.Item[0].StageName))

	_, err = c.DeleteStage(ctx, &apigateway.DeleteStageInput{RestApiId: apiId, StageName: aws.String("beta")})
	require.NoError(t, err)
	_, err = c.GetStage(ctx, &apigateway.GetStageInput{RestApiId: apiId, StageName: aws.String("beta")})
	require.ErrorAs(t, err, &notFound)
	_, err = c.GetTags(ctx, &apigateway.GetTagsInput{ResourceArn: aws.String("arn:aws:apigateway:us-east-1::/restapis/" + aws.ToString(apiId) + "/stages/beta")})
	require.ErrorAs(t, err, &notFound, "GetTags of a deleted stage")
}
