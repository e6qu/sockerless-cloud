package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDynamoDB_ExpressionsFailLoud expects a malformed expression, or one naming
// an undefined #name or :value, to raise ValidationException rather than return
// a plausible empty result or flip a condition.
func TestDynamoDB_ExpressionsFailLoud(t *testing.T) {
	t.Parallel()
	c := ddbClient()
	table := "failloud-exprs"

	defer c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)})
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(table),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)

	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(table),
		Item:      map[string]ddbtypes.AttributeValue{"PK": &ddbtypes.AttributeValueMemberS{Value: "row1"}},
	})
	require.NoError(t, err)

	isValidation := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err, "expected a loud error, not a silent success")
		var ae smithy.APIError
		require.True(t, errors.As(err, &ae), "expected smithy.APIError, got %v", err)
		assert.Equal(t, "ValidationException", ae.ErrorCode())
	}

	// The KeyConditionExpression ends in a dangling comparator.
	_, err = c.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(table),
		KeyConditionExpression: aws.String("PK ="),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":v": &ddbtypes.AttributeValueMemberS{Value: "row1"},
		},
	})
	isValidation(t, err)

	// An undefined :value would otherwise match nothing and read as "no data".
	_, err = c.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(table),
		FilterExpression: aws.String("PK = :missing"),
	})
	isValidation(t, err)

	_, err = c.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(table),
		FilterExpression: aws.String("#undef = :v"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":v": &ddbtypes.AttributeValueMemberS{Value: "x"},
		},
	})
	isValidation(t, err)

	_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(table),
		Item:                map[string]ddbtypes.AttributeValue{"PK": &ddbtypes.AttributeValueMemberS{Value: "row2"}},
		ConditionExpression: aws.String("attribute_exists("),
	})
	isValidation(t, err)

	// A well-formed FilterExpression that matches nothing returns zero items
	// without error.
	scan, err := c.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(table),
		FilterExpression: aws.String("PK = :v"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":v": &ddbtypes.AttributeValueMemberS{Value: "no-such-row"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), scan.Count)
}
