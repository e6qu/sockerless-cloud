package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// snsFeedbackProtocols are the attribute prefixes Amazon SNS reads delivery
// status logging from, one per subscription protocol it logs.
var snsFeedbackProtocols = []string{"HTTP", "Firehose", "Lambda", "SQS", "Application"}

var snsFeedbackRoleARNPattern = regexp.MustCompile(`^arn:aws[a-z-]*:iam::\d{12}:role/[\w+=,.@/-]{1,512}$`)

// snsFeedbackProtocol is the attribute prefix of a subscription protocol, empty
// for a protocol Amazon SNS keeps no delivery status for.
func snsFeedbackProtocol(protocol string) string {
	switch strings.ToLower(protocol) {
	case "http", "https":
		return "HTTP"
	case "firehose":
		return "Firehose"
	case "lambda":
		return "Lambda"
	case "sqs":
		return "SQS"
	case "application":
		return "Application"
	}
	return ""
}

// snsInvalidTopicAttribute is the InvalidParameter message Amazon SNS answers
// a topic attribute value with, empty for a value it accepts. An empty value
// removes the attribute and is always accepted.
func snsInvalidTopicAttribute(name, value string) string {
	if value == "" {
		return ""
	}
	if name == "DeliveryPolicy" {
		var doc map[string]any
		if err := json.Unmarshal([]byte(value), &doc); err != nil {
			return "Invalid parameter: DeliveryPolicy: failed to parse JSON. " + err.Error()
		}
	}
	for _, protocol := range snsFeedbackProtocols {
		switch name {
		case protocol + "SuccessFeedbackRoleArn", protocol + "FailureFeedbackRoleArn":
			if !snsFeedbackRoleARNPattern.MatchString(value) {
				return fmt.Sprintf("Invalid parameter: Attributes Reason: %s: %s is not a valid IAM role ARN", name, value)
			}
		case protocol + "SuccessFeedbackSampleRate":
			rate, err := strconv.Atoi(value)
			if err != nil || rate < 0 || rate > 100 {
				return fmt.Sprintf("Invalid parameter: Attributes Reason: %s must be an integer from 0 to 100", name)
			}
		}
	}
	return ""
}

// snsDelivery is the outcome of one notification Amazon SNS handed to one
// subscription endpoint.
type snsDelivery struct {
	TopicARN         string
	MessageID        string
	MessageMD5       string
	PublishedAt      time.Time
	Protocol         string
	Destination      string
	Delivered        bool
	StatusCode       int
	ProviderResponse string
	Attempts         int
}

func snsMessageMD5(message string) string {
	sum := md5.Sum([]byte(message))
	return hex.EncodeToString(sum[:])
}

// snsProviderError is the providerResponse Amazon SNS records for an endpoint
// that refused a notification it will not retry.
func snsProviderError(requestIDField, code, message string) string {
	out, _ := json.Marshal(map[string]string{"ErrorCode": code, "ErrorMessage": message, requestIDField: "Unrecoverable"})
	return string(out)
}

// snsRecordDelivery publishes the topic's delivery metric and writes the
// delivery status log entry the topic's feedback role asks for.
func snsRecordDelivery(d snsDelivery) {
	topicName := snsTopicNameFromARN(d.TopicARN)
	metric := "NumberOfNotificationsFailed"
	if d.Delivered {
		metric = "NumberOfNotificationsDelivered"
	}
	snsPutTopicMetric(topicName, metric, 1, "Count")

	protocol := snsFeedbackProtocol(d.Protocol)
	if protocol == "" {
		return
	}
	topic, ok := snsTopics.Get(topicName)
	if !ok {
		return
	}
	group := fmt.Sprintf("sns/%s/%s/%s", awsRegion(), snsARNAccount(d.TopicARN), topicName)
	role := topic.Attributes[protocol+"FailureFeedbackRoleArn"]
	status := "FAILURE"
	if d.Delivered {
		role = topic.Attributes[protocol+"SuccessFeedbackRoleArn"]
		status = "SUCCESS"
		rate, _ := strconv.Atoi(topic.Attributes[protocol+"SuccessFeedbackSampleRate"])
		if rate <= rand.IntN(100) {
			return
		}
	} else {
		group += "/Failure"
	}
	if role == "" {
		return
	}
	deliveredAt := time.Now().UTC()
	deliveryID := sim.NewUUID()
	actions := map[string]string{
		"logs:CreateLogStream": cwLogStreamArn(group, deliveryID),
		"logs:PutLogEvents":    cwLogStreamArn(group, deliveryID),
	}
	if _, exists := cwLogGroups.Get(group); !exists {
		actions["logs:CreateLogGroup"] = cwLogGroupArn(group)
	}
	if err := iamValidateServiceRole(role, "sns.amazonaws.com", actions); err != nil {
		cwEvalLogger.Error().Err(err).Str("topicARN", d.TopicARN).Str("logGroup", group).
			Msg("Amazon SNS cannot assume its delivery status logging role")
		return
	}
	entry, err := json.Marshal(map[string]any{
		"notification": map[string]any{
			"messageMD5Sum": d.MessageMD5,
			"messageId":     d.MessageID,
			"topicArn":      d.TopicARN,
			"timestamp":     d.PublishedAt.UTC().Format("2006-01-02 15:04:05.000"),
		},
		"delivery": map[string]any{
			"deliveryId":       deliveryID,
			"destination":      d.Destination,
			"providerResponse": d.ProviderResponse,
			"dwellTimeMs":      deliveredAt.Sub(d.PublishedAt).Milliseconds(),
			"attempts":         d.Attempts,
			"statusCode":       d.StatusCode,
		},
		"status": status,
	})
	if err != nil {
		cwEvalLogger.Error().Err(err).Str("topicARN", d.TopicARN).Msg("Amazon SNS delivery status entry does not encode")
		return
	}
	cwEnsureLogStream(group, deliveryID)
	cwIngestWorkloadLogLine(group, deliveryID, string(entry), deliveredAt)
}

func snsPutTopicMetric(topicName, metric string, value float64, unit string) {
	cwStoreDatum(CWMetricDatum{
		Namespace:  "AWS/SNS",
		MetricName: metric,
		Dimensions: []CWDimension{{Name: "TopicName", Value: topicName}},
		Value:      value,
		Unit:       unit,
		Timestamp:  float64(time.Now().UTC().Unix()),
	})
}
