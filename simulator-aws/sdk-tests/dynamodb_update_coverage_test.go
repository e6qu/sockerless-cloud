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

func ddbCoverageTable(t *testing.T, c *dynamodb.Client, name string) {
	t.Helper()
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(name),
		BillingMode:          ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
		KeySchema:            []ddbtypes.KeySchemaElement{{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash}},
	})
	require.NoError(t, err)
}

func ddbKey(v string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{"PK": &ddbtypes.AttributeValueMemberS{Value: v}}
}

// TestDynamoDB_UpdateItemExpression covers UpdateExpression, the path every
// current client uses.
func TestDynamoDB_UpdateItemExpression(t *testing.T) {
	t.Parallel()
	table := uniquename.New("cov-update-expr")
	c := ddbClient()
	ddbCoverageTable(t, c, table)
	get := func() map[string]ddbtypes.AttributeValue {
		out, err := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table), Key: ddbKey("a")})
		require.NoError(t, err)
		return out.Item
	}
	num := func(item map[string]ddbtypes.AttributeValue, attr string) string {
		v, ok := item[attr].(*ddbtypes.AttributeValueMemberN)
		require.Truef(t, ok, "attr %s is a number", attr)
		return v.Value
	}

	_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(table),
		Key:                       ddbKey("a"),
		UpdateExpression:          aws.String("SET #c = :v, #s = :n"),
		ExpressionAttributeNames:  map[string]string{"#c": "cnt", "#s": "label"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":v": &ddbtypes.AttributeValueMemberN{Value: "5"}, ":n": &ddbtypes.AttributeValueMemberS{Value: "hi"}},
	})
	require.NoError(t, err)
	item := get()
	assert.Equal(t, "5", num(item, "cnt"))
	assert.Equal(t, "hi", item["label"].(*ddbtypes.AttributeValueMemberS).Value)

	_, err = c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(table), Key: ddbKey("a"),
		UpdateExpression:          aws.String("SET #c = #c + :i"),
		ExpressionAttributeNames:  map[string]string{"#c": "cnt"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":i": &ddbtypes.AttributeValueMemberN{Value: "3"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "8", num(get(), "cnt"))

	// ADD on a missing attribute starts from 0; if_not_exists keeps an existing
	// value.
	_, err = c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(table), Key: ddbKey("a"),
		UpdateExpression:          aws.String("ADD #v :d SET #c = if_not_exists(#c, :z)"),
		ExpressionAttributeNames:  map[string]string{"#v": "views", "#c": "cnt"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":d": &ddbtypes.AttributeValueMemberN{Value: "10"}, ":z": &ddbtypes.AttributeValueMemberN{Value: "99"}},
	})
	require.NoError(t, err)
	item = get()
	assert.Equal(t, "10", num(item, "views"))
	assert.Equal(t, "8", num(item, "cnt"), "if_not_exists keeps the existing value")

	_, err = c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(table), Key: ddbKey("a"),
		UpdateExpression:         aws.String("REMOVE #s"),
		ExpressionAttributeNames: map[string]string{"#s": "label"},
	})
	require.NoError(t, err)
	_, present := get()["label"]
	assert.False(t, present, "REMOVE drops the attribute")
}

func TestDynamoDB_TimeToLiveAndContinuousBackups(t *testing.T) {
	t.Parallel()
	table := uniquename.New("cov-ttl-pitr")
	c := ddbClient()
	ddbCoverageTable(t, c, table)

	_, err := c.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName: aws.String(table),
		TimeToLiveSpecification: &ddbtypes.TimeToLiveSpecification{
			Enabled: aws.Bool(true), AttributeName: aws.String("ttl"),
		},
	})
	require.NoError(t, err)
	ttl, err := c.DescribeTimeToLive(ctx, &dynamodb.DescribeTimeToLiveInput{TableName: aws.String(table)})
	require.NoError(t, err)
	assert.Equal(t, ddbtypes.TimeToLiveStatusEnabled, ttl.TimeToLiveDescription.TimeToLiveStatus)
	assert.Equal(t, "ttl", aws.ToString(ttl.TimeToLiveDescription.AttributeName))

	_, err = c.UpdateContinuousBackups(ctx, &dynamodb.UpdateContinuousBackupsInput{
		TableName:                        aws.String(table),
		PointInTimeRecoverySpecification: &ddbtypes.PointInTimeRecoverySpecification{PointInTimeRecoveryEnabled: aws.Bool(true)},
	})
	require.NoError(t, err)
	cb, err := c.DescribeContinuousBackups(ctx, &dynamodb.DescribeContinuousBackupsInput{TableName: aws.String(table)})
	require.NoError(t, err)
	assert.Equal(t, ddbtypes.PointInTimeRecoveryStatusEnabled,
		cb.ContinuousBackupsDescription.PointInTimeRecoveryDescription.PointInTimeRecoveryStatus)
}

// TestDynamoDB_UpdateItemMalformedExpression expects a malformed
// UpdateExpression, such as an unterminated if_not_exists(, to fail with
// ValidationException while the service keeps running.
func TestDynamoDB_UpdateItemMalformedExpression(t *testing.T) {
	t.Parallel()
	table := uniquename.New("cov-update-malformed")
	c := ddbClient()
	ddbCoverageTable(t, c, table)

	for _, expr := range []string{
		"SET a = if_not_exists(",
		"SET =if_not_exists(",
		"SET a = if_not_exists(a",
	} {
		_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName:        aws.String(table),
			Key:              ddbKey("m"),
			UpdateExpression: aws.String(expr),
		})
		requireAWSErrorCode(t, err, "ValidationException")
	}

	// A clause keyword after an invalid UTF-8 byte may be accepted or refused, but
	// the service keeps responding, as the well-formed call below shows.
	_, _ = c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        aws.String(table),
		Key:              ddbKey("m"),
		UpdateExpression: aws.String("\xcaREMOVE"),
	})

	_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(table),
		Key:                       ddbKey("m"),
		UpdateExpression:          aws.String("SET #s = :v"),
		ExpressionAttributeNames:  map[string]string{"#s": "label"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":v": &ddbtypes.AttributeValueMemberS{Value: "ok"}},
	})
	require.NoError(t, err)
}
