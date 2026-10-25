package aws_sdk_test

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func snsFeedbackRole(t *testing.T) string {
	t.Helper()
	return createServiceRole(t, uniquename.New("sns-feedback"), "sns.amazonaws.com",
		"logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents", "logs:PutMetricFilter", "logs:PutRetentionPolicy")
}

func snsTopicMetricSum(t *testing.T, topicName, metric string, since time.Time) float64 {
	t.Helper()
	stats, err := cloudwatchClient().GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
		Namespace:  aws.String("AWS/SNS"),
		MetricName: aws.String(metric),
		Dimensions: []cwtypes.Dimension{{Name: aws.String("TopicName"), Value: aws.String(topicName)}},
		StartTime:  aws.Time(since),
		EndTime:    aws.Time(time.Now().UTC().Add(time.Minute)),
		Period:     aws.Int32(300),
		Statistics: []cwtypes.Statistic{cwtypes.StatisticSum},
	})
	require.NoError(t, err)
	var sum float64
	for _, point := range stats.Datapoints {
		sum += aws.ToFloat64(point.Sum)
	}
	return sum
}

func TestSNS_DeliveryStatusAttributesRoundTripAndValidate(t *testing.T) {
	t.Parallel()
	snsC := snsClient()
	role := snsFeedbackRole(t)

	topic, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{
		Name: aws.String(uniquename.New("sns-feedback-attrs")),
		Attributes: map[string]string{
			"SQSSuccessFeedbackRoleArn":    role,
			"SQSSuccessFeedbackSampleRate": "40",
			"SQSFailureFeedbackRoleArn":    role,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: topic.TopicArn}) })

	for name, value := range map[string]string{
		"LambdaSuccessFeedbackRoleArn":    role,
		"LambdaSuccessFeedbackSampleRate": "100",
		"LambdaFailureFeedbackRoleArn":    role,
		"HTTPFailureFeedbackRoleArn":      role,
		"FirehoseSuccessFeedbackRoleArn":  role,
	} {
		_, err = snsC.SetTopicAttributes(ctx, &sns.SetTopicAttributesInput{
			TopicArn: topic.TopicArn, AttributeName: aws.String(name), AttributeValue: aws.String(value),
		})
		require.NoError(t, err, name)
	}
	attrs, err := snsC.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: topic.TopicArn})
	require.NoError(t, err)
	assert.Equal(t, role, attrs.Attributes["SQSSuccessFeedbackRoleArn"])
	assert.Equal(t, "40", attrs.Attributes["SQSSuccessFeedbackSampleRate"])
	assert.Equal(t, role, attrs.Attributes["LambdaFailureFeedbackRoleArn"])
	assert.Equal(t, "100", attrs.Attributes["LambdaSuccessFeedbackSampleRate"])
	assert.Equal(t, role, attrs.Attributes["HTTPFailureFeedbackRoleArn"])
	assert.Equal(t, role, attrs.Attributes["FirehoseSuccessFeedbackRoleArn"])

	for name, value := range map[string]string{
		"LambdaSuccessFeedbackSampleRate": "101",
		"SQSSuccessFeedbackSampleRate":    "half",
		"LambdaFailureFeedbackRoleArn":    "not-a-role",
		"HTTPSuccessFeedbackRoleArn":      "arn:aws:iam::123456789012:user/someone",
	} {
		_, err = snsC.SetTopicAttributes(ctx, &sns.SetTopicAttributesInput{
			TopicArn: topic.TopicArn, AttributeName: aws.String(name), AttributeValue: aws.String(value),
		})
		var invalid *snstypes.InvalidParameterException
		require.True(t, errors.As(err, &invalid), "%s=%s: %v", name, value, err)
	}
	_, err = snsC.CreateTopic(ctx, &sns.CreateTopicInput{
		Name:       aws.String(uniquename.New("sns-feedback-bad")),
		Attributes: map[string]string{"SQSSuccessFeedbackSampleRate": "-1"},
	})
	var invalid *snstypes.InvalidParameterException
	require.True(t, errors.As(err, &invalid), "%v", err)

	_, err = snsC.SetTopicAttributes(ctx, &sns.SetTopicAttributesInput{
		TopicArn: topic.TopicArn, AttributeName: aws.String("LambdaFailureFeedbackRoleArn"), AttributeValue: aws.String(""),
	})
	require.NoError(t, err)
	attrs, err = snsC.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: topic.TopicArn})
	require.NoError(t, err)
	assert.NotContains(t, attrs.Attributes, "LambdaFailureFeedbackRoleArn")
}

// TestSNS_SQSDeliveryStatusLogsAndMetrics delivers one message to a queue that
// admits the topic and one to a queue that does not, and reads each outcome
// back from the topic's delivery status log groups and its AWS/SNS metrics.
func TestSNS_SQSDeliveryStatusLogsAndMetrics(t *testing.T) {
	t.Parallel()
	since := time.Now().UTC().Add(-time.Minute)
	snsC := snsClient()
	sqsC := sqsClient()
	logs := cwLogsClient()
	role := snsFeedbackRole(t)

	topicName := uniquename.New("sns-status-sqs")
	topic, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{
		Name: aws.String(topicName),
		Attributes: map[string]string{
			"SQSSuccessFeedbackRoleArn":    role,
			"SQSSuccessFeedbackSampleRate": "100",
			"SQSFailureFeedbackRoleArn":    role,
		},
	})
	require.NoError(t, err)
	topicARN := aws.ToString(topic.TopicArn)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: topic.TopicArn}) })
	successGroup := snsDeliveryStatusLogGroup(topicARN)
	failureGroup := successGroup + "/Failure"
	t.Cleanup(func() {
		for _, group := range []string{successGroup, failureGroup} {
			_, _ = logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
		}
	})

	admitted, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(uniquename.New("sns-status-ok"))})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: admitted.QueueUrl}) })
	admittedARN := queueARNOf(t, sqsC, admitted.QueueUrl)
	setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(admitted.QueueUrl), admittedARN, topicARN)

	refused, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(uniquename.New("sns-status-deny"))})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: refused.QueueUrl}) })
	refusedARN := queueARNOf(t, sqsC, refused.QueueUrl)

	for _, endpoint := range []string{admittedARN, refusedARN} {
		_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{TopicArn: topic.TopicArn, Protocol: aws.String("sqs"), Endpoint: aws.String(endpoint)})
		require.NoError(t, err)
	}

	message := "delivery-status-payload"
	published, err := snsC.Publish(ctx, &sns.PublishInput{TopicArn: topic.TopicArn, Message: aws.String(message)})
	require.NoError(t, err)
	messageID := aws.ToString(published.MessageId)

	success := snsAwaitDeliveryStatus(t, logs, successGroup, messageID)
	assert.Equal(t, "SUCCESS", success.Status)
	assert.Equal(t, topicARN, success.Notification.TopicArn)
	sum := md5.Sum([]byte(message))
	assert.Equal(t, hex.EncodeToString(sum[:]), success.Notification.MessageMD5Sum)
	assert.Equal(t, admittedARN, success.Delivery.Destination)
	assert.Equal(t, 200, success.Delivery.StatusCode)
	assert.Equal(t, 1, success.Delivery.Attempts)
	assert.Contains(t, success.Delivery.ProviderResponse, "sqsMessageId")
	assert.NotEmpty(t, success.Delivery.DeliveryID)

	failure := snsAwaitDeliveryStatus(t, logs, failureGroup, messageID)
	assert.Equal(t, "FAILURE", failure.Status)
	assert.Equal(t, refusedARN, failure.Delivery.Destination)
	assert.Equal(t, 403, failure.Delivery.StatusCode)
	assert.Contains(t, failure.Delivery.ProviderResponse, "AccessDenied")

	assert.Equal(t, float64(1), snsTopicMetricSum(t, topicName, "NumberOfMessagesPublished", since))
	assert.Equal(t, float64(len(message)), snsTopicMetricSum(t, topicName, "PublishSize", since))
	assert.Equal(t, float64(1), snsTopicMetricSum(t, topicName, "NumberOfNotificationsDelivered", since))
	assert.Equal(t, float64(1), snsTopicMetricSum(t, topicName, "NumberOfNotificationsFailed", since))
}
