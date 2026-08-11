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

func TestCloudFrontFunctionLifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	name := "fn-" + time.Now().Format("150405.000000")
	code := []byte(`function handler(event) { return event.request; }`)

	createOut, err := c.CreateFunction(ctx, &cloudfront.CreateFunctionInput{
		Name:         aws.String(name),
		FunctionCode: code,
		FunctionConfig: &cftypes.FunctionConfig{
			Comment: aws.String("sdk test fn"),
			Runtime: cftypes.FunctionRuntimeCloudfrontJs20,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.FunctionSummary)
	etag := aws.ToString(createOut.ETag)
	require.NotEmpty(t, etag)
	assert.Equal(t, name, aws.ToString(createOut.FunctionSummary.Name))
	assert.Equal(t, cftypes.FunctionStageDevelopment, createOut.FunctionSummary.FunctionMetadata.Stage)

	descOut, err := c.DescribeFunction(ctx, &cloudfront.DescribeFunctionInput{Name: aws.String(name)})
	require.NoError(t, err)
	require.NotNil(t, descOut.FunctionSummary)
	assert.Equal(t, name, aws.ToString(descOut.FunctionSummary.Name))

	listOut, err := c.ListFunctions(ctx, &cloudfront.ListFunctionsInput{})
	require.NoError(t, err)
	found := false
	for _, s := range listOut.FunctionList.Items {
		if aws.ToString(s.Name) == name {
			found = true
		}
	}
	assert.True(t, found)

	// Publish DEVELOPMENT → LIVE
	pubOut, err := c.PublishFunction(ctx, &cloudfront.PublishFunctionInput{
		Name:    aws.String(name),
		IfMatch: aws.String(etag),
	})
	require.NoError(t, err)
	assert.Equal(t, cftypes.FunctionStageLive, pubOut.FunctionSummary.FunctionMetadata.Stage)

	// Tags on the published function ARN. Regression: ListTagsForResource
	// resolved only distribution ARNs, so a function ARN 404'd (NoSuchResource),
	// which the AWS provider surfaced intermittently as a terraform apply
	// failure on aws_cloudfront_function. It must return 200 (empty) and
	// round-trip tag/untag.
	fnARN := aws.ToString(pubOut.FunctionSummary.FunctionMetadata.FunctionARN)
	require.NotEmpty(t, fnARN)

	emptyTags, err := c.ListTagsForResource(ctx, &cloudfront.ListTagsForResourceInput{Resource: aws.String(fnARN)})
	require.NoError(t, err, "ListTagsForResource on a function ARN must not 404")
	assert.Empty(t, emptyTags.Tags.Items)

	_, err = c.TagResource(ctx, &cloudfront.TagResourceInput{
		Resource: aws.String(fnARN),
		Tags:     &cftypes.Tags{Items: []cftypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}}},
	})
	require.NoError(t, err)

	taggedOut, err := c.ListTagsForResource(ctx, &cloudfront.ListTagsForResourceInput{Resource: aws.String(fnARN)})
	require.NoError(t, err)
	require.Len(t, taggedOut.Tags.Items, 1)
	assert.Equal(t, "env", aws.ToString(taggedOut.Tags.Items[0].Key))
	assert.Equal(t, "test", aws.ToString(taggedOut.Tags.Items[0].Value))

	_, err = c.UntagResource(ctx, &cloudfront.UntagResourceInput{
		Resource: aws.String(fnARN),
		TagKeys:  &cftypes.TagKeys{Items: []string{"env"}},
	})
	require.NoError(t, err)

	afterUntag, err := c.ListTagsForResource(ctx, &cloudfront.ListTagsForResourceInput{Resource: aws.String(fnARN)})
	require.NoError(t, err)
	assert.Empty(t, afterUntag.Tags.Items)

	// re-describe and pick up the new ETag for delete
	descOut, err = c.DescribeFunction(ctx, &cloudfront.DescribeFunctionInput{Name: aws.String(name)})
	require.NoError(t, err)
	curETag := aws.ToString(descOut.ETag)

	_, err = c.DeleteFunction(ctx, &cloudfront.DeleteFunctionInput{
		Name:    aws.String(name),
		IfMatch: aws.String(curETag),
	})
	require.NoError(t, err)

	_, err = c.DescribeFunction(ctx, &cloudfront.DescribeFunctionInput{Name: aws.String(name)})
	require.Error(t, err, "describe after delete must 404")
}

func TestCloudFrontPublicKeyLifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	name := "pk-" + time.Now().Format("150405.000000")
	caller := "ref-" + time.Now().Format("150405.000000")
	// Test PEM (not a real key — sim doesn't validate).
	encoded := "-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAtest\n-----END PUBLIC KEY-----\n"

	createOut, err := c.CreatePublicKey(ctx, &cloudfront.CreatePublicKeyInput{
		PublicKeyConfig: &cftypes.PublicKeyConfig{
			CallerReference: aws.String(caller),
			Name:            aws.String(name),
			EncodedKey:      aws.String(encoded),
			Comment:         aws.String("sdk test"),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.PublicKey)
	id := aws.ToString(createOut.PublicKey.Id)
	etag := aws.ToString(createOut.ETag)
	require.NotEmpty(t, id)

	getOut, err := c.GetPublicKey(ctx, &cloudfront.GetPublicKeyInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(getOut.PublicKey.PublicKeyConfig.Name))

	listOut, err := c.ListPublicKeys(ctx, &cloudfront.ListPublicKeysInput{})
	require.NoError(t, err)
	found := false
	for _, s := range listOut.PublicKeyList.Items {
		if aws.ToString(s.Id) == id {
			found = true
		}
	}
	assert.True(t, found)

	// Update rides PUT /2020-05-31/public-key/{Id}/config — the /config
	// suffix is part of the real operation URI.
	updOut, err := c.UpdatePublicKey(ctx, &cloudfront.UpdatePublicKeyInput{
		Id:      aws.String(id),
		IfMatch: aws.String(etag),
		PublicKeyConfig: &cftypes.PublicKeyConfig{
			CallerReference: aws.String(caller),
			Name:            aws.String(name),
			EncodedKey:      aws.String(encoded),
			Comment:         aws.String("sdk test updated"),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, updOut.PublicKey)
	etag = aws.ToString(updOut.ETag)

	getOut, err = c.GetPublicKey(ctx, &cloudfront.GetPublicKeyInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, "sdk test updated", aws.ToString(getOut.PublicKey.PublicKeyConfig.Comment))

	_, err = c.DeletePublicKey(ctx, &cloudfront.DeletePublicKeyInput{
		Id:      aws.String(id),
		IfMatch: aws.String(etag),
	})
	require.NoError(t, err)
}

func TestCloudFrontKeyGroupLifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Need a PublicKey to reference.
	pkOut, err := c.CreatePublicKey(ctx, &cloudfront.CreatePublicKeyInput{
		PublicKeyConfig: &cftypes.PublicKeyConfig{
			CallerReference: aws.String("kg-pk-" + time.Now().Format("150405.000000")),
			Name:            aws.String("kg-pk-" + time.Now().Format("150405.000000")),
			EncodedKey:      aws.String("-----BEGIN PUBLIC KEY-----\nKEYDATA\n-----END PUBLIC KEY-----\n"),
		},
	})
	require.NoError(t, err)
	pkID := aws.ToString(pkOut.PublicKey.Id)

	name := "kg-" + time.Now().Format("150405.000000")
	createOut, err := c.CreateKeyGroup(ctx, &cloudfront.CreateKeyGroupInput{
		KeyGroupConfig: &cftypes.KeyGroupConfig{
			Name:    aws.String(name),
			Items:   []string{pkID},
			Comment: aws.String("sdk test"),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.KeyGroup)
	id := aws.ToString(createOut.KeyGroup.Id)
	etag := aws.ToString(createOut.ETag)

	getOut, err := c.GetKeyGroup(ctx, &cloudfront.GetKeyGroupInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(getOut.KeyGroup.KeyGroupConfig.Name))
	assert.Equal(t, []string{pkID}, getOut.KeyGroup.KeyGroupConfig.Items)

	_, err = c.DeleteKeyGroup(ctx, &cloudfront.DeleteKeyGroupInput{
		Id:      aws.String(id),
		IfMatch: aws.String(etag),
	})
	require.NoError(t, err)

	// cleanup the public key
	_, _ = c.DeletePublicKey(ctx, &cloudfront.DeletePublicKeyInput{
		Id:      aws.String(pkID),
		IfMatch: aws.String(aws.ToString(pkOut.ETag)),
	})
}

func TestCloudFrontInvalidationLifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Create a distribution first.
	caller := "inv-" + time.Now().Format("150405.000000")
	dist, err := c.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{
		DistributionConfig: &cftypes.DistributionConfig{
			CallerReference: aws.String(caller),
			Comment:         aws.String("inv test"),
			Enabled:         aws.Bool(false),
			Origins: &cftypes.Origins{
				Quantity: aws.Int32(1),
				Items: []cftypes.Origin{{
					Id:         aws.String("o1"),
					DomainName: aws.String("example.com"),
					CustomOriginConfig: &cftypes.CustomOriginConfig{
						HTTPPort: aws.Int32(80), HTTPSPort: aws.Int32(443),
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
			ViewerCertificate: &cftypes.ViewerCertificate{
				CloudFrontDefaultCertificate: aws.Bool(true),
			},
			Restrictions: &cftypes.Restrictions{
				GeoRestriction: &cftypes.GeoRestriction{
					RestrictionType: cftypes.GeoRestrictionTypeNone,
					Quantity:        aws.Int32(0),
				},
			},
		},
	})
	require.NoError(t, err)
	distID := aws.ToString(dist.Distribution.Id)
	distETag := aws.ToString(dist.ETag)
	defer func() {
		_, _ = c.DeleteDistribution(ctx, &cloudfront.DeleteDistributionInput{
			Id: aws.String(distID), IfMatch: aws.String(distETag),
		})
	}()

	createOut, err := c.CreateInvalidation(ctx, &cloudfront.CreateInvalidationInput{
		DistributionId: aws.String(distID),
		InvalidationBatch: &cftypes.InvalidationBatch{
			CallerReference: aws.String(caller + "-inv"),
			Paths: &cftypes.Paths{
				Quantity: aws.Int32(2),
				Items:    []string{"/index.html", "/assets/*"},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.Invalidation)
	invID := aws.ToString(createOut.Invalidation.Id)
	require.NotEmpty(t, invID)

	getOut, err := c.GetInvalidation(ctx, &cloudfront.GetInvalidationInput{
		DistributionId: aws.String(distID),
		Id:             aws.String(invID),
	})
	require.NoError(t, err)
	assert.Equal(t, invID, aws.ToString(getOut.Invalidation.Id))

	listOut, err := c.ListInvalidations(ctx, &cloudfront.ListInvalidationsInput{
		DistributionId: aws.String(distID),
	})
	require.NoError(t, err)
	found := false
	for _, s := range listOut.InvalidationList.Items {
		if aws.ToString(s.Id) == invID {
			found = true
		}
	}
	assert.True(t, found)
}
