package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
	"github.com/e6qu/sockerless-cloud/sim/eventbus"
)

// snsMessage is one message published to a topic.
type snsMessage struct {
	TopicARN    string
	MessageID   string
	Subject     string
	Message     string
	Attributes  map[string]SQSMessageAttribute
	PublishedAt time.Time
}

// snsSubscriberDelivery is one message Amazon SNS owes one subscription. Body
// is what the endpoint receives; DeadLetterBody is what the subscription's
// dead-letter queue receives when the delivery fails.
type snsSubscriberDelivery struct {
	Protocol        string
	SubscriptionARN string
	Endpoint        string
	TopicARN        string
	MessageType     string
	MessageID       string
	MessageMD5      string
	Subject         string `json:",omitempty"`
	Raw             bool
	Body            []byte
	Attributes      map[string]SQSMessageAttribute `json:",omitempty"`
	DeadLetterBody  string                         `json:",omitempty"`
	Policy          snsRetryPolicy
}

// snsManagedEndpointRetryPolicy is the delivery policy Amazon SNS applies to
// the AWS managed endpoints — Amazon SQS, AWS Lambda and Amazon Data
// Firehose: 3 immediate retries, 2 one second apart, 10 backing off
// exponentially from 1 to 20 seconds and 100,000 twenty seconds apart.
var snsManagedEndpointRetryPolicy = snsRetryPolicy{
	MinDelayTarget: 1, MaxDelayTarget: 20, NumRetries: 100015,
	NumNoDelayRetries: 3, NumMinDelayRetries: 2, NumMaxDelayRetries: 100000, BackoffFunction: "exponential",
}

// snsCustomerEndpointRetryPolicy is the delivery policy Amazon SNS applies to
// email over SMTP: 2 retries ten seconds apart, 10 backing off exponentially
// from 10 to 600 seconds and 38 six hundred seconds apart.
var snsCustomerEndpointRetryPolicy = snsRetryPolicy{
	MinDelayTarget: 10, MaxDelayTarget: 600, NumRetries: 50,
	NumMinDelayRetries: 2, NumMaxDelayRetries: 38, BackoffFunction: "exponential",
}

var snsTopicBus *eventbus.Bus[snsMessage, SNSSubscription, snsSubscriberDelivery]

func registerSNSDeliveries(srv *sim.Server) {
	store := sim.MakeStore[delivery.Item[snsSubscriberDelivery]](srv.DB(), "sns_deliveries")
	if err := snsMigrateHTTPDeliveries(srv.DB(), store); err != nil {
		panic(err)
	}
	snsTopicBus = eventbus.New(srv, eventbus.Config[snsMessage, SNSSubscription, snsSubscriberDelivery]{
		Subscriptions: func(topicARN string) []SNSSubscription {
			return snsSubscriptions.Filter(func(sub SNSSubscription) bool { return sub.TopicARN == topicARN && sub.Confirmed })
		},
		Admits:    snsAdmits,
		Transform: snsPrepareDelivery,
		Delivery: &eventbus.Delivery[snsSubscriberDelivery]{
			Name:  "Amazon SNS deliveries",
			Store: store,
			Policy: func(d snsSubscriberDelivery) delivery.Policy {
				return delivery.Policy{MaxAttempts: d.Policy.NumRetries + 1, Backoff: d.Policy.delay}
			},
			Attempt: snsAttemptDelivery,
			Inline: func(d snsSubscriberDelivery) bool {
				switch d.Protocol {
				case "sqs", "lambda", "firehose":
					return true
				}
				return false
			},
			Settled:    snsSettleDelivery,
			DeadLetter: snsDeadLetterDelivery,
		},
	})
	snsTopicBus.Resume()
}

// snsMigrateHTTPDeliveries moves the pending HTTP/S deliveries an earlier
// build kept in a table of their own into the deliveries of every protocol.
func snsMigrateHTTPDeliveries(db *sql.DB, store sim.Store[delivery.Item[snsSubscriberDelivery]]) error {
	return sim.Migrate(db, "sns_http_deliveries/sns_deliveries", func() error {
		legacy, rows, err := sim.LegacyRows[delivery.Item[snsSubscriberDelivery]](db, "sns_http_deliveries")
		if err != nil {
			return err
		}
		for _, row := range rows {
			item := row.Item
			item.Payload.Protocol = "http"
			if strings.HasPrefix(strings.ToLower(item.Payload.Endpoint), "https:") {
				item.Payload.Protocol = "https"
			}
			item.Payload.DeadLetterBody = string(item.Payload.Body)
			store.Put(row.ID, item)
			legacy.Delete(row.ID)
		}
		return nil
	})
}

// snsAdmits applies the subscription's filter policy, publishing the metric
// that names why a message was filtered out.
func snsAdmits(sub SNSSubscription, m snsMessage) bool {
	matched, metric := snsFilterOutcome(sub, m.Message, m.Attributes)
	if !matched {
		topic := snsTopicNameFromARN(m.TopicARN)
		snsPutTopicMetric(topic, metric, 1, "Count")
		if metric != "NumberOfNotificationsFilteredOut" {
			snsPutTopicMetric(topic, "NumberOfNotificationsFilteredOut", 1, "Count")
		}
	}
	return matched
}

// snsPrepareDelivery renders a message as the subscription's protocol
// receives it, under the delivery policy Amazon SNS applies to the protocol.
func snsPrepareDelivery(sub SNSSubscription, messages []snsMessage) (snsSubscriberDelivery, bool) {
	m := messages[0]
	raw := strings.EqualFold(sub.Attributes["RawMessageDelivery"], "true")
	envelope := snsNotificationEnvelope(m.TopicARN, m.MessageID, m.Subject, m.Message, m.Attributes)
	d := snsSubscriberDelivery{
		Protocol:        strings.ToLower(sub.Protocol),
		SubscriptionARN: sub.ARN,
		Endpoint:        sub.Endpoint,
		TopicARN:        m.TopicARN,
		MessageType:     "Notification",
		MessageID:       m.MessageID,
		MessageMD5:      snsMessageMD5(m.Message),
		Raw:             raw,
		Body:            []byte(envelope),
		DeadLetterBody:  envelope,
		Policy:          snsManagedEndpointRetryPolicy,
	}
	if raw {
		d.Body, d.DeadLetterBody = []byte(m.Message), m.Message
	}
	switch d.Protocol {
	case "sqs":
		if raw {
			d.Attributes = m.Attributes
		}
	case "firehose":
	case "lambda":
		d.Body = snsLambdaEventPayload(sub.ARN, m)
	case "http", "https":
		d.Policy = snsEffectiveHTTPRetryPolicy(sub)
		if !raw {
			body, err := json.Marshal(snsHTTPNotificationEnvelope(sub, m))
			if err != nil {
				cwEvalLogger.Error().Err(err).Str("endpoint", sub.Endpoint).Msg("Amazon SNS HTTP envelope does not encode")
				return d, false
			}
			d.Body, d.DeadLetterBody = body, string(body)
		}
	case "email", "email-json":
		d.Policy = snsCustomerEndpointRetryPolicy
		d.Subject = m.Subject
		if d.Subject == "" {
			d.Subject = "AWS Notification Message"
		}
		d.Body = []byte(m.Message)
		if d.Protocol == "email-json" {
			body, err := json.Marshal(snsNotificationEmailEnvelope(sub, m.MessageID, m.Subject, m.Message, m.Attributes))
			if err != nil {
				return d, false
			}
			d.Body = body
		}
	default:
		cwEvalLogger.Info().Str("topicARN", m.TopicARN).Str("msgID", m.MessageID).Str("protocol", sub.Protocol).
			Str("endpoint", sub.Endpoint).Msg("SNS fanout skipping unsupported subscription protocol")
		return d, false
	}
	return d, true
}

func snsAttemptDelivery(ctx context.Context, item *delivery.Item[snsSubscriberDelivery]) delivery.Outcome {
	d := item.Payload
	if d.MessageType == "Notification" {
		if _, ok := snsSubscriptions.Get(d.SubscriptionARN); !ok {
			return delivery.Permanent(fmt.Errorf("subscription %s no longer exists", d.SubscriptionARN))
		}
	}
	switch d.Protocol {
	case "sqs":
		return snsAttemptSQS(d)
	case "lambda":
		return snsAttemptLambda(d)
	case "firehose":
		return snsAttemptFirehose(d)
	case "email", "email-json":
		return snsAttemptEmail(d)
	}
	return snsAttemptHTTP(ctx, d)
}

// snsServiceSource is who Amazon SNS delivers as for the topic.
func snsServiceSource(topicARN string) iamServiceSource {
	return iamServiceSource{Service: "sns.amazonaws.com", SourceArn: topicARN, SourceAccount: snsARNAccount(topicARN)}
}

// snsRefused is a client-side error Amazon SNS does not retry, with the
// providerResponse it records for it.
func snsRefused(status int, requestIDField, code, message string) delivery.Outcome {
	outcome := delivery.Permanent(errors.New(code + ": " + message)).WithStatus(status)
	outcome.Response = []byte(snsProviderError(requestIDField, code, message))
	return outcome
}

func snsAccepted(status int, response map[string]string) delivery.Outcome {
	outcome := delivery.Delivered().WithStatus(status)
	outcome.Response, _ = json.Marshal(response)
	return outcome
}

// snsAttemptSQS enqueues the notification into the subscriber queue when the
// queue's resource policy admits sns:SendMessage from the topic.
func snsAttemptSQS(d snsSubscriberDelivery) delivery.Outcome {
	queueARN := d.Endpoint
	if !iamAuthorizeServiceDelivery(queueARN, "sqs:SendMessage", snsServiceSource(d.TopicARN)) {
		return snsRefused(http.StatusForbidden, "sqsRequestId", "AccessDenied", "Access to the resource "+queueARN+" is denied.")
	}
	queueName := snsTopicNameFromARN(queueARN)
	if _, ok := sqsQueues.Get(queueName); !ok {
		return snsRefused(http.StatusBadRequest, "sqsRequestId", "AWS.SimpleQueueService.NonExistentQueue", "The specified queue does not exist.")
	}
	sent := sqsEnqueue(queueName, sqsSendEntry{MessageBody: string(d.Body), MessageAttributes: d.Attributes})
	return snsAccepted(http.StatusOK, map[string]string{"sqsRequestId": sim.NewUUID(), "sqsMessageId": sent.MessageID})
}

// snsAttemptLambda invokes the function asynchronously with the SNS event
// when the function's resource policy admits lambda:InvokeFunction from the
// topic.
func snsAttemptLambda(d snsSubscriberDelivery) delivery.Outcome {
	functionARN := d.Endpoint
	if !iamAuthorizeServiceDelivery(functionARN, "lambda:InvokeFunction", snsServiceSource(d.TopicARN)) {
		return snsRefused(http.StatusForbidden, "lambdaRequestId", "AccessDeniedException",
			"User: sns.amazonaws.com is not authorized to perform: lambda:InvokeFunction on resource: "+functionARN+
				" because no resource-based policy allows the lambda:InvokeFunction action")
	}
	fn, _, ok := lambdaResolveInvocationTarget(functionARN, "")
	if !ok {
		return snsRefused(http.StatusNotFound, "lambdaRequestId", "ResourceNotFoundException", "Function not found: "+functionARN)
	}
	requestID := lambdaInvokeAsynchronously(fn, d.Body, lambdaAsyncQualifier(functionARN, ""))
	return snsAccepted(http.StatusAccepted, map[string]string{"lambdaRequestId": requestID})
}

// snsAttemptFirehose puts the notification on the delivery stream as the
// subscription's SubscriptionRoleArn.
func snsAttemptFirehose(d snsSubscriberDelivery) delivery.Outcome {
	sub, _ := snsSubscriptions.Get(d.SubscriptionARN)
	if err := iamValidateServiceRole(sub.Attributes["SubscriptionRoleArn"], "sns.amazonaws.com", map[string]string{
		"firehose:PutRecord": d.Endpoint,
	}); err != nil {
		return snsRefused(http.StatusForbidden, "firehoseRequestId", "AccessDeniedException", err.Error())
	}
	recordID, err := firehosePutServiceRecord(d.Endpoint, d.Body)
	if err != nil {
		return snsRefused(http.StatusBadRequest, "firehoseRequestId", "ResourceNotFoundException", err.Error())
	}
	return snsAccepted(http.StatusOK, map[string]string{"firehoseRequestId": sim.NewUUID(), "recordId": recordID})
}

// snsAttemptEmail sends the notification over SMTP. A permanent SMTP reply
// (5xx) refuses it; an unreachable server or a transient reply is retried.
func snsAttemptEmail(d snsSubscriberDelivery) delivery.Outcome {
	sub, _ := snsSubscriptions.Get(d.SubscriptionARN)
	err := snsSendEmail(sub, d.Subject, string(d.Body))
	if err == nil {
		return delivery.Delivered()
	}
	var reply *textproto.Error
	if errors.As(err, &reply) && reply.Code >= 500 {
		return delivery.Permanent(err)
	}
	if _, invalid := snsEmailDomain(d.Endpoint); invalid != nil {
		return delivery.Permanent(err)
	}
	return delivery.Retryable(err)
}

// snsSettleDelivery publishes the topic's delivery metric and status log for
// a notification once its delivery ended.
func snsSettleDelivery(item delivery.Item[snsSubscriberDelivery], reason delivery.Reason) {
	d := item.Payload
	if d.MessageType != "Notification" {
		if reason != delivery.Succeeded {
			cwEvalLogger.Error().Str("endpoint", d.Endpoint).Str("reason", string(reason)).Str("error", item.LastError).
				Msg("Amazon SNS could not deliver a subscription confirmation")
		}
		return
	}
	providerResponse := item.LastResponse
	if providerResponse == "" {
		providerResponse = item.LastError
	}
	snsRecordDelivery(snsDelivery{
		TopicARN: d.TopicARN, MessageID: d.MessageID, MessageMD5: d.MessageMD5, PublishedAt: item.EnqueuedAt,
		Protocol: d.Protocol, Destination: d.Endpoint, Delivered: reason == delivery.Succeeded,
		StatusCode: item.LastStatus, ProviderResponse: providerResponse, Attempts: item.Attempts,
	})
}

// snsDeadLetterDelivery moves a notification Amazon SNS could not deliver to
// the subscription's dead-letter queue, carrying the RequestID, ErrorCode and
// ErrorMessage attributes the service adds.
func snsDeadLetterDelivery(item delivery.Item[snsSubscriberDelivery], reason delivery.Reason) {
	d := item.Payload
	cwEvalLogger.Error().Str("protocol", d.Protocol).Str("endpoint", d.Endpoint).Str("reason", string(reason)).
		Str("error", item.LastError).Int("attempts", item.Attempts).Msg("Amazon SNS delivery failed")
	if d.MessageType != "Notification" {
		return
	}
	sub, ok := snsSubscriptions.Get(d.SubscriptionARN)
	if !ok {
		return
	}
	dlq := snsRedriveTarget(sub)
	if dlq == "" {
		return
	}
	snsDeadLetter(sub, dlq, d.DeadLetterBody, map[string]SQSMessageAttribute{
		"RequestID":    {DataType: "String", StringValue: item.ID},
		"ErrorCode":    {DataType: "String", StringValue: snsDeadLetterErrorCode(d.Protocol, item)},
		"ErrorMessage": {DataType: "String", StringValue: item.LastError},
	})
}

// snsDeadLetterErrorCode names why the last attempt failed: an HTTP/S
// endpoint's status, the error code an AWS managed endpoint answered with, or
// EndpointUnavailable for an endpoint that did not answer.
func snsDeadLetterErrorCode(protocol string, item delivery.Item[snsSubscriberDelivery]) string {
	if protocol != "http" && protocol != "https" {
		var provider struct {
			ErrorCode string `json:"ErrorCode"`
		}
		if json.Unmarshal([]byte(item.LastResponse), &provider) == nil && provider.ErrorCode != "" {
			return provider.ErrorCode
		}
	}
	if item.LastStatus != 0 {
		return strconv.Itoa(item.LastStatus)
	}
	return "EndpointUnavailable"
}
