package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIGatewayV2CompleteCLI exercises the API/api-mapping update and
// reset-authorizers-cache surface through the aws CLI: update-api,
// update-api-mapping, reset-authorizers-cache.
//
// The developer-portal, portal-product, product-page, and routing-rule
// operations registered in this slice ship in API Gateway v2's newer wire
// surface that the local aws CLI 2.26.6 does not yet expose (no
// create-portal / create-portal-product / create-routing-rule subcommands).
// Those operations are covered by the SDK suite (TestAPIGatewayV2_* in
// sdk-tests), which exercises the same handlers and the testing-contract hook.
func TestAPIGatewayV2CompleteCLI(t *testing.T) {
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

	// update-api (PATCH) renames the API; the response reflects it.
	assert.Contains(t, runCLI(t, awsCLI("apigatewayv2", "update-api",
		"--api-id", api.APIID,
		"--name", "cli-update-api-renamed",
	)), `"Name": "cli-update-api-renamed"`)

	// create-stage then reset-authorizers-cache (DELETE, empty 204 body).
	runCLI(t, awsCLI("apigatewayv2", "create-stage",
		"--api-id", api.APIID,
		"--stage-name", "prod",
	))
	runCLI(t, awsCLI("apigatewayv2", "reset-authorizers-cache",
		"--api-id", api.APIID,
		"--stage-name", "prod",
	))

	// update-api-mapping against a domain name.
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
