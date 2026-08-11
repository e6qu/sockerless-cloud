package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigwtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// apigwNewRestAPIWithStage creates a REST API with a stage backed by a real
// deployment, returning the api id and stage name — the fixture GetExport /
// GetSdk and the documentation/gateway-response ops attach to.
func apigwNewRestAPIWithStage(t *testing.T, c *apigateway.Client, name string) (string, string) {
	t.Helper()
	api, err := c.CreateRestApi(ctx, &apigateway.CreateRestApiInput{Name: aws.String(name)})
	require.NoError(t, err)
	apiID := aws.ToString(api.Id)
	t.Cleanup(func() {
		_, _ = c.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: aws.String(apiID)})
	})
	// Add a GET method on the root resource so the export carries a path.
	_, err = c.PutMethod(ctx, &apigateway.PutMethodInput{
		RestApiId:         aws.String(apiID),
		ResourceId:        api.RootResourceId,
		HttpMethod:        aws.String("GET"),
		AuthorizationType: aws.String("NONE"),
	})
	require.NoError(t, err)
	dep, err := c.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{
		RestApiId: aws.String(apiID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(dep.Id))
	_, err = c.CreateStage(ctx, &apigateway.CreateStageInput{
		RestApiId:    aws.String(apiID),
		StageName:    aws.String("prod"),
		DeploymentId: dep.Id,
	})
	require.NoError(t, err)
	return apiID, "prod"
}

// TestAPIGateway_BasePathMappings exercises the base-path-mapping CRUD cycle
// under a custom domain name: CreateBasePathMapping → GetBasePathMappings →
// GetBasePathMapping → UpdateBasePathMapping → DeleteBasePathMapping.
func TestAPIGateway_BasePathMappings(t *testing.T) {
	c := apigwClient()
	apiID, stage := apigwNewRestAPIWithStage(t, c, "bpm-api")
	domain := "bpm.example.com"

	create, err := c.CreateBasePathMapping(ctx, &apigateway.CreateBasePathMappingInput{
		DomainName: aws.String(domain),
		RestApiId:  aws.String(apiID),
		BasePath:   aws.String("v1"),
		Stage:      aws.String(stage),
	})
	require.NoError(t, err)
	assert.Equal(t, "v1", aws.ToString(create.BasePath))
	assert.Equal(t, apiID, aws.ToString(create.RestApiId))

	list, err := c.GetBasePathMappings(ctx, &apigateway.GetBasePathMappingsInput{DomainName: aws.String(domain)})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)

	get, err := c.GetBasePathMapping(ctx, &apigateway.GetBasePathMappingInput{
		DomainName: aws.String(domain),
		BasePath:   aws.String("v1"),
	})
	require.NoError(t, err)
	assert.Equal(t, stage, aws.ToString(get.Stage))

	upd, err := c.UpdateBasePathMapping(ctx, &apigateway.UpdateBasePathMappingInput{
		DomainName: aws.String(domain),
		BasePath:   aws.String("v1"),
		PatchOperations: []apigwtypes.PatchOperation{
			{Op: apigwtypes.OpReplace, Path: aws.String("/stage"), Value: aws.String("prod")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "prod", aws.ToString(upd.Stage))

	_, err = c.DeleteBasePathMapping(ctx, &apigateway.DeleteBasePathMappingInput{
		DomainName: aws.String(domain),
		BasePath:   aws.String("v1"),
	})
	require.NoError(t, err)
}

// TestAPIGateway_ClientCertificates exercises GenerateClientCertificate →
// GetClientCertificates → GetClientCertificate → UpdateClientCertificate →
// DeleteClientCertificate, asserting the generated PEM is real-shaped.
func TestAPIGateway_ClientCertificates(t *testing.T) {
	c := apigwClient()
	gen, err := c.GenerateClientCertificate(ctx, &apigateway.GenerateClientCertificateInput{
		Description: aws.String("integration cert"),
	})
	require.NoError(t, err)
	certID := aws.ToString(gen.ClientCertificateId)
	require.NotEmpty(t, certID)
	assert.Contains(t, aws.ToString(gen.PemEncodedCertificate), "BEGIN CERTIFICATE")
	t.Cleanup(func() {
		_, _ = c.DeleteClientCertificate(ctx, &apigateway.DeleteClientCertificateInput{ClientCertificateId: aws.String(certID)})
	})

	list, err := c.GetClientCertificates(ctx, &apigateway.GetClientCertificatesInput{})
	require.NoError(t, err)
	found := false
	for _, cc := range list.Items {
		if aws.ToString(cc.ClientCertificateId) == certID {
			found = true
		}
	}
	assert.True(t, found)

	get, err := c.GetClientCertificate(ctx, &apigateway.GetClientCertificateInput{ClientCertificateId: aws.String(certID)})
	require.NoError(t, err)
	assert.Equal(t, "integration cert", aws.ToString(get.Description))

	upd, err := c.UpdateClientCertificate(ctx, &apigateway.UpdateClientCertificateInput{
		ClientCertificateId: aws.String(certID),
		PatchOperations: []apigwtypes.PatchOperation{
			{Op: apigwtypes.OpReplace, Path: aws.String("/description"), Value: aws.String("updated cert")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "updated cert", aws.ToString(upd.Description))

	_, err = c.DeleteClientCertificate(ctx, &apigateway.DeleteClientCertificateInput{ClientCertificateId: aws.String(certID)})
	require.NoError(t, err)
}

// TestAPIGateway_Documentation exercises documentation parts and versions:
// CreateDocumentationPart → GetDocumentationParts → GetDocumentationPart →
// UpdateDocumentationPart → ImportDocumentationParts →
// CreateDocumentationVersion → GetDocumentationVersions →
// GetDocumentationVersion → UpdateDocumentationVersion → Delete{Part,Version}.
func TestAPIGateway_Documentation(t *testing.T) {
	c := apigwClient()
	apiID, _ := apigwNewRestAPIWithStage(t, c, "doc-api")

	part, err := c.CreateDocumentationPart(ctx, &apigateway.CreateDocumentationPartInput{
		RestApiId:  aws.String(apiID),
		Location:   &apigwtypes.DocumentationPartLocation{Type: apigwtypes.DocumentationPartTypeApi},
		Properties: aws.String(`{"description":"My API"}`),
	})
	require.NoError(t, err)
	partID := aws.ToString(part.Id)
	require.NotEmpty(t, partID)

	parts, err := c.GetDocumentationParts(ctx, &apigateway.GetDocumentationPartsInput{RestApiId: aws.String(apiID)})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(parts.Items), 1)

	getPart, err := c.GetDocumentationPart(ctx, &apigateway.GetDocumentationPartInput{
		RestApiId:           aws.String(apiID),
		DocumentationPartId: aws.String(partID),
	})
	require.NoError(t, err)
	assert.Equal(t, apigwtypes.DocumentationPartTypeApi, getPart.Location.Type)

	updPart, err := c.UpdateDocumentationPart(ctx, &apigateway.UpdateDocumentationPartInput{
		RestApiId:           aws.String(apiID),
		DocumentationPartId: aws.String(partID),
		PatchOperations: []apigwtypes.PatchOperation{
			{Op: apigwtypes.OpReplace, Path: aws.String("/properties"), Value: aws.String(`{"description":"Updated"}`)},
		},
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(updPart.Properties), "Updated")

	imp, err := c.ImportDocumentationParts(ctx, &apigateway.ImportDocumentationPartsInput{
		RestApiId: aws.String(apiID),
		Body:      []byte(`{"openapi":"3.0.1","info":{"title":"x"}}`),
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(imp.Ids), 1)

	ver, err := c.CreateDocumentationVersion(ctx, &apigateway.CreateDocumentationVersionInput{
		RestApiId:            aws.String(apiID),
		DocumentationVersion: aws.String("1.0.0"),
		Description:          aws.String("first cut"),
	})
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", aws.ToString(ver.Version))

	vers, err := c.GetDocumentationVersions(ctx, &apigateway.GetDocumentationVersionsInput{RestApiId: aws.String(apiID)})
	require.NoError(t, err)
	require.Len(t, vers.Items, 1)

	getVer, err := c.GetDocumentationVersion(ctx, &apigateway.GetDocumentationVersionInput{
		RestApiId:            aws.String(apiID),
		DocumentationVersion: aws.String("1.0.0"),
	})
	require.NoError(t, err)
	assert.Equal(t, "first cut", aws.ToString(getVer.Description))

	updVer, err := c.UpdateDocumentationVersion(ctx, &apigateway.UpdateDocumentationVersionInput{
		RestApiId:            aws.String(apiID),
		DocumentationVersion: aws.String("1.0.0"),
		PatchOperations: []apigwtypes.PatchOperation{
			{Op: apigwtypes.OpReplace, Path: aws.String("/description"), Value: aws.String("released")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "released", aws.ToString(updVer.Description))

	_, err = c.DeleteDocumentationVersion(ctx, &apigateway.DeleteDocumentationVersionInput{
		RestApiId:            aws.String(apiID),
		DocumentationVersion: aws.String("1.0.0"),
	})
	require.NoError(t, err)

	_, err = c.DeleteDocumentationPart(ctx, &apigateway.DeleteDocumentationPartInput{
		RestApiId:           aws.String(apiID),
		DocumentationPartId: aws.String(partID),
	})
	require.NoError(t, err)
}

// TestAPIGateway_GatewayResponses exercises the gateway-response slice with
// its seeded defaults: GetGatewayResponses → PutGatewayResponse →
// GetGatewayResponse → UpdateGatewayResponse → DeleteGatewayResponse.
func TestAPIGateway_GatewayResponses(t *testing.T) {
	c := apigwClient()
	apiID, _ := apigwNewRestAPIWithStage(t, c, "gr-api")

	defaults, err := c.GetGatewayResponses(ctx, &apigateway.GetGatewayResponsesInput{RestApiId: aws.String(apiID)})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(defaults.Items), 20)

	put, err := c.PutGatewayResponse(ctx, &apigateway.PutGatewayResponseInput{
		RestApiId:    aws.String(apiID),
		ResponseType: apigwtypes.GatewayResponseTypeDefault4xx,
		StatusCode:   aws.String("444"),
	})
	require.NoError(t, err)
	assert.Equal(t, "444", aws.ToString(put.StatusCode))

	get, err := c.GetGatewayResponse(ctx, &apigateway.GetGatewayResponseInput{
		RestApiId:    aws.String(apiID),
		ResponseType: apigwtypes.GatewayResponseTypeDefault4xx,
	})
	require.NoError(t, err)
	assert.Equal(t, "444", aws.ToString(get.StatusCode))
	assert.False(t, get.DefaultResponse)

	// A type with no override reports defaultResponse=true.
	getDefault, err := c.GetGatewayResponse(ctx, &apigateway.GetGatewayResponseInput{
		RestApiId:    aws.String(apiID),
		ResponseType: apigwtypes.GatewayResponseTypeUnauthorized,
	})
	require.NoError(t, err)
	assert.True(t, getDefault.DefaultResponse)

	upd, err := c.UpdateGatewayResponse(ctx, &apigateway.UpdateGatewayResponseInput{
		RestApiId:    aws.String(apiID),
		ResponseType: apigwtypes.GatewayResponseTypeDefault4xx,
		PatchOperations: []apigwtypes.PatchOperation{
			{Op: apigwtypes.OpReplace, Path: aws.String("/statusCode"), Value: aws.String("455")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "455", aws.ToString(upd.StatusCode))

	_, err = c.DeleteGatewayResponse(ctx, &apigateway.DeleteGatewayResponseInput{
		RestApiId:    aws.String(apiID),
		ResponseType: apigwtypes.GatewayResponseTypeDefault4xx,
	})
	require.NoError(t, err)
}

// TestAPIGateway_AccountAndMisc exercises GetAccount → UpdateAccount, the
// request-validator and model-template reads, and the SDK-type catalog
// (GetSdkTypes → GetSdkType).
func TestAPIGateway_AccountAndMisc(t *testing.T) {
	c := apigwClient()

	acct, err := c.GetAccount(ctx, &apigateway.GetAccountInput{})
	require.NoError(t, err)
	assert.Contains(t, acct.Features, "UsagePlans")

	updAcct, err := c.UpdateAccount(ctx, &apigateway.UpdateAccountInput{
		PatchOperations: []apigwtypes.PatchOperation{
			{Op: apigwtypes.OpReplace, Path: aws.String("/cloudwatchRoleArn"), Value: aws.String("arn:aws:iam::123456789012:role/cw")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::123456789012:role/cw", aws.ToString(updAcct.CloudwatchRoleArn))

	sdkTypes, err := c.GetSdkTypes(ctx, &apigateway.GetSdkTypesInput{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(sdkTypes.Items), 1)

	sdkType, err := c.GetSdkType(ctx, &apigateway.GetSdkTypeInput{Id: aws.String("java")})
	require.NoError(t, err)
	assert.Equal(t, "java", aws.ToString(sdkType.Id))
	assert.NotEmpty(t, sdkType.ConfigurationProperties)

	// Request validator round-trip via GetRequestValidator.
	apiID, _ := apigwNewRestAPIWithStage(t, c, "rv-api")
	rv, err := c.CreateRequestValidator(ctx, &apigateway.CreateRequestValidatorInput{
		RestApiId:           aws.String(apiID),
		Name:                aws.String("validate-body"),
		ValidateRequestBody: true,
	})
	require.NoError(t, err)
	getRV, err := c.GetRequestValidator(ctx, &apigateway.GetRequestValidatorInput{
		RestApiId:          aws.String(apiID),
		RequestValidatorId: rv.Id,
	})
	require.NoError(t, err)
	assert.True(t, getRV.ValidateRequestBody)

	// Model default template via GetModelTemplate.
	model, err := c.CreateModel(ctx, &apigateway.CreateModelInput{
		RestApiId:   aws.String(apiID),
		Name:        aws.String("Pet"),
		ContentType: aws.String("application/json"),
		Schema:      aws.String(`{"type":"object"}`),
	})
	require.NoError(t, err)
	tmpl, err := c.GetModelTemplate(ctx, &apigateway.GetModelTemplateInput{
		RestApiId: aws.String(apiID),
		ModelName: model.Name,
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(tmpl.Value), "Pet")
}

// TestAPIGateway_ExportSdkUsageTags exercises GetExport → GetSdk against a
// deployed stage, GetUsage off a usage plan, and GetTags off a stored
// resource ARN.
func TestAPIGateway_ExportSdkUsageTags(t *testing.T) {
	c := apigwClient()
	apiID, stage := apigwNewRestAPIWithStage(t, c, "export-api")

	exp, err := c.GetExport(ctx, &apigateway.GetExportInput{
		RestApiId:  aws.String(apiID),
		StageName:  aws.String(stage),
		ExportType: aws.String("oas30"),
	})
	require.NoError(t, err)
	assert.Contains(t, string(exp.Body), "openapi")

	sdk, err := c.GetSdk(ctx, &apigateway.GetSdkInput{
		RestApiId: aws.String(apiID),
		StageName: aws.String(stage),
		SdkType:   aws.String("java"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, sdk.Body)

	// Usage plan with the api stage, then GetUsage.
	up, err := c.CreateUsagePlan(ctx, &apigateway.CreateUsagePlanInput{
		Name: aws.String("export-plan"),
		ApiStages: []apigwtypes.ApiStage{
			{ApiId: aws.String(apiID), Stage: aws.String(stage)},
		},
		Quota: &apigwtypes.QuotaSettings{Limit: 1000, Period: apigwtypes.QuotaPeriodTypeMonth},
	})
	require.NoError(t, err)
	upID := aws.ToString(up.Id)
	t.Cleanup(func() {
		_, _ = c.DeleteUsagePlan(ctx, &apigateway.DeleteUsagePlanInput{UsagePlanId: aws.String(upID)})
	})
	usage, err := c.GetUsage(ctx, &apigateway.GetUsageInput{
		UsagePlanId: aws.String(upID),
		StartDate:   aws.String("2024-01-01"),
		EndDate:     aws.String("2024-01-02"),
	})
	require.NoError(t, err)
	assert.Equal(t, upID, aws.ToString(usage.UsagePlanId))

	// GetTags off the REST API ARN.
	arn := "arn:aws:apigateway:us-east-1::/restapis/" + apiID
	tags, err := c.GetTags(ctx, &apigateway.GetTagsInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	assert.NotNil(t, tags.Tags)
}
