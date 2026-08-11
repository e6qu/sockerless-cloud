package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlueBusinessContextAndEntityRecords_CLI drives the complete AWS Glue
// business-context surface and the DynamoDB connection metadata/preview path
// through the official AWS Command Line Interface.
func TestGlueBusinessContextAndEntityRecords_CLI(t *testing.T) {
	runCLI(t, awsCLI("glue", "put-form-type",
		"--name", "CliBusinessMetadata",
		"--schema", "structure CliBusinessMetadata { owner: String }"))
	form := strings.TrimSpace(runCLI(t, awsCLI("glue", "get-form-type",
		"--identifier", "CliBusinessMetadata", "--query", "Id", "--output", "text")))
	assert.Equal(t, "CliBusinessMetadata", form)
	runCLI(t, awsCLI("glue", "list-form-types", "--max-results", "10"))

	runCLI(t, awsCLI("glue", "put-asset-type",
		"--name", "CliBusinessDataSet",
		"--forms", `{"metadata":{"FormTypeIdentifier":"CliBusinessMetadata"}}`))
	assetType := strings.TrimSpace(runCLI(t, awsCLI("glue", "get-asset-type",
		"--identifier", "CliBusinessDataSet", "--query", "Id", "--output", "text")))
	assert.Equal(t, "CliBusinessDataSet", assetType)
	runCLI(t, awsCLI("glue", "list-asset-types", "--max-results", "10"))

	runCLI(t, awsCLI("glue", "put-asset",
		"--asset-type-id", "CliBusinessDataSet",
		"--identifier", "cli-quarterly-sales",
		"--name", "CLI Quarterly Sales",
		"--description", "CLI revenue data",
		"--forms", `{"metadata":{"FormTypeId":"CliBusinessMetadata","Content":"{\"owner\":\"finance\"}"}}`))
	runCLI(t, awsCLI("glue", "put-attachment",
		"--asset-identifier", "cli-quarterly-sales",
		"--attachment-name", "review",
		"--form-type-id", "CliBusinessMetadata",
		"--content", `{"owner":"controller"}`))
	asset := runCLI(t, awsCLI("glue", "get-asset", "--identifier", "cli-quarterly-sales"))
	assert.Contains(t, asset, "controller")
	runCLI(t, awsCLI("glue", "update-asset",
		"--identifier", "cli-quarterly-sales",
		"--name", "CLI Quarterly Net Sales"))
	search := runCLI(t, awsCLI("glue", "search-assets",
		"--search-text", "net sales",
		"--filter-clause", `{"AttributeFilter":{"Attribute":"AssetTypeId","Operator":"equals","Value":{"StringValue":"CliBusinessDataSet"}}}`))
	assert.Contains(t, search, "cli-quarterly-sales")

	glossaryID := strings.TrimSpace(runCLI(t, awsCLI("glue", "create-glossary",
		"--name", "CLI Finance Terms",
		"--description", "CLI controlled vocabulary",
		"--query", "Id", "--output", "text")))
	require.NotEmpty(t, glossaryID)
	runCLI(t, awsCLI("glue", "get-glossary", "--identifier", glossaryID))
	runCLI(t, awsCLI("glue", "list-glossaries", "--max-results", "10"))
	runCLI(t, awsCLI("glue", "update-glossary",
		"--identifier", glossaryID, "--description", "Updated CLI vocabulary"))

	termID := strings.TrimSpace(runCLI(t, awsCLI("glue", "create-glossary-term",
		"--glossary-identifier", glossaryID,
		"--name", "CLI Net Revenue",
		"--short-description", "Revenue after deductions",
		"--query", "Id", "--output", "text")))
	require.NotEmpty(t, termID)
	runCLI(t, awsCLI("glue", "get-glossary-term", "--identifier", termID))
	runCLI(t, awsCLI("glue", "list-glossary-terms", "--glossary-identifier", glossaryID))
	runCLI(t, awsCLI("glue", "update-glossary-term",
		"--identifier", termID, "--short-description", "Updated net revenue"))
	runCLI(t, awsCLI("glue", "associate-glossary-terms",
		"--asset-identifier", "cli-quarterly-sales",
		"--glossary-term-identifiers", termID))
	runCLI(t, awsCLI("glue", "disassociate-glossary-terms",
		"--asset-identifier", "cli-quarterly-sales",
		"--glossary-term-identifiers", termID))

	runCLIExpectError(t, awsCLI("glue", "list-iterable-forms",
		"--asset-identifier", "cli-quarterly-sales", "--iterable-form-name", "columns"))
	runCLIExpectError(t, awsCLI("glue", "batch-get-iterable-forms",
		"--asset-identifier", "cli-quarterly-sales", "--iterable-form-name", "columns",
		"--item-identifiers", "amount"))

	batch := runCLI(t, awsCLI("glue", "batch-get-data-quality-ruleset-evaluation-run",
		"--run-ids", "cli-missing-run"))
	assert.Contains(t, batch, "cli-missing-run")

	runCLI(t, awsCLI("dynamodb", "create-table",
		"--table-name", "glue-cli-business-entities",
		"--attribute-definitions", "AttributeName=id,AttributeType=S",
		"--key-schema", "AttributeName=id,KeyType=HASH",
		"--billing-mode", "PAY_PER_REQUEST"))
	runCLI(t, awsCLI("dynamodb", "put-item",
		"--table-name", "glue-cli-business-entities",
		"--item", `{"id":{"S":"1"},"region":{"S":"emea"},"amount":{"N":"42"}}`))
	runCLI(t, awsCLI("glue", "create-connection",
		"--connection-input", `{"Name":"glue-cli-dynamodb","ConnectionType":"DYNAMODB","ConnectionProperties":{}}`))
	entities := runCLI(t, awsCLI("glue", "list-entities", "--connection-name", "glue-cli-dynamodb"))
	assert.Contains(t, entities, "glue-cli-business-entities")
	fields := runCLI(t, awsCLI("glue", "describe-entity",
		"--connection-name", "glue-cli-dynamodb", "--entity-name", "glue-cli-business-entities"))
	assert.Contains(t, fields, `"FieldName": "id"`)
	records := runCLI(t, awsCLI("glue", "get-entity-records",
		"--connection-name", "glue-cli-dynamodb",
		"--entity-name", "glue-cli-business-entities",
		"--limit", "10",
		"--selected-fields", "id", "amount"))
	assert.Contains(t, records, `"amount": 42`)

	runCLI(t, awsCLI("glue", "delete-connection", "--connection-name", "glue-cli-dynamodb"))
	runCLI(t, awsCLI("dynamodb", "delete-table", "--table-name", "glue-cli-business-entities"))
	runCLI(t, awsCLI("glue", "delete-attachment",
		"--asset-identifier", "cli-quarterly-sales", "--attachment-name", "review"))
	runCLI(t, awsCLI("glue", "delete-glossary-term", "--identifier", termID))
	runCLI(t, awsCLI("glue", "delete-glossary", "--identifier", glossaryID))
	runCLI(t, awsCLI("glue", "delete-asset", "--identifier", "cli-quarterly-sales"))
	runCLI(t, awsCLI("glue", "delete-asset-type", "--identifier", "CliBusinessDataSet"))
	runCLI(t, awsCLI("glue", "delete-form-type", "--identifier", "CliBusinessMetadata"))
}
