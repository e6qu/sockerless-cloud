package aws_cli_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createDummyZip(t *testing.T) string {
	t.Helper()
	zipPath := filepath.Join(tmpDir, "lambda-func.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	fw, err := w.Create("index.js")
	require.NoError(t, err)
	fw.Write([]byte(`exports.handler = async () => ({ statusCode: 200, body: "hello" });`))
	w.Close()
	f.Close()
	return zipPath
}

func createLambdaSourceZip(t *testing.T, name, source string) string {
	t.Helper()
	zipPath := filepath.Join(tmpDir, name+".zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	fw, err := w.Create("index.js")
	require.NoError(t, err)
	_, err = fw.Write([]byte(source))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
	return zipPath
}

func TestLambda_CreateAndGetFunction(t *testing.T) {
	t.Parallel()
	zipPath := createDummyZip(t)

	out := runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", "cli-test-func",
		"--runtime", "nodejs18.x",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"),
		"--handler", "index.handler",
		"--zip-file", "fileb://"+zipPath,
		"--output", "json",
	))

	var createResult struct {
		FunctionName string `json:"FunctionName"`
		FunctionArn  string `json:"FunctionArn"`
		Runtime      string `json:"Runtime"`
		State        string `json:"State"`
	}
	parseJSON(t, out, &createResult)
	assert.Equal(t, "cli-test-func", createResult.FunctionName)
	assert.Equal(t, "nodejs18.x", createResult.Runtime)

	// Get function
	out = runCLI(t, awsCLI("lambda", "get-function",
		"--function-name", "cli-test-func",
		"--output", "json",
	))

	var getResult struct {
		Configuration struct {
			FunctionName string `json:"FunctionName"`
			Runtime      string `json:"Runtime"`
		} `json:"Configuration"`
	}
	parseJSON(t, out, &getResult)
	assert.Equal(t, "cli-test-func", getResult.Configuration.FunctionName)

	// Cleanup
	runCLI(t, awsCLI("lambda", "delete-function", "--function-name", "cli-test-func"))
}

// TestLambda_CheckpointAndCallbackOperationsCLI exercises the AWS CLI
// CheckpointDurableExecution, SendDurableExecutionCallbackHeartbeat, and
// SendDurableExecutionCallbackSuccess operations against a running function.
func TestLambda_CheckpointAndCallbackOperationsCLI(t *testing.T) {
	t.Parallel()
	const fn = "cli-durable-callback-fn"
	source := `
exports.handler = async (event) => {
  const callback = event.InitialExecutionState.Operations.find(
    (operation) => operation.Type === "CALLBACK"
  );
  if (callback?.Status === "SUCCEEDED") {
    return {Status: "SUCCEEDED", Result: callback.CallbackDetails.Result};
  }
  console.log("CHECKPOINT_TOKEN=" + event.CheckpointToken);
  console.log("DURABLE_ARN=" + event.DurableExecutionArn);
  return {Status: "PENDING"};
};`
	zipPath := createLambdaSourceZip(t, fn, source)
	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", fn,
		"--runtime", "nodejs20.x",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"),
		"--handler", "index.handler",
		"--zip-file", "fileb://"+zipPath,
		"--durable-config", "ExecutionTimeout=60,RetentionPeriodInDays=1",
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("lambda", "delete-function", "--function-name", fn))
	})

	payloadPath := filepath.Join(tmpDir, fn+"-payload.json")
	resultPath := filepath.Join(tmpDir, fn+"-result.json")
	require.NoError(t, os.WriteFile(payloadPath, []byte(`{"orderId":"CLI-1"}`), 0600))
	invoke := awsCLI("lambda", "invoke",
		"--function-name", fn+":$LATEST",
		"--durable-execution-name", "cli-callback-replay",
		"--cli-binary-format", "raw-in-base64-out",
		"--payload", "fileb://"+payloadPath,
		resultPath,
	)
	var invokeOutput bytes.Buffer
	invoke.Stdout = &invokeOutput
	invoke.Stderr = &invokeOutput
	require.NoError(t, invoke.Start())
	invokeDone := make(chan error, 1)
	go func() { invokeDone <- invoke.Wait() }()
	t.Cleanup(func() {
		if invoke.ProcessState == nil {
			_ = invoke.Process.Kill()
		}
	})

	var checkpointToken, durableARN string
	require.Eventually(t, func() bool {
		select {
		case invokeErr := <-invokeDone:
			t.Fatalf("AWS CLI durable Invoke ended before its callback: %v\n%s", invokeErr, invokeOutput.String())
		default:
		}
		streamJSON, streamErr := awsCLI("logs", "describe-log-streams",
			"--log-group-name", "/aws/lambda/"+fn,
			"--output", "json",
		).CombinedOutput()
		if streamErr != nil {
			return false
		}
		var streams struct {
			LogStreams []struct {
				LogStreamName string `json:"logStreamName"`
			} `json:"logStreams"`
		}
		if json.Unmarshal(streamJSON, &streams) != nil {
			return false
		}
		for _, stream := range streams.LogStreams {
			eventJSON, eventErr := awsCLI("logs", "get-log-events",
				"--log-group-name", "/aws/lambda/"+fn,
				"--log-stream-name", stream.LogStreamName,
				"--output", "json",
			).CombinedOutput()
			if eventErr != nil {
				continue
			}
			var events struct {
				Events []struct {
					Message string `json:"message"`
				} `json:"events"`
			}
			if json.Unmarshal(eventJSON, &events) != nil {
				continue
			}
			for _, event := range events.Events {
				if strings.Contains(event.Message, "CHECKPOINT_TOKEN=") {
					checkpointToken = strings.TrimSpace(strings.SplitN(event.Message, "CHECKPOINT_TOKEN=", 2)[1])
				}
				if strings.Contains(event.Message, "DURABLE_ARN=") {
					durableARN = strings.TrimSpace(strings.SplitN(event.Message, "DURABLE_ARN=", 2)[1])
				}
			}
		}
		return checkpointToken != "" && durableARN != ""
	}, 20*time.Second, 200*time.Millisecond)

	checkpointJSON := runCLI(t, awsCLI("lambda", "checkpoint-durable-execution",
		"--durable-execution-arn", durableARN,
		"--checkpoint-token", checkpointToken,
		"--updates", `[{"Id":"approval","Name":"approval","Type":"CALLBACK","Action":"START","CallbackOptions":{"HeartbeatTimeoutSeconds":10,"TimeoutSeconds":30}}]`,
		"--output", "json",
	))
	var checkpoint struct {
		NewExecutionState struct {
			Operations []struct {
				Type            string `json:"Type"`
				CallbackDetails struct {
					CallbackID string `json:"CallbackId"`
				} `json:"CallbackDetails"`
			} `json:"Operations"`
		} `json:"NewExecutionState"`
	}
	require.NoError(t, json.Unmarshal([]byte(checkpointJSON), &checkpoint))
	var callbackID string
	for _, operation := range checkpoint.NewExecutionState.Operations {
		if operation.Type == "CALLBACK" {
			callbackID = operation.CallbackDetails.CallbackID
		}
	}
	require.NotEmpty(t, callbackID)
	runCLI(t, awsCLI("lambda", "send-durable-execution-callback-heartbeat",
		"--callback-id", callbackID,
	))
	callbackResult := filepath.Join(tmpDir, fn+"-callback-result.json")
	require.NoError(t, os.WriteFile(callbackResult, []byte(`{"approved":true}`), 0600))
	runCLI(t, awsCLI("lambda", "send-durable-execution-callback-success",
		"--callback-id", callbackID,
		"--result", "fileb://"+callbackResult,
	))

	select {
	case err := <-invokeDone:
		require.NoError(t, err, invokeOutput.String())
	case <-time.After(20 * time.Second):
		_ = invoke.Process.Kill()
		t.Fatal("AWS CLI synchronous durable Invoke did not complete")
	}
	result, err := os.ReadFile(resultPath)
	require.NoError(t, err)
	assert.JSONEq(t, `{"approved":true}`, string(result))
	assert.Contains(t, invokeOutput.String(), durableARN)
}

func TestLambda_GetFunctionCodeSigningConfigWithoutAttachment(t *testing.T) {
	t.Parallel()
	zipPath := createDummyZip(t)
	fnName := "cli-csc-unconfigured-func"

	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", fnName,
		"--runtime", "nodejs18.x",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"),
		"--handler", "index.handler",
		"--zip-file", "fileb://"+zipPath,
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("lambda", "delete-function", "--function-name", fnName))
	})

	out := runCLI(t, awsCLI("lambda", "get-function-code-signing-config",
		"--function-name", fnName,
		"--output", "json",
	))
	var result struct {
		CodeSigningConfigArn string `json:"CodeSigningConfigArn"`
		FunctionName         string `json:"FunctionName"`
	}
	parseJSON(t, out, &result)
	assert.Equal(t, fnName, result.FunctionName)
	assert.Empty(t, result.CodeSigningConfigArn)
}

func TestLambda_ListFunctions(t *testing.T) {
	t.Parallel()
	zipPath := createDummyZip(t)

	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", "list-test-func",
		"--runtime", "python3.12",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"),
		"--handler", "handler.handler",
		"--zip-file", "fileb://"+zipPath,
	))

	out := runCLI(t, awsCLI("lambda", "list-functions", "--output", "json"))

	var result struct {
		Functions []struct {
			FunctionName string `json:"FunctionName"`
		} `json:"Functions"`
	}
	parseJSON(t, out, &result)

	found := false
	for _, f := range result.Functions {
		if f.FunctionName == "list-test-func" {
			found = true
		}
	}
	assert.True(t, found, "Expected to find list-test-func in list")

	// Cleanup
	runCLI(t, awsCLI("lambda", "delete-function", "--function-name", "list-test-func"))
}

func TestLambda_InvokeFunction(t *testing.T) {
	t.Parallel()
	zipPath := createDummyZip(t)

	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", "invoke-test-func",
		"--runtime", "nodejs18.x",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"),
		"--handler", "index.handler",
		"--zip-file", "fileb://"+zipPath,
	))

	outFile := filepath.Join(tmpDir, "invoke-output.json")
	out := runCLI(t, awsCLI("lambda", "invoke",
		"--function-name", "invoke-test-func",
		outFile,
		"--output", "json",
	))

	var invokeResult struct {
		StatusCode      int    `json:"StatusCode"`
		ExecutedVersion string `json:"ExecutedVersion"`
	}
	parseJSON(t, out, &invokeResult)
	assert.Equal(t, 200, invokeResult.StatusCode)

	// Cleanup
	runCLI(t, awsCLI("lambda", "delete-function", "--function-name", "invoke-test-func"))
}

func TestLambda_UpdateFunctionConfiguration(t *testing.T) {
	t.Parallel()
	zipPath := createDummyZip(t)

	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", "update-test-func",
		"--runtime", "nodejs18.x",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"),
		"--handler", "index.handler",
		"--zip-file", "fileb://"+zipPath,
	))

	out := runCLI(t, awsCLI("lambda", "update-function-configuration",
		"--function-name", "update-test-func",
		"--memory-size", "256",
		"--timeout", "30",
		"--output", "json",
	))

	var result struct {
		MemorySize int `json:"MemorySize"`
		Timeout    int `json:"Timeout"`
	}
	parseJSON(t, out, &result)
	assert.Equal(t, 256, result.MemorySize)
	assert.Equal(t, 30, result.Timeout)

	// Cleanup
	runCLI(t, awsCLI("lambda", "delete-function", "--function-name", "update-test-func"))
}

func TestLambda_CLI_InvokeAndCheckLogs(t *testing.T) {
	t.Parallel()
	zipPath := createDummyZip(t)

	fnName := "cli-log-invoke-func"

	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", fnName,
		"--runtime", "nodejs18.x",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"),
		"--handler", "index.handler",
		"--zip-file", "fileb://"+zipPath,
	))

	// Invoke the function
	outFile := filepath.Join(tmpDir, "cli-log-invoke-output.json")
	runCLI(t, awsCLI("lambda", "invoke",
		"--function-name", fnName,
		outFile,
		"--output", "json",
	))

	// Query CloudWatch logs for this function
	logGroupName := "/aws/lambda/" + fnName
	out := runCLI(t, awsCLI("logs", "filter-log-events",
		"--log-group-name", logGroupName,
		"--output", "json",
	))

	var logResult struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	parseJSON(t, out, &logResult)
	require.NotEmpty(t, logResult.Events, "expected log events for Lambda invocation")

	// Verify START/END/REPORT log entries
	var messages []string
	for _, e := range logResult.Events {
		messages = append(messages, e.Message)
	}

	hasStart := false
	hasEnd := false
	for _, m := range messages {
		if strings.Contains(m, "START RequestId:") {
			hasStart = true
		}
		if strings.Contains(m, "END RequestId:") {
			hasEnd = true
		}
	}
	assert.True(t, hasStart, "expected START log entry, got: %v", messages)
	assert.True(t, hasEnd, "expected END log entry, got: %v", messages)

	// Cleanup
	runCLI(t, awsCLI("lambda", "delete-function", "--function-name", fnName))
}

// TestLambda_InvokeRuntimeAPI_CLI exercises the Runtime API invoke
// path through the aws CLI: creates an Image-package function whose
// image speaks the real Lambda Runtime API, invokes with a payload,
// and verifies the payload round-trips via /next + /response.
func TestLambda_InvokeRuntimeAPI_CLI(t *testing.T) {
	t.Parallel()
	fnName := "cli-runtime-api-fn"
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.79.0.0/16",
		"--query", "Vpc.VpcId",
		"--output", "text",
	)))
	subnetID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID,
		"--cidr-block", "10.79.1.0/24",
		"--query", "Subnet.SubnetId",
		"--output", "text",
	)))
	securityGroupID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-security-group",
		"--group-name", "cli-lambda-vpc-runtime",
		"--description", "AWS Lambda VPC runtime CLI coverage",
		"--vpc-id", vpcID,
		"--query", "GroupId",
		"--output", "text",
	)))
	defer func() {
		runCLI(t, awsCLI("ec2", "delete-security-group", "--group-id", securityGroupID))
		runCLI(t, awsCLI("ec2", "delete-subnet", "--subnet-id", subnetID))
		runCLI(t, awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID))
	}()

	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", fnName,
		"--package-type", "Image",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"),
		"--code", "ImageUri="+lambdaHandlerImageName,
		"--architectures", nativeLambdaArchitecture(),
		"--vpc-config", "SubnetIds="+subnetID+",SecurityGroupIds="+securityGroupID,
	))
	defer runCLI(t, awsCLI("lambda", "delete-function", "--function-name", fnName))

	payloadFile := filepath.Join(tmpDir, "cli-invoke-payload.json")
	require.NoError(t, os.WriteFile(payloadFile, []byte(`{"msg":"cli-roundtrip"}`), 0644))
	outFile := filepath.Join(tmpDir, "cli-invoke-output.json")

	out := runCLI(t, awsCLI("lambda", "invoke",
		"--function-name", fnName,
		"--payload", "fileb://"+payloadFile,
		"--cli-binary-format", "raw-in-base64-out",
		outFile,
		"--output", "json",
	))
	var invokeResult struct {
		StatusCode    int    `json:"StatusCode"`
		FunctionError string `json:"FunctionError,omitempty"`
	}
	parseJSON(t, out, &invokeResult)
	assert.Equal(t, 200, invokeResult.StatusCode)
	assert.Empty(t, invokeResult.FunctionError, "unexpected function error")

	body, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Contains(t, string(body), "cli-roundtrip",
		"handler should echo payload back via Runtime API /response")
}

// TestLambda_InvokeRuntimeAPIError_CLI exercises the /error path via
// the aws CLI. Payload with "cause":"error" triggers the handler to
// POST /invocation/{id}/error; caller sees FunctionError=Unhandled.
func TestLambda_InvokeRuntimeAPIError_CLI(t *testing.T) {
	t.Parallel()
	fnName := "cli-runtime-api-error-fn"

	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", fnName,
		"--package-type", "Image",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"),
		"--code", "ImageUri="+lambdaHandlerImageName,
		"--architectures", nativeLambdaArchitecture(),
	))
	defer runCLI(t, awsCLI("lambda", "delete-function", "--function-name", fnName))

	payloadFile := filepath.Join(tmpDir, "cli-invoke-error-payload.json")
	require.NoError(t, os.WriteFile(payloadFile, []byte(`{"cause":"error"}`), 0644))
	outFile := filepath.Join(tmpDir, "cli-invoke-error-output.json")

	out := runCLI(t, awsCLI("lambda", "invoke",
		"--function-name", fnName,
		"--payload", "fileb://"+payloadFile,
		"--cli-binary-format", "raw-in-base64-out",
		outFile,
		"--output", "json",
	))
	var invokeResult struct {
		StatusCode    int    `json:"StatusCode"`
		FunctionError string `json:"FunctionError,omitempty"`
	}
	parseJSON(t, out, &invokeResult)
	assert.Equal(t, 200, invokeResult.StatusCode)
	assert.Equal(t, "Unhandled", invokeResult.FunctionError)

	body, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Contains(t, string(body), "test error from handler")
}

func TestLambda_DeleteFunction(t *testing.T) {
	t.Parallel()
	zipPath := createDummyZip(t)

	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", "delete-test-func",
		"--runtime", "nodejs18.x",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"),
		"--handler", "index.handler",
		"--zip-file", "fileb://"+zipPath,
	))

	runCLI(t, awsCLI("lambda", "delete-function", "--function-name", "delete-test-func"))

	// Verify deletion
	out := runCLI(t, awsCLI("lambda", "list-functions", "--output", "json"))
	var result struct {
		Functions []struct {
			FunctionName string `json:"FunctionName"`
		} `json:"Functions"`
	}
	parseJSON(t, out, &result)
	for _, f := range result.Functions {
		assert.NotEqual(t, "delete-test-func", f.FunctionName)
	}
}

// TestLambda_AsyncDestinationDeliversAsTheRole_CLI configures an Amazon SQS
// OnSuccess destination with put-function-event-invoke-config, invokes the
// function asynchronously and receives the invocation record, which AWS
// Lambda sends as the execution role, whose policy allows sqs:SendMessage.
func TestLambda_AsyncDestinationDeliversAsTheRole_CLI(t *testing.T) {
	t.Parallel()
	fnName := uniquename.New("cli-async-destination")
	out := runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", uniquename.New("cli-lambda-destination")))
	var queue struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, out, &queue)
	t.Cleanup(func() { runCLI(t, awsCLI("sqs", "delete-queue", "--queue-url", queue.QueueUrl)) })
	out = runCLI(t, awsCLI("sqs", "get-queue-attributes", "--queue-url", queue.QueueUrl, "--attribute-names", "QueueArn"))
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrs)

	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", fnName,
		"--package-type", "Image",
		"--role", cliRoleAllowing(t, "lambda.amazonaws.com", "sqs:SendMessage"),
		"--code", "ImageUri="+lambdaHandlerImageName,
		"--architectures", nativeLambdaArchitecture(),
	))
	t.Cleanup(func() { runCLI(t, awsCLI("lambda", "delete-function", "--function-name", fnName)) })
	runCLI(t, awsCLI("lambda", "put-function-event-invoke-config",
		"--function-name", fnName,
		"--maximum-retry-attempts", "0",
		"--destination-config", `{"OnSuccess":{"Destination":"`+attrs.Attributes["QueueArn"]+`"}}`,
	))

	payloadFile := filepath.Join(tmpDir, fnName+"-payload.json")
	require.NoError(t, os.WriteFile(payloadFile, []byte(`{"msg":"cli-destination"}`), 0644))
	out = runCLI(t, awsCLI("lambda", "invoke",
		"--function-name", fnName,
		"--invocation-type", "Event",
		"--payload", "fileb://"+payloadFile,
		"--cli-binary-format", "raw-in-base64-out",
		filepath.Join(tmpDir, fnName+"-output.json"),
		"--output", "json",
	))
	var invoked struct {
		StatusCode int `json:"StatusCode"`
	}
	parseJSON(t, out, &invoked)
	assert.Equal(t, 202, invoked.StatusCode)

	// receive-message long-polls the queue; the record arrives once the
	// asynchronous invocation ran, which a cold runtime may take longer than
	// one poll's twenty seconds to do.
	var received struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	deadline := time.Now().Add(90 * time.Second)
	for len(received.Messages) == 0 && time.Now().Before(deadline) {
		out = runCLI(t, awsCLI("sqs", "receive-message", "--queue-url", queue.QueueUrl, "--wait-time-seconds", "20"))
		if strings.TrimSpace(out) != "" {
			parseJSON(t, out, &received)
		}
	}
	require.Len(t, received.Messages, 1, "the invocation record must reach the destination queue")
	var record struct {
		RequestContext map[string]any `json:"requestContext"`
		RequestPayload map[string]any `json:"requestPayload"`
	}
	require.NoError(t, json.Unmarshal([]byte(received.Messages[0].Body), &record))
	assert.Equal(t, "Success", record.RequestContext["condition"])
	assert.Equal(t, "cli-destination", record.RequestPayload["msg"])
}
