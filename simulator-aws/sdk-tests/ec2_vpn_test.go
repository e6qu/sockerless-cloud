package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_VPNSiteToSite covers the full Site-to-Site VPN control plane:
// customer gateway + VPN gateway + VPN connection (with two IPsec tunnels and a
// static route), the Describe read-backs, ModifyVpnConnectionOptions /
// ModifyVpnTunnelOptions, GetActiveVpnTunnelStatus, the device-type catalog and
// sample-configuration generator, and the tolerant teardown.
func TestEC2_VPNSiteToSite(t *testing.T) {
	c := ec2Client()

	// Customer gateway.
	cgwOut, err := c.CreateCustomerGateway(ctx, &ec2.CreateCustomerGatewayInput{
		BgpAsn:    aws.Int32(65000),
		IpAddress: aws.String("198.51.100.7"),
		Type:      types.GatewayTypeIpsec1,
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeCustomerGateway,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("onprem")}},
		}},
	})
	require.NoError(t, err)
	cgwID := aws.ToString(cgwOut.CustomerGateway.CustomerGatewayId)
	require.NotEmpty(t, cgwID)
	assert.Equal(t, "198.51.100.7", aws.ToString(cgwOut.CustomerGateway.IpAddress))
	assert.Equal(t, "available", aws.ToString(cgwOut.CustomerGateway.State))

	t.Cleanup(func() {
		_, _ = c.DeleteCustomerGateway(ctx, &ec2.DeleteCustomerGatewayInput{CustomerGatewayId: aws.String(cgwID)})
	})

	dcgw, err := c.DescribeCustomerGateways(ctx, &ec2.DescribeCustomerGatewaysInput{
		CustomerGatewayIds: []string{cgwID},
	})
	require.NoError(t, err)
	require.Len(t, dcgw.CustomerGateways, 1)
	assert.Equal(t, "65000", aws.ToString(dcgw.CustomerGateways[0].BgpAsn))
	require.Len(t, dcgw.CustomerGateways[0].Tags, 1)
	assert.Equal(t, "Name", aws.ToString(dcgw.CustomerGateways[0].Tags[0].Key))

	// VPN gateway + VPC attachment.
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.200.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)

	vgwOut, err := c.CreateVpnGateway(ctx, &ec2.CreateVpnGatewayInput{
		Type:          types.GatewayTypeIpsec1,
		AmazonSideAsn: aws.Int64(64512),
	})
	require.NoError(t, err)
	vgwID := aws.ToString(vgwOut.VpnGateway.VpnGatewayId)
	require.NotEmpty(t, vgwID)

	t.Cleanup(func() {
		_, _ = c.DetachVpnGateway(ctx, &ec2.DetachVpnGatewayInput{VpnGatewayId: aws.String(vgwID), VpcId: aws.String(vpcID)})
		_, _ = c.DeleteVpnGateway(ctx, &ec2.DeleteVpnGatewayInput{VpnGatewayId: aws.String(vgwID)})
	})

	att, err := c.AttachVpnGateway(ctx, &ec2.AttachVpnGatewayInput{
		VpnGatewayId: aws.String(vgwID), VpcId: aws.String(vpcID),
	})
	require.NoError(t, err)
	require.NotNil(t, att.VpcAttachment)
	assert.Equal(t, vpcID, aws.ToString(att.VpcAttachment.VpcId))

	dvgw, err := c.DescribeVpnGateways(ctx, &ec2.DescribeVpnGatewaysInput{VpnGatewayIds: []string{vgwID}})
	require.NoError(t, err)
	require.Len(t, dvgw.VpnGateways, 1)
	require.Len(t, dvgw.VpnGateways[0].VpcAttachments, 1)
	assert.Equal(t, vpcID, aws.ToString(dvgw.VpnGateways[0].VpcAttachments[0].VpcId))

	// VPN connection with static routing.
	connOut, err := c.CreateVpnConnection(ctx, &ec2.CreateVpnConnectionInput{
		CustomerGatewayId: aws.String(cgwID),
		VpnGatewayId:      aws.String(vgwID),
		Type:              aws.String("ipsec.1"),
		Options:           &types.VpnConnectionOptionsSpecification{StaticRoutesOnly: aws.Bool(true)},
	})
	require.NoError(t, err)
	conn := connOut.VpnConnection
	require.NotNil(t, conn)
	connID := aws.ToString(conn.VpnConnectionId)
	require.NotEmpty(t, connID)
	assert.Equal(t, cgwID, aws.ToString(conn.CustomerGatewayId))
	assert.NotEmpty(t, aws.ToString(conn.CustomerGatewayConfiguration), "tunnel config payload")
	require.Len(t, conn.VgwTelemetry, 2, "two IPsec tunnels")
	assert.NotEmpty(t, aws.ToString(conn.VgwTelemetry[0].OutsideIpAddress))
	require.NotNil(t, conn.Options)
	assert.True(t, aws.ToBool(conn.Options.StaticRoutesOnly))

	t.Cleanup(func() {
		_, _ = c.DeleteVpnConnection(ctx, &ec2.DeleteVpnConnectionInput{VpnConnectionId: aws.String(connID)})
	})

	// Static route add + read-back.
	_, err = c.CreateVpnConnectionRoute(ctx, &ec2.CreateVpnConnectionRouteInput{
		VpnConnectionId: aws.String(connID), DestinationCidrBlock: aws.String("172.16.0.0/16"),
	})
	require.NoError(t, err)

	dconn, err := c.DescribeVpnConnections(ctx, &ec2.DescribeVpnConnectionsInput{VpnConnectionIds: []string{connID}})
	require.NoError(t, err)
	require.Len(t, dconn.VpnConnections, 1)
	require.Len(t, dconn.VpnConnections[0].Routes, 1)
	assert.Equal(t, "172.16.0.0/16", aws.ToString(dconn.VpnConnections[0].Routes[0].DestinationCidrBlock))

	// Modify options + tunnel options.
	_, err = c.ModifyVpnConnectionOptions(ctx, &ec2.ModifyVpnConnectionOptionsInput{
		VpnConnectionId:       aws.String(connID),
		LocalIpv4NetworkCidr:  aws.String("10.0.0.0/16"),
		RemoteIpv4NetworkCidr: aws.String("192.168.0.0/16"),
	})
	require.NoError(t, err)

	tunOut, err := c.ModifyVpnTunnelOptions(ctx, &ec2.ModifyVpnTunnelOptionsInput{
		VpnConnectionId:           aws.String(connID),
		VpnTunnelOutsideIpAddress: aws.String(aws.ToString(conn.VgwTelemetry[0].OutsideIpAddress)),
		TunnelOptions:             &types.ModifyVpnTunnelOptionsSpecification{TunnelInsideCidr: aws.String("169.254.20.0/30")},
	})
	require.NoError(t, err)
	require.NotNil(t, tunOut.VpnConnection)

	// Active tunnel status.
	st, err := c.GetActiveVpnTunnelStatus(ctx, &ec2.GetActiveVpnTunnelStatusInput{
		VpnConnectionId:           aws.String(connID),
		VpnTunnelOutsideIpAddress: conn.VgwTelemetry[0].OutsideIpAddress,
	})
	require.NoError(t, err)
	require.NotNil(t, st.ActiveVpnTunnelStatus)
	assert.NotEmpty(t, st.ActiveVpnTunnelStatus.Phase1EncryptionAlgorithm)

	// Modify the connection (re-point to the same gateway is a no-op-shaped path).
	_, err = c.ModifyVpnConnection(ctx, &ec2.ModifyVpnConnectionInput{
		VpnConnectionId: aws.String(connID), VpnGatewayId: aws.String(vgwID),
	})
	require.NoError(t, err)

	// Route delete.
	_, err = c.DeleteVpnConnectionRoute(ctx, &ec2.DeleteVpnConnectionRouteInput{
		VpnConnectionId: aws.String(connID), DestinationCidrBlock: aws.String("172.16.0.0/16"),
	})
	require.NoError(t, err)

	// Device-type catalog + sample configuration.
	devs, err := c.GetVpnConnectionDeviceTypes(ctx, &ec2.GetVpnConnectionDeviceTypesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, devs.VpnConnectionDeviceTypes)
	assert.NotEmpty(t, aws.ToString(devs.VpnConnectionDeviceTypes[0].VpnConnectionDeviceTypeId))
	assert.NotEmpty(t, aws.ToString(devs.VpnConnectionDeviceTypes[0].Vendor))

	sample, err := c.GetVpnConnectionDeviceSampleConfiguration(ctx, &ec2.GetVpnConnectionDeviceSampleConfigurationInput{
		VpnConnectionId:           aws.String(connID),
		VpnConnectionDeviceTypeId: devs.VpnConnectionDeviceTypes[0].VpnConnectionDeviceTypeId,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(sample.VpnConnectionDeviceSampleConfiguration))
}

// TestEC2_ClientVPNLifecycle covers the Client VPN family: endpoint create with
// mutual authentication, the Describe read-back, ModifyClientVpnEndpoint, routes,
// authorization (ingress) rules, target-network associations,
// ApplySecurityGroupsToClientVpnTargetNetwork, the connection list +
// terminate, and a tolerant teardown.
func TestEC2_ClientVPNLifecycle(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.210.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.210.1.0/24")
	subnetID := aws.ToString(subnet)

	epOut, err := c.CreateClientVpnEndpoint(ctx, &ec2.CreateClientVpnEndpointInput{
		ClientCidrBlock:      aws.String("10.250.0.0/22"),
		ServerCertificateArn: aws.String("arn:aws:acm:us-east-1:123456789012:certificate/abc"),
		AuthenticationOptions: []types.ClientVpnAuthenticationRequest{{
			Type: types.ClientVpnAuthenticationTypeCertificateAuthentication,
			MutualAuthentication: &types.CertificateAuthenticationRequest{
				ClientRootCertificateChainArn: aws.String("arn:aws:acm:us-east-1:123456789012:certificate/root"),
			},
		}},
		ConnectionLogOptions: &types.ConnectionLogOptions{Enabled: aws.Bool(false)},
		Description:          aws.String("dev client vpn"),
		TransportProtocol:    types.TransportProtocolUdp,
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeClientVpnEndpoint,
			Tags:         []types.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
		}},
	})
	require.NoError(t, err)
	epID := aws.ToString(epOut.ClientVpnEndpointId)
	require.NotEmpty(t, epID)
	require.NotNil(t, epOut.Status)

	t.Cleanup(func() {
		_, _ = c.DeleteClientVpnEndpoint(ctx, &ec2.DeleteClientVpnEndpointInput{ClientVpnEndpointId: aws.String(epID)})
	})

	dep, err := c.DescribeClientVpnEndpoints(ctx, &ec2.DescribeClientVpnEndpointsInput{
		ClientVpnEndpointIds: []string{epID},
	})
	require.NoError(t, err)
	require.Len(t, dep.ClientVpnEndpoints, 1)
	got := dep.ClientVpnEndpoints[0]
	assert.Equal(t, "10.250.0.0/22", aws.ToString(got.ClientCidrBlock))
	assert.Equal(t, "dev client vpn", aws.ToString(got.Description))
	require.Len(t, got.AuthenticationOptions, 1)
	require.Len(t, got.Tags, 1)
	assert.Equal(t, "team", aws.ToString(got.Tags[0].Key))

	// Modify.
	_, err = c.ModifyClientVpnEndpoint(ctx, &ec2.ModifyClientVpnEndpointInput{
		ClientVpnEndpointId: aws.String(epID),
		Description:         aws.String("updated description"),
		SplitTunnel:         aws.Bool(true),
	})
	require.NoError(t, err)

	// Associate a target network (subnet).
	assoc, err := c.AssociateClientVpnTargetNetwork(ctx, &ec2.AssociateClientVpnTargetNetworkInput{
		ClientVpnEndpointId: aws.String(epID), SubnetId: aws.String(subnetID),
	})
	require.NoError(t, err)
	assocID := aws.ToString(assoc.AssociationId)
	require.NotEmpty(t, assocID)

	dtn, err := c.DescribeClientVpnTargetNetworks(ctx, &ec2.DescribeClientVpnTargetNetworksInput{
		ClientVpnEndpointId: aws.String(epID),
	})
	require.NoError(t, err)
	require.Len(t, dtn.ClientVpnTargetNetworks, 1)
	assert.Equal(t, subnetID, aws.ToString(dtn.ClientVpnTargetNetworks[0].TargetNetworkId))

	// Authorization (ingress) rule.
	_, err = c.AuthorizeClientVpnIngress(ctx, &ec2.AuthorizeClientVpnIngressInput{
		ClientVpnEndpointId: aws.String(epID),
		TargetNetworkCidr:   aws.String("0.0.0.0/0"),
		AuthorizeAllGroups:  aws.Bool(true),
		Description:         aws.String("allow all"),
	})
	require.NoError(t, err)

	dar, err := c.DescribeClientVpnAuthorizationRules(ctx, &ec2.DescribeClientVpnAuthorizationRulesInput{
		ClientVpnEndpointId: aws.String(epID),
	})
	require.NoError(t, err)
	require.Len(t, dar.AuthorizationRules, 1)
	assert.Equal(t, "0.0.0.0/0", aws.ToString(dar.AuthorizationRules[0].DestinationCidr))
	assert.True(t, aws.ToBool(dar.AuthorizationRules[0].AccessAll))

	// Route (depends on the association being in place).
	_, err = c.CreateClientVpnRoute(ctx, &ec2.CreateClientVpnRouteInput{
		ClientVpnEndpointId:  aws.String(epID),
		DestinationCidrBlock: aws.String("0.0.0.0/0"),
		TargetVpcSubnetId:    aws.String(subnetID),
		Description:          aws.String("default route"),
	})
	require.NoError(t, err)

	drt, err := c.DescribeClientVpnRoutes(ctx, &ec2.DescribeClientVpnRoutesInput{
		ClientVpnEndpointId: aws.String(epID),
	})
	require.NoError(t, err)
	require.Len(t, drt.Routes, 1)
	assert.Equal(t, "0.0.0.0/0", aws.ToString(drt.Routes[0].DestinationCidr))
	assert.Equal(t, subnetID, aws.ToString(drt.Routes[0].TargetSubnet))

	// Apply security groups to the target network.
	sg, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("clientvpn-sg"), Description: aws.String("cvpn"), VpcId: vpc.Vpc.VpcId,
	})
	require.NoError(t, err)
	applied, err := c.ApplySecurityGroupsToClientVpnTargetNetwork(ctx, &ec2.ApplySecurityGroupsToClientVpnTargetNetworkInput{
		ClientVpnEndpointId: aws.String(epID),
		VpcId:               vpc.Vpc.VpcId,
		SecurityGroupIds:    []string{aws.ToString(sg.GroupId)},
	})
	require.NoError(t, err)
	require.Contains(t, applied.SecurityGroupIds, aws.ToString(sg.GroupId))

	// Connections list (empty for a freshly created endpoint) + terminate.
	conns, err := c.DescribeClientVpnConnections(ctx, &ec2.DescribeClientVpnConnectionsInput{
		ClientVpnEndpointId: aws.String(epID),
	})
	require.NoError(t, err)
	assert.Empty(t, conns.Connections)

	term, err := c.TerminateClientVpnConnections(ctx, &ec2.TerminateClientVpnConnectionsInput{
		ClientVpnEndpointId: aws.String(epID), Username: aws.String("alice"),
	})
	require.NoError(t, err)
	assert.Equal(t, epID, aws.ToString(term.ClientVpnEndpointId))

	// Tear the routes/rules/associations down (tolerant).
	_, _ = c.DeleteClientVpnRoute(ctx, &ec2.DeleteClientVpnRouteInput{
		ClientVpnEndpointId: aws.String(epID), DestinationCidrBlock: aws.String("0.0.0.0/0"), TargetVpcSubnetId: aws.String(subnetID),
	})
	_, _ = c.RevokeClientVpnIngress(ctx, &ec2.RevokeClientVpnIngressInput{
		ClientVpnEndpointId: aws.String(epID), TargetNetworkCidr: aws.String("0.0.0.0/0"), RevokeAllGroups: aws.Bool(true),
	})
	_, _ = c.DisassociateClientVpnTargetNetwork(ctx, &ec2.DisassociateClientVpnTargetNetworkInput{
		ClientVpnEndpointId: aws.String(epID), AssociationId: aws.String(assocID),
	})
}
