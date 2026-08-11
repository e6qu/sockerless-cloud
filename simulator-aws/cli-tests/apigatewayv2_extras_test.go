package aws_cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIGatewayV2IntegrationRouteResponsesCLI exercises the integration- and
// route-response child surfaces through the aws CLI:
// create/get/get-list/update/delete-integration-response and the route-response
// counterparts.
func TestAPIGatewayV2IntegrationRouteResponsesCLI(t *testing.T) {
	apiOut := runCLI(t, awsCLI("apigatewayv2", "create-api",
		"--name", "cli-ws-api",
		"--protocol-type", "WEBSOCKET",
		"--route-selection-expression", "$request.body.action",
	))
	var api struct {
		APIID string `json:"ApiId"`
	}
	require.NoError(t, json.Unmarshal([]byte(apiOut), &api))
	require.NotEmpty(t, api.APIID)
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigatewayv2", "delete-api", "--api-id", api.APIID)) })

	integOut := runCLI(t, awsCLI("apigatewayv2", "create-integration",
		"--api-id", api.APIID,
		"--integration-type", "MOCK",
	))
	var integ struct {
		IntegrationID string `json:"IntegrationId"`
	}
	require.NoError(t, json.Unmarshal([]byte(integOut), &integ))
	require.NotEmpty(t, integ.IntegrationID)

	irOut := runCLI(t, awsCLI("apigatewayv2", "create-integration-response",
		"--api-id", api.APIID,
		"--integration-id", integ.IntegrationID,
		"--integration-response-key", "$default",
	))
	var ir struct {
		IntegrationResponseID string `json:"IntegrationResponseId"`
	}
	require.NoError(t, json.Unmarshal([]byte(irOut), &ir))
	require.NotEmpty(t, ir.IntegrationResponseID)

	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-integration-response",
		"--api-id", api.APIID, "--integration-id", integ.IntegrationID,
		"--integration-response-id", ir.IntegrationResponseID,
	)), ir.IntegrationResponseID)

	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-integration-responses",
		"--api-id", api.APIID, "--integration-id", integ.IntegrationID,
	)), ir.IntegrationResponseID)

	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "update-integration-response",
		"--api-id", api.APIID, "--integration-id", integ.IntegrationID,
		"--integration-response-id", ir.IntegrationResponseID,
		"--template-selection-expression", "200",
	)), `"TemplateSelectionExpression": "200"`)

	runCLI(t, awsCLI("apigatewayv2", "delete-integration-response",
		"--api-id", api.APIID, "--integration-id", integ.IntegrationID,
		"--integration-response-id", ir.IntegrationResponseID,
	))

	// Route responses.
	routeOut := runCLI(t, awsCLI("apigatewayv2", "create-route",
		"--api-id", api.APIID, "--route-key", "$default",
	))
	var route struct {
		RouteID string `json:"RouteId"`
	}
	require.NoError(t, json.Unmarshal([]byte(routeOut), &route))
	require.NotEmpty(t, route.RouteID)

	rrOut := runCLI(t, awsCLI("apigatewayv2", "create-route-response",
		"--api-id", api.APIID, "--route-id", route.RouteID,
		"--route-response-key", "$default",
	))
	var rr struct {
		RouteResponseID string `json:"RouteResponseId"`
	}
	require.NoError(t, json.Unmarshal([]byte(rrOut), &rr))
	require.NotEmpty(t, rr.RouteResponseID)

	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-route-response",
		"--api-id", api.APIID, "--route-id", route.RouteID,
		"--route-response-id", rr.RouteResponseID,
	)), rr.RouteResponseID)

	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-route-responses",
		"--api-id", api.APIID, "--route-id", route.RouteID,
	)), rr.RouteResponseID)

	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "update-route-response",
		"--api-id", api.APIID, "--route-id", route.RouteID,
		"--route-response-id", rr.RouteResponseID,
		"--model-selection-expression", "$default",
	)), `"ModelSelectionExpression": "$default"`)

	runCLI(t, awsCLI("apigatewayv2", "delete-route-response",
		"--api-id", api.APIID, "--route-id", route.RouteID,
		"--route-response-id", rr.RouteResponseID,
	))
}

// TestAPIGatewayV2ImportExportTagsTemplateCLI exercises import-api, reimport-api,
// export-api, get-model-template, and get-tags through the aws CLI.
func TestAPIGatewayV2ImportExportTagsTemplateCLI(t *testing.T) {
	openapi := `{"openapi":"3.0.1","info":{"title":"cli-imported-api","version":"1.0"},"paths":{"/pets":{"get":{"responses":{"200":{"description":"ok"}}}}}}`
	bodyFile := filepath.Join(t.TempDir(), "openapi.json")
	require.NoError(t, os.WriteFile(bodyFile, []byte(openapi), 0o600))

	impOut := runCLI(t, awsCLI("apigatewayv2", "import-api", "--body", "file://"+bodyFile))
	var imp struct {
		APIID string `json:"ApiId"`
		Name  string `json:"Name"`
	}
	require.NoError(t, json.Unmarshal([]byte(impOut), &imp))
	require.NotEmpty(t, imp.APIID)
	assert.Equal(t, "cli-imported-api", imp.Name)
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigatewayv2", "delete-api", "--api-id", imp.APIID)) })

	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "reimport-api",
		"--api-id", imp.APIID, "--body", "file://"+bodyFile,
	)), imp.APIID)

	exportFile := filepath.Join(t.TempDir(), "export.json")
	runCLI(t, awsCLI("apigatewayv2", "export-api",
		"--api-id", imp.APIID,
		"--specification", "OAS30",
		"--output-type", "JSON",
		exportFile,
	))
	exported, err := os.ReadFile(exportFile)
	require.NoError(t, err)
	assert.Contains(t, string(exported), "openapi")

	modelOut := runCLI(t, awsCLI("apigatewayv2", "create-model",
		"--api-id", imp.APIID,
		"--name", "Pet",
		"--content-type", "application/json",
		"--schema", `{"type":"object"}`,
	))
	var model struct {
		ModelID string `json:"ModelId"`
	}
	require.NoError(t, json.Unmarshal([]byte(modelOut), &model))
	require.NotEmpty(t, model.ModelID)
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-model-template",
		"--api-id", imp.APIID, "--model-id", model.ModelID,
	)), "object")

	// get-tags reads tags off a tagged API ARN.
	taggedOut := runCLI(t, awsCLI("apigatewayv2", "create-api",
		"--name", "cli-tagged-api",
		"--protocol-type", "HTTP",
		"--tags", "env=cli",
	))
	var tagged struct {
		APIID string `json:"ApiId"`
	}
	require.NoError(t, json.Unmarshal([]byte(taggedOut), &tagged))
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigatewayv2", "delete-api", "--api-id", tagged.APIID)) })

	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "get-tags",
		"--resource-arn", "arn:aws:apigateway:us-east-1::/apis/"+tagged.APIID,
	)), `"env": "cli"`)
}

// TestAPIGatewayV2ConfigDeletesCLI exercises the per-stage / per-api config
// deletes: delete-cors-configuration, delete-access-log-settings,
// delete-route-request-parameter, delete-route-settings.
func TestAPIGatewayV2ConfigDeletesCLI(t *testing.T) {
	apiOut := runCLI(t, awsCLI("apigatewayv2", "create-api",
		"--name", "cli-cfg-api",
		"--protocol-type", "HTTP",
	))
	var api struct {
		APIID string `json:"ApiId"`
	}
	require.NoError(t, json.Unmarshal([]byte(apiOut), &api))
	require.NotEmpty(t, api.APIID)
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigatewayv2", "delete-api", "--api-id", api.APIID)) })

	routeOut := runCLI(t, awsCLI("apigatewayv2", "create-route",
		"--api-id", api.APIID, "--route-key", "GET /pets",
	))
	var route struct {
		RouteID string `json:"RouteId"`
	}
	require.NoError(t, json.Unmarshal([]byte(routeOut), &route))

	runCLI(t, awsCLI("apigatewayv2", "create-stage",
		"--api-id", api.APIID, "--stage-name", "prod",
	))

	runCLI(t, awsCLI("apigatewayv2", "delete-cors-configuration", "--api-id", api.APIID))
	runCLI(t, awsCLI("apigatewayv2", "delete-access-log-settings",
		"--api-id", api.APIID, "--stage-name", "prod"))
	runCLI(t, awsCLI("apigatewayv2", "delete-route-request-parameter",
		"--api-id", api.APIID, "--route-id", route.RouteID,
		"--request-parameter-key", "route.request.querystring.id"))
	runCLI(t, awsCLI("apigatewayv2", "delete-route-settings",
		"--api-id", api.APIID, "--stage-name", "prod",
		"--route-key", "GET /pets"))
}
