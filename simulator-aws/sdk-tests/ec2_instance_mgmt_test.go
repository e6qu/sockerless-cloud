package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_InstanceConnectEndpointLifecycle expects Create to settle
// create-complete with an ENI and a DNS name.
func TestEC2_InstanceConnectEndpointLifecycle(t *testing.T) {
	t.Parallel()
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

// TestEC2_SerialConsoleAccess expects the account's serial console access
// disabled by default.
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

func TestEC2_InstanceEventNotificationAttributes(t *testing.T) {
	t.Parallel()
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

func TestEC2_MonitorInstances(t *testing.T) {
	t.Parallel()
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.181.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.181.1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.NoError(t, err)
	terminateEC2InstancesOnCleanup(t, c, run)
	instID := aws.ToString(run.Instances[0].InstanceId)
	waitForEC2InstanceRunning(t, c, instID)

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

	// Resetting sourceDestCheck restores its default, true.
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

// TestEC2_ClassicLinkInstances expects no instances: everything runs in a VPC.
func TestEC2_ClassicLinkInstances(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	out, err := c.DescribeClassicLinkInstances(ctx, &ec2.DescribeClassicLinkInstancesInput{})
	require.NoError(t, err)
	assert.Empty(t, out.Instances)
}
