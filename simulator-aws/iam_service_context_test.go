package main

import "testing"

// TestIAMServiceInitiated proves Service-principal matching + the aws:SourceArn
// condition: an AWS-service-initiated call is authorized by the target's
// resource policy only when the calling service AND the source ARN match.
func TestIAMServiceInitiated(t *testing.T) {
	const fnArn = "arn:aws:lambda:us-east-1:123456789012:function:f"
	const topicArn = "arn:aws:sns:us-east-1:123456789012:t"
	doc, err := parseIAMPolicy(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
		`"Principal":{"Service":"sns.amazonaws.com"},"Action":"lambda:InvokeFunction","Resource":"` + fnArn + `",` +
		`"Condition":{"ArnLike":{"aws:SourceArn":"` + topicArn + `"}}}]}`)
	if err != nil {
		t.Fatalf("parse policy: %v", err)
	}
	docs := []iamPolicyDoc{doc}

	// Right service + right source → allowed.
	if !iamEvalServiceInitiated(docs, "lambda:InvokeFunction", fnArn,
		iamServiceSource{Service: "sns.amazonaws.com", SourceArn: topicArn, SourceAccount: "123456789012"}) {
		t.Error("SNS with the granted source topic must be authorized")
	}
	// Wrong source ARN → denied by the condition.
	if iamEvalServiceInitiated(docs, "lambda:InvokeFunction", fnArn,
		iamServiceSource{Service: "sns.amazonaws.com", SourceArn: "arn:aws:sns:us-east-1:123456789012:other"}) {
		t.Error("a different source topic must be denied")
	}
	// Wrong calling service → Principal:{Service} doesn't match.
	if iamEvalServiceInitiated(docs, "lambda:InvokeFunction", fnArn,
		iamServiceSource{Service: "s3.amazonaws.com", SourceArn: topicArn}) {
		t.Error("a different calling service must be denied")
	}
}
