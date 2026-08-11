package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_IdFormatRoundTripSDK covers the account-level ID-format settings:
// DescribeIdFormat, ModifyIdFormat, DescribeIdentityIdFormat,
// ModifyIdentityIdFormat, DescribeAggregateIdFormat, DescribePrincipalIdFormat.
func TestEC2_IdFormatRoundTripSDK(t *testing.T) {
	c := ec2Client()

	desc, err := c.DescribeIdFormat(ctx, &ec2.DescribeIdFormatInput{Resource: aws.String("vpc")})
	require.NoError(t, err)
	require.Len(t, desc.Statuses, 1)
	assert.Equal(t, "vpc", aws.ToString(desc.Statuses[0].Resource))
	assert.True(t, aws.ToBool(desc.Statuses[0].UseLongIds), "long IDs default on")

	_, err = c.ModifyIdFormat(ctx, &ec2.ModifyIdFormatInput{
		Resource:   aws.String("instance"),
		UseLongIds: aws.Bool(true),
	})
	require.NoError(t, err)

	idn, err := c.DescribeIdentityIdFormat(ctx, &ec2.DescribeIdentityIdFormatInput{
		PrincipalArn: aws.String("arn:aws:iam::000000000000:role/r"),
		Resource:     aws.String("subnet"),
	})
	require.NoError(t, err)
	require.Len(t, idn.Statuses, 1)
	assert.Equal(t, "subnet", aws.ToString(idn.Statuses[0].Resource))

	_, err = c.ModifyIdentityIdFormat(ctx, &ec2.ModifyIdentityIdFormatInput{
		PrincipalArn: aws.String("arn:aws:iam::000000000000:role/r"),
		Resource:     aws.String("subnet"),
		UseLongIds:   aws.Bool(true),
	})
	require.NoError(t, err)

	agg, err := c.DescribeAggregateIdFormat(ctx, &ec2.DescribeAggregateIdFormatInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, agg.Statuses)

	pr, err := c.DescribePrincipalIdFormat(ctx, &ec2.DescribePrincipalIdFormatInput{
		Resources: []string{"vpc"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, pr.Principals)
	assert.NotEmpty(t, aws.ToString(pr.Principals[0].Arn))
	assert.NotEmpty(t, pr.Principals[0].Statuses)
}

// TestEC2_AddressAttributeRoundTripSDK covers ModifyAddressAttribute /
// ResetAddressAttribute (the PTR/reverse-DNS record on an Elastic IP).
func TestEC2_AddressAttributeRoundTripSDK(t *testing.T) {
	c := ec2Client()

	eip, err := c.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: types.DomainTypeVpc})
	require.NoError(t, err)
	allocID := aws.ToString(eip.AllocationId)
	defer func() { _, _ = c.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: eip.AllocationId}) }()

	mod, err := c.ModifyAddressAttribute(ctx, &ec2.ModifyAddressAttributeInput{
		AllocationId: aws.String(allocID),
		DomainName:   aws.String("example.com"),
	})
	require.NoError(t, err)
	require.NotNil(t, mod.Address)
	assert.Equal(t, allocID, aws.ToString(mod.Address.AllocationId))
	assert.Equal(t, "example.com.", aws.ToString(mod.Address.PtrRecord))

	rst, err := c.ResetAddressAttribute(ctx, &ec2.ResetAddressAttributeInput{
		AllocationId: aws.String(allocID),
		Attribute:    types.AddressAttributeNameDomainName,
	})
	require.NoError(t, err)
	require.NotNil(t, rst.Address)
	assert.Equal(t, allocID, aws.ToString(rst.Address.AllocationId))
}

// TestEC2_MovingAddressesRoundTripSDK covers MoveAddressToVpc,
// RestoreAddressToClassic, and DescribeMovingAddresses (EC2-Classic ↔ VPC EIP
// domain transitions).
func TestEC2_MovingAddressesRoundTripSDK(t *testing.T) {
	c := ec2Client()

	eip, err := c.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: types.DomainTypeVpc})
	require.NoError(t, err)
	publicIP := aws.ToString(eip.PublicIp)
	defer func() { _, _ = c.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: eip.AllocationId}) }()

	rc, err := c.RestoreAddressToClassic(ctx, &ec2.RestoreAddressToClassicInput{PublicIp: aws.String(publicIP)})
	require.NoError(t, err)
	assert.Equal(t, publicIP, aws.ToString(rc.PublicIp))
	assert.Equal(t, types.StatusInClassic, rc.Status)

	mv, err := c.DescribeMovingAddresses(ctx, &ec2.DescribeMovingAddressesInput{PublicIps: []string{publicIP}})
	require.NoError(t, err)
	require.Len(t, mv.MovingAddressStatuses, 1)
	assert.Equal(t, publicIP, aws.ToString(mv.MovingAddressStatuses[0].PublicIp))

	back, err := c.MoveAddressToVpc(ctx, &ec2.MoveAddressToVpcInput{PublicIp: aws.String(publicIP)})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(eip.AllocationId), aws.ToString(back.AllocationId))
	assert.Equal(t, types.StatusInVpc, back.Status)
}

// TestEC2_ClassicLinkRoundTripSDK covers AttachClassicLinkVpc /
// DetachClassicLinkVpc on an existing instance + VPC.
func TestEC2_ClassicLinkRoundTripSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.71.0.0/16")})
	require.NoError(t, err)
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}) }()

	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String("ami-12345678"),
		InstanceType: types.InstanceTypeT3Micro,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
	})
	require.NoError(t, err)
	require.NotEmpty(t, run.Instances)
	instID := aws.ToString(run.Instances[0].InstanceId)
	defer func() {
		_, _ = c.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{instID}})
	}()

	att, err := c.AttachClassicLinkVpc(ctx, &ec2.AttachClassicLinkVpcInput{
		InstanceId: aws.String(instID),
		VpcId:      vpc.Vpc.VpcId,
		Groups:     []string{"sg-12345678"},
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(att.Return))

	det, err := c.DetachClassicLinkVpc(ctx, &ec2.DetachClassicLinkVpcInput{
		InstanceId: aws.String(instID),
		VpcId:      vpc.Vpc.VpcId,
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(det.Return))
}

// TestEC2_EnclaveCertificateRoundTripSDK covers AssociateEnclaveCertificateIamRole,
// GetAssociatedEnclaveCertificateIamRoles, and DisassociateEnclaveCertificateIamRole.
func TestEC2_EnclaveCertificateRoundTripSDK(t *testing.T) {
	c := ec2Client()
	const certArn = "arn:aws:acm:us-east-1:000000000000:certificate/abcd1234-ef56-7890-abcd-ef1234567890"
	const roleArn = "arn:aws:iam::000000000000:role/enclave-role"

	assoc, err := c.AssociateEnclaveCertificateIamRole(ctx, &ec2.AssociateEnclaveCertificateIamRoleInput{
		CertificateArn: aws.String(certArn),
		RoleArn:        aws.String(roleArn),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(assoc.CertificateS3BucketName))
	assert.Equal(t, roleArn+"/"+certArn, aws.ToString(assoc.CertificateS3ObjectKey))
	assert.NotEmpty(t, aws.ToString(assoc.EncryptionKmsKeyId))

	get, err := c.GetAssociatedEnclaveCertificateIamRoles(ctx, &ec2.GetAssociatedEnclaveCertificateIamRolesInput{
		CertificateArn: aws.String(certArn),
	})
	require.NoError(t, err)
	require.Len(t, get.AssociatedRoles, 1)
	assert.Equal(t, roleArn, aws.ToString(get.AssociatedRoles[0].AssociatedRoleArn))

	_, err = c.DisassociateEnclaveCertificateIamRole(ctx, &ec2.DisassociateEnclaveCertificateIamRoleInput{
		CertificateArn: aws.String(certArn),
		RoleArn:        aws.String(roleArn),
	})
	require.NoError(t, err)

	get2, err := c.GetAssociatedEnclaveCertificateIamRoles(ctx, &ec2.GetAssociatedEnclaveCertificateIamRolesInput{
		CertificateArn: aws.String(certArn),
	})
	require.NoError(t, err)
	assert.Empty(t, get2.AssociatedRoles)
}

// TestEC2_ClientVpnExportRoundTripSDK covers ExportClientVpnClientConfiguration,
// ImportClientVpnClientCertificateRevocationList, and
// ExportClientVpnClientCertificateRevocationList.
func TestEC2_ClientVpnExportRoundTripSDK(t *testing.T) {
	c := ec2Client()

	ep, err := c.CreateClientVpnEndpoint(ctx, &ec2.CreateClientVpnEndpointInput{
		ClientCidrBlock:      aws.String("10.200.0.0/22"),
		ServerCertificateArn: aws.String("arn:aws:acm:us-east-1:000000000000:certificate/server"),
		AuthenticationOptions: []types.ClientVpnAuthenticationRequest{{
			Type: types.ClientVpnAuthenticationTypeCertificateAuthentication,
			MutualAuthentication: &types.CertificateAuthenticationRequest{
				ClientRootCertificateChainArn: aws.String("arn:aws:acm:us-east-1:000000000000:certificate/client"),
			},
		}},
		ConnectionLogOptions: &types.ConnectionLogOptions{Enabled: aws.Bool(false)},
	})
	require.NoError(t, err)
	epID := aws.ToString(ep.ClientVpnEndpointId)
	defer func() {
		_, _ = c.DeleteClientVpnEndpoint(ctx, &ec2.DeleteClientVpnEndpointInput{ClientVpnEndpointId: aws.String(epID)})
	}()

	cfg, err := c.ExportClientVpnClientConfiguration(ctx, &ec2.ExportClientVpnClientConfigurationInput{
		ClientVpnEndpointId: aws.String(epID),
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(cfg.ClientConfiguration), "client")

	const crlPem = "-----BEGIN X509 CRL-----\nMIIBxzCCAS0CAQEw...\n-----END X509 CRL-----\n"
	_, err = c.ImportClientVpnClientCertificateRevocationList(ctx, &ec2.ImportClientVpnClientCertificateRevocationListInput{
		ClientVpnEndpointId:       aws.String(epID),
		CertificateRevocationList: aws.String(crlPem),
	})
	require.NoError(t, err)

	exp, err := c.ExportClientVpnClientCertificateRevocationList(ctx, &ec2.ExportClientVpnClientCertificateRevocationListInput{
		ClientVpnEndpointId: aws.String(epID),
	})
	require.NoError(t, err)
	assert.Equal(t, crlPem, aws.ToString(exp.CertificateRevocationList))
	require.NotNil(t, exp.Status)
	assert.Equal(t, types.ClientCertificateRevocationListStatusCodeActive, exp.Status.Code)
}

// TestEC2_TgwConnectPeerRoundTripSDK covers CreateTransitGatewayConnectPeer,
// DescribeTransitGatewayConnectPeers, and DeleteTransitGatewayConnectPeer.
func TestEC2_TgwConnectPeerRoundTripSDK(t *testing.T) {
	c := ec2Client()

	tgw, err := c.CreateTransitGateway(ctx, &ec2.CreateTransitGatewayInput{})
	require.NoError(t, err)
	tgwID := aws.ToString(tgw.TransitGateway.TransitGatewayId)
	defer func() {
		_, _ = c.DeleteTransitGateway(ctx, &ec2.DeleteTransitGatewayInput{TransitGatewayId: aws.String(tgwID)})
	}()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.81.0.0/16")})
	require.NoError(t, err)
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}) }()
	subnet, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.81.1.0/24"),
	})
	require.NoError(t, err)
	att, err := c.CreateTransitGatewayVpcAttachment(ctx, &ec2.CreateTransitGatewayVpcAttachmentInput{
		TransitGatewayId: aws.String(tgwID),
		VpcId:            vpc.Vpc.VpcId,
		SubnetIds:        []string{aws.ToString(subnet.Subnet.SubnetId)},
	})
	require.NoError(t, err)
	attID := aws.ToString(att.TransitGatewayVpcAttachment.TransitGatewayAttachmentId)

	conn, err := c.CreateTransitGatewayConnect(ctx, &ec2.CreateTransitGatewayConnectInput{
		TransportTransitGatewayAttachmentId: aws.String(attID),
		Options:                             &types.CreateTransitGatewayConnectRequestOptions{Protocol: types.ProtocolValueGre},
	})
	require.NoError(t, err)
	connAttID := aws.ToString(conn.TransitGatewayConnect.TransitGatewayAttachmentId)

	peer, err := c.CreateTransitGatewayConnectPeer(ctx, &ec2.CreateTransitGatewayConnectPeerInput{
		TransitGatewayAttachmentId: aws.String(connAttID),
		PeerAddress:                aws.String("10.81.2.10"),
		InsideCidrBlocks:           []string{"169.254.6.0/29"},
		BgpOptions:                 &types.TransitGatewayConnectRequestBgpOptions{PeerAsn: aws.Int64(64513)},
	})
	require.NoError(t, err)
	require.NotNil(t, peer.TransitGatewayConnectPeer)
	peerID := aws.ToString(peer.TransitGatewayConnectPeer.TransitGatewayConnectPeerId)
	assert.Equal(t, connAttID, aws.ToString(peer.TransitGatewayConnectPeer.TransitGatewayAttachmentId))
	require.NotNil(t, peer.TransitGatewayConnectPeer.ConnectPeerConfiguration)
	assert.Equal(t, "10.81.2.10", aws.ToString(peer.TransitGatewayConnectPeer.ConnectPeerConfiguration.PeerAddress))

	desc, err := c.DescribeTransitGatewayConnectPeers(ctx, &ec2.DescribeTransitGatewayConnectPeersInput{
		TransitGatewayConnectPeerIds: []string{peerID},
	})
	require.NoError(t, err)
	require.Len(t, desc.TransitGatewayConnectPeers, 1)
	assert.Equal(t, peerID, aws.ToString(desc.TransitGatewayConnectPeers[0].TransitGatewayConnectPeerId))

	del, err := c.DeleteTransitGatewayConnectPeer(ctx, &ec2.DeleteTransitGatewayConnectPeerInput{
		TransitGatewayConnectPeerId: aws.String(peerID),
	})
	require.NoError(t, err)
	assert.Equal(t, peerID, aws.ToString(del.TransitGatewayConnectPeer.TransitGatewayConnectPeerId))
}

// TestEC2_TgwClientVpnAttachmentSDK covers AcceptTransitGatewayClientVpnAttachment,
// RejectTransitGatewayClientVpnAttachment, DeleteTransitGatewayClientVpnAttachment,
// and DescribeTransitGatewayMeteringPolicies.
func TestEC2_TgwClientVpnAttachmentSDK(t *testing.T) {
	c := ec2Client()
	const attID = "tgw-attach-0123456789abcdef0"

	acc, err := c.AcceptTransitGatewayClientVpnAttachment(ctx, &ec2.AcceptTransitGatewayClientVpnAttachmentInput{
		TransitGatewayAttachmentId: aws.String(attID),
	})
	require.NoError(t, err)
	require.NotNil(t, acc.TransitGatewayClientVpnAttachment)
	assert.Equal(t, attID, aws.ToString(acc.TransitGatewayClientVpnAttachment.TransitGatewayAttachmentId))

	rej, err := c.RejectTransitGatewayClientVpnAttachment(ctx, &ec2.RejectTransitGatewayClientVpnAttachmentInput{
		TransitGatewayAttachmentId: aws.String(attID),
	})
	require.NoError(t, err)
	require.NotNil(t, rej.TransitGatewayClientVpnAttachment)

	del, err := c.DeleteTransitGatewayClientVpnAttachment(ctx, &ec2.DeleteTransitGatewayClientVpnAttachmentInput{
		TransitGatewayAttachmentId: aws.String(attID),
	})
	require.NoError(t, err)
	require.NotNil(t, del.TransitGatewayClientVpnAttachment)

	pol, err := c.DescribeTransitGatewayMeteringPolicies(ctx, &ec2.DescribeTransitGatewayMeteringPoliciesInput{})
	require.NoError(t, err)
	assert.Empty(t, pol.TransitGatewayMeteringPolicies)
}

// TestEC2_VpcPeeringOptionsRoundTripSDK covers ModifyVpcPeeringConnectionOptions
// and RejectVpcPeeringConnection on an existing peering connection.
func TestEC2_VpcPeeringOptionsRoundTripSDK(t *testing.T) {
	c := ec2Client()

	a, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.91.0.0/16")})
	require.NoError(t, err)
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: a.Vpc.VpcId}) }()
	b, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.92.0.0/16")})
	require.NoError(t, err)
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: b.Vpc.VpcId}) }()

	pcx, err := c.CreateVpcPeeringConnection(ctx, &ec2.CreateVpcPeeringConnectionInput{
		VpcId:     a.Vpc.VpcId,
		PeerVpcId: b.Vpc.VpcId,
	})
	require.NoError(t, err)
	pcxID := aws.ToString(pcx.VpcPeeringConnection.VpcPeeringConnectionId)

	opts, err := c.ModifyVpcPeeringConnectionOptions(ctx, &ec2.ModifyVpcPeeringConnectionOptionsInput{
		VpcPeeringConnectionId: aws.String(pcxID),
		RequesterPeeringConnectionOptions: &types.PeeringConnectionOptionsRequest{
			AllowDnsResolutionFromRemoteVpc: aws.Bool(true),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, opts.RequesterPeeringConnectionOptions)
	assert.True(t, aws.ToBool(opts.RequesterPeeringConnectionOptions.AllowDnsResolutionFromRemoteVpc))

	// A second, independent peering to reject (rejecting the one above would
	// leave it in a non-deletable state for the deferred cleanup).
	d, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.93.0.0/16")})
	require.NoError(t, err)
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: d.Vpc.VpcId}) }()
	pcx2, err := c.CreateVpcPeeringConnection(ctx, &ec2.CreateVpcPeeringConnectionInput{
		VpcId:     a.Vpc.VpcId,
		PeerVpcId: d.Vpc.VpcId,
	})
	require.NoError(t, err)
	rej, err := c.RejectVpcPeeringConnection(ctx, &ec2.RejectVpcPeeringConnectionInput{
		VpcPeeringConnectionId: pcx2.VpcPeeringConnection.VpcPeeringConnectionId,
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(rej.Return))

	_, _ = c.DeleteVpcPeeringConnection(ctx, &ec2.DeleteVpcPeeringConnectionInput{VpcPeeringConnectionId: aws.String(pcxID)})
}

// TestEC2_NetworkAclAssociationRoundTripSDK covers ReplaceNetworkAclAssociation,
// swapping a subnet's network-ACL association to a new ACL.
func TestEC2_NetworkAclAssociationRoundTripSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.94.0.0/16")})
	require.NoError(t, err)
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}) }()
	subnet, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.94.1.0/24"),
	})
	require.NoError(t, err)
	subnetID := aws.ToString(subnet.Subnet.SubnetId)

	acl1, err := c.CreateNetworkAcl(ctx, &ec2.CreateNetworkAclInput{VpcId: vpc.Vpc.VpcId})
	require.NoError(t, err)
	acl1ID := aws.ToString(acl1.NetworkAcl.NetworkAclId)
	defer func() { _, _ = c.DeleteNetworkAcl(ctx, &ec2.DeleteNetworkAclInput{NetworkAclId: aws.String(acl1ID)}) }()
	acl2, err := c.CreateNetworkAcl(ctx, &ec2.CreateNetworkAclInput{VpcId: vpc.Vpc.VpcId})
	require.NoError(t, err)
	acl2ID := aws.ToString(acl2.NetworkAcl.NetworkAclId)
	defer func() { _, _ = c.DeleteNetworkAcl(ctx, &ec2.DeleteNetworkAclInput{NetworkAclId: aws.String(acl2ID)}) }()

	// Seed an association on acl1 bound to the subnet (associating a brand-new
	// ACL to a subnet that has no prior custom association uses a synthetic
	// placeholder id), then verify the real id round-trips via DescribeNetworkAcls.
	seedAssoc, err := c.ReplaceNetworkAclAssociation(ctx, &ec2.ReplaceNetworkAclAssociationInput{
		AssociationId: aws.String("aclassoc-seed-" + subnetID),
		NetworkAclId:  aws.String(acl1ID),
	})
	require.NoError(t, err)
	assocID := aws.ToString(seedAssoc.NewAssociationId)
	require.NotEmpty(t, assocID)

	// Now swap that association onto acl2.
	repl, err := c.ReplaceNetworkAclAssociation(ctx, &ec2.ReplaceNetworkAclAssociationInput{
		AssociationId: aws.String(assocID),
		NetworkAclId:  aws.String(acl2ID),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(repl.NewAssociationId))
	assert.NotEqual(t, assocID, aws.ToString(repl.NewAssociationId))

	// The new association now lives on acl2.
	desc, err := c.DescribeNetworkAcls(ctx, &ec2.DescribeNetworkAclsInput{NetworkAclIds: []string{acl2ID}})
	require.NoError(t, err)
	require.Len(t, desc.NetworkAcls, 1)
	var found bool
	for _, a := range desc.NetworkAcls[0].Associations {
		if aws.ToString(a.NetworkAclAssociationId) == aws.ToString(repl.NewAssociationId) {
			found = true
		}
	}
	assert.True(t, found, "new association must live on the target ACL")
}

// TestEC2_VpnTunnelReplacementSDK covers GetVpnTunnelReplacementStatus,
// ModifyVpnTunnelCertificate, and ReplaceVpnTunnel against an existing
// Site-to-Site VPN connection.
func TestEC2_VpnTunnelReplacementSDK(t *testing.T) {
	c := ec2Client()

	cgw, err := c.CreateCustomerGateway(ctx, &ec2.CreateCustomerGatewayInput{
		BgpAsn:    aws.Int32(65010),
		IpAddress: aws.String("198.51.100.99"),
		Type:      types.GatewayTypeIpsec1,
	})
	require.NoError(t, err)
	cgwID := aws.ToString(cgw.CustomerGateway.CustomerGatewayId)
	defer func() {
		_, _ = c.DeleteCustomerGateway(ctx, &ec2.DeleteCustomerGatewayInput{CustomerGatewayId: aws.String(cgwID)})
	}()

	vgw, err := c.CreateVpnGateway(ctx, &ec2.CreateVpnGatewayInput{Type: types.GatewayTypeIpsec1})
	require.NoError(t, err)
	vgwID := aws.ToString(vgw.VpnGateway.VpnGatewayId)
	defer func() { _, _ = c.DeleteVpnGateway(ctx, &ec2.DeleteVpnGatewayInput{VpnGatewayId: aws.String(vgwID)}) }()

	conn, err := c.CreateVpnConnection(ctx, &ec2.CreateVpnConnectionInput{
		CustomerGatewayId: aws.String(cgwID),
		VpnGatewayId:      aws.String(vgwID),
		Type:              aws.String("ipsec.1"),
	})
	require.NoError(t, err)
	connID := aws.ToString(conn.VpnConnection.VpnConnectionId)
	require.NotEmpty(t, conn.VpnConnection.VgwTelemetry)
	outsideIP := aws.ToString(conn.VpnConnection.VgwTelemetry[0].OutsideIpAddress)
	defer func() {
		_, _ = c.DeleteVpnConnection(ctx, &ec2.DeleteVpnConnectionInput{VpnConnectionId: aws.String(connID)})
	}()

	status, err := c.GetVpnTunnelReplacementStatus(ctx, &ec2.GetVpnTunnelReplacementStatusInput{
		VpnConnectionId:           aws.String(connID),
		VpnTunnelOutsideIpAddress: aws.String(outsideIP),
	})
	require.NoError(t, err)
	assert.Equal(t, connID, aws.ToString(status.VpnConnectionId))
	assert.Equal(t, outsideIP, aws.ToString(status.VpnTunnelOutsideIpAddress))

	cert, err := c.ModifyVpnTunnelCertificate(ctx, &ec2.ModifyVpnTunnelCertificateInput{
		VpnConnectionId:           aws.String(connID),
		VpnTunnelOutsideIpAddress: aws.String(outsideIP),
	})
	require.NoError(t, err)
	require.NotNil(t, cert.VpnConnection)
	assert.Equal(t, connID, aws.ToString(cert.VpnConnection.VpnConnectionId))

	repl, err := c.ReplaceVpnTunnel(ctx, &ec2.ReplaceVpnTunnelInput{
		VpnConnectionId:           aws.String(connID),
		VpnTunnelOutsideIpAddress: aws.String(outsideIP),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(repl.Return))
}

// TestEC2_OutpostLagsAndFlowLogsTemplateSDK covers DescribeOutpostLags (too new
// for the local aws CLI, so SDK-only) and GetFlowLogsIntegrationTemplate.
func TestEC2_OutpostLagsAndFlowLogsTemplateSDK(t *testing.T) {
	c := ec2Client()

	lags, err := c.DescribeOutpostLags(ctx, &ec2.DescribeOutpostLagsInput{})
	require.NoError(t, err)
	assert.Empty(t, lags.OutpostLags)

	// GetFlowLogsIntegrationTemplate needs a flow-log id; create one against the
	// account's default VPC.
	vpcs, err := c.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, vpcs.Vpcs)
	vpcID := aws.ToString(vpcs.Vpcs[0].VpcId)

	fl, err := c.CreateFlowLogs(ctx, &ec2.CreateFlowLogsInput{
		ResourceIds:        []string{vpcID},
		ResourceType:       types.FlowLogsResourceTypeVpc,
		TrafficType:        types.TrafficTypeAll,
		LogDestinationType: types.LogDestinationTypeS3,
		LogDestination:     aws.String("arn:aws:s3:::flow-logs-bucket"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, fl.FlowLogIds)
	flID := fl.FlowLogIds[0]
	defer func() { _, _ = c.DeleteFlowLogs(ctx, &ec2.DeleteFlowLogsInput{FlowLogIds: []string{flID}}) }()

	tmpl, err := c.GetFlowLogsIntegrationTemplate(ctx, &ec2.GetFlowLogsIntegrationTemplateInput{
		FlowLogId:                      aws.String(flID),
		ConfigDeliveryS3DestinationArn: aws.String("arn:aws:s3:::flow-logs-bucket/templates"),
		IntegrateServices: &types.IntegrateServices{
			AthenaIntegrations: []types.AthenaIntegration{{
				IntegrationResultS3DestinationArn: aws.String("arn:aws:s3:::flow-logs-bucket/athena"),
				PartitionLoadFrequency:            types.PartitionLoadFrequencyNone,
			}},
		},
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(tmpl.Result), "AWSTemplateFormatVersion")
}

// TestEC2_MiscHonestEmptySDK covers the read-only / honest-empty and account
// settings operations: DescribeVpcEndpointAssociations, DescribeElasticGpus,
// DescribeSecondaryInterfaces, DescribeServiceLinkVirtualInterfaces,
// EnableReachabilityAnalyzerOrganizationSharing,
// GetVpcResourcesBlockingEncryptionEnforcement, GetManagedResourceVisibility,
// and ModifyManagedResourceVisibility. The latter six lack an aws CLI 2.26.6
// verb, so the SDK suite owns the simulator-test-contract hook for them.
func TestEC2_MiscHonestEmptySDK(t *testing.T) {
	c := ec2Client()

	assoc, err := c.DescribeVpcEndpointAssociations(ctx, &ec2.DescribeVpcEndpointAssociationsInput{})
	require.NoError(t, err)
	assert.Empty(t, assoc.VpcEndpointAssociations)

	gpus, err := c.DescribeElasticGpus(ctx, &ec2.DescribeElasticGpusInput{})
	require.NoError(t, err)
	assert.Empty(t, gpus.ElasticGpuSet)

	sec, err := c.DescribeSecondaryInterfaces(ctx, &ec2.DescribeSecondaryInterfacesInput{})
	require.NoError(t, err)
	assert.Empty(t, sec.SecondaryInterfaces)

	slvi, err := c.DescribeServiceLinkVirtualInterfaces(ctx, &ec2.DescribeServiceLinkVirtualInterfacesInput{})
	require.NoError(t, err)
	assert.Empty(t, slvi.ServiceLinkVirtualInterfaces)

	share, err := c.EnableReachabilityAnalyzerOrganizationSharing(ctx, &ec2.EnableReachabilityAnalyzerOrganizationSharingInput{})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(share.ReturnValue))

	vpcs, err := c.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, vpcs.Vpcs)
	enc, err := c.GetVpcResourcesBlockingEncryptionEnforcement(ctx, &ec2.GetVpcResourcesBlockingEncryptionEnforcementInput{
		VpcId: vpcs.Vpcs[0].VpcId,
	})
	require.NoError(t, err)
	assert.Empty(t, enc.NonCompliantResources)

	vis, err := c.GetManagedResourceVisibility(ctx, &ec2.GetManagedResourceVisibilityInput{})
	require.NoError(t, err)
	require.NotNil(t, vis.Visibility)

	mod, err := c.ModifyManagedResourceVisibility(ctx, &ec2.ModifyManagedResourceVisibilityInput{
		DefaultVisibility: types.ManagedResourceDefaultVisibilityHidden,
	})
	require.NoError(t, err)
	require.NotNil(t, mod.Visibility)
	assert.Equal(t, types.ManagedResourceDefaultVisibilityHidden, mod.Visibility.DefaultVisibility)
	// Restore the default so the shared-DB setting doesn't leak into other tests.
	_, _ = c.ModifyManagedResourceVisibility(ctx, &ec2.ModifyManagedResourceVisibilityInput{
		DefaultVisibility: types.ManagedResourceDefaultVisibilityVisible,
	})
}
