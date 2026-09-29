package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// snsRetryPolicy is the healthyRetryPolicy of an Amazon SNS HTTP/S delivery
// policy. Retries run in four phases — immediate, pre-backoff at
// minDelayTarget, backoff from minDelayTarget to maxDelayTarget along
// backoffFunction, post-backoff at maxDelayTarget — numRetries in all.
type snsRetryPolicy struct {
	MinDelayTarget     int    `json:"minDelayTarget"`
	MaxDelayTarget     int    `json:"maxDelayTarget"`
	NumRetries         int    `json:"numRetries"`
	NumNoDelayRetries  int    `json:"numNoDelayRetries"`
	NumMinDelayRetries int    `json:"numMinDelayRetries"`
	NumMaxDelayRetries int    `json:"numMaxDelayRetries"`
	BackoffFunction    string `json:"backoffFunction"`
}

// snsDefaultHTTPRetryPolicy is the policy Amazon SNS applies to an HTTP/S
// subscription neither it nor its topic configures.
var snsDefaultHTTPRetryPolicy = snsRetryPolicy{
	MinDelayTarget: 20, MaxDelayTarget: 20, NumRetries: 3, BackoffFunction: "linear",
}

func (p snsRetryPolicy) validate() error {
	switch {
	case p.MinDelayTarget < 1 || p.MaxDelayTarget < p.MinDelayTarget || p.MaxDelayTarget > 3600:
		return fmt.Errorf("minDelayTarget and maxDelayTarget must satisfy 1 <= minDelayTarget <= maxDelayTarget <= 3600")
	case p.NumRetries < 0 || p.NumRetries > 100:
		return fmt.Errorf("numRetries must be between 0 and 100")
	case p.NumNoDelayRetries < 0 || p.NumMinDelayRetries < 0 || p.NumMaxDelayRetries < 0 ||
		p.NumNoDelayRetries+p.NumMinDelayRetries+p.NumMaxDelayRetries > p.NumRetries:
		return fmt.Errorf("the no-delay, min-delay and max-delay retries cannot exceed numRetries")
	}
	switch p.BackoffFunction {
	case "arithmetic", "exponential", "geometric", "linear":
		return nil
	}
	return fmt.Errorf("backoffFunction %q is not arithmetic, exponential, geometric or linear", p.BackoffFunction)
}

// delay is the wait before retry n (1-based).
func (p snsRetryPolicy) delay(retry int) time.Duration {
	seconds := func(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
	minDelay, maxDelay := float64(p.MinDelayTarget), float64(p.MaxDelayTarget)
	backoffRetries := p.NumRetries - p.NumNoDelayRetries - p.NumMinDelayRetries - p.NumMaxDelayRetries
	switch {
	case retry <= p.NumNoDelayRetries:
		return 0
	case retry <= p.NumNoDelayRetries+p.NumMinDelayRetries:
		return seconds(minDelay)
	case retry <= p.NumNoDelayRetries+p.NumMinDelayRetries+backoffRetries:
		step := retry - p.NumNoDelayRetries - p.NumMinDelayRetries
		fraction := float64(step) / float64(backoffRetries)
		switch p.BackoffFunction {
		case "arithmetic":
			return seconds(minDelay + (maxDelay-minDelay)*fraction*fraction)
		case "geometric":
			return seconds(minDelay + (maxDelay-minDelay)*math.Sqrt(fraction))
		case "exponential":
			return seconds(minDelay * math.Pow(maxDelay/minDelay, fraction))
		}
		return seconds(minDelay + (maxDelay-minDelay)*fraction)
	}
	return seconds(maxDelay)
}

// snsEffectiveHTTPRetryPolicy is the subscription's own healthyRetryPolicy,
// else the topic's http.defaultHealthyRetryPolicy (which also wins when the
// topic disables subscription overrides), else the service default.
func snsEffectiveHTTPRetryPolicy(sub SNSSubscription) snsRetryPolicy {
	var topicPolicy struct {
		HTTP struct {
			DefaultHealthyRetryPolicy    *snsRetryPolicy `json:"defaultHealthyRetryPolicy"`
			DisableSubscriptionOverrides bool            `json:"disableSubscriptionOverrides"`
		} `json:"http"`
	}
	if topic, ok := snsTopics.Get(snsTopicNameFromARN(sub.TopicARN)); ok && topic.Attributes["DeliveryPolicy"] != "" {
		_ = json.Unmarshal([]byte(topic.Attributes["DeliveryPolicy"]), &topicPolicy)
	}
	if !topicPolicy.HTTP.DisableSubscriptionOverrides {
		if policy, ok := snsSubscriptionRetryPolicy(sub.Attributes["DeliveryPolicy"]); ok {
			return policy
		}
	}
	if policy := topicPolicy.HTTP.DefaultHealthyRetryPolicy; policy != nil {
		return snsCompleteRetryPolicy(*policy)
	}
	return snsDefaultHTTPRetryPolicy
}

func snsSubscriptionRetryPolicy(raw string) (snsRetryPolicy, bool) {
	if raw == "" {
		return snsRetryPolicy{}, false
	}
	var policy struct {
		HealthyRetryPolicy *snsRetryPolicy `json:"healthyRetryPolicy"`
	}
	if json.Unmarshal([]byte(raw), &policy) != nil || policy.HealthyRetryPolicy == nil {
		return snsRetryPolicy{}, false
	}
	return snsCompleteRetryPolicy(*policy.HealthyRetryPolicy), true
}

func snsCompleteRetryPolicy(p snsRetryPolicy) snsRetryPolicy {
	if p.BackoffFunction == "" {
		p.BackoffFunction = "linear"
	}
	return p
}

// snsValidateDeliveryAttributes checks the DeliveryPolicy and RedrivePolicy
// documents Amazon SNS parses when a subscription is created or changed.
func snsValidateDeliveryAttributes(attributes map[string]string) error {
	if raw := attributes["DeliveryPolicy"]; raw != "" {
		var policy struct {
			HealthyRetryPolicy *snsRetryPolicy `json:"healthyRetryPolicy"`
		}
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			return fmt.Errorf("DeliveryPolicy: %w", err)
		}
		if policy.HealthyRetryPolicy != nil {
			if err := snsCompleteRetryPolicy(*policy.HealthyRetryPolicy).validate(); err != nil {
				return fmt.Errorf("DeliveryPolicy: %w", err)
			}
		}
	}
	if raw := attributes["RedrivePolicy"]; raw != "" {
		var policy struct {
			DeadLetterTargetArn string `json:"deadLetterTargetArn"`
		}
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			return fmt.Errorf("RedrivePolicy: %w", err)
		}
		if !strings.HasPrefix(policy.DeadLetterTargetArn, "arn:aws:sqs:") {
			return fmt.Errorf("RedrivePolicy: deadLetterTargetArn must be an Amazon SQS queue ARN")
		}
	}
	return nil
}

// snsEffectiveDeliveryPolicy renders the EffectiveDeliveryPolicy attribute of
// an HTTP/S subscription.
func snsEffectiveDeliveryPolicy(sub SNSSubscription) string {
	raw, err := json.Marshal(map[string]any{
		"healthyRetryPolicy": snsEffectiveHTTPRetryPolicy(sub),
		"sicklyRetryPolicy":  nil,
		"throttlePolicy":     nil,
		"guaranteed":         false,
	})
	if err != nil {
		return ""
	}
	return string(raw)
}
