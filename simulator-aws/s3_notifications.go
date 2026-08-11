package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// S3 event notifications: on a successful object create/remove, S3 fires the
// bucket's stored NotificationConfiguration to every configured target (SQS
// queue, SNS topic, Lambda function) whose Event list matches the event type.
//
// Each delivery is authorized against the TARGET's resource-based policy under
// the AWS-service-initiated IAM context (s3.amazonaws.com originating from the
// bucket ARN). A target that doesn't admit S3 is dropped, exactly as real AWS
// silently drops an unauthorized delivery — there is no error back to the
// PutObject/DeleteObject caller.
//
// Real-S3 reference:
//   https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-content-structure.html
//   https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketNotificationConfiguration.html

// s3NotificationConfiguration mirrors the wire shape of the
// NotificationConfiguration document S3 stores under `?notification`. The
// element names (Queue / Topic / CloudFunction, Event) are the AWS S3 REST API
// wire names — the same the aws-sdk-go-v2 serializer emits.
type s3NotificationConfiguration struct {
	XMLName               xml.Name                  `xml:"NotificationConfiguration"`
	QueueConfigurations   []s3QueueNotification     `xml:"QueueConfiguration"`
	TopicConfigurations   []s3TopicNotification     `xml:"TopicConfiguration"`
	LambdaConfigurations  []s3LambdaNotification    `xml:"CloudFunctionConfiguration"`
	LambdaConfigurations2 []s3LambdaNotificationAlt `xml:"LambdaFunctionConfiguration"`
}

type s3QueueNotification struct {
	ID     string   `xml:"Id"`
	Queue  string   `xml:"Queue"`
	Events []string `xml:"Event"`
}

type s3TopicNotification struct {
	ID     string   `xml:"Id"`
	Topic  string   `xml:"Topic"`
	Events []string `xml:"Event"`
}

// s3LambdaNotification is the canonical S3 wire shape
// (CloudFunctionConfiguration / CloudFunction).
type s3LambdaNotification struct {
	ID            string   `xml:"Id"`
	CloudFunction string   `xml:"CloudFunction"`
	Events        []string `xml:"Event"`
}

// s3LambdaNotificationAlt accepts the LambdaFunctionConfiguration /
// LambdaFunctionArn spelling some clients/docs use as an alias for the same
// Lambda target.
type s3LambdaNotificationAlt struct {
	ID            string   `xml:"Id"`
	CloudFunction string   `xml:"LambdaFunctionArn"`
	Events        []string `xml:"Event"`
}

// s3EventMatches reports whether a configured event filter (e.g.
// "s3:ObjectCreated:*" or "s3:ObjectCreated:Put") matches the concrete event
// name that occurred (e.g. "s3:ObjectCreated:Put"). A trailing "*" on the
// configured event matches any specifier within the same category; otherwise
// the match is exact.
func s3EventMatches(configured, occurred string) bool {
	if configured == occurred {
		return true
	}
	if strings.HasSuffix(configured, ":*") {
		prefix := strings.TrimSuffix(configured, "*")
		return strings.HasPrefix(occurred, prefix)
	}
	return false
}

func s3EventListMatches(configured []string, occurred string) bool {
	for _, c := range configured {
		if s3EventMatches(c, occurred) {
			return true
		}
	}
	return false
}

// s3EventNotificationJSON builds a faithful S3 event-notification record for the
// given bucket/key and concrete event name (e.g. "ObjectCreated:Put"). The shape
// matches the real S3 Records[].s3 document.
func s3EventNotificationJSON(bucket, key, eventName, etag string, size int64) string {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	record := map[string]any{
		"eventVersion": "2.1",
		"eventSource":  "aws:s3",
		"awsRegion":    awsRegion(),
		"eventTime":    now,
		"eventName":    eventName,
		"s3": map[string]any{
			"s3SchemaVersion": "1.0",
			"bucket": map[string]any{
				"name": bucket,
				"arn":  s3BucketARN(bucket),
				"ownerIdentity": map[string]any{
					"principalId": awsAccountID(),
				},
			},
			"object": map[string]any{
				"key":       key,
				"size":      size,
				"eTag":      strings.Trim(etag, `"`),
				"sequencer": fmt.Sprintf("%016X", time.Now().UnixNano()),
			},
		},
	}
	envelope := map[string]any{"Records": []any{record}}
	b, _ := json.Marshal(envelope)
	return string(b)
}

// s3FireObjectNotifications dispatches the bucket's stored NotificationConfiguration
// for one object event. eventName is the concrete S3 event name without the
// "s3:" prefix (e.g. "ObjectCreated:Put", "ObjectRemoved:Delete").
func s3FireObjectNotifications(bucket, key, eventName, etag string, size int64) {
	body, _, _, ok := getStoredBucketSubresource(bucket, "notification")
	if !ok {
		return
	}
	var cfg s3NotificationConfiguration
	if err := xml.Unmarshal(body, &cfg); err != nil {
		return
	}

	qualified := "s3:" + eventName
	eventJSON := s3EventNotificationJSON(bucket, key, eventName, etag, size)
	src := iamServiceSource{
		Service:       "s3.amazonaws.com",
		SourceArn:     s3BucketARN(bucket),
		SourceAccount: awsAccountID(),
	}

	for _, qc := range cfg.QueueConfigurations {
		if qc.Queue == "" || !s3EventListMatches(qc.Events, qualified) {
			continue
		}
		if !iamAuthorizeServiceDelivery(qc.Queue, "sqs:SendMessage", src) {
			continue
		}
		sqsEnqueueByARN(qc.Queue, eventJSON)
	}

	for _, tc := range cfg.TopicConfigurations {
		if tc.Topic == "" || !s3EventListMatches(tc.Events, qualified) {
			continue
		}
		if !iamAuthorizeServiceDelivery(tc.Topic, "sns:Publish", src) {
			continue
		}
		s3PublishToTopic(tc.Topic, eventJSON)
	}

	lambdaTargets := make([]s3LambdaNotification, 0, len(cfg.LambdaConfigurations)+len(cfg.LambdaConfigurations2))
	lambdaTargets = append(lambdaTargets, cfg.LambdaConfigurations...)
	for _, lc := range cfg.LambdaConfigurations2 {
		lambdaTargets = append(lambdaTargets, s3LambdaNotification(lc))
	}
	for _, lc := range lambdaTargets {
		if lc.CloudFunction == "" || !s3EventListMatches(lc.Events, qualified) {
			continue
		}
		if !iamAuthorizeServiceDelivery(lc.CloudFunction, "lambda:InvokeFunction", src) {
			continue
		}
		s3InvokeLambda(lc.CloudFunction, []byte(eventJSON))
	}
}

// s3PublishToTopic delivers an S3 event to an SNS topic by fanning it out to the
// topic's subscribers in-process (the same path a real SNS Publish takes). The
// caller has already authorized sns:Publish against the topic policy.
func s3PublishToTopic(topicARN, message string) {
	msgID := fmt.Sprintf("%016x", time.Now().UnixNano())
	snsFanout(topicARN, msgID, "Amazon S3 Notification", message, nil)
}

// s3InvokeLambda performs a real in-process async Lambda invoke with the S3
// event payload. The caller has already authorized lambda:InvokeFunction
// against the function policy.
func s3InvokeLambda(functionARN string, payload []byte) {
	name := functionARN
	if i := strings.LastIndex(functionARN, ":"); i >= 0 {
		name = functionARN[i+1:]
	}
	fn, ok := lambdaFunctions.Get(name)
	if !ok {
		return
	}
	go func() { _, _, _ = invokeLambdaViaRuntimeAPI(fn, payload) }()
}
