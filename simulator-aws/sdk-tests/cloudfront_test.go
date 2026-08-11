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

func cfClient() *cloudfront.Client {
	cfg := sdkConfig()
	return cloudfront.NewFromConfig(cfg, func(o *cloudfront.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestCloudFrontDistributionLifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create
	caller := "sdk-test-" + time.Now().Format("150405.000000")
	createOut, err := c.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{
		DistributionConfig: &cftypes.DistributionConfig{
			CallerReference: aws.String(caller),
			Comment:         aws.String("sdk lifecycle test"),
			Enabled:         aws.Bool(true),
			Origins: &cftypes.Origins{
				Quantity: aws.Int32(1),
				Items: []cftypes.Origin{
					{
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
					},
				},
			},
			DefaultCacheBehavior: &cftypes.DefaultCacheBehavior{
				TargetOriginId:       aws.String("o1"),
				ViewerProtocolPolicy: cftypes.ViewerProtocolPolicyAllowAll,
				AllowedMethods: &cftypes.AllowedMethods{
					Quantity: aws.Int32(2),
					Items:    []cftypes.Method{cftypes.MethodGet, cftypes.MethodHead},
					CachedMethods: &cftypes.CachedMethods{
						Quantity: aws.Int32(2),
						Items:    []cftypes.Method{cftypes.MethodGet, cftypes.MethodHead},
					},
				},
				ForwardedValues: &cftypes.ForwardedValues{
					QueryString: aws.Bool(false),
					Cookies: &cftypes.CookiePreference{
						Forward: cftypes.ItemSelectionNone,
					},
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
	require.NotNil(t, createOut.Distribution)
	id := aws.ToString(createOut.Distribution.Id)
	require.NotEmpty(t, id)
	require.NotNil(t, createOut.ETag)
	etag := aws.ToString(createOut.ETag)
	require.NotEmpty(t, etag)
	assert.NotEmpty(t, aws.ToString(createOut.Distribution.DomainName))
	assert.Equal(t, "Deployed", aws.ToString(createOut.Distribution.Status))
	assert.Equal(t, caller, aws.ToString(createOut.Distribution.DistributionConfig.CallerReference))

	// Get
	getOut, err := c.GetDistribution(ctx, &cloudfront.GetDistributionInput{Id: aws.String(id)})
	require.NoError(t, err)
	require.NotNil(t, getOut.Distribution)
	assert.Equal(t, id, aws.ToString(getOut.Distribution.Id))
	assert.Equal(t, etag, aws.ToString(getOut.ETag))

	// List
	listOut, err := c.ListDistributions(ctx, &cloudfront.ListDistributionsInput{})
	require.NoError(t, err)
	require.NotNil(t, listOut.DistributionList)
	require.NotZero(t, aws.ToInt32(listOut.DistributionList.Quantity))
	found := false
	for _, s := range listOut.DistributionList.Items {
		if aws.ToString(s.Id) == id {
			found = true
			break
		}
	}
	assert.True(t, found, "expected new distribution %q in list", id)

	// Update (set Enabled=false so we can delete)
	cfg := *getOut.Distribution.DistributionConfig
	cfg.Enabled = aws.Bool(false)
	cfg.Comment = aws.String("disabled for cleanup")
	updateOut, err := c.UpdateDistribution(ctx, &cloudfront.UpdateDistributionInput{
		Id:                 aws.String(id),
		IfMatch:            aws.String(etag),
		DistributionConfig: &cfg,
	})
	require.NoError(t, err)
	newETag := aws.ToString(updateOut.ETag)
	require.NotEmpty(t, newETag)
	require.NotEqual(t, etag, newETag, "ETag must rotate on update")

	// Delete with stale ETag → PreconditionFailed
	_, err = c.DeleteDistribution(ctx, &cloudfront.DeleteDistributionInput{
		Id:      aws.String(id),
		IfMatch: aws.String(etag),
	})
	require.Error(t, err, "stale ETag must fail")

	// Delete with current ETag → succeeds
	_, err = c.DeleteDistribution(ctx, &cloudfront.DeleteDistributionInput{
		Id:      aws.String(id),
		IfMatch: aws.String(newETag),
	})
	require.NoError(t, err)

	// Get after delete → NoSuchDistribution
	_, err = c.GetDistribution(ctx, &cloudfront.GetDistributionInput{Id: aws.String(id)})
	require.Error(t, err, "GetDistribution after delete must fail")
}

func TestCloudFrontOriginAccessControlLifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	name := "oac-" + time.Now().Format("150405.000000")
	createOut, err := c.CreateOriginAccessControl(ctx, &cloudfront.CreateOriginAccessControlInput{
		OriginAccessControlConfig: &cftypes.OriginAccessControlConfig{
			Name:                          aws.String(name),
			Description:                   aws.String("sdk test"),
			SigningProtocol:               cftypes.OriginAccessControlSigningProtocolsSigv4,
			SigningBehavior:               cftypes.OriginAccessControlSigningBehaviorsAlways,
			OriginAccessControlOriginType: cftypes.OriginAccessControlOriginTypesS3,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.OriginAccessControl)
	id := aws.ToString(createOut.OriginAccessControl.Id)
	etag := aws.ToString(createOut.ETag)
	require.NotEmpty(t, id)
	require.NotEmpty(t, etag)

	getOut, err := c.GetOriginAccessControl(ctx, &cloudfront.GetOriginAccessControlInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(getOut.OriginAccessControl.OriginAccessControlConfig.Name))

	listOut, err := c.ListOriginAccessControls(ctx, &cloudfront.ListOriginAccessControlsInput{})
	require.NoError(t, err)
	found := false
	for _, s := range listOut.OriginAccessControlList.Items {
		if aws.ToString(s.Id) == id {
			found = true
			break
		}
	}
	assert.True(t, found)

	_, err = c.DeleteOriginAccessControl(ctx, &cloudfront.DeleteOriginAccessControlInput{
		Id:      aws.String(id),
		IfMatch: aws.String(etag),
	})
	require.NoError(t, err)
}

func TestCloudFrontDistributionRejectsInvalidConfig(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Missing required CallerReference → server returns InvalidArgument.
	// (SDK normally enforces field-presence, so submit via lower-level path
	// would be needed for an empty-CallerReference test. Instead drive a
	// realistic spec-violation: zero origins.)
	_, err := c.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{
		DistributionConfig: &cftypes.DistributionConfig{
			CallerReference: aws.String("bad-" + time.Now().Format("150405.000000")),
			Comment:         aws.String("bad"),
			Enabled:         aws.Bool(true),
			Origins: &cftypes.Origins{
				Quantity: aws.Int32(0),
				Items:    []cftypes.Origin{},
			},
			DefaultCacheBehavior: &cftypes.DefaultCacheBehavior{
				TargetOriginId:       aws.String("o1"),
				ViewerProtocolPolicy: cftypes.ViewerProtocolPolicyAllowAll,
			},
		},
	})
	require.Error(t, err, "zero origins must be rejected, not silently accepted")
}
