package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_AddressTransferLifecycleSDK covers EnableAddressTransfer (offer an
// Elastic IP to another account), DescribeAddressTransfers, AcceptAddressTransfer,
// and DisableAddressTransfer.
func TestEC2_AddressTransferLifecycleSDK(t *testing.T) {
	c := ec2Client()

	eip, err := c.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: types.DomainTypeVpc})
	require.NoError(t, err)
	allocID := aws.ToString(eip.AllocationId)
	publicIP := aws.ToString(eip.PublicIp)
	defer func() { _, _ = c.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: eip.AllocationId}) }()

	en, err := c.EnableAddressTransfer(ctx, &ec2.EnableAddressTransferInput{
		AllocationId:      aws.String(allocID),
		TransferAccountId: aws.String("999988887777"),
	})
	require.NoError(t, err)
	require.NotNil(t, en.AddressTransfer)
	assert.Equal(t, allocID, aws.ToString(en.AddressTransfer.AllocationId))
	assert.Equal(t, "999988887777", aws.ToString(en.AddressTransfer.TransferAccountId))
	assert.Equal(t, types.AddressTransferStatusPending, en.AddressTransfer.AddressTransferStatus)

	desc, err := c.DescribeAddressTransfers(ctx, &ec2.DescribeAddressTransfersInput{
		AllocationIds: []string{allocID},
	})
	require.NoError(t, err)
	require.Len(t, desc.AddressTransfers, 1)
	assert.Equal(t, publicIP, aws.ToString(desc.AddressTransfers[0].PublicIp))

	acc, err := c.AcceptAddressTransfer(ctx, &ec2.AcceptAddressTransferInput{
		Address: aws.String(publicIP),
	})
	require.NoError(t, err)
	require.NotNil(t, acc.AddressTransfer)
	assert.Equal(t, types.AddressTransferStatusAccepted, acc.AddressTransfer.AddressTransferStatus)

	// Re-enable then disable to exercise DisableAddressTransfer.
	_, err = c.EnableAddressTransfer(ctx, &ec2.EnableAddressTransferInput{
		AllocationId:      aws.String(allocID),
		TransferAccountId: aws.String("999988887777"),
	})
	require.NoError(t, err)
	dis, err := c.DisableAddressTransfer(ctx, &ec2.DisableAddressTransferInput{AllocationId: aws.String(allocID)})
	require.NoError(t, err)
	require.NotNil(t, dis.AddressTransfer)
	assert.Equal(t, allocID, aws.ToString(dis.AddressTransfer.AllocationId))
}

// TestEC2_ByoipCidrLifecycleSDK covers ProvisionByoipCidr (own CIDR ->
// provisioned), AdvertiseByoipCidr (-> advertised), DescribeByoipCidrs,
// WithdrawByoipCidr (-> provisioned), and DeprovisionByoipCidr.
func TestEC2_ByoipCidrLifecycleSDK(t *testing.T) {
	c := ec2Client()
	const cidr = "203.0.113.0/24"

	prov, err := c.ProvisionByoipCidr(ctx, &ec2.ProvisionByoipCidrInput{
		Cidr:        aws.String(cidr),
		Description: aws.String("byoip-test"),
	})
	require.NoError(t, err)
	require.NotNil(t, prov.ByoipCidr)
	assert.Equal(t, cidr, aws.ToString(prov.ByoipCidr.Cidr))
	assert.Equal(t, types.ByoipCidrStateProvisioned, prov.ByoipCidr.State)

	adv, err := c.AdvertiseByoipCidr(ctx, &ec2.AdvertiseByoipCidrInput{Cidr: aws.String(cidr)})
	require.NoError(t, err)
	assert.Equal(t, types.ByoipCidrStateAdvertised, adv.ByoipCidr.State)

	desc, err := c.DescribeByoipCidrs(ctx, &ec2.DescribeByoipCidrsInput{MaxResults: aws.Int32(10)})
	require.NoError(t, err)
	var found *types.ByoipCidr
	for i := range desc.ByoipCidrs {
		if aws.ToString(desc.ByoipCidrs[i].Cidr) == cidr {
			found = &desc.ByoipCidrs[i]
		}
	}
	require.NotNil(t, found, "provisioned CIDR must round-trip in DescribeByoipCidrs")
	assert.Equal(t, "byoip-test", aws.ToString(found.Description))

	wd, err := c.WithdrawByoipCidr(ctx, &ec2.WithdrawByoipCidrInput{Cidr: aws.String(cidr)})
	require.NoError(t, err)
	assert.Equal(t, types.ByoipCidrStateProvisioned, wd.ByoipCidr.State)

	_, err = c.DeprovisionByoipCidr(ctx, &ec2.DeprovisionByoipCidrInput{Cidr: aws.String(cidr)})
	require.NoError(t, err)
}

// TestEC2_PublicIpv4PoolLifecycleSDK covers CreatePublicIpv4Pool,
// ProvisionPublicIpv4PoolCidr, DescribePublicIpv4Pools (range read-back),
// DeprovisionPublicIpv4PoolCidr, and DeletePublicIpv4Pool.
func TestEC2_PublicIpv4PoolLifecycleSDK(t *testing.T) {
	c := ec2Client()

	created, err := c.CreatePublicIpv4Pool(ctx, &ec2.CreatePublicIpv4PoolInput{
		NetworkBorderGroup: aws.String("us-east-1"),
	})
	require.NoError(t, err)
	poolID := aws.ToString(created.PoolId)
	require.NotEmpty(t, poolID)
	defer func() { _, _ = c.DeletePublicIpv4Pool(ctx, &ec2.DeletePublicIpv4PoolInput{PoolId: aws.String(poolID)}) }()

	prov, err := c.ProvisionPublicIpv4PoolCidr(ctx, &ec2.ProvisionPublicIpv4PoolCidrInput{
		PoolId:        aws.String(poolID),
		IpamPoolId:    aws.String("ipam-pool-00000000000000000"),
		NetmaskLength: aws.Int32(28),
	})
	require.NoError(t, err)
	assert.Equal(t, poolID, aws.ToString(prov.PoolId))
	require.NotNil(t, prov.PoolAddressRange)
	assert.Equal(t, int32(16), aws.ToInt32(prov.PoolAddressRange.AddressCount))

	desc, err := c.DescribePublicIpv4Pools(ctx, &ec2.DescribePublicIpv4PoolsInput{
		PoolIds: []string{poolID},
	})
	require.NoError(t, err)
	require.Len(t, desc.PublicIpv4Pools, 1)
	require.Len(t, desc.PublicIpv4Pools[0].PoolAddressRanges, 1)
	assert.Equal(t, int32(16), aws.ToInt32(desc.PublicIpv4Pools[0].TotalAddressCount))

	deprov, err := c.DeprovisionPublicIpv4PoolCidr(ctx, &ec2.DeprovisionPublicIpv4PoolCidrInput{
		PoolId: aws.String(poolID),
		Cidr:   aws.String("10.0.0.0/28"),
	})
	require.NoError(t, err)
	assert.Equal(t, poolID, aws.ToString(deprov.PoolId))
	assert.Contains(t, deprov.DeprovisionedAddresses, "10.0.0.0/28")
}

// TestEC2_NatGatewayAddressLifecycleSDK covers AssignPrivateNatGatewayAddress,
// AssociateNatGatewayAddress, DisassociateNatGatewayAddress, and
// UnassignPrivateNatGatewayAddress against a real NAT gateway.
func TestEC2_NatGatewayAddressLifecycleSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.220.0.0/16")})
	require.NoError(t, err)
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}) }()
	sub, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.220.1.0/24")})
	require.NoError(t, err)
	eip, err := c.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: types.DomainTypeVpc})
	require.NoError(t, err)
	defer func() { _, _ = c.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: eip.AllocationId}) }()

	nat, err := c.CreateNatGateway(ctx, &ec2.CreateNatGatewayInput{
		SubnetId:     sub.Subnet.SubnetId,
		AllocationId: eip.AllocationId,
	})
	require.NoError(t, err)
	natID := nat.NatGateway.NatGatewayId

	assign, err := c.AssignPrivateNatGatewayAddress(ctx, &ec2.AssignPrivateNatGatewayAddressInput{
		NatGatewayId:          natID,
		PrivateIpAddressCount: aws.Int32(1),
	})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(natID), aws.ToString(assign.NatGatewayId))
	require.GreaterOrEqual(t, len(assign.NatGatewayAddresses), 2, "primary + one assigned secondary")

	// The assigned secondary's private IP, to unassign later.
	var secondaryIP string
	for _, a := range assign.NatGatewayAddresses {
		if !aws.ToBool(a.IsPrimary) {
			secondaryIP = aws.ToString(a.PrivateIp)
		}
	}
	require.NotEmpty(t, secondaryIP)

	eip2, err := c.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: types.DomainTypeVpc})
	require.NoError(t, err)
	defer func() { _, _ = c.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: eip2.AllocationId}) }()
	assoc, err := c.AssociateNatGatewayAddress(ctx, &ec2.AssociateNatGatewayAddressInput{
		NatGatewayId:  natID,
		AllocationIds: []string{aws.ToString(eip2.AllocationId)},
	})
	require.NoError(t, err)
	var newAssocID string
	for _, a := range assoc.NatGatewayAddresses {
		if aws.ToString(a.AllocationId) == aws.ToString(eip2.AllocationId) {
			newAssocID = aws.ToString(a.AssociationId)
		}
	}
	require.NotEmpty(t, newAssocID, "newly associated EIP must surface an associationId")

	_, err = c.DisassociateNatGatewayAddress(ctx, &ec2.DisassociateNatGatewayAddressInput{
		NatGatewayId:   natID,
		AssociationIds: []string{newAssocID},
	})
	require.NoError(t, err)

	_, err = c.UnassignPrivateNatGatewayAddress(ctx, &ec2.UnassignPrivateNatGatewayAddressInput{
		NatGatewayId:       natID,
		PrivateIpAddresses: []string{secondaryIP},
	})
	require.NoError(t, err)
}

// TestEC2_TrunkInterfaceLifecycleSDK covers AssociateTrunkInterface (trunk +
// branch ENI), DescribeTrunkInterfaceAssociations, and
// DisassociateTrunkInterface.
func TestEC2_TrunkInterfaceLifecycleSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.221.0.0/16")})
	require.NoError(t, err)
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}) }()
	sub, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.221.1.0/24")})
	require.NoError(t, err)

	trunk, err := c.CreateNetworkInterface(ctx, &ec2.CreateNetworkInterfaceInput{SubnetId: sub.Subnet.SubnetId})
	require.NoError(t, err)
	branch, err := c.CreateNetworkInterface(ctx, &ec2.CreateNetworkInterfaceInput{SubnetId: sub.Subnet.SubnetId})
	require.NoError(t, err)

	assoc, err := c.AssociateTrunkInterface(ctx, &ec2.AssociateTrunkInterfaceInput{
		TrunkInterfaceId:  trunk.NetworkInterface.NetworkInterfaceId,
		BranchInterfaceId: branch.NetworkInterface.NetworkInterfaceId,
		VlanId:            aws.Int32(101),
	})
	require.NoError(t, err)
	require.NotNil(t, assoc.InterfaceAssociation)
	assocID := assoc.InterfaceAssociation.AssociationId
	require.NotEmpty(t, aws.ToString(assocID))
	assert.Equal(t, int32(101), aws.ToInt32(assoc.InterfaceAssociation.VlanId))

	desc, err := c.DescribeTrunkInterfaceAssociations(ctx, &ec2.DescribeTrunkInterfaceAssociationsInput{
		AssociationIds: []string{aws.ToString(assocID)},
	})
	require.NoError(t, err)
	require.Len(t, desc.InterfaceAssociations, 1)
	assert.Equal(t, aws.ToString(trunk.NetworkInterface.NetworkInterfaceId), aws.ToString(desc.InterfaceAssociations[0].TrunkInterfaceId))

	dis, err := c.DisassociateTrunkInterface(ctx, &ec2.DisassociateTrunkInterfaceInput{AssociationId: assocID})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(dis.Return))
}

// TestEC2_CarrierGatewayLifecycleSDK covers CreateCarrierGateway,
// DescribeCarrierGateways, and DeleteCarrierGateway.
func TestEC2_CarrierGatewayLifecycleSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.222.0.0/16")})
	require.NoError(t, err)
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}) }()

	created, err := c.CreateCarrierGateway(ctx, &ec2.CreateCarrierGatewayInput{
		VpcId: vpc.Vpc.VpcId,
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeCarrierGateway,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("cagw-a")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, created.CarrierGateway)
	cagwID := created.CarrierGateway.CarrierGatewayId
	require.NotEmpty(t, aws.ToString(cagwID))
	assert.Equal(t, aws.ToString(vpc.Vpc.VpcId), aws.ToString(created.CarrierGateway.VpcId))
	assert.Equal(t, types.CarrierGatewayStateAvailable, created.CarrierGateway.State)

	desc, err := c.DescribeCarrierGateways(ctx, &ec2.DescribeCarrierGatewaysInput{
		CarrierGatewayIds: []string{aws.ToString(cagwID)},
	})
	require.NoError(t, err)
	require.Len(t, desc.CarrierGateways, 1)

	del, err := c.DeleteCarrierGateway(ctx, &ec2.DeleteCarrierGatewayInput{CarrierGatewayId: cagwID})
	require.NoError(t, err)
	assert.Equal(t, types.CarrierGatewayStateDeleted, del.CarrierGateway.State)
}

// TestEC2_CoipPoolLifecycleSDK covers CreateCoipPool, DescribeCoipPools,
// GetCoipPoolUsage, and DeleteCoipPool.
func TestEC2_CoipPoolLifecycleSDK(t *testing.T) {
	c := ec2Client()

	created, err := c.CreateCoipPool(ctx, &ec2.CreateCoipPoolInput{
		LocalGatewayRouteTableId: aws.String("lgw-rtb-00000000000000000"),
	})
	require.NoError(t, err)
	require.NotNil(t, created.CoipPool)
	poolID := created.CoipPool.PoolId
	require.NotEmpty(t, aws.ToString(poolID))
	assert.Equal(t, "lgw-rtb-00000000000000000", aws.ToString(created.CoipPool.LocalGatewayRouteTableId))

	desc, err := c.DescribeCoipPools(ctx, &ec2.DescribeCoipPoolsInput{PoolIds: []string{aws.ToString(poolID)}})
	require.NoError(t, err)
	require.Len(t, desc.CoipPools, 1)

	usage, err := c.GetCoipPoolUsage(ctx, &ec2.GetCoipPoolUsageInput{PoolId: poolID})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(poolID), aws.ToString(usage.CoipPoolId))

	_, err = c.DeleteCoipPool(ctx, &ec2.DeleteCoipPoolInput{CoipPoolId: poolID})
	require.NoError(t, err)
}

// TestEC2_NetworkInterfacePermissionLifecycleSDK covers
// CreateNetworkInterfacePermission, DescribeNetworkInterfacePermissions, and
// DeleteNetworkInterfacePermission.
func TestEC2_NetworkInterfacePermissionLifecycleSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.223.0.0/16")})
	require.NoError(t, err)
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}) }()
	sub, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.223.1.0/24")})
	require.NoError(t, err)
	eni, err := c.CreateNetworkInterface(ctx, &ec2.CreateNetworkInterfaceInput{SubnetId: sub.Subnet.SubnetId})
	require.NoError(t, err)

	created, err := c.CreateNetworkInterfacePermission(ctx, &ec2.CreateNetworkInterfacePermissionInput{
		NetworkInterfaceId: eni.NetworkInterface.NetworkInterfaceId,
		AwsAccountId:       aws.String("999988887777"),
		Permission:         types.InterfacePermissionTypeInstanceAttach,
	})
	require.NoError(t, err)
	require.NotNil(t, created.InterfacePermission)
	permID := created.InterfacePermission.NetworkInterfacePermissionId
	require.NotEmpty(t, aws.ToString(permID))
	assert.Equal(t, types.InterfacePermissionTypeInstanceAttach, created.InterfacePermission.Permission)

	desc, err := c.DescribeNetworkInterfacePermissions(ctx, &ec2.DescribeNetworkInterfacePermissionsInput{
		NetworkInterfacePermissionIds: []string{aws.ToString(permID)},
	})
	require.NoError(t, err)
	require.Len(t, desc.NetworkInterfacePermissions, 1)
	assert.Equal(t, aws.ToString(eni.NetworkInterface.NetworkInterfaceId), aws.ToString(desc.NetworkInterfacePermissions[0].NetworkInterfaceId))

	del, err := c.DeleteNetworkInterfacePermission(ctx, &ec2.DeleteNetworkInterfacePermissionInput{
		NetworkInterfacePermissionId: permID,
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(del.Return))
}

// TestEC2_VgwRoutePropagationSDK covers EnableVgwRoutePropagation and
// DisableVgwRoutePropagation against a route table + virtual private gateway.
func TestEC2_VgwRoutePropagationSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.224.0.0/16")})
	require.NoError(t, err)
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}) }()
	rt, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: vpc.Vpc.VpcId})
	require.NoError(t, err)
	vgw, err := c.CreateVpnGateway(ctx, &ec2.CreateVpnGatewayInput{Type: types.GatewayTypeIpsec1})
	require.NoError(t, err)

	_, err = c.EnableVgwRoutePropagation(ctx, &ec2.EnableVgwRoutePropagationInput{
		GatewayId:    vgw.VpnGateway.VpnGatewayId,
		RouteTableId: rt.RouteTable.RouteTableId,
	})
	require.NoError(t, err)

	_, err = c.DisableVgwRoutePropagation(ctx, &ec2.DisableVgwRoutePropagationInput{
		GatewayId:    vgw.VpnGateway.VpnGatewayId,
		RouteTableId: rt.RouteTable.RouteTableId,
	})
	require.NoError(t, err)
}

// TestEC2_VpnConcentratorLifecycleSDK covers CreateVpnConcentrator,
// DescribeVpnConcentrators, and DeleteVpnConcentrator.
func TestEC2_VpnConcentratorLifecycleSDK(t *testing.T) {
	c := ec2Client()

	created, err := c.CreateVpnConcentrator(ctx, &ec2.CreateVpnConcentratorInput{
		Type: types.VpnConcentratorTypeIpsec1,
	})
	require.NoError(t, err)
	require.NotNil(t, created.VpnConcentrator)
	id := created.VpnConcentrator.VpnConcentratorId
	require.NotEmpty(t, aws.ToString(id))

	desc, err := c.DescribeVpnConcentrators(ctx, &ec2.DescribeVpnConcentratorsInput{
		VpnConcentratorIds: []string{aws.ToString(id)},
	})
	require.NoError(t, err)
	require.Len(t, desc.VpnConcentrators, 1)
	assert.Equal(t, aws.ToString(id), aws.ToString(desc.VpnConcentrators[0].VpnConcentratorId))

	_, err = c.DeleteVpnConcentrator(ctx, &ec2.DeleteVpnConcentratorInput{VpnConcentratorId: id})
	require.NoError(t, err)
}

// TestEC2_ModifyManagedPrefixListSDK covers ModifyManagedPrefixList: adding and
// removing entries and bumping the version on an existing managed prefix list.
func TestEC2_ModifyManagedPrefixListSDK(t *testing.T) {
	c := ec2Client()

	created, err := c.CreateManagedPrefixList(ctx, &ec2.CreateManagedPrefixListInput{
		PrefixListName: aws.String("modify-cidrs"),
		AddressFamily:  aws.String("IPv4"),
		MaxEntries:     aws.Int32(10),
		Entries: []types.AddPrefixListEntry{
			{Cidr: aws.String("10.0.0.0/8")},
		},
	})
	require.NoError(t, err)
	plID := created.PrefixList.PrefixListId
	startVersion := aws.ToInt64(created.PrefixList.Version)

	mod, err := c.ModifyManagedPrefixList(ctx, &ec2.ModifyManagedPrefixListInput{
		PrefixListId:   plID,
		CurrentVersion: created.PrefixList.Version,
		AddEntries: []types.AddPrefixListEntry{
			{Cidr: aws.String("172.16.0.0/12"), Description: aws.String("new")},
		},
		RemoveEntries: []types.RemovePrefixListEntry{
			{Cidr: aws.String("10.0.0.0/8")},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, mod.PrefixList)
	assert.Greater(t, aws.ToInt64(mod.PrefixList.Version), startVersion, "version must advance")

	entries, err := c.GetManagedPrefixListEntries(ctx, &ec2.GetManagedPrefixListEntriesInput{PrefixListId: plID})
	require.NoError(t, err)
	cidrs := map[string]bool{}
	for _, e := range entries.Entries {
		cidrs[aws.ToString(e.Cidr)] = true
	}
	assert.True(t, cidrs["172.16.0.0/12"], "added entry present")
	assert.False(t, cidrs["10.0.0.0/8"], "removed entry absent")

	_, err = c.DeleteManagedPrefixList(ctx, &ec2.DeleteManagedPrefixListInput{PrefixListId: plID})
	require.NoError(t, err)
}
