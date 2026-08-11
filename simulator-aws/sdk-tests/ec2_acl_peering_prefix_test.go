package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_NetworkAclLifecycleSDK exercises the network ACL control plane:
// CreateNetworkAcl, CreateNetworkAclEntry (with a port range), ReplaceNetworkAclEntry,
// DescribeNetworkAcls read-back of the entries, DeleteNetworkAclEntry, and
// DeleteNetworkAcl.
func TestEC2_NetworkAclLifecycleSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.200.0.0/16")})
	require.NoError(t, err)
	vpcID := vpc.Vpc.VpcId
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpcID}) }()

	created, err := c.CreateNetworkAcl(ctx, &ec2.CreateNetworkAclInput{
		VpcId: vpcID,
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeNetworkAcl,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("acl-a")}},
		}},
	})
	require.NoError(t, err)
	aclID := created.NetworkAcl.NetworkAclId
	require.NotEmpty(t, aws.ToString(aclID))
	assert.Equal(t, aws.ToString(vpcID), aws.ToString(created.NetworkAcl.VpcId))
	defer func() { _, _ = c.DeleteNetworkAcl(ctx, &ec2.DeleteNetworkAclInput{NetworkAclId: aclID}) }()

	_, err = c.CreateNetworkAclEntry(ctx, &ec2.CreateNetworkAclEntryInput{
		NetworkAclId: aclID,
		RuleNumber:   aws.Int32(100),
		Protocol:     aws.String("6"),
		RuleAction:   types.RuleActionAllow,
		Egress:       aws.Bool(false),
		CidrBlock:    aws.String("0.0.0.0/0"),
		PortRange:    &types.PortRange{From: aws.Int32(80), To: aws.Int32(80)},
	})
	require.NoError(t, err)

	desc, err := c.DescribeNetworkAcls(ctx, &ec2.DescribeNetworkAclsInput{
		NetworkAclIds: []string{aws.ToString(aclID)},
	})
	require.NoError(t, err)
	require.Len(t, desc.NetworkAcls, 1)
	acl := desc.NetworkAcls[0]
	var found *types.NetworkAclEntry
	for i := range acl.Entries {
		if aws.ToInt32(acl.Entries[i].RuleNumber) == 100 && !aws.ToBool(acl.Entries[i].Egress) {
			found = &acl.Entries[i]
		}
	}
	require.NotNil(t, found, "rule 100 must round-trip")
	assert.Equal(t, types.RuleActionAllow, found.RuleAction)
	require.NotNil(t, found.PortRange)
	assert.Equal(t, int32(80), aws.ToInt32(found.PortRange.From))

	// Replace the rule with a deny on port 443.
	_, err = c.ReplaceNetworkAclEntry(ctx, &ec2.ReplaceNetworkAclEntryInput{
		NetworkAclId: aclID,
		RuleNumber:   aws.Int32(100),
		Protocol:     aws.String("6"),
		RuleAction:   types.RuleActionDeny,
		Egress:       aws.Bool(false),
		CidrBlock:    aws.String("0.0.0.0/0"),
		PortRange:    &types.PortRange{From: aws.Int32(443), To: aws.Int32(443)},
	})
	require.NoError(t, err)

	desc2, err := c.DescribeNetworkAcls(ctx, &ec2.DescribeNetworkAclsInput{NetworkAclIds: []string{aws.ToString(aclID)}})
	require.NoError(t, err)
	require.Len(t, desc2.NetworkAcls, 1)
	for _, e := range desc2.NetworkAcls[0].Entries {
		if aws.ToInt32(e.RuleNumber) == 100 && !aws.ToBool(e.Egress) {
			assert.Equal(t, types.RuleActionDeny, e.RuleAction)
			require.NotNil(t, e.PortRange)
			assert.Equal(t, int32(443), aws.ToInt32(e.PortRange.From))
		}
	}

	_, err = c.DeleteNetworkAclEntry(ctx, &ec2.DeleteNetworkAclEntryInput{
		NetworkAclId: aclID,
		RuleNumber:   aws.Int32(100),
		Egress:       aws.Bool(false),
	})
	require.NoError(t, err)
}

// TestEC2_VpcPeeringLifecycleSDK covers CreateVpcPeeringConnection,
// DescribeVpcPeeringConnections, AcceptVpcPeeringConnection (pending-acceptance
// -> active), and DeleteVpcPeeringConnection.
func TestEC2_VpcPeeringLifecycleSDK(t *testing.T) {
	c := ec2Client()

	a, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.201.0.0/16")})
	require.NoError(t, err)
	b, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.202.0.0/16")})
	require.NoError(t, err)
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: a.Vpc.VpcId}) }()
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: b.Vpc.VpcId}) }()

	created, err := c.CreateVpcPeeringConnection(ctx, &ec2.CreateVpcPeeringConnectionInput{
		VpcId:     a.Vpc.VpcId,
		PeerVpcId: b.Vpc.VpcId,
	})
	require.NoError(t, err)
	pcxID := created.VpcPeeringConnection.VpcPeeringConnectionId
	require.NotEmpty(t, aws.ToString(pcxID))
	assert.Equal(t, types.VpcPeeringConnectionStateReasonCodePendingAcceptance, created.VpcPeeringConnection.Status.Code)
	assert.Equal(t, aws.ToString(a.Vpc.VpcId), aws.ToString(created.VpcPeeringConnection.RequesterVpcInfo.VpcId))
	assert.Equal(t, aws.ToString(b.Vpc.VpcId), aws.ToString(created.VpcPeeringConnection.AccepterVpcInfo.VpcId))
	defer func() {
		_, _ = c.DeleteVpcPeeringConnection(ctx, &ec2.DeleteVpcPeeringConnectionInput{VpcPeeringConnectionId: pcxID})
	}()

	acc, err := c.AcceptVpcPeeringConnection(ctx, &ec2.AcceptVpcPeeringConnectionInput{VpcPeeringConnectionId: pcxID})
	require.NoError(t, err)
	assert.Equal(t, types.VpcPeeringConnectionStateReasonCodeActive, acc.VpcPeeringConnection.Status.Code)

	desc, err := c.DescribeVpcPeeringConnections(ctx, &ec2.DescribeVpcPeeringConnectionsInput{
		VpcPeeringConnectionIds: []string{aws.ToString(pcxID)},
	})
	require.NoError(t, err)
	require.Len(t, desc.VpcPeeringConnections, 1)
	assert.Equal(t, types.VpcPeeringConnectionStateReasonCodeActive, desc.VpcPeeringConnections[0].Status.Code)
}

// TestEC2_ManagedPrefixListLifecycleSDK covers CreateManagedPrefixList,
// DescribeManagedPrefixLists, GetManagedPrefixListEntries, and
// DeleteManagedPrefixList.
func TestEC2_ManagedPrefixListLifecycleSDK(t *testing.T) {
	c := ec2Client()

	created, err := c.CreateManagedPrefixList(ctx, &ec2.CreateManagedPrefixListInput{
		PrefixListName: aws.String("corp-cidrs"),
		AddressFamily:  aws.String("IPv4"),
		MaxEntries:     aws.Int32(5),
		Entries: []types.AddPrefixListEntry{
			{Cidr: aws.String("10.0.0.0/8"), Description: aws.String("corp")},
			{Cidr: aws.String("192.168.0.0/16")},
		},
	})
	require.NoError(t, err)
	plID := created.PrefixList.PrefixListId
	require.NotEmpty(t, aws.ToString(plID))
	assert.Equal(t, "corp-cidrs", aws.ToString(created.PrefixList.PrefixListName))
	assert.Equal(t, "IPv4", aws.ToString(created.PrefixList.AddressFamily))
	assert.Equal(t, int32(5), aws.ToInt32(created.PrefixList.MaxEntries))

	desc, err := c.DescribeManagedPrefixLists(ctx, &ec2.DescribeManagedPrefixListsInput{
		PrefixListIds: []string{aws.ToString(plID)},
	})
	require.NoError(t, err)
	require.Len(t, desc.PrefixLists, 1)
	assert.Equal(t, "corp-cidrs", aws.ToString(desc.PrefixLists[0].PrefixListName))

	entries, err := c.GetManagedPrefixListEntries(ctx, &ec2.GetManagedPrefixListEntriesInput{PrefixListId: plID})
	require.NoError(t, err)
	require.Len(t, entries.Entries, 2)
	cidrs := map[string]string{}
	for _, e := range entries.Entries {
		cidrs[aws.ToString(e.Cidr)] = aws.ToString(e.Description)
	}
	assert.Equal(t, "corp", cidrs["10.0.0.0/8"])
	_, ok := cidrs["192.168.0.0/16"]
	assert.True(t, ok)

	_, err = c.DeleteManagedPrefixList(ctx, &ec2.DeleteManagedPrefixListInput{PrefixListId: plID})
	require.NoError(t, err)
}

// TestEC2_FlowLogsLifecycleSDK covers CreateFlowLogs against a VPC,
// DescribeFlowLogs read-back, and DeleteFlowLogs.
func TestEC2_FlowLogsLifecycleSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.203.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId}) }()

	created, err := c.CreateFlowLogs(ctx, &ec2.CreateFlowLogsInput{
		ResourceIds:              []string{vpcID},
		ResourceType:             types.FlowLogsResourceTypeVpc,
		TrafficType:              types.TrafficTypeAll,
		LogDestinationType:       types.LogDestinationTypeCloudWatchLogs,
		LogGroupName:             aws.String("/vpc/flowlogs"),
		DeliverLogsPermissionArn: aws.String("arn:aws:iam::123456789012:role/flow-logs"),
	})
	require.NoError(t, err)
	require.Len(t, created.FlowLogIds, 1)
	require.Empty(t, created.Unsuccessful)
	flID := created.FlowLogIds[0]

	desc, err := c.DescribeFlowLogs(ctx, &ec2.DescribeFlowLogsInput{FlowLogIds: []string{flID}})
	require.NoError(t, err)
	require.Len(t, desc.FlowLogs, 1)
	assert.Equal(t, vpcID, aws.ToString(desc.FlowLogs[0].ResourceId))
	assert.Equal(t, types.TrafficTypeAll, desc.FlowLogs[0].TrafficType)
	assert.Equal(t, "ACTIVE", aws.ToString(desc.FlowLogs[0].FlowLogStatus))

	del, err := c.DeleteFlowLogs(ctx, &ec2.DeleteFlowLogsInput{FlowLogIds: []string{flID}})
	require.NoError(t, err)
	assert.Empty(t, del.Unsuccessful)
}

// TestEC2_EgressOnlyInternetGatewayLifecycleSDK covers
// CreateEgressOnlyInternetGateway, DescribeEgressOnlyInternetGateways, and
// DeleteEgressOnlyInternetGateway.
func TestEC2_EgressOnlyInternetGatewayLifecycleSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.204.0.0/16")})
	require.NoError(t, err)
	vpcID := vpc.Vpc.VpcId
	defer func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpcID}) }()

	created, err := c.CreateEgressOnlyInternetGateway(ctx, &ec2.CreateEgressOnlyInternetGatewayInput{VpcId: vpcID})
	require.NoError(t, err)
	eigwID := created.EgressOnlyInternetGateway.EgressOnlyInternetGatewayId
	require.NotEmpty(t, aws.ToString(eigwID))
	require.Len(t, created.EgressOnlyInternetGateway.Attachments, 1)
	assert.Equal(t, aws.ToString(vpcID), aws.ToString(created.EgressOnlyInternetGateway.Attachments[0].VpcId))

	desc, err := c.DescribeEgressOnlyInternetGateways(ctx, &ec2.DescribeEgressOnlyInternetGatewaysInput{
		EgressOnlyInternetGatewayIds: []string{aws.ToString(eigwID)},
	})
	require.NoError(t, err)
	require.Len(t, desc.EgressOnlyInternetGateways, 1)

	del, err := c.DeleteEgressOnlyInternetGateway(ctx, &ec2.DeleteEgressOnlyInternetGatewayInput{
		EgressOnlyInternetGatewayId: eigwID,
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(del.ReturnCode))
}
