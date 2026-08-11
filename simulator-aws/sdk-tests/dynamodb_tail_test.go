package aws_sdk_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── PartiQL ───────────────────────────────────────────────────────────────────

// TestDDBPartiQLExecuteStatement covers INSERT / SELECT / UPDATE / DELETE through
// ExecuteStatement.
func TestDDBPartiQLExecuteStatement(t *testing.T) {
	c := ddbClient()
	tbl := "partiql-exec"
	ddbSimpleTable(t, c, tbl)

	// INSERT
	_, err := c.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
		Statement:  aws.String(`INSERT INTO "` + tbl + `" VALUE {'pk': ?, 'n': ?}`),
		Parameters: []ddbtypes.AttributeValue{sS("a"), sN("1")},
	})
	require.NoError(t, err)

	// Duplicate INSERT must fail.
	_, err = c.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
		Statement:  aws.String(`INSERT INTO "` + tbl + `" VALUE {'pk': ?}`),
		Parameters: []ddbtypes.AttributeValue{sS("a")},
	})
	require.Error(t, err, "duplicate INSERT must fail")

	// SELECT by full key → point read.
	sel, err := c.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
		Statement:  aws.String(`SELECT * FROM "` + tbl + `" WHERE pk = ?`),
		Parameters: []ddbtypes.AttributeValue{sS("a")},
	})
	require.NoError(t, err)
	require.Len(t, sel.Items, 1)
	assert.Equal(t, "1", sel.Items[0]["n"].(*ddbtypes.AttributeValueMemberN).Value)

	// UPDATE SET.
	_, err = c.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
		Statement:  aws.String(`UPDATE "` + tbl + `" SET n = ? WHERE pk = ?`),
		Parameters: []ddbtypes.AttributeValue{sN("99"), sS("a")},
	})
	require.NoError(t, err)
	sel2, _ := c.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
		Statement:  aws.String(`SELECT n FROM "` + tbl + `" WHERE pk = ?`),
		Parameters: []ddbtypes.AttributeValue{sS("a")},
	})
	require.Len(t, sel2.Items, 1)
	assert.Equal(t, "99", sel2.Items[0]["n"].(*ddbtypes.AttributeValueMemberN).Value)

	// DELETE.
	_, err = c.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
		Statement:  aws.String(`DELETE FROM "` + tbl + `" WHERE pk = ?`),
		Parameters: []ddbtypes.AttributeValue{sS("a")},
	})
	require.NoError(t, err)
	sel3, _ := c.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
		Statement:  aws.String(`SELECT * FROM "` + tbl + `" WHERE pk = ?`),
		Parameters: []ddbtypes.AttributeValue{sS("a")},
	})
	assert.Empty(t, sel3.Items)
}

// TestDDBPartiQLSelectFilterAndOrder covers a non-key SELECT (scan + filter) with
// ORDER BY.
func TestDDBPartiQLSelectFilterAndOrder(t *testing.T) {
	c := ddbClient()
	tbl := "partiql-select"
	ddbSortTable(t, c, tbl)
	for _, sk := range []string{"1", "2", "3"} {
		_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tbl),
			Item: map[string]ddbtypes.AttributeValue{"pk": sS("p"), "sk": sN(sk)}})
		require.NoError(t, err)
	}
	out, err := c.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
		Statement:  aws.String(`SELECT sk FROM "` + tbl + `" WHERE pk = ? ORDER BY sk DESC`),
		Parameters: []ddbtypes.AttributeValue{sS("p")},
	})
	require.NoError(t, err)
	require.Len(t, out.Items, 3)
	assert.Equal(t, "3", out.Items[0]["sk"].(*ddbtypes.AttributeValueMemberN).Value, "ORDER BY ... DESC")
	assert.Equal(t, "1", out.Items[2]["sk"].(*ddbtypes.AttributeValueMemberN).Value)
}

// TestDDBPartiQLBatch covers BatchExecuteStatement — per-statement errors don't
// fail the batch.
func TestDDBPartiQLBatch(t *testing.T) {
	c := ddbClient()
	tbl := "partiql-batch"
	ddbSimpleTable(t, c, tbl)
	_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tbl), Item: map[string]ddbtypes.AttributeValue{"pk": sS("x")}})
	require.NoError(t, err)

	out, err := c.BatchExecuteStatement(ctx, &dynamodb.BatchExecuteStatementInput{
		Statements: []ddbtypes.BatchStatementRequest{
			{Statement: aws.String(`SELECT * FROM "` + tbl + `" WHERE pk = ?`), Parameters: []ddbtypes.AttributeValue{sS("x")}},
			{Statement: aws.String(`INSERT INTO "` + tbl + `" VALUE {'pk': ?}`), Parameters: []ddbtypes.AttributeValue{sS("x")}}, // dup → error
		},
	})
	require.NoError(t, err, "the batch call itself succeeds")
	require.Len(t, out.Responses, 2)
	assert.NotNil(t, out.Responses[0].Item, "first statement read the item")
	assert.NotNil(t, out.Responses[1].Error, "second statement (duplicate insert) carries a per-item Error")
}

// TestDDBPartiQLTransaction covers ExecuteTransaction — all-or-nothing.
func TestDDBPartiQLTransaction(t *testing.T) {
	c := ddbClient()
	tbl := "partiql-tx"
	ddbSimpleTable(t, c, tbl)
	_, err := c.ExecuteTransaction(ctx, &dynamodb.ExecuteTransactionInput{
		TransactStatements: []ddbtypes.ParameterizedStatement{
			{Statement: aws.String(`INSERT INTO "` + tbl + `" VALUE {'pk': ?}`), Parameters: []ddbtypes.AttributeValue{sS("t1")}},
			{Statement: aws.String(`INSERT INTO "` + tbl + `" VALUE {'pk': ?}`), Parameters: []ddbtypes.AttributeValue{sS("t2")}},
		},
	})
	require.NoError(t, err)
	scan, _ := c.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(tbl)})
	assert.Equal(t, int32(2), scan.Count)

	// A transaction with a failing statement must roll back entirely.
	_, err = c.ExecuteTransaction(ctx, &dynamodb.ExecuteTransactionInput{
		TransactStatements: []ddbtypes.ParameterizedStatement{
			{Statement: aws.String(`INSERT INTO "` + tbl + `" VALUE {'pk': ?}`), Parameters: []ddbtypes.AttributeValue{sS("t3")}},
			{Statement: aws.String(`INSERT INTO "` + tbl + `" VALUE {'pk': ?}`), Parameters: []ddbtypes.AttributeValue{sS("t1")}}, // dup → cancel
		},
	})
	require.Error(t, err)
	scan2, _ := c.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(tbl)})
	assert.Equal(t, int32(2), scan2.Count, "t3 must not have been inserted (rolled back)")
}

// ── tail fidelity fixes ───────────────────────────────────────────────────────

// TestDDBDescribeLimits covers the DescribeLimits op.
func TestDDBDescribeLimits(t *testing.T) {
	c := ddbClient()
	out, err := c.DescribeLimits(ctx, &dynamodb.DescribeLimitsInput{})
	require.NoError(t, err)
	assert.Greater(t, aws.ToInt64(out.AccountMaxReadCapacityUnits), int64(0))
	assert.Greater(t, aws.ToInt64(out.TableMaxWriteCapacityUnits), int64(0))
}

// TestDDBConsumedCapacity — ReturnConsumedCapacity yields a ConsumedCapacity block.
func TestDDBConsumedCapacity(t *testing.T) {
	c := ddbClient()
	tbl := "cc"
	ddbSimpleTable(t, c, tbl)
	put, err := c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:              aws.String(tbl),
		Item:                   map[string]ddbtypes.AttributeValue{"pk": sS("x")},
		ReturnConsumedCapacity: ddbtypes.ReturnConsumedCapacityTotal,
	})
	require.NoError(t, err)
	require.NotNil(t, put.ConsumedCapacity, "ConsumedCapacity must be returned")
	assert.Equal(t, tbl, aws.ToString(put.ConsumedCapacity.TableName))
	assert.Greater(t, aws.ToFloat64(put.ConsumedCapacity.CapacityUnits), 0.0)
}

func TestDDBStoredByteAccountingAndItemLimit(t *testing.T) {
	c := ddbClient()
	tbl := "item-byte-accounting"
	ddbSimpleTable(t, c, tbl)

	put, err := c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tbl),
		Item: map[string]ddbtypes.AttributeValue{
			"pk":      sS("binary"),
			"payload": &ddbtypes.AttributeValueMemberB{Value: bytes.Repeat([]byte{0x7f}, 760)},
		},
		ReturnConsumedCapacity: ddbtypes.ReturnConsumedCapacityTotal,
	})
	require.NoError(t, err)
	require.NotNil(t, put.ConsumedCapacity)
	assert.Equal(t, 1.0, aws.ToFloat64(put.ConsumedCapacity.CapacityUnits),
		"binary capacity uses decoded bytes rather than base64 wire length")

	const maxItemBytes = 400 * 1024
	exactPayload := strings.Repeat("x", maxItemBytes-len("pk")-len("exact")-len("payload"))
	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tbl),
		Item: map[string]ddbtypes.AttributeValue{
			"pk":      sS("exact"),
			"payload": sS(exactPayload),
		},
	})
	require.NoError(t, err, "an item exactly at the 400 KiB stored-size limit must succeed")

	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tbl),
		Item: map[string]ddbtypes.AttributeValue{
			"pk":      sS("exact"),
			"payload": sS(exactPayload + "x"),
		},
	})
	require.Error(t, err, "an item one byte over the 400 KiB stored-size limit must fail")
	assert.Contains(t, err.Error(), "ValidationException")
}

// TestDDBCreateTableKeySchemaValidation — a key attr missing from
// AttributeDefinitions is rejected.
func TestDDBCreateTableKeySchemaValidation(t *testing.T) {
	c := ddbClient()
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String("bad-schema"),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
		KeySchema:            []ddbtypes.KeySchemaElement{{AttributeName: aws.String("notdefined"), KeyType: ddbtypes.KeyTypeHash}},
		BillingMode:          ddbtypes.BillingModePayPerRequest,
	})
	require.Error(t, err, "key attr not in AttributeDefinitions must be rejected")
	assert.Contains(t, err.Error(), "ValidationException")
}

// TestDDBLegacyAttributeUpdatesAdd — AttributeUpdates with Action ADD increments.
func TestDDBLegacyAttributeUpdatesAdd(t *testing.T) {
	c := ddbClient()
	tbl := "legacy-add"
	ddbSimpleTable(t, c, tbl)
	for i := 0; i < 2; i++ {
		_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: aws.String(tbl),
			Key:       map[string]ddbtypes.AttributeValue{"pk": sS("c")},
			AttributeUpdates: map[string]ddbtypes.AttributeValueUpdate{
				"count": {Action: ddbtypes.AttributeActionAdd, Value: sN("1")},
			},
		})
		require.NoError(t, err)
	}
	got, _ := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(tbl), Key: map[string]ddbtypes.AttributeValue{"pk": sS("c")}})
	assert.Equal(t, "2", got.Item["count"].(*ddbtypes.AttributeValueMemberN).Value, "ADD must increment, not overwrite")
}

// TestDDBNestedProjection — ProjectionExpression a.b returns only the nested attr.
func TestDDBNestedProjection(t *testing.T) {
	c := ddbClient()
	tbl := "nested-proj"
	ddbSimpleTable(t, c, tbl)
	_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tbl), Item: map[string]ddbtypes.AttributeValue{
		"pk": sS("x"),
		"profile": &ddbtypes.AttributeValueMemberM{Value: map[string]ddbtypes.AttributeValue{
			"age":  sN("42"),
			"name": sS("zed"),
		}},
	}})
	require.NoError(t, err)
	got, err := c.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:                aws.String(tbl),
		Key:                      map[string]ddbtypes.AttributeValue{"pk": sS("x")},
		ProjectionExpression:     aws.String("#p.age"),
		ExpressionAttributeNames: map[string]string{"#p": "profile"},
	})
	require.NoError(t, err)
	prof, ok := got.Item["profile"].(*ddbtypes.AttributeValueMemberM)
	require.True(t, ok)
	_, hasAge := prof.Value["age"]
	_, hasName := prof.Value["name"]
	assert.True(t, hasAge, "projected nested attr present")
	assert.False(t, hasName, "non-projected sibling must be absent")
}

// TestDDBLegacyExpected — the legacy Expected map gates a write.
func TestDDBLegacyExpected(t *testing.T) {
	c := ddbClient()
	tbl := "legacy-expected"
	ddbSimpleTable(t, c, tbl)
	// First write requires the item NOT to exist.
	_, err := c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tbl),
		Item:      map[string]ddbtypes.AttributeValue{"pk": sS("u")},
		Expected:  map[string]ddbtypes.ExpectedAttributeValue{"pk": {Exists: aws.Bool(false)}},
	})
	require.NoError(t, err)
	// Second write with the same Expected must fail (it now exists).
	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tbl),
		Item:      map[string]ddbtypes.AttributeValue{"pk": sS("u")},
		Expected:  map[string]ddbtypes.ExpectedAttributeValue{"pk": {Exists: aws.Bool(false)}},
	})
	require.Error(t, err, "Expected{Exists:false} must fail once the item exists")
}

// TestDDBConsistentReadOnGSI — ConsistentRead against a GSI is rejected.
func TestDDBConsistentReadOnGSI(t *testing.T) {
	c := ddbClient()
	tbl := "gsi-consistent"
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tbl),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema:   []ddbtypes.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash}},
		BillingMode: ddbtypes.BillingModePayPerRequest,
		GlobalSecondaryIndexes: []ddbtypes.GlobalSecondaryIndex{{
			IndexName:  aws.String("gsi1"),
			KeySchema:  []ddbtypes.KeySchemaElement{{AttributeName: aws.String("gsk"), KeyType: ddbtypes.KeyTypeHash}},
			Projection: &ddbtypes.Projection{ProjectionType: ddbtypes.ProjectionTypeAll},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(tbl)}) })

	_, err = c.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(tbl),
		IndexName:                 aws.String("gsi1"),
		KeyConditionExpression:    aws.String("gsk = :v"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":v": sS("a")},
		ConsistentRead:            aws.Bool(true),
	})
	require.Error(t, err, "ConsistentRead on a GSI must be rejected")
	assert.True(t, strings.Contains(err.Error(), "ValidationException") || strings.Contains(err.Error(), "onsistent"))
}

// ── AWS DynamoDB expression-engine fixes (BUG-2150..2153) ─────────────────────

// TestDDBNestedIfNotExists — if_not_exists() and a bare-path copy resolve NESTED
// document paths, not just top-level attrs (BUG-2150, follow-on to #648).
func TestDDBNestedIfNotExists(t *testing.T) {
	c := ddbClient()
	tbl := "nested-ine"
	ddbSimpleTable(t, c, tbl)
	_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tbl), Item: map[string]ddbtypes.AttributeValue{
		"pk": sS("x"),
		"a":  &ddbtypes.AttributeValueMemberM{Value: map[string]ddbtypes.AttributeValue{"b": sN("7")}},
	}})
	require.NoError(t, err)
	// if_not_exists(a.b, :z) must find the EXISTING nested 7, so 7 - 1 = 6.
	_, err = c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(tbl),
		Key:                       map[string]ddbtypes.AttributeValue{"pk": sS("x")},
		UpdateExpression:          aws.String("SET #a.#b = if_not_exists(#a.#b, :z) - :v"),
		ExpressionAttributeNames:  map[string]string{"#a": "a", "#b": "b"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":z": sN("0"), ":v": sN("1")},
	})
	require.NoError(t, err)
	got, _ := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(tbl), Key: map[string]ddbtypes.AttributeValue{"pk": sS("x")}})
	m := got.Item["a"].(*ddbtypes.AttributeValueMemberM)
	assert.Equal(t, "6", m.Value["b"].(*ddbtypes.AttributeValueMemberN).Value, "nested if_not_exists must use the existing value")
}

// TestDDBNumberEqualityCanonical — N equality compares by value (5 == 5.0) in a
// FilterExpression (BUG-2151).
func TestDDBNumberEqualityCanonical(t *testing.T) {
	c := ddbClient()
	tbl := "num-eq"
	ddbSimpleTable(t, c, tbl)
	_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tbl),
		Item: map[string]ddbtypes.AttributeValue{"pk": sS("x"), "n": sN("5.0")}})
	require.NoError(t, err)
	out, err := c.Scan(ctx, &dynamodb.ScanInput{
		TableName:                 aws.String(tbl),
		FilterExpression:          aws.String("n = :p"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":p": sN("5")},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), out.Count, "{N:5.0} must equal {N:5}")
}

// TestDDBBigNumberArithmetic — arithmetic SET keeps full integer precision past
// 2^53 (BUG-2152).
func TestDDBBigNumberArithmetic(t *testing.T) {
	c := ddbClient()
	tbl := "big-num"
	ddbSimpleTable(t, c, tbl)
	_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(tbl),
		Key:                       map[string]ddbtypes.AttributeValue{"pk": sS("x")},
		UpdateExpression:          aws.String("SET n = if_not_exists(n, :start) + :one"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":start": sN("9007199254740993"), ":one": sN("1")},
	})
	require.NoError(t, err)
	got, _ := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(tbl), Key: map[string]ddbtypes.AttributeValue{"pk": sS("x")}})
	assert.Equal(t, "9007199254740994", got.Item["n"].(*ddbtypes.AttributeValueMemberN).Value, "no float64 rounding")
}

// TestDDBArithmeticNonNumericRejected — +/- on a non-numeric operand errors
// instead of silently storing 0 (BUG-2153).
func TestDDBArithmeticNonNumericRejected(t *testing.T) {
	c := ddbClient()
	tbl := "arith-type"
	ddbSimpleTable(t, c, tbl)
	_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(tbl),
		Key:                       map[string]ddbtypes.AttributeValue{"pk": sS("x")},
		UpdateExpression:          aws.String("SET n = :s + :one"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":s": sS("hello"), ":one": sN("1")},
	})
	require.Error(t, err, "arithmetic on a string operand must be rejected")
}
