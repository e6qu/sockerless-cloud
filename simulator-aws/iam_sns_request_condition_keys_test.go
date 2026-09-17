package main

import "testing"

func TestSNSRequestConditionKeysReadTheSubscription(t *testing.T) {
	assertConditionValues(t, requestConditionContext("sns", "Subscribe", formRequest(map[string]string{
		"Action":   "Subscribe",
		"TopicArn": "arn:aws:sns:us-east-1:123456789012:orders",
		"Protocol": "https",
		"Endpoint": "https%3A%2F%2Fhooks.example.com%2Forders",
	}), ""), map[string][]string{
		"sns:Protocol": {"https"},
		"sns:Endpoint": {"https://hooks.example.com/orders"},
	})
	assertConditionValues(t, requestConditionContext("sns", "Subscribe", formRequest(map[string]string{
		"Action":   "Subscribe",
		"TopicArn": "arn:aws:sns:us-east-1:123456789012:orders",
	}), ""), map[string][]string{})
	assertConditionValues(t, requestConditionContext("sns", "Publish", formRequest(map[string]string{
		"Action":   "Publish",
		"Protocol": "https",
	}), ""), map[string][]string{})
}
