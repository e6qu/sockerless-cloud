package main

import "testing"

func logsConditionContext(operation, body string) map[string][]string {
	return requestConditionContext(jsonConditionRequest("Logs_20140328."+operation), "logs", operation, body)
}

func TestLogsConditionKeysReadTheDeliveryResources(t *testing.T) {
	ctx := logsConditionContext("PutDeliverySource",
		`{"name": "src", "resourceArn": "arn:aws:bedrock:us-east-1:123456789012:knowledge-base/kb", "logType": "APPLICATION_LOGS"}`)
	assertConditionValues(t, ctx, map[string][]string{
		"logs:LogGeneratingResourceArns": {"arn:aws:bedrock:us-east-1:123456789012:knowledge-base/kb"},
	})

	ctx = logsConditionContext("PutDeliveryDestination",
		`{"name": "dst", "deliveryDestinationConfiguration": {"destinationResourceArn": "arn:aws:s3:::logs-bucket"}}`)
	assertConditionValues(t, ctx, map[string][]string{
		"logs:DeliveryDestinationResourceArn": {"arn:aws:s3:::logs-bucket"},
	})
}

func TestLogsConditionKeysAbsentWithoutTheirMembers(t *testing.T) {
	ctx := logsConditionContext("PutDeliveryDestination", `{"name": "dst", "deliveryDestinationType": "XRAY"}`)
	assertConditionKeysAbsent(t, ctx, "logs:DeliveryDestinationResourceArn")
	ctx = logsConditionContext("GetDeliverySource", `{"name": "src", "resourceArn": "arn:aws:s3:::x"}`)
	assertConditionKeysAbsent(t, ctx, "logs:LogGeneratingResourceArns")
}
