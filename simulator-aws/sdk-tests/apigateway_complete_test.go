package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigwtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// patch builds a single replace PatchOperation.
func apigwPatch(path, value string) apigwtypes.PatchOperation {
	return apigwtypes.PatchOperation{Op: apigwtypes.OpReplace, Path: aws.String(path), Value: aws.String(value)}
}

// TestAPIGateway_UpdateLifecycle drives the full REST-API resource tree through
// its Update* operations: UpdateRestApi, UpdateResource, UpdateMethod,
// UpdateMethodResponse, UpdateIntegration, UpdateModel, UpdateRequestValidator,
// UpdateDeployment, UpdateStage — each a PATCH against the stored resource.
func TestAPIGateway_UpdateLifecycle(t *testing.T) {
	c := apigwClient()

	api, err := c.CreateRestApi(ctx, &apigateway.CreateRestApiInput{Name: aws.String("update-lifecycle")})
	require.NoError(t, err)
	apiId := aws.ToString(api.Id)
	t.Cleanup(func() { _, _ = c.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: aws.String(apiId)}) })
	rootId := aws.ToString(api.RootResourceId)

	upApi, err := c.UpdateRestApi(ctx, &apigateway.UpdateRestApiInput{
		RestApiId:       aws.String(apiId),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/description", "renamed")},
	})
	require.NoError(t, err)
	assert.Equal(t, "renamed", aws.ToString(upApi.Description))

	res, err := c.CreateResource(ctx, &apigateway.CreateResourceInput{
		RestApiId: aws.String(apiId), ParentId: aws.String(rootId), PathPart: aws.String("widgets"),
	})
	require.NoError(t, err)
	resId := aws.ToString(res.Id)

	upRes, err := c.UpdateResource(ctx, &apigateway.UpdateResourceInput{
		RestApiId: aws.String(apiId), ResourceId: aws.String(resId),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/pathPart", "gadgets")},
	})
	require.NoError(t, err)
	assert.Equal(t, "gadgets", aws.ToString(upRes.PathPart))

	_, err = c.PutMethod(ctx, &apigateway.PutMethodInput{
		RestApiId: aws.String(apiId), ResourceId: aws.String(resId),
		HttpMethod: aws.String("GET"), AuthorizationType: aws.String("NONE"),
	})
	require.NoError(t, err)

	upMethod, err := c.UpdateMethod(ctx, &apigateway.UpdateMethodInput{
		RestApiId: aws.String(apiId), ResourceId: aws.String(resId), HttpMethod: aws.String("GET"),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/apiKeyRequired", "true")},
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(upMethod.ApiKeyRequired))

	_, err = c.PutMethodResponse(ctx, &apigateway.PutMethodResponseInput{
		RestApiId: aws.String(apiId), ResourceId: aws.String(resId), HttpMethod: aws.String("GET"),
		StatusCode: aws.String("200"),
	})
	require.NoError(t, err)
	upMR, err := c.UpdateMethodResponse(ctx, &apigateway.UpdateMethodResponseInput{
		RestApiId: aws.String(apiId), ResourceId: aws.String(resId), HttpMethod: aws.String("GET"),
		StatusCode:      aws.String("200"),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/responseModels/application~1json", "Empty")},
	})
	require.NoError(t, err)
	assert.Equal(t, "Empty", upMR.ResponseModels["application/json"])

	_, err = c.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId: aws.String(apiId), ResourceId: aws.String(resId), HttpMethod: aws.String("GET"),
		Type: apigwtypes.IntegrationTypeMock,
	})
	require.NoError(t, err)
	upInt, err := c.UpdateIntegration(ctx, &apigateway.UpdateIntegrationInput{
		RestApiId: aws.String(apiId), ResourceId: aws.String(resId), HttpMethod: aws.String("GET"),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/timeoutInMillis", "12000")},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(12000), upInt.TimeoutInMillis)

	model, err := c.CreateModel(ctx, &apigateway.CreateModelInput{
		RestApiId: aws.String(apiId), Name: aws.String("Widget"),
		ContentType: aws.String("application/json"), Schema: aws.String(`{"type":"object"}`),
	})
	require.NoError(t, err)
	upModel, err := c.UpdateModel(ctx, &apigateway.UpdateModelInput{
		RestApiId: aws.String(apiId), ModelName: model.Name,
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/description", "the widget model")},
	})
	require.NoError(t, err)
	assert.Equal(t, "the widget model", aws.ToString(upModel.Description))

	rv, err := c.CreateRequestValidator(ctx, &apigateway.CreateRequestValidatorInput{
		RestApiId: aws.String(apiId), Name: aws.String("v1"),
	})
	require.NoError(t, err)
	upRV, err := c.UpdateRequestValidator(ctx, &apigateway.UpdateRequestValidatorInput{
		RestApiId: aws.String(apiId), RequestValidatorId: rv.Id,
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/validateRequestBody", "true")},
	})
	require.NoError(t, err)
	assert.True(t, upRV.ValidateRequestBody)

	dep, err := c.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{RestApiId: aws.String(apiId)})
	require.NoError(t, err)
	depId := aws.ToString(dep.Id)
	upDep, err := c.UpdateDeployment(ctx, &apigateway.UpdateDeploymentInput{
		RestApiId: aws.String(apiId), DeploymentId: aws.String(depId),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/description", "prod deploy")},
	})
	require.NoError(t, err)
	assert.Equal(t, "prod deploy", aws.ToString(upDep.Description))

	stage, err := c.CreateStage(ctx, &apigateway.CreateStageInput{
		RestApiId: aws.String(apiId), StageName: aws.String("prod"), DeploymentId: aws.String(depId),
	})
	require.NoError(t, err)
	_ = stage
	dep2, err := c.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{RestApiId: aws.String(apiId)})
	require.NoError(t, err)
	upStage, err := c.UpdateStage(ctx, &apigateway.UpdateStageInput{
		RestApiId: aws.String(apiId), StageName: aws.String("prod"),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/deploymentId", aws.ToString(dep2.Id))},
	})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(dep2.Id), aws.ToString(upStage.DeploymentId))

	// FlushStageCache + FlushStageAuthorizersCache on the deployed stage.
	_, err = c.FlushStageCache(ctx, &apigateway.FlushStageCacheInput{RestApiId: aws.String(apiId), StageName: aws.String("prod")})
	require.NoError(t, err)
	_, err = c.FlushStageAuthorizersCache(ctx, &apigateway.FlushStageAuthorizersCacheInput{RestApiId: aws.String(apiId), StageName: aws.String("prod")})
	require.NoError(t, err)

	// TestInvokeMethod against the stored GET method.
	ti, err := c.TestInvokeMethod(ctx, &apigateway.TestInvokeMethodInput{
		RestApiId: aws.String(apiId), ResourceId: aws.String(resId), HttpMethod: aws.String("GET"),
		PathWithQueryString: aws.String("/gadgets"),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(200), ti.Status)
	assert.NotEmpty(t, aws.ToString(ti.Log))

	// TestInvokeAuthorizer against a stored authorizer.
	authz, err := c.CreateAuthorizer(ctx, &apigateway.CreateAuthorizerInput{
		RestApiId: aws.String(apiId), Name: aws.String("authz"),
		Type: apigwtypes.AuthorizerTypeToken, IdentitySource: aws.String("method.request.header.Authorization"),
		AuthorizerUri: aws.String("arn:aws:apigateway:us-east-1:lambda:path/x"),
	})
	require.NoError(t, err)
	ta, err := c.TestInvokeAuthorizer(ctx, &apigateway.TestInvokeAuthorizerInput{
		RestApiId: aws.String(apiId), AuthorizerId: authz.Id,
		Headers: map[string]string{"Authorization": "tok"},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(200), ta.ClientStatus)
	assert.Equal(t, "test-principal", aws.ToString(ta.PrincipalId))
}

// TestAPIGateway_ImportAndTag covers ImportRestApi, PutRestApi, ImportApiKeys,
// TagResource and UntagResource.
func TestAPIGateway_ImportAndTag(t *testing.T) {
	c := apigwClient()

	openapi := []byte(`{"openapi":"3.0.1","info":{"title":"imported-api","version":"1.0"},"paths":{"/ping":{"get":{"responses":{"200":{"description":"ok"}}}}}}`)
	imp, err := c.ImportRestApi(ctx, &apigateway.ImportRestApiInput{Body: openapi})
	require.NoError(t, err)
	impId := aws.ToString(imp.Id)
	require.NotEmpty(t, impId)
	t.Cleanup(func() { _, _ = c.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: aws.String(impId)}) })
	assert.Equal(t, "imported-api", aws.ToString(imp.Name))

	// Imported resource tree carries the /ping resource.
	resources, err := c.GetResources(ctx, &apigateway.GetResourcesInput{RestApiId: aws.String(impId)})
	require.NoError(t, err)
	var hasPing bool
	for _, r := range resources.Items {
		if aws.ToString(r.Path) == "/ping" {
			hasPing = true
		}
	}
	assert.True(t, hasPing, "imported API should carry the /ping resource")

	// PutRestApi overwrites the resource tree from a fresh OpenAPI body.
	openapi2 := []byte(`{"openapi":"3.0.1","info":{"title":"imported-api","version":"2.0"},"paths":{"/pong":{"post":{"responses":{"200":{"description":"ok"}}}}}}`)
	_, err = c.PutRestApi(ctx, &apigateway.PutRestApiInput{
		RestApiId: aws.String(impId), Body: openapi2, Mode: apigwtypes.PutModeOverwrite,
	})
	require.NoError(t, err)
	resources2, err := c.GetResources(ctx, &apigateway.GetResourcesInput{RestApiId: aws.String(impId)})
	require.NoError(t, err)
	var hasPong, stillPing bool
	for _, r := range resources2.Items {
		switch aws.ToString(r.Path) {
		case "/pong":
			hasPong = true
		case "/ping":
			stillPing = true
		}
	}
	assert.True(t, hasPong, "overwrite should add /pong")
	assert.False(t, stillPing, "overwrite should drop /ping")

	// ImportApiKeys bulk-creates keys from a CSV payload.
	csv := []byte("name,key\nkey-one,abcdefghij1234567890\nkey-two,zyxwvutsrq0987654321\n")
	impKeys, err := c.ImportApiKeys(ctx, &apigateway.ImportApiKeysInput{
		Body: csv, Format: apigwtypes.ApiKeysFormatCsv,
	})
	require.NoError(t, err)
	require.Len(t, impKeys.Ids, 2)
	for _, id := range impKeys.Ids {
		t.Cleanup(func() { _, _ = c.DeleteApiKey(ctx, &apigateway.DeleteApiKeyInput{ApiKey: aws.String(id)}) })
	}

	// TagResource + UntagResource on the imported REST API's ARN.
	arn := "arn:aws:apigateway:us-east-1::/restapis/" + impId
	_, err = c.TagResource(ctx, &apigateway.TagResourceInput{
		ResourceArn: aws.String(arn), Tags: map[string]string{"team": "platform", "env": "test"},
	})
	require.NoError(t, err)
	tags, err := c.GetTags(ctx, &apigateway.GetTagsInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, "platform", tags.Tags["team"])

	_, err = c.UntagResource(ctx, &apigateway.UntagResourceInput{
		ResourceArn: aws.String(arn), TagKeys: []string{"env"},
	})
	require.NoError(t, err)
	tags2, err := c.GetTags(ctx, &apigateway.GetTagsInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	_, has := tags2.Tags["env"]
	assert.False(t, has, "untag should drop env")
	assert.Equal(t, "platform", tags2.Tags["team"])
}

// TestAPIGateway_DomainVpcUsageAndAccess covers UpdateDomainName, UpdateVpcLink,
// UpdateUsage, and the domain-name access-association operations
// (CreateDomainNameAccessAssociation, GetDomainNameAccessAssociations,
// RejectDomainNameAccessAssociation, DeleteDomainNameAccessAssociation).
func TestAPIGateway_DomainVpcUsageAndAccess(t *testing.T) {
	c := apigwClient()

	// UpdateDomainName patches a custom domain's certificate settings.
	upDomain, err := c.UpdateDomainName(ctx, &apigateway.UpdateDomainNameInput{
		DomainName:      aws.String("api.example.com"),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/certificateName", "prod-cert")},
	})
	require.NoError(t, err)
	assert.Equal(t, "prod-cert", aws.ToString(upDomain.CertificateName))

	// UpdateVpcLink patches a VPC link's name.
	upLink, err := c.UpdateVpcLink(ctx, &apigateway.UpdateVpcLinkInput{
		VpcLinkId:       aws.String("vpclink-1"),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/name", "private-backends")},
	})
	require.NoError(t, err)
	assert.Equal(t, "private-backends", aws.ToString(upLink.Name))

	// UpdateUsage against a usage-plan key.
	up, err := c.CreateUsagePlan(ctx, &apigateway.CreateUsagePlanInput{
		Name:  aws.String("plan"),
		Quota: &apigwtypes.QuotaSettings{Limit: 1000, Period: apigwtypes.QuotaPeriodTypeMonth},
	})
	require.NoError(t, err)
	upId := aws.ToString(up.Id)
	t.Cleanup(func() { _, _ = c.DeleteUsagePlan(ctx, &apigateway.DeleteUsagePlanInput{UsagePlanId: aws.String(upId)}) })
	key, err := c.CreateApiKey(ctx, &apigateway.CreateApiKeyInput{Name: aws.String("uk"), Enabled: true})
	require.NoError(t, err)
	keyId := aws.ToString(key.Id)
	t.Cleanup(func() { _, _ = c.DeleteApiKey(ctx, &apigateway.DeleteApiKeyInput{ApiKey: aws.String(keyId)}) })
	_, err = c.CreateUsagePlanKey(ctx, &apigateway.CreateUsagePlanKeyInput{
		UsagePlanId: aws.String(upId), KeyId: aws.String(keyId), KeyType: aws.String("API_KEY"),
	})
	require.NoError(t, err)
	usage, err := c.UpdateUsage(ctx, &apigateway.UpdateUsageInput{
		UsagePlanId: aws.String(upId), KeyId: aws.String(keyId),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/remaining", "500")},
	})
	require.NoError(t, err)
	assert.Equal(t, upId, aws.ToString(usage.UsagePlanId))
	require.Contains(t, usage.Items, keyId)

	// Domain-name access associations.
	assoc, err := c.CreateDomainNameAccessAssociation(ctx, &apigateway.CreateDomainNameAccessAssociationInput{
		DomainNameArn:               aws.String("arn:aws:apigateway:us-east-1::/domainnames/api.example.com"),
		AccessAssociationSource:     aws.String("arn:aws:ec2:us-east-1:111122223333:vpc-endpoint/vpce-123"),
		AccessAssociationSourceType: apigwtypes.AccessAssociationSourceTypeVpce,
	})
	require.NoError(t, err)
	assocArn := aws.ToString(assoc.DomainNameAccessAssociationArn)
	require.NotEmpty(t, assocArn)

	list, err := c.GetDomainNameAccessAssociations(ctx, &apigateway.GetDomainNameAccessAssociationsInput{})
	require.NoError(t, err)
	var found bool
	for _, a := range list.Items {
		if aws.ToString(a.DomainNameAccessAssociationArn) == assocArn {
			found = true
		}
	}
	assert.True(t, found, "created access association should be listed")

	// Reject removes a pending association; create a second one to reject.
	assoc2, err := c.CreateDomainNameAccessAssociation(ctx, &apigateway.CreateDomainNameAccessAssociationInput{
		DomainNameArn:               aws.String("arn:aws:apigateway:us-east-1::/domainnames/api2.example.com"),
		AccessAssociationSource:     aws.String("arn:aws:ec2:us-east-1:111122223333:vpc-endpoint/vpce-456"),
		AccessAssociationSourceType: apigwtypes.AccessAssociationSourceTypeVpce,
	})
	require.NoError(t, err)
	_, err = c.RejectDomainNameAccessAssociation(ctx, &apigateway.RejectDomainNameAccessAssociationInput{
		DomainNameAccessAssociationArn: assoc2.DomainNameAccessAssociationArn,
		DomainNameArn:                  aws.String("arn:aws:apigateway:us-east-1::/domainnames/api2.example.com"),
	})
	require.NoError(t, err)

	_, err = c.DeleteDomainNameAccessAssociation(ctx, &apigateway.DeleteDomainNameAccessAssociationInput{
		DomainNameAccessAssociationArn: aws.String(assocArn),
	})
	require.NoError(t, err)
}
