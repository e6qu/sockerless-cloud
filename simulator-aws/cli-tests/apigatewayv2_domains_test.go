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
