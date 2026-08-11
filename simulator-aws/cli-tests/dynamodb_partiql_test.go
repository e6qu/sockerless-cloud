package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ddbCLICreateTable(t *testing.T, table string) {
	t.Helper()
	runCLI(t, awsCLI("dynamodb", "create-table",
		"--table-name", table,
		"--attribute-definitions", "AttributeName=pk,AttributeType=S",
		"--key-schema", "AttributeName=pk,KeyType=HASH",
		"--billing-mode", "PAY_PER_REQUEST"))
	t.Cleanup(func() { _ = awsCLI("dynamodb", "delete-table", "--table-name", table).Run() })
}

// TestDynamoDBCLI_PartiQL drives ExecuteStatement (INSERT/SELECT/UPDATE/DELETE).
func TestDynamoDBCLI_PartiQL(t *testing.T) {
	table := "cli-partiql"
	ddbCLICreateTable(t, table)

	runCLI(t, awsCLI("dynamodb", "execute-statement",
		"--statement", `INSERT INTO "`+table+`" VALUE {'pk': 'a', 'n': 1}`))

	out := runCLI(t, awsCLI("dynamodb", "execute-statement",
		"--statement", `SELECT * FROM "`+table+`" WHERE pk = 'a'`))
	var sel struct {
		Items []map[string]map[string]string `json:"Items"`
	}
	parseJSON(t, out, &sel)
	require.Len(t, sel.Items, 1)
	assert.Equal(t, "1", sel.Items[0]["n"]["N"])

	runCLI(t, awsCLI("dynamodb", "execute-statement",
		"--statement", `UPDATE "`+table+`" SET n = 99 WHERE pk = 'a'`))
	out2 := runCLI(t, awsCLI("dynamodb", "execute-statement",
		"--statement", `SELECT n FROM "`+table+`" WHERE pk = 'a'`))
	parseJSON(t, out2, &sel)
	require.Len(t, sel.Items, 1)
	assert.Equal(t, "99", sel.Items[0]["n"]["N"])

	runCLI(t, awsCLI("dynamodb", "execute-statement",
		"--statement", `DELETE FROM "`+table+`" WHERE pk = 'a'`))
	out3 := runCLI(t, awsCLI("dynamodb", "execute-statement",
		"--statement", `SELECT * FROM "`+table+`" WHERE pk = 'a'`))
	parseJSON(t, out3, &sel)
	assert.Empty(t, sel.Items)
}

// TestDynamoDBCLI_PartiQLBatch drives BatchExecuteStatement.
func TestDynamoDBCLI_PartiQLBatch(t *testing.T) {
	table := "cli-partiql-batch"
	ddbCLICreateTable(t, table)
	runCLI(t, awsCLI("dynamodb", "execute-statement",
		"--statement", `INSERT INTO "`+table+`" VALUE {'pk': 'x'}`))

	out := runCLI(t, awsCLI("dynamodb", "batch-execute-statement",
		"--statements",
		`[{"Statement": "SELECT * FROM \"`+table+`\" WHERE pk = 'x'"}, {"Statement": "INSERT INTO \"`+table+`\" VALUE {'pk': 'x'}"}]`))
	var batch struct {
		Responses []struct {
			Item  map[string]map[string]string `json:"Item"`
			Error *struct {
				Code string `json:"Code"`
			} `json:"Error"`
		} `json:"Responses"`
	}
	parseJSON(t, out, &batch)
	require.Len(t, batch.Responses, 2)
	assert.NotNil(t, batch.Responses[0].Item, "first statement read the item")
	require.NotNil(t, batch.Responses[1].Error, "duplicate insert reports a per-item Error")
	assert.Equal(t, "DuplicateItem", batch.Responses[1].Error.Code)
}

// TestDynamoDBCLI_PartiQLTransaction drives ExecuteTransaction.
func TestDynamoDBCLI_PartiQLTransaction(t *testing.T) {
	table := "cli-partiql-tx"
	ddbCLICreateTable(t, table)

	runCLI(t, awsCLI("dynamodb", "execute-transaction",
		"--transact-statements",
		`[{"Statement": "INSERT INTO \"`+table+`\" VALUE {'pk': 't1'}"}, {"Statement": "INSERT INTO \"`+table+`\" VALUE {'pk': 't2'}"}]`))

	out := runCLI(t, awsCLI("dynamodb", "execute-statement",
		"--statement", `SELECT * FROM "`+table+`" WHERE pk = 't1'`))
	var sel struct {
		Items []map[string]any `json:"Items"`
	}
	parseJSON(t, out, &sel)
	assert.Len(t, sel.Items, 1, "transaction committed t1")
}

// TestDynamoDBCLI_DescribeLimits drives DescribeLimits.
func TestDynamoDBCLI_DescribeLimits(t *testing.T) {
	out := runCLI(t, awsCLI("dynamodb", "describe-limits"))
	var lim struct {
		AccountMaxReadCapacityUnits  int64 `json:"AccountMaxReadCapacityUnits"`
		AccountMaxWriteCapacityUnits int64 `json:"AccountMaxWriteCapacityUnits"`
	}
	parseJSON(t, out, &lim)
	assert.Greater(t, lim.AccountMaxReadCapacityUnits, int64(0))
	assert.Greater(t, lim.AccountMaxWriteCapacityUnits, int64(0))
}
