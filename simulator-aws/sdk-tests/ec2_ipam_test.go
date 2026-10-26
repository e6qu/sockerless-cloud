package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_IPAMLifecycleSDK expects an IPAM to come with public and private
// default scopes and a default resource discovery, and covers scopes, the
// resource-discovery associations and the IPAM Organizations delegated admin.
func TestEC2_IPAMLifecycleSDK(t *testing.T) {
	t.Parallel()
	c := ec2Client()

	created, err := c.CreateIpam(ctx, &ec2.CreateIpamInput{
		Description: aws.String("ipam-a"),
		OperatingRegions: []types.AddIpamOperatingRegion{
			{RegionName: aws.String("us-east-1")},
		},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeIpam,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("ipam-a")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, created.Ipam)
	ipamID := aws.ToString(created.Ipam.IpamId)
	require.NotEmpty(t, ipamID)
	assert.NotEmpty(t, aws.ToString(created.Ipam.PublicDefaultScopeId))
	assert.NotEmpty(t, aws.ToString(created.Ipam.PrivateDefaultScopeId))
	assert.NotEmpty(t, aws.ToString(created.Ipam.DefaultResourceDiscoveryId))
	assert.NotEmpty(t, aws.ToString(created.Ipam.DefaultResourceDiscoveryAssociationId))
	assert.Equal(t, int32(2), aws.ToInt32(created.Ipam.ScopeCount))
	assert.Equal(t, "create-complete", string(created.Ipam.State))
	defer func() { _, _ = c.DeleteIpam(ctx, &ec2.DeleteIpamInput{IpamId: aws.String(ipamID)}) }()

	desc, err := c.DescribeIpams(ctx, &ec2.DescribeIpamsInput{IpamIds: []string{ipamID}})
	require.NoError(t, err)
	require.Len(t, desc.Ipams, 1)
	assert.Equal(t, "ipam-a", aws.ToString(desc.Ipams[0].Description))
	require.Len(t, desc.Ipams[0].OperatingRegions, 1)
	assert.Equal(t, "us-east-1", aws.ToString(desc.Ipams[0].OperatingRegions[0].RegionName))
	var nameTag string
	for _, tg := range desc.Ipams[0].Tags {
		if aws.ToString(tg.Key) == "Name" {
			nameTag = aws.ToString(tg.Value)
		}
	}
	assert.Equal(t, "ipam-a", nameTag)

	mod, err := c.ModifyIpam(ctx, &ec2.ModifyIpamInput{
		IpamId:      aws.String(ipamID),
		Description: aws.String("ipam-a-updated"),
	})
	require.NoError(t, err)
	assert.Equal(t, "ipam-a-updated", aws.ToString(mod.Ipam.Description))

	scope, err := c.CreateIpamScope(ctx, &ec2.CreateIpamScopeInput{
		IpamId:      aws.String(ipamID),
		Description: aws.String("scope-a"),
	})
	require.NoError(t, err)
	scopeID := aws.ToString(scope.IpamScope.IpamScopeId)
	require.NotEmpty(t, scopeID)
	assert.False(t, aws.ToBool(scope.IpamScope.IsDefault))
	defer func() { _, _ = c.DeleteIpamScope(ctx, &ec2.DeleteIpamScopeInput{IpamScopeId: aws.String(scopeID)}) }()

	_, err = c.ModifyIpamScope(ctx, &ec2.ModifyIpamScopeInput{
		IpamScopeId: aws.String(scopeID),
		Description: aws.String("scope-a-updated"),
	})
	require.NoError(t, err)

	dscope, err := c.DescribeIpamScopes(ctx, &ec2.DescribeIpamScopesInput{IpamScopeIds: []string{scopeID}})
	require.NoError(t, err)
	require.Len(t, dscope.IpamScopes, 1)
	assert.Equal(t, "scope-a-updated", aws.ToString(dscope.IpamScopes[0].Description))

	rd, err := c.CreateIpamResourceDiscovery(ctx, &ec2.CreateIpamResourceDiscoveryInput{
		Description: aws.String("rd-a"),
		OperatingRegions: []types.AddIpamOperatingRegion{
			{RegionName: aws.String("us-east-1")},
		},
	})
	require.NoError(t, err)
	rdID := aws.ToString(rd.IpamResourceDiscovery.IpamResourceDiscoveryId)
	require.NotEmpty(t, rdID)
	defer func() {
		_, _ = c.DeleteIpamResourceDiscovery(ctx, &ec2.DeleteIpamResourceDiscoveryInput{IpamResourceDiscoveryId: aws.String(rdID)})
	}()

	_, err = c.ModifyIpamResourceDiscovery(ctx, &ec2.ModifyIpamResourceDiscoveryInput{
		IpamResourceDiscoveryId: aws.String(rdID),
		Description:             aws.String("rd-a-updated"),
	})
	require.NoError(t, err)

	drd, err := c.DescribeIpamResourceDiscoveries(ctx, &ec2.DescribeIpamResourceDiscoveriesInput{
		IpamResourceDiscoveryIds: []string{rdID},
	})
	require.NoError(t, err)
	require.Len(t, drd.IpamResourceDiscoveries, 1)
	assert.Equal(t, "rd-a-updated", aws.ToString(drd.IpamResourceDiscoveries[0].Description))

	assoc, err := c.AssociateIpamResourceDiscovery(ctx, &ec2.AssociateIpamResourceDiscoveryInput{
		IpamId:                  aws.String(ipamID),
		IpamResourceDiscoveryId: aws.String(rdID),
	})
	require.NoError(t, err)
	assocID := aws.ToString(assoc.IpamResourceDiscoveryAssociation.IpamResourceDiscoveryAssociationId)
	require.NotEmpty(t, assocID)

	dassoc, err := c.DescribeIpamResourceDiscoveryAssociations(ctx, &ec2.DescribeIpamResourceDiscoveryAssociationsInput{
		IpamResourceDiscoveryAssociationIds: []string{assocID},
	})
	require.NoError(t, err)
	require.Len(t, dassoc.IpamResourceDiscoveryAssociations, 1)
	assert.Equal(t, ipamID, aws.ToString(dassoc.IpamResourceDiscoveryAssociations[0].IpamId))

	_, err = c.DisassociateIpamResourceDiscovery(ctx, &ec2.DisassociateIpamResourceDiscoveryInput{
		IpamResourceDiscoveryAssociationId: aws.String(assocID),
	})
	require.NoError(t, err)

	_, err = c.EnableIpamOrganizationAdminAccount(ctx, &ec2.EnableIpamOrganizationAdminAccountInput{
		DelegatedAdminAccountId: aws.String("210987654321"),
	})
	require.NoError(t, err)
	_, err = c.DisableIpamOrganizationAdminAccount(ctx, &ec2.DisableIpamOrganizationAdminAccountInput{
		DelegatedAdminAccountId: aws.String("210987654321"),
	})
	require.NoError(t, err)
}

func TestEC2_IPAMPoolAllocationSDK(t *testing.T) {
	t.Parallel()
	c := ec2Client()

	ipam, err := c.CreateIpam(ctx, &ec2.CreateIpamInput{
		OperatingRegions: []types.AddIpamOperatingRegion{{RegionName: aws.String("us-east-1")}},
	})
	require.NoError(t, err)
	ipamID := aws.ToString(ipam.Ipam.IpamId)
	scopeID := aws.ToString(ipam.Ipam.PrivateDefaultScopeId)
	defer func() { _, _ = c.DeleteIpam(ctx, &ec2.DeleteIpamInput{IpamId: aws.String(ipamID)}) }()

	pool, err := c.CreateIpamPool(ctx, &ec2.CreateIpamPoolInput{
		IpamScopeId:                aws.String(scopeID),
		AddressFamily:              types.AddressFamilyIpv4,
		Description:                aws.String("pool-a"),
		AllocationMinNetmaskLength: aws.Int32(16),
		AllocationMaxNetmaskLength: aws.Int32(28),
		AllocationResourceTags: []types.RequestIpamResourceTag{
			{Key: aws.String("env"), Value: aws.String("test")},
		},
	})
	require.NoError(t, err)
	poolID := aws.ToString(pool.IpamPool.IpamPoolId)
	require.NotEmpty(t, poolID)
	assert.Equal(t, types.AddressFamilyIpv4, pool.IpamPool.AddressFamily)
	defer func() { _, _ = c.DeleteIpamPool(ctx, &ec2.DeleteIpamPoolInput{IpamPoolId: aws.String(poolID)}) }()

	dpool, err := c.DescribeIpamPools(ctx, &ec2.DescribeIpamPoolsInput{IpamPoolIds: []string{poolID}})
	require.NoError(t, err)
	require.Len(t, dpool.IpamPools, 1)
	assert.Equal(t, "pool-a", aws.ToString(dpool.IpamPools[0].Description))

	prov, err := c.ProvisionIpamPoolCidr(ctx, &ec2.ProvisionIpamPoolCidrInput{
		IpamPoolId: aws.String(poolID),
		Cidr:       aws.String("10.0.0.0/16"),
	})
	require.NoError(t, err)
	assert.Equal(t, "provisioned", string(prov.IpamPoolCidr.State))

	gcidrs, err := c.GetIpamPoolCidrs(ctx, &ec2.GetIpamPoolCidrsInput{IpamPoolId: aws.String(poolID)})
	require.NoError(t, err)
	require.Len(t, gcidrs.IpamPoolCidrs, 1)
	assert.Equal(t, "10.0.0.0/16", aws.ToString(gcidrs.IpamPoolCidrs[0].Cidr))

	alloc, err := c.AllocateIpamPoolCidr(ctx, &ec2.AllocateIpamPoolCidrInput{
		IpamPoolId:    aws.String(poolID),
		NetmaskLength: aws.Int32(24),
		Description:   aws.String("alloc-a"),
	})
	require.NoError(t, err)
	require.NotNil(t, alloc.IpamPoolAllocation)
	allocID := aws.ToString(alloc.IpamPoolAllocation.IpamPoolAllocationId)
	require.NotEmpty(t, allocID)
	assert.Equal(t, "10.0.0.0/24", aws.ToString(alloc.IpamPoolAllocation.Cidr))

	// A second carve does not overlap the first.
	alloc2, err := c.AllocateIpamPoolCidr(ctx, &ec2.AllocateIpamPoolCidrInput{
		IpamPoolId:    aws.String(poolID),
		NetmaskLength: aws.Int32(24),
	})
	require.NoError(t, err)
	assert.Equal(t, "10.0.1.0/24", aws.ToString(alloc2.IpamPoolAllocation.Cidr))

	galloc, err := c.GetIpamPoolAllocations(ctx, &ec2.GetIpamPoolAllocationsInput{IpamPoolId: aws.String(poolID)})
	require.NoError(t, err)
	require.Len(t, galloc.IpamPoolAllocations, 2)

	dalloc, err := c.DescribeIpamPoolAllocations(ctx, &ec2.DescribeIpamPoolAllocationsInput{
		IpamPoolAllocationIds: []string{allocID},
	})
	require.NoError(t, err)
	require.Len(t, dalloc.IpamPoolAllocations, 1)

	_, err = c.ModifyIpamPool(ctx, &ec2.ModifyIpamPoolInput{
		IpamPoolId:  aws.String(poolID),
		Description: aws.String("pool-a-updated"),
	})
	require.NoError(t, err)

	rel, err := c.ReleaseIpamPoolAllocation(ctx, &ec2.ReleaseIpamPoolAllocationInput{
		IpamPoolId:           aws.String(poolID),
		IpamPoolAllocationId: aws.String(allocID),
		Cidr:                 alloc.IpamPoolAllocation.Cidr,
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(rel.Success))

	galloc2, err := c.GetIpamPoolAllocations(ctx, &ec2.GetIpamPoolAllocationsInput{IpamPoolId: aws.String(poolID)})
	require.NoError(t, err)
	require.Len(t, galloc2.IpamPoolAllocations, 1)

	// An empty allocation carries no resource.
	_, err = c.GetIpamResourceCidrs(ctx, &ec2.GetIpamResourceCidrsInput{IpamScopeId: aws.String(scopeID)})
	require.NoError(t, err)

	_, err = c.GetIpamAddressHistory(ctx, &ec2.GetIpamAddressHistoryInput{
		IpamScopeId: aws.String(scopeID),
		Cidr:        aws.String("10.0.1.0/24"),
	})
	require.NoError(t, err)

	mrc, err := c.ModifyIpamResourceCidr(ctx, &ec2.ModifyIpamResourceCidrInput{
		ResourceId:         aws.String("vpc-12345678"),
		ResourceCidr:       aws.String("10.0.1.0/24"),
		ResourceRegion:     aws.String("us-east-1"),
		CurrentIpamScopeId: aws.String(scopeID),
		Monitored:          aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, "vpc-12345678", aws.ToString(mrc.IpamResourceCidr.ResourceId))

	// Deprovisioning needs the allocations released first.
	_, err = c.ReleaseIpamPoolAllocation(ctx, &ec2.ReleaseIpamPoolAllocationInput{
		IpamPoolId:           aws.String(poolID),
		IpamPoolAllocationId: alloc2.IpamPoolAllocation.IpamPoolAllocationId,
		Cidr:                 alloc2.IpamPoolAllocation.Cidr,
	})
	require.NoError(t, err)
	deprov, err := c.DeprovisionIpamPoolCidr(ctx, &ec2.DeprovisionIpamPoolCidrInput{
		IpamPoolId: aws.String(poolID),
		Cidr:       aws.String("10.0.0.0/16"),
	})
	require.NoError(t, err)
	assert.Equal(t, "deprovisioned", string(deprov.IpamPoolCidr.State))
}
