package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigwtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	apigwv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func apigwv2RegionalConfig(t *testing.T, domain string) []apigwv2types.DomainNameConfiguration {
	return []apigwv2types.DomainNameConfiguration{{
		EndpointType:   apigwv2types.EndpointTypeRegional,
		CertificateArn: aws.String(importELBv2Certificate(t, domain)),
	}}
}

// TestAPIGatewayV2_DomainNamesShareTheV1Store expects a custom domain name
// either API version creates to be read, updated, tagged and deleted through
// the other, and CreateDomainName to refuse a certificate AWS Certificate
// Manager does not hold.
func TestAPIGatewayV2_DomainNamesShareTheV1Store(t *testing.T) {
	t.Parallel()
	v1 := apigwClient()
	v2 := apigwv2Client()

	_, err := v2.CreateDomainName(ctx, &apigatewayv2.CreateDomainNameInput{
		DomainName: aws.String("nocert.sdk-v2-domains.example.com"),
		DomainNameConfigurations: []apigwv2types.DomainNameConfiguration{{
			EndpointType:   apigwv2types.EndpointTypeRegional,
			CertificateArn: aws.String("arn:aws:acm:us-east-1:123456789012:certificate/00000000-0000-0000-0000-000000000000"),
		}},
	})
	var badRequest *apigwv2types.BadRequestException
	require.ErrorAs(t, err, &badRequest, "an unknown certificate")

	name := "http.sdk-v2-domains.example.com"
	created, err := v2.CreateDomainName(ctx, &apigatewayv2.CreateDomainNameInput{
		DomainName:               aws.String(name),
		DomainNameConfigurations: apigwv2RegionalConfig(t, name),
		Tags:                     map[string]string{"team": "http"},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = v2.DeleteDomainName(ctx, &apigatewayv2.DeleteDomainNameInput{DomainName: aws.String(name)})
	})
	require.Len(t, created.DomainNameConfigurations, 1)
	cfg := created.DomainNameConfigurations[0]
	assert.Equal(t, "Z1UJRXOUMOOFQ8", aws.ToString(cfg.HostedZoneId))
	assert.Equal(t, apigwv2types.DomainNameStatusAvailable, cfg.DomainNameStatus)
	assert.Equal(t, apigwv2types.RoutingModeApiMappingOnly, created.RoutingMode)

	asV1, err := v1.GetDomainName(ctx, &apigateway.GetDomainNameInput{DomainName: aws.String(name)})
	require.NoError(t, err, "the v1 API reads a domain name the v2 API created")
	assert.Equal(t, aws.ToString(cfg.ApiGatewayDomainName), aws.ToString(asV1.RegionalDomainName))
	assert.Equal(t, aws.ToString(cfg.CertificateArn), aws.ToString(asV1.RegionalCertificateArn))
	assert.Equal(t, apigwtypes.RoutingModeBasePathMappingOnly, asV1.RoutingMode)

	_, err = v1.CreateDomainName(ctx, &apigateway.CreateDomainNameInput{
		DomainName:             aws.String(name),
		RegionalCertificateArn: cfg.CertificateArn,
		EndpointConfiguration:  &apigwtypes.EndpointConfiguration{Types: []apigwtypes.EndpointType{apigwtypes.EndpointTypeRegional}},
	})
	var conflict *apigwtypes.ConflictException
	require.ErrorAs(t, err, &conflict, "the v1 API refuses a name the v2 API holds")

	renewed := importELBv2Certificate(t, name)
	updated, err := v2.UpdateDomainName(ctx, &apigatewayv2.UpdateDomainNameInput{
		DomainName: aws.String(name),
		DomainNameConfigurations: []apigwv2types.DomainNameConfiguration{{
			EndpointType:   apigwv2types.EndpointTypeRegional,
			CertificateArn: aws.String(renewed),
			SecurityPolicy: apigwv2types.SecurityPolicyTls12,
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, renewed, aws.ToString(updated.DomainNameConfigurations[0].CertificateArn))
	assert.Equal(t, aws.ToString(cfg.ApiGatewayDomainName), aws.ToString(updated.DomainNameConfigurations[0].ApiGatewayDomainName),
		"a certificate change keeps the Regional endpoint")

	arn := aws.ToString(created.DomainNameArn)
	_, err = v2.TagResource(ctx, &apigatewayv2.TagResourceInput{ResourceArn: aws.String(arn), Tags: map[string]string{"env": "prod"}})
	require.NoError(t, err)
	_, err = v2.UntagResource(ctx, &apigatewayv2.UntagResourceInput{ResourceArn: aws.String(arn), TagKeys: []string{"team"}})
	require.NoError(t, err)
	v1Tags, err := v1.GetTags(ctx, &apigateway.GetTagsInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"env": "prod"}, v1Tags.Tags, "the v1 API reads tags the v2 API set")

	edgeName := "edge.sdk-v2-domains.example.com"
	_, err = v1.CreateDomainName(ctx, &apigateway.CreateDomainNameInput{
		DomainName:     aws.String(edgeName),
		CertificateArn: aws.String(importELBv2Certificate(t, edgeName)),
	})
	require.NoError(t, err)
	edge, err := v2.GetDomainName(ctx, &apigatewayv2.GetDomainNameInput{DomainName: aws.String(edgeName)})
	require.NoError(t, err, "the v2 API reads a domain name the v1 API created")
	assert.Equal(t, apigwv2types.EndpointTypeEdge, edge.DomainNameConfigurations[0].EndpointType)
	assert.Equal(t, "Z2FDTNDATAQYW2", aws.ToString(edge.DomainNameConfigurations[0].HostedZoneId))

	api, err := v2.CreateApi(ctx, &apigatewayv2.CreateApiInput{Name: aws.String("sdk-v2-domains"), ProtocolType: apigwv2types.ProtocolTypeHttp})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = v2.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: api.ApiId}) })
	_, err = v2.CreateStage(ctx, &apigatewayv2.CreateStageInput{ApiId: api.ApiId, StageName: aws.String("prod")})
	require.NoError(t, err)
	_, err = v2.CreateApiMapping(ctx, &apigatewayv2.CreateApiMappingInput{DomainName: aws.String(edgeName), ApiId: api.ApiId, Stage: aws.String("prod")})
	require.NoError(t, err)
	_, err = v1.DeleteDomainName(ctx, &apigateway.DeleteDomainNameInput{DomainName: aws.String(edgeName)})
	require.NoError(t, err)
	_, err = v2.GetApiMappings(ctx, &apigatewayv2.GetApiMappingsInput{DomainName: aws.String(edgeName)})
	var notFound *apigwv2types.NotFoundException
	require.ErrorAs(t, err, &notFound, "a v1 delete removes the domain name from the v2 API")
}

// TestAPIGatewayV2_UpdatesAndTags covers UpdateDeployment, UpdateModel,
// UpdateVpcLink, and TagResource, UntagResource and GetTags on an API and its
// stage.
func TestAPIGatewayV2_UpdatesAndTags(t *testing.T) {
	t.Parallel()
	c := apigwv2Client()
	api, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{Name: aws.String("sdk-v2-updates"), ProtocolType: apigwv2types.ProtocolTypeHttp})
	require.NoError(t, err)
	apiId := api.ApiId
	t.Cleanup(func() { _, _ = c.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: apiId}) })

	dep, err := c.CreateDeployment(ctx, &apigatewayv2.CreateDeploymentInput{ApiId: apiId, Description: aws.String("first")})
	require.NoError(t, err)
	upDep, err := c.UpdateDeployment(ctx, &apigatewayv2.UpdateDeploymentInput{ApiId: apiId, DeploymentId: dep.DeploymentId, Description: aws.String("second")})
	require.NoError(t, err)
	assert.Equal(t, "second", aws.ToString(upDep.Description))
	_, err = c.UpdateDeployment(ctx, &apigatewayv2.UpdateDeploymentInput{ApiId: apiId, DeploymentId: aws.String("absent0000"), Description: aws.String("x")})
	var notFound *apigwv2types.NotFoundException
	require.ErrorAs(t, err, &notFound)

	model, err := c.CreateModel(ctx, &apigatewayv2.CreateModelInput{
		ApiId: apiId, Name: aws.String("Order"), ContentType: aws.String("application/json"), Schema: aws.String(`{"type":"object"}`),
	})
	require.NoError(t, err)
	upModel, err := c.UpdateModel(ctx, &apigatewayv2.UpdateModelInput{ApiId: apiId, ModelId: model.ModelId, Description: aws.String("an order")})
	require.NoError(t, err)
	assert.Equal(t, "an order", aws.ToString(upModel.Description))
	assert.Equal(t, "Order", aws.ToString(upModel.Name), "UpdateModel keeps the members it does not name")

	vl, err := c.CreateVpcLink(ctx, &apigatewayv2.CreateVpcLinkInput{Name: aws.String("sdk-v2-link"), SubnetIds: []string{"subnet-aaaa1111"}})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteVpcLink(ctx, &apigatewayv2.DeleteVpcLinkInput{VpcLinkId: vl.VpcLinkId}) })
	upVl, err := c.UpdateVpcLink(ctx, &apigatewayv2.UpdateVpcLinkInput{VpcLinkId: vl.VpcLinkId, Name: aws.String("sdk-v2-link-renamed")})
	require.NoError(t, err)
	assert.Equal(t, "sdk-v2-link-renamed", aws.ToString(upVl.Name))

	_, err = c.CreateStage(ctx, &apigatewayv2.CreateStageInput{ApiId: apiId, StageName: aws.String("prod")})
	require.NoError(t, err)
	apiArn := "arn:aws:apigateway:us-east-1::/apis/" + aws.ToString(apiId)
	stageArn := apiArn + "/stages/prod"
	for _, arn := range []string{apiArn, stageArn} {
		_, err = c.TagResource(ctx, &apigatewayv2.TagResourceInput{ResourceArn: aws.String(arn), Tags: map[string]string{"env": "prod", "team": "api"}})
		require.NoError(t, err, arn)
		_, err = c.UntagResource(ctx, &apigatewayv2.UntagResourceInput{ResourceArn: aws.String(arn), TagKeys: []string{"team"}})
		require.NoError(t, err, arn)
		tags, err := c.GetTags(ctx, &apigatewayv2.GetTagsInput{ResourceArn: aws.String(arn)})
		require.NoError(t, err, arn)
		assert.Equal(t, map[string]string{"env": "prod"}, tags.Tags, arn)
	}
	_, err = c.TagResource(ctx, &apigatewayv2.TagResourceInput{ResourceArn: aws.String(apiArn + "/stages/absent"), Tags: map[string]string{"a": "b"}})
	require.ErrorAs(t, err, &notFound, "TagResource of an unknown stage")
}
