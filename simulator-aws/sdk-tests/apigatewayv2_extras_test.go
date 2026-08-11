package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	apigwv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIGatewayV2_IntegrationResponsesSDK exercises the integration-response
// child surface: CreateIntegrationResponse, GetIntegrationResponse,
// GetIntegrationResponses, UpdateIntegrationResponse, DeleteIntegrationResponse.
func TestAPIGatewayV2_IntegrationResponsesSDK(t *testing.T) {
	c := apigwv2Client()
	api, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name:                     aws.String("ir-api"),
		ProtocolType:             apigwv2types.ProtocolTypeWebsocket,
		RouteSelectionExpression: aws.String("$request.body.action"),
	})
	require.NoError(t, err)
	apiId := aws.ToString(api.ApiId)
	t.Cleanup(func() { _, _ = c.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: aws.String(apiId)}) })

	integ, err := c.CreateIntegration(ctx, &apigatewayv2.CreateIntegrationInput{
		ApiId:           aws.String(apiId),
		IntegrationType: apigwv2types.IntegrationTypeMock,
	})
	require.NoError(t, err)
	integId := aws.ToString(integ.IntegrationId)

	ir, err := c.CreateIntegrationResponse(ctx, &apigatewayv2.CreateIntegrationResponseInput{
		ApiId:                  aws.String(apiId),
		IntegrationId:          aws.String(integId),
		IntegrationResponseKey: aws.String("$default"),
		ResponseTemplates:      map[string]string{"application/json": "{}"},
	})
	require.NoError(t, err)
	irId := aws.ToString(ir.IntegrationResponseId)
	require.NotEmpty(t, irId)
	assert.Equal(t, "$default", aws.ToString(ir.IntegrationResponseKey))

	got, err := c.GetIntegrationResponse(ctx, &apigatewayv2.GetIntegrationResponseInput{
		ApiId: aws.String(apiId), IntegrationId: aws.String(integId), IntegrationResponseId: aws.String(irId),
	})
	require.NoError(t, err)
	assert.Equal(t, irId, aws.ToString(got.IntegrationResponseId))

	list, err := c.GetIntegrationResponses(ctx, &apigatewayv2.GetIntegrationResponsesInput{
		ApiId: aws.String(apiId), IntegrationId: aws.String(integId),
	})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)

	upd, err := c.UpdateIntegrationResponse(ctx, &apigatewayv2.UpdateIntegrationResponseInput{
		ApiId: aws.String(apiId), IntegrationId: aws.String(integId), IntegrationResponseId: aws.String(irId),
		IntegrationResponseKey:      aws.String("$default"),
		TemplateSelectionExpression: aws.String("200"),
	})
	require.NoError(t, err)
	assert.Equal(t, "200", aws.ToString(upd.TemplateSelectionExpression))

	_, err = c.DeleteIntegrationResponse(ctx, &apigatewayv2.DeleteIntegrationResponseInput{
		ApiId: aws.String(apiId), IntegrationId: aws.String(integId), IntegrationResponseId: aws.String(irId),
	})
	require.NoError(t, err)

	_, err = c.GetIntegrationResponse(ctx, &apigatewayv2.GetIntegrationResponseInput{
		ApiId: aws.String(apiId), IntegrationId: aws.String(integId), IntegrationResponseId: aws.String(irId),
	})
	require.Error(t, err, "deleted integration response must 404")
}

// TestAPIGatewayV2_RouteResponsesSDK exercises the route-response child surface:
// CreateRouteResponse, GetRouteResponse, GetRouteResponses, UpdateRouteResponse,
// DeleteRouteResponse.
func TestAPIGatewayV2_RouteResponsesSDK(t *testing.T) {
	c := apigwv2Client()
	api, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name:                     aws.String("rr-api"),
		ProtocolType:             apigwv2types.ProtocolTypeWebsocket,
		RouteSelectionExpression: aws.String("$request.body.action"),
	})
	require.NoError(t, err)
	apiId := aws.ToString(api.ApiId)
	t.Cleanup(func() { _, _ = c.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: aws.String(apiId)}) })

	route, err := c.CreateRoute(ctx, &apigatewayv2.CreateRouteInput{
		ApiId: aws.String(apiId), RouteKey: aws.String("$default"),
	})
	require.NoError(t, err)
	routeId := aws.ToString(route.RouteId)

	rr, err := c.CreateRouteResponse(ctx, &apigatewayv2.CreateRouteResponseInput{
		ApiId: aws.String(apiId), RouteId: aws.String(routeId),
		RouteResponseKey: aws.String("$default"),
		ResponseParameters: map[string]apigwv2types.ParameterConstraints{
			"method.response.header.X-Test": {Required: aws.Bool(true)},
		},
	})
	require.NoError(t, err)
	rrId := aws.ToString(rr.RouteResponseId)
	require.NotEmpty(t, rrId)
	require.NotNil(t, rr.ResponseParameters["method.response.header.X-Test"])
	assert.True(t, aws.ToBool(rr.ResponseParameters["method.response.header.X-Test"].Required))

	got, err := c.GetRouteResponse(ctx, &apigatewayv2.GetRouteResponseInput{
		ApiId: aws.String(apiId), RouteId: aws.String(routeId), RouteResponseId: aws.String(rrId),
	})
	require.NoError(t, err)
	assert.Equal(t, rrId, aws.ToString(got.RouteResponseId))

	list, err := c.GetRouteResponses(ctx, &apigatewayv2.GetRouteResponsesInput{
		ApiId: aws.String(apiId), RouteId: aws.String(routeId),
	})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)

	upd, err := c.UpdateRouteResponse(ctx, &apigatewayv2.UpdateRouteResponseInput{
		ApiId: aws.String(apiId), RouteId: aws.String(routeId), RouteResponseId: aws.String(rrId),
		ModelSelectionExpression: aws.String("$default"),
	})
	require.NoError(t, err)
	assert.Equal(t, "$default", aws.ToString(upd.ModelSelectionExpression))

	_, err = c.DeleteRouteResponse(ctx, &apigatewayv2.DeleteRouteResponseInput{
		ApiId: aws.String(apiId), RouteId: aws.String(routeId), RouteResponseId: aws.String(rrId),
	})
	require.NoError(t, err)
}

// TestAPIGatewayV2_ImportExportTagsTemplateSDK exercises ImportApi, ReimportApi,
// ExportApi, GetModelTemplate, and GetTags.
func TestAPIGatewayV2_ImportExportTagsTemplateSDK(t *testing.T) {
	c := apigwv2Client()

	openapi := `{"openapi":"3.0.1","info":{"title":"imported-http-api","version":"1.0"},"paths":{"/pets":{"get":{"responses":{"200":{"description":"ok"}}}}}}`

	imp, err := c.ImportApi(ctx, &apigatewayv2.ImportApiInput{Body: aws.String(openapi)})
	require.NoError(t, err)
	apiId := aws.ToString(imp.ApiId)
	require.NotEmpty(t, apiId)
	assert.Equal(t, "imported-http-api", aws.ToString(imp.Name))
	t.Cleanup(func() { _, _ = c.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: aws.String(apiId)}) })

	reimp, err := c.ReimportApi(ctx, &apigatewayv2.ReimportApiInput{
		ApiId: aws.String(apiId),
		Body:  aws.String(strings.ReplaceAll(openapi, "imported-http-api", "reimported-http-api")),
	})
	require.NoError(t, err)
	assert.Equal(t, apiId, aws.ToString(reimp.ApiId), "reimport preserves the API id")
	assert.Equal(t, "reimported-http-api", aws.ToString(reimp.Name))

	exp, err := c.ExportApi(ctx, &apigatewayv2.ExportApiInput{
		ApiId:         aws.String(apiId),
		Specification: aws.String("OAS30"),
		OutputType:    aws.String("JSON"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, exp.Body)
	assert.Contains(t, string(exp.Body), "openapi")

	// GetModelTemplate: create a model first.
	model, err := c.CreateModel(ctx, &apigatewayv2.CreateModelInput{
		ApiId:       aws.String(apiId),
		Name:        aws.String("Pet"),
		ContentType: aws.String("application/json"),
		Schema:      aws.String(`{"type":"object"}`),
	})
	require.NoError(t, err)
	tmpl, err := c.GetModelTemplate(ctx, &apigatewayv2.GetModelTemplateInput{
		ApiId: aws.String(apiId), ModelId: model.ModelId,
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(tmpl.Value), "object")

	// GetTags against the API ARN — tags come from the tagged API.
	tagged, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name:         aws.String("tagged-api"),
		ProtocolType: apigwv2types.ProtocolTypeHttp,
		Tags:         map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	taggedId := aws.ToString(tagged.ApiId)
	t.Cleanup(func() { _, _ = c.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: aws.String(taggedId)}) })

	gt, err := c.GetTags(ctx, &apigatewayv2.GetTagsInput{
		ResourceArn: aws.String("arn:aws:apigateway:us-east-1::/apis/" + taggedId),
	})
	require.NoError(t, err)
	assert.Equal(t, "test", gt.Tags["env"])
}

// TestAPIGatewayV2_ConfigDeletesSDK exercises the per-stage / per-api config
// deletes: DeleteAccessLogSettings, DeleteCorsConfiguration,
// DeleteRouteRequestParameter, DeleteRouteSettings.
func TestAPIGatewayV2_ConfigDeletesSDK(t *testing.T) {
	c := apigwv2Client()
	api, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name:         aws.String("cfg-api"),
		ProtocolType: apigwv2types.ProtocolTypeHttp,
	})
	require.NoError(t, err)
	apiId := aws.ToString(api.ApiId)
	t.Cleanup(func() { _, _ = c.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: aws.String(apiId)}) })

	route, err := c.CreateRoute(ctx, &apigatewayv2.CreateRouteInput{
		ApiId: aws.String(apiId), RouteKey: aws.String("GET /pets"),
	})
	require.NoError(t, err)
	routeId := aws.ToString(route.RouteId)

	stage, err := c.CreateStage(ctx, &apigatewayv2.CreateStageInput{
		ApiId: aws.String(apiId), StageName: aws.String("prod"),
	})
	require.NoError(t, err)
	stageName := aws.ToString(stage.StageName)

	_, err = c.DeleteCorsConfiguration(ctx, &apigatewayv2.DeleteCorsConfigurationInput{ApiId: aws.String(apiId)})
	require.NoError(t, err)

	_, err = c.DeleteAccessLogSettings(ctx, &apigatewayv2.DeleteAccessLogSettingsInput{
		ApiId: aws.String(apiId), StageName: aws.String(stageName),
	})
	require.NoError(t, err)

	_, err = c.DeleteRouteRequestParameter(ctx, &apigatewayv2.DeleteRouteRequestParameterInput{
		ApiId: aws.String(apiId), RouteId: aws.String(routeId),
		RequestParameterKey: aws.String("route.request.querystring.id"),
	})
	require.NoError(t, err)

	_, err = c.DeleteRouteSettings(ctx, &apigatewayv2.DeleteRouteSettingsInput{
		ApiId: aws.String(apiId), StageName: aws.String(stageName),
		RouteKey: aws.String("GET /pets"),
	})
	require.NoError(t, err)
}
