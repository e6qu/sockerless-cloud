package aws_cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// apigwCLINewAPIWithStage creates a REST API + GET method on root + a
// deployed "prod" stage, returning the api id and stage name.
func apigwCLINewAPIWithStage(t *testing.T, name string) (string, string) {
	t.Helper()
	out := runCLI(t, awsCLI("apigateway", "create-rest-api", "--name", name))
	var created struct {
		ID             string `json:"id"`
		RootResourceID string `json:"rootResourceId"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	require.NotEmpty(t, created.ID)
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("apigateway", "delete-rest-api", "--rest-api-id", created.ID))
	})
	runCLI(t, awsCLI("apigateway", "put-method",
		"--rest-api-id", created.ID,
		"--resource-id", created.RootResourceID,
		"--http-method", "GET",
		"--authorization-type", "NONE",
	))
	depOut := runCLI(t, awsCLI("apigateway", "create-deployment", "--rest-api-id", created.ID))
	var dep struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(depOut), &dep))
	runCLI(t, awsCLI("apigateway", "create-stage",
		"--rest-api-id", created.ID,
		"--stage-name", "prod",
		"--deployment-id", dep.ID,
	))
	return created.ID, "prod"
}

// TestAPIGatewayCLI_BasePathMappings drives the base-path-mapping CRUD cycle
// via the aws CLI.
func TestAPIGatewayCLI_BasePathMappings(t *testing.T) {
	apiID, stage := apigwCLINewAPIWithStage(t, "cli-bpm-api")
	domain := "cli-bpm.example.com"

	createOut := runCLI(t, awsCLI("apigateway", "create-base-path-mapping",
		"--domain-name", domain,
		"--rest-api-id", apiID,
		"--base-path", "v1",
		"--stage", stage,
	))
	var bpm struct {
		BasePath  string `json:"basePath"`
		RestApiID string `json:"restApiId"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &bpm))
	assert.Equal(t, "v1", bpm.BasePath)

	listOut := runCLI(t, awsCLI("apigateway", "get-base-path-mappings", "--domain-name", domain))
	assert.Contains(t, listOut, "v1")

	getOut := runCLI(t, awsCLI("apigateway", "get-base-path-mapping", "--domain-name", domain, "--base-path", "v1"))
	assert.Contains(t, getOut, apiID)

	runCLI(t, awsCLI("apigateway", "update-base-path-mapping",
		"--domain-name", domain, "--base-path", "v1",
		"--patch-operations", "op=replace,path=/stage,value=prod",
	))

	runCLI(t, awsCLI("apigateway", "delete-base-path-mapping", "--domain-name", domain, "--base-path", "v1"))
}

// TestAPIGatewayCLI_ClientCertificates drives the client-certificate cycle.
func TestAPIGatewayCLI_ClientCertificates(t *testing.T) {
	genOut := runCLI(t, awsCLI("apigateway", "generate-client-certificate", "--description", "cli cert"))
	var cert struct {
		ID  string `json:"clientCertificateId"`
		PEM string `json:"pemEncodedCertificate"`
	}
	require.NoError(t, json.Unmarshal([]byte(genOut), &cert))
	require.NotEmpty(t, cert.ID)
	assert.Contains(t, cert.PEM, "BEGIN CERTIFICATE")
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("apigateway", "delete-client-certificate", "--client-certificate-id", cert.ID))
	})

	listOut := runCLI(t, awsCLI("apigateway", "get-client-certificates"))
	assert.Contains(t, listOut, cert.ID)

	getOut := runCLI(t, awsCLI("apigateway", "get-client-certificate", "--client-certificate-id", cert.ID))
	assert.Contains(t, getOut, "cli cert")

	updOut := runCLI(t, awsCLI("apigateway", "update-client-certificate",
		"--client-certificate-id", cert.ID,
		"--patch-operations", "op=replace,path=/description,value=updated",
	))
	assert.Contains(t, updOut, "updated")

	runCLI(t, awsCLI("apigateway", "delete-client-certificate", "--client-certificate-id", cert.ID))
}

// TestAPIGatewayCLI_Documentation drives documentation parts + versions.
func TestAPIGatewayCLI_Documentation(t *testing.T) {
	apiID, _ := apigwCLINewAPIWithStage(t, "cli-doc-api")

	partOut := runCLI(t, awsCLI("apigateway", "create-documentation-part",
		"--rest-api-id", apiID,
		"--location", "type=API",
		"--properties", `{"description":"My API"}`,
	))
	var part struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(partOut), &part))
	require.NotEmpty(t, part.ID)

	runCLI(t, awsCLI("apigateway", "get-documentation-parts", "--rest-api-id", apiID))
	runCLI(t, awsCLI("apigateway", "get-documentation-part", "--rest-api-id", apiID, "--documentation-part-id", part.ID))
	runCLI(t, awsCLI("apigateway", "update-documentation-part",
		"--rest-api-id", apiID, "--documentation-part-id", part.ID,
		"--patch-operations", "op=replace,path=/properties,value=updated-properties",
	))
	// --body is a Blob: feed the OpenAPI document from a file so the CLI
	// passes it verbatim rather than base64-decoding the literal argument.
	bodyFile := filepath.Join(t.TempDir(), "doc.json")
	require.NoError(t, os.WriteFile(bodyFile, []byte(`{"openapi":"3.0.1","info":{"title":"x"}}`), 0o644))
	runCLI(t, awsCLI("apigateway", "import-documentation-parts",
		"--rest-api-id", apiID,
		"--body", "fileb://"+bodyFile,
	))

	verOut := runCLI(t, awsCLI("apigateway", "create-documentation-version",
		"--rest-api-id", apiID,
		"--documentation-version", "1.0.0",
		"--description", "first cut",
	))
	assert.Contains(t, verOut, "1.0.0")
	runCLI(t, awsCLI("apigateway", "get-documentation-versions", "--rest-api-id", apiID))
	runCLI(t, awsCLI("apigateway", "get-documentation-version", "--rest-api-id", apiID, "--documentation-version", "1.0.0"))
	runCLI(t, awsCLI("apigateway", "update-documentation-version",
		"--rest-api-id", apiID, "--documentation-version", "1.0.0",
		"--patch-operations", "op=replace,path=/description,value=released",
	))
	runCLI(t, awsCLI("apigateway", "delete-documentation-version", "--rest-api-id", apiID, "--documentation-version", "1.0.0"))
	runCLI(t, awsCLI("apigateway", "delete-documentation-part", "--rest-api-id", apiID, "--documentation-part-id", part.ID))
}

// TestAPIGatewayCLI_GatewayResponses drives the gateway-response slice.
func TestAPIGatewayCLI_GatewayResponses(t *testing.T) {
	apiID, _ := apigwCLINewAPIWithStage(t, "cli-gr-api")

	listOut := runCLI(t, awsCLI("apigateway", "get-gateway-responses", "--rest-api-id", apiID))
	assert.Contains(t, listOut, "DEFAULT_4XX")

	putOut := runCLI(t, awsCLI("apigateway", "put-gateway-response",
		"--rest-api-id", apiID,
		"--response-type", "DEFAULT_4XX",
		"--status-code", "444",
	))
	assert.Contains(t, putOut, "444")

	runCLI(t, awsCLI("apigateway", "get-gateway-response", "--rest-api-id", apiID, "--response-type", "DEFAULT_4XX"))
	runCLI(t, awsCLI("apigateway", "update-gateway-response",
		"--rest-api-id", apiID, "--response-type", "DEFAULT_4XX",
		"--patch-operations", "op=replace,path=/statusCode,value=455",
	))
	runCLI(t, awsCLI("apigateway", "delete-gateway-response", "--rest-api-id", apiID, "--response-type", "DEFAULT_4XX"))
}

// TestAPIGatewayCLI_AccountExportSdkUsageTags drives account settings, OpenAPI
// export, SDK generation, the SDK-type catalog, request validator + model
// template, usage, and tag reads.
func TestAPIGatewayCLI_AccountExportSdkUsageTags(t *testing.T) {
	apiID, stage := apigwCLINewAPIWithStage(t, "cli-export-api")

	acctOut := runCLI(t, awsCLI("apigateway", "get-account"))
	assert.Contains(t, acctOut, "UsagePlans")
	runCLI(t, awsCLI("apigateway", "update-account",
		"--patch-operations", "op=replace,path=/cloudwatchRoleArn,value=arn:aws:iam::123456789012:role/cw",
	))

	sdkTypesOut := runCLI(t, awsCLI("apigateway", "get-sdk-types"))
	assert.Contains(t, sdkTypesOut, "java")
	runCLI(t, awsCLI("apigateway", "get-sdk-type", "--id", "java"))

	// Request validator + model template.
	rvOut := runCLI(t, awsCLI("apigateway", "create-request-validator",
		"--rest-api-id", apiID, "--name", "validate-body", "--validate-request-body",
	))
	var rv struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(rvOut), &rv))
	runCLI(t, awsCLI("apigateway", "get-request-validator", "--rest-api-id", apiID, "--request-validator-id", rv.ID))

	runCLI(t, awsCLI("apigateway", "create-model",
		"--rest-api-id", apiID, "--name", "Pet",
		"--content-type", "application/json", "--schema", `{"type":"object"}`,
	))
	runCLI(t, awsCLI("apigateway", "get-model-template", "--rest-api-id", apiID, "--model-name", "Pet"))

	// Export + SDK (CLI writes the binary blob to an output file argument).
	exportFile := t.TempDir() + "/export.json"
	runCLI(t, awsCLI("apigateway", "get-export",
		"--rest-api-id", apiID, "--stage-name", stage, "--export-type", "oas30", exportFile,
	))
	sdkFile := t.TempDir() + "/sdk.zip"
	runCLI(t, awsCLI("apigateway", "get-sdk",
		"--rest-api-id", apiID, "--stage-name", stage, "--sdk-type", "java", sdkFile,
	))

	// Usage plan + GetUsage.
	upOut := runCLI(t, awsCLI("apigateway", "create-usage-plan",
		"--name", "cli-export-plan",
		"--api-stages", "apiId="+apiID+",stage="+stage,
		"--quota", "limit=1000,period=MONTH",
	))
	var up struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(upOut), &up))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("apigateway", "delete-usage-plan", "--usage-plan-id", up.ID))
	})
	runCLI(t, awsCLI("apigateway", "get-usage",
		"--usage-plan-id", up.ID, "--start-date", "2024-01-01", "--end-date", "2024-01-02",
	))

	// GetTags off the REST API ARN.
	arn := "arn:aws:apigateway:us-east-1::/restapis/" + apiID
	runCLI(t, awsCLI("apigateway", "get-tags", "--resource-arn", arn))
}
