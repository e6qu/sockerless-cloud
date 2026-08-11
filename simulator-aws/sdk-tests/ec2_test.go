package aws_sdk_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ec2Client() *ec2.Client {
	return ec2.NewFromConfig(sdkConfig(), func(o *ec2.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestEC2_CreateVpc(t *testing.T) {
	client := ec2Client()
	out, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String("10.0.0.0/16"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, *out.Vpc.VpcId)
	assert.Equal(t, "10.0.0.0/16", *out.Vpc.CidrBlock)
}

func TestEC2_CreateSubnet(t *testing.T) {
	client := ec2Client()

	vpcOut, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String("10.1.0.0/16"),
	})
	require.NoError(t, err)

	out, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:     vpcOut.Vpc.VpcId,
		CidrBlock: aws.String("10.1.1.0/24"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, *out.Subnet.SubnetId)
	assert.Equal(t, *vpcOut.Vpc.VpcId, *out.Subnet.VpcId)
}

func TestEC2_SecurityGroup(t *testing.T) {
	client := ec2Client()

	vpcOut, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String("10.2.0.0/16"),
	})
	require.NoError(t, err)

	sgOut, err := client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("test-sg"),
		Description: aws.String("test security group"),
		VpcId:       vpcOut.Vpc.VpcId,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, *sgOut.GroupId)

	descOut, err := client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		GroupIds: []string{*sgOut.GroupId},
	})
	require.NoError(t, err)
	require.Len(t, descOut.SecurityGroups, 1)
	assert.Equal(t, "test-sg", *descOut.SecurityGroups[0].GroupName)
}

func TestEC2_InternetGateway(t *testing.T) {
	client := ec2Client()

	igwOut, err := client.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, *igwOut.InternetGateway.InternetGatewayId)

	descOut, err := client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
		InternetGatewayIds: []string{*igwOut.InternetGateway.InternetGatewayId},
	})
	require.NoError(t, err)
	require.Len(t, descOut.InternetGateways, 1)
}

func TestEC2_ElasticIPNatGatewayAndRoute(t *testing.T) {
	client := ec2Client()

	vpcOut, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String("10.79.0.0/16"),
	})
	require.NoError(t, err)

	subnetOut, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            vpcOut.Vpc.VpcId,
		CidrBlock:        aws.String("10.79.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)

	eipOut, err := client.AllocateAddress(ctx, &ec2.AllocateAddressInput{
		Domain: types.DomainTypeVpc,
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeElasticIp,
			Tags:         []types.Tag{{Key: aws.String("env"), Value: aws.String("sdk")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, eipOut.AllocationId)
	require.NotNil(t, eipOut.PublicIp)

	addrOut, err := client.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{
		AllocationIds: []string{*eipOut.AllocationId},
	})
	require.NoError(t, err)
	require.Len(t, addrOut.Addresses, 1)
	assert.Equal(t, *eipOut.PublicIp, *addrOut.Addresses[0].PublicIp)

	attrOut, err := client.DescribeAddressesAttribute(ctx, &ec2.DescribeAddressesAttributeInput{
		AllocationIds: []string{*eipOut.AllocationId},
	})
	require.NoError(t, err)
	require.NotNil(t, attrOut)

	natOut, err := client.CreateNatGateway(ctx, &ec2.CreateNatGatewayInput{
		AllocationId: eipOut.AllocationId,
		SubnetId:     subnetOut.Subnet.SubnetId,
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeNatgateway,
			Tags:         []types.Tag{{Key: aws.String("env"), Value: aws.String("sdk")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, natOut.NatGateway)
	require.NotNil(t, natOut.NatGateway.NatGatewayId)
	assert.Equal(t, types.NatGatewayStateAvailable, natOut.NatGateway.State)

	natID := *natOut.NatGateway.NatGatewayId
	natDesc, err := client.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{
		NatGatewayIds: []string{natID},
	})
	require.NoError(t, err)
	require.Len(t, natDesc.NatGateways, 1)
	require.Len(t, natDesc.NatGateways[0].NatGatewayAddresses, 1)
	assert.Equal(t, *eipOut.AllocationId, *natDesc.NatGateways[0].NatGatewayAddresses[0].AllocationId)

	rtOut, err := client.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{
		VpcId: vpcOut.Vpc.VpcId,
	})
	require.NoError(t, err)
	require.NotNil(t, rtOut.RouteTable.RouteTableId)
	_, err = client.CreateRoute(ctx, &ec2.CreateRouteInput{
		RouteTableId:         rtOut.RouteTable.RouteTableId,
		DestinationCidrBlock: aws.String("0.0.0.0/0"),
		NatGatewayId:         aws.String(natID),
	})
	require.NoError(t, err)

	rtDesc, err := client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		RouteTableIds: []string{*rtOut.RouteTable.RouteTableId},
	})
	require.NoError(t, err)
	require.Len(t, rtDesc.RouteTables, 1)
	foundRoute := false
	for _, route := range rtDesc.RouteTables[0].Routes {
		if route.NatGatewayId != nil && *route.NatGatewayId == natID {
			foundRoute = true
			break
		}
	}
	assert.True(t, foundRoute, "route table must include NAT gateway route")

	_, err = client.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{NatGatewayId: aws.String(natID)})
	require.NoError(t, err)
	_, err = client.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: eipOut.AllocationId})
	require.NoError(t, err)
}

func TestEC2_InstanceLifecycle(t *testing.T) {
	client := ec2Client()

	images, err := client.DescribeImages(ctx, &ec2.DescribeImagesInput{
		ImageIds: []string{"ami-test1234"},
	})
	require.NoError(t, err)
	require.Len(t, images.Images, 1)

	accountAttrs, err := client.DescribeAccountAttributes(ctx, &ec2.DescribeAccountAttributesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, accountAttrs.AccountAttributes)

	zones, err := client.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, zones.AvailabilityZones)

	regions, err := client.DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, regions.Regions)

	instanceTypes, err := client.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{
		InstanceTypes: []types.InstanceType{types.InstanceTypeT3Micro},
	})
	require.NoError(t, err)
	require.Len(t, instanceTypes.InstanceTypes, 1)

	keys, err := client.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{})
	require.NoError(t, err)
	assert.Empty(t, keys.KeyPairs)

	vpcOut, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String("10.66.0.0/16"),
	})
	require.NoError(t, err)

	subnetOut, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            vpcOut.Vpc.VpcId,
		CidrBlock:        aws.String("10.66.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)

	sgOut, err := client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("instance-sg"),
		Description: aws.String("instance lifecycle"),
		VpcId:       vpcOut.Vpc.VpcId,
	})
	require.NoError(t, err)

	runOut, err := client.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String("ami-test1234"),
		InstanceType: types.InstanceTypeT3Micro,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		SubnetId:     subnetOut.Subnet.SubnetId,
		SecurityGroupIds: []string{
			*sgOut.GroupId,
		},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeInstance,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("sdk-instance")}},
		}},
	})
	require.NoError(t, err)
	require.Len(t, runOut.Instances, 1)
	instanceID := *runOut.Instances[0].InstanceId
	assert.Equal(t, types.InstanceStateNamePending, runOut.Instances[0].State.Name)

	descOut := waitForEC2InstanceState(t, client, instanceID, types.InstanceStateNameRunning)
	require.Len(t, descOut.Reservations, 1)
	require.Len(t, descOut.Reservations[0].Instances, 1)
	assert.Equal(t, instanceID, *descOut.Reservations[0].Instances[0].InstanceId)
	assert.NotEmpty(t, descOut.Reservations[0].Instances[0].PrivateIpAddress)
	require.Len(t, descOut.Reservations[0].Instances[0].NetworkInterfaces, 1)

	_, err = client.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{instanceID},
		Tags:      []types.Tag{{Key: aws.String("phase"), Value: aws.String("sdk")}},
	})
	require.NoError(t, err)
	tagged, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []types.Filter{{Name: aws.String("tag:phase"), Values: []string{"sdk"}}},
	})
	require.NoError(t, err)
	require.Len(t, tagged.Reservations, 1)
	assert.Equal(t, instanceID, aws.ToString(tagged.Reservations[0].Instances[0].InstanceId))
	// Scope the state filter to this test's own (tagged) instance — the shared
	// instance store also holds instances other tests launch (fleets, spot), so
	// a bare instance-state-name filter is not account-empty. The intent here is
	// that this running instance does not appear under a stopped filter.
	stoppedFiltered, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []types.Filter{
			{Name: aws.String("instance-state-name"), Values: []string{"stopped"}},
			{Name: aws.String("tag:phase"), Values: []string{"sdk"}},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, stoppedFiltered.Reservations)
	_, err = client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []types.Filter{{Name: aws.String("unsupported-filter-name"), Values: []string{"x"}}},
	})
	require.Error(t, err)
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "InvalidParameterValue", apiErr.ErrorCode())

	descOut, err = client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	require.NoError(t, err)

	_, err = client.DeleteTags(ctx, &ec2.DeleteTagsInput{
		Resources: []string{instanceID},
		Tags:      []types.Tag{{Key: aws.String("phase")}},
	})
	require.NoError(t, err)

	statusOut, err := client.DescribeInstanceStatus(ctx, &ec2.DescribeInstanceStatusInput{
		InstanceIds: []string{instanceID},
	})
	require.NoError(t, err)
	require.Len(t, statusOut.InstanceStatuses, 1)
	assert.Equal(t, instanceID, *statusOut.InstanceStatuses[0].InstanceId)

	attrOut, err := client.DescribeInstanceAttribute(ctx, &ec2.DescribeInstanceAttributeInput{
		InstanceId: aws.String(instanceID),
		Attribute:  types.InstanceAttributeNameInstanceType,
	})
	require.NoError(t, err)
	require.NotNil(t, attrOut.InstanceType)
	assert.Equal(t, "t3.micro", *attrOut.InstanceType.Value)
	stopAttrOut, err := client.DescribeInstanceAttribute(ctx, &ec2.DescribeInstanceAttributeInput{
		InstanceId: aws.String(instanceID),
		Attribute:  types.InstanceAttributeNameDisableApiStop,
	})
	require.NoError(t, err)
	require.NotNil(t, stopAttrOut.DisableApiStop)
	assert.False(t, *stopAttrOut.DisableApiStop.Value)
	_, err = client.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{
		InstanceId: aws.String(instanceID),
		SourceDestCheck: &types.AttributeBooleanValue{
			Value: aws.Bool(true),
		},
	})
	require.NoError(t, err)

	tagsOut, err := client.DescribeTags(ctx, &ec2.DescribeTagsInput{})
	require.NoError(t, err)
	assert.NotNil(t, tagsOut.Tags)

	volumesOut, err := client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, volumesOut.Volumes)

	eniID := *descOut.Reservations[0].Instances[0].NetworkInterfaces[0].NetworkInterfaceId
	eniOut, err := client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		NetworkInterfaceIds: []string{eniID},
	})
	require.NoError(t, err)
	require.Len(t, eniOut.NetworkInterfaces, 1)
	assert.Equal(t, instanceID, *eniOut.NetworkInterfaces[0].Attachment.InstanceId)

	_, err = client.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	stopped, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	stoppedInstance := stopped.Reservations[0].Instances[0]
	assert.Equal(t, types.InstanceStateNameStopped, stoppedInstance.State.Name)
	assert.Contains(t, aws.ToString(stoppedInstance.StateTransitionReason), "User initiated")
	require.NotNil(t, stoppedInstance.StateReason)
	assert.Equal(t, "Client.UserInitiatedShutdown", aws.ToString(stoppedInstance.StateReason.Code))

	_, err = client.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	running, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	runningInstance := running.Reservations[0].Instances[0]
	assert.Equal(t, types.InstanceStateNameRunning, runningInstance.State.Name)
	assert.Empty(t, aws.ToString(runningInstance.StateTransitionReason))
	assert.Nil(t, runningInstance.StateReason)

	_, err = client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	terminated, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	assert.Equal(t, types.InstanceStateNameTerminated, terminated.Reservations[0].Instances[0].State.Name)
}

func TestEC2_RunInstancesHonorsMaxCount(t *testing.T) {
	client := ec2Client()
	vpcOut, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.67.0.0/16")})
	require.NoError(t, err)
	subnetOut, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:     vpcOut.Vpc.VpcId,
		CidrBlock: aws.String("10.67.1.0/24"),
	})
	require.NoError(t, err)

	runOut, err := client.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String("ami-count1234"),
		InstanceType: types.InstanceTypeT3Micro,
		MinCount:     aws.Int32(2),
		MaxCount:     aws.Int32(2),
		SubnetId:     subnetOut.Subnet.SubnetId,
	})
	require.NoError(t, err)
	require.Len(t, runOut.Instances, 2)
	for _, inst := range runOut.Instances {
		assert.Equal(t, types.InstanceStateNamePending, inst.State.Name)
		waitForEC2InstanceState(t, client, aws.ToString(inst.InstanceId), types.InstanceStateNameRunning)
		_, err = client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{aws.ToString(inst.InstanceId)}})
		require.NoError(t, err)
	}
}

// TestEC2_RunInstancesClientTokenReplayReportsLaunchState proves a retried
// RunInstances (same ClientToken — the AWS SDK auto-generates one) replays the
// original launch response: the instance is still reported "pending" even after
// the control plane has transitioned it to "running". A replay that re-read the
// live state instead raced that transition and could report "running".
func TestEC2_RunInstancesClientTokenReplayReportsLaunchState(t *testing.T) {
	client := ec2Client()
	vpcOut, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.68.0.0/16")})
	require.NoError(t, err)
	subnetOut, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:     vpcOut.Vpc.VpcId,
		CidrBlock: aws.String("10.68.1.0/24"),
	})
	require.NoError(t, err)

	token := "replay-token-run-instances"
	input := &ec2.RunInstancesInput{
		ImageId:      aws.String("ami-replay1234"),
		InstanceType: types.InstanceTypeT3Micro,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		SubnetId:     subnetOut.Subnet.SubnetId,
		ClientToken:  aws.String(token),
	}
	first, err := client.RunInstances(ctx, input)
	require.NoError(t, err)
	require.Len(t, first.Instances, 1)
	require.Equal(t, types.InstanceStateNamePending, first.Instances[0].State.Name)
	instanceID := aws.ToString(first.Instances[0].InstanceId)

	// Let the control plane transition the instance to running.
	waitForEC2InstanceState(t, client, instanceID, types.InstanceStateNameRunning)

	// The idempotent retry replays the original launch response: still pending,
	// same instance id and reservation.
	replay, err := client.RunInstances(ctx, input)
	require.NoError(t, err)
	require.Len(t, replay.Instances, 1)
	assert.Equal(t, instanceID, aws.ToString(replay.Instances[0].InstanceId))
	assert.Equal(t, types.InstanceStateNamePending, replay.Instances[0].State.Name)

	_, err = client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
}

func TestEC2_EBSSnapshotCompletesWithoutVPCSDK(t *testing.T) {
	client := ec2Client()

	created, err := client.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"),
		Size:             aws.Int32(1),
		VolumeType:       types.VolumeTypeGp3,
	})
	require.NoError(t, err)
	volumeID := aws.ToString(created.VolumeId)
	require.NotEmpty(t, volumeID)
	defer client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(volumeID)})

	snapshotOut, err := client.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{
		VolumeId:    aws.String(volumeID),
		Description: aws.String("standalone sdk snapshot"),
	})
	require.NoError(t, err)
	snapshotID := aws.ToString(snapshotOut.SnapshotId)
	require.NotEmpty(t, snapshotID)
	assert.Equal(t, types.SnapshotStatePending, snapshotOut.State)
	defer client.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: aws.String(snapshotID)})

	filtered := waitForEC2SnapshotState(t, client, snapshotID, types.SnapshotStateCompleted)
	require.Len(t, filtered.Snapshots, 1)

	unfiltered, err := client.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{})
	require.NoError(t, err)
	foundCompleted := false
	for _, snap := range unfiltered.Snapshots {
		if aws.ToString(snap.SnapshotId) == snapshotID && snap.State == types.SnapshotStateCompleted {
			foundCompleted = true
			break
		}
	}
	require.True(t, foundCompleted, "unfiltered DescribeSnapshots must expose completed state for %s", snapshotID)

	restored, err := client.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"),
		SnapshotId:       aws.String(snapshotID),
		VolumeType:       types.VolumeTypeGp3,
	})
	require.NoError(t, err)
	restoredID := aws.ToString(restored.VolumeId)
	require.NotEmpty(t, restoredID)
	assert.Equal(t, snapshotID, aws.ToString(restored.SnapshotId))
	defer client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(restoredID)})
}

func TestEC2_EBSVolumeSnapshotLifecycleSDK(t *testing.T) {
	client := ec2Client()
	vpcOut, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.68.0.0/16")})
	require.NoError(t, err)
	subnetOut, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            vpcOut.Vpc.VpcId,
		CidrBlock:        aws.String("10.68.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)

	runOut, err := client.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String("ami-ebs1234"),
		InstanceType: types.InstanceTypeT3Micro,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		SubnetId:     subnetOut.Subnet.SubnetId,
	})
	require.NoError(t, err)
	require.Len(t, runOut.Instances, 1)
	instanceID := aws.ToString(runOut.Instances[0].InstanceId)
	waitForEC2InstanceState(t, client, instanceID, types.InstanceStateNameRunning)
	t.Cleanup(func() {
		_, _ = client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{instanceID}})
	})

	created, err := client.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"),
		Size:             aws.Int32(1),
		VolumeType:       types.VolumeTypeGp3,
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeVolume,
			Tags:         []types.Tag{{Key: aws.String("env"), Value: aws.String("sdk")}},
		}},
	})
	require.NoError(t, err)
	volumeID := aws.ToString(created.VolumeId)
	require.NotEmpty(t, volumeID)
	assert.Equal(t, types.VolumeStateAvailable, created.State)

	attached, err := client.AttachVolume(ctx, &ec2.AttachVolumeInput{
		Device:     aws.String("/dev/sdf"),
		InstanceId: aws.String(instanceID),
		VolumeId:   aws.String(volumeID),
	})
	require.NoError(t, err)
	assert.Equal(t, types.VolumeAttachmentStateAttached, attached.State)

	described, err := client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{volumeID}})
	require.NoError(t, err)
	require.Len(t, described.Volumes, 1)
	assert.Equal(t, types.VolumeStateInUse, described.Volumes[0].State)
	require.Len(t, described.Volumes[0].Attachments, 1)
	assert.Equal(t, instanceID, aws.ToString(described.Volumes[0].Attachments[0].InstanceId))

	modified, err := client.ModifyVolume(ctx, &ec2.ModifyVolumeInput{
		VolumeId:   aws.String(volumeID),
		Size:       aws.Int32(2),
		VolumeType: types.VolumeTypeGp3,
	})
	require.NoError(t, err)
	require.NotNil(t, modified.VolumeModification)
	assert.Equal(t, int32(2), aws.ToInt32(modified.VolumeModification.TargetSize))

	snapshotOut, err := client.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{
		VolumeId:    aws.String(volumeID),
		Description: aws.String("sdk snapshot"),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeSnapshot,
			Tags:         []types.Tag{{Key: aws.String("env"), Value: aws.String("sdk")}},
		}},
	})
	require.NoError(t, err)
	snapshotID := aws.ToString(snapshotOut.SnapshotId)
	require.NotEmpty(t, snapshotID)
	assert.Equal(t, types.SnapshotStatePending, snapshotOut.State)

	snaps := waitForEC2SnapshotState(t, client, snapshotID, types.SnapshotStateCompleted)
	require.Len(t, snaps.Snapshots, 1)
	assert.Equal(t, volumeID, aws.ToString(snaps.Snapshots[0].VolumeId))

	restored, err := client.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"),
		SnapshotId:       aws.String(snapshotID),
		VolumeType:       types.VolumeTypeGp3,
	})
	require.NoError(t, err)
	restoredID := aws.ToString(restored.VolumeId)
	require.NotEmpty(t, restoredID)
	assert.Equal(t, snapshotID, aws.ToString(restored.SnapshotId))

	_, err = client.DetachVolume(ctx, &ec2.DetachVolumeInput{VolumeId: aws.String(volumeID)})
	require.NoError(t, err)
	_, err = client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(volumeID)})
	require.NoError(t, err)
	_, err = client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(restoredID)})
	require.NoError(t, err)
	_, err = client.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: aws.String(snapshotID)})
	require.NoError(t, err)
}

func waitForEC2InstanceState(t *testing.T, client *ec2.Client, instanceID string, want types.InstanceStateName) *ec2.DescribeInstancesOutput {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	var last *ec2.DescribeInstancesOutput
	for time.Now().Before(deadline) {
		out, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
		require.NoError(t, err)
		last = out
		if len(out.Reservations) == 1 && len(out.Reservations[0].Instances) == 1 &&
			out.Reservations[0].Instances[0].State.Name == want {
			return out
		}
		time.Sleep(1 * time.Second)
	}
	if last != nil && len(last.Reservations) == 1 && len(last.Reservations[0].Instances) == 1 {
		t.Fatalf("instance %s state = %s, want %s", instanceID, last.Reservations[0].Instances[0].State.Name, want)
	}
	t.Fatalf("instance %s did not reach %s", instanceID, want)
	return nil
}

func waitForEC2SnapshotState(t *testing.T, client *ec2.Client, snapshotID string, want types.SnapshotState) *ec2.DescribeSnapshotsOutput {
	t.Helper()
	// Generous deadline: the snapshot transition is fast, but a tight 2s window
	// can expire under CI scheduling stalls / GC pauses and flake the test.
	deadline := time.Now().Add(60 * time.Second)
	var last *ec2.DescribeSnapshotsOutput
	for time.Now().Before(deadline) {
		out, err := client.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{snapshotID}})
		require.NoError(t, err)
		last = out
		if len(out.Snapshots) == 1 && out.Snapshots[0].State == want {
			return out
		}
		time.Sleep(25 * time.Millisecond)
	}
	if last != nil && len(last.Snapshots) == 1 {
		t.Fatalf("snapshot %s state = %s, want %s", snapshotID, last.Snapshots[0].State, want)
	}
	t.Fatalf("snapshot %s did not reach %s", snapshotID, want)
	return nil
}
