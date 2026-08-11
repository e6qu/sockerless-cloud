package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ddbSortTable creates a hash(pk:S)+range(sk:N) table.
func ddbSortTable(t *testing.T, c *dynamodb.Client, name string) {
	t.Helper()
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(name),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: ddbtypes.ScalarAttributeTypeN},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: ddbtypes.KeyTypeRange},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(name)}) })
}

// TestDDBQueryScanIndexForwardAndCount covers ScanIndexForward (descending) and
// Select=COUNT.
func TestDDBQueryScanIndexForwardAndCount(t *testing.T) {
	c := ddbClient()
	tbl := "sweep-query"
	ddbSortTable(t, c, tbl)
	for _, sk := range []string{"1", "2", "3"} {
		_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tbl),
			Item: map[string]ddbtypes.AttributeValue{"pk": sS("p"), "sk": sN(sk)}})
		require.NoError(t, err)
	}
	desc, err := c.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(tbl),
		KeyConditionExpression:    aws.String("pk = :p"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":p": sS("p")},
		ScanIndexForward:          aws.Bool(false),
	})
	require.NoError(t, err)
	require.Len(t, desc.Items, 3)
	assert.Equal(t, "3", desc.Items[0]["sk"].(*ddbtypes.AttributeValueMemberN).Value, "ScanIndexForward=false → descending")
	assert.Equal(t, "1", desc.Items[2]["sk"].(*ddbtypes.AttributeValueMemberN).Value)

	count, err := c.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(tbl),
		KeyConditionExpression:    aws.String("pk = :p"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":p": sS("p")},
		Select:                    ddbtypes.SelectCount,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), count.Count)
	assert.Empty(t, count.Items, "Select=COUNT omits Items")
}

// TestDDBNumericKeyCanonicalization — "01" and "1" address the same item.
func TestDDBNumericKeyCanonicalization(t *testing.T) {
	c := ddbClient()
	tbl := "sweep-numkey"
	ddbSortTable(t, c, tbl)
	_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tbl),
		Item: map[string]ddbtypes.AttributeValue{"pk": sS("p"), "sk": sN("01"), "v": sS("first")}})
	require.NoError(t, err)
	got, err := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(tbl),
		Key: map[string]ddbtypes.AttributeValue{"pk": sS("p"), "sk": sN("1")}})
	require.NoError(t, err)
	require.NotEmpty(t, got.Item, "GetItem with '1' must find the item Put with '01'")
	assert.Equal(t, "first", got.Item["v"].(*ddbtypes.AttributeValueMemberS).Value)
}

// TestDDBNestedSetAndContains covers nested SET (map path) + contains() on a set.
func TestDDBNestedSetAndContains(t *testing.T) {
	c := ddbClient()
	tbl := "sweep-nested"
	ddbSimpleTable(t, c, tbl)
	_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                aws.String(tbl),
		Key:                      map[string]ddbtypes.AttributeValue{"pk": sS("x")},
		UpdateExpression:         aws.String("SET #p.#a = :v ADD tags :t"),
		ExpressionAttributeNames: map[string]string{"#p": "profile", "#a": "age"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":v": sN("42"),
			":t": &ddbtypes.AttributeValueMemberSS{Value: []string{"admin"}},
		},
	})
	require.NoError(t, err)
	got, _ := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(tbl), Key: map[string]ddbtypes.AttributeValue{"pk": sS("x")}})
	prof, ok := got.Item["profile"].(*ddbtypes.AttributeValueMemberM)
	require.True(t, ok, "nested SET must create the map: %#v", got.Item["profile"])
	assert.Equal(t, "42", prof.Value["age"].(*ddbtypes.AttributeValueMemberN).Value)

	// contains() over the SS we ADDed.
	q, err := c.Scan(ctx, &dynamodb.ScanInput{
		TableName:                 aws.String(tbl),
		FilterExpression:          aws.String("contains(tags, :a)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":a": sS("admin")},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), q.Count, "contains() must match set membership")
}

// TestDDBItemDepthLimit — an item nested past 32 levels is rejected.
func TestDDBItemDepthLimit(t *testing.T) {
	c := ddbClient()
	tbl := "sweep-depth"
	ddbSimpleTable(t, c, tbl)
	// Build a 40-level nested map AttributeValue.
	av := sS("leaf")
	for i := 0; i < 40; i++ {
		av = &ddbtypes.AttributeValueMemberM{Value: map[string]ddbtypes.AttributeValue{"n": av}}
	}
	_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tbl),
		Item: map[string]ddbtypes.AttributeValue{"pk": sS("deep"), "d": av}})
	require.Error(t, err, "an item nested past 32 levels must be rejected")
	assert.True(t, strings.Contains(err.Error(), "ValidationException") || strings.Contains(err.Error(), "nesting"))
}

// TestDDBParallelScanDisjoint — TotalSegments partitions the table with no overlap.
func TestDDBParallelScanDisjoint(t *testing.T) {
	c := ddbClient()
	tbl := "sweep-parscan"
	ddbSimpleTable(t, c, tbl)
	for i := 0; i < 10; i++ {
		_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tbl),
			Item: map[string]ddbtypes.AttributeValue{"pk": sS(string(rune('a' + i)))}})
		require.NoError(t, err)
	}
	seen := map[string]bool{}
	total := int32(0)
	for seg := 0; seg < 3; seg++ {
		out, err := c.Scan(ctx, &dynamodb.ScanInput{
			TableName: aws.String(tbl), Segment: aws.Int32(int32(seg)), TotalSegments: aws.Int32(3),
		})
		require.NoError(t, err)
		total += out.Count
		for _, it := range out.Items {
			k := it["pk"].(*ddbtypes.AttributeValueMemberS).Value
			assert.False(t, seen[k], "item %s returned by more than one segment", k)
			seen[k] = true
		}
	}
	assert.Equal(t, int32(10), total, "the union of all segments is the whole table")
}
