package aws_sdk_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudFront_ConnectionFunction_Lifecycle exercises the full connection
// function CRUD + lifecycle surface (Create, Get, Describe, Update, Publish,
// Test, List, Delete) plus ListDistributionsByConnectionFunction, all through
// the real aws-sdk-go-v2 CloudFront client against the simulator.
func TestCloudFront_ConnectionFunction_Lifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	name := "cnxfn-" + time.Now().Format("150405.000000")
	code := []byte("function handler(event) { return event; }")

	createOut, err := c.CreateConnectionFunction(ctx, &cloudfront.CreateConnectionFunctionInput{
		Name:                   aws.String(name),
		ConnectionFunctionCode: code,
		ConnectionFunctionConfig: &cftypes.FunctionConfig{
			Comment: aws.String("sdk connection function test"),
			Runtime: cftypes.FunctionRuntimeCloudfrontJs20,
		},
		Tags: &cftypes.Tags{Items: []cftypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}}},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.ConnectionFunctionSummary)
	id := aws.ToString(createOut.ConnectionFunctionSummary.Id)
	require.NotEmpty(t, id)
	require.NotEmpty(t, aws.ToString(createOut.ETag))
	assert.Equal(t, "DEVELOPMENT", string(createOut.ConnectionFunctionSummary.Stage))
	assert.Contains(t, aws.ToString(createOut.ConnectionFunctionSummary.ConnectionFunctionArn), "connection-function/")

	defer func() {
		// Tolerant cleanup: best-effort delete with whatever the latest ETag is.
		desc, derr := c.DescribeConnectionFunction(ctx, &cloudfront.DescribeConnectionFunctionInput{Identifier: aws.String(id)})
		if derr == nil {
			_, _ = c.DeleteConnectionFunction(ctx, &cloudfront.DeleteConnectionFunctionInput{Id: aws.String(id), IfMatch: desc.ETag})
		}
	}()

	// Get returns the code as the payload.
	getOut, err := c.GetConnectionFunction(ctx, &cloudfront.GetConnectionFunctionInput{Identifier: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, code, getOut.ConnectionFunctionCode)
	assert.NotEmpty(t, aws.ToString(getOut.ETag))

	// Describe returns the summary + ETag.
	descOut, err := c.DescribeConnectionFunction(ctx, &cloudfront.DescribeConnectionFunctionInput{Identifier: aws.String(id)})
	require.NoError(t, err)
	require.NotNil(t, descOut.ConnectionFunctionSummary)
	assert.Equal(t, name, aws.ToString(descOut.ConnectionFunctionSummary.Name))
	etag := aws.ToString(descOut.ETag)
	require.NotEmpty(t, etag)

	// Update — bumps the ETag.
	newCode := []byte("function handler(event) { event.x = 1; return event; }")
	updOut, err := c.UpdateConnectionFunction(ctx, &cloudfront.UpdateConnectionFunctionInput{
		Id:                     aws.String(id),
		IfMatch:                aws.String(etag),
		ConnectionFunctionCode: newCode,
		ConnectionFunctionConfig: &cftypes.FunctionConfig{
			Comment: aws.String("updated"),
			Runtime: cftypes.FunctionRuntimeCloudfrontJs20,
		},
	})
	require.NoError(t, err)
	updETag := aws.ToString(updOut.ETag)
	require.NotEmpty(t, updETag)
	assert.NotEqual(t, etag, updETag)

	// Test — returns a real-shaped test result.
	testOut, err := c.TestConnectionFunction(ctx, &cloudfront.TestConnectionFunctionInput{
		Id:               aws.String(id),
		IfMatch:          aws.String(updETag),
		ConnectionObject: []byte(`{"request":{}}`),
	})
	require.NoError(t, err)
	require.NotNil(t, testOut.ConnectionFunctionTestResult)
	assert.NotNil(t, testOut.ConnectionFunctionTestResult.ConnectionFunctionSummary)

	// Publish — promotes DEVELOPMENT → LIVE.
	pubOut, err := c.PublishConnectionFunction(ctx, &cloudfront.PublishConnectionFunctionInput{
		Id:      aws.String(id),
		IfMatch: aws.String(updETag),
	})
	require.NoError(t, err)
	require.NotNil(t, pubOut.ConnectionFunctionSummary)
	assert.Equal(t, "LIVE", string(pubOut.ConnectionFunctionSummary.Stage))

	// List — finds the function.
	listOut, err := c.ListConnectionFunctions(ctx, &cloudfront.ListConnectionFunctionsInput{})
	require.NoError(t, err)
	found := false
	for _, s := range listOut.ConnectionFunctions {
		if aws.ToString(s.Id) == id {
			found = true
		}
	}
	assert.True(t, found, "created connection function should appear in ListConnectionFunctions")

	// ListDistributionsByConnectionFunction — honest empty list.
	byFnOut, err := c.ListDistributionsByConnectionFunction(ctx, &cloudfront.ListDistributionsByConnectionFunctionInput{
		ConnectionFunctionIdentifier: aws.String(id),
	})
	require.NoError(t, err)
	require.NotNil(t, byFnOut.DistributionList)
	assert.Equal(t, int32(0), aws.ToInt32(byFnOut.DistributionList.Quantity))
}

// TestCloudFront_TestFunction exercises the CloudFront Functions TestFunction
// op against a real stored function.
func TestCloudFront_TestFunction(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	name := "fn-" + time.Now().Format("150405.000000")
	createOut, err := c.CreateFunction(ctx, &cloudfront.CreateFunctionInput{
		Name:         aws.String(name),
		FunctionCode: []byte("function handler(event) { return event.request; }"),
		FunctionConfig: &cftypes.FunctionConfig{
			Comment: aws.String("test-function fixture"),
			Runtime: cftypes.FunctionRuntimeCloudfrontJs20,
		},
	})
	require.NoError(t, err)
	etag := aws.ToString(createOut.ETag)
	require.NotEmpty(t, etag)
	defer func() {
		desc, derr := c.DescribeFunction(ctx, &cloudfront.DescribeFunctionInput{Name: aws.String(name)})
		if derr == nil {
			_, _ = c.DeleteFunction(ctx, &cloudfront.DeleteFunctionInput{Name: aws.String(name), IfMatch: desc.ETag})
		}
	}()

	testOut, err := c.TestFunction(ctx, &cloudfront.TestFunctionInput{
		Name:        aws.String(name),
		IfMatch:     aws.String(etag),
		EventObject: []byte(`{"version":"1.0","request":{"uri":"/"}}`),
	})
	require.NoError(t, err)
	require.NotNil(t, testOut.TestResult)
	require.NotNil(t, testOut.TestResult.FunctionSummary)
	assert.Equal(t, name, aws.ToString(testOut.TestResult.FunctionSummary.Name))
}

// TestCloudFront_TagResource exercises Tag/UntagResource + ListTagsForResource
// against a stored distribution ARN.
func TestCloudFront_TagResource(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	arn, id, etag := cfCreateMinimalDistribution(t, c, ctx)
	defer cfTolerantDeleteDistribution(c, ctx, id, etag)

	_, err := c.TagResource(ctx, &cloudfront.TagResourceInput{
		Resource: aws.String(arn),
		Tags:     &cftypes.Tags{Items: []cftypes.Tag{{Key: aws.String("team"), Value: aws.String("infra")}}},
	})
	require.NoError(t, err)

	listOut, err := c.ListTagsForResource(ctx, &cloudfront.ListTagsForResourceInput{Resource: aws.String(arn)})
	require.NoError(t, err)
	require.NotNil(t, listOut.Tags)
	var teamVal string
	for _, tg := range listOut.Tags.Items {
		if aws.ToString(tg.Key) == "team" {
			teamVal = aws.ToString(tg.Value)
		}
	}
	assert.Equal(t, "infra", teamVal)

	_, err = c.UntagResource(ctx, &cloudfront.UntagResourceInput{
		Resource: aws.String(arn),
		TagKeys:  &cftypes.TagKeys{Items: []string{"team"}},
	})
	require.NoError(t, err)

	listOut2, err := c.ListTagsForResource(ctx, &cloudfront.ListTagsForResourceInput{Resource: aws.String(arn)})
	require.NoError(t, err)
	for _, tg := range listOut2.Tags.Items {
		assert.NotEqual(t, "team", aws.ToString(tg.Key))
	}
}

// TestCloudFront_CreateDistributionVariants exercises CreateDistribution and
// CreateDistributionWithTags (both serviced by the shared POST /distribution
// dynamic dispatcher) plus CreateStreamingDistributionWithTags.
func TestCloudFront_CreateDistributionVariants(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// CreateDistribution (no tags).
	arn, id, etag := cfCreateMinimalDistribution(t, c, ctx)
	require.NotEmpty(t, arn)
	cfTolerantDeleteDistribution(c, ctx, id, etag)

	// CreateDistributionWithTags.
	caller := "withtags-" + time.Now().Format("150405.000000")
	wtOut, err := c.CreateDistributionWithTags(ctx, &cloudfront.CreateDistributionWithTagsInput{
		DistributionConfigWithTags: &cftypes.DistributionConfigWithTags{
			DistributionConfig: cfMinimalDistributionConfig(caller),
			Tags:               &cftypes.Tags{Items: []cftypes.Tag{{Key: aws.String("k"), Value: aws.String("v")}}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, wtOut.Distribution)
	cfTolerantDeleteDistribution(c, ctx, aws.ToString(wtOut.Distribution.Id), aws.ToString(wtOut.ETag))

	// CreateStreamingDistributionWithTags.
	scaller := "stream-wt-" + time.Now().Format("150405.000000")
	swtOut, err := c.CreateStreamingDistributionWithTags(ctx, &cloudfront.CreateStreamingDistributionWithTagsInput{
		StreamingDistributionConfigWithTags: &cftypes.StreamingDistributionConfigWithTags{
			StreamingDistributionConfig: &cftypes.StreamingDistributionConfig{
				CallerReference: aws.String(scaller),
				Comment:         aws.String("streaming withtags"),
				Enabled:         aws.Bool(false),
				S3Origin: &cftypes.S3Origin{
					DomainName:           aws.String("example.s3.amazonaws.com"),
					OriginAccessIdentity: aws.String(""),
				},
				TrustedSigners: &cftypes.TrustedSigners{
					Enabled:  aws.Bool(false),
					Quantity: aws.Int32(0),
				},
			},
			Tags: &cftypes.Tags{Items: []cftypes.Tag{{Key: aws.String("k"), Value: aws.String("v")}}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, swtOut.StreamingDistribution)
	if swtOut.StreamingDistribution != nil {
		_, _ = c.DeleteStreamingDistribution(ctx, &cloudfront.DeleteStreamingDistributionInput{
			Id:      swtOut.StreamingDistribution.Id,
			IfMatch: swtOut.ETag,
		})
	}
}

// ---- helpers ----

func cfMinimalDistributionConfig(caller string) *cftypes.DistributionConfig {
	return &cftypes.DistributionConfig{
		CallerReference: aws.String(caller),
		Comment:         aws.String("complete-test distribution"),
		Enabled:         aws.Bool(false),
		Origins: &cftypes.Origins{
			Quantity: aws.Int32(1),
			Items: []cftypes.Origin{{
				Id:         aws.String("o1"),
				DomainName: aws.String("example.com"),
				CustomOriginConfig: &cftypes.CustomOriginConfig{
					HTTPPort:             aws.Int32(80),
					HTTPSPort:            aws.Int32(443),
					OriginProtocolPolicy: cftypes.OriginProtocolPolicyHttpOnly,
					OriginSslProtocols: &cftypes.OriginSslProtocols{
						Quantity: aws.Int32(1),
						Items:    []cftypes.SslProtocol{cftypes.SslProtocolTLSv12},
					},
				},
			}},
		},
		DefaultCacheBehavior: &cftypes.DefaultCacheBehavior{
			TargetOriginId:       aws.String("o1"),
			ViewerProtocolPolicy: cftypes.ViewerProtocolPolicyAllowAll,
			ForwardedValues: &cftypes.ForwardedValues{
				QueryString: aws.Bool(false),
				Cookies:     &cftypes.CookiePreference{Forward: cftypes.ItemSelectionNone},
			},
			MinTTL: aws.Int64(0),
		},
	}
}

func cfCreateMinimalDistribution(t *testing.T, c *cloudfront.Client, ctx context.Context) (arn, id, etag string) {
	t.Helper()
	caller := "complete-" + time.Now().Format("150405.000000")
	out, err := c.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{
		DistributionConfig: cfMinimalDistributionConfig(caller),
	})
	require.NoError(t, err)
	require.NotNil(t, out.Distribution)
	return aws.ToString(out.Distribution.ARN), aws.ToString(out.Distribution.Id), aws.ToString(out.ETag)
}

func cfTolerantDeleteDistribution(c *cloudfront.Client, ctx context.Context, id, etag string) {
	if id == "" {
		return
	}
	// The distribution was created disabled, so it can be deleted directly.
	_, _ = c.DeleteDistribution(ctx, &cloudfront.DeleteDistributionInput{Id: aws.String(id), IfMatch: aws.String(etag)})
}
