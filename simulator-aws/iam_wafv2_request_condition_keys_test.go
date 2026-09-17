package main

import "testing"

func wafv2ConditionContext(operation, body string) map[string][]string {
	return populatedConditionContext(jsonConditionRequest("AWSWAF_20190729."+operation), "wafv2", operation, body)
}

func TestWAFv2ConditionKeysReadTheLoggingConfiguration(t *testing.T) {
	ctx := wafv2ConditionContext("PutLoggingConfiguration", `{"LoggingConfiguration": {
		"ResourceArn": "arn:aws:wafv2:us-east-1:123456789012:regional/webacl/a/1",
		"LogDestinationConfigs": ["arn:aws:logs:us-east-1:123456789012:log-group:aws-waf-logs-a"],
		"LogType": "WAF_LOGS",
		"LogScope": "CUSTOMER"
	}}`)
	assertPopulatedConditionValues(t, ctx, map[string][]string{
		"wafv2:LogDestinationResource": {"arn:aws:logs:us-east-1:123456789012:log-group:aws-waf-logs-a"},
		"wafv2:LogScope":               {"CUSTOMER"},
	})

	for _, operation := range []string{"GetLoggingConfiguration", "DeleteLoggingConfiguration", "ListLoggingConfigurations"} {
		ctx = wafv2ConditionContext(operation, `{"Scope": "REGIONAL", "LogScope": "SECURITY_LAKE"}`)
		assertPopulatedConditionValues(t, ctx, map[string][]string{"wafv2:LogScope": {"SECURITY_LAKE"}})
	}
}

func TestWAFv2ConditionKeysAbsentWithoutTheirMembers(t *testing.T) {
	ctx := wafv2ConditionContext("GetLoggingConfiguration", `{"ResourceArn": "arn:aws:wafv2:us-east-1:123456789012:regional/webacl/a/1"}`)
	assertConditionKeysAbsent(t, ctx, "wafv2:LogScope", "wafv2:LogDestinationResource")
	ctx = wafv2ConditionContext("GetWebACL", `{"LogScope": "CUSTOMER"}`)
	assertConditionKeysAbsent(t, ctx, "wafv2:LogScope")
}
