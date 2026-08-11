package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_CreateSnapshotsSDK covers the multi-volume snapshot set: a running
// instance with an attached data volume is snapshotted via CreateSnapshots, and
// the returned SnapshotInfo carries the volume id, owner, size, and tags.
func TestEC2_CreateSnapshotsSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.210.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.210.1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String("ami-12345678"), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.NoError(t, err)
	instID := aws.ToString(run.Instances[0].InstanceId)

	vol, err := c.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(10),
	})
	require.NoError(t, err)
	_, err = c.AttachVolume(ctx, &ec2.AttachVolumeInput{
		VolumeId: vol.VolumeId, InstanceId: aws.String(instID), Device: aws.String("/dev/sdf"),
	})
	require.NoError(t, err)

	out, err := c.CreateSnapshots(ctx, &ec2.CreateSnapshotsInput{
		InstanceSpecification: &types.InstanceSpecification{InstanceId: aws.String(instID)},
		Description:           aws.String("multi-vol set"),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeSnapshot,
			Tags:         []types.Tag{{Key: aws.String("set"), Value: aws.String("nightly")}},
		}},
	})
	require.NoError(t, err)
	// The multi-volume snapshot set covers the instance's root volume plus the
	// attached data volume.
	require.Len(t, out.Snapshots, 2)
	byVol := map[string]types.SnapshotInfo{}
	for _, si := range out.Snapshots {
		byVol[aws.ToString(si.VolumeId)] = si
		assert.NotEmpty(t, aws.ToString(si.SnapshotId))
		require.Len(t, si.Tags, 1)
		assert.Equal(t, "nightly", aws.ToString(si.Tags[0].Value))
	}
	data, ok := byVol[aws.ToString(vol.VolumeId)]
	require.True(t, ok, "data volume is in the snapshot set")
	assert.Equal(t, int32(10), aws.ToInt32(data.VolumeSize))

	// ExcludeBootVolume drops the root volume, leaving only the data volume.
	noBoot, err := c.CreateSnapshots(ctx, &ec2.CreateSnapshotsInput{
		InstanceSpecification: &types.InstanceSpecification{
			InstanceId:        aws.String(instID),
			ExcludeBootVolume: aws.Bool(true),
		},
	})
	require.NoError(t, err)
	require.Len(t, noBoot.Snapshots, 1)
	assert.Equal(t, aws.ToString(vol.VolumeId), aws.ToString(noBoot.Snapshots[0].VolumeId))
}

// TestEC2_VolumeStatusSDK covers DescribeVolumeStatus + EnableVolumeIO + the
// CopyVolumes duplication path over an existing volume.
func TestEC2_VolumeStatusSDK(t *testing.T) {
	c := ec2Client()

	vol, err := c.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(20), VolumeType: types.VolumeTypeGp3,
	})
	require.NoError(t, err)
	volID := aws.ToString(vol.VolumeId)

	st, err := c.DescribeVolumeStatus(ctx, &ec2.DescribeVolumeStatusInput{VolumeIds: []string{volID}})
	require.NoError(t, err)
	require.Len(t, st.VolumeStatuses, 1)
	assert.Equal(t, volID, aws.ToString(st.VolumeStatuses[0].VolumeId))
	assert.Equal(t, types.VolumeStatusInfoStatusOk, st.VolumeStatuses[0].VolumeStatus.Status)

	_, err = c.EnableVolumeIO(ctx, &ec2.EnableVolumeIOInput{VolumeId: aws.String(volID)})
	require.NoError(t, err)

	cp, err := c.CopyVolumes(ctx, &ec2.CopyVolumesInput{SourceVolumeId: aws.String(volID)})
	require.NoError(t, err)
	require.Len(t, cp.Volumes, 1)
	assert.NotEqual(t, volID, aws.ToString(cp.Volumes[0].VolumeId))
	assert.Equal(t, int32(20), aws.ToInt32(cp.Volumes[0].Size))

	// ImportVolume materializes a fresh volume via a conversion task.
	imp, err := c.ImportVolume(ctx, &ec2.ImportVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"),
		Image:            &types.DiskImageDetail{Bytes: aws.Int64(1 << 30), Format: types.DiskImageFormatVmdk, ImportManifestUrl: aws.String("https://example/manifest")},
		Volume:           &types.VolumeDetail{Size: aws.Int64(8)},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(imp.ConversionTask.ConversionTaskId))
}

// TestEC2_RecycleBinSDK covers the volume / snapshot Recycle Bin list + restore
// surface, plus DescribeLockedSnapshots / DescribeImportSnapshotTasks read-back.
func TestEC2_RecycleBinSDK(t *testing.T) {
	c := ec2Client()

	// The list surfaces start empty (no resources in the bin), proving the shape
	// round-trips.
	lv, err := c.ListVolumesInRecycleBin(ctx, &ec2.ListVolumesInRecycleBinInput{})
	require.NoError(t, err)
	assert.Empty(t, lv.Volumes)

	ls, err := c.ListSnapshotsInRecycleBin(ctx, &ec2.ListSnapshotsInRecycleBinInput{})
	require.NoError(t, err)
	assert.Empty(t, ls.Snapshots)

	locked, err := c.DescribeLockedSnapshots(ctx, &ec2.DescribeLockedSnapshotsInput{})
	require.NoError(t, err)
	assert.Empty(t, locked.Snapshots)

	tasks, err := c.DescribeImportSnapshotTasks(ctx, &ec2.DescribeImportSnapshotTasksInput{})
	require.NoError(t, err)
	assert.Empty(t, tasks.ImportSnapshotTasks)

	// A volume not in the bin is rejected by RestoreVolumeFromRecycleBin.
	_, err = c.RestoreVolumeFromRecycleBin(ctx, &ec2.RestoreVolumeFromRecycleBinInput{VolumeId: aws.String("vol-deadbeef")})
	require.Error(t, err)
}

// TestEC2_ReplaceRootVolumeSDK covers the replace-root-volume task + Mac
// volume-ownership / SIP modification tasks (control-plane task records).
func TestEC2_ReplaceRootVolumeSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.211.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.211.1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String("ami-12345678"), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.NoError(t, err)
	instID := aws.ToString(run.Instances[0].InstanceId)

	rrv, err := c.CreateReplaceRootVolumeTask(ctx, &ec2.CreateReplaceRootVolumeTaskInput{
		InstanceId: aws.String(instID), ImageId: aws.String("ami-99999999"),
	})
	require.NoError(t, err)
	taskID := aws.ToString(rrv.ReplaceRootVolumeTask.ReplaceRootVolumeTaskId)
	require.NotEmpty(t, taskID)

	desc, err := c.DescribeReplaceRootVolumeTasks(ctx, &ec2.DescribeReplaceRootVolumeTasksInput{
		ReplaceRootVolumeTaskIds: []string{taskID},
	})
	require.NoError(t, err)
	require.Len(t, desc.ReplaceRootVolumeTasks, 1)
	assert.Equal(t, instID, aws.ToString(desc.ReplaceRootVolumeTasks[0].InstanceId))

	mac, err := c.CreateDelegateMacVolumeOwnershipTask(ctx, &ec2.CreateDelegateMacVolumeOwnershipTaskInput{
		InstanceId:     aws.String(instID),
		MacCredentials: aws.String("dGVzdA=="),
	})
	require.NoError(t, err)
	assert.Equal(t, types.MacModificationTaskTypeVolumeOwnershipDelegation, mac.MacModificationTask.TaskType)

	sip, err := c.CreateMacSystemIntegrityProtectionModificationTask(ctx, &ec2.CreateMacSystemIntegrityProtectionModificationTaskInput{
		InstanceId:                         aws.String(instID),
		MacCredentials:                     aws.String("dGVzdA=="),
		MacSystemIntegrityProtectionStatus: types.MacSystemIntegrityProtectionSettingStatusDisabled,
	})
	require.NoError(t, err)
	assert.Equal(t, types.MacModificationTaskTypeSIPModification, sip.MacModificationTask.TaskType)
}

// TestEC2_CoipCidrSDK covers CoIP CIDR add/delete on an existing CoIP pool.
func TestEC2_CoipCidrSDK(t *testing.T) {
	c := ec2Client()

	pool, err := c.CreateCoipPool(ctx, &ec2.CreateCoipPoolInput{
		LocalGatewayRouteTableId: aws.String("lgw-rtb-0123456789abcdef0"),
	})
	require.NoError(t, err)
	poolID := aws.ToString(pool.CoipPool.PoolId)
	require.NotEmpty(t, poolID)

	add, err := c.CreateCoipCidr(ctx, &ec2.CreateCoipCidrInput{
		Cidr: aws.String("10.40.0.0/24"), CoipPoolId: aws.String(poolID),
	})
	require.NoError(t, err)
	assert.Equal(t, "10.40.0.0/24", aws.ToString(add.CoipCidr.Cidr))
	assert.Equal(t, poolID, aws.ToString(add.CoipCidr.CoipPoolId))

	del, err := c.DeleteCoipCidr(ctx, &ec2.DeleteCoipCidrInput{
		Cidr: aws.String("10.40.0.0/24"), CoipPoolId: aws.String(poolID),
	})
	require.NoError(t, err)
	assert.Equal(t, "10.40.0.0/24", aws.ToString(del.CoipCidr.Cidr))
}

// TestEC2_DefaultVpcSDK covers CreateDefaultVpc / CreateDefaultSubnet. The
// account is auto-provisioned with a default VPC (as a real AWS account is), so
// CreateDefaultVpc reports DefaultVpcAlreadyExists and CreateDefaultSubnet lands
// a default subnet in that VPC.
func TestEC2_DefaultVpcSDK(t *testing.T) {
	c := ec2Client()

	// The default VPC already exists for the account.
	_, err := c.CreateDefaultVpc(ctx, &ec2.CreateDefaultVpcInput{})
	require.Error(t, err)
	var apiErr smithy.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "DefaultVpcAlreadyExists", apiErr.ErrorCode())

	// Locate the seeded default VPC, then create a default subnet in a fresh AZ.
	vpcs, err := c.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []types.Filter{{Name: aws.String("is-default"), Values: []string{"true"}}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, vpcs.Vpcs)
	defVpcID := aws.ToString(vpcs.Vpcs[0].VpcId)

	sn, err := c.CreateDefaultSubnet(ctx, &ec2.CreateDefaultSubnetInput{
		AvailabilityZone: aws.String("us-east-1b"),
	})
	require.NoError(t, err)
	assert.Equal(t, defVpcID, aws.ToString(sn.Subnet.VpcId))
	assert.True(t, aws.ToBool(sn.Subnet.MapPublicIpOnLaunch))
}

// TestEC2_PrefixListsSDK covers DescribePrefixLists (AWS-managed + customer
// lists) and the managed-prefix-list association / version-restore surface.
func TestEC2_PrefixListsSDK(t *testing.T) {
	c := ec2Client()

	pls, err := c.DescribePrefixLists(ctx, &ec2.DescribePrefixListsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, pls.PrefixLists, "AWS-managed gateway-endpoint prefix lists are always present")
	foundS3 := false
	for _, pl := range pls.PrefixLists {
		if name := aws.ToString(pl.PrefixListName); name == "com.amazonaws.us-east-1.s3" {
			foundS3 = true
			assert.NotEmpty(t, pl.Cidrs)
		}
	}
	assert.True(t, foundS3, "S3 gateway-endpoint prefix list present")

	mpl, err := c.CreateManagedPrefixList(ctx, &ec2.CreateManagedPrefixListInput{
		PrefixListName: aws.String("sdk-test-pl"),
		AddressFamily:  aws.String("IPv4"),
		MaxEntries:     aws.Int32(5),
		Entries: []types.AddPrefixListEntry{
			{Cidr: aws.String("10.50.0.0/24"), Description: aws.String("one")},
		},
	})
	require.NoError(t, err)
	plID := aws.ToString(mpl.PrefixList.PrefixListId)

	assoc, err := c.GetManagedPrefixListAssociations(ctx, &ec2.GetManagedPrefixListAssociationsInput{
		PrefixListId: aws.String(plID),
	})
	require.NoError(t, err)
	assert.Empty(t, assoc.PrefixListAssociations, "no SG rule references the new list yet")

	restored, err := c.RestoreManagedPrefixListVersion(ctx, &ec2.RestoreManagedPrefixListVersionInput{
		PrefixListId:    aws.String(plID),
		CurrentVersion:  aws.Int64(1),
		PreviousVersion: aws.Int64(1),
	})
	require.NoError(t, err)
	assert.Equal(t, plID, aws.ToString(restored.PrefixList.PrefixListId))
}

// TestEC2_SecurityGroupReferencesSDK covers the security-group reference /
// stale / for-vpc surfaces derived from the SG store.
func TestEC2_SecurityGroupReferencesSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.212.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)

	sg, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("sg-refs"), Description: aws.String("refs test"), VpcId: aws.String(vpcID),
	})
	require.NoError(t, err)
	sgID := aws.ToString(sg.GroupId)

	forVpc, err := c.GetSecurityGroupsForVpc(ctx, &ec2.GetSecurityGroupsForVpcInput{VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	found := false
	for _, g := range forVpc.SecurityGroupForVpcs {
		if aws.ToString(g.GroupId) == sgID {
			found = true
		}
	}
	assert.True(t, found, "the created SG is listed for its VPC")

	refs, err := c.DescribeSecurityGroupReferences(ctx, &ec2.DescribeSecurityGroupReferencesInput{
		GroupId: []string{sgID},
	})
	require.NoError(t, err)
	assert.Empty(t, refs.SecurityGroupReferenceSet, "no other SG references this group")

	stale, err := c.DescribeStaleSecurityGroups(ctx, &ec2.DescribeStaleSecurityGroupsInput{VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	assert.Empty(t, stale.StaleSecurityGroupSet, "no stale rules in a fresh VPC")
}

// TestEC2_LaunchTemplateDataSDK covers GetLaunchTemplateData (derived from an
// instance) and DeleteLaunchTemplateVersions.
func TestEC2_LaunchTemplateDataSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.213.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.213.1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String("ami-12345678"), InstanceType: types.InstanceTypeT3Small,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.NoError(t, err)
	instID := aws.ToString(run.Instances[0].InstanceId)

	data, err := c.GetLaunchTemplateData(ctx, &ec2.GetLaunchTemplateDataInput{InstanceId: aws.String(instID)})
	require.NoError(t, err)
	assert.Equal(t, "ami-12345678", aws.ToString(data.LaunchTemplateData.ImageId))
	assert.Equal(t, types.InstanceTypeT3Small, data.LaunchTemplateData.InstanceType)

	lt, err := c.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String("sdk-lt-data"),
		LaunchTemplateData: &types.RequestLaunchTemplateData{ImageId: aws.String("ami-11111111")},
	})
	require.NoError(t, err)
	ltID := aws.ToString(lt.LaunchTemplate.LaunchTemplateId)
	_, err = c.CreateLaunchTemplateVersion(ctx, &ec2.CreateLaunchTemplateVersionInput{
		LaunchTemplateId:   aws.String(ltID),
		LaunchTemplateData: &types.RequestLaunchTemplateData{ImageId: aws.String("ami-22222222")},
	})
	require.NoError(t, err)

	del, err := c.DeleteLaunchTemplateVersions(ctx, &ec2.DeleteLaunchTemplateVersionsInput{
		LaunchTemplateId: aws.String(ltID),
		Versions:         []string{"2"},
	})
	require.NoError(t, err)
	require.Len(t, del.SuccessfullyDeletedLaunchTemplateVersions, 1)
	assert.Equal(t, int64(2), aws.ToInt64(del.SuccessfullyDeletedLaunchTemplateVersions[0].VersionNumber))

	// Deleting the default version (1) fails.
	fail, err := c.DeleteLaunchTemplateVersions(ctx, &ec2.DeleteLaunchTemplateVersionsInput{
		LaunchTemplateId: aws.String(ltID),
		Versions:         []string{"1"},
	})
	require.NoError(t, err)
	require.Len(t, fail.UnsuccessfullyDeletedLaunchTemplateVersions, 1)
}

// TestEC2_Ipv6AddressesSDK covers IPv6 assign / unassign on an ENI plus
// UnassignPrivateIpAddresses and the IPv6-pool describe surface.
func TestEC2_Ipv6AddressesSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.214.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.214.1.0/24")
	eni, err := c.CreateNetworkInterface(ctx, &ec2.CreateNetworkInterfaceInput{SubnetId: subnet})
	require.NoError(t, err)
	eniID := aws.ToString(eni.NetworkInterface.NetworkInterfaceId)

	asn, err := c.AssignIpv6Addresses(ctx, &ec2.AssignIpv6AddressesInput{
		NetworkInterfaceId: aws.String(eniID), Ipv6AddressCount: aws.Int32(2),
	})
	require.NoError(t, err)
	require.Len(t, asn.AssignedIpv6Addresses, 2)
	assert.Equal(t, eniID, aws.ToString(asn.NetworkInterfaceId))

	un, err := c.UnassignIpv6Addresses(ctx, &ec2.UnassignIpv6AddressesInput{
		NetworkInterfaceId: aws.String(eniID), Ipv6Addresses: asn.AssignedIpv6Addresses,
	})
	require.NoError(t, err)
	assert.Len(t, un.UnassignedIpv6Addresses, 2)

	_, err = c.UnassignPrivateIpAddresses(ctx, &ec2.UnassignPrivateIpAddressesInput{
		NetworkInterfaceId: aws.String(eniID), PrivateIpAddresses: []string{"10.214.1.55"},
	})
	require.NoError(t, err)

	pools, err := c.DescribeIpv6Pools(ctx, &ec2.DescribeIpv6PoolsInput{})
	require.NoError(t, err)
	assert.Empty(t, pools.Ipv6Pools)
}

// TestEC2_DefaultCreditSpecificationSDK covers the default credit specification,
// AZ-group opt-in, VPC tenancy, and the ENI-attribute / DNS-name-option /
// route-table-association / VPC-endpoint / diagnostic-interrupt surfaces.
func TestEC2_DefaultCreditSpecificationSDK(t *testing.T) {
	c := ec2Client()

	got, err := c.GetDefaultCreditSpecification(ctx, &ec2.GetDefaultCreditSpecificationInput{
		InstanceFamily: types.UnlimitedSupportedInstanceFamilyT3,
	})
	require.NoError(t, err)
	assert.Equal(t, "unlimited", aws.ToString(got.InstanceFamilyCreditSpecification.CpuCredits))

	mod, err := c.ModifyDefaultCreditSpecification(ctx, &ec2.ModifyDefaultCreditSpecificationInput{
		InstanceFamily: types.UnlimitedSupportedInstanceFamilyT3, CpuCredits: aws.String("standard"),
	})
	require.NoError(t, err)
	assert.Equal(t, "standard", aws.ToString(mod.InstanceFamilyCreditSpecification.CpuCredits))

	az, err := c.ModifyAvailabilityZoneGroup(ctx, &ec2.ModifyAvailabilityZoneGroupInput{
		GroupName: aws.String("us-east-1-wl1-bos-wlz-1"), OptInStatus: types.ModifyAvailabilityZoneOptInStatusOptedIn,
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(az.Return))

	// VPC tenancy + DNS-name-options + ENI attribute + route-table association +
	// VPC endpoint + diagnostic interrupt, exercised over real resources.
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.215.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	tenancy, err := c.ModifyVpcTenancy(ctx, &ec2.ModifyVpcTenancyInput{
		VpcId: aws.String(vpcID), InstanceTenancy: types.VpcTenancyDefault,
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(tenancy.ReturnValue))

	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.215.1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String("ami-12345678"), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.NoError(t, err)
	instID := aws.ToString(run.Instances[0].InstanceId)
	dns, err := c.ModifyPrivateDnsNameOptions(ctx, &ec2.ModifyPrivateDnsNameOptionsInput{
		InstanceId: aws.String(instID), EnableResourceNameDnsARecord: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(dns.Return))
	_, err = c.SendDiagnosticInterrupt(ctx, &ec2.SendDiagnosticInterruptInput{InstanceId: aws.String(instID)})
	require.NoError(t, err)

	eni, err := c.CreateNetworkInterface(ctx, &ec2.CreateNetworkInterfaceInput{SubnetId: subnet})
	require.NoError(t, err)
	eniID := aws.ToString(eni.NetworkInterface.NetworkInterfaceId)
	attr, err := c.DescribeNetworkInterfaceAttribute(ctx, &ec2.DescribeNetworkInterfaceAttributeInput{
		NetworkInterfaceId: aws.String(eniID), Attribute: types.NetworkInterfaceAttributeSourceDestCheck,
	})
	require.NoError(t, err)
	require.NotNil(t, attr.SourceDestCheck)
	assert.True(t, aws.ToBool(attr.SourceDestCheck.Value))
	_, err = c.ResetNetworkInterfaceAttribute(ctx, &ec2.ResetNetworkInterfaceAttributeInput{
		NetworkInterfaceId: aws.String(eniID), SourceDestCheck: aws.String("sourceDestCheck"),
	})
	require.NoError(t, err)
	_, err = c.ModifyPublicIpDnsNameOptions(ctx, &ec2.ModifyPublicIpDnsNameOptionsInput{
		NetworkInterfaceId: aws.String(eniID), HostnameType: types.PublicIpDnsOptionPublicDualStackDnsName,
	})
	require.NoError(t, err)

	// Route-table association replacement.
	rt, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	rtID := aws.ToString(rt.RouteTable.RouteTableId)
	assoc, err := c.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{
		RouteTableId: aws.String(rtID), SubnetId: subnet,
	})
	require.NoError(t, err)
	rt2, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	repl, err := c.ReplaceRouteTableAssociation(ctx, &ec2.ReplaceRouteTableAssociationInput{
		AssociationId: assoc.AssociationId, RouteTableId: rt2.RouteTable.RouteTableId,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(repl.NewAssociationId))

	// VPC endpoint modify.
	ep, err := c.CreateVpcEndpoint(ctx, &ec2.CreateVpcEndpointInput{
		VpcId: aws.String(vpcID), ServiceName: aws.String("com.amazonaws.us-east-1.s3"),
		VpcEndpointType: types.VpcEndpointTypeGateway,
	})
	require.NoError(t, err)
	em, err := c.ModifyVpcEndpoint(ctx, &ec2.ModifyVpcEndpointInput{
		VpcEndpointId:    ep.VpcEndpoint.VpcEndpointId,
		AddRouteTableIds: []string{rtID},
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(em.Return))
}

// TestEC2_InterruptibleAndTasksSDK covers the interruptible capacity-reservation
// allocation and export/import-task surfaces.
func TestEC2_InterruptibleAndTasksSDK(t *testing.T) {
	c := ec2Client()

	alloc, err := c.CreateInterruptibleCapacityReservationAllocation(ctx, &ec2.CreateInterruptibleCapacityReservationAllocationInput{
		CapacityReservationId: aws.String("cr-0123456789abcdef0"),
		InstanceCount:         aws.Int32(3),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), aws.ToInt32(alloc.TargetInstanceCount))

	upd, err := c.UpdateInterruptibleCapacityReservationAllocation(ctx, &ec2.UpdateInterruptibleCapacityReservationAllocationInput{
		CapacityReservationId: aws.String("cr-0123456789abcdef0"),
		TargetInstanceCount:   aws.Int32(5),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(5), aws.ToInt32(upd.TargetInstanceCount))

	tasks, err := c.DescribeExportTasks(ctx, &ec2.DescribeExportTasksInput{})
	require.NoError(t, err)
	assert.Empty(t, tasks.ExportTasks)

	_, err = c.CancelExportTask(ctx, &ec2.CancelExportTaskInput{ExportTaskId: aws.String("export-i-0123456789abcdef0")})
	require.NoError(t, err)

	imp, err := c.CancelImportTask(ctx, &ec2.CancelImportTaskInput{ImportTaskId: aws.String("import-ami-0123456789abcdef0")})
	require.NoError(t, err)
	assert.Equal(t, "import-ami-0123456789abcdef0", aws.ToString(imp.ImportTaskId))

	// GetAssociatedIpv6PoolCidrs read-back over a (non-existent) pool id is an
	// empty association set.
	cidrs, err := c.GetAssociatedIpv6PoolCidrs(ctx, &ec2.GetAssociatedIpv6PoolCidrsInput{PoolId: aws.String("ipv6pool-ec2-0123456789abcdef0")})
	require.NoError(t, err)
	assert.Empty(t, cidrs.Ipv6CidrAssociations)
}
