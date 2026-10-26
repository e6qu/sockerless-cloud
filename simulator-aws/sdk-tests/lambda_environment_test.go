package aws_sdk_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AWS Lambda reuses an execution environment that finished an invocation:
// "When the function is invoked again, Lambda thaws the environment for reuse
// ... Objects declared outside of the function's handler method remain
// initialized" (lambda-runtime-environment.md.gz). An environment serves one
// invocation at a time, so concurrent invocations scale out, and reserved
// concurrency caps them: "To intentionally throttle a function, set its
// reserved concurrency to 0" (configuration-concurrency.md.gz).

// createCountingFunction creates an image function on the test handler, whose
// "count" action reports the invocations its execution environment served.
func createCountingFunction(t *testing.T, lc *lambda.Client, name string, publish bool) *lambda.CreateFunctionOutput {
	t.Helper()
	created, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName:  aws.String(name),
		Role:          aws.String(serviceRole(t, "lambda.amazonaws.com")),
		PackageType:   lambdatypes.PackageTypeImage,
		Code:          &lambdatypes.FunctionCode{ImageUri: aws.String(lambdaHandlerImageName)},
		Architectures: nativeLambdaArchitectures(),
		Timeout:       aws.Int32(30),
		Publish:       publish,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(name)})
	})
	return created
}

type environmentCount struct {
	Invocations        int    `json:"invocations"`
	InitializationType string `json:"initializationType"`
}

func invokeCount(t *testing.T, lc *lambda.Client, name, qualifier string) environmentCount {
	t.Helper()
	input := &lambda.InvokeInput{FunctionName: aws.String(name), Payload: []byte(`{"action":"count"}`)}
	if qualifier != "" {
		input.Qualifier = aws.String(qualifier)
	}
	out, err := lc.Invoke(ctx, input)
	require.NoError(t, err)
	require.Empty(t, aws.ToString(out.FunctionError), string(out.Payload))
	var count environmentCount
	require.NoError(t, json.Unmarshal(out.Payload, &count), string(out.Payload))
	return count
}

// invocationGate holds an invocation until the test releases it. The handler's
// "hold" action GETs its URL, which answers only once released, and each
// request it receives tells the test that an invocation is in flight.
type invocationGate struct {
	url     string
	arrived chan struct{}
	release chan struct{}
	once    sync.Once
}

func newInvocationGate(t *testing.T) *invocationGate {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	g := &invocationGate{arrived: make(chan struct{}, 8), release: make(chan struct{})}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.arrived <- struct{}{}
		select {
		case <-g.release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() {
		g.open()
		_ = server.Close()
	})
	g.url = fmt.Sprintf("http://host.docker.internal:%d/hold", ln.Addr().(*net.TCPAddr).Port)
	return g
}

func (g *invocationGate) open() { g.once.Do(func() { close(g.release) }) }

func (g *invocationGate) awaitArrival(t *testing.T) {
	t.Helper()
	select {
	case <-g.arrived:
	case <-time.After(2 * time.Minute):
		t.Fatal("the held invocation never reached the gate")
	}
}

func TestLambda_ExecutionEnvironmentIsReused(t *testing.T) {
	t.Parallel()
	lc := lambdaClient()
	name := uniquename.New("env-reuse-fn")
	createCountingFunction(t, lc, name, false)

	for want := 1; want <= 3; want++ {
		count := invokeCount(t, lc, name, "")
		assert.Equal(t, want, count.Invocations, "invocation %d must run in the environment the earlier ones initialised", want)
		assert.Equal(t, "on-demand", count.InitializationType)
	}

	cw := cwLogsClient()
	streams, err := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{LogGroupName: aws.String("/aws/lambda/" + name)})
	require.NoError(t, err)
	require.Len(t, streams.LogStreams, 1, "one execution environment writes one log stream")
	events, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName: aws.String("/aws/lambda/" + name), LogStreamName: streams.LogStreams[0].LogStreamName, StartFromHead: aws.Bool(true),
	})
	require.NoError(t, err)
	var reports []string
	for _, event := range events.Events {
		if strings.HasPrefix(aws.ToString(event.Message), "REPORT RequestId:") {
			reports = append(reports, aws.ToString(event.Message))
		}
	}
	require.Len(t, reports, 3)
	assert.Contains(t, reports[0], "Init Duration:", "the cold start reports its initialisation")
	assert.NotContains(t, reports[1], "Init Duration:", "a warm invocation has no initialisation to report")
	assert.NotContains(t, reports[2], "Init Duration:")
}

func TestLambda_ConfigurationUpdateStartsNewEnvironments(t *testing.T) {
	t.Parallel()
	lc := lambdaClient()
	const name = "env-update-fn"
	createCountingFunction(t, lc, name, false)
	require.Equal(t, 1, invokeCount(t, lc, name, "").Invocations)
	require.Equal(t, 2, invokeCount(t, lc, name, "").Invocations)

	_, err := lc.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
		FunctionName: aws.String(name),
		Environment:  &lambdatypes.Environment{Variables: map[string]string{"GENERATION": "2"}},
	})
	require.NoError(t, err)
	require.NoError(t, lambda.NewFunctionUpdatedV2Waiter(lc, func(o *lambda.FunctionUpdatedV2WaiterOptions) {
		o.MinDelay, o.MaxDelay = waiterMinDelay, waiterMaxDelay
	}).Wait(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(name)}, 2*time.Minute))

	assert.Equal(t, 1, invokeCount(t, lc, name, "").Invocations,
		"an invocation after a configuration update runs on a new execution environment")
}

func TestLambda_ReservedConcurrencyThrottles(t *testing.T) {
	t.Parallel()
	lc := lambdaClient()
	// The SDK retries a throttle on its own; this client reports the first.
	once := lambda.NewFromConfig(sdkConfig(), func(o *lambda.Options) {
		o.BaseEndpoint = aws.String(baseURL)
		o.RetryMaxAttempts = 1
	})
	const name = "env-throttle-fn"
	createCountingFunction(t, lc, name, false)
	_, err := lc.PutFunctionConcurrency(ctx, &lambda.PutFunctionConcurrencyInput{
		FunctionName: aws.String(name), ReservedConcurrentExecutions: aws.Int32(1),
	})
	require.NoError(t, err)

	gate := newInvocationGate(t)
	held := make(chan *lambda.InvokeOutput, 1)
	heldErr := make(chan error, 1)
	go func() {
		out, err := lc.Invoke(ctx, &lambda.InvokeInput{
			FunctionName: aws.String(name), Payload: []byte(`{"action":"hold","url":"` + gate.url + `"}`),
		})
		heldErr <- err
		held <- out
	}()
	gate.awaitArrival(t)

	_, err = once.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(name), Payload: []byte(`{"action":"count"}`)})
	var throttled *lambdatypes.TooManyRequestsException
	require.True(t, errors.As(err, &throttled), "an invocation past reserved concurrency of 1: %v", err)
	assert.Equal(t, lambdatypes.ThrottleReasonReservedFunctionConcurrentInvocationLimitExceeded, throttled.Reason)

	gate.open()
	require.NoError(t, <-heldErr)
	out := <-held
	require.Empty(t, aws.ToString(out.FunctionError), string(out.Payload))
	assert.JSONEq(t, `{"invocations":1}`, string(out.Payload))
	assert.Equal(t, 2, invokeCount(t, lc, name, "").Invocations,
		"once the held invocation ended, the next reuses its environment")

	_, err = lc.PutFunctionConcurrency(ctx, &lambda.PutFunctionConcurrencyInput{
		FunctionName: aws.String(name), ReservedConcurrentExecutions: aws.Int32(0),
	})
	require.NoError(t, err)
	_, err = once.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(name), Payload: []byte(`{"action":"count"}`)})
	require.True(t, errors.As(err, &throttled), "reserved concurrency of 0 throttles every invocation: %v", err)

	_, err = lc.DeleteFunctionConcurrency(ctx, &lambda.DeleteFunctionConcurrencyInput{FunctionName: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, 3, invokeCount(t, lc, name, "").Invocations)
}

// A throttled asynchronous invocation goes back to the queue — "For
// throttling errors (429) ... Lambda returns the event to the queue and
// attempts to run the function again for up to 6 hours by default"
// (invocation-async-error-handling.md.gz) — until its maximum age, when the
// on-failure destination receives it.
func TestLambda_ThrottledAsyncInvocationRetriesUntilItsAge(t *testing.T) {
	t.Parallel()
	lc := lambdaClient()
	sqsC := sqsClient()
	const name = "env-async-throttle-fn"
	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName:  aws.String(name),
		Role:          aws.String(lambdaRole(t, "sqs:SendMessage")),
		PackageType:   lambdatypes.PackageTypeImage,
		Code:          &lambdatypes.FunctionCode{ImageUri: aws.String(lambdaHandlerImageName)},
		Architectures: nativeLambdaArchitectures(),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(name)})
	})
	queue, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("env-async-throttle-failures")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl}) })
	attributes, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: queue.QueueUrl, AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueARN := attributes.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]

	_, err = lc.PutFunctionConcurrency(ctx, &lambda.PutFunctionConcurrencyInput{
		FunctionName: aws.String(name), ReservedConcurrentExecutions: aws.Int32(0),
	})
	require.NoError(t, err)
	_, err = lc.PutFunctionEventInvokeConfig(ctx, &lambda.PutFunctionEventInvokeConfigInput{
		FunctionName:             aws.String(name),
		MaximumRetryAttempts:     aws.Int32(0),
		MaximumEventAgeInSeconds: aws.Int32(60),
		DestinationConfig:        &lambdatypes.DestinationConfig{OnFailure: &lambdatypes.OnFailure{Destination: aws.String(queueARN)}},
	})
	require.NoError(t, err)

	invoked, err := lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(name), InvocationType: lambdatypes.InvocationTypeEvent, Payload: []byte(`{"action":"count"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(202), invoked.StatusCode)

	body := aws.ToString(receiveSQSMessages(t, sqsC, queue.QueueUrl, 1, 2*time.Minute)[0].Body)
	var record struct {
		RequestContext map[string]any `json:"requestContext"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &record))
	assert.Equal(t, "EventAgeExceeded", record.RequestContext["condition"],
		"a throttled event spends none of MaximumRetryAttempts and ends at its maximum age")
}

// Provisioned concurrency initialises environments before any invocation:
// PutProvisionedConcurrencyConfig answers IN_PROGRESS, the configuration
// reads READY once every environment has initialised, and an invocation then
// finds one warm, with AWS_LAMBDA_INITIALIZATION_TYPE provisioned-concurrency
// (provisioned-concurrency.md.gz).
func TestLambda_ProvisionedConcurrencyWarmsEnvironments(t *testing.T) {
	t.Parallel()
	lc := lambdaClient()
	const name = "env-provisioned-fn"
	created := createCountingFunction(t, lc, name, true)
	version := aws.ToString(created.Version)
	require.NotEqual(t, "$LATEST", version)

	put, err := lc.PutProvisionedConcurrencyConfig(ctx, &lambda.PutProvisionedConcurrencyConfigInput{
		FunctionName: aws.String(name), Qualifier: aws.String(version), ProvisionedConcurrentExecutions: aws.Int32(1),
	})
	require.NoError(t, err)
	assert.Equal(t, lambdatypes.ProvisionedConcurrencyStatusEnumInProgress, put.Status)
	assert.Equal(t, int32(1), aws.ToInt32(put.RequestedProvisionedConcurrentExecutions))

	ready := awaitProvisionedConcurrency(t, lc, name, version)
	assert.Equal(t, int32(1), aws.ToInt32(ready.AllocatedProvisionedConcurrentExecutions))
	assert.Equal(t, int32(1), aws.ToInt32(ready.AvailableProvisionedConcurrentExecutions))

	count := invokeCount(t, lc, name, version)
	assert.Equal(t, 1, count.Invocations)
	assert.Equal(t, "provisioned-concurrency", count.InitializationType,
		"the first invocation of the version runs on its provisioned environment")

	log := lambdaInvocationLog(t, name)
	assert.Contains(t, log, "INIT_REPORT Init Duration:", "a provisioned environment always reports its initialisation")
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "REPORT RequestId:") {
			assert.NotContains(t, line, "Init Duration:", "a warm invocation reports no initialisation")
		}
	}

	_, err = lc.PutProvisionedConcurrencyConfig(ctx, &lambda.PutProvisionedConcurrencyConfigInput{
		FunctionName: aws.String(name), Qualifier: aws.String("$LATEST"), ProvisionedConcurrentExecutions: aws.Int32(1),
	})
	var invalid *lambdatypes.InvalidParameterValueException
	assert.True(t, errors.As(err, &invalid), "provisioned concurrency on $LATEST: %v", err)
}

// awaitProvisionedConcurrency polls the configuration's documented Status until
// READY: the API offers nothing to wait on.
func awaitProvisionedConcurrency(t *testing.T, lc *lambda.Client, name, qualifier string) *lambda.GetProvisionedConcurrencyConfigOutput {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		got, err := lc.GetProvisionedConcurrencyConfig(ctx, &lambda.GetProvisionedConcurrencyConfigInput{
			FunctionName: aws.String(name), Qualifier: aws.String(qualifier),
		})
		require.NoError(t, err)
		switch got.Status {
		case lambdatypes.ProvisionedConcurrencyStatusEnumReady:
			return got
		case lambdatypes.ProvisionedConcurrencyStatusEnumFailed:
			t.Fatalf("provisioned concurrency failed: %s", aws.ToString(got.StatusReason))
		}
		require.True(t, time.Now().Before(deadline), "provisioned concurrency never became READY")
		<-time.After(waiterMinDelay)
	}
}
