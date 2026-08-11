package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIGateway_RestApiLifecycle(t *testing.T) {
	createdOut := runCLI(t, awsCLI("apigateway", "create-rest-api",
		"--name", "cli-rest-api",
		"--description", "cli coverage",
	))
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(createdOut), &created))
	require.NotEmpty(t, created.ID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigateway", "delete-rest-api", "--rest-api-id", created.ID))
	})

	apisOut := runCLI(t, awsCLI("apigateway", "get-rest-apis"))
	assert.Contains(t, apisOut, created.ID)

	resourcesOut := runCLI(t, awsCLI("apigateway", "get-resources", "--rest-api-id", created.ID))
	var resources struct {
		Items []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(resourcesOut), &resources))
	require.Len(t, resources.Items, 1)
	rootID := resources.Items[0].ID

	resourceOut := runCLI(t, awsCLI("apigateway", "create-resource",
		"--rest-api-id", created.ID,
		"--parent-id", rootID,
		"--path-part", "cli",
	))
	var resource struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	require.NoError(t, json.Unmarshal([]byte(resourceOut), &resource))
	require.NotEmpty(t, resource.ID)
	assert.Equal(t, "/cli", resource.Path)

	runCLI(t, awsCLI("apigateway", "put-method",
		"--rest-api-id", created.ID,
		"--resource-id", resource.ID,
		"--http-method", "GET",
		"--authorization-type", "NONE",
	))
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-method",
		"--rest-api-id", created.ID,
		"--resource-id", resource.ID,
		"--http-method", "GET",
	)), `"httpMethod": "GET"`)

	runCLI(t, awsCLI("apigateway", "put-integration",
		"--rest-api-id", created.ID,
		"--resource-id", resource.ID,
		"--http-method", "GET",
		"--type", "MOCK",
		"--request-templates", `{"application/json":"{\"statusCode\":200}"}`,
	))
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-integration",
		"--rest-api-id", created.ID,
		"--resource-id", resource.ID,
		"--http-method", "GET",
	)), `"type": "MOCK"`)

	deploymentOut := runCLI(t, awsCLI("apigateway", "create-deployment",
		"--rest-api-id", created.ID,
		"--description", "cli deployment",
	))
	var deployment struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(deploymentOut), &deployment))
	require.NotEmpty(t, deployment.ID)

	runCLI(t, awsCLI("apigateway", "create-stage",
		"--rest-api-id", created.ID,
		"--stage-name", "cli",
		"--deployment-id", deployment.ID,
	))
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-stage",
		"--rest-api-id", created.ID,
		"--stage-name", "cli",
	)), `"stageName": "cli"`)

	// List endpoints: the deployment + stage just created appear.
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-deployments",
		"--rest-api-id", created.ID,
	)), deployment.ID)
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-stages",
		"--rest-api-id", created.ID,
	)), `"stageName": "cli"`)

	runCLI(t, awsCLI("apigateway", "delete-stage",
		"--rest-api-id", created.ID,
		"--stage-name", "cli",
	))
	runCLI(t, awsCLI("apigateway", "delete-deployment",
		"--rest-api-id", created.ID,
		"--deployment-id", deployment.ID,
	))
	runCLI(t, awsCLI("apigateway", "delete-integration",
		"--rest-api-id", created.ID,
		"--resource-id", resource.ID,
		"--http-method", "GET",
	))
	runCLI(t, awsCLI("apigateway", "delete-method",
		"--rest-api-id", created.ID,
		"--resource-id", resource.ID,
		"--http-method", "GET",
	))
	runCLI(t, awsCLI("apigateway", "delete-resource",
		"--rest-api-id", created.ID,
		"--resource-id", resource.ID,
	))
}

func TestAPIGatewayV2_HttpApiLifecycle(t *testing.T) {
	apiOut := runCLI(t, awsCLI("apigatewayv2", "create-api",
		"--name", "cli-http-api",
		"--protocol-type", "HTTP",
	))
	var api struct {
		APIID string `json:"ApiId"`
	}
	require.NoError(t, json.Unmarshal([]byte(apiOut), &api))
	require.NotEmpty(t, api.APIID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigatewayv2", "delete-api", "--api-id", api.APIID))
	})

	apis := runCLI(t, awsCLI("apigatewayv2", "get-apis"))
	assert.Contains(t, apis, api.APIID)
	assert.Contains(t, apis, `"$request.header.x-api-key"`)

	integrationOut := runCLI(t, awsCLI("apigatewayv2", "create-integration",
		"--api-id", api.APIID,
		"--integration-type", "HTTP_PROXY",
		"--integration-uri", "https://example.com",
		"--integration-method", "GET",
		"--payload-format-version", "1.0",
	))
	var integration struct {
		IntegrationID string `json:"IntegrationId"`
	}
	require.NoError(t, json.Unmarshal([]byte(integrationOut), &integration))
	require.NotEmpty(t, integration.IntegrationID)
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-integration",
		"--api-id", api.APIID,
		"--integration-id", integration.IntegrationID,
	)), `"ConnectionType": "INTERNET"`)
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "update-integration",
		"--api-id", api.APIID,
		"--integration-id", integration.IntegrationID,
		"--integration-method", "POST",
	)), `"IntegrationMethod": "POST"`)

	routeOut := runCLI(t, awsCLI("apigatewayv2", "create-route",
		"--api-id", api.APIID,
		"--route-key", "GET /cli",
		"--target", "integrations/"+integration.IntegrationID,
	))
	var route struct {
		RouteID string `json:"RouteId"`
	}
	require.NoError(t, json.Unmarshal([]byte(routeOut), &route))
	require.NotEmpty(t, route.RouteID)
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-route",
		"--api-id", api.APIID,
		"--route-id", route.RouteID,
	)), route.RouteID)

	// UpdateRoute (PATCH) changes the route key; GetRoute reflects it.
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "update-route",
		"--api-id", api.APIID,
		"--route-id", route.RouteID,
		"--route-key", "POST /cli-updated",
	)), `"RouteKey": "POST /cli-updated"`)
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-route",
		"--api-id", api.APIID,
		"--route-id", route.RouteID,
	)), `"RouteKey": "POST /cli-updated"`)

	deploymentOut := runCLI(t, awsCLI("apigatewayv2", "create-deployment",
		"--api-id", api.APIID,
		"--description", "cli deployment",
	))
	var deployment struct {
		DeploymentID string `json:"DeploymentId"`
	}
	require.NoError(t, json.Unmarshal([]byte(deploymentOut), &deployment))
	require.NotEmpty(t, deployment.DeploymentID)

	runCLI(t, awsCLI("apigatewayv2", "create-stage",
		"--api-id", api.APIID,
		"--stage-name", "cli",
		"--deployment-id", deployment.DeploymentID,
		"--tags", "consumer=ecs-dev-desktop",
	))
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-stage",
		"--api-id", api.APIID,
		"--stage-name", "cli",
	)), `"consumer": "ecs-dev-desktop"`)
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "update-stage",
		"--api-id", api.APIID,
		"--stage-name", "cli",
		"--description", "consumer deployment",
	)), `"Description": "consumer deployment"`)

	runCLI(t, awsCLI("apigatewayv2", "delete-stage",
		"--api-id", api.APIID,
		"--stage-name", "cli",
	))
	runCLI(t, awsCLI("apigatewayv2", "delete-deployment",
		"--api-id", api.APIID,
		"--deployment-id", deployment.DeploymentID,
	))
	runCLI(t, awsCLI("apigatewayv2", "delete-route",
		"--api-id", api.APIID,
		"--route-id", route.RouteID,
	))
	runCLI(t, awsCLI("apigatewayv2", "delete-integration",
		"--api-id", api.APIID,
		"--integration-id", integration.IntegrationID,
	))
}

// TestAPIGateway_ApiKeysUsagePlansCLI exercises the v1 API-key + usage-plan
// surface through the aws CLI: create-api-key / get-api-key / get-api-keys /
// update-api-key, create-usage-plan / get-usage-plan / get-usage-plans /
// update-usage-plan, create-usage-plan-key / get-usage-plan-key /
// get-usage-plan-keys, plus the deletes.
func TestAPIGateway_ApiKeysUsagePlansCLI(t *testing.T) {
	keyOut := runCLI(t, awsCLI("apigateway", "create-api-key",
		"--name", "cli-key", "--description", "cli", "--enabled",
	))
	var key struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal([]byte(keyOut), &key))
	require.NotEmpty(t, key.ID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigateway", "delete-api-key", "--api-key", key.ID))
	})

	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-api-key", "--api-key", key.ID)), `"cli-key"`)
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-api-keys")), key.ID)

	updOut := runCLI(t, awsCLI("apigateway", "update-api-key",
		"--api-key", key.ID,
		"--patch-operations", "op=replace,path=/description,value=cli-updated",
	))
	assert.Contains(t, updOut, "cli-updated")

	planOut := runCLI(t, awsCLI("apigateway", "create-usage-plan",
		"--name", "cli-plan",
		"--throttle", "burstLimit=200,rateLimit=100",
		"--quota", "limit=5000,period=MONTH",
	))
	var plan struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(planOut), &plan))
	require.NotEmpty(t, plan.ID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigateway", "delete-usage-plan", "--usage-plan-id", plan.ID))
	})

	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-usage-plan", "--usage-plan-id", plan.ID)), `"cli-plan"`)
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-usage-plans")), plan.ID)
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "update-usage-plan",
		"--usage-plan-id", plan.ID,
		"--patch-operations", "op=replace,path=/description,value=plan-cli-updated",
	)), "plan-cli-updated")

	upkOut := runCLI(t, awsCLI("apigateway", "create-usage-plan-key",
		"--usage-plan-id", plan.ID, "--key-id", key.ID, "--key-type", "API_KEY",
	))
	var upk struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(upkOut), &upk))
	require.Equal(t, key.ID, upk.ID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigateway", "delete-usage-plan-key", "--usage-plan-id", plan.ID, "--key-id", key.ID))
	})

	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-usage-plan-key",
		"--usage-plan-id", plan.ID, "--key-id", key.ID)), key.ID)
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-usage-plan-keys",
		"--usage-plan-id", plan.ID)), key.ID)
}

// TestAPIGateway_ModelsValidatorsAuthorizersCLI exercises the v1 REST-API-scoped
// model / request-validator / authorizer CRUD through the aws CLI.
func TestAPIGateway_ModelsValidatorsAuthorizersCLI(t *testing.T) {
	apiOut := runCLI(t, awsCLI("apigateway", "create-rest-api", "--name", "cli-mva-api"))
	var api struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(apiOut), &api))
	require.NotEmpty(t, api.ID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigateway", "delete-rest-api", "--rest-api-id", api.ID))
	})

	runCLI(t, awsCLI("apigateway", "create-model",
		"--rest-api-id", api.ID, "--name", "CliModel",
		"--content-type", "application/json", "--schema", `{"type":"object"}`,
	))
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-model",
		"--rest-api-id", api.ID, "--model-name", "CliModel")), `"CliModel"`)
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-models", "--rest-api-id", api.ID)), "CliModel")
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigateway", "delete-model", "--rest-api-id", api.ID, "--model-name", "CliModel"))
	})

	rvOut := runCLI(t, awsCLI("apigateway", "create-request-validator",
		"--rest-api-id", api.ID, "--name", "cli-validator", "--validate-request-body",
	))
	var rv struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(rvOut), &rv))
	require.NotEmpty(t, rv.ID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigateway", "delete-request-validator",
			"--rest-api-id", api.ID, "--request-validator-id", rv.ID))
	})
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-request-validators", "--rest-api-id", api.ID)), rv.ID)

	authOut := runCLI(t, awsCLI("apigateway", "create-authorizer",
		"--rest-api-id", api.ID, "--name", "cli-authorizer", "--type", "TOKEN",
		"--authorizer-uri", "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:auth/invocations",
		"--identity-source", "method.request.header.Authorization",
	))
	var auth struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(authOut), &auth))
	require.NotEmpty(t, auth.ID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigateway", "delete-authorizer",
			"--rest-api-id", api.ID, "--authorizer-id", auth.ID))
	})
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-authorizer",
		"--rest-api-id", api.ID, "--authorizer-id", auth.ID)), `"cli-authorizer"`)
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-authorizers", "--rest-api-id", api.ID)), auth.ID)
}

// TestAPIGatewayV2_AuthorizersModelsCLI exercises the v2 API-scoped authorizer
// + model CRUD through the aws CLI (output is PascalCase).
func TestAPIGatewayV2_AuthorizersModelsCLI(t *testing.T) {
	apiOut := runCLI(t, awsCLI("apigatewayv2", "create-api",
		"--name", "cli-v2-am", "--protocol-type", "HTTP"))
	var api struct {
		APIID string `json:"ApiId"`
	}
	require.NoError(t, json.Unmarshal([]byte(apiOut), &api))
	require.NotEmpty(t, api.APIID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigatewayv2", "delete-api", "--api-id", api.APIID))
	})

	authOut := runCLI(t, awsCLI("apigatewayv2", "create-authorizer",
		"--api-id", api.APIID, "--name", "cli-jwt", "--authorizer-type", "JWT",
		"--identity-source", "$request.header.Authorization",
		"--jwt-configuration", "Audience=aud1,Issuer=https://issuer.example.com",
	))
	var auth struct {
		AuthorizerID string `json:"AuthorizerId"`
	}
	require.NoError(t, json.Unmarshal([]byte(authOut), &auth))
	require.NotEmpty(t, auth.AuthorizerID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigatewayv2", "delete-authorizer",
			"--api-id", api.APIID, "--authorizer-id", auth.AuthorizerID))
	})
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-authorizer",
		"--api-id", api.APIID, "--authorizer-id", auth.AuthorizerID)), `"cli-jwt"`)
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-authorizers", "--api-id", api.APIID)), auth.AuthorizerID)
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "update-authorizer",
		"--api-id", api.APIID, "--authorizer-id", auth.AuthorizerID, "--name", "cli-jwt-renamed")), "cli-jwt-renamed")

	modelOut := runCLI(t, awsCLI("apigatewayv2", "create-model",
		"--api-id", api.APIID, "--name", "CliV2Model",
		"--content-type", "application/json", "--schema", `{"type":"object"}`,
	))
	var model struct {
		ModelID string `json:"ModelId"`
	}
	require.NoError(t, json.Unmarshal([]byte(modelOut), &model))
	require.NotEmpty(t, model.ModelID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigatewayv2", "delete-model", "--api-id", api.APIID, "--model-id", model.ModelID))
	})
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-model",
		"--api-id", api.APIID, "--model-id", model.ModelID)), `"CliV2Model"`)
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-models", "--api-id", api.APIID)), "CliV2Model")
}

// TestAPIGatewayV2_DomainNamesMappingsVpcLinksCLI exercises the v2 domain-name +
// API-mapping + VPC-link surface through the aws CLI.
func TestAPIGatewayV2_DomainNamesMappingsVpcLinksCLI(t *testing.T) {
	domain := fmt.Sprintf("cli-%d.example.com", time.Now().UnixNano())
	dnOut := runCLI(t, awsCLI("apigatewayv2", "create-domain-name",
		"--domain-name", domain,
		"--domain-name-configurations", "EndpointType=REGIONAL,CertificateArn=arn:aws:acm:us-east-1:000000000000:certificate/abc",
	))
	var dn struct {
		DomainName string `json:"DomainName"`
	}
	require.NoError(t, json.Unmarshal([]byte(dnOut), &dn))
	require.Equal(t, domain, dn.DomainName)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigatewayv2", "delete-domain-name", "--domain-name", domain))
	})

	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-domain-name", "--domain-name", domain)), domain)
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-domain-names")), domain)

	apiOut := runCLI(t, awsCLI("apigatewayv2", "create-api", "--name", "cli-v2-mapping", "--protocol-type", "HTTP"))
	var api struct {
		APIID string `json:"ApiId"`
	}
	require.NoError(t, json.Unmarshal([]byte(apiOut), &api))
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigatewayv2", "delete-api", "--api-id", api.APIID))
	})
	runCLI(t, awsCLI("apigatewayv2", "create-stage", "--api-id", api.APIID, "--stage-name", "prod"))

	mapOut := runCLI(t, awsCLI("apigatewayv2", "create-api-mapping",
		"--domain-name", domain, "--api-id", api.APIID, "--stage", "prod", "--api-mapping-key", "v1",
	))
	var mapping struct {
		ApiMappingID string `json:"ApiMappingId"`
	}
	require.NoError(t, json.Unmarshal([]byte(mapOut), &mapping))
	require.NotEmpty(t, mapping.ApiMappingID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigatewayv2", "delete-api-mapping",
			"--domain-name", domain, "--api-mapping-id", mapping.ApiMappingID))
	})
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-api-mapping",
		"--domain-name", domain, "--api-mapping-id", mapping.ApiMappingID)), `"v1"`)
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-api-mappings", "--domain-name", domain)), mapping.ApiMappingID)

	vlOut := runCLI(t, awsCLI("apigatewayv2", "create-vpc-link",
		"--name", "cli-vpclink", "--subnet-ids", "subnet-aaaa1111",
	))
	var vl struct {
		VpcLinkID string `json:"VpcLinkId"`
	}
	require.NoError(t, json.Unmarshal([]byte(vlOut), &vl))
	require.NotEmpty(t, vl.VpcLinkID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigatewayv2", "delete-vpc-link", "--vpc-link-id", vl.VpcLinkID))
	})
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-vpc-link", "--vpc-link-id", vl.VpcLinkID)), `"cli-vpclink"`)
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-vpc-links")), vl.VpcLinkID)
}
