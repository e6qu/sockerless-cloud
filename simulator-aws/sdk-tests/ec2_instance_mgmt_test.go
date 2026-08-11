package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_InstanceConnectEndpointLifecycle covers the EC2 Instance Connect
// Endpoint control plane via the SDK: Create in a subnet (state
// create-complete, an auto-created ENI, a DNS name), Describe read-back +
// id-filter, Modify, and Delete.
func TestEC2_InstanceConnectEndpointLifecycle(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.180.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.180.1.0/24")

	create, err := c.CreateInstanceConnectEndpoint(ctx, &ec2.CreateInstanceConnectEndpointInput{
		SubnetId:         subnet,
		PreserveClientIp: aws.Bool(true),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeInstanceConnectEndpoint,
			Tags:         []types.Tag{{Key: aws.String("env"), Value: aws.String("dev")}},
		}},
	})
	require.NoError(t, err)
	eice := create.InstanceConnectEndpoint
	require.NotNil(t, eice)
	id := aws.ToString(eice.InstanceConnectEndpointId)
	require.NotEmpty(t, id)
	assert.Equal(t, types.Ec2InstanceConnectEndpointStateCreateComplete, eice.State)
	assert.Equal(t, aws.ToString(subnet), aws.ToString(eice.SubnetId))
	assert.Equal(t, aws.ToString(vpc.Vpc.VpcId), aws.ToString(eice.VpcId))
	assert.True(t, aws.ToBool(eice.PreserveClientIp))
	assert.NotEmpty(t, aws.ToString(eice.DnsName))
	require.Len(t, eice.NetworkInterfaceIds, 1, "endpoint provisions an ENI")
	assert.NotEmpty(t, eice.NetworkInterfaceIds[0])

	desc, err := c.DescribeInstanceConnectEndpoints(ctx, &ec2.DescribeInstanceConnectEndpointsInput{
		InstanceConnectEndpointIds: []string{id},
	})
	require.NoError(t, err)
	require.Len(t, desc.InstanceConnectEndpoints, 1)
	assert.Equal(t, id, aws.ToString(desc.InstanceConnectEndpoints[0].InstanceConnectEndpointId))
	require.Len(t, desc.InstanceConnectEndpoints[0].Tags, 1)
	assert.Equal(t, "env", aws.ToString(desc.InstanceConnectEndpoints[0].Tags[0].Key))

	// Filter by subnet-id.
	bySubnet, err := c.DescribeInstanceConnectEndpoints(ctx, &ec2.DescribeInstanceConnectEndpointsInput{
		Filters: []types.Filter{{Name: aws.String("subnet-id"), Values: []string{aws.ToString(subnet)}}},
	})
	require.NoError(t, err)
	require.Len(t, bySubnet.InstanceConnectEndpoints, 1)
	assert.Equal(t, id, aws.ToString(bySubnet.InstanceConnectEndpoints[0].InstanceConnectEndpointId))

	_, err = c.ModifyInstanceConnectEndpoint(ctx, &ec2.ModifyInstanceConnectEndpointInput{
		InstanceConnectEndpointId: aws.String(id),
	})
	require.NoError(t, err)

	del, err := c.DeleteInstanceConnectEndpoint(ctx, &ec2.DeleteInstanceConnectEndpointInput{
		InstanceConnectEndpointId: aws.String(id),
	})
	require.NoError(t, err)
	require.NotNil(t, del.InstanceConnectEndpoint)

	gone, err := c.DescribeInstanceConnectEndpoints(ctx, &ec2.DescribeInstanceConnectEndpointsInput{})
	require.NoError(t, err)
	for _, e := range gone.InstanceConnectEndpoints {
		assert.NotEqual(t, id, aws.ToString(e.InstanceConnectEndpointId), "deleted endpoint must be gone")
	}
}

// TestEC2_SerialConsoleAccess covers the account-level serial-console flag:
// Get (default disabled), Enable, Get (enabled), Disable, Get (disabled).
func TestEC2_SerialConsoleAccess(t *testing.T) {
	c := ec2Client()

	en, err := c.EnableSerialConsoleAccess(ctx, &ec2.EnableSerialConsoleAccessInput{})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(en.SerialConsoleAccessEnabled))

	got, err := c.GetSerialConsoleAccessStatus(ctx, &ec2.GetSerialConsoleAccessStatusInput{})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(got.SerialConsoleAccessEnabled))

	dis, err := c.DisableSerialConsoleAccess(ctx, &ec2.DisableSerialConsoleAccessInput{})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(dis.SerialConsoleAccessEnabled))

	got2, err := c.GetSerialConsoleAccessStatus(ctx, &ec2.GetSerialConsoleAccessStatusInput{})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(got2.SerialConsoleAccessEnabled))
}

// TestEC2_InstanceMetadataDefaults covers the account-level IMDS defaults:
// Modify sets http-tokens/hop-limit/endpoint, Get reads them back.
func TestEC2_InstanceMetadataDefaults(t *testing.T) {
	c := ec2Client()

	_, err := c.ModifyInstanceMetadataDefaults(ctx, &ec2.ModifyInstanceMetadataDefaultsInput{
		HttpTokens:              types.MetadataDefaultHttpTokensStateRequired,
		HttpPutResponseHopLimit: aws.Int32(3),
		HttpEndpoint:            types.DefaultInstanceMetadataEndpointStateEnabled,
	})
	require.NoError(t, err)

	got, err := c.GetInstanceMetadataDefaults(ctx, &ec2.GetInstanceMetadataDefaultsInput{})
	require.NoError(t, err)
	require.NotNil(t, got.AccountLevel)
	assert.Equal(t, types.HttpTokensStateRequired, got.AccountLevel.HttpTokens)
	assert.Equal(t, int32(3), aws.ToInt32(got.AccountLevel.HttpPutResponseHopLimit))
	assert.Equal(t, types.InstanceMetadataEndpointStateEnabled, got.AccountLevel.HttpEndpoint)
}

// TestEC2_InstanceEventNotificationAttributes covers the account-level set of
// tag keys registered for instance-event notifications:
// Register adds keys, Describe reads them, Deregister removes one.
func TestEC2_InstanceEventNotificationAttributes(t *testing.T) {
	c := ec2Client()

	reg, err := c.RegisterInstanceEventNotificationAttributes(ctx, &ec2.RegisterInstanceEventNotificationAttributesInput{
		InstanceTagAttribute: &types.RegisterInstanceTagAttributeRequest{
			InstanceTagKeys: []string{"Name", "CostCenter"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, reg.InstanceTagAttribute)
	assert.ElementsMatch(t, []string{"Name", "CostCenter"}, reg.InstanceTagAttribute.InstanceTagKeys)

	desc, err := c.DescribeInstanceEventNotificationAttributes(ctx, &ec2.DescribeInstanceEventNotificationAttributesInput{})
	require.NoError(t, err)
	require.NotNil(t, desc.InstanceTagAttribute)
	assert.Contains(t, desc.InstanceTagAttribute.InstanceTagKeys, "Name")
	assert.Contains(t, desc.InstanceTagAttribute.InstanceTagKeys, "CostCenter")

	dereg, err := c.DeregisterInstanceEventNotificationAttributes(ctx, &ec2.DeregisterInstanceEventNotificationAttributesInput{
		InstanceTagAttribute: &types.DeregisterInstanceTagAttributeRequest{
			InstanceTagKeys: []string{"CostCenter"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, dereg.InstanceTagAttribute)
	assert.NotContains(t, dereg.InstanceTagAttribute.InstanceTagKeys, "CostCenter")
	assert.Contains(t, dereg.InstanceTagAttribute.InstanceTagKeys, "Name")
}

// TestEC2_MonitorInstances covers detailed-monitoring toggling plus the
// reboot/report/reset instance-management ops on a launched instance.
func TestEC2_MonitorInstances(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.181.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.181.1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String("ami-12345678"), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.NoError(t, err)
	instID := aws.ToString(run.Instances[0].InstanceId)

	mon, err := c.MonitorInstances(ctx, &ec2.MonitorInstancesInput{InstanceIds: []string{instID}})
	require.NoError(t, err)
	require.Len(t, mon.InstanceMonitorings, 1)
	assert.Equal(t, instID, aws.ToString(mon.InstanceMonitorings[0].InstanceId))
	assert.Equal(t, types.MonitoringStateEnabled, mon.InstanceMonitorings[0].Monitoring.State)

	unmon, err := c.UnmonitorInstances(ctx, &ec2.UnmonitorInstancesInput{InstanceIds: []string{instID}})
	require.NoError(t, err)
	require.Len(t, unmon.InstanceMonitorings, 1)
	assert.Equal(t, types.MonitoringStateDisabled, unmon.InstanceMonitorings[0].Monitoring.State)

	_, err = c.RebootInstances(ctx, &ec2.RebootInstancesInput{InstanceIds: []string{instID}})
	require.NoError(t, err)

	_, err = c.ReportInstanceStatus(ctx, &ec2.ReportInstanceStatusInput{
		Instances:   []string{instID},
		Status:      types.ReportStatusTypeImpaired,
		ReasonCodes: []types.ReportInstanceReasonCodes{types.ReportInstanceReasonCodesUnresponsive},
	})
	require.NoError(t, err)

	// Disable the source/dest check, then reset it back to the default (true).
	_, err = c.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{
		InstanceId:      aws.String(instID),
		SourceDestCheck: &types.AttributeBooleanValue{Value: aws.Bool(false)},
	})
	require.NoError(t, err)
	_, err = c.ResetInstanceAttribute(ctx, &ec2.ResetInstanceAttributeInput{
		InstanceId: aws.String(instID),
		Attribute:  types.InstanceAttributeNameSourceDestCheck,
	})
	require.NoError(t, err)
	attr, err := c.DescribeInstanceAttribute(ctx, &ec2.DescribeInstanceAttributeInput{
		InstanceId: aws.String(instID),
		Attribute:  types.InstanceAttributeNameSourceDestCheck,
	})
	require.NoError(t, err)
	require.NotNil(t, attr.SourceDestCheck)
	assert.True(t, aws.ToBool(attr.SourceDestCheck.Value), "ResetInstanceAttribute restores sourceDestCheck=true")
}

// TestEC2_ClassicLinkInstances asserts the honest-empty EC2-Classic read: the
// sim runs only in a VPC, so DescribeClassicLinkInstances returns no instances.
func TestEC2_ClassicLinkInstances(t *testing.T) {
	c := ec2Client()
	out, err := c.DescribeClassicLinkInstances(ctx, &ec2.DescribeClassicLinkInstancesInput{})
	require.NoError(t, err)
	assert.Empty(t, out.Instances)
}
