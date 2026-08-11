package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_HostsLifecycleSDK exercises the Dedicated Host control plane:
// AllocateHosts reserves hosts in an AZ for an instance family, DescribeHosts
// reads them back (id / state / AZ / family), ModifyHosts flips auto-placement
// + recovery, DescribeMacHosts filters mac-only hosts (empty here), and
// ReleaseHosts decommissions them.
func TestEC2_HostsLifecycleSDK(t *testing.T) {
	c := ec2Client()

	alloc, err := c.AllocateHosts(ctx, &ec2.AllocateHostsInput{
		AvailabilityZone: aws.String("us-east-1a"),
		InstanceFamily:   aws.String("m5"),
		Quantity:         aws.Int32(2),
		AutoPlacement:    types.AutoPlacementOff,
		HostRecovery:     types.HostRecoveryOff,
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeDedicatedHost,
			Tags:         []types.Tag{{Key: aws.String("team"), Value: aws.String("ci")}},
		}},
	})
	require.NoError(t, err)
	require.Len(t, alloc.HostIds, 2)
	hostID := alloc.HostIds[0]
	defer func() { _, _ = c.ReleaseHosts(ctx, &ec2.ReleaseHostsInput{HostIds: alloc.HostIds}) }()

	desc, err := c.DescribeHosts(ctx, &ec2.DescribeHostsInput{HostIds: []string{hostID}})
	require.NoError(t, err)
	require.Len(t, desc.Hosts, 1)
	h := desc.Hosts[0]
	assert.Equal(t, hostID, aws.ToString(h.HostId))
	assert.Equal(t, types.AllocationStateAvailable, h.State)
	assert.Equal(t, "us-east-1a", aws.ToString(h.AvailabilityZone))
	require.NotNil(t, h.HostProperties)
	assert.Equal(t, "m5", aws.ToString(h.HostProperties.InstanceFamily))
	// Tag round-trips through DescribeHosts.
	var foundTag bool
	for _, tg := range h.Tags {
		if aws.ToString(tg.Key) == "team" && aws.ToString(tg.Value) == "ci" {
			foundTag = true
		}
	}
	assert.True(t, foundTag, "expected team=ci tag on host")

	mod, err := c.ModifyHosts(ctx, &ec2.ModifyHostsInput{
		HostIds:       []string{hostID},
		AutoPlacement: types.AutoPlacementOn,
		HostRecovery:  types.HostRecoveryOn,
	})
	require.NoError(t, err)
	assert.Contains(t, mod.Successful, hostID)

	desc2, err := c.DescribeHosts(ctx, &ec2.DescribeHostsInput{HostIds: []string{hostID}})
	require.NoError(t, err)
	require.Len(t, desc2.Hosts, 1)
	assert.Equal(t, types.AutoPlacementOn, desc2.Hosts[0].AutoPlacement)

	// DescribeMacHosts is a faithful empty list (no mac hosts allocated).
	macs, err := c.DescribeMacHosts(ctx, &ec2.DescribeMacHostsInput{})
	require.NoError(t, err)
	assert.Empty(t, macs.MacHosts)

	rel, err := c.ReleaseHosts(ctx, &ec2.ReleaseHostsInput{HostIds: alloc.HostIds})
	require.NoError(t, err)
	assert.Len(t, rel.Successful, 2)
}

// TestEC2_ImageAttrLifecycleSDK covers per-AMI attribute state: a private AMI
// gets a launchPermission granting a second account + the "all" group (going
// public), DescribeImageAttribute reads it back, the description attribute is
// settable + readable, then ResetImageAttribute reverts launchPermission. Also
// flips DisableImage/EnableImage state and runs the image import/export +
// restore tasks.
func TestEC2_ImageAttrLifecycleSDK(t *testing.T) {
	c := ec2Client()

	reg, err := c.RegisterImage(ctx, &ec2.RegisterImageInput{
		Name:           aws.String("attr-test-ami"),
		Architecture:   types.ArchitectureValuesX8664,
		RootDeviceName: aws.String("/dev/sda1"),
	})
	require.NoError(t, err)
	amiID := aws.ToString(reg.ImageId)

	_, err = c.ModifyImageAttribute(ctx, &ec2.ModifyImageAttributeInput{
		ImageId:       aws.String(amiID),
		OperationType: types.OperationTypeAdd,
		Attribute:     aws.String("launchPermission"),
		UserIds:       []string{"210987654321"},
		UserGroups:    []string{"all"},
	})
	require.NoError(t, err)

	la, err := c.DescribeImageAttribute(ctx, &ec2.DescribeImageAttributeInput{
		ImageId:   aws.String(amiID),
		Attribute: types.ImageAttributeNameLaunchPermission,
	})
	require.NoError(t, err)
	var sawUser, sawGroup bool
	for _, p := range la.LaunchPermissions {
		if aws.ToString(p.UserId) == "210987654321" {
			sawUser = true
		}
		if p.Group == types.PermissionGroupAll {
			sawGroup = true
		}
	}
	assert.True(t, sawUser, "expected launch permission for user")
	assert.True(t, sawGroup, "expected launch permission for all group")

	// Description attribute round-trip.
	_, err = c.ModifyImageAttribute(ctx, &ec2.ModifyImageAttributeInput{
		ImageId:     aws.String(amiID),
		Attribute:   aws.String("description"),
		Description: &types.AttributeValue{Value: aws.String("golden image")},
	})
	require.NoError(t, err)
	da, err := c.DescribeImageAttribute(ctx, &ec2.DescribeImageAttributeInput{
		ImageId:   aws.String(amiID),
		Attribute: types.ImageAttributeNameDescription,
	})
	require.NoError(t, err)
	require.NotNil(t, da.Description)
	assert.Equal(t, "golden image", aws.ToString(da.Description.Value))

	_, err = c.ResetImageAttribute(ctx, &ec2.ResetImageAttributeInput{
		ImageId:   aws.String(amiID),
		Attribute: types.ResetImageAttributeNameLaunchPermission,
	})
	require.NoError(t, err)
	la2, err := c.DescribeImageAttribute(ctx, &ec2.DescribeImageAttributeInput{
		ImageId:   aws.String(amiID),
		Attribute: types.ImageAttributeNameLaunchPermission,
	})
	require.NoError(t, err)
	assert.Empty(t, la2.LaunchPermissions)

	// Disable/Enable flips state.
	dis, err := c.DisableImage(ctx, &ec2.DisableImageInput{ImageId: aws.String(amiID)})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(dis.Return))
	en, err := c.EnableImage(ctx, &ec2.EnableImageInput{ImageId: aws.String(amiID)})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(en.Return))

	// ExportImage / ImportImage / CreateRestoreImageTask /
	// RestoreImageFromRecycleBin task ops.
	exp, err := c.ExportImage(ctx, &ec2.ExportImageInput{
		ImageId:         aws.String(amiID),
		DiskImageFormat: types.DiskImageFormatVmdk,
		S3ExportLocation: &types.ExportTaskS3LocationRequest{
			S3Bucket: aws.String("my-bucket"),
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(exp.ExportImageTaskId))
	assert.Equal(t, amiID, aws.ToString(exp.ImageId))

	imp, err := c.ImportImage(ctx, &ec2.ImportImageInput{
		Architecture: aws.String("x86_64"),
		Description:  aws.String("imported"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(imp.ImportTaskId))

	cr, err := c.CreateRestoreImageTask(ctx, &ec2.CreateRestoreImageTaskInput{
		ObjectKey: aws.String("ami-backup"),
		Bucket:    aws.String("my-bucket"),
		Name:      aws.String("restored"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(cr.ImageId))

	rb, err := c.RestoreImageFromRecycleBin(ctx, &ec2.RestoreImageFromRecycleBinInput{ImageId: aws.String(amiID)})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(rb.Return))
}

// TestEC2_SnapshotAttrLifecycleSDK covers per-snapshot createVolumePermission
// state and the snapshot tier/lock/import ops over a real snapshot.
func TestEC2_SnapshotAttrLifecycleSDK(t *testing.T) {
	c := ec2Client()

	vol, err := c.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"),
		Size:             aws.Int32(8),
	})
	require.NoError(t, err)
	snap, err := c.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{VolumeId: vol.VolumeId})
	require.NoError(t, err)
	snapID := aws.ToString(snap.SnapshotId)

	_, err = c.ModifySnapshotAttribute(ctx, &ec2.ModifySnapshotAttributeInput{
		SnapshotId:    aws.String(snapID),
		Attribute:     types.SnapshotAttributeNameCreateVolumePermission,
		OperationType: types.OperationTypeAdd,
		UserIds:       []string{"210987654321"},
	})
	require.NoError(t, err)

	sa, err := c.DescribeSnapshotAttribute(ctx, &ec2.DescribeSnapshotAttributeInput{
		SnapshotId: aws.String(snapID),
		Attribute:  types.SnapshotAttributeNameCreateVolumePermission,
	})
	require.NoError(t, err)
	assert.Equal(t, snapID, aws.ToString(sa.SnapshotId))
	require.Len(t, sa.CreateVolumePermissions, 1)
	assert.Equal(t, "210987654321", aws.ToString(sa.CreateVolumePermissions[0].UserId))

	_, err = c.ResetSnapshotAttribute(ctx, &ec2.ResetSnapshotAttributeInput{
		SnapshotId: aws.String(snapID),
		Attribute:  types.SnapshotAttributeNameCreateVolumePermission,
	})
	require.NoError(t, err)
	sa2, err := c.DescribeSnapshotAttribute(ctx, &ec2.DescribeSnapshotAttributeInput{
		SnapshotId: aws.String(snapID),
		Attribute:  types.SnapshotAttributeNameCreateVolumePermission,
	})
	require.NoError(t, err)
	assert.Empty(t, sa2.CreateVolumePermissions)

	ts, err := c.DescribeSnapshotTierStatus(ctx, &ec2.DescribeSnapshotTierStatusInput{})
	require.NoError(t, err)
	var found bool
	for _, st := range ts.SnapshotTierStatuses {
		if aws.ToString(st.SnapshotId) == snapID {
			found = true
			assert.Equal(t, types.StorageTierStandard, st.StorageTier)
		}
	}
	assert.True(t, found, "expected snapshot in tier status")

	lk, err := c.LockSnapshot(ctx, &ec2.LockSnapshotInput{
		SnapshotId:   aws.String(snapID),
		LockMode:     types.LockModeGovernance,
		LockDuration: aws.Int32(1),
	})
	require.NoError(t, err)
	assert.Equal(t, snapID, aws.ToString(lk.SnapshotId))
	assert.Equal(t, types.LockStateGovernance, lk.LockState)

	ul, err := c.UnlockSnapshot(ctx, &ec2.UnlockSnapshotInput{SnapshotId: aws.String(snapID)})
	require.NoError(t, err)
	assert.Equal(t, snapID, aws.ToString(ul.SnapshotId))

	is, err := c.ImportSnapshot(ctx, &ec2.ImportSnapshotInput{
		Description: aws.String("imported-snap"),
		DiskContainer: &types.SnapshotDiskContainer{
			Format:     aws.String("VMDK"),
			UserBucket: &types.UserBucket{S3Bucket: aws.String("b"), S3Key: aws.String("k")},
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(is.ImportTaskId))
	require.NotNil(t, is.SnapshotTaskDetail)
	assert.NotEmpty(t, aws.ToString(is.SnapshotTaskDetail.SnapshotId))
}

// TestEC2_InstanceEventWindowLifecycleSDK covers the maintenance event window
// CRUD: create with a weekly time range, describe, modify the name, associate
// + disassociate an instance target, and delete.
func TestEC2_InstanceEventWindowLifecycleSDK(t *testing.T) {
	c := ec2Client()

	cw, err := c.CreateInstanceEventWindow(ctx, &ec2.CreateInstanceEventWindowInput{
		Name: aws.String("weekend-maint"),
		TimeRanges: []types.InstanceEventWindowTimeRangeRequest{{
			StartWeekDay: types.WeekDaySunday,
			StartHour:    aws.Int32(2),
			EndWeekDay:   types.WeekDaySunday,
			EndHour:      aws.Int32(4),
		}},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeInstanceEventWindow,
			Tags:         []types.Tag{{Key: aws.String("env"), Value: aws.String("prod")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, cw.InstanceEventWindow)
	iewID := aws.ToString(cw.InstanceEventWindow.InstanceEventWindowId)
	require.NotEmpty(t, iewID)
	require.Len(t, cw.InstanceEventWindow.TimeRanges, 1)
	assert.Equal(t, types.WeekDaySunday, cw.InstanceEventWindow.TimeRanges[0].StartWeekDay)
	assert.Equal(t, int32(2), aws.ToInt32(cw.InstanceEventWindow.TimeRanges[0].StartHour))
	defer func() {
		_, _ = c.DeleteInstanceEventWindow(ctx, &ec2.DeleteInstanceEventWindowInput{InstanceEventWindowId: aws.String(iewID)})
	}()

	desc, err := c.DescribeInstanceEventWindows(ctx, &ec2.DescribeInstanceEventWindowsInput{
		InstanceEventWindowIds: []string{iewID},
	})
	require.NoError(t, err)
	require.Len(t, desc.InstanceEventWindows, 1)
	assert.Equal(t, "weekend-maint", aws.ToString(desc.InstanceEventWindows[0].Name))

	mod, err := c.ModifyInstanceEventWindow(ctx, &ec2.ModifyInstanceEventWindowInput{
		InstanceEventWindowId: aws.String(iewID),
		Name:                  aws.String("renamed-maint"),
	})
	require.NoError(t, err)
	assert.Equal(t, "renamed-maint", aws.ToString(mod.InstanceEventWindow.Name))

	as, err := c.AssociateInstanceEventWindow(ctx, &ec2.AssociateInstanceEventWindowInput{
		InstanceEventWindowId: aws.String(iewID),
		AssociationTarget: &types.InstanceEventWindowAssociationRequest{
			InstanceIds: []string{"i-0123456789abcdef0"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, as.InstanceEventWindow.AssociationTarget)
	assert.Contains(t, as.InstanceEventWindow.AssociationTarget.InstanceIds, "i-0123456789abcdef0")

	dis, err := c.DisassociateInstanceEventWindow(ctx, &ec2.DisassociateInstanceEventWindowInput{
		InstanceEventWindowId: aws.String(iewID),
		AssociationTarget: &types.InstanceEventWindowDisassociationRequest{
			InstanceIds: []string{"i-0123456789abcdef0"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, dis.InstanceEventWindow)
	if dis.InstanceEventWindow.AssociationTarget != nil {
		assert.NotContains(t, dis.InstanceEventWindow.AssociationTarget.InstanceIds, "i-0123456789abcdef0")
	}

	del, err := c.DeleteInstanceEventWindow(ctx, &ec2.DeleteInstanceEventWindowInput{
		InstanceEventWindowId: aws.String(iewID),
	})
	require.NoError(t, err)
	require.NotNil(t, del.InstanceEventWindowState)
	assert.Equal(t, iewID, aws.ToString(del.InstanceEventWindowState.InstanceEventWindowId))
}

// TestEC2_VpcClassicLinkSDK covers the per-VPC ClassicLink + ClassicLink-DNS
// flags through enable/describe/disable.
func TestEC2_VpcClassicLinkSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.180.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)

	en, err := c.EnableVpcClassicLink(ctx, &ec2.EnableVpcClassicLinkInput{VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(en.Return))

	desc, err := c.DescribeVpcClassicLink(ctx, &ec2.DescribeVpcClassicLinkInput{VpcIds: []string{vpcID}})
	require.NoError(t, err)
	require.Len(t, desc.Vpcs, 1)
	assert.True(t, aws.ToBool(desc.Vpcs[0].ClassicLinkEnabled))
	assert.Equal(t, vpcID, aws.ToString(desc.Vpcs[0].VpcId))

	_, err = c.EnableVpcClassicLinkDnsSupport(ctx, &ec2.EnableVpcClassicLinkDnsSupportInput{VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	dns, err := c.DescribeVpcClassicLinkDnsSupport(ctx, &ec2.DescribeVpcClassicLinkDnsSupportInput{VpcIds: []string{vpcID}})
	require.NoError(t, err)
	var dnsEnabled bool
	for _, v := range dns.Vpcs {
		if aws.ToString(v.VpcId) == vpcID {
			dnsEnabled = aws.ToBool(v.ClassicLinkDnsSupported)
		}
	}
	assert.True(t, dnsEnabled)

	dis, err := c.DisableVpcClassicLink(ctx, &ec2.DisableVpcClassicLinkInput{VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(dis.Return))
	_, err = c.DisableVpcClassicLinkDnsSupport(ctx, &ec2.DisableVpcClassicLinkDnsSupportInput{VpcId: aws.String(vpcID)})
	require.NoError(t, err)

	desc2, err := c.DescribeVpcClassicLink(ctx, &ec2.DescribeVpcClassicLinkInput{VpcIds: []string{vpcID}})
	require.NoError(t, err)
	require.Len(t, desc2.Vpcs, 1)
	assert.False(t, aws.ToBool(desc2.Vpcs[0].ClassicLinkEnabled))
}

// TestEC2_VpcEndpointConnectionNotificationsSDK covers the connection
// notification CRUD (the endpoint connection accept/reject ops are covered by
// their empty-list happy path here too).
func TestEC2_VpcEndpointConnectionNotificationsSDK(t *testing.T) {
	c := ec2Client()

	cn, err := c.CreateVpcEndpointConnectionNotification(ctx, &ec2.CreateVpcEndpointConnectionNotificationInput{
		ConnectionNotificationArn: aws.String("arn:aws:sns:us-east-1:123456789012:vpce-events"),
		ServiceId:                 aws.String("vpce-svc-0123456789abcdef0"),
		ConnectionEvents:          []string{"Accept", "Reject"},
	})
	require.NoError(t, err)
	require.NotNil(t, cn.ConnectionNotification)
	nfnID := aws.ToString(cn.ConnectionNotification.ConnectionNotificationId)
	require.NotEmpty(t, nfnID)
	defer func() {
		_, _ = c.DeleteVpcEndpointConnectionNotifications(ctx, &ec2.DeleteVpcEndpointConnectionNotificationsInput{
			ConnectionNotificationIds: []string{nfnID},
		})
	}()

	desc, err := c.DescribeVpcEndpointConnectionNotifications(ctx, &ec2.DescribeVpcEndpointConnectionNotificationsInput{
		ConnectionNotificationId: aws.String(nfnID),
	})
	require.NoError(t, err)
	require.Len(t, desc.ConnectionNotificationSet, 1)
	assert.Equal(t, "arn:aws:sns:us-east-1:123456789012:vpce-events",
		aws.ToString(desc.ConnectionNotificationSet[0].ConnectionNotificationArn))

	_, err = c.ModifyVpcEndpointConnectionNotification(ctx, &ec2.ModifyVpcEndpointConnectionNotificationInput{
		ConnectionNotificationId: aws.String(nfnID),
		ConnectionEvents:         []string{"Accept", "Reject", "Connect"},
	})
	require.NoError(t, err)

	// DescribeVpcEndpointConnections is a faithful empty list (no connections).
	conns, err := c.DescribeVpcEndpointConnections(ctx, &ec2.DescribeVpcEndpointConnectionsInput{})
	require.NoError(t, err)
	assert.Empty(t, conns.VpcEndpointConnections)

	// Accept/Reject with no matching connection returns Unsuccessful entries.
	acc, err := c.AcceptVpcEndpointConnections(ctx, &ec2.AcceptVpcEndpointConnectionsInput{
		ServiceId:      aws.String("vpce-svc-0123456789abcdef0"),
		VpcEndpointIds: []string{"vpce-missing"},
	})
	require.NoError(t, err)
	assert.Len(t, acc.Unsuccessful, 1)
	rej, err := c.RejectVpcEndpointConnections(ctx, &ec2.RejectVpcEndpointConnectionsInput{
		ServiceId:      aws.String("vpce-svc-0123456789abcdef0"),
		VpcEndpointIds: []string{"vpce-missing"},
	})
	require.NoError(t, err)
	assert.Len(t, rej.Unsuccessful, 1)

	del, err := c.DeleteVpcEndpointConnectionNotifications(ctx, &ec2.DeleteVpcEndpointConnectionNotificationsInput{
		ConnectionNotificationIds: []string{nfnID},
	})
	require.NoError(t, err)
	assert.Empty(t, del.Unsuccessful)
}

// TestEC2_VpcBlockPublicAccessSDK covers the account-level BPA options
// (modify/describe) and the per-resource BPA exclusions CRUD.
func TestEC2_VpcBlockPublicAccessSDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.181.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)

	mod, err := c.ModifyVpcBlockPublicAccessOptions(ctx, &ec2.ModifyVpcBlockPublicAccessOptionsInput{
		InternetGatewayBlockMode: types.InternetGatewayBlockModeBlockBidirectional,
	})
	require.NoError(t, err)
	require.NotNil(t, mod.VpcBlockPublicAccessOptions)
	assert.Equal(t, types.InternetGatewayBlockModeBlockBidirectional, mod.VpcBlockPublicAccessOptions.InternetGatewayBlockMode)

	opts, err := c.DescribeVpcBlockPublicAccessOptions(ctx, &ec2.DescribeVpcBlockPublicAccessOptionsInput{})
	require.NoError(t, err)
	require.NotNil(t, opts.VpcBlockPublicAccessOptions)
	assert.Equal(t, types.InternetGatewayBlockModeBlockBidirectional, opts.VpcBlockPublicAccessOptions.InternetGatewayBlockMode)
	assert.Equal(t, "123456789012", aws.ToString(opts.VpcBlockPublicAccessOptions.AwsAccountId))

	ex, err := c.CreateVpcBlockPublicAccessExclusion(ctx, &ec2.CreateVpcBlockPublicAccessExclusionInput{
		VpcId:                        aws.String(vpcID),
		InternetGatewayExclusionMode: types.InternetGatewayExclusionModeAllowBidirectional,
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeVpcBlockPublicAccessExclusion,
			Tags:         []types.Tag{{Key: aws.String("k"), Value: aws.String("v")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, ex.VpcBlockPublicAccessExclusion)
	exID := aws.ToString(ex.VpcBlockPublicAccessExclusion.ExclusionId)
	require.NotEmpty(t, exID)

	desc, err := c.DescribeVpcBlockPublicAccessExclusions(ctx, &ec2.DescribeVpcBlockPublicAccessExclusionsInput{
		ExclusionIds: []string{exID},
	})
	require.NoError(t, err)
	require.Len(t, desc.VpcBlockPublicAccessExclusions, 1)
	assert.Equal(t, types.InternetGatewayExclusionModeAllowBidirectional,
		desc.VpcBlockPublicAccessExclusions[0].InternetGatewayExclusionMode)

	modEx, err := c.ModifyVpcBlockPublicAccessExclusion(ctx, &ec2.ModifyVpcBlockPublicAccessExclusionInput{
		ExclusionId:                  aws.String(exID),
		InternetGatewayExclusionMode: types.InternetGatewayExclusionModeAllowEgress,
	})
	require.NoError(t, err)
	assert.Equal(t, types.InternetGatewayExclusionModeAllowEgress,
		modEx.VpcBlockPublicAccessExclusion.InternetGatewayExclusionMode)

	delEx, err := c.DeleteVpcBlockPublicAccessExclusion(ctx, &ec2.DeleteVpcBlockPublicAccessExclusionInput{
		ExclusionId: aws.String(exID),
	})
	require.NoError(t, err)
	require.NotNil(t, delEx.VpcBlockPublicAccessExclusion)
}
