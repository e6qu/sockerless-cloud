package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// buildResourcePolicySim builds the full simulator in-process so the
// resource-based policy mirror (S3 bucket policy, Lambda function policy,
// SNS topic policy, SQS queue policy) can be driven through the real
// service handlers and then resolved via iamResourcePolicyDocsForARN —
// the entry point the IAM enforcement gate calls.
func buildResourcePolicySim(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	t.Setenv("SIM_DNS_PORT", "0")
	srv, _, _, err := buildSimulator(sim.Config{
		Provider: "aws", ListenAddr: ":0", LogLevel: "error",
	})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	// Built but never served: its background workers keep reading
	// package-level stores after this test ends, and the next test to build a
	// simulator reassigns those stores underneath them.
	t.Cleanup(srv.StopBackground)
	return srv
}

func doReq(t *testing.T, srv *sim.Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func TestIAMResourcePolicy_S3BucketPolicyMirror(t *testing.T) {
	srv := buildResourcePolicySim(t)
	const bucket = "policy-bucket"
	arn := s3BucketARN(bucket)

	// Create the bucket.
	if rr := doReq(t, srv, httptest.NewRequest("PUT", "/"+bucket, nil)); rr.Code != http.StatusOK {
		t.Fatalf("CreateBucket: status %d body %s", rr.Code, rr.Body.String())
	}

	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"S1","Effect":"Allow","Principal":{"AWS":"*"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::policy-bucket/*"}]}`
	if rr := doReq(t, srv, httptest.NewRequest("PUT", "/"+bucket+"?policy", strings.NewReader(policy))); rr.Code != http.StatusNoContent {
		t.Fatalf("PutBucketPolicy: status %d body %s", rr.Code, rr.Body.String())
	}

	docs := iamResourcePolicyDocsForARN(arn)
	if len(docs) != 1 || len(docs[0].Statement) != 1 || docs[0].Statement[0].Sid != "S1" {
		t.Fatalf("resolver did not return mirrored bucket policy: %+v", docs)
	}

	// GetBucketPolicy round-trips.
	if rr := doReq(t, srv, httptest.NewRequest("GET", "/"+bucket+"?policy", nil)); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "s3:GetObject") {
		t.Fatalf("GetBucketPolicy: status %d body %s", rr.Code, rr.Body.String())
	}

	// DeleteBucketPolicy clears the mirror, and a subsequent GET 404s.
	if rr := doReq(t, srv, httptest.NewRequest("DELETE", "/"+bucket+"?policy", nil)); rr.Code != http.StatusNoContent {
		t.Fatalf("DeleteBucketPolicy: status %d body %s", rr.Code, rr.Body.String())
	}
	if docs := iamResourcePolicyDocsForARN(arn); docs != nil {
		t.Fatalf("resolver still returns a policy after delete: %+v", docs)
	}
	if rr := doReq(t, srv, httptest.NewRequest("GET", "/"+bucket+"?policy", nil)); rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "NoSuchBucketPolicy") {
		t.Fatalf("GetBucketPolicy after delete: status %d body %s", rr.Code, rr.Body.String())
	}
}

func TestIAMResourcePolicy_LambdaPolicyMirror(t *testing.T) {
	srv := buildResourcePolicySim(t)
	const fn = "policy-fn"

	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	entry, err := zipWriter.Create("bootstrap")
	if err != nil {
		t.Fatalf("create Lambda ZIP entry: %v", err)
	}
	if _, err := entry.Write([]byte("#!/bin/sh\n")); err != nil {
		t.Fatalf("write Lambda ZIP entry: %v", err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatalf("close Lambda ZIP: %v", err)
	}
	create := fmt.Sprintf(
		`{"FunctionName":"policy-fn","Role":"arn:aws:iam::000000000000:role/r","Code":{"ZipFile":%q},"Handler":"bootstrap","Runtime":"provided.al2023","PackageType":"Zip"}`,
		base64.StdEncoding.EncodeToString(archive.Bytes()),
	)
	if rr := doReq(t, srv, lambdaReq("POST", "/2015-03-31/functions", create)); rr.Code != http.StatusCreated {
		t.Fatalf("CreateFunction: status %d body %s", rr.Code, rr.Body.String())
	}
	arn := lambdaArn(fn)

	addPerm := `{"StatementId":"AllowInvoke","Action":"lambda:InvokeFunction","Principal":"events.amazonaws.com","SourceArn":"arn:aws:events:us-east-1:000000000000:rule/r"}`
	if rr := doReq(t, srv, lambdaReq("POST", "/2015-03-31/functions/"+fn+"/policy", addPerm)); rr.Code != http.StatusCreated {
		t.Fatalf("AddPermission: status %d body %s", rr.Code, rr.Body.String())
	}

	docs := iamResourcePolicyDocsForARN(arn)
	if len(docs) != 1 || len(docs[0].Statement) != 1 || docs[0].Statement[0].Sid != "AllowInvoke" {
		t.Fatalf("resolver did not return mirrored lambda policy: %+v", docs)
	}

	// GetPolicy contains the statement.
	if rr := doReq(t, srv, lambdaReq("GET", "/2015-03-31/functions/"+fn+"/policy", "")); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "AllowInvoke") {
		t.Fatalf("GetPolicy: status %d body %s", rr.Code, rr.Body.String())
	}

	// RemovePermission empties the policy, so the mirror clears and GetPolicy 404s.
	if rr := doReq(t, srv, lambdaReq("DELETE", "/2015-03-31/functions/"+fn+"/policy/AllowInvoke", "")); rr.Code != http.StatusNoContent {
		t.Fatalf("RemovePermission: status %d body %s", rr.Code, rr.Body.String())
	}
	if docs := iamResourcePolicyDocsForARN(arn); docs != nil {
		t.Fatalf("resolver still returns a lambda policy after RemovePermission: %+v", docs)
	}
	if rr := doReq(t, srv, lambdaReq("GET", "/2015-03-31/functions/"+fn+"/policy", "")); rr.Code != http.StatusNotFound {
		t.Fatalf("GetPolicy after remove: status %d body %s", rr.Code, rr.Body.String())
	}
}

func queryReq(action string, form url.Values) *http.Request {
	form.Set("Action", action)
	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	signSeedControlPlane(req)
	return req
}

func jsonTargetReq(target, body string) *http.Request {
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("X-Amz-Target", target)
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	signSeedControlPlane(req)
	return req
}

// lambdaReq builds a signed Lambda REST request. Lambda's control plane
// (lambda_enforcement.go) authenticates every request the same way the
// awsJson/awsQuery control plane does, so REST calls need the same seeded
// administrator signature queryReq/jsonTargetReq attach for their protocols.
func lambdaReq(method, path, body string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	signSeedControlPlane(req)
	return req
}

func TestIAMResourcePolicy_SNSTopicPolicyMirror(t *testing.T) {
	srv := buildResourcePolicySim(t)

	createForm := url.Values{}
	createForm.Set("Version", "2010-03-31")
	createForm.Set("Name", "policy-topic")
	rr := doReq(t, srv, queryReq("CreateTopic", createForm))
	if rr.Code != http.StatusOK {
		t.Fatalf("CreateTopic: status %d body %s", rr.Code, rr.Body.String())
	}
	i := strings.Index(rr.Body.String(), "<TopicArn>")
	j := strings.Index(rr.Body.String(), "</TopicArn>")
	if i < 0 || j < 0 {
		t.Fatalf("CreateTopic missing TopicArn: %s", rr.Body.String())
	}
	arn := rr.Body.String()[i+len("<TopicArn>") : j]

	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"T1","Effect":"Allow","Principal":{"AWS":"*"},"Action":"sns:Publish","Resource":"` + arn + `"}]}`
	setForm := url.Values{}
	setForm.Set("Version", "2010-03-31")
	setForm.Set("TopicArn", arn)
	setForm.Set("AttributeName", "Policy")
	setForm.Set("AttributeValue", policy)
	if rr := doReq(t, srv, queryReq("SetTopicAttributes", setForm)); rr.Code != http.StatusOK {
		t.Fatalf("SetTopicAttributes: status %d body %s", rr.Code, rr.Body.String())
	}

	docs := iamResourcePolicyDocsForARN(arn)
	if len(docs) != 1 || len(docs[0].Statement) != 1 || docs[0].Statement[0].Sid != "T1" {
		t.Fatalf("resolver did not return mirrored topic policy: %+v", docs)
	}

	// GetTopicAttributes echoes the policy back.
	getForm := url.Values{}
	getForm.Set("Version", "2010-03-31")
	getForm.Set("TopicArn", arn)
	if rr := doReq(t, srv, queryReq("GetTopicAttributes", getForm)); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "sns:Publish") {
		t.Fatalf("GetTopicAttributes: status %d body %s", rr.Code, rr.Body.String())
	}
}

func TestIAMResourcePolicy_SQSQueuePolicyMirror(t *testing.T) {
	srv := buildResourcePolicySim(t)

	rr := doReq(t, srv, jsonTargetReq("AmazonSQS.CreateQueue", `{"QueueName":"policy-queue"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("CreateQueue: status %d body %s", rr.Code, rr.Body.String())
	}
	queueURL := sqsQueueURL("policy-queue")
	arn := sqsQueueARN("policy-queue")

	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"Q1","Effect":"Allow","Principal":{"AWS":"*"},"Action":"sqs:SendMessage","Resource":"` + arn + `"}]}`
	setBody := `{"QueueUrl":"` + queueURL + `","Attributes":{"Policy":` + jsonString(policy) + `}}`
	if rr := doReq(t, srv, jsonTargetReq("AmazonSQS.SetQueueAttributes", setBody)); rr.Code != http.StatusOK {
		t.Fatalf("SetQueueAttributes: status %d body %s", rr.Code, rr.Body.String())
	}

	docs := iamResourcePolicyDocsForARN(arn)
	if len(docs) != 1 || len(docs[0].Statement) != 1 || docs[0].Statement[0].Sid != "Q1" {
		t.Fatalf("resolver did not return mirrored queue policy: %+v", docs)
	}

	// GetQueueAttributes echoes the policy back.
	getBody := `{"QueueUrl":"` + queueURL + `","AttributeNames":["Policy"]}`
	if rr := doReq(t, srv, jsonTargetReq("AmazonSQS.GetQueueAttributes", getBody)); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "sqs:SendMessage") {
		t.Fatalf("GetQueueAttributes: status %d body %s", rr.Code, rr.Body.String())
	}
}

// jsonString JSON-encodes a string so it can be embedded as a JSON value
// (the SQS Attributes map carries the policy as a nested JSON string).
func jsonString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}
