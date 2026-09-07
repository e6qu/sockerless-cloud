package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A function whose image is an Amazon ECR reference under a pull-through
// cache rule runs the image the rule fetches from its upstream — here the
// ECR Public Gallery — as Lambda runs it after ECR hydrates the cache.
func TestLambda_ImageThroughPullThroughCacheRule(t *testing.T) {
	lc := lambdaClient()
	cw := cwLogsClient()
	ec := ecrClient()

	_, err := ec.CreatePullThroughCacheRule(ctx, &ecr.CreatePullThroughCacheRuleInput{
		EcrRepositoryPrefix: aws.String("public-ecr-aws"),
		UpstreamRegistryUrl: aws.String("public.ecr.aws"),
	})
	if err != nil {
		require.Contains(t, err.Error(), "PullThroughCacheRuleAlreadyExistsException")
	}

	fnName := "cache-rule-image-fn"
	_, err = lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String("123456789012.dkr.ecr.us-east-1.amazonaws.com/public-ecr-aws/docker/library/alpine:latest")},
		ImageConfig:  &lambdatypes.ImageConfig{Command: []string{"sh", "-c", "echo through-the-cache-rule"}},
	})
	require.NoError(t, err)
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})

	invokeOut, err := lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	assert.NotContains(t, string(invokeOut.Payload), "pull access denied", "the host must run the rule's upstream image, not a Docker Hub name spelt from the cache path")

	logGroupName := "/aws/lambda/" + fnName
	events, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(logGroupName)})
	require.NoError(t, err)
	var messages []string
	for _, e := range events.Events {
		messages = append(messages, *e.Message)
	}
	assert.True(t, strings.Contains(strings.Join(messages, "\n"), "through-the-cache-rule"), "the image's command ran: %v", messages)
	cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroupName)})
}
