package aws_cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIGatewayCompleteCLIUpdateLifecycle drives the REST-API resource tree
// through its Update* operations plus the stage-cache flush and test-invoke ops
// via the aws CLI: update-rest-api, update-resource, update-method,
// update-method-response, update-integration, update-model,
// update-request-validator, update-deployment, update-stage,
// flush-stage-cache, flush-stage-authorizers-cache, test-invoke-method,
// test-invoke-authorizer.
func TestAPIGatewayCompleteCLIUpdateLifecycle(t *testing.T) {
	var api struct {
		ID             string `json:"id"`
		RootResourceID string `json:"rootResourceId"`
		Description    string `json:"description"`
	}
	out := runCLI(t, awsCLI("apigateway", "create-rest-api", "--name", "cli-complete"))
	require.NoError(t, json.Unmarshal([]byte(out), &api))
	require.NotEmpty(t, api.ID)
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigateway", "delete-rest-api", "--rest-api-id", api.ID)) })

	out = runCLI(t, awsCLI("apigateway", "update-rest-api", "--rest-api-id", api.ID,
		"--patch-operations", "op=replace,path=/description,value=renamed"))
	require.NoError(t, json.Unmarshal([]byte(out), &api))
	assert.Equal(t, "renamed", api.Description)

	var res struct {
		ID       string `json:"id"`
		PathPart string `json:"pathPart"`
	}
	out = runCLI(t, awsCLI("apigateway", "create-resource", "--rest-api-id", api.ID,
		"--parent-id", api.RootResourceID, "--path-part", "widgets"))
	require.NoError(t, json.Unmarshal([]byte(out), &res))

	out = runCLI(t, awsCLI("apigateway", "update-resource", "--rest-api-id", api.ID,
		"--resource-id", res.ID, "--patch-operations", "op=replace,path=/pathPart,value=gadgets"))
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.Equal(t, "gadgets", res.PathPart)

	runCLI(t, awsCLI("apigateway", "put-method", "--rest-api-id", api.ID, "--resource-id", res.ID,
		"--http-method", "GET", "--authorization-type", "NONE"))
	var method struct {
		ApiKeyRequired bool `json:"apiKeyRequired"`
	}
	out = runCLI(t, awsCLI("apigateway", "update-method", "--rest-api-id", api.ID, "--resource-id", res.ID,
		"--http-method", "GET", "--patch-operations", "op=replace,path=/apiKeyRequired,value=true"))
	require.NoError(t, json.Unmarshal([]byte(out), &method))
	assert.True(t, method.ApiKeyRequired)

	runCLI(t, awsCLI("apigateway", "put-method-response", "--rest-api-id", api.ID, "--resource-id", res.ID,
		"--http-method", "GET", "--status-code", "200"))
	runCLI(t, awsCLI("apigateway", "update-method-response", "--rest-api-id", api.ID, "--resource-id", res.ID,
		"--http-method", "GET", "--status-code", "200",
		"--patch-operations", "op=replace,path=/responseModels/application~1json,value=Empty"))

	runCLI(t, awsCLI("apigateway", "put-integration", "--rest-api-id", api.ID, "--resource-id", res.ID,
		"--http-method", "GET", "--type", "MOCK"))
	var integration struct {
		TimeoutInMillis int `json:"timeoutInMillis"`
	}
	out = runCLI(t, awsCLI("apigateway", "update-integration", "--rest-api-id", api.ID, "--resource-id", res.ID,
		"--http-method", "GET", "--patch-operations", "op=replace,path=/timeoutInMillis,value=12000"))
	require.NoError(t, json.Unmarshal([]byte(out), &integration))
	assert.Equal(t, 12000, integration.TimeoutInMillis)

	var model struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	out = runCLI(t, awsCLI("apigateway", "create-model", "--rest-api-id", api.ID, "--name", "Widget",
		"--content-type", "application/json", "--schema", `{"type":"object"}`))
	require.NoError(t, json.Unmarshal([]byte(out), &model))
	out = runCLI(t, awsCLI("apigateway", "update-model", "--rest-api-id", api.ID, "--model-name", model.Name,
		"--patch-operations", "op=replace,path=/description,value=widget-model"))
	require.NoError(t, json.Unmarshal([]byte(out), &model))
	assert.Equal(t, "widget-model", model.Description)

	var rv struct {
		ID                  string `json:"id"`
		ValidateRequestBody bool   `json:"validateRequestBody"`
	}
	out = runCLI(t, awsCLI("apigateway", "create-request-validator", "--rest-api-id", api.ID, "--name", "v1"))
	require.NoError(t, json.Unmarshal([]byte(out), &rv))
	out = runCLI(t, awsCLI("apigateway", "update-request-validator", "--rest-api-id", api.ID,
		"--request-validator-id", rv.ID, "--patch-operations", "op=replace,path=/validateRequestBody,value=true"))
	require.NoError(t, json.Unmarshal([]byte(out), &rv))
	assert.True(t, rv.ValidateRequestBody)

	var dep struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	}
	out = runCLI(t, awsCLI("apigateway", "create-deployment", "--rest-api-id", api.ID))
	require.NoError(t, json.Unmarshal([]byte(out), &dep))
	out = runCLI(t, awsCLI("apigateway", "update-deployment", "--rest-api-id", api.ID,
		"--deployment-id", dep.ID, "--patch-operations", "op=replace,path=/description,value=prod-deploy"))
	require.NoError(t, json.Unmarshal([]byte(out), &dep))
	assert.Equal(t, "prod-deploy", dep.Description)

	runCLI(t, awsCLI("apigateway", "create-stage", "--rest-api-id", api.ID,
		"--stage-name", "prod", "--deployment-id", dep.ID))
	var dep2 struct {
		ID string `json:"id"`
	}
	out = runCLI(t, awsCLI("apigateway", "create-deployment", "--rest-api-id", api.ID))
	require.NoError(t, json.Unmarshal([]byte(out), &dep2))
	var stage struct {
		DeploymentID string `json:"deploymentId"`
	}
	out = runCLI(t, awsCLI("apigateway", "update-stage", "--rest-api-id", api.ID, "--stage-name", "prod",
		"--patch-operations", "op=replace,path=/deploymentId,value="+dep2.ID))
	require.NoError(t, json.Unmarshal([]byte(out), &stage))
	assert.Equal(t, dep2.ID, stage.DeploymentID)

	runCLI(t, awsCLI("apigateway", "flush-stage-cache", "--rest-api-id", api.ID, "--stage-name", "prod"))
	runCLI(t, awsCLI("apigateway", "flush-stage-authorizers-cache", "--rest-api-id", api.ID, "--stage-name", "prod"))

	var invoke struct {
		Status int `json:"status"`
	}
	out = runCLI(t, awsCLI("apigateway", "test-invoke-method", "--rest-api-id", api.ID, "--resource-id", res.ID,
		"--http-method", "GET", "--path-with-query-string", "/gadgets"))
	require.NoError(t, json.Unmarshal([]byte(out), &invoke))
	assert.Equal(t, 200, invoke.Status)

	var authz struct {
		ID string `json:"id"`
	}
	out = runCLI(t, awsCLI("apigateway", "create-authorizer", "--rest-api-id", api.ID, "--name", "authz",
		"--type", "TOKEN", "--identity-source", "method.request.header.Authorization",
		"--authorizer-uri", "arn:aws:apigateway:us-east-1:lambda:path/x"))
	require.NoError(t, json.Unmarshal([]byte(out), &authz))
	var authResult struct {
		ClientStatus int    `json:"clientStatus"`
		PrincipalID  string `json:"principalId"`
	}
	out = runCLI(t, awsCLI("apigateway", "test-invoke-authorizer", "--rest-api-id", api.ID,
		"--authorizer-id", authz.ID, "--headers", "Authorization=tok"))
	require.NoError(t, json.Unmarshal([]byte(out), &authResult))
	assert.Equal(t, 200, authResult.ClientStatus)
	assert.Equal(t, "test-principal", authResult.PrincipalID)
}

// TestAPIGatewayCompleteCLIImportAndTag covers import-rest-api, put-rest-api,
// import-api-keys, tag-resource, and untag-resource via the aws CLI.
func TestAPIGatewayCompleteCLIImportAndTag(t *testing.T) {
	dir := t.TempDir()
	openapi := filepath.Join(dir, "api.json")
	require.NoError(t, os.WriteFile(openapi,
		[]byte(`{"openapi":"3.0.1","info":{"title":"cli-imported","version":"1.0"},"paths":{"/ping":{"get":{"responses":{"200":{"description":"ok"}}}}}}`), 0o644))

	var imp struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := runCLI(t, awsCLI("apigateway", "import-rest-api", "--body", "fileb://"+openapi))
	require.NoError(t, json.Unmarshal([]byte(out), &imp))
	require.NotEmpty(t, imp.ID)
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigateway", "delete-rest-api", "--rest-api-id", imp.ID)) })
	assert.Equal(t, "cli-imported", imp.Name)

	openapi2 := filepath.Join(dir, "api2.json")
	require.NoError(t, os.WriteFile(openapi2,
		[]byte(`{"openapi":"3.0.1","info":{"title":"cli-imported","version":"2.0"},"paths":{"/pong":{"post":{"responses":{"200":{"description":"ok"}}}}}}`), 0o644))
	runCLI(t, awsCLI("apigateway", "put-rest-api", "--rest-api-id", imp.ID,
		"--mode", "overwrite", "--body", "fileb://"+openapi2))

	var resources struct {
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	out = runCLI(t, awsCLI("apigateway", "get-resources", "--rest-api-id", imp.ID))
	require.NoError(t, json.Unmarshal([]byte(out), &resources))
	var hasPong bool
	for _, r := range resources.Items {
		if r.Path == "/pong" {
			hasPong = true
		}
	}
	assert.True(t, hasPong, "overwrite should add /pong")

	csv := filepath.Join(dir, "keys.csv")
	require.NoError(t, os.WriteFile(csv,
		[]byte("name,key\ncli-key-one,abcdefghij1234567890\ncli-key-two,zyxwvutsrq0987654321\n"), 0o644))
	var keys struct {
		IDs []string `json:"ids"`
	}
	out = runCLI(t, awsCLI("apigateway", "import-api-keys", "--body", "fileb://"+csv, "--format", "csv"))
	require.NoError(t, json.Unmarshal([]byte(out), &keys))
	require.Len(t, keys.IDs, 2)
	for _, id := range keys.IDs {
		id := id
		t.Cleanup(func() { runCLIIgnore(awsCLI("apigateway", "delete-api-key", "--api-key", id)) })
	}

	arn := "arn:aws:apigateway:us-east-1::/restapis/" + imp.ID
	runCLI(t, awsCLI("apigateway", "tag-resource", "--resource-arn", arn, "--tags", "team=platform,env=test"))
	var tags struct {
		Tags map[string]string `json:"tags"`
	}
	out = runCLI(t, awsCLI("apigateway", "get-tags", "--resource-arn", arn))
	require.NoError(t, json.Unmarshal([]byte(out), &tags))
	assert.Equal(t, "platform", tags.Tags["team"])

	runCLI(t, awsCLI("apigateway", "untag-resource", "--resource-arn", arn, "--tag-keys", "env"))
	out = runCLI(t, awsCLI("apigateway", "get-tags", "--resource-arn", arn))
	tags.Tags = nil
	require.NoError(t, json.Unmarshal([]byte(out), &tags))
	_, has := tags.Tags["env"]
	assert.False(t, has, "untag should drop env")
}

// TestAPIGatewayCompleteCLIDomainVpcUsageAndAccess covers update-domain-name,
// update-vpc-link, update-usage, and the domain-name access-association ops
// (create-domain-name-access-association, get-domain-name-access-associations,
// reject-domain-name-access-association, delete-domain-name-access-association).
func TestAPIGatewayCompleteCLIDomainVpcUsageAndAccess(t *testing.T) {
	var domain struct {
		CertificateName string `json:"certificateName"`
	}
	out := runCLI(t, awsCLI("apigateway", "update-domain-name", "--domain-name", "cli.example.com",
		"--patch-operations", "op=replace,path=/certificateName,value=prod-cert"))
	require.NoError(t, json.Unmarshal([]byte(out), &domain))
	assert.Equal(t, "prod-cert", domain.CertificateName)

	var link struct {
		Name string `json:"name"`
	}
	out = runCLI(t, awsCLI("apigateway", "update-vpc-link", "--vpc-link-id", "cli-vpclink-1",
		"--patch-operations", "op=replace,path=/name,value=private-backends"))
	require.NoError(t, json.Unmarshal([]byte(out), &link))
	assert.Equal(t, "private-backends", link.Name)

	var up struct {
		ID string `json:"id"`
	}
	out = runCLI(t, awsCLI("apigateway", "create-usage-plan", "--name", "cli-plan",
		"--quota", "limit=1000,period=MONTH"))
	require.NoError(t, json.Unmarshal([]byte(out), &up))
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigateway", "delete-usage-plan", "--usage-plan-id", up.ID)) })
	var key struct {
		ID string `json:"id"`
	}
	out = runCLI(t, awsCLI("apigateway", "create-api-key", "--name", "cli-uk", "--enabled"))
	require.NoError(t, json.Unmarshal([]byte(out), &key))
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigateway", "delete-api-key", "--api-key", key.ID)) })
	runCLI(t, awsCLI("apigateway", "create-usage-plan-key", "--usage-plan-id", up.ID,
		"--key-id", key.ID, "--key-type", "API_KEY"))
	var usage struct {
		UsagePlanID string `json:"usagePlanId"`
	}
	out = runCLI(t, awsCLI("apigateway", "update-usage", "--usage-plan-id", up.ID, "--key-id", key.ID,
		"--patch-operations", "op=replace,path=/remaining,value=500"))
	require.NoError(t, json.Unmarshal([]byte(out), &usage))
	assert.Equal(t, up.ID, usage.UsagePlanID)

	var assoc struct {
		Arn string `json:"domainNameAccessAssociationArn"`
	}
	out = runCLI(t, awsCLI("apigateway", "create-domain-name-access-association",
		"--domain-name-arn", "arn:aws:apigateway:us-east-1::/domainnames/cli.example.com",
		"--access-association-source", "arn:aws:ec2:us-east-1:111122223333:vpc-endpoint/vpce-123",
		"--access-association-source-type", "VPCE"))
	require.NoError(t, json.Unmarshal([]byte(out), &assoc))
	require.NotEmpty(t, assoc.Arn)

	out = runCLI(t, awsCLI("apigateway", "get-domain-name-access-associations"))
	assert.Contains(t, out, assoc.Arn)

	var assoc2 struct {
		Arn string `json:"domainNameAccessAssociationArn"`
	}
	out = runCLI(t, awsCLI("apigateway", "create-domain-name-access-association",
		"--domain-name-arn", "arn:aws:apigateway:us-east-1::/domainnames/cli2.example.com",
		"--access-association-source", "arn:aws:ec2:us-east-1:111122223333:vpc-endpoint/vpce-456",
		"--access-association-source-type", "VPCE"))
	require.NoError(t, json.Unmarshal([]byte(out), &assoc2))
	runCLI(t, awsCLI("apigateway", "reject-domain-name-access-association",
		"--domain-name-access-association-arn", assoc2.Arn,
		"--domain-name-arn", "arn:aws:apigateway:us-east-1::/domainnames/cli2.example.com"))

	runCLI(t, awsCLI("apigateway", "delete-domain-name-access-association",
		"--domain-name-access-association-arn", assoc.Arn))
}
