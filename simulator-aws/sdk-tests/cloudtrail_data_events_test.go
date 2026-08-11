package aws_sdk_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cttypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudTrail_NoDynamoDataEventsInLookup (#651) — DynamoDB item-level ops are
// DATA events and must not appear in LookupEvents; CreateTable (management) must.
func TestCloudTrail_NoDynamoDataEventsInLookup(t *testing.T) {
	c := ddbClient()
	ct := cloudTrailClient()
	tbl := "ct-data-events"
	ddbSimpleTable(t, c, tbl) // CreateTable — a management event
	_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tbl),
		Item: map[string]ddbtypes.AttributeValue{"pk": sS("a")}})
	require.NoError(t, err)
	_, err = c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(tbl),
		Key: map[string]ddbtypes.AttributeValue{"pk": sS("a")}})
	require.NoError(t, err)
	_, err = c.Query(ctx, &dynamodb.QueryInput{TableName: aws.String(tbl),
		KeyConditionExpression:    aws.String("pk = :p"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":p": sS("a")}})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return ctHasEventNamed(ctLookup(t, ct, cttypes.LookupAttributeKeyEventName, "CreateTable"), "CreateTable")
	}, 10*time.Second, 500*time.Millisecond, "CreateTable (management) must appear in LookupEvents")

	for _, op := range []string{"PutItem", "GetItem", "Query", "Scan", "UpdateItem", "DeleteItem", "BatchWriteItem"} {
		assert.Empty(t, ctLookup(t, ct, cttypes.LookupAttributeKeyEventName, op),
			"%s is a DynamoDB data event and must not appear in LookupEvents", op)
	}
}

// TestCloudTrail_NoPhantomListBuckets (#650) — an unauthenticated GET / (the
// container healthcheck's shape, which the S3 slice routes to ListBuckets) must
// not generate phantom CloudTrail events.
func TestCloudTrail_NoPhantomListBuckets(t *testing.T) {
	ct := cloudTrailClient()
	before := len(ctLookup(t, ct, cttypes.LookupAttributeKeyEventName, "ListBuckets"))
	for i := 0; i < 3; i++ {
		resp, err := http.Get(baseURL + "/") // no SigV4 — unauthenticated
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
	after := len(ctLookup(t, ct, cttypes.LookupAttributeKeyEventName, "ListBuckets"))
	assert.Equal(t, before, after, "unauthenticated GET / must not record phantom ListBuckets events")
}

// TestCloudTrail_NoCrossServiceDataEvents (#652 guard) — the data/management
// classification holds across services: a client-initiated SQS SendMessage (a
// data event) is excluded from LookupEvents while CreateQueue (management) is not.
func TestCloudTrail_NoCrossServiceDataEvents(t *testing.T) {
	sqsc := sqsClient()
	ct := cloudTrailClient()
	cq, err := sqsc.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("ct-guard-queue")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsc.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: cq.QueueUrl}) })
	_, err = sqsc.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: cq.QueueUrl, MessageBody: aws.String("hi")})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return ctHasEventNamed(ctLookup(t, ct, cttypes.LookupAttributeKeyEventName, "CreateQueue"), "CreateQueue")
	}, 10*time.Second, 500*time.Millisecond, "CreateQueue (management) must appear in LookupEvents")
	assert.Empty(t, ctLookup(t, ct, cttypes.LookupAttributeKeyEventName, "SendMessage"),
		"SQS SendMessage is a data event and must not appear in LookupEvents")
}
