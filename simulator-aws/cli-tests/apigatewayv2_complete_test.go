package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIGatewayV2CompleteCLI covers update-api, update-api-mapping and
// reset-authorizers-cache. aws CLI 2.26.6 has no portal, portal-product,
// product-page or routing-rule subcommands; the SDK suite covers those.
func TestAPIGatewayV2CompleteCLI(t *testing.T) {
	t.Parallel()
	apiOut := runCLI(t, awsCLI("apigatewayv2", "create-api",
		"--name", "cli-update-api",
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

	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "update-api",
		"--api-id", api.APIID,
		"--name", "cli-update-api-renamed",
	)), `"Name": "cli-update-api-renamed"`)

	// reset-authorizers-cache answers DELETE with an empty 204.
	runCLI(t, awsCLI("apigatewayv2", "create-stage",
		"--api-id", api.APIID,
		"--stage-name", "prod",
	))
	runCLI(t, awsCLI("apigatewayv2", "reset-authorizers-cache",
		"--api-id", api.APIID,
		"--stage-name", "prod",
	))

	dn := fmt.Sprintf("cli-map-%d.example.com", time.Now().UnixNano())
	runCLI(t, awsCLI("apigatewayv2", "create-domain-name", "--domain-name", dn))
	t.Cleanup(func() {
		runCLI(t, awsCLI("apigatewayv2", "delete-domain-name", "--domain-name", dn))
	})

	mappingOut := runCLI(t, awsCLI("apigatewayv2", "create-api-mapping",
		"--domain-name", dn,
		"--api-id", api.APIID,
		"--stage", "prod",
	))
	var mapping struct {
		APIMappingID string `json:"ApiMappingId"`
	}
	require.NoError(t, json.Unmarshal([]byte(mappingOut), &mapping))
	require.NotEmpty(t, mapping.APIMappingID)

	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "update-api-mapping",
		"--domain-name", dn,
		"--api-mapping-id", mapping.APIMappingID,
		"--api-id", api.APIID,
		"--api-mapping-key", "v1",
		"--stage", "prod",
	)), `"ApiMappingKey": "v1"`)
}
