package aws_sdk_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIGwV2_CreateDeployment_RoutesPastS3 locks the invariant from
// POST /v2/apis/{apiId}/deployments must
// route to the API Gateway v2 handler (registered explicitly), not
// fall through to S3's wildcard POST /{bucket}/{key...} dispatcher.
// Pre-fix, the request hit handleS3PostObjectDispatch and got an
// S3-shaped "InvalidRequest: POST on an object requires ?uploads"
// envelope. Post-fix, the v2 deployment endpoint is registered AND
// S3 gates by known-bucket so a regression here can't slip through.
func TestAPIGwV2_CreateDeployment_RoutesPastS3(t *testing.T) {
	ctx := context.Background()
	c := apigwv2Client()

	api, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name:         aws.String("tf-apigw-deploy-test"),
		ProtocolType: types.ProtocolTypeHttp,
	})
	require.NoError(t, err, "CreateApi must succeed")
	t.Cleanup(func() {
		_, _ = c.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: api.ApiId})
	})

	dep, err := c.CreateDeployment(ctx, &apigatewayv2.CreateDeploymentInput{
		ApiId:       api.ApiId,
		Description: aws.String("initial deploy"),
	})
	require.NoError(t, err, "CreateDeployment must succeed (routed to v2 handler, not S3 wildcard)")
	require.NotNil(t, dep.DeploymentId)
	assert.Equal(t, types.DeploymentStatusDeployed, dep.DeploymentStatus,
		"sim emits DEPLOYED status inline since there's no async deploy work")

	get, err := c.GetDeployment(ctx, &apigatewayv2.GetDeploymentInput{
		ApiId:        api.ApiId,
		DeploymentId: dep.DeploymentId,
	})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(dep.DeploymentId), aws.ToString(get.DeploymentId))

	list, err := c.GetDeployments(ctx, &apigatewayv2.GetDeploymentsInput{ApiId: api.ApiId})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)

	_, err = c.DeleteDeployment(ctx, &apigatewayv2.DeleteDeploymentInput{
		ApiId:        api.ApiId,
		DeploymentId: dep.DeploymentId,
	})
	require.NoError(t, err)
}
