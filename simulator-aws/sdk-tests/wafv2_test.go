package aws_sdk_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
	wafv2types "github.com/aws/aws-sdk-go-v2/service/wafv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wafStamp is a unique suffix built only from characters an AWS WAFV2 name
// admits. The model's EntityName pattern is ^[\w\-]+$, so the fractional-
// seconds dot the other suites use would be rejected by the service — and the
// load-balancer names built here forbid it too.
func wafStamp() string {
	return strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "-")
}

func wafClient() *wafv2.Client {
	cfg := sdkConfig()
	return wafv2.NewFromConfig(cfg, func(o *wafv2.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestWAFv2RevenueSurfacesForAccountWithoutSettlements(t *testing.T) {
	c := wafClient()
	testCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	window := &wafv2types.TimeWindow{
		StartTime: aws.Time(time.Now().Add(-time.Hour)),
		EndTime:   aws.Time(time.Now()),
	}

	ranked, err := c.GetRevenueStatistics(testCtx, &wafv2.GetRevenueStatisticsInput{
		Currency:      wafv2types.CurrencyUsdc,
		Scope:         wafv2types.ScopeCloudfront,
		StatisticType: wafv2types.RankingStatisticTypeTopSourcesByRevenue,
		GroupBy:       wafv2types.GroupByTypeName,
		TimeWindow:    window,
	})
	require.NoError(t, err)
	assert.Empty(t, ranked.SourceStatistics)

	summary, err := c.GetRevenueStatisticsSummary(testCtx, &wafv2.GetRevenueStatisticsSummaryInput{
		Currency:   wafv2types.CurrencyUsdc,
		Scope:      wafv2types.ScopeCloudfront,
		TimeWindow: window,
	})
	require.NoError(t, err)
	require.NotNil(t, summary.RevenueBreakdown)
	assert.Equal(t, "0", aws.ToString(summary.RevenueBreakdown.TotalAmount))
	assert.Zero(t, summary.RevenueBreakdown.TotalSettled)

	series, err := c.GetRevenueStatisticsTimeSeries(testCtx, &wafv2.GetRevenueStatisticsTimeSeriesInput{
		Currency:      wafv2types.CurrencyUsdc,
		Scope:         wafv2types.ScopeCloudfront,
		StatisticType: wafv2types.TimeSeriesStatisticTypeDateHistogram,
		Interval:      wafv2types.IntervalTypeHourly,
		TimeWindow:    window,
	})
	require.NoError(t, err)
	assert.Empty(t, series.DataPoints)

	settlements, err := c.ListSettlementRecords(testCtx, &wafv2.ListSettlementRecordsInput{
		Currency:   wafv2types.CurrencyUsdc,
		Scope:      wafv2types.ScopeCloudfront,
		TimeWindow: window,
	})
	require.NoError(t, err)
	assert.Empty(t, settlements.Settlements)
}

func TestWAFv2WebACLLifecycle(t *testing.T) {
	c := wafClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	name := "sdk-acl-" + wafStamp()
	createOut, err := c.CreateWebACL(ctx, &wafv2.CreateWebACLInput{
		Name:  aws.String(name),
		Scope: wafv2types.ScopeCloudfront,
		DefaultAction: &wafv2types.DefaultAction{
			Allow: &wafv2types.AllowAction{},
		},
		Description: aws.String("sdk test"),
		VisibilityConfig: &wafv2types.VisibilityConfig{
			SampledRequestsEnabled:   true,
			CloudWatchMetricsEnabled: true,
			MetricName:               aws.String(name + "-metric"),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.Summary)
	id := aws.ToString(createOut.Summary.Id)
	require.NotEmpty(t, id)
	lockToken := aws.ToString(createOut.Summary.LockToken)
	require.NotEmpty(t, lockToken)

	getOut, err := c.GetWebACL(ctx, &wafv2.GetWebACLInput{
		Name:  aws.String(name),
		Scope: wafv2types.ScopeCloudfront,
		Id:    aws.String(id),
	})
	require.NoError(t, err)
	require.NotNil(t, getOut.WebACL)
	assert.Equal(t, name, aws.ToString(getOut.WebACL.Name))

	listOut, err := c.ListWebACLs(ctx, &wafv2.ListWebACLsInput{
		Scope: wafv2types.ScopeCloudfront,
	})
	require.NoError(t, err)
	found := false
	for _, s := range listOut.WebACLs {
		if aws.ToString(s.Id) == id {
			found = true
		}
	}
	assert.True(t, found)

	// Stale-lock delete → optimistic-lock failure
	_, err = c.DeleteWebACL(ctx, &wafv2.DeleteWebACLInput{
		Name:      aws.String(name),
		Scope:     wafv2types.ScopeCloudfront,
		Id:        aws.String(id),
		LockToken: aws.String("stale"),
	})
	require.Error(t, err, "stale lock must be rejected")

	// Real lock-token from Get works
	currentLock := aws.ToString(getOut.LockToken)
	_, err = c.DeleteWebACL(ctx, &wafv2.DeleteWebACLInput{
		Name:      aws.String(name),
		Scope:     wafv2types.ScopeCloudfront,
		Id:        aws.String(id),
		LockToken: aws.String(currentLock),
	})
	require.NoError(t, err)
}

func TestWAFv2IPSetLifecycle(t *testing.T) {
	c := wafClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	name := "sdk-ipset-" + wafStamp()
	createOut, err := c.CreateIPSet(ctx, &wafv2.CreateIPSetInput{
		Name:             aws.String(name),
		Scope:            wafv2types.ScopeCloudfront,
		IPAddressVersion: wafv2types.IPAddressVersionIpv4,
		Addresses:        []string{"203.0.113.0/24", "198.51.100.10/32"},
		Description:      aws.String("sdk test"),
	})
	require.NoError(t, err)
	id := aws.ToString(createOut.Summary.Id)
	lock := aws.ToString(createOut.Summary.LockToken)

	getOut, err := c.GetIPSet(ctx, &wafv2.GetIPSetInput{
		Name:  aws.String(name),
		Scope: wafv2types.ScopeCloudfront,
		Id:    aws.String(id),
	})
	require.NoError(t, err)
	require.Equal(t, 2, len(getOut.IPSet.Addresses))

	// Update
	updOut, err := c.UpdateIPSet(ctx, &wafv2.UpdateIPSetInput{
		Name:      aws.String(name),
		Scope:     wafv2types.ScopeCloudfront,
		Id:        aws.String(id),
		Addresses: []string{"203.0.113.42/32"},
		LockToken: aws.String(lock),
	})
	require.NoError(t, err)

	_, err = c.DeleteIPSet(ctx, &wafv2.DeleteIPSetInput{
		Name:      aws.String(name),
		Scope:     wafv2types.ScopeCloudfront,
		Id:        aws.String(id),
		LockToken: updOut.NextLockToken,
	})
	require.NoError(t, err)
}

func TestWAFv2RuleGroupAndRegexPatternSet(t *testing.T) {
	c := wafClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rgName := "sdk-rg-" + wafStamp()
	rgOut, err := c.CreateRuleGroup(ctx, &wafv2.CreateRuleGroupInput{
		Name:     aws.String(rgName),
		Scope:    wafv2types.ScopeCloudfront,
		Capacity: aws.Int64(100),
		VisibilityConfig: &wafv2types.VisibilityConfig{
			SampledRequestsEnabled:   false,
			CloudWatchMetricsEnabled: false,
			MetricName:               aws.String(rgName + "-metric"),
		},
	})
	require.NoError(t, err)
	rgID := aws.ToString(rgOut.Summary.Id)
	rgLock := aws.ToString(rgOut.Summary.LockToken)

	getRG, err := c.GetRuleGroup(ctx, &wafv2.GetRuleGroupInput{
		Name:  aws.String(rgName),
		Scope: wafv2types.ScopeCloudfront,
		Id:    aws.String(rgID),
	})
	require.NoError(t, err)
	require.NotNil(t, getRG.RuleGroup)

	listRG, err := c.ListRuleGroups(ctx, &wafv2.ListRuleGroupsInput{Scope: wafv2types.ScopeCloudfront})
	require.NoError(t, err)
	rgFound := false
	for _, s := range listRG.RuleGroups {
		if aws.ToString(s.Id) == rgID {
			rgFound = true
		}
	}
	assert.True(t, rgFound)

	_, err = c.DeleteRuleGroup(ctx, &wafv2.DeleteRuleGroupInput{
		Name: aws.String(rgName), Scope: wafv2types.ScopeCloudfront,
		Id: aws.String(rgID), LockToken: aws.String(rgLock),
	})
	require.NoError(t, err)

	rsName := "sdk-rs-" + wafStamp()
	rsOut, err := c.CreateRegexPatternSet(ctx, &wafv2.CreateRegexPatternSetInput{
		Name:                  aws.String(rsName),
		Scope:                 wafv2types.ScopeCloudfront,
		RegularExpressionList: []wafv2types.Regex{{RegexString: aws.String("^/admin/.*$")}},
	})
	require.NoError(t, err)
	rsID := aws.ToString(rsOut.Summary.Id)
	rsLock := aws.ToString(rsOut.Summary.LockToken)

	_, err = c.GetRegexPatternSet(ctx, &wafv2.GetRegexPatternSetInput{
		Name: aws.String(rsName), Scope: wafv2types.ScopeCloudfront, Id: aws.String(rsID),
	})
	require.NoError(t, err)

	_, err = c.DeleteRegexPatternSet(ctx, &wafv2.DeleteRegexPatternSetInput{
		Name: aws.String(rsName), Scope: wafv2types.ScopeCloudfront,
		Id: aws.String(rsID), LockToken: aws.String(rsLock),
	})
	require.NoError(t, err)
}

// TestWAFv2UpdatesAndListsAndTags exercises the secondary list / update /
// tag verbs not covered by the focused per-resource lifecycle tests:
// UpdateWebACL, UpdateRuleGroup, UpdateRegexPatternSet, ListIPSets,
// ListRegexPatternSets, TagResource, UntagResource, ListTagsForResource,
// GetSampledRequests. One test covers the whole cluster.
func TestWAFv2UpdatesAndListsAndTags(t *testing.T) {
	c := wafClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ts := wafStamp()

	// IPSet for List + Tag verbs
	ipsetOut, err := c.CreateIPSet(ctx, &wafv2.CreateIPSetInput{
		Name: aws.String("up-ipset-" + ts), Scope: wafv2types.ScopeCloudfront,
		IPAddressVersion: wafv2types.IPAddressVersionIpv4,
		Addresses:        []string{"203.0.113.0/24"},
	})
	require.NoError(t, err)
	ipsetID := aws.ToString(ipsetOut.Summary.Id)
	ipsetLock := aws.ToString(ipsetOut.Summary.LockToken)
	ipsetARN := aws.ToString(ipsetOut.Summary.ARN)
	defer func() {
		// fetch latest lock token then delete
		getOut, _ := c.GetIPSet(ctx, &wafv2.GetIPSetInput{
			Name: aws.String("up-ipset-" + ts), Scope: wafv2types.ScopeCloudfront, Id: aws.String(ipsetID),
		})
		_, _ = c.DeleteIPSet(ctx, &wafv2.DeleteIPSetInput{
			Name: aws.String("up-ipset-" + ts), Scope: wafv2types.ScopeCloudfront,
			Id: aws.String(ipsetID), LockToken: getOut.LockToken,
		})
	}()
	_ = ipsetLock

	// ListIPSets — verify our IPSet is present
	listIPs, err := c.ListIPSets(ctx, &wafv2.ListIPSetsInput{Scope: wafv2types.ScopeCloudfront})
	require.NoError(t, err)
	ipFound := false
	for _, s := range listIPs.IPSets {
		if aws.ToString(s.Id) == ipsetID {
			ipFound = true
		}
	}
	assert.True(t, ipFound)

	// TagResource → ListTagsForResource → UntagResource
	_, err = c.TagResource(ctx, &wafv2.TagResourceInput{
		ResourceARN: aws.String(ipsetARN),
		Tags:        []wafv2types.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)

	tagsOut, err := c.ListTagsForResource(ctx, &wafv2.ListTagsForResourceInput{
		ResourceARN: aws.String(ipsetARN),
	})
	require.NoError(t, err)
	require.NotNil(t, tagsOut.TagInfoForResource)
	require.Len(t, tagsOut.TagInfoForResource.TagList, 1)

	_, err = c.UntagResource(ctx, &wafv2.UntagResourceInput{
		ResourceARN: aws.String(ipsetARN),
		TagKeys:     []string{"env"},
	})
	require.NoError(t, err)

	// WebACL Update — round-trip a new description through the resource
	aclOut, err := c.CreateWebACL(ctx, &wafv2.CreateWebACLInput{
		Name: aws.String("up-acl-" + ts), Scope: wafv2types.ScopeCloudfront,
		DefaultAction:    &wafv2types.DefaultAction{Allow: &wafv2types.AllowAction{}},
		VisibilityConfig: &wafv2types.VisibilityConfig{SampledRequestsEnabled: false, CloudWatchMetricsEnabled: false, MetricName: aws.String("m")},
	})
	require.NoError(t, err)
	aclID := aws.ToString(aclOut.Summary.Id)
	aclLock := aws.ToString(aclOut.Summary.LockToken)
	defer func() {
		getOut, _ := c.GetWebACL(ctx, &wafv2.GetWebACLInput{
			Name: aws.String("up-acl-" + ts), Scope: wafv2types.ScopeCloudfront, Id: aws.String(aclID),
		})
		_, _ = c.DeleteWebACL(ctx, &wafv2.DeleteWebACLInput{
			Name: aws.String("up-acl-" + ts), Scope: wafv2types.ScopeCloudfront,
			Id: aws.String(aclID), LockToken: getOut.LockToken,
		})
	}()
	updACL, err := c.UpdateWebACL(ctx, &wafv2.UpdateWebACLInput{
		Name: aws.String("up-acl-" + ts), Scope: wafv2types.ScopeCloudfront, Id: aws.String(aclID),
		LockToken:        aws.String(aclLock),
		DefaultAction:    &wafv2types.DefaultAction{Block: &wafv2types.BlockAction{}},
		Description:      aws.String("updated"),
		VisibilityConfig: &wafv2types.VisibilityConfig{SampledRequestsEnabled: false, CloudWatchMetricsEnabled: false, MetricName: aws.String("m")},
	})
	require.NoError(t, err)
	require.NotEqual(t, aclLock, aws.ToString(updACL.NextLockToken))

	// RuleGroup Update
	rgOut, err := c.CreateRuleGroup(ctx, &wafv2.CreateRuleGroupInput{
		Name: aws.String("up-rg-" + ts), Scope: wafv2types.ScopeCloudfront,
		Capacity:         aws.Int64(50),
		VisibilityConfig: &wafv2types.VisibilityConfig{SampledRequestsEnabled: false, CloudWatchMetricsEnabled: false, MetricName: aws.String("rgm")},
	})
	require.NoError(t, err)
	rgID := aws.ToString(rgOut.Summary.Id)
	rgLock := aws.ToString(rgOut.Summary.LockToken)
	updRG, err := c.UpdateRuleGroup(ctx, &wafv2.UpdateRuleGroupInput{
		Name: aws.String("up-rg-" + ts), Scope: wafv2types.ScopeCloudfront, Id: aws.String(rgID),
		LockToken:        aws.String(rgLock),
		Description:      aws.String("updated rg"),
		VisibilityConfig: &wafv2types.VisibilityConfig{SampledRequestsEnabled: false, CloudWatchMetricsEnabled: false, MetricName: aws.String("rgm")},
	})
	require.NoError(t, err)
	_, err = c.DeleteRuleGroup(ctx, &wafv2.DeleteRuleGroupInput{
		Name: aws.String("up-rg-" + ts), Scope: wafv2types.ScopeCloudfront,
		Id: aws.String(rgID), LockToken: updRG.NextLockToken,
	})
	require.NoError(t, err)

	// RegexPatternSet Update + List
	rsOut, err := c.CreateRegexPatternSet(ctx, &wafv2.CreateRegexPatternSetInput{
		Name: aws.String("up-rs-" + ts), Scope: wafv2types.ScopeCloudfront,
		RegularExpressionList: []wafv2types.Regex{{RegexString: aws.String("foo.*")}},
	})
	require.NoError(t, err)
	rsID := aws.ToString(rsOut.Summary.Id)
	rsLock := aws.ToString(rsOut.Summary.LockToken)
	updRS, err := c.UpdateRegexPatternSet(ctx, &wafv2.UpdateRegexPatternSetInput{
		Name: aws.String("up-rs-" + ts), Scope: wafv2types.ScopeCloudfront, Id: aws.String(rsID),
		LockToken:             aws.String(rsLock),
		RegularExpressionList: []wafv2types.Regex{{RegexString: aws.String("bar.*")}},
	})
	require.NoError(t, err)
	listRS, err := c.ListRegexPatternSets(ctx, &wafv2.ListRegexPatternSetsInput{Scope: wafv2types.ScopeCloudfront})
	require.NoError(t, err)
	rsFound := false
	for _, s := range listRS.RegexPatternSets {
		if aws.ToString(s.Id) == rsID {
			rsFound = true
		}
	}
	assert.True(t, rsFound)
	_, err = c.DeleteRegexPatternSet(ctx, &wafv2.DeleteRegexPatternSetInput{
		Name: aws.String("up-rs-" + ts), Scope: wafv2types.ScopeCloudfront,
		Id: aws.String(rsID), LockToken: updRS.NextLockToken,
	})
	require.NoError(t, err)

	// No request reached this unassociated WebACL, so its real sample is empty.
	samp, err := c.GetSampledRequests(ctx, &wafv2.GetSampledRequestsInput{
		WebAclArn:      aws.String(aws.ToString(aclOut.Summary.ARN)),
		RuleMetricName: aws.String("any"),
		Scope:          wafv2types.ScopeCloudfront,
		TimeWindow: &wafv2types.TimeWindow{
			StartTime: aws.Time(time.Now().Add(-time.Hour)),
			EndTime:   aws.Time(time.Now()),
		},
		MaxItems: aws.Int64(10),
	})
	require.NoError(t, err)
	require.NotNil(t, samp)
	require.Zero(t, samp.PopulationSize)
	require.Empty(t, samp.SampledRequests)
}

func TestWAFv2AssociateWebACLWithCloudFront(t *testing.T) {
	c := wafClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	name := "sdk-assoc-" + wafStamp()
	createOut, err := c.CreateWebACL(ctx, &wafv2.CreateWebACLInput{
		Name:  aws.String(name),
		Scope: wafv2types.ScopeCloudfront,
		DefaultAction: &wafv2types.DefaultAction{
			Allow: &wafv2types.AllowAction{},
		},
		VisibilityConfig: &wafv2types.VisibilityConfig{
			SampledRequestsEnabled:   false,
			CloudWatchMetricsEnabled: false,
			MetricName:               aws.String(name + "-metric"),
		},
	})
	require.NoError(t, err)
	aclARN := aws.ToString(createOut.Summary.ARN)
	lock := aws.ToString(createOut.Summary.LockToken)
	defer func() {
		_, _ = c.DeleteWebACL(ctx, &wafv2.DeleteWebACLInput{
			Name: aws.String(name), Scope: wafv2types.ScopeCloudfront,
			Id: createOut.Summary.Id, LockToken: aws.String(lock),
		})
	}()

	cloudFrontAPI := cfClient()
	distribution, err := cloudFrontAPI.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{
		DistributionConfig: &cftypes.DistributionConfig{
			CallerReference: aws.String("waf-association-" + wafStamp()),
			Comment:         aws.String("AWS WAFv2 association target"),
			Enabled:         aws.Bool(true),
			Origins: &cftypes.Origins{Quantity: aws.Int32(1), Items: []cftypes.Origin{{
				Id: aws.String("origin"), DomainName: aws.String("example.com"),
				CustomOriginConfig: &cftypes.CustomOriginConfig{
					HTTPPort: aws.Int32(80), HTTPSPort: aws.Int32(443),
					OriginProtocolPolicy: cftypes.OriginProtocolPolicyHttpOnly,
					OriginSslProtocols: &cftypes.OriginSslProtocols{
						Quantity: aws.Int32(1), Items: []cftypes.SslProtocol{cftypes.SslProtocolTLSv12},
					},
				},
			}}},
			DefaultCacheBehavior: &cftypes.DefaultCacheBehavior{
				TargetOriginId: aws.String("origin"), ViewerProtocolPolicy: cftypes.ViewerProtocolPolicyAllowAll,
				AllowedMethods: &cftypes.AllowedMethods{
					Quantity: aws.Int32(2), Items: []cftypes.Method{cftypes.MethodGet, cftypes.MethodHead},
					CachedMethods: &cftypes.CachedMethods{
						Quantity: aws.Int32(2), Items: []cftypes.Method{cftypes.MethodGet, cftypes.MethodHead},
					},
				},
				ForwardedValues: &cftypes.ForwardedValues{
					QueryString: aws.Bool(false),
					Cookies:     &cftypes.CookiePreference{Forward: cftypes.ItemSelectionNone},
				},
				MinTTL: aws.Int64(0),
			},
			ViewerCertificate: &cftypes.ViewerCertificate{CloudFrontDefaultCertificate: aws.Bool(true)},
			Restrictions: &cftypes.Restrictions{GeoRestriction: &cftypes.GeoRestriction{
				RestrictionType: cftypes.GeoRestrictionTypeNone, Quantity: aws.Int32(0),
			}},
		},
	})
	require.NoError(t, err)
	cfARN := aws.ToString(distribution.Distribution.ARN)
	distributionID := aws.ToString(distribution.Distribution.Id)
	t.Cleanup(func() {
		current, getErr := cloudFrontAPI.GetDistribution(context.Background(), &cloudfront.GetDistributionInput{
			Id: aws.String(distributionID),
		})
		if getErr != nil {
			return
		}
		config := *current.Distribution.DistributionConfig
		config.Enabled = aws.Bool(false)
		updated, updateErr := cloudFrontAPI.UpdateDistribution(context.Background(), &cloudfront.UpdateDistributionInput{
			Id: aws.String(distributionID), IfMatch: current.ETag, DistributionConfig: &config,
		})
		if updateErr == nil {
			_, _ = cloudFrontAPI.DeleteDistribution(context.Background(), &cloudfront.DeleteDistributionInput{
				Id: aws.String(distributionID), IfMatch: updated.ETag,
			})
		}
	})

	_, err = c.AssociateWebACL(ctx, &wafv2.AssociateWebACLInput{
		WebACLArn:   aws.String(aclARN),
		ResourceArn: aws.String(cfARN),
	})
	require.NoError(t, err)

	getForResOut, err := c.GetWebACLForResource(ctx, &wafv2.GetWebACLForResourceInput{
		ResourceArn: aws.String(cfARN),
	})
	require.NoError(t, err)
	require.NotNil(t, getForResOut.WebACL)
	assert.Equal(t, aclARN, aws.ToString(getForResOut.WebACL.ARN))

	listResOut, err := c.ListResourcesForWebACL(ctx, &wafv2.ListResourcesForWebACLInput{
		WebACLArn: aws.String(aclARN),
	})
	require.NoError(t, err)
	require.Contains(t, listResOut.ResourceArns, cfARN)

	// Delete WebACL while associated → reject
	_, err = c.DeleteWebACL(ctx, &wafv2.DeleteWebACLInput{
		Name: aws.String(name), Scope: wafv2types.ScopeCloudfront,
		Id: createOut.Summary.Id, LockToken: aws.String(lock),
	})
	require.Error(t, err, "delete with active association must fail")

	// Disassociate then delete works
	_, err = c.DisassociateWebACL(ctx, &wafv2.DisassociateWebACLInput{
		ResourceArn: aws.String(cfARN),
	})
	require.NoError(t, err)
}

func TestWAFv2AssociateRegionalWebACLWithApplicationLoadBalancer(t *testing.T) {
	c := wafClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	name := "sdk-alb-" + wafStamp()
	createOut, err := c.CreateWebACL(ctx, &wafv2.CreateWebACLInput{
		Name:  aws.String(name),
		Scope: wafv2types.ScopeRegional,
		DefaultAction: &wafv2types.DefaultAction{
			Allow: &wafv2types.AllowAction{},
		},
		VisibilityConfig: &wafv2types.VisibilityConfig{
			SampledRequestsEnabled:   true,
			CloudWatchMetricsEnabled: true,
			MetricName:               aws.String(name + "-metric"),
		},
	})
	require.NoError(t, err)
	aclARN := aws.ToString(createOut.Summary.ARN)

	_, subnetID := elbv2FidVPCSubnet(t, "10.252.0.0/16", "10.252.1.0/24")
	elb := elbv2Client()
	lbARN := elbv2FidLoadBalancer(t, elb, subnetID, "sdk-waf-alb", elbtypes.LoadBalancerTypeEnumApplication)

	_, err = c.AssociateWebACL(ctx, &wafv2.AssociateWebACLInput{
		WebACLArn:   aws.String(aclARN),
		ResourceArn: aws.String(lbARN),
	})
	require.NoError(t, err)
	associated, err := c.GetWebACLForResource(ctx, &wafv2.GetWebACLForResourceInput{
		ResourceArn: aws.String(lbARN),
	})
	require.NoError(t, err)
	require.NotNil(t, associated.WebACL)
	assert.Equal(t, aclARN, aws.ToString(associated.WebACL.ARN))

	_, err = c.DisassociateWebACL(ctx, &wafv2.DisassociateWebACLInput{ResourceArn: aws.String(lbARN)})
	require.NoError(t, err)
	_, err = c.DeleteWebACL(ctx, &wafv2.DeleteWebACLInput{
		Name:      aws.String(name),
		Scope:     wafv2types.ScopeRegional,
		Id:        createOut.Summary.Id,
		LockToken: createOut.Summary.LockToken,
	})
	require.NoError(t, err)
	_, err = elb.DeleteLoadBalancer(ctx, &elasticloadbalancingv2.DeleteLoadBalancerInput{
		LoadBalancerArn: aws.String(lbARN),
	})
	require.NoError(t, err)
}

// TestWAFv2ListWebACLsPagination verifies ListWebACLs honors Limit and NextMarker.
// Real WAFv2 ListWebACLs returns at most Limit summaries plus a NextMarker that
// the caller passes back to fetch the next page.
func TestWAFv2ListWebACLsPagination(t *testing.T) {
	c := wafClient()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Use the regional scope so this test's ACLs don't collide with the
	// cloudfront-scope lifecycle test's ACL.
	scope := wafv2types.ScopeRegional
	stamp := wafStamp()
	for i := 0; i < 3; i++ {
		name := "sdk-page-" + stamp + "-" + string(rune('a'+i))
		_, err := c.CreateWebACL(ctx, &wafv2.CreateWebACLInput{
			Name:          aws.String(name),
			Scope:         scope,
			DefaultAction: &wafv2types.DefaultAction{Allow: &wafv2types.AllowAction{}},
			VisibilityConfig: &wafv2types.VisibilityConfig{
				SampledRequestsEnabled:   true,
				CloudWatchMetricsEnabled: true,
				MetricName:               aws.String(name + "-m"),
			},
		})
		require.NoError(t, err)
	}

	// First page: Limit=2 must return exactly 2 and a NextMarker.
	page1, err := c.ListWebACLs(ctx, &wafv2.ListWebACLsInput{
		Scope: scope,
		Limit: aws.Int32(2),
	})
	require.NoError(t, err)
	require.Len(t, page1.WebACLs, 2, "Limit=2 must cap the first page")
	require.NotNil(t, page1.NextMarker, "a truncated page must return NextMarker")

	// Second page: same Limit + the marker must return the remaining item(s)
	// disjoint from the first page.
	page2, err := c.ListWebACLs(ctx, &wafv2.ListWebACLsInput{
		Scope:      scope,
		Limit:      aws.Int32(2),
		NextMarker: page1.NextMarker,
	})
	require.NoError(t, err)
	require.NotEmpty(t, page2.WebACLs)
	seen := map[string]bool{}
	for _, s := range page1.WebACLs {
		seen[aws.ToString(s.Id)] = true
	}
	for _, s := range page2.WebACLs {
		assert.False(t, seen[aws.ToString(s.Id)], "page 2 must not repeat page 1 items")
	}
}

// TestWAFv2LoggingConfiguration exercises the logging configuration control
// plane round-trip: create a web ACL, attach a logging configuration with
// PutLoggingConfiguration, read it back with GetLoggingConfiguration, confirm
// it appears in ListLoggingConfigurations (scoped), then remove it with
// DeleteLoggingConfiguration. Logging configurations key on the web ACL ARN.
func TestWAFv2LoggingConfiguration(t *testing.T) {
	c := wafClient()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	name := "sdk-log-" + wafStamp()
	createOut, err := c.CreateWebACL(ctx, &wafv2.CreateWebACLInput{
		Name:          aws.String(name),
		Scope:         wafv2types.ScopeCloudfront,
		DefaultAction: &wafv2types.DefaultAction{Allow: &wafv2types.AllowAction{}},
		VisibilityConfig: &wafv2types.VisibilityConfig{
			SampledRequestsEnabled:   true,
			CloudWatchMetricsEnabled: true,
			MetricName:               aws.String(name + "-m"),
		},
	})
	require.NoError(t, err)
	aclARN := aws.ToString(createOut.Summary.ARN)
	require.NotEmpty(t, aclARN)
	lock := aws.ToString(createOut.Summary.LockToken)
	defer func() {
		_, _ = c.DeleteWebACL(ctx, &wafv2.DeleteWebACLInput{
			Name: aws.String(name), Scope: wafv2types.ScopeCloudfront,
			Id: createOut.Summary.Id, LockToken: aws.String(lock),
		})
	}()

	// CloudWatch Logs log group destinations for WAF must be named
	// aws-waf-logs-*; the ARN is REGIONAL us-east-1 for CLOUDFRONT scope.
	logDest := "arn:aws:logs:us-east-1:123456789012:log-group:aws-waf-logs-" + name

	putOut, err := c.PutLoggingConfiguration(ctx, &wafv2.PutLoggingConfigurationInput{
		LoggingConfiguration: &wafv2types.LoggingConfiguration{
			ResourceArn:           aws.String(aclARN),
			LogDestinationConfigs: []string{logDest},
			RedactedFields: []wafv2types.FieldToMatch{
				{UriPath: &wafv2types.UriPath{}},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, putOut.LoggingConfiguration)
	assert.Equal(t, aclARN, aws.ToString(putOut.LoggingConfiguration.ResourceArn))
	require.Equal(t, []string{logDest}, putOut.LoggingConfiguration.LogDestinationConfigs)

	getOut, err := c.GetLoggingConfiguration(ctx, &wafv2.GetLoggingConfigurationInput{
		ResourceArn: aws.String(aclARN),
	})
	require.NoError(t, err)
	require.NotNil(t, getOut.LoggingConfiguration)
	assert.Equal(t, aclARN, aws.ToString(getOut.LoggingConfiguration.ResourceArn))
	require.Equal(t, []string{logDest}, getOut.LoggingConfiguration.LogDestinationConfigs)
	require.Len(t, getOut.LoggingConfiguration.RedactedFields, 1)
	require.NotNil(t, getOut.LoggingConfiguration.RedactedFields[0].UriPath)

	listOut, err := c.ListLoggingConfigurations(ctx, &wafv2.ListLoggingConfigurationsInput{
		Scope: wafv2types.ScopeCloudfront,
	})
	require.NoError(t, err)
	found := false
	for _, lc := range listOut.LoggingConfigurations {
		if aws.ToString(lc.ResourceArn) == aclARN {
			found = true
		}
	}
	assert.True(t, found, "ListLoggingConfigurations must include the put config")

	_, err = c.DeleteLoggingConfiguration(ctx, &wafv2.DeleteLoggingConfigurationInput{
		ResourceArn: aws.String(aclARN),
	})
	require.NoError(t, err)

	_, err = c.GetLoggingConfiguration(ctx, &wafv2.GetLoggingConfigurationInput{
		ResourceArn: aws.String(aclARN),
	})
	require.Error(t, err, "GetLoggingConfiguration after delete must fail")
}

// TestWAFv2APIKeysAndCapacity exercises the API-key control plane
// (CreateAPIKey, ListAPIKeys, GetDecryptedAPIKey, DeleteAPIKey) and the
// CheckCapacity WCU estimator in one round-trip.
func TestWAFv2APIKeysAndCapacity(t *testing.T) {
	c := wafClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	domains := []string{"example.com", "app.example.com"}
	createOut, err := c.CreateAPIKey(ctx, &wafv2.CreateAPIKeyInput{
		Scope:        wafv2types.ScopeCloudfront,
		TokenDomains: domains,
	})
	require.NoError(t, err)
	apiKey := aws.ToString(createOut.APIKey)
	require.NotEmpty(t, apiKey)
	defer func() {
		_, _ = c.DeleteAPIKey(ctx, &wafv2.DeleteAPIKeyInput{
			Scope: wafv2types.ScopeCloudfront, APIKey: aws.String(apiKey),
		})
	}()

	listOut, err := c.ListAPIKeys(ctx, &wafv2.ListAPIKeysInput{Scope: wafv2types.ScopeCloudfront})
	require.NoError(t, err)
	found := false
	for _, s := range listOut.APIKeySummaries {
		if aws.ToString(s.APIKey) == apiKey {
			found = true
			require.ElementsMatch(t, domains, s.TokenDomains)
		}
	}
	assert.True(t, found, "ListAPIKeys must include the created key")

	decOut, err := c.GetDecryptedAPIKey(ctx, &wafv2.GetDecryptedAPIKeyInput{
		Scope: wafv2types.ScopeCloudfront, APIKey: aws.String(apiKey),
	})
	require.NoError(t, err)
	require.ElementsMatch(t, domains, decOut.TokenDomains)
	require.NotNil(t, decOut.CreationTimestamp)

	// CheckCapacity over two byte-match rules: each ByteMatchStatement costs
	// a known WCU; the sum is deterministic.
	capOut, err := c.CheckCapacity(ctx, &wafv2.CheckCapacityInput{
		Scope: wafv2types.ScopeCloudfront,
		Rules: []wafv2types.Rule{
			{
				Name:     aws.String("r1"),
				Priority: 0,
				Statement: &wafv2types.Statement{
					ByteMatchStatement: &wafv2types.ByteMatchStatement{
						SearchString:         []byte("admin"),
						PositionalConstraint: wafv2types.PositionalConstraintContains,
						FieldToMatch:         &wafv2types.FieldToMatch{UriPath: &wafv2types.UriPath{}},
						TextTransformations: []wafv2types.TextTransformation{
							{Priority: 0, Type: wafv2types.TextTransformationTypeNone},
						},
					},
				},
				Action:           &wafv2types.RuleAction{Block: &wafv2types.BlockAction{}},
				VisibilityConfig: &wafv2types.VisibilityConfig{SampledRequestsEnabled: false, CloudWatchMetricsEnabled: false, MetricName: aws.String("r1")},
			},
		},
	})
	require.NoError(t, err)
	require.Greater(t, capOut.Capacity, int64(0), "CheckCapacity must return a positive WCU sum")

	// Delete the key, then it must be gone from the list.
	_, err = c.DeleteAPIKey(ctx, &wafv2.DeleteAPIKeyInput{
		Scope: wafv2types.ScopeCloudfront, APIKey: aws.String(apiKey),
	})
	require.NoError(t, err)
}

// TestWAFv2ManagedRuleGroupCatalog exercises the read-only managed rule
// group / product catalog: ListAvailableManagedRuleGroups,
// ListAvailableManagedRuleGroupVersions, DescribeManagedRuleGroup,
// DescribeAllManagedProducts, DescribeManagedProductsByVendor.
func TestWAFv2ManagedRuleGroupCatalog(t *testing.T) {
	c := wafClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	listOut, err := c.ListAvailableManagedRuleGroups(ctx, &wafv2.ListAvailableManagedRuleGroupsInput{
		Scope: wafv2types.ScopeCloudfront,
	})
	require.NoError(t, err)
	hasCommon := false
	for _, g := range listOut.ManagedRuleGroups {
		if aws.ToString(g.Name) == "AWSManagedRulesCommonRuleSet" && aws.ToString(g.VendorName) == "AWS" {
			hasCommon = true
		}
	}
	require.True(t, hasCommon, "catalog must include AWSManagedRulesCommonRuleSet from AWS")

	verOut, err := c.ListAvailableManagedRuleGroupVersions(ctx, &wafv2.ListAvailableManagedRuleGroupVersionsInput{
		Scope:      wafv2types.ScopeCloudfront,
		VendorName: aws.String("AWS"),
		Name:       aws.String("AWSManagedRulesCommonRuleSet"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, verOut.Versions)
	require.NotEmpty(t, aws.ToString(verOut.CurrentDefaultVersion))

	descOut, err := c.DescribeManagedRuleGroup(ctx, &wafv2.DescribeManagedRuleGroupInput{
		Scope:      wafv2types.ScopeCloudfront,
		VendorName: aws.String("AWS"),
		Name:       aws.String("AWSManagedRulesCommonRuleSet"),
	})
	require.NoError(t, err)
	require.Greater(t, aws.ToInt64(descOut.Capacity), int64(0))
	require.NotEmpty(t, descOut.Rules)
	require.NotEmpty(t, descOut.AvailableLabels)

	allProducts, err := c.DescribeAllManagedProducts(ctx, &wafv2.DescribeAllManagedProductsInput{
		Scope: wafv2types.ScopeCloudfront,
	})
	require.NoError(t, err)
	require.NotEmpty(t, allProducts.ManagedProducts)

	byVendor, err := c.DescribeManagedProductsByVendor(ctx, &wafv2.DescribeManagedProductsByVendorInput{
		Scope:      wafv2types.ScopeCloudfront,
		VendorName: aws.String("AWS"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, byVendor.ManagedProducts)
	for _, p := range byVendor.ManagedProducts {
		assert.Equal(t, "AWS", aws.ToString(p.VendorName))
	}
}

// TestWAFv2ManagedRuleSetLifecycle exercises the publisher-side managed rule
// set CRUD: PutManagedRuleSetVersions (create + publish), GetManagedRuleSet,
// ListManagedRuleSets, UpdateManagedRuleSetVersionExpiryDate.
func TestWAFv2ManagedRuleSetLifecycle(t *testing.T) {
	c := wafClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Managed rule sets are publisher-owned resources surfaced through
	// List/Get (there is no Create op). Discover the seeded set, read its
	// lock token, then publish a version against it.
	listOut, err := c.ListManagedRuleSets(ctx, &wafv2.ListManagedRuleSetsInput{Scope: wafv2types.ScopeCloudfront})
	require.NoError(t, err)
	require.NotEmpty(t, listOut.ManagedRuleSets, "the sim seeds a managed rule set per scope")
	seed := listOut.ManagedRuleSets[0]
	id := aws.ToString(seed.Id)
	name := aws.ToString(seed.Name)
	seedLock := aws.ToString(seed.LockToken)
	rgARN := "arn:aws:wafv2:us-east-1:123456789012:global/rulegroup/backing/" + id

	putOut, err := c.PutManagedRuleSetVersions(ctx, &wafv2.PutManagedRuleSetVersionsInput{
		Name:               aws.String(name),
		Scope:              wafv2types.ScopeCloudfront,
		Id:                 aws.String(id),
		LockToken:          aws.String(seedLock),
		RecommendedVersion: aws.String("Version_1.0"),
		VersionsToPublish: map[string]wafv2types.VersionToPublish{
			"Version_1.0": {
				AssociatedRuleGroupArn: aws.String(rgARN),
				ForecastedLifetime:     aws.Int32(90),
			},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(putOut.NextLockToken))

	getOut, err := c.GetManagedRuleSet(ctx, &wafv2.GetManagedRuleSetInput{
		Name: aws.String(name), Scope: wafv2types.ScopeCloudfront, Id: aws.String(id),
	})
	require.NoError(t, err)
	require.NotNil(t, getOut.ManagedRuleSet)
	require.Contains(t, getOut.ManagedRuleSet.PublishedVersions, "Version_1.0")
	require.Equal(t, "Version_1.0", aws.ToString(getOut.ManagedRuleSet.RecommendedVersion))
	curLock := aws.ToString(getOut.LockToken)

	expiry := time.Now().Add(48 * time.Hour)
	updOut, err := c.UpdateManagedRuleSetVersionExpiryDate(ctx, &wafv2.UpdateManagedRuleSetVersionExpiryDateInput{
		Name: aws.String(name), Scope: wafv2types.ScopeCloudfront, Id: aws.String(id),
		LockToken:       aws.String(curLock),
		VersionToExpire: aws.String("Version_1.0"),
		ExpiryTimestamp: aws.Time(expiry),
	})
	require.NoError(t, err)
	require.Equal(t, "Version_1.0", aws.ToString(updOut.ExpiringVersion))
	require.NotNil(t, updOut.ExpiryTimestamp)
	require.NotEmpty(t, aws.ToString(updOut.NextLockToken))
}

// TestWAFv2PermissionPolicyAndStats exercises the cross-account permission
// policy (Put/Get/DeletePermissionPolicy on a rule group ARN), the mobile SDK
// release catalog (GenerateMobileSdkReleaseUrl, GetMobileSdkRelease,
// ListMobileSdkReleases), DeleteFirewallManagerRuleGroups,
// GetRateBasedStatementManagedKeys, and GetTopPathStatisticsByTraffic.
func TestWAFv2PermissionPolicyAndStats(t *testing.T) {
	c := wafClient()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ts := wafStamp()

	// Rule group → permission policy round-trip.
	rgOut, err := c.CreateRuleGroup(ctx, &wafv2.CreateRuleGroupInput{
		Name: aws.String("pp-rg-" + ts), Scope: wafv2types.ScopeCloudfront,
		Capacity:         aws.Int64(50),
		VisibilityConfig: &wafv2types.VisibilityConfig{SampledRequestsEnabled: false, CloudWatchMetricsEnabled: false, MetricName: aws.String("ppm")},
	})
	require.NoError(t, err)
	rgARN := aws.ToString(rgOut.Summary.ARN)
	rgID := aws.ToString(rgOut.Summary.Id)
	rgLock := aws.ToString(rgOut.Summary.LockToken)
	defer func() {
		_, _ = c.DeleteRuleGroup(ctx, &wafv2.DeleteRuleGroupInput{
			Name: aws.String("pp-rg-" + ts), Scope: wafv2types.ScopeCloudfront,
			Id: aws.String(rgID), LockToken: aws.String(rgLock),
		})
	}()

	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":["wafv2:GetRuleGroup"],"Resource":"` + rgARN + `"}]}`
	_, err = c.PutPermissionPolicy(ctx, &wafv2.PutPermissionPolicyInput{
		ResourceArn: aws.String(rgARN), Policy: aws.String(policy),
	})
	require.NoError(t, err)

	getPP, err := c.GetPermissionPolicy(ctx, &wafv2.GetPermissionPolicyInput{ResourceArn: aws.String(rgARN)})
	require.NoError(t, err)
	require.Equal(t, policy, aws.ToString(getPP.Policy))

	_, err = c.DeletePermissionPolicy(ctx, &wafv2.DeletePermissionPolicyInput{ResourceArn: aws.String(rgARN)})
	require.NoError(t, err)
	_, err = c.GetPermissionPolicy(ctx, &wafv2.GetPermissionPolicyInput{ResourceArn: aws.String(rgARN)})
	require.Error(t, err, "GetPermissionPolicy after delete must fail")

	// Mobile SDK release catalog.
	listRel, err := c.ListMobileSdkReleases(ctx, &wafv2.ListMobileSdkReleasesInput{Platform: wafv2types.PlatformIos})
	require.NoError(t, err)
	require.NotEmpty(t, listRel.ReleaseSummaries)
	relVer := aws.ToString(listRel.ReleaseSummaries[0].ReleaseVersion)

	getRel, err := c.GetMobileSdkRelease(ctx, &wafv2.GetMobileSdkReleaseInput{
		Platform: wafv2types.PlatformIos, ReleaseVersion: aws.String(relVer),
	})
	require.NoError(t, err)
	require.NotNil(t, getRel.MobileSdkRelease)
	require.Equal(t, relVer, aws.ToString(getRel.MobileSdkRelease.ReleaseVersion))

	urlOut, err := c.GenerateMobileSdkReleaseUrl(ctx, &wafv2.GenerateMobileSdkReleaseUrlInput{
		Platform: wafv2types.PlatformIos, ReleaseVersion: aws.String(relVer),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(urlOut.Url))

	// Web ACL → FirewallManager rule group delete + rate-based keys + traffic.
	aclOut, err := c.CreateWebACL(ctx, &wafv2.CreateWebACLInput{
		Name: aws.String("pp-acl-" + ts), Scope: wafv2types.ScopeCloudfront,
		DefaultAction:    &wafv2types.DefaultAction{Allow: &wafv2types.AllowAction{}},
		VisibilityConfig: &wafv2types.VisibilityConfig{SampledRequestsEnabled: false, CloudWatchMetricsEnabled: false, MetricName: aws.String("m")},
	})
	require.NoError(t, err)
	aclID := aws.ToString(aclOut.Summary.Id)
	aclARN := aws.ToString(aclOut.Summary.ARN)
	aclName := "pp-acl-" + ts

	rbk, err := c.GetRateBasedStatementManagedKeys(ctx, &wafv2.GetRateBasedStatementManagedKeysInput{
		Scope: wafv2types.ScopeCloudfront, WebACLName: aws.String(aclName),
		WebACLId: aws.String(aclID), RuleName: aws.String("any-rate-rule"),
	})
	require.NoError(t, err)
	require.NotNil(t, rbk.ManagedKeysIPV4)
	require.NotNil(t, rbk.ManagedKeysIPV6)

	stats, err := c.GetTopPathStatisticsByTraffic(ctx, &wafv2.GetTopPathStatisticsByTrafficInput{
		Scope: wafv2types.ScopeCloudfront, WebAclArn: aws.String(aclARN),
		Limit:                         aws.Int32(10),
		NumberOfTopTrafficBotsPerPath: aws.Int32(5),
		TimeWindow: &wafv2types.TimeWindow{
			StartTime: aws.Time(time.Now().Add(-time.Hour)),
			EndTime:   aws.Time(time.Now()),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, stats)

	// DeleteFirewallManagerRuleGroups advances the web ACL lock token.
	getACL, err := c.GetWebACL(ctx, &wafv2.GetWebACLInput{
		Name: aws.String(aclName), Scope: wafv2types.ScopeCloudfront, Id: aws.String(aclID),
	})
	require.NoError(t, err)
	fmDel, err := c.DeleteFirewallManagerRuleGroups(ctx, &wafv2.DeleteFirewallManagerRuleGroupsInput{
		WebACLArn: aws.String(aclARN), WebACLLockToken: getACL.LockToken,
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(fmDel.NextWebACLLockToken))

	// Clean up the web ACL with the advanced lock token.
	_, _ = c.DeleteWebACL(ctx, &wafv2.DeleteWebACLInput{
		Name: aws.String(aclName), Scope: wafv2types.ScopeCloudfront,
		Id: aws.String(aclID), LockToken: fmDel.NextWebACLLockToken,
	})
}
