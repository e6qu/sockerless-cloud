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

func TestCloudFrontCachePolicyLifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	name := "cp-" + time.Now().Format("150405.000000")
	createOut, err := c.CreateCachePolicy(ctx, &cloudfront.CreateCachePolicyInput{
		CachePolicyConfig: &cftypes.CachePolicyConfig{
			Name:       aws.String(name),
			Comment:    aws.String("sdk cache policy test"),
			DefaultTTL: aws.Int64(86400),
			MaxTTL:     aws.Int64(31536000),
			MinTTL:     aws.Int64(1),
			ParametersInCacheKeyAndForwardedToOrigin: &cftypes.ParametersInCacheKeyAndForwardedToOrigin{
				EnableAcceptEncodingGzip:   aws.Bool(true),
				EnableAcceptEncodingBrotli: aws.Bool(true),
				HeadersConfig: &cftypes.CachePolicyHeadersConfig{
					HeaderBehavior: cftypes.CachePolicyHeaderBehaviorNone,
				},
				CookiesConfig: &cftypes.CachePolicyCookiesConfig{
					CookieBehavior: cftypes.CachePolicyCookieBehaviorNone,
				},
				QueryStringsConfig: &cftypes.CachePolicyQueryStringsConfig{
					QueryStringBehavior: cftypes.CachePolicyQueryStringBehaviorNone,
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.CachePolicy)
	id := aws.ToString(createOut.CachePolicy.Id)
	etag := aws.ToString(createOut.ETag)
	require.NotEmpty(t, id)
	require.NotEmpty(t, etag)
	assert.Equal(t, name, aws.ToString(createOut.CachePolicy.CachePolicyConfig.Name))

	getOut, err := c.GetCachePolicy(ctx, &cloudfront.GetCachePolicyInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(getOut.CachePolicy.Id))

	listOut, err := c.ListCachePolicies(ctx, &cloudfront.ListCachePoliciesInput{})
	require.NoError(t, err)
	found := false
	for _, s := range listOut.CachePolicyList.Items {
		if aws.ToString(s.CachePolicy.Id) == id {
			found = true
		}
	}
	assert.True(t, found, "expected new cache policy %q in list", id)

	_, err = c.DeleteCachePolicy(ctx, &cloudfront.DeleteCachePolicyInput{
		Id:      aws.String(id),
		IfMatch: aws.String(etag),
	})
	require.NoError(t, err)

	_, err = c.GetCachePolicy(ctx, &cloudfront.GetCachePolicyInput{Id: aws.String(id)})
	require.Error(t, err, "get after delete must fail")
}

func TestCloudFrontOriginRequestPolicyLifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	name := "orp-" + time.Now().Format("150405.000000")
	createOut, err := c.CreateOriginRequestPolicy(ctx, &cloudfront.CreateOriginRequestPolicyInput{
		OriginRequestPolicyConfig: &cftypes.OriginRequestPolicyConfig{
			Name:    aws.String(name),
			Comment: aws.String("sdk origin request policy test"),
			HeadersConfig: &cftypes.OriginRequestPolicyHeadersConfig{
				HeaderBehavior: cftypes.OriginRequestPolicyHeaderBehaviorNone,
			},
			CookiesConfig: &cftypes.OriginRequestPolicyCookiesConfig{
				CookieBehavior: cftypes.OriginRequestPolicyCookieBehaviorNone,
			},
			QueryStringsConfig: &cftypes.OriginRequestPolicyQueryStringsConfig{
				QueryStringBehavior: cftypes.OriginRequestPolicyQueryStringBehaviorNone,
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.OriginRequestPolicy)
	id := aws.ToString(createOut.OriginRequestPolicy.Id)
	etag := aws.ToString(createOut.ETag)
	require.NotEmpty(t, id)
	require.NotEmpty(t, etag)

	_, err = c.GetOriginRequestPolicy(ctx, &cloudfront.GetOriginRequestPolicyInput{Id: aws.String(id)})
	require.NoError(t, err)

	listOut, err := c.ListOriginRequestPolicies(ctx, &cloudfront.ListOriginRequestPoliciesInput{})
	require.NoError(t, err)
	found := false
	for _, s := range listOut.OriginRequestPolicyList.Items {
		if aws.ToString(s.OriginRequestPolicy.Id) == id {
			found = true
		}
	}
	assert.True(t, found)

	_, err = c.DeleteOriginRequestPolicy(ctx, &cloudfront.DeleteOriginRequestPolicyInput{
		Id:      aws.String(id),
		IfMatch: aws.String(etag),
	})
	require.NoError(t, err)
}

func TestCloudFrontResponseHeadersPolicyLifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	name := "rhp-" + time.Now().Format("150405.000000")
	createOut, err := c.CreateResponseHeadersPolicy(ctx, &cloudfront.CreateResponseHeadersPolicyInput{
		ResponseHeadersPolicyConfig: &cftypes.ResponseHeadersPolicyConfig{
			Name:    aws.String(name),
			Comment: aws.String("sdk response headers policy test"),
			SecurityHeadersConfig: &cftypes.ResponseHeadersPolicySecurityHeadersConfig{
				ContentTypeOptions: &cftypes.ResponseHeadersPolicyContentTypeOptions{
					Override: aws.Bool(true),
				},
				FrameOptions: &cftypes.ResponseHeadersPolicyFrameOptions{
					Override:    aws.Bool(true),
					FrameOption: cftypes.FrameOptionsListDeny,
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.ResponseHeadersPolicy)
	id := aws.ToString(createOut.ResponseHeadersPolicy.Id)
	etag := aws.ToString(createOut.ETag)
	require.NotEmpty(t, id)
	require.NotEmpty(t, etag)

	_, err = c.GetResponseHeadersPolicy(ctx, &cloudfront.GetResponseHeadersPolicyInput{Id: aws.String(id)})
	require.NoError(t, err)

	listOut, err := c.ListResponseHeadersPolicies(ctx, &cloudfront.ListResponseHeadersPoliciesInput{})
	require.NoError(t, err)
	found := false
	for _, s := range listOut.ResponseHeadersPolicyList.Items {
		if aws.ToString(s.ResponseHeadersPolicy.Id) == id {
			found = true
		}
	}
	assert.True(t, found)

	_, err = c.DeleteResponseHeadersPolicy(ctx, &cloudfront.DeleteResponseHeadersPolicyInput{
		Id:      aws.String(id),
		IfMatch: aws.String(etag),
	})
	require.NoError(t, err)
}
