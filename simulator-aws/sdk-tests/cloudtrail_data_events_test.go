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

// TestCloudTrail_NoDynamoDataEventsInLookup: DynamoDB item operations are data
// events and stay out of LookupEvents; CreateTable, a management event, does
// not.
func TestCloudTrail_NoDynamoDataEventsInLookup(t *testing.T) {
	t.Parallel()
	c := ddbClient()
	ct := cloudTrailClient()
	tbl := "ct-data-events"
	ddbSimpleTable(t, c, tbl)
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

// TestCloudTrail_NoPhantomListBuckets: an unauthenticated GET / (a container
// healthcheck, which Amazon S3 routes to ListBuckets) records no event.
func TestCloudTrail_NoPhantomListBuckets(t *testing.T) {
	t.Parallel()
	ct := cloudTrailClient()
	before := len(ctLookup(t, ct, cttypes.LookupAttributeKeyEventName, "ListBuckets"))
	for i := 0; i < 3; i++ {
		resp, err := http.Get(baseURL + "/")
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
	after := len(ctLookup(t, ct, cttypes.LookupAttributeKeyEventName, "ListBuckets"))
	assert.Equal(t, before, after, "unauthenticated GET / must not record phantom ListBuckets events")
}

// TestCloudTrail_NoCrossServiceDataEvents: the data/management split holds
// across services, so SQS SendMessage stays out of LookupEvents while
// CreateQueue does not.
func TestCloudTrail_NoCrossServiceDataEvents(t *testing.T) {
	t.Parallel()
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
