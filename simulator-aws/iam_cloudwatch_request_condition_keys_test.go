package main

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

const cloudWatchConditionTarget = "GraniteServiceVersion20100801."

func TestCloudWatchConditionKeysReadAlarmActionsOverEveryProtocol(t *testing.T) {
	want := map[string][]string{"cloudwatch:AlarmActions": {
		"arn:aws:sns:us-east-1:123456789012:page",
		"arn:aws:automate:us-east-1:ec2:stop",
	}}

	query := queryConditionRequest(url.Values{
		"Action":                []string{"PutMetricAlarm"},
		"Version":               []string{"2010-08-01"},
		"AlarmName":             []string{"a"},
		"AlarmActions.member.1": []string{"arn:aws:sns:us-east-1:123456789012:page"},
		"AlarmActions.member.2": []string{"arn:aws:automate:us-east-1:ec2:stop"},
	})
	assertConditionValues(t, requestConditionContext(query, "cloudwatch", "PutMetricAlarm", ""), want)

	jsonBody := `{"AlarmName": "a", "AlarmRule": "ALARM(x)", "AlarmActions": ["arn:aws:sns:us-east-1:123456789012:page", "arn:aws:automate:us-east-1:ec2:stop"]}`
	ctx := requestConditionContext(jsonConditionRequest(cloudWatchConditionTarget+"PutCompositeAlarm"),
		"cloudwatch", "PutCompositeAlarm", jsonBody)
	assertConditionValues(t, ctx, want)

	encoded, err := cbor.Marshal(map[string]any{
		"AlarmName":    "a",
		"AlarmActions": want["cloudwatch:AlarmActions"],
	})
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(encoded); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/service/GraniteServiceVersion20100801/operation/PutLogAlarm", nil)
	r.Header.Set("Content-Type", "application/cbor")
	ctx = requestConditionContext(r, "cloudwatch", "PutLogAlarm", compressed.String())
	assertConditionValues(t, ctx, want)
}

func TestCloudWatchConditionKeysReadInsightRuleResources(t *testing.T) {
	ctx := requestConditionContext(jsonConditionRequest(cloudWatchConditionTarget+"PutInsightRule"),
		"cloudwatch", "PutInsightRule",
		`{"RuleName": "r", "RuleDefinition": "{\"Schema\":{\"Name\":\"CloudWatchLogRule\",\"Version\":1},\"LogGroupNames\":[\"/aws/lambda/a\",\"API-Gateway-*\"],\"LogFormat\":\"JSON\",\"Contribution\":{\"Keys\":[\"$.ip\"]},\"AggregateOn\":\"Count\"}"}`)
	assertConditionValues(t, ctx, map[string][]string{
		"cloudwatch:requestInsightRuleLogGroups": {"/aws/lambda/a", "API-Gateway-*"},
	})

	query := queryConditionRequest(url.Values{
		"Action":                             []string{"PutManagedInsightRules"},
		"ManagedRules.member.1.TemplateName": []string{"t"},
		"ManagedRules.member.1.ResourceARN":  []string{"arn:aws:dynamodb:us-east-1:123456789012:table/a"},
		"ManagedRules.member.2.TemplateName": []string{"t"},
		"ManagedRules.member.2.ResourceARN":  []string{"arn:aws:dynamodb:us-east-1:123456789012:table/b"},
	})
	assertConditionValues(t, requestConditionContext(query, "cloudwatch", "PutManagedInsightRules", ""),
		map[string][]string{"cloudwatch:requestManagedResourceARNs": {
			"arn:aws:dynamodb:us-east-1:123456789012:table/a",
			"arn:aws:dynamodb:us-east-1:123456789012:table/b",
		}})

	ctx = requestConditionContext(jsonConditionRequest(cloudWatchConditionTarget+"ListManagedInsightRules"),
		"cloudwatch", "ListManagedInsightRules", `{"ResourceARN": "arn:aws:dynamodb:us-east-1:123456789012:table/a"}`)
	assertConditionValues(t, ctx, map[string][]string{
		"cloudwatch:requestManagedResourceARNs": {"arn:aws:dynamodb:us-east-1:123456789012:table/a"},
	})
}

func TestCloudWatchConditionKeysAbsentWithoutTheirMembers(t *testing.T) {
	ctx := requestConditionContext(jsonConditionRequest(cloudWatchConditionTarget+"PutMetricAlarm"),
		"cloudwatch", "PutMetricAlarm", `{"AlarmName": "a"}`)
	assertConditionKeysAbsent(t, ctx, "cloudwatch:AlarmActions")
	ctx = requestConditionContext(jsonConditionRequest(cloudWatchConditionTarget+"DescribeAlarms"),
		"cloudwatch", "DescribeAlarms", `{"AlarmActions": ["arn:aws:sns:us-east-1:123456789012:page"]}`)
	assertConditionKeysAbsent(t, ctx, "cloudwatch:AlarmActions")
}
