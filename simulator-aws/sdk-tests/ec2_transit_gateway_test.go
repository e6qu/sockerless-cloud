package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_TransitGatewayCore exercises the transit gateway, its route tables,
// VPC attachments, and the association/propagation/route plumbing — the core
// CRUD path a typical aws_ec2_transit_gateway Terraform stack relies on.
func TestEC2_TransitGatewayCore(t *testing.T) {
	client := ec2Client()

	// --- Transit gateway ---
	tgwOut, err := client.CreateTransitGateway(ctx, &ec2.CreateTransitGatewayInput{
		Description: aws.String("sdk tgw"),
		Options: &types.TransitGatewayRequestOptions{
			AmazonSideAsn:    aws.Int64(64513),
			DnsSupport:       types.DnsSupportValueEnable,
			MulticastSupport: types.MulticastSupportValueEnable,
		},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeTransitGateway,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("core-tgw")}},
		}},
	})
	require.NoError(t, err)
	tgw := tgwOut.TransitGateway
	require.NotNil(t, tgw)
	tgwID := aws.ToString(tgw.TransitGatewayId)
	assert.Contains(t, tgwID, "tgw-")
	assert.Equal(t, types.TransitGatewayStateAvailable, tgw.State)
	assert.Contains(t, aws.ToString(tgw.TransitGatewayArn), "transit-gateway/")
	require.NotNil(t, tgw.Options)
	assert.Equal(t, int64(64513), aws.ToInt64(tgw.Options.AmazonSideAsn))
	// Default route tables auto-created.
	assocRT := aws.ToString(tgw.Options.AssociationDefaultRouteTableId)
	require.NotEmpty(t, assocRT)

	// Describe + Modify.
	desc, err := client.DescribeTransitGateways(ctx, &ec2.DescribeTransitGatewaysInput{
		TransitGatewayIds: []string{tgwID},
	})
	require.NoError(t, err)
	require.Len(t, desc.TransitGateways, 1)
	assert.Equal(t, tgwID, aws.ToString(desc.TransitGateways[0].TransitGatewayId))

	_, err = client.ModifyTransitGateway(ctx, &ec2.ModifyTransitGatewayInput{
		TransitGatewayId: aws.String(tgwID),
		Description:      aws.String("updated"),
	})
	require.NoError(t, err)

	// --- Route table ---
	rtOut, err := client.CreateTransitGatewayRouteTable(ctx, &ec2.CreateTransitGatewayRouteTableInput{
		TransitGatewayId: aws.String(tgwID),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeTransitGatewayRouteTable,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("rt1")}},
		}},
	})
	require.NoError(t, err)
	rtID := aws.ToString(rtOut.TransitGatewayRouteTable.TransitGatewayRouteTableId)
	assert.Contains(t, rtID, "tgw-rtb-")
	assert.Equal(t, types.TransitGatewayRouteTableStateAvailable, rtOut.TransitGatewayRouteTable.State)

	rtDesc, err := client.DescribeTransitGatewayRouteTables(ctx, &ec2.DescribeTransitGatewayRouteTablesInput{
		TransitGatewayRouteTableIds: []string{rtID},
	})
	require.NoError(t, err)
	require.Len(t, rtDesc.TransitGatewayRouteTables, 1)

	// --- VPC attachment ---
	vpc, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.40.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	subnet, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: aws.String(vpcID), CidrBlock: aws.String("10.40.1.0/24"),
	})
	require.NoError(t, err)
	subnetID := aws.ToString(subnet.Subnet.SubnetId)

	attOut, err := client.CreateTransitGatewayVpcAttachment(ctx, &ec2.CreateTransitGatewayVpcAttachmentInput{
		TransitGatewayId: aws.String(tgwID),
		VpcId:            aws.String(vpcID),
		SubnetIds:        []string{subnetID},
		Options: &types.CreateTransitGatewayVpcAttachmentRequestOptions{
			DnsSupport:  types.DnsSupportValueEnable,
			Ipv6Support: types.Ipv6SupportValueDisable,
		},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeTransitGatewayAttachment,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("att1")}},
		}},
	})
	require.NoError(t, err)
	att := attOut.TransitGatewayVpcAttachment
	attID := aws.ToString(att.TransitGatewayAttachmentId)
	assert.Contains(t, attID, "tgw-attach-")
	assert.Equal(t, vpcID, aws.ToString(att.VpcId))
	require.Len(t, att.SubnetIds, 1)
	assert.Equal(t, types.TransitGatewayAttachmentStateAvailable, att.State)

	attDesc, err := client.DescribeTransitGatewayVpcAttachments(ctx, &ec2.DescribeTransitGatewayVpcAttachmentsInput{
		TransitGatewayAttachmentIds: []string{attID},
	})
	require.NoError(t, err)
	require.Len(t, attDesc.TransitGatewayVpcAttachments, 1)

	_, err = client.ModifyTransitGatewayVpcAttachment(ctx, &ec2.ModifyTransitGatewayVpcAttachmentInput{
		TransitGatewayAttachmentId: aws.String(attID),
		Options: &types.ModifyTransitGatewayVpcAttachmentRequestOptions{
			ApplianceModeSupport: types.ApplianceModeSupportValueEnable,
		},
	})
	require.NoError(t, err)

	// AcceptTransitGatewayVpcAttachment + Reject are valid no-op transitions
	// here (we own the gateway so it is already available).
	_, err = client.AcceptTransitGatewayVpcAttachment(ctx, &ec2.AcceptTransitGatewayVpcAttachmentInput{
		TransitGatewayAttachmentId: aws.String(attID),
	})
	require.NoError(t, err)

	// Cross-type attachment listing.
	allAtt, err := client.DescribeTransitGatewayAttachments(ctx, &ec2.DescribeTransitGatewayAttachmentsInput{
		Filters: []types.Filter{{Name: aws.String("transit-gateway-id"), Values: []string{tgwID}}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, allAtt.TransitGatewayAttachments)
	assert.Equal(t, types.TransitGatewayAttachmentResourceTypeVpc, allAtt.TransitGatewayAttachments[0].ResourceType)

	// --- Associations + propagations ---
	_, err = client.DisassociateTransitGatewayRouteTable(ctx, &ec2.DisassociateTransitGatewayRouteTableInput{
		TransitGatewayRouteTableId: aws.String(assocRT),
		TransitGatewayAttachmentId: aws.String(attID),
	})
	require.NoError(t, err)

	assocOut, err := client.AssociateTransitGatewayRouteTable(ctx, &ec2.AssociateTransitGatewayRouteTableInput{
		TransitGatewayRouteTableId: aws.String(rtID),
		TransitGatewayAttachmentId: aws.String(attID),
	})
	require.NoError(t, err)
	require.NotNil(t, assocOut.Association)
	assert.Equal(t, attID, aws.ToString(assocOut.Association.TransitGatewayAttachmentId))

	getAssoc, err := client.GetTransitGatewayRouteTableAssociations(ctx, &ec2.GetTransitGatewayRouteTableAssociationsInput{
		TransitGatewayRouteTableId: aws.String(rtID),
	})
	require.NoError(t, err)
	require.Len(t, getAssoc.Associations, 1)
	assert.Equal(t, attID, aws.ToString(getAssoc.Associations[0].TransitGatewayAttachmentId))

	enProp, err := client.EnableTransitGatewayRouteTablePropagation(ctx, &ec2.EnableTransitGatewayRouteTablePropagationInput{
		TransitGatewayRouteTableId: aws.String(rtID),
		TransitGatewayAttachmentId: aws.String(attID),
	})
	require.NoError(t, err)
	require.NotNil(t, enProp.Propagation)

	getProp, err := client.GetTransitGatewayRouteTablePropagations(ctx, &ec2.GetTransitGatewayRouteTablePropagationsInput{
		TransitGatewayRouteTableId: aws.String(rtID),
	})
	require.NoError(t, err)
	require.Len(t, getProp.TransitGatewayRouteTablePropagations, 1)

	// The attachment auto-propagated to the gateway's default propagation route
	// table at create time, and now also to rtID via the explicit Enable above,
	// so its propagation set spans both route tables.
	attProp, err := client.GetTransitGatewayAttachmentPropagations(ctx, &ec2.GetTransitGatewayAttachmentPropagationsInput{
		TransitGatewayAttachmentId: aws.String(attID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, attProp.TransitGatewayAttachmentPropagations)
	var propRTs []string
	for _, p := range attProp.TransitGatewayAttachmentPropagations {
		propRTs = append(propRTs, aws.ToString(p.TransitGatewayRouteTableId))
	}
	assert.Contains(t, propRTs, rtID)

	_, err = client.DisableTransitGatewayRouteTablePropagation(ctx, &ec2.DisableTransitGatewayRouteTablePropagationInput{
		TransitGatewayRouteTableId: aws.String(rtID),
		TransitGatewayAttachmentId: aws.String(attID),
	})
	require.NoError(t, err)

	// --- Routes ---
	rOut, err := client.CreateTransitGatewayRoute(ctx, &ec2.CreateTransitGatewayRouteInput{
		TransitGatewayRouteTableId: aws.String(rtID),
		DestinationCidrBlock:       aws.String("10.99.0.0/16"),
		TransitGatewayAttachmentId: aws.String(attID),
	})
	require.NoError(t, err)
	require.NotNil(t, rOut.Route)
	assert.Equal(t, types.TransitGatewayRouteTypeStatic, rOut.Route.Type)
	assert.Equal(t, types.TransitGatewayRouteStateActive, rOut.Route.State)

	_, err = client.ReplaceTransitGatewayRoute(ctx, &ec2.ReplaceTransitGatewayRouteInput{
		TransitGatewayRouteTableId: aws.String(rtID),
		DestinationCidrBlock:       aws.String("10.99.0.0/16"),
		Blackhole:                  aws.Bool(true),
	})
	require.NoError(t, err)

	search, err := client.SearchTransitGatewayRoutes(ctx, &ec2.SearchTransitGatewayRoutesInput{
		TransitGatewayRouteTableId: aws.String(rtID),
		Filters:                    []types.Filter{{Name: aws.String("type"), Values: []string{"static"}}},
	})
	require.NoError(t, err)
	require.Len(t, search.Routes, 1)
	assert.Equal(t, "10.99.0.0/16", aws.ToString(search.Routes[0].DestinationCidrBlock))

	_, err = client.DeleteTransitGatewayRoute(ctx, &ec2.DeleteTransitGatewayRouteInput{
		TransitGatewayRouteTableId: aws.String(rtID),
		DestinationCidrBlock:       aws.String("10.99.0.0/16"),
	})
	require.NoError(t, err)

	export, err := client.ExportTransitGatewayRoutes(ctx, &ec2.ExportTransitGatewayRoutesInput{
		TransitGatewayRouteTableId: aws.String(rtID),
		S3Bucket:                   aws.String("my-bucket"),
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(export.S3Location), "s3://my-bucket")

	// --- Cleanup (tolerant) ---
	_, _ = client.RejectTransitGatewayVpcAttachment(ctx, &ec2.RejectTransitGatewayVpcAttachmentInput{
		TransitGatewayAttachmentId: aws.String(attID),
	})
	_, _ = client.DeleteTransitGatewayVpcAttachment(ctx, &ec2.DeleteTransitGatewayVpcAttachmentInput{
		TransitGatewayAttachmentId: aws.String(attID),
	})
	_, _ = client.DeleteTransitGatewayRouteTable(ctx, &ec2.DeleteTransitGatewayRouteTableInput{
		TransitGatewayRouteTableId: aws.String(rtID),
	})
	_, _ = client.DeleteTransitGateway(ctx, &ec2.DeleteTransitGatewayInput{
		TransitGatewayId: aws.String(tgwID),
	})
}

// TestEC2_TransitGatewayPrefixListPeeringConnectMulticast covers prefix-list
// references, peering attachments, connect attachments, and multicast domains.
func TestEC2_TransitGatewayPrefixListPeeringConnectMulticast(t *testing.T) {
	client := ec2Client()

	tgwOut, err := client.CreateTransitGateway(ctx, &ec2.CreateTransitGatewayInput{
		Options: &types.TransitGatewayRequestOptions{MulticastSupport: types.MulticastSupportValueEnable},
	})
	require.NoError(t, err)
	tgwID := aws.ToString(tgwOut.TransitGateway.TransitGatewayId)
	assocRT := aws.ToString(tgwOut.TransitGateway.Options.AssociationDefaultRouteTableId)

	vpc, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.41.0.0/16")})
	require.NoError(t, err)
	subnet, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.41.1.0/24"),
	})
	require.NoError(t, err)
	attOut, err := client.CreateTransitGatewayVpcAttachment(ctx, &ec2.CreateTransitGatewayVpcAttachmentInput{
		TransitGatewayId: aws.String(tgwID),
		VpcId:            vpc.Vpc.VpcId,
		SubnetIds:        []string{aws.ToString(subnet.Subnet.SubnetId)},
	})
	require.NoError(t, err)
	attID := aws.ToString(attOut.TransitGatewayVpcAttachment.TransitGatewayAttachmentId)

	// --- Prefix list references ---
	pl, err := client.CreateManagedPrefixList(ctx, &ec2.CreateManagedPrefixListInput{
		PrefixListName: aws.String("tgw-pl"),
		MaxEntries:     aws.Int32(5),
		AddressFamily:  aws.String("IPv4"),
	})
	require.NoError(t, err)
	plID := aws.ToString(pl.PrefixList.PrefixListId)

	plRef, err := client.CreateTransitGatewayPrefixListReference(ctx, &ec2.CreateTransitGatewayPrefixListReferenceInput{
		TransitGatewayRouteTableId: aws.String(assocRT),
		PrefixListId:               aws.String(plID),
		TransitGatewayAttachmentId: aws.String(attID),
	})
	require.NoError(t, err)
	require.NotNil(t, plRef.TransitGatewayPrefixListReference)
	assert.Equal(t, plID, aws.ToString(plRef.TransitGatewayPrefixListReference.PrefixListId))

	getRefs, err := client.GetTransitGatewayPrefixListReferences(ctx, &ec2.GetTransitGatewayPrefixListReferencesInput{
		TransitGatewayRouteTableId: aws.String(assocRT),
	})
	require.NoError(t, err)
	require.Len(t, getRefs.TransitGatewayPrefixListReferences, 1)

	_, err = client.ModifyTransitGatewayPrefixListReference(ctx, &ec2.ModifyTransitGatewayPrefixListReferenceInput{
		TransitGatewayRouteTableId: aws.String(assocRT),
		PrefixListId:               aws.String(plID),
		Blackhole:                  aws.Bool(true),
	})
	require.NoError(t, err)

	_, err = client.DeleteTransitGatewayPrefixListReference(ctx, &ec2.DeleteTransitGatewayPrefixListReferenceInput{
		TransitGatewayRouteTableId: aws.String(assocRT),
		PrefixListId:               aws.String(plID),
	})
	require.NoError(t, err)

	// --- Connect ---
	connOut, err := client.CreateTransitGatewayConnect(ctx, &ec2.CreateTransitGatewayConnectInput{
		TransportTransitGatewayAttachmentId: aws.String(attID),
		Options:                             &types.CreateTransitGatewayConnectRequestOptions{Protocol: types.ProtocolValueGre},
	})
	require.NoError(t, err)
	connID := aws.ToString(connOut.TransitGatewayConnect.TransitGatewayAttachmentId)
	assert.Equal(t, attID, aws.ToString(connOut.TransitGatewayConnect.TransportTransitGatewayAttachmentId))

	connDesc, err := client.DescribeTransitGatewayConnects(ctx, &ec2.DescribeTransitGatewayConnectsInput{
		TransitGatewayAttachmentIds: []string{connID},
	})
	require.NoError(t, err)
	require.Len(t, connDesc.TransitGatewayConnects, 1)

	// --- Multicast domain ---
	mc, err := client.CreateTransitGatewayMulticastDomain(ctx, &ec2.CreateTransitGatewayMulticastDomainInput{
		TransitGatewayId: aws.String(tgwID),
		Options: &types.CreateTransitGatewayMulticastDomainRequestOptions{
			Igmpv2Support: types.Igmpv2SupportValueEnable,
		},
	})
	require.NoError(t, err)
	mcID := aws.ToString(mc.TransitGatewayMulticastDomain.TransitGatewayMulticastDomainId)
	assert.Contains(t, mcID, "tgw-mcast-domain-")
	assert.Contains(t, aws.ToString(mc.TransitGatewayMulticastDomain.TransitGatewayMulticastDomainArn), "multicast-domain/")

	mcDesc, err := client.DescribeTransitGatewayMulticastDomains(ctx, &ec2.DescribeTransitGatewayMulticastDomainsInput{
		TransitGatewayMulticastDomainIds: []string{mcID},
	})
	require.NoError(t, err)
	require.Len(t, mcDesc.TransitGatewayMulticastDomains, 1)

	// --- Peering ---
	peerTGW, err := client.CreateTransitGateway(ctx, &ec2.CreateTransitGatewayInput{})
	require.NoError(t, err)
	peerTGWID := aws.ToString(peerTGW.TransitGateway.TransitGatewayId)

	peerOut, err := client.CreateTransitGatewayPeeringAttachment(ctx, &ec2.CreateTransitGatewayPeeringAttachmentInput{
		TransitGatewayId:     aws.String(tgwID),
		PeerTransitGatewayId: aws.String(peerTGWID),
		PeerAccountId:        aws.String("123456789012"),
		PeerRegion:           aws.String("us-west-2"),
	})
	require.NoError(t, err)
	peerID := aws.ToString(peerOut.TransitGatewayPeeringAttachment.TransitGatewayAttachmentId)
	assert.Equal(t, types.TransitGatewayAttachmentStatePendingAcceptance, peerOut.TransitGatewayPeeringAttachment.State)

	peerDesc, err := client.DescribeTransitGatewayPeeringAttachments(ctx, &ec2.DescribeTransitGatewayPeeringAttachmentsInput{
		TransitGatewayAttachmentIds: []string{peerID},
	})
	require.NoError(t, err)
	require.Len(t, peerDesc.TransitGatewayPeeringAttachments, 1)

	accepted, err := client.AcceptTransitGatewayPeeringAttachment(ctx, &ec2.AcceptTransitGatewayPeeringAttachmentInput{
		TransitGatewayAttachmentId: aws.String(peerID),
	})
	require.NoError(t, err)
	assert.Equal(t, types.TransitGatewayAttachmentStateAvailable, accepted.TransitGatewayPeeringAttachment.State)

	// RejectTransitGatewayPeeringAttachment is a valid transition target too.
	_, err = client.RejectTransitGatewayPeeringAttachment(ctx, &ec2.RejectTransitGatewayPeeringAttachmentInput{
		TransitGatewayAttachmentId: aws.String(peerID),
	})
	require.NoError(t, err)

	// --- Cleanup (tolerant) ---
	_, _ = client.DeleteTransitGatewayPeeringAttachment(ctx, &ec2.DeleteTransitGatewayPeeringAttachmentInput{
		TransitGatewayAttachmentId: aws.String(peerID),
	})
	_, _ = client.DeleteTransitGatewayConnect(ctx, &ec2.DeleteTransitGatewayConnectInput{
		TransitGatewayAttachmentId: aws.String(connID),
	})
	_, _ = client.DeleteTransitGatewayMulticastDomain(ctx, &ec2.DeleteTransitGatewayMulticastDomainInput{
		TransitGatewayMulticastDomainId: aws.String(mcID),
	})
	_, _ = client.DeleteTransitGatewayVpcAttachment(ctx, &ec2.DeleteTransitGatewayVpcAttachmentInput{
		TransitGatewayAttachmentId: aws.String(attID),
	})
	_, _ = client.DeleteTransitGateway(ctx, &ec2.DeleteTransitGatewayInput{TransitGatewayId: aws.String(tgwID)})
	_, _ = client.DeleteTransitGateway(ctx, &ec2.DeleteTransitGatewayInput{TransitGatewayId: aws.String(peerTGWID)})
}
