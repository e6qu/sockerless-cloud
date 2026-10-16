package main

import (
	"encoding/json"
	"fmt"
)

// snsTopicAttributeNames are the attributes SetTopicAttributes documents.
var snsTopicAttributeNames = map[string]bool{
	"DeliveryPolicy": true, "DisplayName": true, "MaximumMessageSize": true, "Policy": true, "TracingConfig": true,
	"HTTPSuccessFeedbackRoleArn": true, "HTTPSuccessFeedbackSampleRate": true, "HTTPFailureFeedbackRoleArn": true,
	"FirehoseSuccessFeedbackRoleArn": true, "FirehoseSuccessFeedbackSampleRate": true, "FirehoseFailureFeedbackRoleArn": true,
	"LambdaSuccessFeedbackRoleArn": true, "LambdaSuccessFeedbackSampleRate": true, "LambdaFailureFeedbackRoleArn": true,
	"ApplicationSuccessFeedbackRoleArn": true, "ApplicationSuccessFeedbackSampleRate": true, "ApplicationFailureFeedbackRoleArn": true,
	"SQSSuccessFeedbackRoleArn": true, "SQSSuccessFeedbackSampleRate": true, "SQSFailureFeedbackRoleArn": true,
	"KmsMasterKeyId": true, "SignatureVersion": true,
	"ArchivePolicy": true, "ContentBasedDeduplication": true, "FifoThroughputScope": true,
}

// snsCreateTopicAttributeNames are the attributes CreateTopic takes: those a
// topic can later change, and FifoTopic, which only creation sets.
var snsCreateTopicAttributeNames = func() map[string]bool {
	names := map[string]bool{"FifoTopic": true}
	for name := range snsTopicAttributeNames {
		names[name] = true
	}
	return names
}()

// snsTopicEffectiveDeliveryPolicy is the topic's delivery policy with the
// service defaults filled in where it says nothing.
func snsTopicEffectiveDeliveryPolicy(t SNSTopic) string {
	policy := map[string]any{}
	if raw := t.Attributes["DeliveryPolicy"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			// snsInvalidTopicAttribute refused this document when it was set.
			panic(fmt.Sprintf("sns: unparseable topic DeliveryPolicy attribute: %v", err))
		}
	}
	httpPolicy, _ := policy["http"].(map[string]any)
	if httpPolicy == nil {
		httpPolicy = map[string]any{}
	}
	retry := snsDefaultHTTPRetryPolicy
	if given, ok := httpPolicy["defaultHealthyRetryPolicy"]; ok {
		if raw, err := json.Marshal(given); err == nil {
			var parsed snsRetryPolicy
			if json.Unmarshal(raw, &parsed) == nil {
				retry = snsCompleteRetryPolicy(parsed)
			}
		}
	}
	httpPolicy["defaultHealthyRetryPolicy"] = retry
	if _, ok := httpPolicy["disableSubscriptionOverrides"]; !ok {
		httpPolicy["disableSubscriptionOverrides"] = false
	}
	if _, ok := httpPolicy["defaultRequestPolicy"]; !ok {
		httpPolicy["defaultRequestPolicy"] = map[string]any{"headerContentType": "text/plain; charset=UTF-8"}
	}
	policy["http"] = httpPolicy
	raw, err := json.Marshal(policy)
	if err != nil {
		return ""
	}
	return string(raw)
}
