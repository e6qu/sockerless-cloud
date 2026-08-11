package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIGW_MethodResponse_RoundTrip exercises the API Gateway v1
// `aws_api_gateway_method_response` lifecycle: CreateRestApi →
// (root resource auto-created) → PutMethod → PutMethodResponse →
// GetMethodResponse → DeleteMethodResponse. Same shape for
// integration responses.
func TestAPIGW_MethodResponse_RoundTrip(t *testing.T) {
	c := apigwClient()

	api, err := c.CreateRestApi(ctx, &apigateway.CreateRestApiInput{Name: aws.String("mr-test")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: api.Id}) })

	// Find the auto-created root resource.
	resources, err := c.GetResources(ctx, &apigateway.GetResourcesInput{RestApiId: api.Id})
	require.NoError(t, err)
	require.NotEmpty(t, resources.Items, "root resource auto-created on CreateRestApi")
	rootID := resources.Items[0].Id

	_, err = c.PutMethod(ctx, &apigateway.PutMethodInput{
		RestApiId:         api.Id,
		ResourceId:        rootID,
		HttpMethod:        aws.String("GET"),
		AuthorizationType: aws.String("NONE"),
	})
	require.NoError(t, err)

	// PutMethodResponse for 200.
	_, err = c.PutMethodResponse(ctx, &apigateway.PutMethodResponseInput{
		RestApiId:  api.Id,
		ResourceId: rootID,
		HttpMethod: aws.String("GET"),
		StatusCode: aws.String("200"),
		ResponseModels: map[string]string{
			"application/json": "Empty",
		},
	})
	require.NoError(t, err)

	got, err := c.GetMethodResponse(ctx, &apigateway.GetMethodResponseInput{
		RestApiId:  api.Id,
		ResourceId: rootID,
		HttpMethod: aws.String("GET"),
		StatusCode: aws.String("200"),
	})
	require.NoError(t, err)
	assert.Equal(t, "200", aws.ToString(got.StatusCode))
	assert.Equal(t, "Empty", got.ResponseModels["application/json"])

	_, err = c.DeleteMethodResponse(ctx, &apigateway.DeleteMethodResponseInput{
		RestApiId:  api.Id,
		ResourceId: rootID,
		HttpMethod: aws.String("GET"),
		StatusCode: aws.String("200"),
	})
	require.NoError(t, err)

	_, err = c.GetMethodResponse(ctx, &apigateway.GetMethodResponseInput{
		RestApiId:  api.Id,
		ResourceId: rootID,
		HttpMethod: aws.String("GET"),
		StatusCode: aws.String("200"),
	})
	require.Error(t, err, "deleted method response must 404 on read")
}

// TestAPIGW_IntegrationResponse_RoundTrip exercises the same shape
// for integration responses.
func TestAPIGW_IntegrationResponse_RoundTrip(t *testing.T) {
	c := apigwClient()

	api, err := c.CreateRestApi(ctx, &apigateway.CreateRestApiInput{Name: aws.String("ir-test")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: api.Id}) })

	resources, err := c.GetResources(ctx, &apigateway.GetResourcesInput{RestApiId: api.Id})
	require.NoError(t, err)
	rootID := resources.Items[0].Id

	_, err = c.PutMethod(ctx, &apigateway.PutMethodInput{
		RestApiId:         api.Id,
		ResourceId:        rootID,
		HttpMethod:        aws.String("POST"),
		AuthorizationType: aws.String("NONE"),
	})
	require.NoError(t, err)

	_, err = c.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId:  api.Id,
		ResourceId: rootID,
		HttpMethod: aws.String("POST"),
		Type:       "MOCK",
	})
	require.NoError(t, err)

	_, err = c.PutIntegrationResponse(ctx, &apigateway.PutIntegrationResponseInput{
		RestApiId:        api.Id,
		ResourceId:       rootID,
		HttpMethod:       aws.String("POST"),
		StatusCode:       aws.String("200"),
		SelectionPattern: aws.String(""),
		ResponseTemplates: map[string]string{
			"application/json": `{"ok":true}`,
		},
	})
	require.NoError(t, err)

	got, err := c.GetIntegrationResponse(ctx, &apigateway.GetIntegrationResponseInput{
		RestApiId:  api.Id,
		ResourceId: rootID,
		HttpMethod: aws.String("POST"),
		StatusCode: aws.String("200"),
	})
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, got.ResponseTemplates["application/json"])

	_, err = c.DeleteIntegrationResponse(ctx, &apigateway.DeleteIntegrationResponseInput{
		RestApiId:  api.Id,
		ResourceId: rootID,
		HttpMethod: aws.String("POST"),
		StatusCode: aws.String("200"),
	})
	require.NoError(t, err)
}
