package aws_sdk_test

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Init phase of the AWS Lambda execution environment lifecycle is bounded
// by its own limit, not by the function's timeout:
//
//	"The Init phase ends when the runtime and all extensions signal that they
//	 are ready by sending a Next API request. The Init phase is limited to 10
//	 seconds. If all three tasks do not complete within 10 seconds, Lambda
//	 retries the Init phase at the time of the first function invocation with
//	 the configured function timeout."
//
//	"The function's timeout setting limits the duration of the entire Invoke
//	 phase."
//
// (AWS Lambda Developer Guide, "Understanding the Lambda execution environment
// lifecycle".) The same guide's worked example of a three-second function
// timing out reports `Duration: 3004.92 ms … Init Duration: 111.23 ms`, so the
// initialisation that preceded the invocation is counted beside the duration
// the timeout bounds rather than inside it.

// lambdaSlowInitZip builds a deployment package whose module-level code — the
// function's static initialisation, which runs in the Init phase — blocks for
// initMillis before the handler is even defined. Atomics.wait is Node's
// synchronous sleep, so the delay is a real initialisation that the runtime
// cannot report readiness through.
func lambdaSlowInitZip(t *testing.T, initMillis int) []byte {
	t.Helper()
	return lambdaNodeSourceZip(t, fmt.Sprintf(
		"Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, %d);\n"+
			"exports.handler = async (event) => ({ initialized: true });\n", initMillis))
}

// lambdaInvocationLog returns the newest CloudWatch log stream of a function,
// joined, which is where START / END / REPORT / INIT_REPORT land.
func lambdaInvocationLog(t *testing.T, functionName string) string {
	t.Helper()
	cw := cloudwatchlogs.NewFromConfig(sdkConfig(), func(o *cloudwatchlogs.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
	group := "/aws/lambda/" + functionName
	streams, err := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(group),
		OrderBy:      cwltypes.OrderByLastEventTime,
		Descending:   aws.Bool(true),
		Limit:        aws.Int32(1),
	})
	require.NoError(t, err)
	require.Len(t, streams.LogStreams, 1)
	events, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: streams.LogStreams[0].LogStreamName,
		StartFromHead: aws.Bool(true),
	})
	require.NoError(t, err)
	lines := make([]string, 0, len(events.Events))
	for _, event := range events.Events {
		lines = append(lines, aws.ToString(event.Message))
	}
	return strings.Join(lines, "\n")
}

// lambdaReportEntry returns the REPORT line that closes an invocation.
func lambdaReportEntry(t *testing.T, log string) string {
	t.Helper()
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "REPORT RequestId:") {
			return line
		}
	}
	t.Fatalf("no REPORT entry in the invocation log:\n%s", log)
	return ""
}

// lambdaReportMillis reads one `<field>: <n> ms` measurement out of a REPORT
// line. The fields are tab separated, so a field name that is a suffix of
// another (Duration inside Billed Duration) still matches only its own.
func lambdaReportMillis(t *testing.T, report, field string) float64 {
	t.Helper()
	pattern := regexp.MustCompile(`(?:^|\t)` + regexp.QuoteMeta(field) + `: ([0-9.]+) ms`)
	match := pattern.FindStringSubmatch(report)
	require.Len(t, match, 2, "REPORT must carry %q:\n%s", field, report)
	value, err := strconv.ParseFloat(match[1], 64)
	require.NoError(t, err)
	return value
}

// TestLambda_InitPhaseIsOutsideTheFunctionTimeout runs a function whose
// initialisation is deliberately slower than its configured timeout. Lambda
// bounds initialisation by the Init phase's own limit, so the handler — which
// returns immediately — must still succeed, and the REPORT must account for the
// initialisation separately from the duration the timeout limits.
func TestLambda_InitPhaseIsOutsideTheFunctionTimeout(t *testing.T) {
	lc := lambdaClient()
	const fnName = "slow-init-inside-init-limit"

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs18x,
		Handler:      aws.String("index.handler"),
		Timeout:      aws.Int32(3),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaSlowInitZip(t, 5000)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})
	})

	out, err := lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(fnName),
		Payload:      []byte(`{}`),
	})
	require.NoError(t, err)
	require.Empty(t, aws.ToString(out.FunctionError),
		"a five-second initialisation must not consume the three-second invocation budget: %s", out.Payload)
	assert.JSONEq(t, `{"initialized":true}`, string(out.Payload))

	log := lambdaInvocationLog(t, fnName)
	assert.NotContains(t, log, "Task timed out")
	report := lambdaReportEntry(t, log)
	initMillis := lambdaReportMillis(t, report, "Init Duration")
	durationMillis := lambdaReportMillis(t, report, "Duration")
	assert.Greater(t, initMillis, 4500.0,
		"the reported Init Duration must be the initialisation that actually ran:\n%s", report)
	assert.Less(t, durationMillis, 3000.0,
		"Duration is the Invoke phase alone, so it must exclude the initialisation:\n%s", report)
	billed := lambdaReportMillis(t, report, "Billed Duration")
	assert.InDelta(t, billed, initMillis+durationMillis, 2.0,
		"Billed Duration is the invocation and the initialisation together:\n%s", report)
}

// TestLambda_InitPhaseBeyondItsLimitIsRetriedInsideTheFunctionTimeout covers
// the other side of the same rule: an initialisation that overruns the Init
// phase's ten-second limit is retried "at the time of the first function
// invocation with the configured function timeout", so the retried attempt is
// the one the three-second timeout bounds and the invocation ends as a timeout.
func TestLambda_InitPhaseBeyondItsLimitIsRetriedInsideTheFunctionTimeout(t *testing.T) {
	lc := lambdaClient()
	const fnName = "slow-init-beyond-init-limit"

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs18x,
		Handler:      aws.String("index.handler"),
		Timeout:      aws.Int32(3),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaSlowInitZip(t, 60000)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})
	})

	out, err := lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	assert.Equal(t, "Unhandled", aws.ToString(out.FunctionError))
	assert.Contains(t, string(out.Payload), "Task timed out after 3.00 seconds")

	log := lambdaInvocationLog(t, fnName)
	assert.Contains(t, log, "Phase: init Status: timeout",
		"an initialisation past the Init phase limit must be reported as an INIT_REPORT timeout:\n%s", log)
	assert.NotContains(t, lambdaReportEntry(t, log), "Init Duration:",
		"a retried initialisation runs inside the function timeout, so REPORT carries no separate Init Duration:\n%s", log)
}
