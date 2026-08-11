package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ddbSimpleTable creates a single-hash-key (S) table named pk and registers
// cleanup. Returns the table name.
func ddbSimpleTable(t *testing.T, c *dynamodb.Client, name string) {
	t.Helper()
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(name),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema:   []ddbtypes.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash}},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(name)}) })
}

func sN(v string) ddbtypes.AttributeValue { return &ddbtypes.AttributeValueMemberN{Value: v} }
func sS(v string) ddbtypes.AttributeValue { return &ddbtypes.AttributeValueMemberS{Value: v} }

// TestDDBIssue641_TransactWriteUpdateApplies — the Update action in a
// TransactWriteItems must mutate AND evaluate its ConditionExpression (it was
// silently dropped). Runs the atomic-counter-with-cap repro.
func TestDDBIssue641_TransactWriteUpdateApplies(t *testing.T) {
	c := ddbClient()
	tbl := "tx-update-641"
	ddbSimpleTable(t, c, tbl)

	ok, cancelled := 0, 0
	for i := 0; i < 7; i++ {
		_, err := c.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
			TransactItems: []ddbtypes.TransactWriteItem{
				{Put: &ddbtypes.Put{TableName: aws.String(tbl), Item: map[string]ddbtypes.AttributeValue{"pk": sS("ws#" + string(rune('0'+i)))}}},
				{Update: &ddbtypes.Update{
					TableName:                 aws.String(tbl),
					Key:                       map[string]ddbtypes.AttributeValue{"pk": sS("owner#a")},
					UpdateExpression:          aws.String("ADD #c :one"),
					ConditionExpression:       aws.String("attribute_not_exists(#c) OR #c < :limit"),
					ExpressionAttributeNames:  map[string]string{"#c": "count"},
					ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":one": sN("1"), ":limit": sN("3")},
				}},
			},
		})
		if err == nil {
			ok++
		} else {
			cancelled++
		}
	}
	// The Update's condition (count < 3) must be enforced: 3 succeed, 4 cancel.
	assert.Equal(t, 3, ok, "exactly 3 transactions should commit (cap enforced)")
	assert.Equal(t, 4, cancelled, "the rest cancel on the condition")

	got, err := c.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(tbl), Key: map[string]ddbtypes.AttributeValue{"pk": sS("owner#a")},
	})
	require.NoError(t, err)
	require.NotEmpty(t, got.Item, "counter item must exist (Update applied)")
	assert.Equal(t, "3", got.Item["count"].(*ddbtypes.AttributeValueMemberN).Value)
}

// TestDDBIssue642_CancellationReasons — a cancelled transaction must carry the
// per-item CancellationReasons array.
func TestDDBIssue642_CancellationReasons(t *testing.T) {
	c := ddbClient()
	tbl := "tx-reasons-642"
	ddbSimpleTable(t, c, tbl)
	// Seed the row item[1] will condition-fail on.
	_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tbl), Item: map[string]ddbtypes.AttributeValue{"pk": sS("exists")}})
	require.NoError(t, err)

	_, err = c.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []ddbtypes.TransactWriteItem{
			{Put: &ddbtypes.Put{TableName: aws.String(tbl), Item: map[string]ddbtypes.AttributeValue{"pk": sS("new")}}},
			{Put: &ddbtypes.Put{TableName: aws.String(tbl), Item: map[string]ddbtypes.AttributeValue{"pk": sS("exists")},
				ConditionExpression: aws.String("attribute_not_exists(pk)")}}, // fails: already exists
		},
	})
	require.Error(t, err)
	var tce *ddbtypes.TransactionCanceledException
	require.True(t, errors.As(err, &tce), "must be TransactionCanceledException, got %v", err)
	require.Len(t, tce.CancellationReasons, 2, "one reason per item, in order")
	assert.Equal(t, "None", aws.ToString(tce.CancellationReasons[0].Code))
	assert.Equal(t, "ConditionalCheckFailed", aws.ToString(tce.CancellationReasons[1].Code))
}

// TestDDBIssue643_IfNotExistsArithmetic — SET with an if_not_exists() operand in
// arithmetic must compute, not store null.
func TestDDBIssue643_IfNotExistsArithmetic(t *testing.T) {
	c := ddbClient()
	tbl := "ine-arith-643"
	ddbSimpleTable(t, c, tbl)

	// Decrement a fresh item: if_not_exists(c,:0) - :1 = -1.
	_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(tbl),
		Key:                       map[string]ddbtypes.AttributeValue{"pk": sS("dec")},
		UpdateExpression:          aws.String("SET #c = if_not_exists(#c, :z) - :v"),
		ExpressionAttributeNames:  map[string]string{"#c": "c"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":z": sN("0"), ":v": sN("1")},
	})
	require.NoError(t, err)
	got, err := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(tbl), Key: map[string]ddbtypes.AttributeValue{"pk": sS("dec")}})
	require.NoError(t, err)
	cv, ok := got.Item["c"].(*ddbtypes.AttributeValueMemberN)
	require.True(t, ok, "c must be a number, not null: %#v", got.Item["c"])
	assert.Equal(t, "-1", cv.Value)

	// Increment a fresh item: if_not_exists(c,:0) + :1 = 1.
	_, err = c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(tbl),
		Key:                       map[string]ddbtypes.AttributeValue{"pk": sS("inc")},
		UpdateExpression:          aws.String("SET #c = if_not_exists(#c, :z) + :v"),
		ExpressionAttributeNames:  map[string]string{"#c": "c"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":z": sN("0"), ":v": sN("1")},
	})
	require.NoError(t, err)
	got2, _ := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(tbl), Key: map[string]ddbtypes.AttributeValue{"pk": sS("inc")}})
	assert.Equal(t, "1", got2.Item["c"].(*ddbtypes.AttributeValueMemberN).Value)
}

// TestDDBIssue648_ParenthesizedSetRHS — a fully-enclosed SET RHS must be stripped
// of its parentheses and evaluate as the unparenthesized form (ElectroDB always
// wraps the arithmetic RHS of .subtract()/.add() in parens).
func TestDDBIssue648_ParenthesizedSetRHS(t *testing.T) {
	c := ddbClient()
	tbl := "paren-rhs-648"
	ddbSimpleTable(t, c, tbl)

	// (if_not_exists(c,:z) - :v) on a fresh item = -1.
	_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(tbl),
		Key:                       map[string]ddbtypes.AttributeValue{"pk": sS("dec")},
		UpdateExpression:          aws.String("SET #c = (if_not_exists(#c, :z) - :v)"),
		ExpressionAttributeNames:  map[string]string{"#c": "c"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":z": sN("0"), ":v": sN("1")},
	})
	require.NoError(t, err)
	got, _ := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(tbl), Key: map[string]ddbtypes.AttributeValue{"pk": sS("dec")}})
	cv, ok := got.Item["c"].(*ddbtypes.AttributeValueMemberN)
	require.True(t, ok, "c must be a number, not null: %#v", got.Item["c"])
	assert.Equal(t, "-1", cv.Value)

	// A plain parenthesized value (:z) must also resolve.
	_, err = c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(tbl),
		Key:                       map[string]ddbtypes.AttributeValue{"pk": sS("plain")},
		UpdateExpression:          aws.String("SET #c = (:z)"),
		ExpressionAttributeNames:  map[string]string{"#c": "c"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":z": sN("7")},
	})
	require.NoError(t, err)
	got2, _ := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(tbl), Key: map[string]ddbtypes.AttributeValue{"pk": sS("plain")}})
	assert.Equal(t, "7", got2.Item["c"].(*ddbtypes.AttributeValueMemberN).Value)
}

// TestDDBIssue644_DeleteTablePurgesItems — items must not survive a drop+recreate.
func TestDDBIssue644_DeleteTablePurgesItems(t *testing.T) {
	c := ddbClient()
	tbl := "drop-recreate-644"
	ddbSimpleTable(t, c, tbl)
	_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tbl), Item: map[string]ddbtypes.AttributeValue{"pk": sS("ghost")}})
	require.NoError(t, err)

	_, err = c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(tbl)})
	require.NoError(t, err)
	ddbSimpleTable(t, c, tbl) // recreate same name

	scan, err := c.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(tbl)})
	require.NoError(t, err)
	assert.Equal(t, int32(0), scan.Count, "recreated table must be empty (items purged on delete)")
}

// TestDDBListAppend — SET with list_append() concatenates lists (was unimplemented).
func TestDDBListAppend(t *testing.T) {
	c := ddbClient()
	tbl := "list-append"
	ddbSimpleTable(t, c, tbl)
	_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                aws.String(tbl),
		Key:                      map[string]ddbtypes.AttributeValue{"pk": sS("l")},
		UpdateExpression:         aws.String("SET #l = list_append(if_not_exists(#l, :empty), :new)"),
		ExpressionAttributeNames: map[string]string{"#l": "items"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":empty": &ddbtypes.AttributeValueMemberL{Value: []ddbtypes.AttributeValue{}},
			":new":   &ddbtypes.AttributeValueMemberL{Value: []ddbtypes.AttributeValue{sS("a"), sS("b")}},
		},
	})
	require.NoError(t, err)
	got, _ := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(tbl), Key: map[string]ddbtypes.AttributeValue{"pk": sS("l")}})
	l, ok := got.Item["items"].(*ddbtypes.AttributeValueMemberL)
	require.True(t, ok, "items must be a list")
	require.Len(t, l.Value, 2)
}
