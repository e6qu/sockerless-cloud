package main

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCloudTrailLookupEventsReturnsNewestMatchesFirst(t *testing.T) {
	events := make([]CloudTrailEvent, 0, 61)
	for i := 0; i < 60; i++ {
		events = append(events, CloudTrailEvent{
			EventId:     fmt.Sprintf("event-old-%02d", i),
			EventName:   "DescribeInstances",
			EventSource: "ec2.amazonaws.com",
			EventTime:   "2026-06-02T15:00:00Z",
			Username:    "sockerless",
		})
	}
	events = append(events, CloudTrailEvent{
		EventId:     "event-new",
		EventName:   "CreateVpc",
		EventSource: "ec2.amazonaws.com",
		EventTime:   "2026-06-02T15:01:00Z",
		Username:    "sockerless",
	})

	out := cloudTrailMatchedOrdered(events, nil)
	if got := out[0].EventName; got != "CreateVpc" {
		t.Fatalf("newest event must be first; got %v", got)
	}

	out = cloudTrailMatchedOrdered(events, []cloudTrailLookupAttribute{
		{AttributeKey: "EventName", AttributeValue: "CreateVpc"},
	})
	if len(out) != 1 {
		t.Fatalf("expected one filtered CreateVpc event, got %d", len(out))
	}
}

// TestCloudTrailEventMatchesAllKeys pins LookupEvents filtering for all eight
// AttributeKey values, so every supported filter key narrows the event stream.
func TestCloudTrailEventMatchesAllKeys(t *testing.T) {
	ev := CloudTrailEvent{
		EventId:     "id-1",
		EventName:   "CreateCluster",
		EventSource: "ecs.amazonaws.com",
		Username:    "alice",
		AccessKeyId: "AKIATEST",
		ReadOnly:    false,
		Resources:   []CloudTrailResource{{ResourceType: "AWS::ECS::Cluster", ResourceName: "probe"}},
	}
	cases := []struct {
		key, val string
		want     bool
	}{
		{"EventId", "id-1", true}, {"EventId", "other", false},
		{"EventName", "CreateCluster", true}, {"EventName", "X", false},
		{"EventSource", "ecs.amazonaws.com", true}, {"EventSource", "s3.amazonaws.com", false},
		{"Username", "alice", true}, {"Username", "bob", false},
		{"AccessKeyId", "AKIATEST", true}, {"AccessKeyId", "X", false},
		{"ReadOnly", "false", true}, {"ReadOnly", "true", false},
		{"ResourceName", "probe", true}, {"ResourceName", "nope", false},
		{"ResourceType", "AWS::ECS::Cluster", true}, {"ResourceType", "AWS::S3::Bucket", false},
	}
	for _, c := range cases {
		got := cloudTrailEventMatches(ev, []cloudTrailLookupAttribute{{AttributeKey: c.key, AttributeValue: c.val}})
		if got != c.want {
			t.Errorf("filter %s=%q: got %v want %v", c.key, c.val, got, c.want)
		}
	}
	// An unknown attribute key must never silently match-all (the #496 defect).
	if cloudTrailEventMatches(ev, []cloudTrailLookupAttribute{{AttributeKey: "Bogus", AttributeValue: "x"}}) {
		t.Error("unknown attribute key must not match")
	}
	// Multiple attributes are ANDed.
	if !cloudTrailEventMatches(ev, []cloudTrailLookupAttribute{
		{AttributeKey: "EventName", AttributeValue: "CreateCluster"},
		{AttributeKey: "ResourceName", AttributeValue: "probe"},
	}) {
		t.Error("AND of two matching attributes should match")
	}
	if cloudTrailEventMatches(ev, []cloudTrailLookupAttribute{
		{AttributeKey: "EventName", AttributeValue: "CreateCluster"},
		{AttributeKey: "ResourceName", AttributeValue: "other"},
	}) {
		t.Error("AND with one non-matching attribute should not match")
	}
}

func TestCloudTrailReadOnly(t *testing.T) {
	for op, want := range map[string]bool{
		"DescribeInstances": true, "GetParameter": true, "ListSchedules": true,
		"LookupEvents": true, "ScanTable": true,
		"CreateCluster": false, "RunTask": false, "PutItem": false, "DeleteTrail": false,
	} {
		if got := cloudTrailReadOnly(op); got != want {
			t.Errorf("cloudTrailReadOnly(%q) = %v, want %v", op, got, want)
		}
	}
}

func TestCloudTrailAccessKeyID(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/20260607/us-east-1/ecs/aws4_request, SignedHeaders=host, Signature=abc")
	if got := cloudTrailAccessKeyID(r); got != "AKIAEXAMPLE" {
		t.Fatalf("got %q, want AKIAEXAMPLE", got)
	}
	if got := cloudTrailAccessKeyID(httptest.NewRequest("POST", "/", nil)); got != "" {
		t.Fatalf("unsigned request: got %q, want empty", got)
	}
}

func TestCloudTrailResourcesExtraction(t *testing.T) {
	jsonReq := httptest.NewRequest("POST", "/", nil)
	jsonReq.Header.Set("X-Amz-Target", "AmazonEC2ContainerServiceV20141113.CreateCluster")

	res := cloudTrailResources("ecs.amazonaws.com", "CreateCluster", []byte(`{"clusterName":"probe"}`), jsonReq)
	if len(res) != 1 || res[0].ResourceName != "probe" || res[0].ResourceType != "AWS::ECS::Cluster" {
		t.Fatalf("ecs create-cluster resources = %+v", res)
	}
	// PascalCase wire key resolved case-insensitively.
	res = cloudTrailResources("dynamodb.amazonaws.com", "PutItem", []byte(`{"TableName":"orders"}`), jsonReq)
	if len(res) != 1 || res[0].ResourceName != "orders" || res[0].ResourceType != "AWS::DynamoDB::Table" {
		t.Fatalf("dynamodb resources = %+v", res)
	}
	// Query-protocol service: identifier comes from the form, not the JSON body.
	form := url.Values{"Action": {"DescribeInstances"}, "InstanceId": {"i-0abc"}}
	formReq := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	formReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res = cloudTrailResources("ec2.amazonaws.com", "DescribeInstances", nil, formReq)
	if len(res) != 1 || res[0].ResourceName != "i-0abc" || res[0].ResourceType != "AWS::EC2::Instance" {
		t.Fatalf("ec2 resources = %+v", res)
	}
	// An operation acting on no named resource records none — never fabricated.
	res = cloudTrailResources("sts.amazonaws.com", "GetCallerIdentity", nil, formReq)
	if len(res) != 0 {
		t.Fatalf("sts GetCallerIdentity should record no resource, got %+v", res)
	}
}

// TestAWSEventSourceCoversAllServiceSlices pins the CloudTrail eventSource the
// sim records for every awsJson and query-protocol service slice it implements.
// Real CloudTrail labels each management event with the service's
// `<service>.amazonaws.com` endpoint; LookupEvents supports filtering by
// EventSource, so a wrong/generic source makes those filters silently miss.
func TestAWSEventSourceCoversAllServiceSlices(t *testing.T) {
	jsonCases := map[string]string{
		// X-Amz-Target service prefix -> eventSource
		"DynamoDB_20120810.PutItem":                           "dynamodb.amazonaws.com",
		"AmazonSQS.SendMessage":                               "sqs.amazonaws.com",
		"AmazonSSM.GetParameter":                              "ssm.amazonaws.com",
		"TrentService.Encrypt":                                "kms.amazonaws.com",
		"Kinesis_20131202.PutRecord":                          "kinesis.amazonaws.com",
		"Logs_20140328.FilterLogEvents":                       "logs.amazonaws.com",
		"GraniteServiceVersion20100801.PutMetricData":         "monitoring.amazonaws.com",
		"AWSEvents.PutEvents":                                 "events.amazonaws.com",
		"AWSGlue.GetDatabase":                                 "glue.amazonaws.com",
		"AWSStepFunctions.StartExecution":                     "states.amazonaws.com",
		"AWSWAF_20190729.GetWebACL":                           "wafv2.amazonaws.com",
		"CertificateManager.DescribeCertificate":              "acm.amazonaws.com",
		"ACMPrivateCA.DescribeCertificateAuthority":           "acm-pca.amazonaws.com",
		"Firehose_20150804.PutRecord":                         "firehose.amazonaws.com",
		"CodeBuild_20161006.StartBuild":                       "codebuild.amazonaws.com",
		"AmazonEC2ContainerServiceV20141113.RunTask":          "ecs.amazonaws.com",
		"AmazonEC2ContainerRegistry_V20150921.DescribeImages": "ecr.amazonaws.com",
		"AWSBudgetServiceGateway.CreateBudget":                "budgets.amazonaws.com",
		"AnyScaleFrontendService.RegisterScalableTarget":      "application-autoscaling.amazonaws.com",
		"Route53AutoNaming_v20170314.CreateService":           "servicediscovery.amazonaws.com",
		"CloudTrail_20131101.LookupEvents":                    "cloudtrail.amazonaws.com",
		"secretsmanager.GetSecretValue":                       "secretsmanager.amazonaws.com",
	}
	for target, want := range jsonCases {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("X-Amz-Target", target)
		got, ok := awsEventSource(r)
		if !ok || got != want {
			t.Errorf("awsEventSource(X-Amz-Target=%q) = (%q, %v), want (%q, true)", target, got, ok, want)
		}
	}

	queryCases := map[string]string{
		"2016-11-15": "ec2.amazonaws.com",
		"2011-01-01": "autoscaling.amazonaws.com",
		"2010-08-01": "monitoring.amazonaws.com",
		"2010-03-31": "sns.amazonaws.com",
		"2015-12-01": "elasticloadbalancing.amazonaws.com",
		"2014-10-31": "rds.amazonaws.com",
		"2010-05-08": "iam.amazonaws.com",
		"2011-06-15": "sts.amazonaws.com",
		"2015-02-02": "elasticache.amazonaws.com",
	}
	for version, want := range queryCases {
		form := url.Values{"Action": {"X"}, "Version": {version}}
		r := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		got, ok := awsEventSource(r)
		if !ok || got != want {
			t.Errorf("awsEventSource(Version=%q) = (%q, %v), want (%q, true)", version, got, ok, want)
		}
	}

	// No fabrication: an unmapped service slice must report ok=false rather than
	// fall back to a generic source (the defect a default would reintroduce).
	unknownTarget := httptest.NewRequest("POST", "/", nil)
	unknownTarget.Header.Set("X-Amz-Target", "SomeNewServiceV2.DoThing")
	if src, ok := awsEventSource(unknownTarget); ok {
		t.Errorf("awsEventSource(unknown target) = (%q, true), want ok=false", src)
	}
	unknownQuery := httptest.NewRequest("POST", "/", strings.NewReader("Action=X&Version=1999-01-01"))
	unknownQuery.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if src, ok := awsEventSource(unknownQuery); ok {
		t.Errorf("awsEventSource(unknown version) = (%q, true), want ok=false", src)
	}
}
