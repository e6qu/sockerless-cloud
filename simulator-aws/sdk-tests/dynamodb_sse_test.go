package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDynamoDB_SSEDescriptionRoundTrip verifies a table created with
// server-side encryption reports the full SSEDescription on DescribeTable.
func TestDynamoDB_SSEDescriptionRoundTrip(t *testing.T) {
	c := ddbClient()
	keyArn := "arn:aws:kms:us-east-1:123456789012:key/test-key"
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String("sse-table"),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
		KeySchema:            []ddbtypes.KeySchemaElement{{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash}},
		BillingMode:          ddbtypes.BillingModePayPerRequest,
		SSESpecification: &ddbtypes.SSESpecification{
			Enabled:        aws.Bool(true),
			SSEType:        ddbtypes.SSETypeKms,
			KMSMasterKeyId: aws.String(keyArn),
		},
	})
	require.NoError(t, err)

	desc, err := c.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String("sse-table")})
	require.NoError(t, err)
	require.NotNil(t, desc.Table.SSEDescription, "SSEDescription must round-trip through DescribeTable")
	assert.Equal(t, ddbtypes.SSEStatusEnabled, desc.Table.SSEDescription.Status)
	assert.Equal(t, ddbtypes.SSETypeKms, desc.Table.SSEDescription.SSEType)
	assert.Equal(t, keyArn, aws.ToString(desc.Table.SSEDescription.KMSMasterKeyArn))
}
