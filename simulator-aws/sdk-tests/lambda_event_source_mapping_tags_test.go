package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An event source mapping carries its own tags, which CreateEventSourceMapping
// sets and ListTags, TagResource and UntagResource address by its ARN.
func TestLambda_EventSourceMappingTags(t *testing.T) {
	t.Parallel()
	lc := lambdaClient()
	sqsC := sqsClient()
	const fn = "esm-tags-fn"
	lambdaExtrasFunc(t, lc, fn)
	queue, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("esm-tags-queue")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl}) })

	mapping, err := lc.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{
		FunctionName:   aws.String(fn),
		EventSourceArn: aws.String(queueARNOf(t, sqsC, queue.QueueUrl)),
		Enabled:        aws.Bool(false),
		Tags:           map[string]string{"team": "blue"},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteEventSourceMapping(ctx, &lambda.DeleteEventSourceMappingInput{UUID: mapping.UUID})
	})
	arn := mapping.EventSourceMappingArn

	listed, err := lc.ListTags(ctx, &lambda.ListTagsInput{Resource: arn})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"team": "blue"}, listed.Tags)

	_, err = lc.TagResource(ctx, &lambda.TagResourceInput{Resource: arn, Tags: map[string]string{"env": "test"}})
	require.NoError(t, err)
	_, err = lc.UntagResource(ctx, &lambda.UntagResourceInput{Resource: arn, TagKeys: []string{"team"}})
	require.NoError(t, err)
	listed, err = lc.ListTags(ctx, &lambda.ListTagsInput{Resource: arn})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"env": "test"}, listed.Tags)
}
