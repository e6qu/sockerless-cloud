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

func ddbClient() *dynamodb.Client {
	return dynamodb.NewFromConfig(sdkConfig(), func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestDynamoDB_TableLifecycle covers Create/Describe/List/Delete on
// the table surface terraform's `aws_dynamodb_table` exercises.
func TestDynamoDB_TableLifecycle(t *testing.T) {
	c := ddbClient()
	tableName := "runner-state-test"

	defer c.DeleteTable(ctx, &dynamodb.DeleteTableInput{
		TableName: aws.String(tableName),
	})

	createOut, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("LockID"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("LockID"), KeyType: ddbtypes.KeyTypeHash},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.TableDescription)
	assert.Equal(t, "ACTIVE", string(createOut.TableDescription.TableStatus))
	assert.Contains(t, *createOut.TableDescription.TableArn, "arn:aws:dynamodb:")

	desc, err := c.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(tableName),
	})
	require.NoError(t, err)
	require.NotNil(t, desc.Table)
	assert.Equal(t, tableName, *desc.Table.TableName)
	assert.Equal(t, "ACTIVE", string(desc.Table.TableStatus))
	assert.Contains(t, *desc.Table.TableArn, "arn:aws:dynamodb:")
	// BillingModeSummary must reflect PAY_PER_REQUEST.
	require.NotNil(t, desc.Table.BillingModeSummary, "BillingModeSummary must be present")
	assert.Equal(t, "PAY_PER_REQUEST", string(desc.Table.BillingModeSummary.BillingMode))
	// ProvisionedThroughput must be present (zero-filled) even for on-demand tables.
	require.NotNil(t, desc.Table.ProvisionedThroughput, "ProvisionedThroughput must be present")
	// TableClassSummary must default to STANDARD.
	require.NotNil(t, desc.Table.TableClassSummary, "TableClassSummary must be present")
	assert.Equal(t, "STANDARD", string(desc.Table.TableClassSummary.TableClass))
	// WarmThroughput must be non-nil with Status=ACTIVE — terraform-provider-aws v6
	// waitTableWarmThroughputActive loops 21 times and errors if the field is absent.
	require.NotNil(t, desc.Table.WarmThroughput, "WarmThroughput must be present")
	assert.Equal(t, "ACTIVE", string(desc.Table.WarmThroughput.Status))

	list, err := c.ListTables(ctx, &dynamodb.ListTablesInput{})
	require.NoError(t, err)
	assert.Contains(t, list.TableNames, tableName)
}

// TestDynamoDB_GlobalSecondaryIndexes pins CreateTable
// must echo each GSI in TableDescription with IndexStatus==ACTIVE, and
// DescribeTable must report the same. Pre-fix the sim dropped all GSIs
// (returned null), so terraform-provider-aws waited for GSI ACTIVE forever.
func TestDynamoDB_GlobalSecondaryIndexes(t *testing.T) {
	c := ddbClient()
	tableName := "gsi-probe"
	defer c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(tableName)})

	createOut, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("SK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("GSI1PK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("GSI2PK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String("SK"), KeyType: ddbtypes.KeyTypeRange},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
		GlobalSecondaryIndexes: []ddbtypes.GlobalSecondaryIndex{
			{
				IndexName:  aws.String("GSI1"),
				KeySchema:  []ddbtypes.KeySchemaElement{{AttributeName: aws.String("GSI1PK"), KeyType: ddbtypes.KeyTypeHash}},
				Projection: &ddbtypes.Projection{ProjectionType: ddbtypes.ProjectionTypeAll},
			},
			{
				IndexName:  aws.String("GSI2"),
				KeySchema:  []ddbtypes.KeySchemaElement{{AttributeName: aws.String("GSI2PK"), KeyType: ddbtypes.KeyTypeHash}},
				Projection: &ddbtypes.Projection{ProjectionType: ddbtypes.ProjectionTypeKeysOnly},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.TableDescription)

	// gsiStatus maps index name -> status, requiring the projection to round-trip.
	gsiStatus := func(gsis []ddbtypes.GlobalSecondaryIndexDescription) map[string]string {
		m := map[string]string{}
		for _, g := range gsis {
			m[aws.ToString(g.IndexName)] = string(g.IndexStatus)
			require.NotNil(t, g.Projection, "GSI %s must carry its Projection", aws.ToString(g.IndexName))
			assert.Contains(t, aws.ToString(g.IndexArn), ":table/"+tableName+"/index/"+aws.ToString(g.IndexName))
		}
		return m
	}

	created := gsiStatus(createOut.TableDescription.GlobalSecondaryIndexes)
	require.Len(t, created, 2, "CreateTable response must echo both GSIs (was null pre-fix)")
	assert.Equal(t, "ACTIVE", created["GSI1"])
	assert.Equal(t, "ACTIVE", created["GSI2"])

	desc, err := c.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)})
	require.NoError(t, err)
	require.NotNil(t, desc.Table)
	described := gsiStatus(desc.Table.GlobalSecondaryIndexes)
	require.Len(t, described, 2, "DescribeTable must report both GSIs")
	assert.Equal(t, "ACTIVE", described["GSI1"])
	assert.Equal(t, "ACTIVE", described["GSI2"])
}

// TestDynamoDB_UpdateTableGSI covers the post-create GSI lifecycle that
// terraform-provider-aws drives via UpdateTable's GlobalSecondaryIndexUpdates:
// add a GSI (declaring its new attribute), confirm it goes ACTIVE, then delete
// it. Pre-fix UpdateTable was unregistered (404 UnknownOperation).
func TestDynamoDB_UpdateTableGSI(t *testing.T) {
	c := ddbClient()
	table := "update-gsi"
	defer c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)})

	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(table),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema:   []ddbtypes.KeySchemaElement{{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash}},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)

	// Add a GSI post-create (declaring the new attribute in the same call).
	upd, err := c.UpdateTable(ctx, &dynamodb.UpdateTableInput{
		TableName: aws.String(table),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("GSIPK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		GlobalSecondaryIndexUpdates: []ddbtypes.GlobalSecondaryIndexUpdate{
			{Create: &ddbtypes.CreateGlobalSecondaryIndexAction{
				IndexName:  aws.String("LateGSI"),
				KeySchema:  []ddbtypes.KeySchemaElement{{AttributeName: aws.String("GSIPK"), KeyType: ddbtypes.KeyTypeHash}},
				Projection: &ddbtypes.Projection{ProjectionType: ddbtypes.ProjectionTypeAll},
			}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, upd.TableDescription)
	require.Len(t, upd.TableDescription.GlobalSecondaryIndexes, 1)
	assert.Equal(t, "ACTIVE", string(upd.TableDescription.GlobalSecondaryIndexes[0].IndexStatus))

	desc, err := c.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)})
	require.NoError(t, err)
	require.Len(t, desc.Table.GlobalSecondaryIndexes, 1, "DescribeTable must report the added GSI")

	// Delete the GSI.
	_, err = c.UpdateTable(ctx, &dynamodb.UpdateTableInput{
		TableName: aws.String(table),
		GlobalSecondaryIndexUpdates: []ddbtypes.GlobalSecondaryIndexUpdate{
			{Delete: &ddbtypes.DeleteGlobalSecondaryIndexAction{IndexName: aws.String("LateGSI")}},
		},
	})
	require.NoError(t, err)
	desc, err = c.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)})
	require.NoError(t, err)
	assert.Empty(t, desc.Table.GlobalSecondaryIndexes, "deleted GSI must be gone")
}

// TestDynamoDB_QueryOnGSI verifies a Query against a GSI returns the items
// whose GSI key matches, that an unknown index is rejected, and that
// ScannedCount is reported.
func TestDynamoDB_QueryOnGSI(t *testing.T) {
	c := ddbClient()
	table := "gsi-query"
	defer c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)})

	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(table),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("GSIPK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema:   []ddbtypes.KeySchemaElement{{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash}},
		BillingMode: ddbtypes.BillingModePayPerRequest,
		GlobalSecondaryIndexes: []ddbtypes.GlobalSecondaryIndex{{
			IndexName:  aws.String("by-gsi"),
			KeySchema:  []ddbtypes.KeySchemaElement{{AttributeName: aws.String("GSIPK"), KeyType: ddbtypes.KeyTypeHash}},
			Projection: &ddbtypes.Projection{ProjectionType: ddbtypes.ProjectionTypeAll},
		}},
	})
	require.NoError(t, err)

	put := func(pk, gsipk string) {
		item := map[string]ddbtypes.AttributeValue{"PK": &ddbtypes.AttributeValueMemberS{Value: pk}}
		if gsipk != "" {
			item["GSIPK"] = &ddbtypes.AttributeValueMemberS{Value: gsipk}
		}
		_, perr := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: item})
		require.NoError(t, perr)
	}
	put("a", "group1")
	put("b", "group1")
	put("c", "group2")
	put("d", "") // not in the GSI

	out, err := c.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(table),
		IndexName:                 aws.String("by-gsi"),
		KeyConditionExpression:    aws.String("GSIPK = :g"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":g": &ddbtypes.AttributeValueMemberS{Value: "group1"}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, out.Count, "GSI query must return the two group1 items")
	assert.EqualValues(t, 2, out.ScannedCount)

	// Unknown index must be rejected.
	_, err = c.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(table),
		IndexName:                 aws.String("does-not-exist"),
		KeyConditionExpression:    aws.String("GSIPK = :g"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":g": &ddbtypes.AttributeValueMemberS{Value: "group1"}},
	})
	require.Error(t, err, "querying an unknown index must fail with ValidationException")
}

// TestDynamoDB_BatchAndTransact covers BatchWriteItem/BatchGetItem and the
// transactional ops, plus a richer ConditionExpression than the state-lock case.
func TestDynamoDB_BatchAndTransact(t *testing.T) {
	c := ddbClient()
	table := "batch-tx"
	defer c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)})
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(table),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
		KeySchema:            []ddbtypes.KeySchemaElement{{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash}},
		BillingMode:          ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	s := func(v string) *ddbtypes.AttributeValueMemberS { return &ddbtypes.AttributeValueMemberS{Value: v} }

	// BatchWriteItem: put three.
	_, err = c.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
		RequestItems: map[string][]ddbtypes.WriteRequest{table: {
			{PutRequest: &ddbtypes.PutRequest{Item: map[string]ddbtypes.AttributeValue{"PK": s("a")}}},
			{PutRequest: &ddbtypes.PutRequest{Item: map[string]ddbtypes.AttributeValue{"PK": s("b")}}},
			{PutRequest: &ddbtypes.PutRequest{Item: map[string]ddbtypes.AttributeValue{"PK": s("c")}}},
		}},
	})
	require.NoError(t, err)

	// BatchGetItem: read two back.
	bg, err := c.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
		RequestItems: map[string]ddbtypes.KeysAndAttributes{table: {Keys: []map[string]ddbtypes.AttributeValue{
			{"PK": s("a")}, {"PK": s("c")},
		}}},
	})
	require.NoError(t, err)
	assert.Len(t, bg.Responses[table], 2)

	// TransactWriteItems: put "d" only if it doesn't exist — succeeds, then fails.
	txPut := func() error {
		_, e := c.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
			TransactItems: []ddbtypes.TransactWriteItem{{Put: &ddbtypes.Put{
				TableName:           aws.String(table),
				Item:                map[string]ddbtypes.AttributeValue{"PK": s("d")},
				ConditionExpression: aws.String("attribute_not_exists(PK)"),
			}}},
		})
		return e
	}
	require.NoError(t, txPut())
	require.Error(t, txPut(), "second transactional put must be cancelled by the condition")

	// TransactGetItems.
	tg, err := c.TransactGetItems(ctx, &dynamodb.TransactGetItemsInput{
		TransactItems: []ddbtypes.TransactGetItem{{Get: &ddbtypes.Get{
			TableName: aws.String(table), Key: map[string]ddbtypes.AttributeValue{"PK": s("d")},
		}}},
	})
	require.NoError(t, err)
	require.Len(t, tg.Responses, 1)
	require.NotNil(t, tg.Responses[0].Item)

	// Richer ConditionExpression: numeric comparison.
	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(table),
		Item:      map[string]ddbtypes.AttributeValue{"PK": s("ver"), "v": &ddbtypes.AttributeValueMemberN{Value: "5"}},
	})
	require.NoError(t, err)
	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                 aws.String(table),
		Item:                      map[string]ddbtypes.AttributeValue{"PK": s("ver"), "v": &ddbtypes.AttributeValueMemberN{Value: "9"}},
		ConditionExpression:       aws.String("v < :max"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":max": &ddbtypes.AttributeValueMemberN{Value: "10"}},
	})
	require.NoError(t, err, "condition v < 10 should hold (current v=5)")
}

// TestDynamoDB_TerraformStateLockSemantics pins the canonical
// state-lock acquire/release flow that terraform uses with
// `backend "s3" { dynamodb_table = "..." }` — PutItem with
// ConditionExpression="attribute_not_exists(LockID)" must succeed
// the first time and fail with ConditionalCheckFailedException on
// the second concurrent acquire.
func TestDynamoDB_TerraformStateLockSemantics(t *testing.T) {
	c := ddbClient()
	tableName := "tf-lock-test"

	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("LockID"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("LockID"), KeyType: ddbtypes.KeyTypeHash},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	defer c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(tableName)})

	// First acquire — should succeed.
	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tableName),
		Item: map[string]ddbtypes.AttributeValue{
			"LockID": &ddbtypes.AttributeValueMemberS{Value: "tf-state-lock"},
			"Owner":  &ddbtypes.AttributeValueMemberS{Value: "runner-1"},
		},
		ConditionExpression: aws.String("attribute_not_exists(LockID)"),
	})
	require.NoError(t, err, "first PutItem must succeed when no item exists")

	// Second acquire — must fail with the ConditionalCheckFailed shape
	// terraform translates into "lock contention".
	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tableName),
		Item: map[string]ddbtypes.AttributeValue{
			"LockID": &ddbtypes.AttributeValueMemberS{Value: "tf-state-lock"},
			"Owner":  &ddbtypes.AttributeValueMemberS{Value: "runner-2"},
		},
		ConditionExpression: aws.String("attribute_not_exists(LockID)"),
	})
	require.Error(t, err, "second PutItem must fail with ConditionalCheckFailedException")

	// GetItem returns the original item (held by runner-1).
	got, err := c.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(tableName),
		Key: map[string]ddbtypes.AttributeValue{
			"LockID": &ddbtypes.AttributeValueMemberS{Value: "tf-state-lock"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, got.Item)
	owner := got.Item["Owner"].(*ddbtypes.AttributeValueMemberS)
	assert.Equal(t, "runner-1", owner.Value)

	// Release the lock.
	_, err = c.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(tableName),
		Key: map[string]ddbtypes.AttributeValue{
			"LockID": &ddbtypes.AttributeValueMemberS{Value: "tf-state-lock"},
		},
	})
	require.NoError(t, err)

	// New runner can now acquire.
	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tableName),
		Item: map[string]ddbtypes.AttributeValue{
			"LockID": &ddbtypes.AttributeValueMemberS{Value: "tf-state-lock"},
			"Owner":  &ddbtypes.AttributeValueMemberS{Value: "runner-3"},
		},
		ConditionExpression: aws.String("attribute_not_exists(LockID)"),
	})
	require.NoError(t, err, "PutItem after Delete must succeed")
}

func TestDynamoDB_DeleteItemReturnValuesAllOld(t *testing.T) {
	c := ddbClient()
	tableName := "delete-all-old-test"

	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	defer c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(tableName)})

	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tableName),
		Item: map[string]ddbtypes.AttributeValue{
			"PK":   &ddbtypes.AttributeValueMemberS{Value: "key-1"},
			"Data": &ddbtypes.AttributeValueMemberS{Value: "hello"},
		},
	})
	require.NoError(t, err)

	deleted, err := c.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(tableName),
		Key: map[string]ddbtypes.AttributeValue{
			"PK": &ddbtypes.AttributeValueMemberS{Value: "key-1"},
		},
		ReturnValues: ddbtypes.ReturnValueAllOld,
	})
	require.NoError(t, err)
	require.NotNil(t, deleted.Attributes)
	require.Equal(t, "key-1", deleted.Attributes["PK"].(*ddbtypes.AttributeValueMemberS).Value)
	require.Equal(t, "hello", deleted.Attributes["Data"].(*ddbtypes.AttributeValueMemberS).Value)

	missing, err := c.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(tableName),
		Key: map[string]ddbtypes.AttributeValue{
			"PK": &ddbtypes.AttributeValueMemberS{Value: "key-1"},
		},
		ReturnValues: ddbtypes.ReturnValueAllOld,
	})
	require.NoError(t, err)
	assert.Empty(t, missing.Attributes)
}

func TestDynamoDB_QueryAndScan(t *testing.T) {
	c := ddbClient()
	tableName := "query-scan-test"
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("ID"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("ID"), KeyType: ddbtypes.KeyTypeHash},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	defer c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(tableName)})

	for _, id := range []string{"a", "b", "c"} {
		_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String(tableName),
			Item: map[string]ddbtypes.AttributeValue{
				"ID": &ddbtypes.AttributeValueMemberS{Value: id},
			},
		})
		require.NoError(t, err)
	}

	scan, err := c.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(tableName)})
	require.NoError(t, err)
	assert.Equal(t, int32(3), scan.Count)

	query, err := c.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(tableName),
		KeyConditionExpression: aws.String("ID = :id"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":id": &ddbtypes.AttributeValueMemberS{Value: "b"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, int32(1), query.Count)
	assert.Equal(t, "b", query.Items[0]["ID"].(*ddbtypes.AttributeValueMemberS).Value)

	filtered, err := c.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(tableName),
		FilterExpression: aws.String("#id = :id"),
		ExpressionAttributeNames: map[string]string{
			"#id": "ID",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":id": &ddbtypes.AttributeValueMemberS{Value: "c"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, int32(1), filtered.Count)
	assert.Equal(t, "c", filtered.Items[0]["ID"].(*ddbtypes.AttributeValueMemberS).Value)
}

func TestDDB_Scan_Pagination(t *testing.T) {
	client := ddbClient()
	table := "pag-scan-table"
	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(table),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { client.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)}) })

	for _, v := range []string{"a", "b", "c"} {
		_, err = client.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String(table),
			Item:      map[string]ddbtypes.AttributeValue{"pk": &ddbtypes.AttributeValueMemberS{Value: v}},
		})
		require.NoError(t, err)
	}

	seen := map[string]bool{}
	pager := dynamodb.NewScanPaginator(client, &dynamodb.ScanInput{
		TableName: aws.String(table),
		Limit:     aws.Int32(1),
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, item := range page.Items {
			if pk, ok := item["pk"].(*ddbtypes.AttributeValueMemberS); ok {
				seen[pk.Value] = true
			}
		}
	}
	assert.Equal(t, map[string]bool{"a": true, "b": true, "c": true}, seen, "all items should be visible via paginated scan")
}

func TestDDB_ListTables_Pagination(t *testing.T) {
	client := ddbClient()
	names := []string{"pag-tbl-a", "pag-tbl-b", "pag-tbl-c"}
	for _, n := range names {
		_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
			TableName:   aws.String(n),
			BillingMode: ddbtypes.BillingModePayPerRequest,
			AttributeDefinitions: []ddbtypes.AttributeDefinition{
				{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			},
			KeySchema: []ddbtypes.KeySchemaElement{
				{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash},
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { client.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(n)}) })
	}

	seen := map[string]bool{}
	pager := dynamodb.NewListTablesPaginator(client, &dynamodb.ListTablesInput{Limit: aws.Int32(1)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, n := range page.TableNames {
			seen[n] = true
		}
	}
	for _, n := range names {
		assert.True(t, seen[n], "table %s should appear via pagination", n)
	}
}

func TestDDB_GetItem_TableNotFound_ErrorClassification(t *testing.T) {
	client := ddbClient()
	_, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String("nonexistent-table"),
		Key: map[string]ddbtypes.AttributeValue{
			"pk": &ddbtypes.AttributeValueMemberS{Value: "k"},
		},
	})
	require.Error(t, err)
	var notFound *ddbtypes.ResourceNotFoundException
	assert.True(t, errors.As(err, &notFound),
		"DynamoDB ResourceNotFoundException must be classified by SDK errors.As; got %T: %v", err, err)
}
