package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise the resource-based IAM policy surface via the aws CLI:
// an S3 bucket policy, a Lambda function policy, an SNS topic policy, and an SQS
// queue policy. Each set/get round-trips the policy document; the sim also
// mirrors it into the central resource-policy store the IAM enforcement gate
// consults.

func TestS3CLI_BucketPolicyRoundtrip(t *testing.T) {
	bucket := "cli-bucket-policy"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	t.Cleanup(func() {
		_ = awsCLI("s3api", "delete-bucket-policy", "--bucket", bucket).Run()
		runCLI(t, awsCLI("s3api", "delete-bucket", "--bucket", bucket))
	})

	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"PublicRead","Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::` + bucket + `/*"}]}`
	runCLI(t, awsCLI("s3api", "put-bucket-policy", "--bucket", bucket, "--policy", policy))

	out := runCLI(t, awsCLI("s3api", "get-bucket-policy", "--bucket", bucket))
	assert.Contains(t, out, "s3:GetObject")
	assert.Contains(t, out, "PublicRead")

	runCLI(t, awsCLI("s3api", "delete-bucket-policy", "--bucket", bucket))

	out = runCLIExpectError(t, awsCLI("s3api", "get-bucket-policy", "--bucket", bucket))
	assert.Contains(t, out, "NoSuchBucketPolicy")
}

func TestLambdaCLI_AddGetRemovePermission(t *testing.T) {
	zipPath := createDummyZip(t)
	fn := "cli-policy-func"
	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", fn,
		"--runtime", "nodejs18.x",
		"--role", "arn:aws:iam::123456789012:role/test-role",
		"--handler", "index.handler",
		"--zip-file", "fileb://"+zipPath,
		"--output", "json",
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("lambda", "delete-function", "--function-name", fn))
	})

	runCLI(t, awsCLI("lambda", "add-permission",
		"--function-name", fn,
		"--statement-id", "AllowEvents",
		"--action", "lambda:InvokeFunction",
		"--principal", "events.amazonaws.com",
		"--source-arn", "arn:aws:events:us-east-1:000000000000:rule/cli-rule",
		"--output", "json",
	))

	out := runCLI(t, awsCLI("lambda", "get-policy", "--function-name", fn, "--output", "json"))
	assert.Contains(t, out, "AllowEvents")
	assert.Contains(t, out, "lambda:InvokeFunction")

	runCLI(t, awsCLI("lambda", "remove-permission",
		"--function-name", fn,
		"--statement-id", "AllowEvents",
	))

	out = runCLIExpectError(t, awsCLI("lambda", "get-policy", "--function-name", fn))
	assert.Contains(t, out, "ResourceNotFoundException")
}

func TestSNSCLI_TopicPolicyAttribute(t *testing.T) {
	out := runCLI(t, awsCLI("sns", "create-topic", "--name", "cli-policy-topic", "--output", "json"))
	var topic struct {
		TopicArn string `json:"TopicArn"`
	}
	parseJSON(t, out, &topic)
	require.NotEmpty(t, topic.TopicArn)
	t.Cleanup(func() {
		_ = awsCLI("sns", "delete-topic", "--topic-arn", topic.TopicArn).Run()
	})

	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"AllowPublish","Effect":"Allow","Principal":{"AWS":"*"},"Action":"sns:Publish","Resource":"` + topic.TopicArn + `"}]}`
	runCLI(t, awsCLI("sns", "set-topic-attributes",
		"--topic-arn", topic.TopicArn,
		"--attribute-name", "Policy",
		"--attribute-value", policy,
	))

	out = runCLI(t, awsCLI("sns", "get-topic-attributes", "--topic-arn", topic.TopicArn, "--output", "json"))
	assert.Contains(t, out, "sns:Publish")
	assert.Contains(t, out, "AllowPublish")
}

func TestSQSCLI_QueuePolicyAttribute(t *testing.T) {
	out := runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-policy-queue", "--output", "json"))
	var created struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.QueueUrl)
	t.Cleanup(func() {
		_ = awsCLI("sqs", "delete-queue", "--queue-url", created.QueueUrl).Run()
	})

	out = runCLI(t, awsCLI("sqs", "get-queue-attributes",
		"--queue-url", created.QueueUrl,
		"--attribute-names", "QueueArn",
		"--output", "json",
	))
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrs)
	queueARN := attrs.Attributes["QueueArn"]
	require.NotEmpty(t, queueARN)

	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"AllowSend","Effect":"Allow","Principal":{"AWS":"*"},"Action":"sqs:SendMessage","Resource":"` + queueARN + `"}]}`
	attrsJSON := `{"Policy":` + jsonQuoted(policy) + `}`
	runCLI(t, awsCLI("sqs", "set-queue-attributes",
		"--queue-url", created.QueueUrl,
		"--attributes", attrsJSON,
	))

	out = runCLI(t, awsCLI("sqs", "get-queue-attributes",
		"--queue-url", created.QueueUrl,
		"--attribute-names", "Policy",
		"--output", "json",
	))
	assert.Contains(t, out, "sqs:SendMessage")
	assert.Contains(t, out, "AllowSend")
}

// jsonQuoted JSON-encodes a string so it can be embedded as a nested JSON
// value (the SQS --attributes map carries the policy as a JSON string).
func jsonQuoted(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}
