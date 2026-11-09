package logs_subscription_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	ktypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type subscriptionMessage struct {
	Owner               string   `json:"owner"`
	LogGroup            string   `json:"logGroup"`
	LogStream           string   `json:"logStream"`
	SubscriptionFilters []string `json:"subscriptionFilters"`
	MessageType         string   `json:"messageType"`
	LogEvents           []struct {
		Message string `json:"message"`
	} `json:"logEvents"`
	PolicyLevel string `json:"policyLevel"`
}

// decode reads every gzip member of data, as a Firehose object can hold
// several back to back, and keeps the DATA_MESSAGE records.
func decode(t *testing.T, data []byte) []subscriptionMessage {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	raw, err := io.ReadAll(zr)
	require.NoError(t, err)
	var out []subscriptionMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for decoder.More() {
		var m subscriptionMessage
		require.NoError(t, decoder.Decode(&m))
		if m.MessageType == "DATA_MESSAGE" {
			out = append(out, m)
		}
	}
	return out
}

// TestLogsSubscriptionTerraform subscribes one log group to an Amazon Kinesis
// Data Streams stream, an Amazon Data Firehose stream, an AWS Lambda function
// and a CloudWatch Logs destination through terraform-provider-aws, then
// writes events and reads each destination's DATA_MESSAGE back. Deletion
// protection refuses the destroy until it is turned off.
func TestLogsSubscriptionTerraform(t *testing.T) {
	handler, err := filepath.Abs("../../../testdata/lambda-runtime-handler")
	require.NoError(t, err)
	tfsim.BuildGoScratchImage(t, "sockerless-lambda-runtime-handler:aws-terraform", handler, "lambda-runtime-handler")
	architecture := "x86_64"
	if runtime.GOARCH == "arm64" {
		architecture = "arm64"
	}
	vars := func(protected string) []string {
		return []string{"-var", "lambda_architecture=" + architecture, "-var", "deletion_protection=" + protected}
	}

	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, append([]string{"apply", "-auto-approve"}, vars("true")...)...)
	env.Terraform(t, append([]string{"apply", "-auto-approve", "-refresh-only"}, vars("true")...)...)
	outputs := readOutputs(t, env)
	assert.Equal(t, "arn:aws:logs:us-east-1:123456789012:log-group:/tf/logs-subscription", outputs["log_group_arn"])
	assert.Equal(t, "DELIVERY", outputs["archive_class"])
	assert.Equal(t, "2", outputs["archive_retention"], "the log classes guide keeps a Delivery group's events two days")
	assert.Equal(t, "Random", outputs["kinesis_distribution"])
	assert.Equal(t, "ByLogStream", outputs["firehose_distribution"])
	assert.Equal(t, "arn:aws:logs:us-east-1:123456789012:destination:tfDestination", outputs["destination_arn"])
	env.Terraform(t, append([]string{"plan", "-detailed-exitcode"}, vars("true")...)...)

	ctx := context.Background()
	creds := credentials.NewStaticCredentialsProvider("test", "test", "")
	logs := cloudwatchlogs.New(cloudwatchlogs.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), Credentials: creds, HTTPClient: env.Client})
	streams := kinesis.New(kinesis.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), Credentials: creds, HTTPClient: env.Client})
	queues := sqs.New(sqs.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), Credentials: creds, HTTPClient: env.Client})
	buckets := s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), Credentials: creds, HTTPClient: env.Client})

	const group, logStream = "/tf/logs-subscription", "app"
	_, err = logs.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String(logStream)})
	require.NoError(t, err)
	root := `{"userIdentity":{"type":"Root"}}`
	now := time.Now().UnixMilli()
	_, err = logs.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(logStream),
		LogEvents: []cwltypes.InputLogEvent{
			{Timestamp: aws.Int64(now), Message: aws.String(root)},
			{Timestamp: aws.Int64(now), Message: aws.String("ERROR disk full")},
			{Timestamp: aws.Int64(now), Message: aws.String("hello lambda")},
			{Timestamp: aws.Int64(now), Message: aws.String("shared with the recipient")},
		},
	})
	require.NoError(t, err)

	readStream := func(name string) subscriptionMessage {
		iterator, err := streams.GetShardIterator(ctx, &kinesis.GetShardIteratorInput{
			StreamName: aws.String(name), ShardId: aws.String("shardId-000000000000"), ShardIteratorType: ktypes.ShardIteratorTypeTrimHorizon,
		})
		require.NoError(t, err)
		next := iterator.ShardIterator
		var got []subscriptionMessage
		require.Eventually(t, func() bool {
			out, err := streams.GetRecords(ctx, &kinesis.GetRecordsInput{ShardIterator: next})
			if err != nil {
				return false
			}
			next = out.NextShardIterator
			for _, record := range out.Records {
				got = append(got, decode(t, record.Data)...)
			}
			return len(got) > 0
		}, 30*time.Second, 200*time.Millisecond, "stream %s received no DATA_MESSAGE", name)
		return got[0]
	}
	direct := readStream("tf-RootAccess")
	assert.Equal(t, []string{"RootAccess"}, direct.SubscriptionFilters)
	require.Len(t, direct.LogEvents, 1)
	assert.Equal(t, root, direct.LogEvents[0].Message)
	shared := readStream("tf-RecipientStream")
	assert.Equal(t, []string{"RecipientStream"}, shared.SubscriptionFilters)
	require.Len(t, shared.LogEvents, 1)
	assert.Equal(t, "shared with the recipient", shared.LogEvents[0].Message)

	var archived []subscriptionMessage
	require.Eventually(t, func() bool {
		objects, err := buckets.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("tf-logs-subscription-archive"), Prefix: aws.String("firehose/")})
		if err != nil {
			return false
		}
		archived = nil
		for _, object := range objects.Contents {
			got, err := buckets.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("tf-logs-subscription-archive"), Key: object.Key})
			require.NoError(t, err)
			body, err := io.ReadAll(got.Body)
			_ = got.Body.Close()
			require.NoError(t, err)
			archived = append(archived, decode(t, body)...)
		}
		return len(archived) > 0
	}, 60*time.Second, 500*time.Millisecond, "the Firehose stream wrote no DATA_MESSAGE to the bucket")
	assert.Equal(t, []string{"Destination"}, archived[0].SubscriptionFilters)
	require.Len(t, archived[0].LogEvents, 1)
	assert.Equal(t, "ERROR disk full", archived[0].LogEvents[0].Message)

	queueURL, err := queues.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String("tf-logs-subscriber-invocations")})
	require.NoError(t, err)
	var received *sqs.ReceiveMessageOutput
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		received, err = queues.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: queueURL.QueueUrl, WaitTimeSeconds: 20})
		require.NoError(t, err)
		if len(received.Messages) > 0 {
			break
		}
	}
	require.Len(t, received.Messages, 1, "the function's success destination received no invocation record")
	var record struct {
		RequestPayload struct {
			Awslogs struct {
				Data string `json:"data"`
			} `json:"awslogs"`
		} `json:"requestPayload"`
	}
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(received.Messages[0].Body)), &record))
	data, err := base64.StdEncoding.DecodeString(record.RequestPayload.Awslogs.Data)
	require.NoError(t, err)
	invoked := decode(t, data)
	require.Len(t, invoked, 1)
	assert.Equal(t, []string{"demo"}, invoked[0].SubscriptionFilters)
	require.Len(t, invoked[0].LogEvents, 1)
	assert.Equal(t, "hello lambda", invoked[0].LogEvents[0].Message)

	const sender = "/tf/logs-account-sender"
	_, err = logs.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(sender), LogStreamName: aws.String(logStream)})
	require.NoError(t, err)
	_, err = logs.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName: aws.String(sender), LogStreamName: aws.String(logStream),
		LogEvents: []cwltypes.InputLogEvent{{Timestamp: aws.Int64(time.Now().UnixMilli()), Message: aws.String("Simple AccountPolicyTest")}},
	})
	require.NoError(t, err)
	account := readStream("tf-AccountPolicy")
	assert.Equal(t, sender, account.LogGroup)
	assert.Equal(t, []string{"ExamplePolicy"}, account.SubscriptionFilters)
	assert.Equal(t, "ACCOUNT_LEVEL_POLICY", account.PolicyLevel)

	refused := env.TerraformFails(t, append([]string{"destroy", "-auto-approve"}, vars("true")...)...)
	assert.Contains(t, string(refused), "Cannot delete log group with deletion protection enabled")
	env.Terraform(t, append([]string{"apply", "-auto-approve"}, vars("false")...)...)
	env.Terraform(t, append([]string{"destroy", "-auto-approve"}, vars("false")...)...)
}

func readOutputs(t *testing.T, env *tfsim.Env) map[string]string {
	t.Helper()
	var raw map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &raw))
	out := map[string]string{}
	for key, value := range raw {
		out[key] = value.Value
	}
	return out
}
