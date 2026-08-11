package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tgwMcastFixture builds a transit gateway, a multicast domain on it, a VPC +
// subnet, and a VPC attachment — the shared scaffolding the multicast and
// policy-table tests associate against. Returns (tgwID, domainID, attID,
// subnetID) and registers tolerant cleanups.
func tgwMcastFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	client := ec2Client()

	tgwOut, err := client.CreateTransitGateway(ctx, &ec2.CreateTransitGatewayInput{
		Options: &types.TransitGatewayRequestOptions{MulticastSupport: types.MulticastSupportValueEnable},
	})
	require.NoError(t, err)
	tgwID := aws.ToString(tgwOut.TransitGateway.TransitGatewayId)
	t.Cleanup(func() {
		_, _ = client.DeleteTransitGateway(ctx, &ec2.DeleteTransitGatewayInput{TransitGatewayId: aws.String(tgwID)})
	})

	domOut, err := client.CreateTransitGatewayMulticastDomain(ctx, &ec2.CreateTransitGatewayMulticastDomainInput{
		TransitGatewayId: aws.String(tgwID),
		Options: &types.CreateTransitGatewayMulticastDomainRequestOptions{
			Igmpv2Support:        types.Igmpv2SupportValueEnable,
			StaticSourcesSupport: types.StaticSourcesSupportValueEnable,
		},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeTransitGatewayMulticastDomain,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("mcast")}},
		}},
	})
	require.NoError(t, err)
	domainID := aws.ToString(domOut.TransitGatewayMulticastDomain.TransitGatewayMulticastDomainId)
	t.Cleanup(func() {
		_, _ = client.DeleteTransitGatewayMulticastDomain(ctx, &ec2.DeleteTransitGatewayMulticastDomainInput{
			TransitGatewayMulticastDomainId: aws.String(domainID),
		})
	})

	vpc, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.70.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	subnet, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: aws.String(vpcID), CidrBlock: aws.String("10.70.1.0/24"),
	})
	require.NoError(t, err)
	subnetID := aws.ToString(subnet.Subnet.SubnetId)

	attOut, err := client.CreateTransitGatewayVpcAttachment(ctx, &ec2.CreateTransitGatewayVpcAttachmentInput{
		TransitGatewayId: aws.String(tgwID), VpcId: aws.String(vpcID), SubnetIds: []string{subnetID},
	})
	require.NoError(t, err)
	attID := aws.ToString(attOut.TransitGatewayVpcAttachment.TransitGatewayAttachmentId)
	t.Cleanup(func() {
		_, _ = client.DeleteTransitGatewayVpcAttachment(ctx, &ec2.DeleteTransitGatewayVpcAttachmentInput{
			TransitGatewayAttachmentId: aws.String(attID),
		})
	})
	return tgwID, domainID, attID, subnetID
}

// TestEC2_TGWMulticastDomainAssociations exercises associate/disassociate/accept/
// reject of a multicast domain plus GetTransitGatewayMulticastDomainAssociations.
func TestEC2_TGWMulticastDomainAssociations(t *testing.T) {
	client := ec2Client()
	_, domainID, attID, subnetID := tgwMcastFixture(t)

	assoc, err := client.AssociateTransitGatewayMulticastDomain(ctx, &ec2.AssociateTransitGatewayMulticastDomainInput{
		TransitGatewayMulticastDomainId: aws.String(domainID),
		TransitGatewayAttachmentId:      aws.String(attID),
		SubnetIds:                       []string{subnetID},
	})
	require.NoError(t, err)
	require.NotNil(t, assoc.Associations)
	assert.Equal(t, domainID, aws.ToString(assoc.Associations.TransitGatewayMulticastDomainId))
	assert.Equal(t, attID, aws.ToString(assoc.Associations.TransitGatewayAttachmentId))
	require.Len(t, assoc.Associations.Subnets, 1)
	assert.Equal(t, subnetID, aws.ToString(assoc.Associations.Subnets[0].SubnetId))

	accepted, err := client.AcceptTransitGatewayMulticastDomainAssociations(ctx, &ec2.AcceptTransitGatewayMulticastDomainAssociationsInput{
		TransitGatewayMulticastDomainId: aws.String(domainID),
		TransitGatewayAttachmentId:      aws.String(attID),
		SubnetIds:                       []string{subnetID},
	})
	require.NoError(t, err)
	assert.Equal(t, attID, aws.ToString(accepted.Associations.TransitGatewayAttachmentId))

	got, err := client.GetTransitGatewayMulticastDomainAssociations(ctx, &ec2.GetTransitGatewayMulticastDomainAssociationsInput{
		TransitGatewayMulticastDomainId: aws.String(domainID),
	})
	require.NoError(t, err)
	require.Len(t, got.MulticastDomainAssociations, 1)
	assert.Equal(t, attID, aws.ToString(got.MulticastDomainAssociations[0].TransitGatewayAttachmentId))
	require.NotNil(t, got.MulticastDomainAssociations[0].Subnet)
	assert.Equal(t, subnetID, aws.ToString(got.MulticastDomainAssociations[0].Subnet.SubnetId))

	disassoc, err := client.DisassociateTransitGatewayMulticastDomain(ctx, &ec2.DisassociateTransitGatewayMulticastDomainInput{
		TransitGatewayMulticastDomainId: aws.String(domainID),
		TransitGatewayAttachmentId:      aws.String(attID),
		SubnetIds:                       []string{subnetID},
	})
	require.NoError(t, err)
	assert.Equal(t, attID, aws.ToString(disassoc.Associations.TransitGatewayAttachmentId))

	// Re-associate so reject has something to act on.
	_, err = client.AssociateTransitGatewayMulticastDomain(ctx, &ec2.AssociateTransitGatewayMulticastDomainInput{
		TransitGatewayMulticastDomainId: aws.String(domainID),
		TransitGatewayAttachmentId:      aws.String(attID),
		SubnetIds:                       []string{subnetID},
	})
	require.NoError(t, err)
	rejected, err := client.RejectTransitGatewayMulticastDomainAssociations(ctx, &ec2.RejectTransitGatewayMulticastDomainAssociationsInput{
		TransitGatewayMulticastDomainId: aws.String(domainID),
		TransitGatewayAttachmentId:      aws.String(attID),
		SubnetIds:                       []string{subnetID},
	})
	require.NoError(t, err)
	assert.Equal(t, attID, aws.ToString(rejected.Associations.TransitGatewayAttachmentId))
}

// TestEC2_TGWMulticastGroups exercises register/deregister of multicast group
// members and sources plus SearchTransitGatewayMulticastGroups.
func TestEC2_TGWMulticastGroups(t *testing.T) {
	client := ec2Client()
	_, domainID, _, _ := tgwMcastFixture(t)
	const groupIP = "224.0.1.0"
	const memberENI = "eni-mcast0001"
	const sourceENI = "eni-mcast0002"

	mem, err := client.RegisterTransitGatewayMulticastGroupMembers(ctx, &ec2.RegisterTransitGatewayMulticastGroupMembersInput{
		TransitGatewayMulticastDomainId: aws.String(domainID),
		GroupIpAddress:                  aws.String(groupIP),
		NetworkInterfaceIds:             []string{memberENI},
	})
	require.NoError(t, err)
	require.NotNil(t, mem.RegisteredMulticastGroupMembers)
	assert.Equal(t, groupIP, aws.ToString(mem.RegisteredMulticastGroupMembers.GroupIpAddress))
	assert.Contains(t, mem.RegisteredMulticastGroupMembers.RegisteredNetworkInterfaceIds, memberENI)

	src, err := client.RegisterTransitGatewayMulticastGroupSources(ctx, &ec2.RegisterTransitGatewayMulticastGroupSourcesInput{
		TransitGatewayMulticastDomainId: aws.String(domainID),
		GroupIpAddress:                  aws.String(groupIP),
		NetworkInterfaceIds:             []string{sourceENI},
	})
	require.NoError(t, err)
	require.NotNil(t, src.RegisteredMulticastGroupSources)
	assert.Contains(t, src.RegisteredMulticastGroupSources.RegisteredNetworkInterfaceIds, sourceENI)

	search, err := client.SearchTransitGatewayMulticastGroups(ctx, &ec2.SearchTransitGatewayMulticastGroupsInput{
		TransitGatewayMulticastDomainId: aws.String(domainID),
	})
	require.NoError(t, err)
	require.Len(t, search.MulticastGroups, 2)
	var sawMember, sawSource bool
	for _, g := range search.MulticastGroups {
		assert.Equal(t, groupIP, aws.ToString(g.GroupIpAddress))
		if g.GroupMember != nil && *g.GroupMember {
			sawMember = true
			assert.Equal(t, memberENI, aws.ToString(g.NetworkInterfaceId))
		}
		if g.GroupSource != nil && *g.GroupSource {
			sawSource = true
			assert.Equal(t, sourceENI, aws.ToString(g.NetworkInterfaceId))
		}
	}
	assert.True(t, sawMember, "expected a group member")
	assert.True(t, sawSource, "expected a group source")

	dmem, err := client.DeregisterTransitGatewayMulticastGroupMembers(ctx, &ec2.DeregisterTransitGatewayMulticastGroupMembersInput{
		TransitGatewayMulticastDomainId: aws.String(domainID),
		GroupIpAddress:                  aws.String(groupIP),
		NetworkInterfaceIds:             []string{memberENI},
	})
	require.NoError(t, err)
	assert.Contains(t, dmem.DeregisteredMulticastGroupMembers.DeregisteredNetworkInterfaceIds, memberENI)

	dsrc, err := client.DeregisterTransitGatewayMulticastGroupSources(ctx, &ec2.DeregisterTransitGatewayMulticastGroupSourcesInput{
		TransitGatewayMulticastDomainId: aws.String(domainID),
		GroupIpAddress:                  aws.String(groupIP),
		NetworkInterfaceIds:             []string{sourceENI},
	})
	require.NoError(t, err)
	assert.Contains(t, dsrc.DeregisteredMulticastGroupSources.DeregisteredNetworkInterfaceIds, sourceENI)
}

// TestEC2_TGWPolicyTable exercises the policy-table CRUD, association, and
// entries read-back path.
func TestEC2_TGWPolicyTable(t *testing.T) {
	client := ec2Client()
	tgwID, _, attID, _ := tgwMcastFixture(t)

	ptOut, err := client.CreateTransitGatewayPolicyTable(ctx, &ec2.CreateTransitGatewayPolicyTableInput{
		TransitGatewayId: aws.String(tgwID),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeTransitGatewayPolicyTable,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("pt")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, ptOut.TransitGatewayPolicyTable)
	ptID := aws.ToString(ptOut.TransitGatewayPolicyTable.TransitGatewayPolicyTableId)
	assert.Contains(t, ptID, "tgw-pt-")
	assert.Equal(t, types.TransitGatewayPolicyTableStateAvailable, ptOut.TransitGatewayPolicyTable.State)
	t.Cleanup(func() {
		_, _ = client.DeleteTransitGatewayPolicyTable(ctx, &ec2.DeleteTransitGatewayPolicyTableInput{
			TransitGatewayPolicyTableId: aws.String(ptID),
		})
	})

	desc, err := client.DescribeTransitGatewayPolicyTables(ctx, &ec2.DescribeTransitGatewayPolicyTablesInput{
		TransitGatewayPolicyTableIds: []string{ptID},
	})
	require.NoError(t, err)
	require.Len(t, desc.TransitGatewayPolicyTables, 1)
	assert.Equal(t, tgwID, aws.ToString(desc.TransitGatewayPolicyTables[0].TransitGatewayId))
	require.Len(t, desc.TransitGatewayPolicyTables[0].Tags, 1)

	assoc, err := client.AssociateTransitGatewayPolicyTable(ctx, &ec2.AssociateTransitGatewayPolicyTableInput{
		TransitGatewayPolicyTableId: aws.String(ptID),
		TransitGatewayAttachmentId:  aws.String(attID),
	})
	require.NoError(t, err)
	require.NotNil(t, assoc.Association)
	assert.Equal(t, attID, aws.ToString(assoc.Association.TransitGatewayAttachmentId))

	gotAssoc, err := client.GetTransitGatewayPolicyTableAssociations(ctx, &ec2.GetTransitGatewayPolicyTableAssociationsInput{
		TransitGatewayPolicyTableId: aws.String(ptID),
	})
	require.NoError(t, err)
	require.Len(t, gotAssoc.Associations, 1)
	assert.Equal(t, attID, aws.ToString(gotAssoc.Associations[0].TransitGatewayAttachmentId))

	entries, err := client.GetTransitGatewayPolicyTableEntries(ctx, &ec2.GetTransitGatewayPolicyTableEntriesInput{
		TransitGatewayPolicyTableId: aws.String(ptID),
	})
	require.NoError(t, err)
	assert.Empty(t, entries.TransitGatewayPolicyTableEntries, "a new policy table holds no rules")

	_, err = client.DisassociateTransitGatewayPolicyTable(ctx, &ec2.DisassociateTransitGatewayPolicyTableInput{
		TransitGatewayPolicyTableId: aws.String(ptID),
		TransitGatewayAttachmentId:  aws.String(attID),
	})
	require.NoError(t, err)
}

// TestEC2_TGWPolicyTableEntries drives the policy-rule lifecycle a policy table
// exists for: rules are created against a target route table, modified in
// place, read back in rule-number order, and deleted. The entries read used to
// answer with a fixed empty list because the API modelled no way to put a rule
// in a table; it now reports what the table actually holds.
func TestEC2_TGWPolicyTableEntries(t *testing.T) {
	client := ec2Client()
	tgwID, _, _, _ := tgwMcastFixture(t)

	ptOut, err := client.CreateTransitGatewayPolicyTable(ctx, &ec2.CreateTransitGatewayPolicyTableInput{
		TransitGatewayId: aws.String(tgwID),
	})
	require.NoError(t, err)
	ptID := aws.ToString(ptOut.TransitGatewayPolicyTable.TransitGatewayPolicyTableId)
	t.Cleanup(func() {
		_, _ = client.DeleteTransitGatewayPolicyTable(ctx, &ec2.DeleteTransitGatewayPolicyTableInput{
			TransitGatewayPolicyTableId: aws.String(ptID),
		})
	})

	// The transit gateway's own default route table is the forwarding target.
	rts, err := client.DescribeTransitGatewayRouteTables(ctx, &ec2.DescribeTransitGatewayRouteTablesInput{
		Filters: []types.Filter{{Name: aws.String("transit-gateway-id"), Values: []string{tgwID}}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, rts.TransitGatewayRouteTables)
	rtID := aws.ToString(rts.TransitGatewayRouteTables[0].TransitGatewayRouteTableId)

	created, err := client.CreateTransitGatewayPolicyTableEntry(ctx, &ec2.CreateTransitGatewayPolicyTableEntryInput{
		TransitGatewayPolicyTableId: aws.String(ptID),
		PolicyRuleNumber:            aws.String("10"),
		TargetRouteTableId:          aws.String(rtID),
		PolicyRule: &types.TransitGatewayRequestPolicyRule{
			SourceCidrBlock:      aws.String("10.0.0.0/16"),
			DestinationCidrBlock: aws.String("10.1.0.0/16"),
			Protocol:             aws.String("tcp"),
			DestinationPortRange: aws.String("443"),
			MetaData: &types.TransitGatewayRequestPolicyRuleMetaData{
				MetaDataKey:   aws.String("tier"),
				MetaDataValue: aws.String("prod"),
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, created.TransitGatewayPolicyTableEntry)
	assert.Equal(t, "10", aws.ToString(created.TransitGatewayPolicyTableEntry.PolicyRuleNumber))
	assert.Equal(t, rtID, aws.ToString(created.TransitGatewayPolicyTableEntry.TargetRouteTableId))
	require.NotNil(t, created.TransitGatewayPolicyTableEntry.PolicyRule)
	assert.Equal(t, "10.0.0.0/16", aws.ToString(created.TransitGatewayPolicyTableEntry.PolicyRule.SourceCidrBlock))
	require.NotNil(t, created.TransitGatewayPolicyTableEntry.PolicyRule.MetaData)
	assert.Equal(t, "prod", aws.ToString(created.TransitGatewayPolicyTableEntry.PolicyRule.MetaData.MetaDataValue))

	// A second rule with a lower number, to prove read-back is ordered by rule
	// number numerically rather than by insertion or string order.
	_, err = client.CreateTransitGatewayPolicyTableEntry(ctx, &ec2.CreateTransitGatewayPolicyTableEntryInput{
		TransitGatewayPolicyTableId: aws.String(ptID),
		PolicyRuleNumber:            aws.String("9"),
		TargetRouteTableId:          aws.String(rtID),
		PolicyRule: &types.TransitGatewayRequestPolicyRule{
			SourceCidrBlock: aws.String("10.2.0.0/16"),
		},
	})
	require.NoError(t, err)

	listed, err := client.GetTransitGatewayPolicyTableEntries(ctx, &ec2.GetTransitGatewayPolicyTableEntriesInput{
		TransitGatewayPolicyTableId: aws.String(ptID),
	})
	require.NoError(t, err)
	require.Len(t, listed.TransitGatewayPolicyTableEntries, 2)
	assert.Equal(t, "9", aws.ToString(listed.TransitGatewayPolicyTableEntries[0].PolicyRuleNumber))
	assert.Equal(t, "10", aws.ToString(listed.TransitGatewayPolicyTableEntries[1].PolicyRuleNumber))

	// A duplicate rule number is rejected rather than silently replacing.
	_, err = client.CreateTransitGatewayPolicyTableEntry(ctx, &ec2.CreateTransitGatewayPolicyTableEntryInput{
		TransitGatewayPolicyTableId: aws.String(ptID),
		PolicyRuleNumber:            aws.String("10"),
		TargetRouteTableId:          aws.String(rtID),
	})
	require.Error(t, err)

	modified, err := client.ModifyTransitGatewayPolicyTableEntry(ctx, &ec2.ModifyTransitGatewayPolicyTableEntryInput{
		TransitGatewayPolicyTableId: aws.String(ptID),
		PolicyRuleNumber:            aws.String("10"),
		PolicyRule: &types.TransitGatewayRequestPolicyRule{
			SourceCidrBlock: aws.String("10.9.0.0/16"),
			Protocol:        aws.String("udp"),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, modified.TransitGatewayPolicyTableEntry.PolicyRule)
	assert.Equal(t, "10.9.0.0/16", aws.ToString(modified.TransitGatewayPolicyTableEntry.PolicyRule.SourceCidrBlock))
	assert.Equal(t, "udp", aws.ToString(modified.TransitGatewayPolicyTableEntry.PolicyRule.Protocol))
	assert.Equal(t, rtID, aws.ToString(modified.TransitGatewayPolicyTableEntry.TargetRouteTableId),
		"an omitted TargetRouteTableId leaves the entry pointing where it already pointed")

	_, err = client.DeleteTransitGatewayPolicyTableEntry(ctx, &ec2.DeleteTransitGatewayPolicyTableEntryInput{
		TransitGatewayPolicyTableId: aws.String(ptID),
		PolicyRuleNumber:            aws.String("10"),
	})
	require.NoError(t, err)

	after, err := client.GetTransitGatewayPolicyTableEntries(ctx, &ec2.GetTransitGatewayPolicyTableEntriesInput{
		TransitGatewayPolicyTableId: aws.String(ptID),
	})
	require.NoError(t, err)
	require.Len(t, after.TransitGatewayPolicyTableEntries, 1)
	assert.Equal(t, "9", aws.ToString(after.TransitGatewayPolicyTableEntries[0].PolicyRuleNumber))

	// Deleting a rule that is gone is an error, not a silent success.
	_, err = client.DeleteTransitGatewayPolicyTableEntry(ctx, &ec2.DeleteTransitGatewayPolicyTableEntryInput{
		TransitGatewayPolicyTableId: aws.String(ptID),
		PolicyRuleNumber:            aws.String("10"),
	})
	require.Error(t, err)
}

// TestEC2_TGWMeteringPolicy exercises metering policy CRUD + entries.
func TestEC2_TGWMeteringPolicy(t *testing.T) {
	client := ec2Client()
	tgwID, _, attID, _ := tgwMcastFixture(t)

	mpOut, err := client.CreateTransitGatewayMeteringPolicy(ctx, &ec2.CreateTransitGatewayMeteringPolicyInput{
		TransitGatewayId:       aws.String(tgwID),
		MiddleboxAttachmentIds: []string{attID},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeTransitGatewayMeteringPolicy,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("mp")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, mpOut.TransitGatewayMeteringPolicy)
	mpID := aws.ToString(mpOut.TransitGatewayMeteringPolicy.TransitGatewayMeteringPolicyId)
	assert.Contains(t, mpID, "tgw-mp-")
	assert.Equal(t, tgwID, aws.ToString(mpOut.TransitGatewayMeteringPolicy.TransitGatewayId))
	assert.Contains(t, mpOut.TransitGatewayMeteringPolicy.MiddleboxAttachmentIds, attID)
	t.Cleanup(func() {
		_, _ = client.DeleteTransitGatewayMeteringPolicy(ctx, &ec2.DeleteTransitGatewayMeteringPolicyInput{
			TransitGatewayMeteringPolicyId: aws.String(mpID),
		})
	})

	modOut, err := client.ModifyTransitGatewayMeteringPolicy(ctx, &ec2.ModifyTransitGatewayMeteringPolicyInput{
		TransitGatewayMeteringPolicyId: aws.String(mpID),
		RemoveMiddleboxAttachmentIds:   []string{attID},
	})
	require.NoError(t, err)
	require.NotNil(t, modOut.TransitGatewayMeteringPolicy)
	assert.NotContains(t, modOut.TransitGatewayMeteringPolicy.MiddleboxAttachmentIds, attID)

	entryOut, err := client.CreateTransitGatewayMeteringPolicyEntry(ctx, &ec2.CreateTransitGatewayMeteringPolicyEntryInput{
		TransitGatewayMeteringPolicyId: aws.String(mpID),
		PolicyRuleNumber:               aws.Int32(100),
		MeteredAccount:                 types.TransitGatewayMeteringPayerTypeSourceAttachmentOwner,
		SourceCidrBlock:                aws.String("10.0.0.0/16"),
		DestinationCidrBlock:           aws.String("10.1.0.0/16"),
		Protocol:                       aws.String("6"),
		SourcePortRange:                aws.String("443"),
	})
	require.NoError(t, err)
	require.NotNil(t, entryOut.TransitGatewayMeteringPolicyEntry)
	assert.Equal(t, "100", aws.ToString(entryOut.TransitGatewayMeteringPolicyEntry.PolicyRuleNumber))
	require.NotNil(t, entryOut.TransitGatewayMeteringPolicyEntry.MeteringPolicyRule)
	assert.Equal(t, "10.0.0.0/16", aws.ToString(entryOut.TransitGatewayMeteringPolicyEntry.MeteringPolicyRule.SourceCidrBlock))

	gotEntries, err := client.GetTransitGatewayMeteringPolicyEntries(ctx, &ec2.GetTransitGatewayMeteringPolicyEntriesInput{
		TransitGatewayMeteringPolicyId: aws.String(mpID),
	})
	require.NoError(t, err)
	require.Len(t, gotEntries.TransitGatewayMeteringPolicyEntries, 1)
	assert.Equal(t, "100", aws.ToString(gotEntries.TransitGatewayMeteringPolicyEntries[0].PolicyRuleNumber))

	_, err = client.DeleteTransitGatewayMeteringPolicyEntry(ctx, &ec2.DeleteTransitGatewayMeteringPolicyEntryInput{
		TransitGatewayMeteringPolicyId: aws.String(mpID),
		PolicyRuleNumber:               aws.Int32(100),
	})
	require.NoError(t, err)
}

// TestEC2_TGWRouteTableAnnouncement exercises the route table announcement
// create/describe/delete path (needs a peering attachment).
func TestEC2_TGWRouteTableAnnouncement(t *testing.T) {
	client := ec2Client()
	tgwID, _, _, _ := tgwMcastFixture(t)

	// A second transit gateway + a peering attachment between them.
	peerTGW, err := client.CreateTransitGateway(ctx, &ec2.CreateTransitGatewayInput{})
	require.NoError(t, err)
	peerTGWID := aws.ToString(peerTGW.TransitGateway.TransitGatewayId)
	t.Cleanup(func() {
		_, _ = client.DeleteTransitGateway(ctx, &ec2.DeleteTransitGatewayInput{TransitGatewayId: aws.String(peerTGWID)})
	})

	peering, err := client.CreateTransitGatewayPeeringAttachment(ctx, &ec2.CreateTransitGatewayPeeringAttachmentInput{
		TransitGatewayId:     aws.String(tgwID),
		PeerTransitGatewayId: aws.String(peerTGWID),
		PeerAccountId:        aws.String("123456789012"),
		PeerRegion:           aws.String("us-east-1"),
	})
	require.NoError(t, err)
	peeringID := aws.ToString(peering.TransitGatewayPeeringAttachment.TransitGatewayAttachmentId)

	rt, err := client.CreateTransitGatewayRouteTable(ctx, &ec2.CreateTransitGatewayRouteTableInput{
		TransitGatewayId: aws.String(tgwID),
	})
	require.NoError(t, err)
	rtID := aws.ToString(rt.TransitGatewayRouteTable.TransitGatewayRouteTableId)

	annOut, err := client.CreateTransitGatewayRouteTableAnnouncement(ctx, &ec2.CreateTransitGatewayRouteTableAnnouncementInput{
		TransitGatewayRouteTableId: aws.String(rtID),
		PeeringAttachmentId:        aws.String(peeringID),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeTransitGatewayRouteTableAnnouncement,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("rta")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, annOut.TransitGatewayRouteTableAnnouncement)
	annID := aws.ToString(annOut.TransitGatewayRouteTableAnnouncement.TransitGatewayRouteTableAnnouncementId)
	assert.Contains(t, annID, "tgw-rta-")
	assert.Equal(t, rtID, aws.ToString(annOut.TransitGatewayRouteTableAnnouncement.TransitGatewayRouteTableId))
	assert.Equal(t, peeringID, aws.ToString(annOut.TransitGatewayRouteTableAnnouncement.PeeringAttachmentId))

	desc, err := client.DescribeTransitGatewayRouteTableAnnouncements(ctx, &ec2.DescribeTransitGatewayRouteTableAnnouncementsInput{
		TransitGatewayRouteTableAnnouncementIds: []string{annID},
	})
	require.NoError(t, err)
	require.Len(t, desc.TransitGatewayRouteTableAnnouncements, 1)
	assert.Equal(t, tgwID, aws.ToString(desc.TransitGatewayRouteTableAnnouncements[0].TransitGatewayId))

	_, err = client.DeleteTransitGatewayRouteTableAnnouncement(ctx, &ec2.DeleteTransitGatewayRouteTableAnnouncementInput{
		TransitGatewayRouteTableAnnouncementId: aws.String(annID),
	})
	require.NoError(t, err)
}
