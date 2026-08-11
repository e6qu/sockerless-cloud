package aws_sdk_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigwtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	apigwv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func apigwClient() *apigateway.Client {
	return apigateway.NewFromConfig(sdkConfig(), func(o *apigateway.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func apigwv2Client() *apigatewayv2.Client {
	return apigatewayv2.NewFromConfig(sdkConfig(), func(o *apigatewayv2.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestAPIGatewayV2_ApiLifecycle exercises the HTTP-API minimal flow:
// CreateApi → CreateIntegration → CreateRoute → CreateStage →
// GetApi → DeleteApi.
func TestAPIGatewayV2_ApiLifecycle(t *testing.T) {
	c := apigwv2Client()
	create, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name:         aws.String("hello-api"),
		ProtocolType: "HTTP",
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(create.ApiId))
	apiId := aws.ToString(create.ApiId)
	t.Cleanup(func() {
		_, _ = c.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: aws.String(apiId)})
	})

	get, err := c.GetApi(ctx, &apigatewayv2.GetApiInput{ApiId: aws.String(apiId)})
	require.NoError(t, err)
	assert.Equal(t, "hello-api", aws.ToString(get.Name))
	assert.Equal(t, "$request.header.x-api-key", aws.ToString(get.ApiKeySelectionExpression))

	intg, err := c.CreateIntegration(ctx, &apigatewayv2.CreateIntegrationInput{
		ApiId:           aws.String(apiId),
		IntegrationType: "HTTP_PROXY",
		IntegrationUri:  aws.String("https://example.com"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(intg.IntegrationId))
	assert.Equal(t, apigwv2types.ConnectionTypeInternet, intg.ConnectionType)

	rt, err := c.CreateRoute(ctx, &apigatewayv2.CreateRouteInput{
		ApiId:    aws.String(apiId),
		RouteKey: aws.String("GET /hello"),
		Target:   aws.String("integrations/" + aws.ToString(intg.IntegrationId)),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(rt.RouteId))

	stage, err := c.CreateStage(ctx, &apigatewayv2.CreateStageInput{
		ApiId:      aws.String(apiId),
		StageName:  aws.String("$default"),
		AutoDeploy: aws.Bool(true),
		Tags:       map[string]string{"consumer": "ecs-dev-desktop"},
	})
	require.NoError(t, err)
	assert.Equal(t, "$default", aws.ToString(stage.StageName))
	assert.Equal(t, "ecs-dev-desktop", stage.Tags["consumer"])

	updatedIntegration, err := c.UpdateIntegration(ctx, &apigatewayv2.UpdateIntegrationInput{
		ApiId:             aws.String(apiId),
		IntegrationId:     intg.IntegrationId,
		IntegrationMethod: aws.String("POST"),
	})
	require.NoError(t, err)
	assert.Equal(t, "POST", aws.ToString(updatedIntegration.IntegrationMethod))

	updatedStage, err := c.UpdateStage(ctx, &apigatewayv2.UpdateStageInput{
		ApiId:       aws.String(apiId),
		StageName:   aws.String("$default"),
		Description: aws.String("consumer deployment"),
	})
	require.NoError(t, err)
	assert.Equal(t, "consumer deployment", aws.ToString(updatedStage.Description))

	list, err := c.GetApis(ctx, &apigatewayv2.GetApisInput{})
	require.NoError(t, err)
	found := false
	for _, item := range list.Items {
		if aws.ToString(item.ApiId) == apiId {
			found = true
			break
		}
	}
	assert.True(t, found)
}

// TestAPIGateway_RestApiLifecycle exercises the REST-API minimal flow:
// CreateRestApi → GetResources → PutMethod → PutIntegration →
// CreateDeployment → CreateStage → DeleteRestApi.
func TestAPIGateway_RestApiLifecycle(t *testing.T) {
	c := apigwClient()
	create, err := c.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name:        aws.String("hello-rest"),
		Description: aws.String("integration test"),
	})
	require.NoError(t, err)
	apiId := aws.ToString(create.Id)
	require.NotEmpty(t, apiId)
	t.Cleanup(func() {
		_, _ = c.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: aws.String(apiId)})
	})

	// CreateRestApi auto-creates the root "/" resource.
	res, err := c.GetResources(ctx, &apigateway.GetResourcesInput{RestApiId: aws.String(apiId)})
	require.NoError(t, err)
	require.Len(t, res.Items, 1, "expected one root resource on a fresh REST API")
	rootId := aws.ToString(res.Items[0].Id)

	// CreateResource as a child of the root.
	child, err := c.CreateResource(ctx, &apigateway.CreateResourceInput{
		RestApiId: aws.String(apiId),
		ParentId:  aws.String(rootId),
		PathPart:  aws.String("hello"),
	})
	require.NoError(t, err)
	childId := aws.ToString(child.Id)
	require.NotEmpty(t, childId)

	// PutMethod on the child.
	_, err = c.PutMethod(ctx, &apigateway.PutMethodInput{
		RestApiId:         aws.String(apiId),
		ResourceId:        aws.String(childId),
		HttpMethod:        aws.String("GET"),
		AuthorizationType: aws.String("NONE"),
	})
	require.NoError(t, err)

	// PutIntegration.
	_, err = c.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId:  aws.String(apiId),
		ResourceId: aws.String(childId),
		HttpMethod: aws.String("GET"),
		Type:       "HTTP_PROXY",
		Uri:        aws.String("https://example.com"),
	})
	require.NoError(t, err)

	// CreateDeployment + CreateStage.
	dep, err := c.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{
		RestApiId: aws.String(apiId),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(dep.Id))

	stage, err := c.CreateStage(ctx, &apigateway.CreateStageInput{
		RestApiId:    aws.String(apiId),
		StageName:    aws.String("prod"),
		DeploymentId: dep.Id,
	})
	require.NoError(t, err)
	assert.Equal(t, "prod", aws.ToString(stage.StageName))

	// GetDeployments + GetStages list the resources just created.
	deps, err := c.GetDeployments(ctx, &apigateway.GetDeploymentsInput{RestApiId: aws.String(apiId)})
	require.NoError(t, err)
	foundDep := false
	for _, d := range deps.Items {
		if aws.ToString(d.Id) == aws.ToString(dep.Id) {
			foundDep = true
			break
		}
	}
	assert.True(t, foundDep, "created deployment should appear in GetDeployments")

	stages, err := c.GetStages(ctx, &apigateway.GetStagesInput{RestApiId: aws.String(apiId)})
	require.NoError(t, err)
	foundStage := false
	for _, s := range stages.Item {
		if aws.ToString(s.StageName) == "prod" {
			foundStage = true
			break
		}
	}
	assert.True(t, foundStage, "created stage should appear in GetStages")
}

// TestAPIGatewayV2_UpdateRoute exercises the PATCH route update path:
// CreateRoute → UpdateRoute (change RouteKey) → GetRoute reflects it.
func TestAPIGatewayV2_UpdateRoute(t *testing.T) {
	c := apigwv2Client()
	create, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name:         aws.String("update-route-api"),
		ProtocolType: "HTTP",
	})
	require.NoError(t, err)
	apiId := aws.ToString(create.ApiId)
	require.NotEmpty(t, apiId)
	t.Cleanup(func() {
		_, _ = c.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: aws.String(apiId)})
	})

	rt, err := c.CreateRoute(ctx, &apigatewayv2.CreateRouteInput{
		ApiId:    aws.String(apiId),
		RouteKey: aws.String("GET /before"),
	})
	require.NoError(t, err)
	routeId := aws.ToString(rt.RouteId)
	require.NotEmpty(t, routeId)

	upd, err := c.UpdateRoute(ctx, &apigatewayv2.UpdateRouteInput{
		ApiId:    aws.String(apiId),
		RouteId:  aws.String(routeId),
		RouteKey: aws.String("POST /after"),
	})
	require.NoError(t, err)
	assert.Equal(t, "POST /after", aws.ToString(upd.RouteKey))

	got, err := c.GetRoute(ctx, &apigatewayv2.GetRouteInput{
		ApiId:   aws.String(apiId),
		RouteId: aws.String(routeId),
	})
	require.NoError(t, err)
	assert.Equal(t, "POST /after", aws.ToString(got.RouteKey))
}

// TestAPIGateway_ApiKeysAndUsagePlans exercises the v1 API-key + usage-plan
// surface end-to-end: CreateApiKey → GetApiKey → GetApiKeys → UpdateApiKey,
// CreateUsagePlan → GetUsagePlan → GetUsagePlans → UpdateUsagePlan,
// CreateUsagePlanKey → GetUsagePlanKey → GetUsagePlanKeys, then the matching
// deletes.
func TestAPIGateway_ApiKeysAndUsagePlans(t *testing.T) {
	c := apigwClient()

	key, err := c.CreateApiKey(ctx, &apigateway.CreateApiKeyInput{
		Name:        aws.String("sdk-key"),
		Description: aws.String("sdk coverage"),
		Enabled:     true,
	})
	require.NoError(t, err)
	keyId := aws.ToString(key.Id)
	require.NotEmpty(t, keyId)
	require.NotEmpty(t, aws.ToString(key.Value))
	t.Cleanup(func() {
		_, _ = c.DeleteApiKey(ctx, &apigateway.DeleteApiKeyInput{ApiKey: aws.String(keyId)})
	})

	got, err := c.GetApiKey(ctx, &apigateway.GetApiKeyInput{ApiKey: aws.String(keyId)})
	require.NoError(t, err)
	assert.Equal(t, "sdk-key", aws.ToString(got.Name))

	listKeys, err := c.GetApiKeys(ctx, &apigateway.GetApiKeysInput{})
	require.NoError(t, err)
	var foundKey bool
	for _, k := range listKeys.Items {
		if aws.ToString(k.Id) == keyId {
			foundKey = true
		}
	}
	assert.True(t, foundKey)

	updKey, err := c.UpdateApiKey(ctx, &apigateway.UpdateApiKeyInput{
		ApiKey: aws.String(keyId),
		PatchOperations: []apigwtypes.PatchOperation{
			{Op: apigwtypes.OpReplace, Path: aws.String("/description"), Value: aws.String("updated")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", aws.ToString(updKey.Description))

	plan, err := c.CreateUsagePlan(ctx, &apigateway.CreateUsagePlanInput{
		Name:        aws.String("sdk-plan"),
		Description: aws.String("sdk plan"),
		Throttle:    &apigwtypes.ThrottleSettings{BurstLimit: 100, RateLimit: 50},
		Quota:       &apigwtypes.QuotaSettings{Limit: 1000, Period: apigwtypes.QuotaPeriodTypeMonth},
	})
	require.NoError(t, err)
	planId := aws.ToString(plan.Id)
	require.NotEmpty(t, planId)
	t.Cleanup(func() {
		_, _ = c.DeleteUsagePlan(ctx, &apigateway.DeleteUsagePlanInput{UsagePlanId: aws.String(planId)})
	})

	gotPlan, err := c.GetUsagePlan(ctx, &apigateway.GetUsagePlanInput{UsagePlanId: aws.String(planId)})
	require.NoError(t, err)
	assert.Equal(t, "sdk-plan", aws.ToString(gotPlan.Name))

	listPlans, err := c.GetUsagePlans(ctx, &apigateway.GetUsagePlansInput{})
	require.NoError(t, err)
	var foundPlan bool
	for _, p := range listPlans.Items {
		if aws.ToString(p.Id) == planId {
			foundPlan = true
		}
	}
	assert.True(t, foundPlan)

	updPlan, err := c.UpdateUsagePlan(ctx, &apigateway.UpdateUsagePlanInput{
		UsagePlanId: aws.String(planId),
		PatchOperations: []apigwtypes.PatchOperation{
			{Op: apigwtypes.OpReplace, Path: aws.String("/description"), Value: aws.String("plan-updated")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "plan-updated", aws.ToString(updPlan.Description))

	upk, err := c.CreateUsagePlanKey(ctx, &apigateway.CreateUsagePlanKeyInput{
		UsagePlanId: aws.String(planId),
		KeyId:       aws.String(keyId),
		KeyType:     aws.String("API_KEY"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(upk.Id))
	t.Cleanup(func() {
		_, _ = c.DeleteUsagePlanKey(ctx, &apigateway.DeleteUsagePlanKeyInput{
			UsagePlanId: aws.String(planId), KeyId: aws.String(keyId),
		})
	})

	gotUpk, err := c.GetUsagePlanKey(ctx, &apigateway.GetUsagePlanKeyInput{
		UsagePlanId: aws.String(planId), KeyId: aws.String(keyId),
	})
	require.NoError(t, err)
	assert.Equal(t, keyId, aws.ToString(gotUpk.Id))

	listUpk, err := c.GetUsagePlanKeys(ctx, &apigateway.GetUsagePlanKeysInput{UsagePlanId: aws.String(planId)})
	require.NoError(t, err)
	require.Len(t, listUpk.Items, 1)
}

// TestAPIGateway_ModelsValidatorsAuthorizers exercises the v1 REST-API-scoped
// model / request-validator / authorizer CRUD: CreateModel → GetModel →
// GetModels, CreateRequestValidator → GetRequestValidators, CreateAuthorizer →
// GetAuthorizer → GetAuthorizers, then the matching deletes.
func TestAPIGateway_ModelsValidatorsAuthorizers(t *testing.T) {
	c := apigwClient()
	api, err := c.CreateRestApi(ctx, &apigateway.CreateRestApiInput{Name: aws.String("sdk-mva-api")})
	require.NoError(t, err)
	apiId := aws.ToString(api.Id)
	require.NotEmpty(t, apiId)
	t.Cleanup(func() {
		_, _ = c.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: aws.String(apiId)})
	})

	model, err := c.CreateModel(ctx, &apigateway.CreateModelInput{
		RestApiId:   aws.String(apiId),
		Name:        aws.String("SdkModel"),
		ContentType: aws.String("application/json"),
		Schema:      aws.String(`{"type":"object"}`),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(model.Id))
	t.Cleanup(func() {
		_, _ = c.DeleteModel(ctx, &apigateway.DeleteModelInput{RestApiId: aws.String(apiId), ModelName: aws.String("SdkModel")})
	})

	gotModel, err := c.GetModel(ctx, &apigateway.GetModelInput{RestApiId: aws.String(apiId), ModelName: aws.String("SdkModel")})
	require.NoError(t, err)
	assert.Equal(t, "SdkModel", aws.ToString(gotModel.Name))

	models, err := c.GetModels(ctx, &apigateway.GetModelsInput{RestApiId: aws.String(apiId)})
	require.NoError(t, err)
	require.Len(t, models.Items, 1)

	rv, err := c.CreateRequestValidator(ctx, &apigateway.CreateRequestValidatorInput{
		RestApiId:           aws.String(apiId),
		Name:                aws.String("sdk-validator"),
		ValidateRequestBody: true,
	})
	require.NoError(t, err)
	rvId := aws.ToString(rv.Id)
	require.NotEmpty(t, rvId)
	t.Cleanup(func() {
		_, _ = c.DeleteRequestValidator(ctx, &apigateway.DeleteRequestValidatorInput{
			RestApiId: aws.String(apiId), RequestValidatorId: aws.String(rvId),
		})
	})

	rvs, err := c.GetRequestValidators(ctx, &apigateway.GetRequestValidatorsInput{RestApiId: aws.String(apiId)})
	require.NoError(t, err)
	require.Len(t, rvs.Items, 1)
	assert.True(t, rvs.Items[0].ValidateRequestBody)

	auth, err := c.CreateAuthorizer(ctx, &apigateway.CreateAuthorizerInput{
		RestApiId:      aws.String(apiId),
		Name:           aws.String("sdk-authorizer"),
		Type:           apigwtypes.AuthorizerTypeToken,
		AuthorizerUri:  aws.String("arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:auth/invocations"),
		IdentitySource: aws.String("method.request.header.Authorization"),
	})
	require.NoError(t, err)
	authId := aws.ToString(auth.Id)
	require.NotEmpty(t, authId)
	t.Cleanup(func() {
		_, _ = c.DeleteAuthorizer(ctx, &apigateway.DeleteAuthorizerInput{
			RestApiId: aws.String(apiId), AuthorizerId: aws.String(authId),
		})
	})

	gotAuth, err := c.GetAuthorizer(ctx, &apigateway.GetAuthorizerInput{
		RestApiId: aws.String(apiId), AuthorizerId: aws.String(authId),
	})
	require.NoError(t, err)
	assert.Equal(t, "sdk-authorizer", aws.ToString(gotAuth.Name))

	auths, err := c.GetAuthorizers(ctx, &apigateway.GetAuthorizersInput{RestApiId: aws.String(apiId)})
	require.NoError(t, err)
	require.Len(t, auths.Items, 1)
}

// TestAPIGatewayV2_AuthorizersAndModels exercises the v2 API-scoped authorizer
// + model CRUD: CreateAuthorizer → GetAuthorizer → GetAuthorizers →
// UpdateAuthorizer, CreateModel → GetModel → GetModels, then deletes.
func TestAPIGatewayV2_AuthorizersAndModels(t *testing.T) {
	c := apigwv2Client()
	api, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{Name: aws.String("v2-am-api"), ProtocolType: "HTTP"})
	require.NoError(t, err)
	apiId := aws.ToString(api.ApiId)
	require.NotEmpty(t, apiId)
	t.Cleanup(func() { _, _ = c.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: aws.String(apiId)}) })

	auth, err := c.CreateAuthorizer(ctx, &apigatewayv2.CreateAuthorizerInput{
		ApiId:          aws.String(apiId),
		Name:           aws.String("v2-jwt-auth"),
		AuthorizerType: apigwv2types.AuthorizerTypeJwt,
		IdentitySource: []string{"$request.header.Authorization"},
		JwtConfiguration: &apigwv2types.JWTConfiguration{
			Audience: []string{"aud1"},
			Issuer:   aws.String("https://issuer.example.com"),
		},
	})
	require.NoError(t, err)
	authId := aws.ToString(auth.AuthorizerId)
	require.NotEmpty(t, authId)
	t.Cleanup(func() {
		_, _ = c.DeleteAuthorizer(ctx, &apigatewayv2.DeleteAuthorizerInput{ApiId: aws.String(apiId), AuthorizerId: aws.String(authId)})
	})

	gotAuth, err := c.GetAuthorizer(ctx, &apigatewayv2.GetAuthorizerInput{ApiId: aws.String(apiId), AuthorizerId: aws.String(authId)})
	require.NoError(t, err)
	assert.Equal(t, "v2-jwt-auth", aws.ToString(gotAuth.Name))

	auths, err := c.GetAuthorizers(ctx, &apigatewayv2.GetAuthorizersInput{ApiId: aws.String(apiId)})
	require.NoError(t, err)
	require.Len(t, auths.Items, 1)

	updAuth, err := c.UpdateAuthorizer(ctx, &apigatewayv2.UpdateAuthorizerInput{
		ApiId: aws.String(apiId), AuthorizerId: aws.String(authId), Name: aws.String("v2-jwt-renamed"),
	})
	require.NoError(t, err)
	assert.Equal(t, "v2-jwt-renamed", aws.ToString(updAuth.Name))

	model, err := c.CreateModel(ctx, &apigatewayv2.CreateModelInput{
		ApiId:       aws.String(apiId),
		Name:        aws.String("V2Model"),
		ContentType: aws.String("application/json"),
		Schema:      aws.String(`{"type":"object"}`),
	})
	require.NoError(t, err)
	modelId := aws.ToString(model.ModelId)
	require.NotEmpty(t, modelId)
	t.Cleanup(func() {
		_, _ = c.DeleteModel(ctx, &apigatewayv2.DeleteModelInput{ApiId: aws.String(apiId), ModelId: aws.String(modelId)})
	})

	gotModel, err := c.GetModel(ctx, &apigatewayv2.GetModelInput{ApiId: aws.String(apiId), ModelId: aws.String(modelId)})
	require.NoError(t, err)
	assert.Equal(t, "V2Model", aws.ToString(gotModel.Name))

	models, err := c.GetModels(ctx, &apigatewayv2.GetModelsInput{ApiId: aws.String(apiId)})
	require.NoError(t, err)
	require.Len(t, models.Items, 1)
}

// TestAPIGatewayV2_DomainNamesMappingsVpcLinks exercises the v2 domain-name +
// API-mapping + VPC-link surface: CreateDomainName → GetDomainName →
// GetDomainNames, CreateApiMapping → GetApiMapping → GetApiMappings,
// CreateVpcLink → GetVpcLink → GetVpcLinks, then deletes.
func TestAPIGatewayV2_DomainNamesMappingsVpcLinks(t *testing.T) {
	c := apigwv2Client()
	domain := fmt.Sprintf("sdk-%d.example.com", time.Now().UnixNano())

	dn, err := c.CreateDomainName(ctx, &apigatewayv2.CreateDomainNameInput{
		DomainName: aws.String(domain),
		DomainNameConfigurations: []apigwv2types.DomainNameConfiguration{
			{EndpointType: apigwv2types.EndpointTypeRegional, CertificateArn: aws.String("arn:aws:acm:us-east-1:000000000000:certificate/abc")},
		},
	})
	require.NoError(t, err)
	require.Equal(t, domain, aws.ToString(dn.DomainName))
	t.Cleanup(func() {
		_, _ = c.DeleteDomainName(ctx, &apigatewayv2.DeleteDomainNameInput{DomainName: aws.String(domain)})
	})

	gotDn, err := c.GetDomainName(ctx, &apigatewayv2.GetDomainNameInput{DomainName: aws.String(domain)})
	require.NoError(t, err)
	assert.Equal(t, domain, aws.ToString(gotDn.DomainName))

	dns, err := c.GetDomainNames(ctx, &apigatewayv2.GetDomainNamesInput{})
	require.NoError(t, err)
	var foundDn bool
	for _, d := range dns.Items {
		if aws.ToString(d.DomainName) == domain {
			foundDn = true
		}
	}
	assert.True(t, foundDn)

	api, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{Name: aws.String("v2-mapping-api"), ProtocolType: "HTTP"})
	require.NoError(t, err)
	apiId := aws.ToString(api.ApiId)
	t.Cleanup(func() { _, _ = c.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: aws.String(apiId)}) })

	stage, err := c.CreateStage(ctx, &apigatewayv2.CreateStageInput{ApiId: aws.String(apiId), StageName: aws.String("prod")})
	require.NoError(t, err)
	require.NotNil(t, stage)

	mapping, err := c.CreateApiMapping(ctx, &apigatewayv2.CreateApiMappingInput{
		DomainName: aws.String(domain), ApiId: aws.String(apiId), Stage: aws.String("prod"), ApiMappingKey: aws.String("v1"),
	})
	require.NoError(t, err)
	mappingId := aws.ToString(mapping.ApiMappingId)
	require.NotEmpty(t, mappingId)
	t.Cleanup(func() {
		_, _ = c.DeleteApiMapping(ctx, &apigatewayv2.DeleteApiMappingInput{DomainName: aws.String(domain), ApiMappingId: aws.String(mappingId)})
	})

	gotMapping, err := c.GetApiMapping(ctx, &apigatewayv2.GetApiMappingInput{DomainName: aws.String(domain), ApiMappingId: aws.String(mappingId)})
	require.NoError(t, err)
	assert.Equal(t, "v1", aws.ToString(gotMapping.ApiMappingKey))

	mappings, err := c.GetApiMappings(ctx, &apigatewayv2.GetApiMappingsInput{DomainName: aws.String(domain)})
	require.NoError(t, err)
	require.Len(t, mappings.Items, 1)

	vl, err := c.CreateVpcLink(ctx, &apigatewayv2.CreateVpcLinkInput{
		Name:      aws.String("sdk-vpclink"),
		SubnetIds: []string{"subnet-aaaa1111"},
	})
	require.NoError(t, err)
	vlId := aws.ToString(vl.VpcLinkId)
	require.NotEmpty(t, vlId)
	t.Cleanup(func() { _, _ = c.DeleteVpcLink(ctx, &apigatewayv2.DeleteVpcLinkInput{VpcLinkId: aws.String(vlId)}) })

	gotVl, err := c.GetVpcLink(ctx, &apigatewayv2.GetVpcLinkInput{VpcLinkId: aws.String(vlId)})
	require.NoError(t, err)
	assert.Equal(t, "sdk-vpclink", aws.ToString(gotVl.Name))

	vls, err := c.GetVpcLinks(ctx, &apigatewayv2.GetVpcLinksInput{})
	require.NoError(t, err)
	var foundVl bool
	for _, v := range vls.Items {
		if aws.ToString(v.VpcLinkId) == vlId {
			foundVl = true
		}
	}
	assert.True(t, foundVl)
}
