package aws_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIGatewayV2CLIDomainNamesAndUpdates covers apigatewayv2
// update-domain-name, tag-resource, untag-resource, get-tags,
// update-deployment, update-model and update-vpc-link, and an apigateway
// get-domain-name of a domain name apigatewayv2 created.
func TestAPIGatewayV2CLIDomainNamesAndUpdates(t *testing.T) {
	t.Parallel()
	name := "http.cli-v2-domains.example.com"
	var created struct {
		DomainNameArn            string `json:"DomainNameArn"`
		DomainNameConfigurations []struct {
			ApiGatewayDomainName string `json:"ApiGatewayDomainName"`
			CertificateArn       string `json:"CertificateArn"`
		} `json:"DomainNameConfigurations"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigatewayv2", "create-domain-name", "--domain-name", name,
		"--domain-name-configurations", "EndpointType=REGIONAL,CertificateArn="+importELBv2CertificateCLI(t, name)))), &created))
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigatewayv2", "delete-domain-name", "--domain-name", name)) })
	require.Len(t, created.DomainNameConfigurations, 1)

	var asV1 struct {
		RegionalDomainName string `json:"regionalDomainName"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "get-domain-name", "--domain-name", name))), &asV1))
	assert.Equal(t, created.DomainNameConfigurations[0].ApiGatewayDomainName, asV1.RegionalDomainName)

	renewed := importELBv2CertificateCLI(t, name)
	var updated struct {
		DomainNameConfigurations []struct {
			CertificateArn string `json:"CertificateArn"`
		} `json:"DomainNameConfigurations"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigatewayv2", "update-domain-name", "--domain-name", name,
		"--domain-name-configurations", "EndpointType=REGIONAL,CertificateArn="+renewed))), &updated))
	require.Len(t, updated.DomainNameConfigurations, 1)
	assert.Equal(t, renewed, updated.DomainNameConfigurations[0].CertificateArn)

	runCLI(t, awsCLI("apigatewayv2", "tag-resource", "--resource-arn", created.DomainNameArn, "--tags", "env=prod,team=http"))
	runCLI(t, awsCLI("apigatewayv2", "untag-resource", "--resource-arn", created.DomainNameArn, "--tag-keys", "team"))
	var tags struct {
		Tags map[string]string `json:"Tags"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigatewayv2", "get-tags", "--resource-arn", created.DomainNameArn))), &tags))
	assert.Equal(t, map[string]string{"env": "prod"}, tags.Tags)

	var api struct {
		APIID string `json:"ApiId"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigatewayv2", "create-api", "--name", "cli-v2-updates", "--protocol-type", "HTTP"))), &api))
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigatewayv2", "delete-api", "--api-id", api.APIID)) })

	var dep struct {
		DeploymentID string `json:"DeploymentId"`
		Description  string `json:"Description"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigatewayv2", "create-deployment", "--api-id", api.APIID))), &dep))
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigatewayv2", "update-deployment",
		"--api-id", api.APIID, "--deployment-id", dep.DeploymentID, "--description", "second"))), &dep))
	assert.Equal(t, "second", dep.Description)

	var model struct {
		ModelID     string `json:"ModelId"`
		Name        string `json:"Name"`
		Description string `json:"Description"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigatewayv2", "create-model", "--api-id", api.APIID,
		"--name", "Order", "--content-type", "application/json", "--schema", `{"type":"object"}`))), &model))
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigatewayv2", "update-model",
		"--api-id", api.APIID, "--model-id", model.ModelID, "--description", "an order"))), &model))
	assert.Equal(t, "an order", model.Description)
	assert.Equal(t, "Order", model.Name)

	var vl struct {
		VpcLinkID string `json:"VpcLinkId"`
		Name      string `json:"Name"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigatewayv2", "create-vpc-link",
		"--name", "cli-v2-link", "--subnet-ids", "subnet-aaaa1111"))), &vl))
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigatewayv2", "delete-vpc-link", "--vpc-link-id", vl.VpcLinkID)) })
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigatewayv2", "update-vpc-link",
		"--vpc-link-id", vl.VpcLinkID, "--name", "cli-v2-link-renamed"))), &vl))
	assert.Equal(t, "cli-v2-link-renamed", vl.Name)

	assert.Contains(t, runCLIExpectError(t, awsCLI("apigatewayv2", "update-vpc-link",
		"--vpc-link-id", "absent0000", "--name", "x")), "NotFoundException")
}

// TestAPIGatewayCLIMappingsShareOneStore expects a base path mapping and an
// API mapping on one custom domain name to be one resource: each API version
// lists what the other created, a multi-level path comes only through
// apigatewayv2, and a path already mapped is refused.
func TestAPIGatewayCLIMappingsShareOneStore(t *testing.T) {
	t.Parallel()
	name := "maps.cli-v2-domains.example.com"
	runCLI(t, awsCLI("apigatewayv2", "create-domain-name", "--domain-name", name,
		"--domain-name-configurations", "EndpointType=REGIONAL,SecurityPolicy=TLS_1_2,CertificateArn="+importELBv2CertificateCLI(t, name)))
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigatewayv2", "delete-domain-name", "--domain-name", name)) })

	var rest struct {
		ID             string `json:"id"`
		RootResourceID string `json:"rootResourceId"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "create-rest-api", "--name", "cli-maps"))), &rest))
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigateway", "delete-rest-api", "--rest-api-id", rest.ID)) })
	runCLI(t, awsCLI("apigateway", "put-method", "--rest-api-id", rest.ID, "--resource-id", rest.RootResourceID,
		"--http-method", "GET", "--authorization-type", "NONE"))
	runCLI(t, awsCLI("apigateway", "put-integration", "--rest-api-id", rest.ID, "--resource-id", rest.RootResourceID,
		"--http-method", "GET", "--type", "MOCK"))
	runCLI(t, awsCLI("apigateway", "create-deployment", "--rest-api-id", rest.ID, "--stage-name", "prod"))

	runCLI(t, awsCLI("apigateway", "create-base-path-mapping", "--domain-name", name,
		"--base-path", "orders", "--rest-api-id", rest.ID, "--stage", "prod"))
	assert.Contains(t, runCLIExpectError(t, awsCLI("apigateway", "create-base-path-mapping", "--domain-name", name,
		"--base-path", "orders/v1", "--rest-api-id", rest.ID, "--stage", "prod")), "BadRequestException")
	runCLI(t, awsCLI("apigatewayv2", "create-api-mapping", "--domain-name", name,
		"--api-mapping-key", "orders/v1/items", "--api-id", rest.ID, "--stage", "prod"))
	assert.Contains(t, runCLIExpectError(t, awsCLI("apigatewayv2", "create-api-mapping", "--domain-name", name,
		"--api-mapping-key", "orders", "--api-id", rest.ID, "--stage", "prod")), "ConflictException")

	var v2 struct {
		Items []struct {
			ApiMappingKey string `json:"ApiMappingKey"`
			ApiID         string `json:"ApiId"`
		} `json:"Items"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigatewayv2", "get-api-mappings", "--domain-name", name))), &v2))
	keys := map[string]string{}
	for _, m := range v2.Items {
		keys[m.ApiMappingKey] = m.ApiID
	}
	assert.Equal(t, map[string]string{"orders": rest.ID, "orders/v1/items": rest.ID}, keys, "apigatewayv2 lists the base path mapping")

	var v1 struct {
		Items []struct {
			BasePath string `json:"basePath"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "get-base-path-mappings", "--domain-name", name))), &v1))
	paths := []string{}
	for _, m := range v1.Items {
		paths = append(paths, m.BasePath)
	}
	assert.ElementsMatch(t, []string{"orders", "orders/v1/items"}, paths, "apigateway lists the API mapping")

	runCLI(t, awsCLI("apigateway", "delete-base-path-mapping", "--domain-name", name, "--base-path", "orders"))
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigatewayv2", "get-api-mappings", "--domain-name", name))), &v2))
	require.Len(t, v2.Items, 1)
	assert.Equal(t, "orders/v1/items", v2.Items[0].ApiMappingKey)
}
