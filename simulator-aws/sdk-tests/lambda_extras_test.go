package aws_sdk_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lambdaExtrasFunc creates a throwaway function for the subresource tests.
func lambdaExtrasFunc(t *testing.T, lc *lambda.Client, name string) {
	t.Helper()
	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(name),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(name)})
	})
}

func TestLambda_EventSourceMappings(t *testing.T) {
	lc := lambdaClient()
	sqsC := sqsClient()
	fn := "esm-fn"
	lambdaExtrasFunc(t, lc, fn)
	queue, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("esm-queue")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl})
	})
	queueARN := queueARNOf(t, sqsC, queue.QueueUrl)

	cre, err := lc.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{
		FunctionName:   aws.String(fn),
		EventSourceArn: aws.String(queueARN),
		BatchSize:      aws.Int32(10),
	})
	require.NoError(t, err)
	uuid := aws.ToString(cre.UUID)
	require.NotEmpty(t, uuid)

	_, err = lc.GetEventSourceMapping(ctx, &lambda.GetEventSourceMappingInput{UUID: aws.String(uuid)})
	require.NoError(t, err)

	lst, err := lc.ListEventSourceMappings(ctx, &lambda.ListEventSourceMappingsInput{FunctionName: aws.String(fn)})
	require.NoError(t, err)
	assert.NotEmpty(t, lst.EventSourceMappings)

	_, err = lc.UpdateEventSourceMapping(ctx, &lambda.UpdateEventSourceMappingInput{
		UUID: aws.String(uuid), BatchSize: aws.Int32(5),
	})
	require.NoError(t, err)

	_, err = lc.DeleteEventSourceMapping(ctx, &lambda.DeleteEventSourceMappingInput{UUID: aws.String(uuid)})
	require.NoError(t, err)
}

func TestLambda_Layers(t *testing.T) {
	lc := lambdaClient()
	archive := lambdaDeploymentZip(t)

	pub, err := lc.PublishLayerVersion(ctx, &lambda.PublishLayerVersionInput{
		LayerName: aws.String("my-layer"),
		Content:   &lambdatypes.LayerVersionContentInput{ZipFile: archive},
	})
	require.NoError(t, err)
	ver := pub.Version
	require.NotZero(t, ver)
	require.NotNil(t, pub.Content)
	download, err := http.Get(aws.ToString(pub.Content.Location))
	require.NoError(t, err)
	defer download.Body.Close()
	downloaded, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, download.StatusCode, string(downloaded))
	require.Equal(t, archive, downloaded)

	_, err = lc.GetLayerVersion(ctx, &lambda.GetLayerVersionInput{
		LayerName: aws.String("my-layer"), VersionNumber: aws.Int64(ver),
	})
	require.NoError(t, err)

	lst, err := lc.ListLayerVersions(ctx, &lambda.ListLayerVersionsInput{LayerName: aws.String("my-layer")})
	require.NoError(t, err)
	assert.NotEmpty(t, lst.LayerVersions)

	all, err := lc.ListLayers(ctx, &lambda.ListLayersInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, all.Layers)

	_, err = lc.DeleteLayerVersion(ctx, &lambda.DeleteLayerVersionInput{
		LayerName: aws.String("my-layer"), VersionNumber: aws.Int64(ver),
	})
	require.NoError(t, err)
}

func TestLambda_ConcurrencyAndUrlList(t *testing.T) {
	lc := lambdaClient()
	fn := "conc-fn"
	lambdaExtrasFunc(t, lc, fn)

	_, err := lc.PutFunctionConcurrency(ctx, &lambda.PutFunctionConcurrencyInput{
		FunctionName: aws.String(fn), ReservedConcurrentExecutions: aws.Int32(5),
	})
	require.NoError(t, err)

	got, err := lc.GetFunctionConcurrency(ctx, &lambda.GetFunctionConcurrencyInput{FunctionName: aws.String(fn)})
	require.NoError(t, err)
	assert.Equal(t, int32(5), aws.ToInt32(got.ReservedConcurrentExecutions))

	function, err := lc.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fn)})
	require.NoError(t, err)
	require.NotNil(t, function.Concurrency)
	assert.Equal(t, int32(5), aws.ToInt32(function.Concurrency.ReservedConcurrentExecutions))

	_, err = lc.DeleteFunctionConcurrency(ctx, &lambda.DeleteFunctionConcurrencyInput{FunctionName: aws.String(fn)})
	require.NoError(t, err)

	// A function URL config + ListFunctionUrlConfigs.
	_, err = lc.CreateFunctionUrlConfig(ctx, &lambda.CreateFunctionUrlConfigInput{
		FunctionName: aws.String(fn), AuthType: lambdatypes.FunctionUrlAuthTypeNone,
	})
	require.NoError(t, err)
	urls, err := lc.ListFunctionUrlConfigs(ctx, &lambda.ListFunctionUrlConfigsInput{FunctionName: aws.String(fn)})
	require.NoError(t, err)
	assert.NotEmpty(t, urls.FunctionUrlConfigs)
}
