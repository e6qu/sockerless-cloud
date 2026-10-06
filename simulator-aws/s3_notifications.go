package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
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
func s3EventNotificationJSON(bucket, key, eventName, etag string, size int64, versionID string) string {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	object := map[string]any{
		"key":       key,
		"size":      size,
		"eTag":      strings.Trim(etag, `"`),
		"sequencer": fmt.Sprintf("%016X", time.Now().UnixNano()),
	}
	// The object's versionId is in the event when the bucket is versioned.
	if versionID != "" && s3VersioningStatus(bucket) != "" {
		object["versionId"] = versionID
	}
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
			"object": object,
		},
	}
	envelope := map[string]any{"Records": []any{record}}
	b, _ := json.Marshal(envelope)
	return string(b)
}

// s3FireObjectNotifications dispatches the bucket's stored NotificationConfiguration
// for one object event. eventName is the concrete S3 event name without the
// "s3:" prefix (e.g. "ObjectCreated:Put", "ObjectRemoved:Delete").
func s3FireObjectNotifications(bucket, key, eventName, etag string, size int64, versionID string) {
	body, _, _, ok := getStoredBucketSubresource(bucket, "notification")
	if !ok {
		return
	}
	var cfg s3NotificationConfiguration
	if err := xml.Unmarshal(body, &cfg); err != nil {
		return
	}

	qualified := "s3:" + eventName
	eventJSON := s3EventNotificationJSON(bucket, key, eventName, etag, size, versionID)
	src := s3NotificationSource(bucket)

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

	for _, lc := range cfg.lambdaTargets() {
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

// s3InvokeLambda invokes the function asynchronously with the S3 event, as
// Amazon S3 does, so the function's retries and destinations apply. The caller
// has already authorized lambda:InvokeFunction against the function policy.
func s3InvokeLambda(functionARN string, payload []byte) {
	fn, qualifier, ok := lambdaResolveInvocationTarget(functionARN, "")
	if !ok {
		return
	}
	lambdaInvokeAsynchronously(fn, payload, qualifier)
}

type s3DestinationRejection struct {
	ARN    string
	Reason string
}

func s3LambdaFunctionName(functionARN string) string {
	parts := strings.Split(functionARN, ":")
	if len(parts) >= 7 && parts[5] == "function" {
		return parts[6]
	}
	return parts[len(parts)-1]
}

func s3NotificationSource(bucket string) iamServiceSource {
	return iamServiceSource{
		Service:       "s3.amazonaws.com",
		SourceArn:     s3BucketARN(bucket),
		SourceAccount: awsAccountID(),
	}
}

func (cfg s3NotificationConfiguration) lambdaTargets() []s3LambdaNotification {
	targets := make([]s3LambdaNotification, 0, len(cfg.LambdaConfigurations)+len(cfg.LambdaConfigurations2))
	targets = append(targets, cfg.LambdaConfigurations...)
	for _, lc := range cfg.LambdaConfigurations2 {
		targets = append(targets, s3LambdaNotification(lc))
	}
	return targets
}

// s3ValidateNotificationDestinations checks, as PutBucketNotificationConfiguration
// does before it stores a configuration, that every destination exists and
// that its resource policy lets Amazon S3 deliver from the bucket.
func s3ValidateNotificationDestinations(bucket string, cfg s3NotificationConfiguration) []s3DestinationRejection {
	src := s3NotificationSource(bucket)
	var rejected []s3DestinationRejection
	for _, qc := range cfg.QueueConfigurations {
		if _, ok := sqsQueueByARN(qc.Queue); !ok {
			rejected = append(rejected, s3DestinationRejection{qc.Queue, "The destination queue does not exist"})
			continue
		}
		if !iamAuthorizeServiceDelivery(qc.Queue, "sqs:SendMessage", src) {
			rejected = append(rejected, s3DestinationRejection{qc.Queue, "Permissions on the destination queue do not allow S3 to publish notifications from this bucket"})
		}
	}
	for _, tc := range cfg.TopicConfigurations {
		if _, ok := snsTopics.Get(snsTopicNameFromARN(tc.Topic)); !ok {
			rejected = append(rejected, s3DestinationRejection{tc.Topic, "The destination topic does not exist"})
			continue
		}
		if !iamAuthorizeServiceDelivery(tc.Topic, "sns:Publish", src) {
			rejected = append(rejected, s3DestinationRejection{tc.Topic, "Permissions on the destination topic do not allow S3 to publish notifications from this bucket"})
		}
	}
	for _, lc := range cfg.lambdaTargets() {
		if _, ok := lambdaFunctions.Get(s3LambdaFunctionName(lc.CloudFunction)); !ok {
			rejected = append(rejected, s3DestinationRejection{lc.CloudFunction, "The destination Lambda function does not exist"})
			continue
		}
		if !iamAuthorizeServiceDelivery(lc.CloudFunction, "lambda:InvokeFunction", src) {
			rejected = append(rejected, s3DestinationRejection{lc.CloudFunction, "Not authorized to invoke function [" + lc.CloudFunction + "]"})
		}
	}
	return rejected
}

// s3DestinationValidationError writes the InvalidArgument error Amazon S3
// returns for a notification configuration it could not validate, naming each
// rejected destination in numbered ArgumentName/ArgumentValue pairs.
func s3DestinationValidationError(w http.ResponseWriter, requestID string, rejected []s3DestinationRejection) {
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString("<Error><Code>InvalidArgument</Code><Message>Unable to validate the following destination configurations</Message>")
	for i, r := range rejected {
		fmt.Fprintf(&b, "<ArgumentName%d>", i+1)
		_ = xml.EscapeText(&b, []byte(r.ARN))
		fmt.Fprintf(&b, "</ArgumentName%d><ArgumentValue%d>", i+1, i+1)
		_ = xml.EscapeText(&b, []byte(r.Reason))
		fmt.Fprintf(&b, "</ArgumentValue%d>", i+1)
	}
	b.WriteString("<RequestId>")
	_ = xml.EscapeText(&b, []byte(requestID))
	b.WriteString("</RequestId></Error>")
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = io.WriteString(w, b.String())
}

// s3SendTestEvents sends the s3:TestEvent message Amazon S3 delivers to each
// queue and topic of a notification configuration it has just validated.
func s3SendTestEvents(bucket, requestID string, cfg s3NotificationConfiguration) {
	body, err := json.Marshal(map[string]string{
		"Service":   "Amazon S3",
		"Event":     "s3:TestEvent",
		"Time":      time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"Bucket":    bucket,
		"RequestId": requestID,
		"HostId":    s3HostID(),
	})
	if err != nil {
		panic(err)
	}
	for _, qc := range cfg.QueueConfigurations {
		sqsEnqueueByARN(qc.Queue, string(body))
	}
	for _, tc := range cfg.TopicConfigurations {
		s3PublishToTopic(tc.Topic, string(body))
	}
}

func s3HostID() string {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}
