package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_VpcEndpointServiceRoundTrip exercises the PrivateLink endpoint-service
// control plane: CreateVpcEndpointServiceConfiguration, Describe…Configurations,
// Modify…Configuration, the allowed-principal permission set
// (Modify/Describe…Permissions), payer responsibility, private-DNS verification,
// DescribeVpcEndpointServices, and the tolerant delete unsuccessful set.
func TestEC2_VpcEndpointServiceRoundTrip(t *testing.T) {
	c := ec2Client()

	const nlbArn = "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/net/vpes/abc"
	create, err := c.CreateVpcEndpointServiceConfiguration(ctx, &ec2.CreateVpcEndpointServiceConfigurationInput{
		AcceptanceRequired:      aws.Bool(true),
		NetworkLoadBalancerArns: []string{nlbArn},
		PrivateDnsName:          aws.String("svc.example.com"),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeVpcEndpointService,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("vpes")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, create.ServiceConfiguration)
	cfg := create.ServiceConfiguration
	svcID := aws.ToString(cfg.ServiceId)
	require.NotEmpty(t, svcID)
	assert.NotEmpty(t, aws.ToString(cfg.ServiceName))
	assert.Equal(t, types.ServiceStateAvailable, cfg.ServiceState)
	assert.True(t, aws.ToBool(cfg.AcceptanceRequired))
	assert.Contains(t, cfg.NetworkLoadBalancerArns, nlbArn)
	assert.Equal(t, "svc.example.com", aws.ToString(cfg.PrivateDnsName))

	// Describe by id.
	desc, err := c.DescribeVpcEndpointServiceConfigurations(ctx, &ec2.DescribeVpcEndpointServiceConfigurationsInput{
		ServiceIds: []string{svcID},
	})
	require.NoError(t, err)
	require.Len(t, desc.ServiceConfigurations, 1)
	assert.Equal(t, svcID, aws.ToString(desc.ServiceConfigurations[0].ServiceId))

	// Modify: flip AcceptanceRequired off.
	_, err = c.ModifyVpcEndpointServiceConfiguration(ctx, &ec2.ModifyVpcEndpointServiceConfigurationInput{
		ServiceId:          aws.String(svcID),
		AcceptanceRequired: aws.Bool(false),
	})
	require.NoError(t, err)
	after, err := c.DescribeVpcEndpointServiceConfigurations(ctx, &ec2.DescribeVpcEndpointServiceConfigurationsInput{
		ServiceIds: []string{svcID},
	})
	require.NoError(t, err)
	require.Len(t, after.ServiceConfigurations, 1)
	assert.False(t, aws.ToBool(after.ServiceConfigurations[0].AcceptanceRequired))

	// Permissions: add a principal, read it back, remove it.
	const principal = "arn:aws:iam::111122223333:root"
	mp, err := c.ModifyVpcEndpointServicePermissions(ctx, &ec2.ModifyVpcEndpointServicePermissionsInput{
		ServiceId:            aws.String(svcID),
		AddAllowedPrincipals: []string{principal},
	})
	require.NoError(t, err)
	require.Len(t, mp.AddedPrincipals, 1)
	assert.Equal(t, principal, aws.ToString(mp.AddedPrincipals[0].Principal))

	perms, err := c.DescribeVpcEndpointServicePermissions(ctx, &ec2.DescribeVpcEndpointServicePermissionsInput{
		ServiceId: aws.String(svcID),
	})
	require.NoError(t, err)
	require.Len(t, perms.AllowedPrincipals, 1)
	assert.Equal(t, principal, aws.ToString(perms.AllowedPrincipals[0].Principal))

	_, err = c.ModifyVpcEndpointServicePermissions(ctx, &ec2.ModifyVpcEndpointServicePermissionsInput{
		ServiceId:               aws.String(svcID),
		RemoveAllowedPrincipals: []string{principal},
	})
	require.NoError(t, err)
	permsGone, err := c.DescribeVpcEndpointServicePermissions(ctx, &ec2.DescribeVpcEndpointServicePermissionsInput{
		ServiceId: aws.String(svcID),
	})
	require.NoError(t, err)
	assert.Empty(t, permsGone.AllowedPrincipals)

	// Payer responsibility + private-DNS verification.
	_, err = c.ModifyVpcEndpointServicePayerResponsibility(ctx, &ec2.ModifyVpcEndpointServicePayerResponsibilityInput{
		ServiceId:           aws.String(svcID),
		PayerResponsibility: types.PayerResponsibilityServiceOwner,
	})
	require.NoError(t, err)
	_, err = c.StartVpcEndpointServicePrivateDnsVerification(ctx, &ec2.StartVpcEndpointServicePrivateDnsVerificationInput{
		ServiceId: aws.String(svcID),
	})
	require.NoError(t, err)

	// DescribeVpcEndpointServices lists the configured service name + detail.
	svcName := aws.ToString(cfg.ServiceName)
	svcs, err := c.DescribeVpcEndpointServices(ctx, &ec2.DescribeVpcEndpointServicesInput{
		ServiceNames: []string{svcName},
	})
	require.NoError(t, err)
	assert.Contains(t, svcs.ServiceNames, svcName)
	require.NotEmpty(t, svcs.ServiceDetails)
	assert.Equal(t, svcName, aws.ToString(svcs.ServiceDetails[0].ServiceName))

	// Delete (tolerant): successful delete leaves an empty unsuccessful set; an
	// unknown id lands in the unsuccessful set rather than erroring.
	del, err := c.DeleteVpcEndpointServiceConfigurations(ctx, &ec2.DeleteVpcEndpointServiceConfigurationsInput{
		ServiceIds: []string{svcID},
	})
	require.NoError(t, err)
	assert.Empty(t, del.Unsuccessful)

	bad, err := c.DeleteVpcEndpointServiceConfigurations(ctx, &ec2.DeleteVpcEndpointServiceConfigurationsInput{
		ServiceIds: []string{"vpce-svc-doesnotexist"},
	})
	require.NoError(t, err)
	require.Len(t, bad.Unsuccessful, 1)
	assert.Equal(t, "vpce-svc-doesnotexist", aws.ToString(bad.Unsuccessful[0].ResourceId))
}

// TestEC2_VpcCidrBlockRoundTrip exercises AssociateVpcCidrBlock /
// DisassociateVpcCidrBlock — adding and removing a secondary IPv4 CIDR on a VPC.
func TestEC2_VpcCidrBlockRoundTrip(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.20.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)

	assoc, err := c.AssociateVpcCidrBlock(ctx, &ec2.AssociateVpcCidrBlockInput{
		VpcId:     aws.String(vpcID),
		CidrBlock: aws.String("10.21.0.0/16"),
	})
	require.NoError(t, err)
	require.NotNil(t, assoc.CidrBlockAssociation)
	assocID := aws.ToString(assoc.CidrBlockAssociation.AssociationId)
	require.NotEmpty(t, assocID)
	assert.Equal(t, "10.21.0.0/16", aws.ToString(assoc.CidrBlockAssociation.CidrBlock))
	require.NotNil(t, assoc.CidrBlockAssociation.CidrBlockState)
	assert.Equal(t, types.VpcCidrBlockStateCodeAssociated, assoc.CidrBlockAssociation.CidrBlockState.State)
	assert.Equal(t, vpcID, aws.ToString(assoc.VpcId))

	disassoc, err := c.DisassociateVpcCidrBlock(ctx, &ec2.DisassociateVpcCidrBlockInput{
		AssociationId: aws.String(assocID),
	})
	require.NoError(t, err)
	require.NotNil(t, disassoc.CidrBlockAssociation)
	assert.Equal(t, assocID, aws.ToString(disassoc.CidrBlockAssociation.AssociationId))
}

// TestEC2_SubnetCidrRoundTrip exercises AssociateSubnetCidrBlock (IPv6),
// DisassociateSubnetCidrBlock, and the subnet CIDR reservation ops
// (Create/Get/Delete).
func TestEC2_SubnetCidrRoundTrip(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.30.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	sub, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: aws.String(vpcID), CidrBlock: aws.String("10.30.1.0/24"),
	})
	require.NoError(t, err)
	subID := aws.ToString(sub.Subnet.SubnetId)

	assoc, err := c.AssociateSubnetCidrBlock(ctx, &ec2.AssociateSubnetCidrBlockInput{
		SubnetId:      aws.String(subID),
		Ipv6CidrBlock: aws.String("2600:1f00:abcd:1::/64"),
	})
	require.NoError(t, err)
	require.NotNil(t, assoc.Ipv6CidrBlockAssociation)
	subAssocID := aws.ToString(assoc.Ipv6CidrBlockAssociation.AssociationId)
	require.NotEmpty(t, subAssocID)
	assert.Equal(t, subID, aws.ToString(assoc.SubnetId))

	_, err = c.DisassociateSubnetCidrBlock(ctx, &ec2.DisassociateSubnetCidrBlockInput{
		AssociationId: aws.String(subAssocID),
	})
	require.NoError(t, err)

	// Subnet CIDR reservation.
	res, err := c.CreateSubnetCidrReservation(ctx, &ec2.CreateSubnetCidrReservationInput{
		SubnetId:        aws.String(subID),
		Cidr:            aws.String("10.30.1.16/28"),
		ReservationType: types.SubnetCidrReservationTypePrefix,
		Description:     aws.String("res"),
	})
	require.NoError(t, err)
	require.NotNil(t, res.SubnetCidrReservation)
	resID := aws.ToString(res.SubnetCidrReservation.SubnetCidrReservationId)
	require.NotEmpty(t, resID)
	assert.Equal(t, "10.30.1.16/28", aws.ToString(res.SubnetCidrReservation.Cidr))
	assert.Equal(t, types.SubnetCidrReservationTypePrefix, res.SubnetCidrReservation.ReservationType)

	get, err := c.GetSubnetCidrReservations(ctx, &ec2.GetSubnetCidrReservationsInput{
		SubnetId: aws.String(subID),
	})
	require.NoError(t, err)
	require.Len(t, get.SubnetIpv4CidrReservations, 1)
	assert.Equal(t, resID, aws.ToString(get.SubnetIpv4CidrReservations[0].SubnetCidrReservationId))

	del, err := c.DeleteSubnetCidrReservation(ctx, &ec2.DeleteSubnetCidrReservationInput{
		SubnetCidrReservationId: aws.String(resID),
	})
	require.NoError(t, err)
	require.NotNil(t, del.DeletedSubnetCidrReservation)
	assert.Equal(t, resID, aws.ToString(del.DeletedSubnetCidrReservation.SubnetCidrReservationId))
}

// TestEC2_SecondaryNetworkRoundTrip exercises Create/Describe/Delete
// SecondaryNetwork and its nested secondary subnets.
func TestEC2_SecondaryNetworkRoundTrip(t *testing.T) {
	c := ec2Client()

	sn, err := c.CreateSecondaryNetwork(ctx, &ec2.CreateSecondaryNetworkInput{
		Ipv4CidrBlock: aws.String("172.31.0.0/16"),
		NetworkType:   types.SecondaryNetworkTypeRdma,
	})
	require.NoError(t, err)
	require.NotNil(t, sn.SecondaryNetwork)
	snID := aws.ToString(sn.SecondaryNetwork.SecondaryNetworkId)
	require.NotEmpty(t, snID)
	assert.Equal(t, types.SecondaryNetworkTypeRdma, sn.SecondaryNetwork.Type)
	require.NotEmpty(t, sn.SecondaryNetwork.Ipv4CidrBlockAssociations)
	assert.Equal(t, "172.31.0.0/16", aws.ToString(sn.SecondaryNetwork.Ipv4CidrBlockAssociations[0].CidrBlock))

	desc, err := c.DescribeSecondaryNetworks(ctx, &ec2.DescribeSecondaryNetworksInput{
		SecondaryNetworkIds: []string{snID},
	})
	require.NoError(t, err)
	require.Len(t, desc.SecondaryNetworks, 1)
	assert.Equal(t, snID, aws.ToString(desc.SecondaryNetworks[0].SecondaryNetworkId))

	ss, err := c.CreateSecondarySubnet(ctx, &ec2.CreateSecondarySubnetInput{
		SecondaryNetworkId: aws.String(snID),
		Ipv4CidrBlock:      aws.String("172.31.1.0/24"),
		AvailabilityZone:   aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	require.NotNil(t, ss.SecondarySubnet)
	ssID := aws.ToString(ss.SecondarySubnet.SecondarySubnetId)
	require.NotEmpty(t, ssID)
	assert.Equal(t, snID, aws.ToString(ss.SecondarySubnet.SecondaryNetworkId))

	descSub, err := c.DescribeSecondarySubnets(ctx, &ec2.DescribeSecondarySubnetsInput{
		SecondarySubnetIds: []string{ssID},
	})
	require.NoError(t, err)
	require.Len(t, descSub.SecondarySubnets, 1)
	assert.Equal(t, ssID, aws.ToString(descSub.SecondarySubnets[0].SecondarySubnetId))

	_, err = c.DeleteSecondarySubnet(ctx, &ec2.DeleteSecondarySubnetInput{
		SecondarySubnetId: aws.String(ssID),
	})
	require.NoError(t, err)
	_, err = c.DeleteSecondaryNetwork(ctx, &ec2.DeleteSecondaryNetworkInput{
		SecondaryNetworkId: aws.String(snID),
	})
	require.NoError(t, err)
}

// TestEC2_SecurityGroupVpcRoundTrip exercises Associate/Disassociate/Describe
// SecurityGroupVpc — attaching a security group to an additional VPC.
func TestEC2_SecurityGroupVpcRoundTrip(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.40.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	other, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.41.0.0/16")})
	require.NoError(t, err)
	otherVpcID := aws.ToString(other.Vpc.VpcId)
	sg, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("sgvpc"), Description: aws.String("sgvpc"), VpcId: aws.String(vpcID),
	})
	require.NoError(t, err)
	sgID := aws.ToString(sg.GroupId)

	a, err := c.AssociateSecurityGroupVpc(ctx, &ec2.AssociateSecurityGroupVpcInput{
		GroupId: aws.String(sgID), VpcId: aws.String(otherVpcID),
	})
	require.NoError(t, err)
	assert.Equal(t, types.SecurityGroupVpcAssociationStateAssociated, a.State)

	desc, err := c.DescribeSecurityGroupVpcAssociations(ctx, &ec2.DescribeSecurityGroupVpcAssociationsInput{
		Filters: []types.Filter{{Name: aws.String("group-id"), Values: []string{sgID}}},
	})
	require.NoError(t, err)
	require.Len(t, desc.SecurityGroupVpcAssociations, 1)
	assert.Equal(t, sgID, aws.ToString(desc.SecurityGroupVpcAssociations[0].GroupId))
	assert.Equal(t, otherVpcID, aws.ToString(desc.SecurityGroupVpcAssociations[0].VpcId))
	assert.Equal(t, types.SecurityGroupVpcAssociationStateAssociated, desc.SecurityGroupVpcAssociations[0].State)

	_, err = c.DisassociateSecurityGroupVpc(ctx, &ec2.DisassociateSecurityGroupVpcInput{
		GroupId: aws.String(sgID), VpcId: aws.String(otherVpcID),
	})
	require.NoError(t, err)
	gone, err := c.DescribeSecurityGroupVpcAssociations(ctx, &ec2.DescribeSecurityGroupVpcAssociationsInput{
		Filters: []types.Filter{{Name: aws.String("group-id"), Values: []string{sgID}}},
	})
	require.NoError(t, err)
	assert.Empty(t, gone.SecurityGroupVpcAssociations)
}

// TestEC2_VpcEncryptionControlRoundTrip exercises Create/Modify/Describe/Delete
// VpcEncryptionControl — a per-VPC enforce/monitor mode with resource exclusions.
func TestEC2_VpcEncryptionControlRoundTrip(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.50.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)

	ctrl, err := c.CreateVpcEncryptionControl(ctx, &ec2.CreateVpcEncryptionControlInput{
		VpcId: aws.String(vpcID),
	})
	require.NoError(t, err)
	require.NotNil(t, ctrl.VpcEncryptionControl)
	ctrlID := aws.ToString(ctrl.VpcEncryptionControl.VpcEncryptionControlId)
	require.NotEmpty(t, ctrlID)
	assert.Equal(t, vpcID, aws.ToString(ctrl.VpcEncryptionControl.VpcId))
	assert.Equal(t, types.VpcEncryptionControlModeMonitor, ctrl.VpcEncryptionControl.Mode)

	mod, err := c.ModifyVpcEncryptionControl(ctx, &ec2.ModifyVpcEncryptionControlInput{
		VpcEncryptionControlId: aws.String(ctrlID),
		Mode:                   types.VpcEncryptionControlModeEnforce,
		NatGatewayExclusion:    types.VpcEncryptionControlExclusionStateInputEnable,
	})
	require.NoError(t, err)
	require.NotNil(t, mod.VpcEncryptionControl)
	assert.Equal(t, types.VpcEncryptionControlModeEnforce, mod.VpcEncryptionControl.Mode)
	require.NotNil(t, mod.VpcEncryptionControl.ResourceExclusions)
	require.NotNil(t, mod.VpcEncryptionControl.ResourceExclusions.NatGateway)
	assert.Equal(t, types.VpcEncryptionControlExclusionStateEnabled, mod.VpcEncryptionControl.ResourceExclusions.NatGateway.State)

	desc, err := c.DescribeVpcEncryptionControls(ctx, &ec2.DescribeVpcEncryptionControlsInput{
		VpcEncryptionControlIds: []string{ctrlID},
	})
	require.NoError(t, err)
	require.Len(t, desc.VpcEncryptionControls, 1)
	assert.Equal(t, ctrlID, aws.ToString(desc.VpcEncryptionControls[0].VpcEncryptionControlId))

	del, err := c.DeleteVpcEncryptionControl(ctx, &ec2.DeleteVpcEncryptionControlInput{
		VpcEncryptionControlId: aws.String(ctrlID),
	})
	require.NoError(t, err)
	require.NotNil(t, del.VpcEncryptionControl)
	assert.Equal(t, ctrlID, aws.ToString(del.VpcEncryptionControl.VpcEncryptionControlId))
}

// TestEC2_AccountVpcEncryptionAndEndpointPayerRoundTrip drives the account-level
// Amazon VPC encryption policy and the per-endpoint AWS PrivateLink payer
// lifecycle through the official Amazon EC2 SDK.
func TestEC2_AccountVpcEncryptionAndEndpointPayerRoundTrip(t *testing.T) {
	c := ec2Client()

	explicit, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String("10.54.0.0/16"),
		VpcEncryptionControl: &types.VpcEncryptionControlConfiguration{
			Mode:                     types.VpcEncryptionControlModeEnforce,
			InternetGatewayExclusion: types.VpcEncryptionControlExclusionStateInputEnable,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, explicit.Vpc.EncryptionControl)
	assert.Equal(t, types.VpcEncryptionControlModeEnforce, explicit.Vpc.EncryptionControl.Mode)
	require.NotNil(t, explicit.Vpc.EncryptionControl.ResourceExclusions)
	require.NotNil(t, explicit.Vpc.EncryptionControl.ResourceExclusions.InternetGateway)
	assert.Equal(t, types.VpcEncryptionControlExclusionStateEnabled,
		explicit.Vpc.EncryptionControl.ResourceExclusions.InternetGateway.State)

	initial, err := c.DescribeAccountVpcEncryptionControl(ctx, &ec2.DescribeAccountVpcEncryptionControlInput{})
	require.NoError(t, err)
	require.NotNil(t, initial.AccountVpcEncryptionControl)
	assert.Equal(t, types.AccountVpcEncryptionControlStateDefaultState, initial.AccountVpcEncryptionControl.State)
	assert.Equal(t, types.AccountVpcEncryptionControlModeUnmanaged, initial.AccountVpcEncryptionControl.Mode)
	assert.Equal(t, types.ManagedByAccount, initial.AccountVpcEncryptionControl.ManagedBy)

	modified, err := c.ModifyAccountVpcEncryptionControl(ctx, &ec2.ModifyAccountVpcEncryptionControlInput{
		Mode:       types.AccountVpcEncryptionControlModeAttemptMonitor,
		NatGateway: types.VpcEncryptionControlExclusionStateInputEnable,
	})
	require.NoError(t, err)
	require.NotNil(t, modified.AccountVpcEncryptionControl)
	assert.Equal(t, types.AccountVpcEncryptionControlStateTransitionsSuccessful, modified.AccountVpcEncryptionControl.State)
	assert.Equal(t, types.AccountVpcEncryptionControlModeAttemptMonitor, modified.AccountVpcEncryptionControl.Mode)
	require.NotNil(t, modified.AccountVpcEncryptionControl.Exclusions)
	assert.Equal(t, types.VpcEncryptionControlExclusionStateEnabled, modified.AccountVpcEncryptionControl.Exclusions.NatGateway)
	require.NotNil(t, modified.AccountVpcEncryptionControl.LastUpdateTimestamp)

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.55.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	controls, err := c.DescribeVpcEncryptionControls(ctx, &ec2.DescribeVpcEncryptionControlsInput{
		VpcIds: []string{vpcID},
	})
	require.NoError(t, err)
	require.Len(t, controls.VpcEncryptionControls, 1)
	assert.Equal(t, types.VpcEncryptionControlModeMonitor, controls.VpcEncryptionControls[0].Mode)
	assert.Nil(t, controls.VpcEncryptionControls[0].ResourceExclusions)

	service, err := c.CreateVpcEndpointServiceConfiguration(ctx, &ec2.CreateVpcEndpointServiceConfigurationInput{
		AcceptanceRequired:      aws.Bool(true),
		NetworkLoadBalancerArns: []string{"arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/net/payer/abc"},
	})
	require.NoError(t, err)
	serviceID := aws.ToString(service.ServiceConfiguration.ServiceId)
	endpoint, err := c.CreateVpcEndpoint(ctx, &ec2.CreateVpcEndpointInput{
		VpcId:           aws.String(vpcID),
		ServiceName:     service.ServiceConfiguration.ServiceName,
		VpcEndpointType: types.VpcEndpointTypeInterface,
	})
	require.NoError(t, err)
	endpointID := aws.ToString(endpoint.VpcEndpoint.VpcEndpointId)
	assert.Equal(t, types.StatePendingAcceptance, endpoint.VpcEndpoint.State)
	require.Len(t, endpoint.VpcEndpoint.PayerResponsibilities, 1)
	assert.Equal(t, types.PayerResponsibilityTypeVpcEndpointAccount, endpoint.VpcEndpoint.PayerResponsibilities[0].PayerResponsibilityType)

	payer, err := c.ModifyVpcEndpointPayerResponsibility(ctx, &ec2.ModifyVpcEndpointPayerResponsibilityInput{
		ServiceId:           aws.String(serviceID),
		VpcEndpointId:       aws.String(endpointID),
		Scope:               types.PayerResponsibilityScopeVpcEndpointCharges,
		PayerResponsibility: types.PayerResponsibilityTypeVpcEndpointServiceAccount,
	})
	require.NoError(t, err)
	assert.Equal(t, endpointID, aws.ToString(payer.VpcEndpointId))
	require.Len(t, payer.PayerResponsibilities, 1)
	assert.Equal(t, types.PayerResponsibilityTypeVpcEndpointServiceAccount, payer.PayerResponsibilities[0].PayerResponsibilityType)

	describedEndpoint, err := c.DescribeVpcEndpoints(ctx, &ec2.DescribeVpcEndpointsInput{
		VpcEndpointIds: []string{endpointID},
	})
	require.NoError(t, err)
	require.Len(t, describedEndpoint.VpcEndpoints, 1)
	require.Len(t, describedEndpoint.VpcEndpoints[0].PayerResponsibilities, 1)
	assert.Equal(t, types.PayerResponsibilityTypeVpcEndpointServiceAccount,
		describedEndpoint.VpcEndpoints[0].PayerResponsibilities[0].PayerResponsibilityType)

	connections, err := c.DescribeVpcEndpointConnections(ctx, &ec2.DescribeVpcEndpointConnectionsInput{
		Filters: []types.Filter{{Name: aws.String("service-id"), Values: []string{serviceID}}},
	})
	require.NoError(t, err)
	require.Len(t, connections.VpcEndpointConnections, 1)
	assert.Equal(t, types.StatePendingAcceptance, connections.VpcEndpointConnections[0].VpcEndpointState)
	require.Len(t, connections.VpcEndpointConnections[0].PayerResponsibilities, 1)
	assert.Equal(t, types.PayerResponsibilityTypeVpcEndpointServiceAccount,
		connections.VpcEndpointConnections[0].PayerResponsibilities[0].PayerResponsibilityType)

	accepted, err := c.AcceptVpcEndpointConnections(ctx, &ec2.AcceptVpcEndpointConnectionsInput{
		ServiceId:      aws.String(serviceID),
		VpcEndpointIds: []string{endpointID},
	})
	require.NoError(t, err)
	assert.Empty(t, accepted.Unsuccessful)

	acceptedEndpoint, err := c.DescribeVpcEndpoints(ctx, &ec2.DescribeVpcEndpointsInput{
		VpcEndpointIds: []string{endpointID},
	})
	require.NoError(t, err)
	require.Len(t, acceptedEndpoint.VpcEndpoints, 1)
	assert.Equal(t, types.StateAvailable, acceptedEndpoint.VpcEndpoints[0].State)
	acceptedConnections, err := c.DescribeVpcEndpointConnections(ctx, &ec2.DescribeVpcEndpointConnectionsInput{
		Filters: []types.Filter{{Name: aws.String("service-id"), Values: []string{serviceID}}},
	})
	require.NoError(t, err)
	require.Len(t, acceptedConnections.VpcEndpointConnections, 1)
	assert.Equal(t, types.StateAvailable, acceptedConnections.VpcEndpointConnections[0].VpcEndpointState)
}
