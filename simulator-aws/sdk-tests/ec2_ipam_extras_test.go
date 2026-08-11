package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ipamExtrasFixtureIpam creates an IPAM and returns its ID plus a pool under
// its private default scope, with tolerant cleanup. The extra IPAM families
// (policies, prefix-list resolvers, external tokens) all hang off a parent
// IPAM, so the round-trip tests share this fixture.
func ipamExtrasFixtureIpam(t *testing.T, c *ec2.Client) (ipamID, scopeID string) {
	t.Helper()
	created, err := c.CreateIpam(ctx, &ec2.CreateIpamInput{
		Description: aws.String("ipam-extras"),
		OperatingRegions: []types.AddIpamOperatingRegion{
			{RegionName: aws.String("us-east-1")},
		},
	})
	require.NoError(t, err)
	ipamID = aws.ToString(created.Ipam.IpamId)
	scopeID = aws.ToString(created.Ipam.PrivateDefaultScopeId)
	require.NotEmpty(t, ipamID)
	t.Cleanup(func() { _, _ = c.DeleteIpam(ctx, &ec2.DeleteIpamInput{IpamId: aws.String(ipamID)}) })
	return ipamID, scopeID
}

// TestEC2_IpamPolicyLifecycleSDK exercises the IPAM policy family end-to-end:
// create, describe read-back, enable/disable with the enabled-policy query,
// allocation-rule modify+get, and the organization-targets read.
func TestEC2_IpamPolicyLifecycleSDK(t *testing.T) {
	c := ec2Client()
	ipamID, _ := ipamExtrasFixtureIpam(t, c)

	pool := ipamExtrasFixturePool(t, c, ipamID)

	created, err := c.CreateIpamPolicy(ctx, &ec2.CreateIpamPolicyInput{
		IpamId: aws.String(ipamID),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeIpamPolicy,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("policy-a")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, created.IpamPolicy)
	policyID := aws.ToString(created.IpamPolicy.IpamPolicyId)
	require.NotEmpty(t, policyID)
	assert.Equal(t, ipamID, aws.ToString(created.IpamPolicy.IpamId))
	assert.NotEmpty(t, aws.ToString(created.IpamPolicy.IpamPolicyArn))
	assert.Equal(t, types.IpamPolicyStateCreateComplete, created.IpamPolicy.State)
	t.Cleanup(func() {
		_, _ = c.DeleteIpamPolicy(ctx, &ec2.DeleteIpamPolicyInput{IpamPolicyId: aws.String(policyID)})
	})

	desc, err := c.DescribeIpamPolicies(ctx, &ec2.DescribeIpamPoliciesInput{
		IpamPolicyIds: []string{policyID},
	})
	require.NoError(t, err)
	require.Len(t, desc.IpamPolicies, 1)
	assert.Equal(t, policyID, aws.ToString(desc.IpamPolicies[0].IpamPolicyId))
	var nameTag string
	for _, tg := range desc.IpamPolicies[0].Tags {
		if aws.ToString(tg.Key) == "Name" {
			nameTag = aws.ToString(tg.Value)
		}
	}
	assert.Equal(t, "policy-a", nameTag)

	// Allocation rules: modify then read back.
	mod, err := c.ModifyIpamPolicyAllocationRules(ctx, &ec2.ModifyIpamPolicyAllocationRulesInput{
		IpamPolicyId: aws.String(policyID),
		Locale:       aws.String("us-east-1"),
		ResourceType: types.IpamPolicyResourceTypeEip,
		AllocationRules: []types.IpamPolicyAllocationRuleRequest{
			{SourceIpamPoolId: aws.String(pool)},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, mod.IpamPolicyDocument)
	require.Len(t, mod.IpamPolicyDocument.AllocationRules, 1)
	assert.Equal(t, pool, aws.ToString(mod.IpamPolicyDocument.AllocationRules[0].SourceIpamPoolId))

	rules, err := c.GetIpamPolicyAllocationRules(ctx, &ec2.GetIpamPolicyAllocationRulesInput{
		IpamPolicyId: aws.String(policyID),
	})
	require.NoError(t, err)
	require.Len(t, rules.IpamPolicyDocuments, 1)
	require.Len(t, rules.IpamPolicyDocuments[0].AllocationRules, 1)
	assert.Equal(t, pool, aws.ToString(rules.IpamPolicyDocuments[0].AllocationRules[0].SourceIpamPoolId))
	assert.Equal(t, "us-east-1", aws.ToString(rules.IpamPolicyDocuments[0].Locale))

	// Enable / GetEnabled / Disable.
	en, err := c.EnableIpamPolicy(ctx, &ec2.EnableIpamPolicyInput{IpamPolicyId: aws.String(policyID)})
	require.NoError(t, err)
	assert.Equal(t, policyID, aws.ToString(en.IpamPolicyId))

	got, err := c.GetEnabledIpamPolicy(ctx, &ec2.GetEnabledIpamPolicyInput{})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(got.IpamPolicyEnabled))
	assert.Equal(t, policyID, aws.ToString(got.IpamPolicyId))
	assert.Equal(t, types.IpamPolicyManagedByAccount, got.ManagedBy)

	dis, err := c.DisableIpamPolicy(ctx, &ec2.DisableIpamPolicyInput{IpamPolicyId: aws.String(policyID)})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(dis.Return))

	gotOff, err := c.GetEnabledIpamPolicy(ctx, &ec2.GetEnabledIpamPolicyInput{})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(gotOff.IpamPolicyEnabled))

	// Organization targets read (honest-empty for an account-managed policy).
	orgs, err := c.GetIpamPolicyOrganizationTargets(ctx, &ec2.GetIpamPolicyOrganizationTargetsInput{
		IpamPolicyId: aws.String(policyID),
	})
	require.NoError(t, err)
	assert.Empty(t, orgs.OrganizationTargets)
}

// ipamExtrasFixturePool provisions a small pool under the IPAM's private
// default scope so allocation-rule and pool-allocation tests have a real pool
// to reference.
func ipamExtrasFixturePool(t *testing.T, c *ec2.Client, ipamID string) string {
	t.Helper()
	ipam, err := c.DescribeIpams(ctx, &ec2.DescribeIpamsInput{IpamIds: []string{ipamID}})
	require.NoError(t, err)
	require.Len(t, ipam.Ipams, 1)
	scopeID := aws.ToString(ipam.Ipams[0].PrivateDefaultScopeId)
	pool, err := c.CreateIpamPool(ctx, &ec2.CreateIpamPoolInput{
		IpamScopeId:   aws.String(scopeID),
		AddressFamily: types.AddressFamilyIpv4,
	})
	require.NoError(t, err)
	poolID := aws.ToString(pool.IpamPool.IpamPoolId)
	require.NotEmpty(t, poolID)
	t.Cleanup(func() { _, _ = c.DeleteIpamPool(ctx, &ec2.DeleteIpamPoolInput{IpamPoolId: aws.String(poolID)}) })
	return poolID
}

// TestEC2_IpamByoasnLifecycleSDK exercises the BYOASN family: provision an ASN
// into the IPAM, describe it, associate/disassociate it to a CIDR, deprovision
// it, and move a BYOIP CIDR into an IPAM pool.
func TestEC2_IpamByoasnLifecycleSDK(t *testing.T) {
	c := ec2Client()
	ipamID, _ := ipamExtrasFixtureIpam(t, c)
	poolID := ipamExtrasFixturePool(t, c, ipamID)

	const asn = "64512"
	prov, err := c.ProvisionIpamByoasn(ctx, &ec2.ProvisionIpamByoasnInput{
		IpamId: aws.String(ipamID),
		Asn:    aws.String(asn),
		AsnAuthorizationContext: &types.AsnAuthorizationContext{
			Message:   aws.String("authorize-asn"),
			Signature: aws.String("c2lnbmF0dXJl"),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, prov.Byoasn)
	assert.Equal(t, asn, aws.ToString(prov.Byoasn.Asn))
	assert.Equal(t, ipamID, aws.ToString(prov.Byoasn.IpamId))
	t.Cleanup(func() {
		_, _ = c.DeprovisionIpamByoasn(ctx, &ec2.DeprovisionIpamByoasnInput{
			IpamId: aws.String(ipamID),
			Asn:    aws.String(asn),
		})
	})

	desc, err := c.DescribeIpamByoasn(ctx, &ec2.DescribeIpamByoasnInput{})
	require.NoError(t, err)
	var seen bool
	for _, b := range desc.Byoasns {
		if aws.ToString(b.Asn) == asn {
			seen = true
			assert.Equal(t, ipamID, aws.ToString(b.IpamId))
		}
	}
	assert.True(t, seen, "provisioned ASN should be in DescribeIpamByoasn")

	assoc, err := c.AssociateIpamByoasn(ctx, &ec2.AssociateIpamByoasnInput{
		Asn:  aws.String(asn),
		Cidr: aws.String("192.0.2.0/24"),
	})
	require.NoError(t, err)
	require.NotNil(t, assoc.AsnAssociation)
	assert.Equal(t, asn, aws.ToString(assoc.AsnAssociation.Asn))
	assert.Equal(t, "192.0.2.0/24", aws.ToString(assoc.AsnAssociation.Cidr))

	disassoc, err := c.DisassociateIpamByoasn(ctx, &ec2.DisassociateIpamByoasnInput{
		Asn:  aws.String(asn),
		Cidr: aws.String("192.0.2.0/24"),
	})
	require.NoError(t, err)
	require.NotNil(t, disassoc.AsnAssociation)
	assert.Equal(t, asn, aws.ToString(disassoc.AsnAssociation.Asn))

	moved, err := c.MoveByoipCidrToIpam(ctx, &ec2.MoveByoipCidrToIpamInput{
		Cidr:          aws.String("198.51.100.0/24"),
		IpamPoolId:    aws.String(poolID),
		IpamPoolOwner: aws.String("123456789012"),
	})
	require.NoError(t, err)
	require.NotNil(t, moved.ByoipCidr)
	assert.Equal(t, "198.51.100.0/24", aws.ToString(moved.ByoipCidr.Cidr))
}

// TestEC2_IpamPrefixListResolverLifecycleSDK exercises the prefix-list resolver
// family: create a resolver with a static-CIDR rule, describe + modify it,
// create a target, describe + modify the target, read the resolver rules and
// versioned entries, then delete everything.
func TestEC2_IpamPrefixListResolverLifecycleSDK(t *testing.T) {
	c := ec2Client()
	ipamID, _ := ipamExtrasFixtureIpam(t, c)

	created, err := c.CreateIpamPrefixListResolver(ctx, &ec2.CreateIpamPrefixListResolverInput{
		IpamId:        aws.String(ipamID),
		Description:   aws.String("resolver-a"),
		AddressFamily: types.AddressFamilyIpv4,
		Rules: []types.IpamPrefixListResolverRuleRequest{
			{
				RuleType:   types.IpamPrefixListResolverRuleTypeStaticCidr,
				StaticCidr: aws.String("10.0.0.0/16"),
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, created.IpamPrefixListResolver)
	resolverID := aws.ToString(created.IpamPrefixListResolver.IpamPrefixListResolverId)
	require.NotEmpty(t, resolverID)
	assert.Equal(t, "resolver-a", aws.ToString(created.IpamPrefixListResolver.Description))
	assert.Equal(t, types.IpamPrefixListResolverStateCreateComplete, created.IpamPrefixListResolver.State)
	t.Cleanup(func() {
		_, _ = c.DeleteIpamPrefixListResolver(ctx, &ec2.DeleteIpamPrefixListResolverInput{
			IpamPrefixListResolverId: aws.String(resolverID),
		})
	})

	desc, err := c.DescribeIpamPrefixListResolvers(ctx, &ec2.DescribeIpamPrefixListResolversInput{
		IpamPrefixListResolverIds: []string{resolverID},
	})
	require.NoError(t, err)
	require.Len(t, desc.IpamPrefixListResolvers, 1)
	assert.Equal(t, resolverID, aws.ToString(desc.IpamPrefixListResolvers[0].IpamPrefixListResolverId))

	mod, err := c.ModifyIpamPrefixListResolver(ctx, &ec2.ModifyIpamPrefixListResolverInput{
		IpamPrefixListResolverId: aws.String(resolverID),
		Description:              aws.String("resolver-a-updated"),
	})
	require.NoError(t, err)
	assert.Equal(t, "resolver-a-updated", aws.ToString(mod.IpamPrefixListResolver.Description))

	rules, err := c.GetIpamPrefixListResolverRules(ctx, &ec2.GetIpamPrefixListResolverRulesInput{
		IpamPrefixListResolverId: aws.String(resolverID),
	})
	require.NoError(t, err)
	require.Len(t, rules.Rules, 1)
	assert.Equal(t, "10.0.0.0/16", aws.ToString(rules.Rules[0].StaticCidr))

	versions, err := c.GetIpamPrefixListResolverVersions(ctx, &ec2.GetIpamPrefixListResolverVersionsInput{
		IpamPrefixListResolverId: aws.String(resolverID),
	})
	require.NoError(t, err)
	require.Len(t, versions.IpamPrefixListResolverVersions, 1)
	assert.Equal(t, int64(1), aws.ToInt64(versions.IpamPrefixListResolverVersions[0].Version))

	entries, err := c.GetIpamPrefixListResolverVersionEntries(ctx, &ec2.GetIpamPrefixListResolverVersionEntriesInput{
		IpamPrefixListResolverId:      aws.String(resolverID),
		IpamPrefixListResolverVersion: aws.Int64(1),
	})
	require.NoError(t, err)
	require.Len(t, entries.Entries, 1)
	assert.Equal(t, "10.0.0.0/16", aws.ToString(entries.Entries[0].Cidr))

	// Target round-trip.
	tgt, err := c.CreateIpamPrefixListResolverTarget(ctx, &ec2.CreateIpamPrefixListResolverTargetInput{
		IpamPrefixListResolverId: aws.String(resolverID),
		PrefixListId:             aws.String("pl-0123456789abcdef0"),
		PrefixListRegion:         aws.String("us-east-1"),
		TrackLatestVersion:       aws.Bool(true),
	})
	require.NoError(t, err)
	require.NotNil(t, tgt.IpamPrefixListResolverTarget)
	targetID := aws.ToString(tgt.IpamPrefixListResolverTarget.IpamPrefixListResolverTargetId)
	require.NotEmpty(t, targetID)
	assert.Equal(t, "pl-0123456789abcdef0", aws.ToString(tgt.IpamPrefixListResolverTarget.PrefixListId))
	assert.True(t, aws.ToBool(tgt.IpamPrefixListResolverTarget.TrackLatestVersion))
	t.Cleanup(func() {
		_, _ = c.DeleteIpamPrefixListResolverTarget(ctx, &ec2.DeleteIpamPrefixListResolverTargetInput{
			IpamPrefixListResolverTargetId: aws.String(targetID),
		})
	})

	descTgts, err := c.DescribeIpamPrefixListResolverTargets(ctx, &ec2.DescribeIpamPrefixListResolverTargetsInput{
		IpamPrefixListResolverId: aws.String(resolverID),
	})
	require.NoError(t, err)
	require.Len(t, descTgts.IpamPrefixListResolverTargets, 1)
	assert.Equal(t, targetID, aws.ToString(descTgts.IpamPrefixListResolverTargets[0].IpamPrefixListResolverTargetId))

	modTgt, err := c.ModifyIpamPrefixListResolverTarget(ctx, &ec2.ModifyIpamPrefixListResolverTargetInput{
		IpamPrefixListResolverTargetId: aws.String(targetID),
		DesiredVersion:                 aws.Int64(2),
		TrackLatestVersion:             aws.Bool(false),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), aws.ToInt64(modTgt.IpamPrefixListResolverTarget.DesiredVersion))
	assert.False(t, aws.ToBool(modTgt.IpamPrefixListResolverTarget.TrackLatestVersion))
}

// TestEC2_IpamExternalResourceVerificationTokenSDK exercises the external
// resource verification token family: create a token, describe it, and delete
// it.
func TestEC2_IpamExternalResourceVerificationTokenSDK(t *testing.T) {
	c := ec2Client()
	ipamID, _ := ipamExtrasFixtureIpam(t, c)

	created, err := c.CreateIpamExternalResourceVerificationToken(ctx, &ec2.CreateIpamExternalResourceVerificationTokenInput{
		IpamId: aws.String(ipamID),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeIpamExternalResourceVerificationToken,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("token-a")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, created.IpamExternalResourceVerificationToken)
	tokenID := aws.ToString(created.IpamExternalResourceVerificationToken.IpamExternalResourceVerificationTokenId)
	require.NotEmpty(t, tokenID)
	assert.NotEmpty(t, aws.ToString(created.IpamExternalResourceVerificationToken.TokenValue))
	assert.Equal(t, types.TokenStateValid, created.IpamExternalResourceVerificationToken.Status)
	t.Cleanup(func() {
		_, _ = c.DeleteIpamExternalResourceVerificationToken(ctx, &ec2.DeleteIpamExternalResourceVerificationTokenInput{
			IpamExternalResourceVerificationTokenId: aws.String(tokenID),
		})
	})

	desc, err := c.DescribeIpamExternalResourceVerificationTokens(ctx, &ec2.DescribeIpamExternalResourceVerificationTokensInput{
		IpamExternalResourceVerificationTokenIds: []string{tokenID},
	})
	require.NoError(t, err)
	require.Len(t, desc.IpamExternalResourceVerificationTokens, 1)
	assert.Equal(t, ipamID, aws.ToString(desc.IpamExternalResourceVerificationTokens[0].IpamId))
}

// TestEC2_IpamDiscoveredResourcesSDK exercises the read-only discovery probes,
// which return honest-empty result sets since the sim has no cross-account
// discovery backend.
func TestEC2_IpamDiscoveredResourcesSDK(t *testing.T) {
	c := ec2Client()
	ipamID, _ := ipamExtrasFixtureIpam(t, c)
	desc, err := c.DescribeIpams(ctx, &ec2.DescribeIpamsInput{IpamIds: []string{ipamID}})
	require.NoError(t, err)
	require.Len(t, desc.Ipams, 1)
	discoID := aws.ToString(desc.Ipams[0].DefaultResourceDiscoveryId)
	require.NotEmpty(t, discoID)

	accts, err := c.GetIpamDiscoveredAccounts(ctx, &ec2.GetIpamDiscoveredAccountsInput{
		IpamResourceDiscoveryId: aws.String(discoID),
		DiscoveryRegion:         aws.String("us-east-1"),
	})
	require.NoError(t, err)
	assert.Empty(t, accts.IpamDiscoveredAccounts)

	addrs, err := c.GetIpamDiscoveredPublicAddresses(ctx, &ec2.GetIpamDiscoveredPublicAddressesInput{
		IpamResourceDiscoveryId: aws.String(discoID),
		AddressRegion:           aws.String("us-east-1"),
	})
	require.NoError(t, err)
	assert.Empty(t, addrs.IpamDiscoveredPublicAddresses)

	cidrs, err := c.GetIpamDiscoveredResourceCidrs(ctx, &ec2.GetIpamDiscoveredResourceCidrsInput{
		IpamResourceDiscoveryId: aws.String(discoID),
		ResourceRegion:          aws.String("us-east-1"),
	})
	require.NoError(t, err)
	assert.Empty(t, cidrs.IpamDiscoveredResourceCidrs)
}

// TestEC2_IpamModifyPoolAllocationSDK exercises ModifyIpamPoolAllocation: a
// CIDR is provisioned and allocated, then the allocation's description is
// modified and read back.
func TestEC2_IpamModifyPoolAllocationSDK(t *testing.T) {
	c := ec2Client()
	ipamID, _ := ipamExtrasFixtureIpam(t, c)
	poolID := ipamExtrasFixturePool(t, c, ipamID)

	_, err := c.ProvisionIpamPoolCidr(ctx, &ec2.ProvisionIpamPoolCidrInput{
		IpamPoolId: aws.String(poolID),
		Cidr:       aws.String("10.0.0.0/16"),
	})
	require.NoError(t, err)

	alloc, err := c.AllocateIpamPoolCidr(ctx, &ec2.AllocateIpamPoolCidrInput{
		IpamPoolId:    aws.String(poolID),
		NetmaskLength: aws.Int32(24),
		Description:   aws.String("alloc-a"),
	})
	require.NoError(t, err)
	require.NotNil(t, alloc.IpamPoolAllocation)
	allocID := aws.ToString(alloc.IpamPoolAllocation.IpamPoolAllocationId)
	require.NotEmpty(t, allocID)

	mod, err := c.ModifyIpamPoolAllocation(ctx, &ec2.ModifyIpamPoolAllocationInput{
		IpamPoolAllocationId: aws.String(allocID),
		Description:          aws.String("alloc-a-updated"),
	})
	require.NoError(t, err)
	require.NotNil(t, mod.IpamPoolAllocation)
	assert.Equal(t, allocID, aws.ToString(mod.IpamPoolAllocation.IpamPoolAllocationId))
	assert.Equal(t, "alloc-a-updated", aws.ToString(mod.IpamPoolAllocation.Description))
}
