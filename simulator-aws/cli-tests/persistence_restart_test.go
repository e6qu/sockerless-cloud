package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAWSServiceStateSurvivesSimulatorRestart_CLI(t *testing.T) {
	const (
		bucketName    = "cli-persistence-cloudtrail"
		functionName  = "cli-persistence-lambda"
		machineName   = "cli-persistence-state-machine"
		eventBusName  = "cli-persistence-event-bus"
		topicName     = "cli-persistence-topic"
		queueName     = "cli-persistence-queue"
		logGroupName  = "/aws/sockerless/cli-persistence"
		logStreamName = "restart-stream"
		trailName     = "cli-persistence-trail"
		scheduleName  = "cli-persistence-schedule"
	)

	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucketName))
	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", functionName,
		"--package-type", "Image",
		"--code", "ImageUri=example.invalid/cli-persistence:1",
		"--role", "arn:aws:iam::123456789012:role/cli-persistence-lambda"))

	var stateMachine struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	parseJSON(t, runCLI(t, awsCLI("stepfunctions", "create-state-machine",
		"--name", machineName,
		"--definition", `{"StartAt":"Persisted","States":{"Persisted":{"Type":"Succeed"}}}`,
		"--role-arn", "arn:aws:iam::123456789012:role/cli-persistence-step-functions",
		"--output", "json")), &stateMachine)

	runCLI(t, awsCLI("events", "create-event-bus", "--name", eventBusName))
	var topic struct {
		TopicArn string `json:"TopicArn"`
	}
	parseJSON(t, runCLI(t, awsCLI("sns", "create-topic", "--name", topicName, "--output", "json")), &topic)
	var queue struct {
		QueueURL string `json:"QueueUrl"`
	}
	parseJSON(t, runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", queueName, "--output", "json")), &queue)
	runCLI(t, awsCLI("sqs", "send-message", "--queue-url", queue.QueueURL, "--message-body", "survived"))
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", logGroupName))
	runCLI(t, awsCLI("logs", "create-log-stream",
		"--log-group-name", logGroupName, "--log-stream-name", logStreamName))
	var firstLogWrite struct {
		NextSequenceToken string `json:"nextSequenceToken"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "put-log-events",
		"--log-group-name", logGroupName,
		"--log-stream-name", logStreamName,
		"--log-events", fmt.Sprintf(
			`[{"timestamp":%d,"message":"before restart"}]`,
			time.Now().UnixMilli(),
		),
		"--output", "json")), &firstLogWrite)
	runCLI(t, awsCLI("cloudtrail", "create-trail", "--name", trailName, "--s3-bucket-name", bucketName))
	runCLI(t, awsCLI("scheduler", "create-schedule",
		"--name", scheduleName,
		"--schedule-expression", "at(2099-01-01T00:00:00)",
		"--flexible-time-window", `{"Mode":"OFF"}`,
		"--target", `{"Arn":"arn:aws:lambda:us-east-1:123456789012:function:`+functionName+`","RoleArn":"arn:aws:iam::123456789012:role/cli-persistence-scheduler"}`))

	restartCLISimulator(t)

	runCLI(t, awsCLI("s3api", "head-bucket", "--bucket", bucketName))
	assertCLIJSONField(t, runCLI(t, awsCLI("lambda", "get-function", "--function-name", functionName)),
		"Configuration.FunctionName", functionName)
	assertCLIJSONField(t, runCLI(t, awsCLI("stepfunctions", "describe-state-machine",
		"--state-machine-arn", stateMachine.StateMachineArn)), "name", machineName)
	assertCLIJSONField(t, runCLI(t, awsCLI("events", "describe-event-bus", "--name", eventBusName)),
		"Name", eventBusName)
	assertCLIJSONField(t, runCLI(t, awsCLI("sns", "get-topic-attributes", "--topic-arn", topic.TopicArn)),
		"Attributes.TopicArn", topic.TopicArn)
	assertCLIJSONField(t, runCLI(t, awsCLI("sqs", "get-queue-attributes",
		"--queue-url", queue.QueueURL, "--attribute-names", "QueueArn")),
		"Attributes.QueueArn", "arn:aws:sqs:us-east-1:123456789012:"+queueName)
	assertCLIJSONField(t, runCLI(t, awsCLI("logs", "describe-log-groups",
		"--log-group-name-prefix", logGroupName)), "logGroups.0.logGroupName", logGroupName)
	var persistedStreams struct {
		LogStreams []struct {
			UploadSequenceToken string `json:"uploadSequenceToken"`
		} `json:"logStreams"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "describe-log-streams",
		"--log-group-name", logGroupName,
		"--log-stream-name-prefix", logStreamName,
		"--output", "json")), &persistedStreams)
	if len(persistedStreams.LogStreams) != 1 ||
		persistedStreams.LogStreams[0].UploadSequenceToken != firstLogWrite.NextSequenceToken {
		t.Fatalf("persisted CloudWatch Logs sequence token mismatch: %#v", persistedStreams.LogStreams)
	}
	var secondLogWrite struct {
		NextSequenceToken string `json:"nextSequenceToken"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "put-log-events",
		"--log-group-name", logGroupName,
		"--log-stream-name", logStreamName,
		"--log-events", fmt.Sprintf(
			`[{"timestamp":%d,"message":"after restart"}]`,
			time.Now().UnixMilli(),
		),
		"--output", "json")), &secondLogWrite)
	if secondLogWrite.NextSequenceToken == firstLogWrite.NextSequenceToken {
		t.Fatalf("CloudWatch Logs reused sequence token %q after restart", firstLogWrite.NextSequenceToken)
	}
	var persistedLogEvents struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "get-log-events",
		"--log-group-name", logGroupName,
		"--log-stream-name", logStreamName,
		"--start-from-head",
		"--output", "json")), &persistedLogEvents)
	if len(persistedLogEvents.Events) != 2 ||
		persistedLogEvents.Events[0].Message != "before restart" ||
		persistedLogEvents.Events[1].Message != "after restart" {
		t.Fatalf("persisted CloudWatch Logs events mismatch: %#v", persistedLogEvents.Events)
	}
	assertCLIJSONField(t, runCLI(t, awsCLI("cloudtrail", "get-trail", "--name", trailName)),
		"Trail.Name", trailName)
	assertCLIJSONField(t, runCLI(t, awsCLI("scheduler", "get-schedule", "--name", scheduleName)),
		"Name", scheduleName)

	var received struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	parseJSON(t, runCLI(t, awsCLI("sqs", "receive-message", "--queue-url", queue.QueueURL, "--output", "json")), &received)
	if len(received.Messages) != 1 || received.Messages[0].Body != "survived" {
		t.Fatalf("persisted SQS message mismatch: %#v", received.Messages)
	}
}

func assertCLIJSONField(t *testing.T, raw, path, want string) {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("decode AWS CLI JSON: %v\n%s", err, raw)
	}
	current := value
	for _, part := range strings.Split(path, ".") {
		switch typed := current.(type) {
		case map[string]any:
			current = typed[part]
		case []any:
			if part != "0" || len(typed) == 0 {
				t.Fatalf("path %q did not resolve in %s", path, raw)
			}
			current = typed[0]
		default:
			t.Fatalf("path %q did not resolve in %s", path, raw)
		}
	}
	if got, ok := current.(string); !ok || got != want {
		t.Fatalf("%s = %#v, want %q", path, current, want)
	}
}
