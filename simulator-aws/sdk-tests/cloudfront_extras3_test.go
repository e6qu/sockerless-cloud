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

// These tests exercise the CloudFront restXml surface added for distribution
// tenants, connection groups, trust stores, resource policies, WebACL
// associations, alias/domain conflicts, the ListDistributionsBy* projections,
// and the CopyDistribution / UpdateDistributionWithStagingConfig /
// UpdateAnycastIpList / GetManagedCertificateDetails variants — via the real
// aws-sdk-go-v2 cloudfront client.

func cfExtras3Caller() string {
	return "sdk-extras3-" + time.Now().Format("150405.000000000")
}

// cfExtras3MakeDistribution creates a minimal enabled distribution and returns
// its id + current ETag, registering a tolerant cleanup that disables then
// deletes it.
func cfExtras3MakeDistribution(t *testing.T, c *cloudfront.Client, ctx context.Context) (string, string) {
	t.Helper()
	caller := cfExtras3Caller()
	out, err := c.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{
		DistributionConfig: &cftypes.DistributionConfig{
			CallerReference: aws.String(caller),
			Comment:         aws.String("sdk extras3"),
			Enabled:         aws.Bool(true),
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
			ViewerCertificate: &cftypes.ViewerCertificate{CloudFrontDefaultCertificate: aws.Bool(true)},
			Restrictions: &cftypes.Restrictions{
				GeoRestriction: &cftypes.GeoRestriction{RestrictionType: cftypes.GeoRestrictionTypeNone, Quantity: aws.Int32(0)},
			},
		},
	})
	require.NoError(t, err)
	id := aws.ToString(out.Distribution.Id)
	etag := aws.ToString(out.ETag)
	t.Cleanup(func() {
		bg := context.Background()
		g, gerr := c.GetDistribution(bg, &cloudfront.GetDistributionInput{Id: aws.String(id)})
		if gerr != nil {
			return
		}
		cfg := *g.Distribution.DistributionConfig
		cfg.Enabled = aws.Bool(false)
		u, uerr := c.UpdateDistribution(bg, &cloudfront.UpdateDistributionInput{Id: aws.String(id), IfMatch: g.ETag, DistributionConfig: &cfg})
		if uerr == nil {
			_, _ = c.DeleteDistribution(bg, &cloudfront.DeleteDistributionInput{Id: aws.String(id), IfMatch: u.ETag})
		}
	})
	return id, etag
}

// TestCloudFront_CopyAndStaging covers CopyDistribution and
// UpdateDistributionWithStagingConfig (promote-staging-config).
func TestCloudFront_CopyAndStaging(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	id, etag := cfExtras3MakeDistribution(t, c, ctx)

	cp, err := c.CopyDistribution(ctx, &cloudfront.CopyDistributionInput{
		PrimaryDistributionId: aws.String(id),
		IfMatch:               aws.String(etag),
		CallerReference:       aws.String(cfExtras3Caller()),
		Staging:               aws.Bool(true),
	})
	require.NoError(t, err)
	stagingID := aws.ToString(cp.Distribution.Id)
	require.NotEmpty(t, stagingID)
	require.NotEqual(t, id, stagingID)
	t.Cleanup(func() {
		bg := context.Background()
		g, gerr := c.GetDistribution(bg, &cloudfront.GetDistributionInput{Id: aws.String(stagingID)})
		if gerr != nil {
			return
		}
		cfg := *g.Distribution.DistributionConfig
		cfg.Enabled = aws.Bool(false)
		u, uerr := c.UpdateDistribution(bg, &cloudfront.UpdateDistributionInput{Id: aws.String(stagingID), IfMatch: g.ETag, DistributionConfig: &cfg})
		if uerr == nil {
			_, _ = c.DeleteDistribution(bg, &cloudfront.DeleteDistributionInput{Id: aws.String(stagingID), IfMatch: u.ETag})
		}
	})

	// Promote the staging config back onto the primary.
	get, err := c.GetDistribution(ctx, &cloudfront.GetDistributionInput{Id: aws.String(id)})
	require.NoError(t, err)
	pr, err := c.UpdateDistributionWithStagingConfig(ctx, &cloudfront.UpdateDistributionWithStagingConfigInput{
		Id:                    aws.String(id),
		StagingDistributionId: aws.String(stagingID),
		IfMatch:               get.ETag,
	})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(pr.Distribution.Id))
}

// TestCloudFront_DistributionTenant covers the distribution-tenant CRUD,
// by-domain lookup, list, WebACL associate/disassociate, the tenant
// invalidation trio, and GetManagedCertificateDetails.
func TestCloudFront_DistributionTenant(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	distID, _ := cfExtras3MakeDistribution(t, c, ctx)
	domain := "tenant-" + time.Now().Format("150405") + ".example.com"

	create, err := c.CreateDistributionTenant(ctx, &cloudfront.CreateDistributionTenantInput{
		DistributionId: aws.String(distID),
		Name:           aws.String("sdk-tenant-" + time.Now().Format("150405")),
		Domains:        []cftypes.DomainItem{{Domain: aws.String(domain)}},
	})
	require.NoError(t, err)
	tenantID := aws.ToString(create.DistributionTenant.Id)
	require.NotEmpty(t, tenantID)
	etag := aws.ToString(create.ETag)
	t.Cleanup(func() {
		bg := context.Background()
		g, gerr := c.GetDistributionTenant(bg, &cloudfront.GetDistributionTenantInput{Identifier: aws.String(tenantID)})
		if gerr == nil {
			_, _ = c.DeleteDistributionTenant(bg, &cloudfront.DeleteDistributionTenantInput{Id: aws.String(tenantID), IfMatch: g.ETag})
		}
	})

	get, err := c.GetDistributionTenant(ctx, &cloudfront.GetDistributionTenantInput{Identifier: aws.String(tenantID)})
	require.NoError(t, err)
	assert.Equal(t, distID, aws.ToString(get.DistributionTenant.DistributionId))

	byDomain, err := c.GetDistributionTenantByDomain(ctx, &cloudfront.GetDistributionTenantByDomainInput{Domain: aws.String(domain)})
	require.NoError(t, err)
	assert.Equal(t, tenantID, aws.ToString(byDomain.DistributionTenant.Id))

	upd, err := c.UpdateDistributionTenant(ctx, &cloudfront.UpdateDistributionTenantInput{
		Id:      aws.String(tenantID),
		IfMatch: aws.String(etag),
		Enabled: aws.Bool(false),
	})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(upd.DistributionTenant.Enabled))
	etag = aws.ToString(upd.ETag)

	list, err := c.ListDistributionTenants(ctx, &cloudfront.ListDistributionTenantsInput{})
	require.NoError(t, err)
	found := false
	for _, s := range list.DistributionTenantList {
		if aws.ToString(s.Id) == tenantID {
			found = true
		}
	}
	assert.True(t, found)

	byCust, err := c.ListDistributionTenantsByCustomization(ctx, &cloudfront.ListDistributionTenantsByCustomizationInput{})
	require.NoError(t, err)
	require.NotNil(t, byCust)

	assoc, err := c.AssociateDistributionTenantWebACL(ctx, &cloudfront.AssociateDistributionTenantWebACLInput{
		Id:        aws.String(tenantID),
		WebACLArn: aws.String("arn:aws:wafv2:us-east-1:111122223333:global/webacl/tenant/abc"),
		IfMatch:   aws.String(etag),
	})
	require.NoError(t, err)
	assert.Equal(t, tenantID, aws.ToString(assoc.Id))
	etag = aws.ToString(assoc.ETag)

	disassoc, err := c.DisassociateDistributionTenantWebACL(ctx, &cloudfront.DisassociateDistributionTenantWebACLInput{
		Id:      aws.String(tenantID),
		IfMatch: aws.String(etag),
	})
	require.NoError(t, err)
	assert.Equal(t, tenantID, aws.ToString(disassoc.Id))

	// Tenant invalidation trio.
	inv, err := c.CreateInvalidationForDistributionTenant(ctx, &cloudfront.CreateInvalidationForDistributionTenantInput{
		Id: aws.String(tenantID),
		InvalidationBatch: &cftypes.InvalidationBatch{
			CallerReference: aws.String(cfExtras3Caller()),
			Paths:           &cftypes.Paths{Quantity: aws.Int32(1), Items: []string{"/*"}},
		},
	})
	require.NoError(t, err)
	invID := aws.ToString(inv.Invalidation.Id)
	require.NotEmpty(t, invID)

	gi, err := c.GetInvalidationForDistributionTenant(ctx, &cloudfront.GetInvalidationForDistributionTenantInput{
		DistributionTenantId: aws.String(tenantID),
		Id:                   aws.String(invID),
	})
	require.NoError(t, err)
	assert.Equal(t, invID, aws.ToString(gi.Invalidation.Id))

	li, err := c.ListInvalidationsForDistributionTenant(ctx, &cloudfront.ListInvalidationsForDistributionTenantInput{Id: aws.String(tenantID)})
	require.NoError(t, err)
	require.NotNil(t, li.InvalidationList)

	mc, err := c.GetManagedCertificateDetails(ctx, &cloudfront.GetManagedCertificateDetailsInput{Identifier: aws.String(tenantID)})
	require.NoError(t, err)
	require.NotNil(t, mc.ManagedCertificateDetails)
}

// TestCloudFront_ConnectionGroup covers connection-group CRUD, by-routing-endpoint
// lookup, and list.
func TestCloudFront_ConnectionGroup(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	create, err := c.CreateConnectionGroup(ctx, &cloudfront.CreateConnectionGroupInput{
		Name:    aws.String("sdk-cg-" + time.Now().Format("150405.000")),
		Enabled: aws.Bool(true),
	})
	require.NoError(t, err)
	id := aws.ToString(create.ConnectionGroup.Id)
	require.NotEmpty(t, id)
	etag := aws.ToString(create.ETag)
	endpoint := aws.ToString(create.ConnectionGroup.RoutingEndpoint)
	t.Cleanup(func() {
		bg := context.Background()
		g, gerr := c.GetConnectionGroup(bg, &cloudfront.GetConnectionGroupInput{Identifier: aws.String(id)})
		if gerr == nil {
			_, _ = c.DeleteConnectionGroup(bg, &cloudfront.DeleteConnectionGroupInput{Id: aws.String(id), IfMatch: g.ETag})
		}
	})

	get, err := c.GetConnectionGroup(ctx, &cloudfront.GetConnectionGroupInput{Identifier: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(get.ConnectionGroup.Id))

	byEndpoint, err := c.GetConnectionGroupByRoutingEndpoint(ctx, &cloudfront.GetConnectionGroupByRoutingEndpointInput{RoutingEndpoint: aws.String(endpoint)})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(byEndpoint.ConnectionGroup.Id))

	upd, err := c.UpdateConnectionGroup(ctx, &cloudfront.UpdateConnectionGroupInput{
		Id:          aws.String(id),
		IfMatch:     aws.String(etag),
		Ipv6Enabled: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(upd.ConnectionGroup.Ipv6Enabled))

	list, err := c.ListConnectionGroups(ctx, &cloudfront.ListConnectionGroupsInput{})
	require.NoError(t, err)
	found := false
	for _, s := range list.ConnectionGroups {
		if aws.ToString(s.Id) == id {
			found = true
		}
	}
	assert.True(t, found)
}

// TestCloudFront_TrustStore covers trust-store CRUD, list, and
// ListDistributionsByTrustStore.
func TestCloudFront_TrustStore(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	create, err := c.CreateTrustStore(ctx, &cloudfront.CreateTrustStoreInput{
		Name: aws.String("sdk-ts-" + time.Now().Format("150405.000")),
		CaCertificatesBundleSource: &cftypes.CaCertificatesBundleSourceMemberCaCertificatesBundleS3Location{
			Value: cftypes.CaCertificatesBundleS3Location{
				Bucket: aws.String("my-bucket"),
				Key:    aws.String("ca-bundle.pem"),
				Region: aws.String("us-east-1"),
			},
		},
	})
	require.NoError(t, err)
	id := aws.ToString(create.TrustStore.Id)
	require.NotEmpty(t, id)
	etag := aws.ToString(create.ETag)
	t.Cleanup(func() {
		bg := context.Background()
		g, gerr := c.GetTrustStore(bg, &cloudfront.GetTrustStoreInput{Identifier: aws.String(id)})
		if gerr == nil {
			_, _ = c.DeleteTrustStore(bg, &cloudfront.DeleteTrustStoreInput{Id: aws.String(id), IfMatch: g.ETag})
		}
	})

	get, err := c.GetTrustStore(ctx, &cloudfront.GetTrustStoreInput{Identifier: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(get.TrustStore.Id))

	upd, err := c.UpdateTrustStore(ctx, &cloudfront.UpdateTrustStoreInput{
		Id:      aws.String(id),
		IfMatch: aws.String(etag),
		CaCertificatesBundleSource: &cftypes.CaCertificatesBundleSourceMemberCaCertificatesBundleS3Location{
			Value: cftypes.CaCertificatesBundleS3Location{
				Bucket: aws.String("my-bucket"),
				Key:    aws.String("ca-bundle-2.pem"),
				Region: aws.String("us-east-1"),
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, upd.TrustStore)

	list, err := c.ListTrustStores(ctx, &cloudfront.ListTrustStoresInput{})
	require.NoError(t, err)
	found := false
	for _, s := range list.TrustStoreList {
		if aws.ToString(s.Id) == id {
			found = true
		}
	}
	assert.True(t, found)

	byTS, err := c.ListDistributionsByTrustStore(ctx, &cloudfront.ListDistributionsByTrustStoreInput{TrustStoreIdentifier: aws.String(id)})
	require.NoError(t, err)
	require.NotNil(t, byTS.DistributionList)
}

// TestCloudFront_ResourcePolicy covers Put/Get/DeleteResourcePolicy.
func TestCloudFront_ResourcePolicy(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	arn := "arn:aws:cloudfront::111122223333:distribution-tenant/DT" + time.Now().Format("150405")
	doc := `{"Version":"2012-10-17","Statement":[]}`

	put, err := c.PutResourcePolicy(ctx, &cloudfront.PutResourcePolicyInput{
		ResourceArn:    aws.String(arn),
		PolicyDocument: aws.String(doc),
	})
	require.NoError(t, err)
	assert.Equal(t, arn, aws.ToString(put.ResourceArn))

	get, err := c.GetResourcePolicy(ctx, &cloudfront.GetResourcePolicyInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, doc, aws.ToString(get.PolicyDocument))

	_, err = c.DeleteResourcePolicy(ctx, &cloudfront.DeleteResourcePolicyInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)

	_, err = c.GetResourcePolicy(ctx, &cloudfront.GetResourcePolicyInput{ResourceArn: aws.String(arn)})
	require.Error(t, err)
}

// TestCloudFront_DistributionWebACLAndAlias covers the distribution WebACL
// associate/disassociate, AssociateAlias, ListConflictingAliases,
// ListDomainConflicts, UpdateDomainAssociation, and VerifyDnsConfiguration.
func TestCloudFront_DistributionWebACLAndAlias(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	id, etag := cfExtras3MakeDistribution(t, c, ctx)

	assoc, err := c.AssociateDistributionWebACL(ctx, &cloudfront.AssociateDistributionWebACLInput{
		Id:        aws.String(id),
		WebACLArn: aws.String("arn:aws:wafv2:us-east-1:111122223333:global/webacl/dist/abc"),
		IfMatch:   aws.String(etag),
	})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(assoc.Id))
	etag = aws.ToString(assoc.ETag)

	disassoc, err := c.DisassociateDistributionWebACL(ctx, &cloudfront.DisassociateDistributionWebACLInput{
		Id:      aws.String(id),
		IfMatch: aws.String(etag),
	})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(disassoc.Id))

	alias := "alias-" + time.Now().Format("150405") + ".example.com"
	_, err = c.AssociateAlias(ctx, &cloudfront.AssociateAliasInput{
		TargetDistributionId: aws.String(id),
		Alias:                aws.String(alias),
	})
	require.NoError(t, err)

	// A second distribution claiming the same alias yields a conflict, and the
	// conflict names the alias and the distribution already holding it.
	other, _ := cfExtras3MakeDistribution(t, c, ctx)
	conf, err := c.ListConflictingAliases(ctx, &cloudfront.ListConflictingAliasesInput{
		DistributionId: aws.String(other),
		Alias:          aws.String(alias),
	})
	require.NoError(t, err)
	require.NotNil(t, conf.ConflictingAliasesList)
	require.NotEmpty(t, conf.ConflictingAliasesList.Items,
		"the alias another distribution holds must be reported as conflicting")
	assert.Equal(t, len(conf.ConflictingAliasesList.Items), int(aws.ToInt32(conf.ConflictingAliasesList.Quantity)))
	conflicting := conf.ConflictingAliasesList.Items[0]
	assert.Equal(t, alias, aws.ToString(conflicting.Alias))
	assert.Equal(t, id, aws.ToString(conflicting.DistributionId),
		"the conflict must name the distribution that holds the alias")

	// An alias nobody holds conflicts with nothing.
	free, err := c.ListConflictingAliases(ctx, &cloudfront.ListConflictingAliasesInput{
		DistributionId: aws.String(other),
		Alias:          aws.String("unclaimed-" + alias),
	})
	require.NoError(t, err)
	require.NotNil(t, free.ConflictingAliasesList)
	assert.Empty(t, free.ConflictingAliasesList.Items)

	dc, err := c.ListDomainConflicts(ctx, &cloudfront.ListDomainConflictsInput{
		Domain:                          aws.String(alias),
		DomainControlValidationResource: &cftypes.DistributionResourceId{DistributionId: aws.String(id)},
	})
	require.NoError(t, err)
	require.NotEmpty(t, dc.DomainConflicts, "the domain this distribution holds must be reported")
	assert.Equal(t, alias, aws.ToString(dc.DomainConflicts[0].Domain))

	uda, err := c.UpdateDomainAssociation(ctx, &cloudfront.UpdateDomainAssociationInput{
		Domain:         aws.String(alias),
		TargetResource: &cftypes.DistributionResourceId{DistributionId: aws.String(id)},
	})
	require.NoError(t, err)
	assert.Equal(t, alias, aws.ToString(uda.Domain))

	vdc, err := c.VerifyDnsConfiguration(ctx, &cloudfront.VerifyDnsConfigurationInput{
		Identifier: aws.String(id),
		Domain:     aws.String(alias),
	})
	require.NoError(t, err)
	require.NotNil(t, vdc.DnsConfigurationList)
}

// TestCloudFront_ListDistributionsByResource covers the ListDistributionsBy*
// projections. Every projection is asserted twice: against the resource the
// distribution really carries, which must return it, and against one it does
// not, which must not. A projection that ignores its filter fails one arm or
// the other, and each list's Quantity is asserted against the items in it,
// which is the wire contract a paginating client reads.
func TestCloudFront_ListDistributionsByResource(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stamp := time.Now().Format("150405.000000000")
	cachePolicy := "cp-" + stamp
	originPolicy := "orp-" + stamp
	headersPolicy := "rhp-" + stamp
	keyGroup := "kg-" + stamp
	anycastIPList := "aipl-" + stamp
	webACL := "arn:aws:wafv2:us-east-1:000000000000:global/webacl/sdk-" + stamp + "/1"

	id := cfExtras3MakeReferencingDistribution(t, c, ctx, cftypes.DistributionConfig{
		AnycastIpListId: aws.String(anycastIPList),
		ConnectionMode:  cftypes.ConnectionModeTenantOnly,
		WebACLId:        aws.String(webACL),
		DefaultCacheBehavior: &cftypes.DefaultCacheBehavior{
			TargetOriginId:          aws.String("o1"),
			ViewerProtocolPolicy:    cftypes.ViewerProtocolPolicyAllowAll,
			CachePolicyId:           aws.String(cachePolicy),
			OriginRequestPolicyId:   aws.String(originPolicy),
			ResponseHeadersPolicyId: aws.String(headersPolicy),
			TrustedKeyGroups: &cftypes.TrustedKeyGroups{
				Enabled:  aws.Bool(true),
				Quantity: aws.Int32(1),
				Items:    []string{keyGroup},
			},
		},
	})

	idsOf := func(list *cftypes.DistributionIdList) []string {
		require.NotNil(t, list)
		require.Equal(t, len(list.Items), int(aws.ToInt32(list.Quantity)),
			"Quantity must count the identifiers returned")
		return list.Items
	}
	summariesOf := func(list *cftypes.DistributionList) []string {
		require.NotNil(t, list)
		require.Equal(t, len(list.Items), int(aws.ToInt32(list.Quantity)),
			"Quantity must count the distributions returned")
		ids := make([]string, 0, len(list.Items))
		for _, item := range list.Items {
			ids = append(ids, aws.ToString(item.Id))
		}
		return ids
	}

	byCache, err := c.ListDistributionsByCachePolicyId(ctx, &cloudfront.ListDistributionsByCachePolicyIdInput{CachePolicyId: aws.String(cachePolicy)})
	require.NoError(t, err)
	assert.Contains(t, idsOf(byCache.DistributionIdList), id)
	byOtherCache, err := c.ListDistributionsByCachePolicyId(ctx, &cloudfront.ListDistributionsByCachePolicyIdInput{CachePolicyId: aws.String("cp-other-" + stamp)})
	require.NoError(t, err)
	assert.NotContains(t, idsOf(byOtherCache.DistributionIdList), id)

	byOrigin, err := c.ListDistributionsByOriginRequestPolicyId(ctx, &cloudfront.ListDistributionsByOriginRequestPolicyIdInput{OriginRequestPolicyId: aws.String(originPolicy)})
	require.NoError(t, err)
	assert.Contains(t, idsOf(byOrigin.DistributionIdList), id)
	byOtherOrigin, err := c.ListDistributionsByOriginRequestPolicyId(ctx, &cloudfront.ListDistributionsByOriginRequestPolicyIdInput{OriginRequestPolicyId: aws.String("orp-other-" + stamp)})
	require.NoError(t, err)
	assert.NotContains(t, idsOf(byOtherOrigin.DistributionIdList), id)

	byResp, err := c.ListDistributionsByResponseHeadersPolicyId(ctx, &cloudfront.ListDistributionsByResponseHeadersPolicyIdInput{ResponseHeadersPolicyId: aws.String(headersPolicy)})
	require.NoError(t, err)
	assert.Contains(t, idsOf(byResp.DistributionIdList), id)
	byOtherResp, err := c.ListDistributionsByResponseHeadersPolicyId(ctx, &cloudfront.ListDistributionsByResponseHeadersPolicyIdInput{ResponseHeadersPolicyId: aws.String("rhp-other-" + stamp)})
	require.NoError(t, err)
	assert.NotContains(t, idsOf(byOtherResp.DistributionIdList), id)

	byKG, err := c.ListDistributionsByKeyGroup(ctx, &cloudfront.ListDistributionsByKeyGroupInput{KeyGroupId: aws.String(keyGroup)})
	require.NoError(t, err)
	assert.Contains(t, idsOf(byKG.DistributionIdList), id)
	byOtherKG, err := c.ListDistributionsByKeyGroup(ctx, &cloudfront.ListDistributionsByKeyGroupInput{KeyGroupId: aws.String("kg-other-" + stamp)})
	require.NoError(t, err)
	assert.NotContains(t, idsOf(byOtherKG.DistributionIdList), id)

	byAnycast, err := c.ListDistributionsByAnycastIpListId(ctx, &cloudfront.ListDistributionsByAnycastIpListIdInput{AnycastIpListId: aws.String(anycastIPList)})
	require.NoError(t, err)
	assert.Contains(t, summariesOf(byAnycast.DistributionList), id)
	byOtherAnycast, err := c.ListDistributionsByAnycastIpListId(ctx, &cloudfront.ListDistributionsByAnycastIpListIdInput{AnycastIpListId: aws.String("aipl-other-" + stamp)})
	require.NoError(t, err)
	assert.NotContains(t, summariesOf(byOtherAnycast.DistributionList), id)

	byWebACL, err := c.ListDistributionsByWebACLId(ctx, &cloudfront.ListDistributionsByWebACLIdInput{WebACLId: aws.String(webACL)})
	require.NoError(t, err)
	assert.Contains(t, summariesOf(byWebACL.DistributionList), id)
	byOtherWebACL, err := c.ListDistributionsByWebACLId(ctx, &cloudfront.ListDistributionsByWebACLIdInput{WebACLId: aws.String(webACL + "-other")})
	require.NoError(t, err)
	assert.NotContains(t, summariesOf(byOtherWebACL.DistributionList), id)

	// The distribution is tenant-only, so it appears under that mode and not
	// under direct: the mode selects, rather than the distribution merely
	// existing.
	byTenantOnly, err := c.ListDistributionsByConnectionMode(ctx, &cloudfront.ListDistributionsByConnectionModeInput{ConnectionMode: cftypes.ConnectionModeTenantOnly})
	require.NoError(t, err)
	assert.Contains(t, summariesOf(byTenantOnly.DistributionList), id)
	byDirect, err := c.ListDistributionsByConnectionMode(ctx, &cloudfront.ListDistributionsByConnectionModeInput{ConnectionMode: cftypes.ConnectionModeDirect})
	require.NoError(t, err)
	assert.NotContains(t, summariesOf(byDirect.DistributionList), id)

	// It names no VPC origin and no real-time log configuration, and owns no
	// resource of another account, so none of those three projects it.
	byVpc, err := c.ListDistributionsByVpcOriginId(ctx, &cloudfront.ListDistributionsByVpcOriginIdInput{VpcOriginId: aws.String("vo-" + stamp)})
	require.NoError(t, err)
	assert.NotContains(t, idsOf(byVpc.DistributionIdList), id)

	byRTL, err := c.ListDistributionsByRealtimeLogConfig(ctx, &cloudfront.ListDistributionsByRealtimeLogConfigInput{RealtimeLogConfigName: aws.String("rtl-" + stamp)})
	require.NoError(t, err)
	assert.NotContains(t, summariesOf(byRTL.DistributionList), id)

	byOwned, err := c.ListDistributionsByOwnedResource(ctx, &cloudfront.ListDistributionsByOwnedResourceInput{ResourceArn: aws.String("arn:aws:wafv2:us-east-1:111122223333:global/webacl/x/y")})
	require.NoError(t, err)
	require.NotNil(t, byOwned.DistributionList)
	assert.Equal(t, len(byOwned.DistributionList.Items), int(aws.ToInt32(byOwned.DistributionList.Quantity)))
	for _, owner := range byOwned.DistributionList.Items {
		assert.NotEqual(t, id, aws.ToString(owner.DistributionId))
	}
}

// cfExtras3MakeReferencingDistribution creates a distribution whose
// configuration carries the references the caller supplies, on top of the
// minimal origin every distribution in this file uses, and returns its id.
func cfExtras3MakeReferencingDistribution(t *testing.T, c *cloudfront.Client, ctx context.Context, overrides cftypes.DistributionConfig) string {
	t.Helper()
	config := &cftypes.DistributionConfig{
		CallerReference: aws.String(cfExtras3Caller()),
		Comment:         aws.String("sdk extras3 by-resource"),
		Enabled:         aws.Bool(true),
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
		DefaultCacheBehavior: overrides.DefaultCacheBehavior,
		AnycastIpListId:      overrides.AnycastIpListId,
		ConnectionMode:       overrides.ConnectionMode,
		WebACLId:             overrides.WebACLId,
		ViewerCertificate:    &cftypes.ViewerCertificate{CloudFrontDefaultCertificate: aws.Bool(true)},
		Restrictions: &cftypes.Restrictions{
			GeoRestriction: &cftypes.GeoRestriction{RestrictionType: cftypes.GeoRestrictionTypeNone, Quantity: aws.Int32(0)},
		},
	}
	out, err := c.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{DistributionConfig: config})
	require.NoError(t, err)
	id := aws.ToString(out.Distribution.Id)
	require.NotEmpty(t, id)
	t.Cleanup(func() {
		bg := context.Background()
		g, gerr := c.GetDistribution(bg, &cloudfront.GetDistributionInput{Id: aws.String(id)})
		if gerr != nil {
			return
		}
		cfg := *g.Distribution.DistributionConfig
		cfg.Enabled = aws.Bool(false)
		u, uerr := c.UpdateDistribution(bg, &cloudfront.UpdateDistributionInput{Id: aws.String(id), IfMatch: g.ETag, DistributionConfig: &cfg})
		if uerr == nil {
			_, _ = c.DeleteDistribution(bg, &cloudfront.DeleteDistributionInput{Id: aws.String(id), IfMatch: u.ETag})
		}
	})
	return id
}

// TestCloudFront_UpdateAnycastIpList covers UpdateAnycastIpList over a created
// Anycast IP list.
func TestCloudFront_UpdateAnycastIpList(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	create, err := c.CreateAnycastIpList(ctx, &cloudfront.CreateAnycastIpListInput{
		Name:    aws.String("sdk-aipl-" + time.Now().Format("150405.000")),
		IpCount: aws.Int32(2),
	})
	require.NoError(t, err)
	id := aws.ToString(create.AnycastIpList.Id)
	require.NotEmpty(t, id)
	etag := aws.ToString(create.ETag)
	t.Cleanup(func() {
		bg := context.Background()
		g, gerr := c.GetAnycastIpList(bg, &cloudfront.GetAnycastIpListInput{Id: aws.String(id)})
		if gerr == nil {
			_, _ = c.DeleteAnycastIpList(bg, &cloudfront.DeleteAnycastIpListInput{Id: aws.String(id), IfMatch: g.ETag})
		}
	})

	require.NotNil(t, create.AnycastIpList)
	assert.Len(t, create.AnycastIpList.AnycastIps, 2, "IpCount addresses are allocated")

	// The update carries a change, so the response and a later read both have
	// to show it: an update that echoed the stored list would pass neither.
	upd, err := c.UpdateAnycastIpList(ctx, &cloudfront.UpdateAnycastIpListInput{
		Id:            aws.String(id),
		IfMatch:       aws.String(etag),
		IpAddressType: cftypes.IpAddressTypeDualStack,
	})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(upd.AnycastIpList.Id))
	assert.Equal(t, cftypes.IpAddressTypeDualStack, upd.AnycastIpList.IpAddressType)
	assert.NotEqual(t, etag, aws.ToString(upd.ETag), "an update issues a new entity tag")

	reread, err := c.GetAnycastIpList(ctx, &cloudfront.GetAnycastIpListInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, cftypes.IpAddressTypeDualStack, reread.AnycastIpList.IpAddressType,
		"the change must be what a later read returns")

	// The stale entity tag is refused, which is what makes the concurrency
	// control real rather than decorative.
	_, err = c.UpdateAnycastIpList(ctx, &cloudfront.UpdateAnycastIpListInput{
		Id:            aws.String(id),
		IfMatch:       aws.String(etag),
		IpAddressType: cftypes.IpAddressTypeIpv4,
	})
	assert.Equal(t, "PreconditionFailed", errCode(t, err))
}
