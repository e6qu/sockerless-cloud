package aws_sdk_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func queueArnAndURL(t *testing.T, sqsClient *sqs.Client, name string) (url, arn string) {
	t.Helper()
	create, err := sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name)})
	require.NoError(t, err)
	url = aws.ToString(create.QueueUrl)
	attrs, err := sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(url),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	arn = attrs.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
	require.NotEmpty(t, arn, "queue must expose its ARN")
	return url, arn
}

// receiveOne long-polls a queue for a single message body: S3 enqueues the
// event after the PutObject response, and the receive returns the moment it
// lands.
func receiveOne(t *testing.T, sqsClient *sqs.Client, url string, wait int32) (string, bool) {
	t.Helper()
	recv, err := sqsClient.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(url),
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     wait,
	})
	require.NoError(t, err)
	if len(recv.Messages) > 0 {
		return aws.ToString(recv.Messages[0].Body), true
	}
	return "", false
}

// TestS3_EventNotification_SQSDelivery proves end-to-end S3→SQS event delivery:
// a bucket notification config with a QueueConfiguration on s3:ObjectCreated:*
// delivers a faithful S3 event-notification record to the queue when the queue
// policy admits s3.amazonaws.com (Principal:{Service} + ArnLike SourceArn), and
// delivers nothing when the policy is absent.
func TestS3_EventNotification_SQSDelivery(t *testing.T) {
	s3c := s3Client()
	sqsc := sqsClient()

	bucket := "evt-notify-bucket"
	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	bucketArn := "arn:aws:s3:::" + bucket

	// Positive: queue WITH a policy admitting S3 from this bucket.
	allowedURL, allowedARN := queueArnAndURL(t, sqsc, "evt-allowed-q")
	policy := fmt.Sprintf(`{
      "Version": "2012-10-17",
      "Statement": [{
        "Effect": "Allow",
        "Principal": {"Service": "s3.amazonaws.com"},
        "Action": "sqs:SendMessage",
        "Resource": %q,
        "Condition": {"ArnLike": {"aws:SourceArn": %q}}
      }]
    }`, allowedARN, bucketArn)
	_, err = sqsc.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl:   aws.String(allowedURL),
		Attributes: map[string]string{string(sqstypes.QueueAttributeNamePolicy): policy},
	})
	require.NoError(t, err)

	// Negative: queue WITHOUT any policy → S3 has no permission to deliver.
	deniedURL, deniedARN := queueArnAndURL(t, sqsc, "evt-denied-q")

	_, err = s3c.PutBucketNotificationConfiguration(ctx, &s3.PutBucketNotificationConfigurationInput{
		Bucket: aws.String(bucket),
		NotificationConfiguration: &s3types.NotificationConfiguration{
			QueueConfigurations: []s3types.QueueConfiguration{
				{
					Id:       aws.String("allowed"),
					QueueArn: aws.String(allowedARN),
					Events:   []s3types.Event{"s3:ObjectCreated:*"},
				},
				{
					Id:       aws.String("denied"),
					QueueArn: aws.String(deniedARN),
					Events:   []s3types.Event{"s3:ObjectCreated:*"},
				},
			},
		},
	})
	require.NoError(t, err)

	// Trigger an ObjectCreated event.
	_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("path/to/object.txt"),
		Body:   bytes.NewReader([]byte("payload")),
	})
	require.NoError(t, err)

	// Positive: the allowed queue receives a faithful S3 event record.
	body, got := receiveOne(t, sqsc, allowedURL, 20)
	require.True(t, got, "S3 must deliver the event to the authorized queue")

	var envelope struct {
		Records []struct {
			EventName   string `json:"eventName"`
			EventSource string `json:"eventSource"`
			S3          struct {
				Bucket struct {
					Name string `json:"name"`
				} `json:"bucket"`
				Object struct {
					Key string `json:"key"`
				} `json:"object"`
			} `json:"s3"`
		} `json:"Records"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &envelope))
	require.Len(t, envelope.Records, 1)
	rec := envelope.Records[0]
	assert.Equal(t, "aws:s3", rec.EventSource)
	assert.Equal(t, "ObjectCreated:Put", rec.EventName)
	assert.Equal(t, bucket, rec.S3.Bucket.Name)
	assert.Equal(t, "path/to/object.txt", rec.S3.Object.Key)

	// Negative: the policy-less queue receives nothing. S3 dispatched both
	// destinations for the one PutObject, and the authorized one has already
	// delivered.
	_, got = receiveOne(t, sqsc, deniedURL, 1)
	assert.False(t, got, "S3 must NOT deliver to a queue whose policy does not admit it")
}
