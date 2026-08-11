package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
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

// TestDynamoDB_UpdateItemExpression exercises UpdateExpression — the path every
// modern client uses (the legacy AttributeUpdates path was the only one
// implemented, so update-item with an UpdateExpression was a silent no-op).
func TestDynamoDB_UpdateItemExpression(t *testing.T) {
	c := ddbClient()
	ddbCoverageTable(t, c, "cov-update-expr")
	get := func() map[string]ddbtypes.AttributeValue {
		out, err := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String("cov-update-expr"), Key: ddbKey("a")})
		require.NoError(t, err)
		return out.Item
	}
	num := func(item map[string]ddbtypes.AttributeValue, attr string) string {
		v, ok := item[attr].(*ddbtypes.AttributeValueMemberN)
		require.Truef(t, ok, "attr %s is a number", attr)
		return v.Value
	}

	// SET assignment.
	_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String("cov-update-expr"),
		Key:                       ddbKey("a"),
		UpdateExpression:          aws.String("SET #c = :v, #s = :n"),
		ExpressionAttributeNames:  map[string]string{"#c": "cnt", "#s": "label"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":v": &ddbtypes.AttributeValueMemberN{Value: "5"}, ":n": &ddbtypes.AttributeValueMemberS{Value: "hi"}},
	})
	require.NoError(t, err)
	item := get()
	assert.Equal(t, "5", num(item, "cnt"))
	assert.Equal(t, "hi", item["label"].(*ddbtypes.AttributeValueMemberS).Value)

	// SET arithmetic increment.
	_, err = c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String("cov-update-expr"), Key: ddbKey("a"),
		UpdateExpression:          aws.String("SET #c = #c + :i"),
		ExpressionAttributeNames:  map[string]string{"#c": "cnt"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":i": &ddbtypes.AttributeValueMemberN{Value: "3"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "8", num(get(), "cnt"))

	// ADD on a missing attribute starts from 0; if_not_exists keeps the existing value.
	_, err = c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String("cov-update-expr"), Key: ddbKey("a"),
		UpdateExpression:          aws.String("ADD #v :d SET #c = if_not_exists(#c, :z)"),
		ExpressionAttributeNames:  map[string]string{"#v": "views", "#c": "cnt"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":d": &ddbtypes.AttributeValueMemberN{Value: "10"}, ":z": &ddbtypes.AttributeValueMemberN{Value: "99"}},
	})
	require.NoError(t, err)
	item = get()
	assert.Equal(t, "10", num(item, "views"))
	assert.Equal(t, "8", num(item, "cnt"), "if_not_exists keeps the existing value")

	// REMOVE drops the attribute.
	_, err = c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String("cov-update-expr"), Key: ddbKey("a"),
		UpdateExpression:         aws.String("REMOVE #s"),
		ExpressionAttributeNames: map[string]string{"#s": "label"},
	})
	require.NoError(t, err)
	_, present := get()["label"]
	assert.False(t, present, "REMOVE drops the attribute")
}

func TestDynamoDB_TimeToLiveAndContinuousBackups(t *testing.T) {
	c := ddbClient()
	ddbCoverageTable(t, c, "cov-ttl-pitr")

	_, err := c.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName: aws.String("cov-ttl-pitr"),
		TimeToLiveSpecification: &ddbtypes.TimeToLiveSpecification{
			Enabled: aws.Bool(true), AttributeName: aws.String("ttl"),
		},
	})
	require.NoError(t, err)
	ttl, err := c.DescribeTimeToLive(ctx, &dynamodb.DescribeTimeToLiveInput{TableName: aws.String("cov-ttl-pitr")})
	require.NoError(t, err)
	assert.Equal(t, ddbtypes.TimeToLiveStatusEnabled, ttl.TimeToLiveDescription.TimeToLiveStatus)
	assert.Equal(t, "ttl", aws.ToString(ttl.TimeToLiveDescription.AttributeName))

	_, err = c.UpdateContinuousBackups(ctx, &dynamodb.UpdateContinuousBackupsInput{
		TableName:                        aws.String("cov-ttl-pitr"),
		PointInTimeRecoverySpecification: &ddbtypes.PointInTimeRecoverySpecification{PointInTimeRecoveryEnabled: aws.Bool(true)},
	})
	require.NoError(t, err)
	cb, err := c.DescribeContinuousBackups(ctx, &dynamodb.DescribeContinuousBackupsInput{TableName: aws.String("cov-ttl-pitr")})
	require.NoError(t, err)
	assert.Equal(t, ddbtypes.PointInTimeRecoveryStatusEnabled,
		cb.ContinuousBackupsDescription.PointInTimeRecoveryDescription.PointInTimeRecoveryStatus)
}

// TestDynamoDB_UpdateItemMalformedExpression verifies that a malformed
// UpdateExpression is rejected with a ValidationException rather than crashing
// the service. The unterminated-if_not_exists( forms previously panicked the
// sim process with "slice bounds out of range".
func TestDynamoDB_UpdateItemMalformedExpression(t *testing.T) {
	c := ddbClient()
	ddbCoverageTable(t, c, "cov-update-malformed")

	for _, expr := range []string{
		"SET a = if_not_exists(",
		"SET =if_not_exists(",
		"SET a = if_not_exists(a",
	} {
		_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName:        aws.String("cov-update-malformed"),
			Key:              ddbKey("m"),
			UpdateExpression: aws.String(expr),
		})
		require.Error(t, err, "malformed expression %q must be rejected", expr)
	}

	// A clause keyword preceded by an invalid-UTF-8 byte previously panicked the
	// process (ddbSplitUpdateClauses sliced expr with strings.ToUpper indices).
	// It must no longer crash — whether it is accepted or rejected, the service
	// must keep responding (verified by the well-formed call below).
	_, _ = c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        aws.String("cov-update-malformed"),
		Key:              ddbKey("m"),
		UpdateExpression: aws.String("\xcaREMOVE"),
	})

	// The service is still alive after the malformed requests: a well-formed
	// UpdateItem still succeeds.
	_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String("cov-update-malformed"),
		Key:                       ddbKey("m"),
		UpdateExpression:          aws.String("SET #s = :v"),
		ExpressionAttributeNames:  map[string]string{"#s": "label"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":v": &ddbtypes.AttributeValueMemberS{Value: "ok"}},
	})
	require.NoError(t, err)
}
