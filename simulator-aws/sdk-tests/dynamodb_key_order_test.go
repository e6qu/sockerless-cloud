package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A query returns a partition's items in sort-key order, numbers by value:
// 9 before 10, and -2.5 before -2.
func TestDynamoDB_QueryOrdersNumericSortKeysByValue(t *testing.T) {
	client := ddbClient()
	table := "sdk-numeric-order"
	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(table),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("n"), AttributeType: ddbtypes.ScalarAttributeTypeN},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String("n"), KeyType: ddbtypes.KeyTypeRange},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)}) })
	for _, n := range []string{"10", "9", "-2", "-2.5", "0", "100", "0.5"} {
		_, err := client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: map[string]ddbtypes.AttributeValue{
			"pk": &ddbtypes.AttributeValueMemberS{Value: "p"}, "n": &ddbtypes.AttributeValueMemberN{Value: n},
		}})
		require.NoError(t, err)
	}
	out, err := client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(table),
		KeyConditionExpression:    aws.String("pk = :p"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":p": &ddbtypes.AttributeValueMemberS{Value: "p"}},
	})
	require.NoError(t, err)
	var order []string
	for _, item := range out.Items {
		order = append(order, item["n"].(*ddbtypes.AttributeValueMemberN).Value)
	}
	assert.Equal(t, []string{"-2.5", "-2", "0", "0.5", "9", "10", "100"}, order)
}
