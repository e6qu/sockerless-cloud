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

// TestCloudFrontOriginAccessIdentityLifecycle exercises the legacy
// pre-OAC origin access identity CRUD:
// Create → Get(+ETag) → GetConfig → Update → List → Delete.
func TestCloudFrontOriginAccessIdentityLifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	caller := "oai-" + time.Now().Format("150405.000000")
	createOut, err := c.CreateCloudFrontOriginAccessIdentity(ctx, &cloudfront.CreateCloudFrontOriginAccessIdentityInput{
		CloudFrontOriginAccessIdentityConfig: &cftypes.CloudFrontOriginAccessIdentityConfig{
			CallerReference: aws.String(caller),
			Comment:         aws.String("sdk oai test"),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.CloudFrontOriginAccessIdentity)
	id := aws.ToString(createOut.CloudFrontOriginAccessIdentity.Id)
	etag := aws.ToString(createOut.ETag)
	require.NotEmpty(t, id)
	require.NotEmpty(t, etag)
	assert.NotEmpty(t, aws.ToString(createOut.CloudFrontOriginAccessIdentity.S3CanonicalUserId))
	assert.Equal(t, "sdk oai test", aws.ToString(createOut.CloudFrontOriginAccessIdentity.CloudFrontOriginAccessIdentityConfig.Comment))

	getOut, err := c.GetCloudFrontOriginAccessIdentity(ctx, &cloudfront.GetCloudFrontOriginAccessIdentityInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(getOut.CloudFrontOriginAccessIdentity.Id))
	getETag := aws.ToString(getOut.ETag)
	require.Equal(t, etag, getETag)

	cfgOut, err := c.GetCloudFrontOriginAccessIdentityConfig(ctx, &cloudfront.GetCloudFrontOriginAccessIdentityConfigInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, caller, aws.ToString(cfgOut.CloudFrontOriginAccessIdentityConfig.CallerReference))

	updOut, err := c.UpdateCloudFrontOriginAccessIdentity(ctx, &cloudfront.UpdateCloudFrontOriginAccessIdentityInput{
		Id:      aws.String(id),
		IfMatch: aws.String(getETag),
		CloudFrontOriginAccessIdentityConfig: &cftypes.CloudFrontOriginAccessIdentityConfig{
			CallerReference: aws.String(caller),
			Comment:         aws.String("sdk oai updated"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "sdk oai updated", aws.ToString(updOut.CloudFrontOriginAccessIdentity.CloudFrontOriginAccessIdentityConfig.Comment))
	updETag := aws.ToString(updOut.ETag)
	require.NotEmpty(t, updETag)

	listOut, err := c.ListCloudFrontOriginAccessIdentities(ctx, &cloudfront.ListCloudFrontOriginAccessIdentitiesInput{})
	require.NoError(t, err)
	require.NotNil(t, listOut.CloudFrontOriginAccessIdentityList)
	found := false
	for _, s := range listOut.CloudFrontOriginAccessIdentityList.Items {
		if aws.ToString(s.Id) == id {
			found = true
		}
	}
	assert.True(t, found, "expected new OAI %q in list", id)

	_, err = c.DeleteCloudFrontOriginAccessIdentity(ctx, &cloudfront.DeleteCloudFrontOriginAccessIdentityInput{
		Id:      aws.String(id),
		IfMatch: aws.String(updETag),
	})
	require.NoError(t, err)

	_, err = c.GetCloudFrontOriginAccessIdentity(ctx, &cloudfront.GetCloudFrontOriginAccessIdentityInput{Id: aws.String(id)})
	require.Error(t, err, "get after delete must fail")
}

// TestCloudFrontContinuousDeploymentPolicyLifecycle exercises CDP CRUD:
// Create → Get(+ETag) → GetConfig → Update → List → Delete.
func TestCloudFrontContinuousDeploymentPolicyLifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := &cftypes.ContinuousDeploymentPolicyConfig{
		Enabled: aws.Bool(true),
		StagingDistributionDnsNames: &cftypes.StagingDistributionDnsNames{
			Quantity: aws.Int32(1),
			Items:    []string{"staging.example.com"},
		},
		TrafficConfig: &cftypes.TrafficConfig{
			Type: cftypes.ContinuousDeploymentPolicyTypeSingleWeight,
			SingleWeightConfig: &cftypes.ContinuousDeploymentSingleWeightConfig{
				Weight: aws.Float32(0.15),
			},
		},
	}
	createOut, err := c.CreateContinuousDeploymentPolicy(ctx, &cloudfront.CreateContinuousDeploymentPolicyInput{
		ContinuousDeploymentPolicyConfig: cfg,
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.ContinuousDeploymentPolicy)
	id := aws.ToString(createOut.ContinuousDeploymentPolicy.Id)
	etag := aws.ToString(createOut.ETag)
	require.NotEmpty(t, id)
	require.NotEmpty(t, etag)

	getOut, err := c.GetContinuousDeploymentPolicy(ctx, &cloudfront.GetContinuousDeploymentPolicyInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(getOut.ContinuousDeploymentPolicy.Id))
	getETag := aws.ToString(getOut.ETag)
	require.Equal(t, etag, getETag)
	require.NotNil(t, getOut.ContinuousDeploymentPolicy.ContinuousDeploymentPolicyConfig)
	assert.True(t, aws.ToBool(getOut.ContinuousDeploymentPolicy.ContinuousDeploymentPolicyConfig.Enabled))

	cfgOut, err := c.GetContinuousDeploymentPolicyConfig(ctx, &cloudfront.GetContinuousDeploymentPolicyConfigInput{Id: aws.String(id)})
	require.NoError(t, err)
	require.NotNil(t, cfgOut.ContinuousDeploymentPolicyConfig)
	assert.Equal(t, cftypes.ContinuousDeploymentPolicyTypeSingleWeight, cfgOut.ContinuousDeploymentPolicyConfig.TrafficConfig.Type)

	updCfg := *cfg
	updCfg.Enabled = aws.Bool(false)
	updOut, err := c.UpdateContinuousDeploymentPolicy(ctx, &cloudfront.UpdateContinuousDeploymentPolicyInput{
		Id:                               aws.String(id),
		IfMatch:                          aws.String(getETag),
		ContinuousDeploymentPolicyConfig: &updCfg,
	})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(updOut.ContinuousDeploymentPolicy.ContinuousDeploymentPolicyConfig.Enabled))
	updETag := aws.ToString(updOut.ETag)
	require.NotEmpty(t, updETag)

	listOut, err := c.ListContinuousDeploymentPolicies(ctx, &cloudfront.ListContinuousDeploymentPoliciesInput{})
	require.NoError(t, err)
	require.NotNil(t, listOut.ContinuousDeploymentPolicyList)
	found := false
	for _, s := range listOut.ContinuousDeploymentPolicyList.Items {
		if aws.ToString(s.ContinuousDeploymentPolicy.Id) == id {
			found = true
		}
	}
	assert.True(t, found, "expected new CDP %q in list", id)

	_, err = c.DeleteContinuousDeploymentPolicy(ctx, &cloudfront.DeleteContinuousDeploymentPolicyInput{
		Id:      aws.String(id),
		IfMatch: aws.String(updETag),
	})
	require.NoError(t, err)

	_, err = c.GetContinuousDeploymentPolicy(ctx, &cloudfront.GetContinuousDeploymentPolicyInput{Id: aws.String(id)})
	require.Error(t, err, "get after delete must fail")
}

// TestCloudFrontMonitoringSubscriptionLifecycle exercises the
// per-distribution monitoring subscription: Create → Get → Delete.
func TestCloudFrontMonitoringSubscriptionLifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	distID := cfCreateTestDistribution(t, ctx, c)

	_, err := c.CreateMonitoringSubscription(ctx, &cloudfront.CreateMonitoringSubscriptionInput{
		DistributionId: aws.String(distID),
		MonitoringSubscription: &cftypes.MonitoringSubscription{
			RealtimeMetricsSubscriptionConfig: &cftypes.RealtimeMetricsSubscriptionConfig{
				RealtimeMetricsSubscriptionStatus: cftypes.RealtimeMetricsSubscriptionStatusEnabled,
			},
		},
	})
	require.NoError(t, err)

	getOut, err := c.GetMonitoringSubscription(ctx, &cloudfront.GetMonitoringSubscriptionInput{
		DistributionId: aws.String(distID),
	})
	require.NoError(t, err)
	require.NotNil(t, getOut.MonitoringSubscription)
	require.NotNil(t, getOut.MonitoringSubscription.RealtimeMetricsSubscriptionConfig)
	assert.Equal(t, cftypes.RealtimeMetricsSubscriptionStatusEnabled,
		getOut.MonitoringSubscription.RealtimeMetricsSubscriptionConfig.RealtimeMetricsSubscriptionStatus)

	_, err = c.DeleteMonitoringSubscription(ctx, &cloudfront.DeleteMonitoringSubscriptionInput{
		DistributionId: aws.String(distID),
	})
	require.NoError(t, err)

	_, err = c.GetMonitoringSubscription(ctx, &cloudfront.GetMonitoringSubscriptionInput{
		DistributionId: aws.String(distID),
	})
	require.Error(t, err, "get after delete must fail")
}

// cfCreateTestDistribution provisions a minimal distribution and returns
// its id, for tests (like monitoring subscriptions) keyed on a real
// distribution.
func cfCreateTestDistribution(t *testing.T, ctx context.Context, c *cloudfront.Client) string {
	t.Helper()
	caller := "ms-dist-" + time.Now().Format("150405.000000")
	out, err := c.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{
		DistributionConfig: &cftypes.DistributionConfig{
			CallerReference: aws.String(caller),
			Comment:         aws.String("monitoring subscription test dist"),
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
	return aws.ToString(out.Distribution.Id)
}
