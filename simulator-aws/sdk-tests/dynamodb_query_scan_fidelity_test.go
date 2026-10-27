package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDynamoDB_ScanLimitAndQueryFilter covers two rules:
//   - Scan's Limit caps the items examined (ScannedCount), not returned: a
//     filtered Scan returns Count <= ScannedCount == Limit and a
//     LastEvaluatedKey to resume.
//   - Query applies a FilterExpression after the key condition.
func TestDynamoDB_ScanLimitAndQueryFilter(t *testing.T) {
	t.Parallel()
	queryTable := uniquename.New("qs-query")
	scanTable := uniquename.New("qs-scan")
	c := ddbClient()

	// A hash-only table of 4 items (a, b, c, d), 2 of them kind=x.
	ddbCoverageTable(t, c, scanTable)
	for _, it := range []struct{ pk, kind string }{{"a", "x"}, {"b", "y"}, {"c", "x"}, {"d", "y"}} {
		_, err := c.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String(scanTable),
			Item: map[string]ddbtypes.AttributeValue{
				"PK":   &ddbtypes.AttributeValueMemberS{Value: it.pk},
				"kind": &ddbtypes.AttributeValueMemberS{Value: it.kind},
			},
		})
		require.NoError(t, err)
	}
	scan, err := c.Scan(ctx, &dynamodb.ScanInput{
		TableName:                 aws.String(scanTable),
		Limit:                     aws.Int32(2),
		FilterExpression:          aws.String("kind = :k"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":k": &ddbtypes.AttributeValueMemberS{Value: "x"}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, scan.ScannedCount, "Limit caps EXAMINED items, not matches")
	assert.EqualValues(t, 1, scan.Count, "of the first 2 scanned (a,b) only a matches kind=x")
	assert.NotEmpty(t, scan.LastEvaluatedKey, "items remain beyond the Limit → resume cursor")

	// A composite key, where FilterExpression narrows the key-matched set.
	_, err = c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(queryTable),
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
			TableName: aws.String(queryTable),
			Item: map[string]ddbtypes.AttributeValue{
				"PK":   &ddbtypes.AttributeValueMemberS{Value: "p"},
				"SK":   &ddbtypes.AttributeValueMemberS{Value: it.sk},
				"kind": &ddbtypes.AttributeValueMemberS{Value: it.kind},
			},
		})
		require.NoError(t, err)
	}
	q, err := c.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(queryTable),
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
