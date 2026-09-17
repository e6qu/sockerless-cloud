package main

import (
	"strconv"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ddbSeedFullTableScanTable stores the table the statements below read: a
// partition and a sort key, plus a global secondary index with a partition key
// of its own. dynamodb:FullTableScan is decided against that schema, so the
// table has to be one the simulator holds.
func ddbSeedFullTableScanTable(t *testing.T) {
	t.Helper()
	// Background work from an earlier test must finish before the store it is
	// reading is replaced.
	AwaitSimulatorBackground()
	ddbTables = sim.MakeStore[DDBTable](nil, "ddb_tables")
	ddbTables.Put("Orders", DDBTable{
		TableName:   "Orders",
		TableStatus: "ACTIVE",
		KeySchema: []DDBKeySchemaEntry{
			{AttributeName: "OrderID", KeyType: "HASH"},
			{AttributeName: "Item", KeyType: "RANGE"},
		},
		AttributeDefinitions: []DDBAttributeDef{
			{AttributeName: "OrderID", AttributeType: "N"},
			{AttributeName: "Item", AttributeType: "S"},
			{AttributeName: "CustomerID", AttributeType: "S"},
		},
		GlobalSecondaryIndexes: []DDBGlobalSecondaryIndex{{
			IndexName: "byCustomer",
			KeySchema: []DDBKeySchemaEntry{{AttributeName: "CustomerID", KeyType: "HASH"}},
		}},
	})
}

// ddbStatementConditionContext is the condition context of a PartiQL request
// authorized as action, carrying one statement.
func ddbStatementConditionContext(action, statement string) map[string][]string {
	ctx := map[string][]string{}
	body := `{"Statement": ` + strconv.Quote(statement) + `}`
	iamPopulateDynamoDBConditionKeys(jsonConditionRequest("DynamoDB_20120810.ExecuteStatement"),
		action, []byte(body), ctx)
	return ctx
}

func TestDynamoDBFullTableScanReadsTheSelectStatement(t *testing.T) {
	ddbSeedFullTableScanTable(t)
	// Amazon DynamoDB documents a SELECT as a full table scan unless its WHERE
	// clause restricts the partition key with an equality or an IN condition.
	for _, tc := range []struct {
		statement string
		want      string
	}{
		{`SELECT * FROM "Orders"`, "true"},
		{`SELECT * FROM "Orders" WHERE Address = 'somewhere'`, "true"},
		{`SELECT * FROM "Orders" WHERE OrderID > 1`, "true"},
		{`SELECT * FROM "Orders" WHERE OrderID BETWEEN 1 AND 9`, "true"},
		{`SELECT * FROM "Orders" WHERE OrderID = 100 OR Address = 'somewhere'`, "true"},
		{`SELECT * FROM "Orders"."byCustomer" WHERE Address = 'somewhere'`, "true"},
		{`SELECT * FROM "Orders" WHERE OrderID = 100`, "false"},
		{`SELECT * FROM "Orders" WHERE OrderID = 100 AND Address = 'somewhere'`, "false"},
		{`SELECT * FROM "Orders" WHERE OrderID = 100 OR OrderID = 200`, "false"},
		{`SELECT * FROM "Orders" WHERE OrderID IN (100, 200)`, "false"},
		{`SELECT * FROM "Orders"."byCustomer" WHERE CustomerID = 'C1'`, "false"},
	} {
		t.Run(tc.statement, func(t *testing.T) {
			assertPopulatedConditionValues(t,
				ddbStatementConditionContext("dynamodb:PartiQLSelect", tc.statement),
				map[string][]string{"dynamodb:FullTableScan": {tc.want}})
		})
	}
}

func TestDynamoDBFullTableScanCoversEveryStatementOfABatch(t *testing.T) {
	ddbSeedFullTableScanTable(t)
	ctx := map[string][]string{}
	iamPopulateDynamoDBConditionKeys(jsonConditionRequest("DynamoDB_20120810.BatchExecuteStatement"),
		"dynamodb:PartiQLSelect", []byte(`{"Statements": [
			{"Statement": "SELECT * FROM \"Orders\" WHERE OrderID = 100"},
			{"Statement": "SELECT * FROM \"Orders\" WHERE Address = 'somewhere'"}
		]}`), ctx)
	// The second statement reads every item whatever the first one narrows to.
	assertPopulatedConditionValues(t, ctx, map[string][]string{"dynamodb:FullTableScan": {"true"}})

	ctx = map[string][]string{}
	iamPopulateDynamoDBConditionKeys(jsonConditionRequest("DynamoDB_20120810.BatchExecuteStatement"),
		"dynamodb:PartiQLSelect", []byte(`{"Statements": [
			{"Statement": "SELECT * FROM \"Orders\" WHERE OrderID = 100"},
			{"Statement": "SELECT * FROM \"Orders\" WHERE OrderID = 200 AND Item = 'x'"}
		]}`), ctx)
	assertPopulatedConditionValues(t, ctx, map[string][]string{"dynamodb:FullTableScan": {"false"}})
}

func TestDynamoDBFullTableScanAbsentWhereItDoesNotApply(t *testing.T) {
	ddbSeedFullTableScanTable(t)
	// Amazon DynamoDB declares the key on PartiQLSelect alone.
	ctx := map[string][]string{}
	iamPopulateDynamoDBConditionKeys(jsonConditionRequest("DynamoDB_20120810.Scan"), "dynamodb:Scan",
		[]byte(`{"TableName": "Orders"}`), ctx)
	assertConditionKeysAbsent(t, ctx, "dynamodb:FullTableScan")

	assertConditionKeysAbsent(t, ddbStatementConditionContext("dynamodb:PartiQLInsert",
		`INSERT INTO "Orders" VALUE {'OrderID': 1, 'Item': 'x'}`), "dynamodb:FullTableScan")

	// A statement reading a table the simulator does not hold settles nothing:
	// its partition key, and with it the answer, is unknown.
	assertConditionKeysAbsent(t, ddbStatementConditionContext("dynamodb:PartiQLSelect",
		`SELECT * FROM "Missing"`), "dynamodb:FullTableScan")

	// So does a statement that does not parse.
	assertConditionKeysAbsent(t, ddbStatementConditionContext("dynamodb:PartiQLSelect",
		`SELECT * WHERE`), "dynamodb:FullTableScan")
}
