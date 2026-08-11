package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_NetworkInterfaceLifecycle covers the standalone ENI
// lifecycle (create → describe → attach → modify source/dest check → detach →
// delete), the fck-nat NAT-instance shape.
func TestEC2_NetworkInterfaceLifecycle(t *testing.T) {
	client := ec2Client()

	vpc, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.77.0.0/16")})
	require.NoError(t, err)
	sn, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.77.1.0/24")})
	require.NoError(t, err)

	// Create a standalone ENI.
	eniOut, err := client.CreateNetworkInterface(ctx, &ec2.CreateNetworkInterfaceInput{
		SubnetId:    sn.Subnet.SubnetId,
		Description: aws.String("fck-nat floating ENI"),
	})
	require.NoError(t, err)
	require.NotNil(t, eniOut.NetworkInterface)
	eniID := aws.ToString(eniOut.NetworkInterface.NetworkInterfaceId)
	require.NotEmpty(t, eniID)
	assert.Equal(t, types.NetworkInterfaceStatusAvailable, eniOut.NetworkInterface.Status)
	assert.Equal(t, aws.ToString(vpc.Vpc.VpcId), aws.ToString(eniOut.NetworkInterface.VpcId))

	desc, err := client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{eniID}})
	require.NoError(t, err)
	require.Len(t, desc.NetworkInterfaces, 1)
	require.NotNil(t, desc.NetworkInterfaces[0].SourceDestCheck)
	assert.True(t, *desc.NetworkInterfaces[0].SourceDestCheck)
	assert.Nil(t, desc.NetworkInterfaces[0].Attachment, "an unattached ENI must have no attachment")

	// Run an instance to attach to.
	runOut, err := client.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String("ami-12345678"),
		InstanceType: types.InstanceTypeT3Micro,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		SubnetId:     sn.Subnet.SubnetId,
	})
	require.NoError(t, err)
	require.NotEmpty(t, runOut.Instances)
	instID := aws.ToString(runOut.Instances[0].InstanceId)

	attachOut, err := client.AttachNetworkInterface(ctx, &ec2.AttachNetworkInterfaceInput{
		NetworkInterfaceId: aws.String(eniID),
		InstanceId:         aws.String(instID),
		DeviceIndex:        aws.Int32(1),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(attachOut.AttachmentId))

	// NAT instances disable source/dest check.
	_, err = client.ModifyNetworkInterfaceAttribute(ctx, &ec2.ModifyNetworkInterfaceAttributeInput{
		NetworkInterfaceId: aws.String(eniID),
		SourceDestCheck:    &types.AttributeBooleanValue{Value: aws.Bool(false)},
	})
	require.NoError(t, err)

	desc, err = client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{eniID}})
	require.NoError(t, err)
	ni := desc.NetworkInterfaces[0]
	assert.Equal(t, types.NetworkInterfaceStatusInUse, ni.Status)
	require.NotNil(t, ni.SourceDestCheck)
	assert.False(t, *ni.SourceDestCheck, "source/dest check must be disabled")
	require.NotNil(t, ni.Attachment)
	assert.Equal(t, instID, aws.ToString(ni.Attachment.InstanceId))

	// Assign a secondary private IP.
	assignOut, err := client.AssignPrivateIpAddresses(ctx, &ec2.AssignPrivateIpAddressesInput{
		NetworkInterfaceId:             aws.String(eniID),
		SecondaryPrivateIpAddressCount: aws.Int32(1),
	})
	require.NoError(t, err)
	require.NotNil(t, assignOut)

	// Detach → available.
	_, err = client.DetachNetworkInterface(ctx, &ec2.DetachNetworkInterfaceInput{AttachmentId: attachOut.AttachmentId})
	require.NoError(t, err)
	desc, err = client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{eniID}})
	require.NoError(t, err)
	assert.Equal(t, types.NetworkInterfaceStatusAvailable, desc.NetworkInterfaces[0].Status)

	// Delete.
	_, err = client.DeleteNetworkInterface(ctx, &ec2.DeleteNetworkInterfaceInput{NetworkInterfaceId: aws.String(eniID)})
	require.NoError(t, err)
	_, err = client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{eniID}})
	require.Error(t, err, "describe after delete must fail")
}

func TestEC2DeleteSubnetRejectsNetworkInterfaceDependency(t *testing.T) {
	client := ec2Client()
	vpc, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.79.0.0/16")})
	require.NoError(t, err)
	vpcID := vpc.Vpc.VpcId
	t.Cleanup(func() {
		_, _ = client.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpcID})
	})
	subnet, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:     vpcID,
		CidrBlock: aws.String("10.79.1.0/24"),
	})
	require.NoError(t, err)
	subnetID := subnet.Subnet.SubnetId
	networkInterface, err := client.CreateNetworkInterface(ctx, &ec2.CreateNetworkInterfaceInput{SubnetId: subnetID})
	require.NoError(t, err)
	networkInterfaceID := networkInterface.NetworkInterface.NetworkInterfaceId

	_, err = client.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: subnetID})
	assert.Equal(t, "DependencyViolation", errCode(t, err))

	_, err = client.DeleteNetworkInterface(ctx, &ec2.DeleteNetworkInterfaceInput{NetworkInterfaceId: networkInterfaceID})
	require.NoError(t, err)
	_, err = client.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: subnetID})
	require.NoError(t, err)
	_, err = client.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: subnetID})
	assert.Equal(t, "InvalidSubnetID.NotFound", errCode(t, err))
}
