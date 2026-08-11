package aws_cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDynamoDBCLI_ExactItemSizeBoundary(t *testing.T) {
	const maxItemBytes = 400 * 1024
	table := "cli-ddb-item-size"
	runCLI(t, awsCLI("dynamodb", "create-table",
		"--table-name", table,
		"--attribute-definitions", "AttributeName=pk,AttributeType=S",
		"--key-schema", "AttributeName=pk,KeyType=HASH",
		"--billing-mode", "PAY_PER_REQUEST"))
	t.Cleanup(func() {
		_, _ = awsCLI("dynamodb", "delete-table", "--table-name", table).CombinedOutput()
	})
	itemPath := filepath.Join(t.TempDir(), "item.json")
	writeItem := func(payloadBytes int) {
		t.Helper()
		item := map[string]any{
			"pk":      map[string]string{"S": "ok"},
			"payload": map[string]string{"S": strings.Repeat("x", payloadBytes)},
		}
		encoded, err := json.Marshal(item)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(itemPath, encoded, 0o600))
	}

	writeItem(maxItemBytes - 11)
	runCLI(t, awsCLI("dynamodb", "put-item",
		"--table-name", table, "--item", "file://"+itemPath))

	writeItem(maxItemBytes - 10)
	output, err := awsCLI("dynamodb", "put-item",
		"--table-name", table, "--item", "file://"+itemPath).CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(string(output)), "item size has exceeded the maximum allowed size")
}

func TestDynamoDBCLI_GlobalSecondaryIndexes(t *testing.T) {
	table := "cli-ddb-gsi"
	runCLI(t, awsCLI("dynamodb", "create-table",
		"--table-name", table,
		"--attribute-definitions",
		"AttributeName=pk,AttributeType=S", "AttributeName=gsipk,AttributeType=S",
		"--key-schema", "AttributeName=pk,KeyType=HASH",
		"--billing-mode", "PAY_PER_REQUEST",
		"--global-secondary-indexes",
		"IndexName=GSI1,KeySchema=[{AttributeName=gsipk,KeyType=HASH}],Projection={ProjectionType=ALL}"))
	t.Cleanup(func() {
		_ = awsCLI("dynamodb", "delete-table", "--table-name", table).Run()
	})

	out := runCLI(t, awsCLI("dynamodb", "describe-table", "--table-name", table, "--output", "json"))
	var desc struct {
		Table struct {
			GlobalSecondaryIndexes []struct {
				IndexName   string `json:"IndexName"`
				IndexStatus string `json:"IndexStatus"`
			} `json:"GlobalSecondaryIndexes"`
		} `json:"Table"`
	}
	parseJSON(t, out, &desc)
	require.Len(t, desc.Table.GlobalSecondaryIndexes, 1, "describe-table must report the GSI (was null pre-fix)")
	assert.Equal(t, "GSI1", desc.Table.GlobalSecondaryIndexes[0].IndexName)
	assert.Equal(t, "ACTIVE", desc.Table.GlobalSecondaryIndexes[0].IndexStatus)
}

func TestDynamoDBCLI_TableAndItems(t *testing.T) {
	table := "cli-ddb-table"

	runCLI(t, awsCLI("dynamodb", "create-table",
		"--table-name", table,
		"--attribute-definitions", "AttributeName=pk,AttributeType=S",
		"--key-schema", "AttributeName=pk,KeyType=HASH",
		"--billing-mode", "PAY_PER_REQUEST"))
	t.Cleanup(func() {
		_ = awsCLI("dynamodb", "delete-table", "--table-name", table).Run()
	})

	out := runCLI(t, awsCLI("dynamodb", "describe-table", "--table-name", table))
	var desc struct {
		Table struct {
			TableName          string `json:"TableName"`
			TableStatus        string `json:"TableStatus"`
			TableArn           string `json:"TableArn"`
			BillingModeSummary struct {
				BillingMode string `json:"BillingMode"`
			} `json:"BillingModeSummary"`
			ProvisionedThroughput struct {
				ReadCapacityUnits  int64 `json:"ReadCapacityUnits"`
				WriteCapacityUnits int64 `json:"WriteCapacityUnits"`
			} `json:"ProvisionedThroughput"`
			TableClassSummary struct {
				TableClass string `json:"TableClass"`
			} `json:"TableClassSummary"`
			WarmThroughput struct {
				Status string `json:"Status"`
			} `json:"WarmThroughput"`
		} `json:"Table"`
	}
	parseJSON(t, out, &desc)
	assert.Equal(t, table, desc.Table.TableName)
	assert.Equal(t, "ACTIVE", desc.Table.TableStatus)
	assert.Contains(t, desc.Table.TableArn, "arn:aws:dynamodb:")
	assert.Equal(t, "PAY_PER_REQUEST", desc.Table.BillingModeSummary.BillingMode)
	assert.Equal(t, "STANDARD", desc.Table.TableClassSummary.TableClass)
	assert.Equal(t, "ACTIVE", desc.Table.WarmThroughput.Status)

	runCLI(t, awsCLI("dynamodb", "put-item",
		"--table-name", table,
		"--item", `{"pk":{"S":"a"},"kind":{"S":"wanted"}}`))
	runCLI(t, awsCLI("dynamodb", "put-item",
		"--table-name", table,
		"--item", `{"pk":{"S":"b"},"kind":{"S":"ignored"}}`))

	out = runCLI(t, awsCLI("dynamodb", "get-item",
		"--table-name", table,
		"--key", `{"pk":{"S":"a"}}`))
	var got struct {
		Item map[string]map[string]string `json:"Item"`
	}
	parseJSON(t, out, &got)
	require.Equal(t, "wanted", got.Item["kind"]["S"])

	out = runCLI(t, awsCLI("dynamodb", "query",
		"--table-name", table,
		"--key-condition-expression", "#pk = :pk",
		"--expression-attribute-names", `{"#pk":"pk"}`,
		"--expression-attribute-values", `{":pk":{"S":"a"}}`))
	var query struct {
		Items []map[string]map[string]string `json:"Items"`
		Count int                            `json:"Count"`
	}
	parseJSON(t, out, &query)
	require.Equal(t, 1, query.Count)
	require.Equal(t, "a", query.Items[0]["pk"]["S"])
}
