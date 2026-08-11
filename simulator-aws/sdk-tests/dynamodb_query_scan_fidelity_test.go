package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDynamoDB_ScanLimitAndQueryFilter pins two fidelity gaps:
//   - Scan's Limit caps items EXAMINED (ScannedCount), not returned: a filtered
//     Scan returns Count <= ScannedCount == Limit, with a LastEvaluatedKey to
//     resume. (Before the fix the loop ran until Limit *matches*, inflating
//     ScannedCount and the cursor.)
//   - Query applies an optional FilterExpression after the key condition (it was
//     silently dropped — the request field didn't exist).
func TestDynamoDB_ScanLimitAndQueryFilter(t *testing.T) {
	c := ddbClient()

	// --- Scan: hash-only table, 4 items (sorted a,b,c,d), 2 match kind=x. ---
	ddbCoverageTable(t, c, "qs-scan")
	for _, it := range []struct{ pk, kind string }{{"a", "x"}, {"b", "y"}, {"c", "x"}, {"d", "y"}} {
		_, err := c.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String("qs-scan"),
			Item: map[string]ddbtypes.AttributeValue{
				"PK":   &ddbtypes.AttributeValueMemberS{Value: it.pk},
				"kind": &ddbtypes.AttributeValueMemberS{Value: it.kind},
			},
		})
		require.NoError(t, err)
	}
	scan, err := c.Scan(ctx, &dynamodb.ScanInput{
		TableName:                 aws.String("qs-scan"),
		Limit:                     aws.Int32(2),
		FilterExpression:          aws.String("kind = :k"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":k": &ddbtypes.AttributeValueMemberS{Value: "x"}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, scan.ScannedCount, "Limit caps EXAMINED items, not matches")
	assert.EqualValues(t, 1, scan.Count, "of the first 2 scanned (a,b) only a matches kind=x")
	assert.NotEmpty(t, scan.LastEvaluatedKey, "items remain beyond the Limit → resume cursor")

	// --- Query: composite key; FilterExpression narrows the key-matched set. ---
	_, err = c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String("qs-query"),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("SK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String("SK"), KeyType: ddbtypes.KeyTypeRange},
		},
	})
	require.NoError(t, err)
	for _, it := range []struct{ sk, kind string }{{"1", "x"}, {"2", "y"}, {"3", "x"}, {"4", "y"}} {
		_, err := c.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String("qs-query"),
			Item: map[string]ddbtypes.AttributeValue{
				"PK":   &ddbtypes.AttributeValueMemberS{Value: "p"},
				"SK":   &ddbtypes.AttributeValueMemberS{Value: it.sk},
				"kind": &ddbtypes.AttributeValueMemberS{Value: it.kind},
			},
		})
		require.NoError(t, err)
	}
	q, err := c.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String("qs-query"),
		KeyConditionExpression: aws.String("PK = :p"),
		FilterExpression:       aws.String("kind = :k"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":p": &ddbtypes.AttributeValueMemberS{Value: "p"},
			":k": &ddbtypes.AttributeValueMemberS{Value: "x"},
		},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, q.Count, "FilterExpression kind=x → SK 1 and 3")
	assert.EqualValues(t, 4, q.ScannedCount, "all 4 key-matched items examined before filtering")
}
