package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// This file implements the EC2 volume / snapshot / recycle-bin / Customer-Owned
// IP (CoIP) / default-VPC / managed-prefix-list / security-group-reference /
// launch-template-data / DNS-name-options / Mac-dedicated-host / IPv6 families.
//
// EC2 speaks the ec2Query protocol: actions are registered by name and respond
// with XML whose element/list casing matches the smithy model's ec2QueryName /
// xmlName traits exactly. Every handler is faithful CRUD over the real
// sim.Stores defined across the other ec2*.go files (volumes, snapshots, ENIs,
// VPCs, subnets, security groups, prefix lists, launch templates, instances,
// CoIP pools), with no synthetic shortcuts.

// EC2 lacks first-class IPv6 fields on the existing EC2NetworkInterface struct,
// so the IPv6 addresses/prefixes assigned to an ENI live in this id-keyed side
// store, the same pattern ec2_ebs_snapshot.go uses for a volume's autoEnableIO
// attribute.
type ec2ENIIPv6State struct {
	Addresses []string
	Prefixes  []string
}

var ec2ENIIPv6States sim.Store[ec2ENIIPv6State]

// Replace-root-volume, Mac-modification, and recycled-volume records are kept in
// dedicated persisted stores so a Describe* / List* reads them back.
var (
	ec2ReplaceRootVolumeTasks sim.Store[EC2ReplaceRootVolumeTask]
	ec2MacModificationTasks   sim.Store[EC2MacModificationTask]
	ec2RecycledVolumes        sim.Store[EC2Volume]
)

// EC2ReplaceRootVolumeTask mirrors the SDK ReplaceRootVolumeTask shape.
type EC2ReplaceRootVolumeTask struct {
	ReplaceRootVolumeTaskId  string
	InstanceId               string
	TaskState                string
	StartTime                string
	CompleteTime             string
	ImageId                  string
	SnapshotId               string
	DeleteReplacedRootVolume bool
	Tags                     []EC2Tag
}

// EC2MacModificationTask mirrors the SDK MacModificationTask shape. TaskType is
// one of sip-modification | volume-ownership-delegation.
type EC2MacModificationTask struct {
	MacModificationTaskId string
	InstanceId            string
	TaskState             string
	TaskType              string
	StartTime             string
	SipConfig             []EC2MacSipSetting
	Tags                  []EC2Tag
}

type EC2MacSipSetting struct {
	Name  string
	Value string
}

func registerEC2VolumesMisc(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2ReplaceRootVolumeTasks = sim.MakeStore[EC2ReplaceRootVolumeTask](srv.DB(), "ec2_replace_root_volume_tasks")
	ec2MacModificationTasks = sim.MakeStore[EC2MacModificationTask](srv.DB(), "ec2_mac_modification_tasks")
	ec2RecycledVolumes = sim.MakeStore[EC2Volume](srv.DB(), "ec2_recycled_volumes")
	ec2ENIIPv6States = sim.MakeStore[ec2ENIIPv6State](srv.DB(), "ec2_eni_ipv6_states")
	ec2DefaultCredit = sim.MakeStore[string](srv.DB(), "ec2_default_credit_specifications")

	for action, h := range map[string]http.HandlerFunc{
		// Volume / snapshot operations.
		"CreateSnapshots":      handleCreateSnapshots,
		"CopyVolumes":          handleCopyVolumes,
		"DescribeVolumeStatus": handleDescribeVolumeStatus,
		"EnableVolumeIO":       handleEnableVolumeIO,
		"ImportVolume":         handleImportVolume,

		// Recycle Bin.
		"RestoreVolumeFromRecycleBin": handleRestoreVolumeFromRecycleBin,
		"ListVolumesInRecycleBin":     handleListVolumesInRecycleBin,
		"ListSnapshotsInRecycleBin":   handleListSnapshotsInRecycleBin,
		"DescribeLockedSnapshots":     handleDescribeLockedSnapshots,
		"DescribeImportSnapshotTasks": handleDescribeImportSnapshotTasks,

		// Replace-root-volume + Mac-dedicated-host tasks.
		"CreateReplaceRootVolumeTask":                        handleCreateReplaceRootVolumeTask,
		"DescribeReplaceRootVolumeTasks":                     handleDescribeReplaceRootVolumeTasks,
		"CreateDelegateMacVolumeOwnershipTask":               handleCreateDelegateMacVolumeOwnershipTask,
		"CreateMacSystemIntegrityProtectionModificationTask": handleCreateMacSipModificationTask,
		"DescribeMacModificationTasks":                       handleDescribeMacModificationTasks,

		// Customer-Owned IP (CoIP) CIDRs.
		"CreateCoipCidr": handleCreateCoipCidr,
		"DeleteCoipCidr": handleDeleteCoipCidr,

		// Default VPC / subnet.
		"CreateDefaultVpc":    handleCreateDefaultVpc,
		"CreateDefaultSubnet": handleCreateDefaultSubnet,

		// Managed / AWS-managed prefix lists.
		"DescribePrefixLists":              handleDescribePrefixLists,
		"GetManagedPrefixListAssociations": handleGetManagedPrefixListAssociations,
		"RestoreManagedPrefixListVersion":  handleRestoreManagedPrefixListVersion,

		// Security-group references / stale / for-vpc.
		"DescribeSecurityGroupReferences": handleDescribeSecurityGroupReferences,
		"DescribeStaleSecurityGroups":     handleDescribeStaleSecurityGroups,
		"GetSecurityGroupsForVpc":         handleGetSecurityGroupsForVpc,

		// Launch-template data + version delete.
		"GetLaunchTemplateData":        handleGetLaunchTemplateData,
		"DeleteLaunchTemplateVersions": handleDeleteLaunchTemplateVersions,

		// DNS-name-options + VPC endpoint + route-table association + ENI attribute.
		"ModifyPrivateDnsNameOptions":       handleModifyPrivateDnsNameOptions,
		"ModifyPublicIpDnsNameOptions":      handleModifyPublicIpDnsNameOptions,
		"ModifyVpcEndpoint":                 handleModifyVpcEndpoint,
		"ReplaceRouteTableAssociation":      handleReplaceRouteTableAssociation,
		"ResetNetworkInterfaceAttribute":    handleResetNetworkInterfaceAttribute,
		"DescribeNetworkInterfaceAttribute": handleDescribeNetworkInterfaceAttribute,
		"SendDiagnosticInterrupt":           handleSendDiagnosticInterrupt,

		// IPv6 address management.
		"AssignIpv6Addresses":        handleAssignIpv6Addresses,
		"UnassignIpv6Addresses":      handleUnassignIpv6Addresses,
		"DescribeIpv6Pools":          handleDescribeIpv6Pools,
		"GetAssociatedIpv6PoolCidrs": handleGetAssociatedIpv6PoolCidrs,
		"UnassignPrivateIpAddresses": handleUnassignPrivateIpAddresses,

		// Availability-zone group / default credit specification / VPC tenancy.
		"ModifyAvailabilityZoneGroup":      handleModifyAvailabilityZoneGroup,
		"GetDefaultCreditSpecification":    handleGetDefaultCreditSpecification,
		"ModifyDefaultCreditSpecification": handleModifyDefaultCreditSpecification,
		"ModifyVpcTenancy":                 handleModifyVpcTenancy,

		// Interruptible capacity-reservation allocation.
		"CreateInterruptibleCapacityReservationAllocation": handleCreateInterruptibleCapacityReservationAllocation,
		"UpdateInterruptibleCapacityReservationAllocation": handleUpdateInterruptibleCapacityReservationAllocation,

		// Export / import tasks.
		"CancelExportTask":    handleCancelExportTask,
		"DescribeExportTasks": handleDescribeExportTasks,
		"CancelImportTask":    handleCancelImportTask,
	} {
		r.Register(action, h)
	}
}

// ----------------------------------------------------------------------------
// CreateSnapshots — multi-volume snapshot set for an instance.
// ----------------------------------------------------------------------------

// handleCreateSnapshots snapshots every EBS volume attached to the instance
// named by InstanceSpecification.InstanceId (optionally excluding the boot
// volume or named data volumes), returning the resulting SnapshotInfo set.
func handleCreateSnapshots(w http.ResponseWriter, r *http.Request) {
	instID := r.FormValue("InstanceSpecification.InstanceId")
	if instID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter InstanceSpecification.InstanceId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Instances.Get(instID); !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID %q does not exist", instID), http.StatusBadRequest)
		return
	}
	excludeBoot := ec2BoolStr(r.FormValue("InstanceSpecification.ExcludeBootVolume"))
	excluded := map[string]bool{}
	for _, id := range ec2ParamList(r, "InstanceSpecification.ExcludeDataVolumeId") {
		excluded[id] = true
	}
	desc := r.FormValue("Description")
	tags := parseTags(r)
	now := time.Now().UTC()

	// Collect the instance's attached volumes from the volume store. The boot
	// volume is the one attached at /dev/sda1 or /dev/xvda.
	var snapshots []EC2Snapshot
	for _, vol := range ec2Volumes.List() {
		attached := false
		boot := false
		for _, att := range vol.Attachments {
			if att.InstanceId == instID {
				attached = true
				if att.Device == "/dev/sda1" || att.Device == "/dev/xvda" {
					boot = true
				}
			}
		}
		if !attached {
			continue
		}
		if excludeBoot && boot {
			continue
		}
		if excluded[vol.VolumeId] {
			continue
		}
		snap := EC2Snapshot{
			SnapshotId:    ec2ID("snap"),
			VolumeId:      vol.VolumeId,
			VolumeSize:    vol.Size,
			State:         "pending",
			StartTime:     now.Format(time.RFC3339),
			CompletionDue: now.Add(100 * time.Millisecond).Format(time.RFC3339Nano),
			Progress:      "0%",
			Description:   desc,
			OwnerId:       ec2Owner(),
			Encrypted:     vol.Encrypted,
			KmsKeyId:      vol.KmsKeyId,
			Tags:          tags,
			VolumeData:    append([]byte(nil), vol.Data...),
		}
		ec2Snapshots.Put(snap.SnapshotId, snap)
		go ec2TransitionSnapshotToCompleted(snap.SnapshotId)
		snapshots = append(snapshots, snap)
	}

	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<CreateSnapshotsResponse %s><requestId>%s</requestId><snapshotSet>`, ec2Xmlns(), generateUUID())
	for _, snap := range snapshots {
		b.WriteString(snapshotInfoItemXML(snap))
	}
	b.WriteString(`</snapshotSet></CreateSnapshotsResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

// snapshotInfoItemXML renders a SnapshotInfo member. Note SnapshotInfo uses
// <state> (not the <status> DescribeSnapshots' Snapshot shape uses).
func snapshotInfoItemXML(snap EC2Snapshot) string {
	kms := ""
	if snap.KmsKeyId != "" {
		kms = fmt.Sprintf("<kmsKeyId>%s</kmsKeyId>", snap.KmsKeyId)
	}
	return fmt.Sprintf(`<item><snapshotId>%s</snapshotId><volumeId>%s</volumeId><state>%s</state><startTime>%s</startTime><progress>%s</progress><ownerId>%s</ownerId><volumeSize>%d</volumeSize><description>%s</description><encrypted>%t</encrypted>%s%s</item>`,
		snap.SnapshotId, snap.VolumeId, snap.State, snap.StartTime, snap.Progress, snap.OwnerId,
		snap.VolumeSize, xmlEscape(snap.Description), snap.Encrypted, kms, writeTagSetXML(snap.Tags))
}

// ----------------------------------------------------------------------------
// CopyVolumes — duplicate existing volumes into fresh volume ids.
// ----------------------------------------------------------------------------

func handleCopyVolumes(w http.ResponseWriter, r *http.Request) {
	// CopyVolumes takes a single SourceVolumeId (scalar wire param).
	srcID := r.FormValue("SourceVolumeId")
	if srcID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter SourceVolumeId", http.StatusBadRequest)
		return
	}
	src, ok := ec2Volumes.Get(srcID)
	if !ok {
		ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", srcID), http.StatusBadRequest)
		return
	}
	cp := EC2Volume{
		VolumeId:           ec2ID("vol"),
		Size:               src.Size,
		SnapshotId:         src.SnapshotId,
		AvailabilityZone:   src.AvailabilityZone,
		State:              "available",
		CreateTime:         time.Now().UTC().Format(time.RFC3339),
		VolumeType:         src.VolumeType,
		Iops:               src.Iops,
		Throughput:         src.Throughput,
		KmsKeyId:           src.KmsKeyId,
		Encrypted:          src.Encrypted,
		MultiAttachEnabled: src.MultiAttachEnabled,
		Tags:               parseTags(r),
		Data:               append([]byte(nil), src.Data...),
	}
	if v := r.FormValue("Size"); v != "" {
		if n := ec2AtoiOr(v, 0); n > cp.Size {
			cp.Size = n
		}
	}
	if vt := r.FormValue("VolumeType"); vt != "" {
		cp.VolumeType = vt
	}
	ec2Volumes.Put(cp.VolumeId, cp)
	copies := []EC2Volume{cp}
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<CopyVolumesResponse %s><requestId>%s</requestId><volumeSet>`, ec2Xmlns(), generateUUID())
	for _, v := range copies {
		b.WriteString(ec2VolumeXML(v))
	}
	b.WriteString(`</volumeSet></CopyVolumesResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

// ----------------------------------------------------------------------------
// DescribeVolumeStatus / EnableVolumeIO.
// ----------------------------------------------------------------------------

func handleDescribeVolumeStatus(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VolumeId")
	filters := ec2Filters(r)
	var vols []EC2Volume
	if len(ids) > 0 {
		for _, id := range ids {
			vol, ok := ec2Volumes.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", id), http.StatusBadRequest)
				return
			}
			vols = append(vols, vol)
		}
	} else {
		for _, vol := range ec2Volumes.List() {
			if !ec2VolumeMatchesFilters(vol, filters) {
				continue
			}
			vols = append(vols, vol)
		}
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].VolumeId < vols[j].VolumeId })
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<DescribeVolumeStatusResponse %s><requestId>%s</requestId><volumeStatusSet>`, ec2Xmlns(), generateUUID())
	for _, vol := range vols {
		// A healthy volume reports an "ok" status with the io-enabled /
		// io-performance checks AWS surfaces.
		fmt.Fprintf(&b, `<item><volumeId>%s</volumeId><availabilityZone>%s</availabilityZone>`+
			`<volumeStatus><status>ok</status><details>`+
			`<item><name>io-enabled</name><status>passed</status></item>`+
			`<item><name>io-performance</name><status>not-applicable</status></item>`+
			`</details></volumeStatus><actionsSet/><eventsSet/></item>`,
			vol.VolumeId, vol.AvailabilityZone)
	}
	b.WriteString(`</volumeStatusSet></DescribeVolumeStatusResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

func handleEnableVolumeIO(w http.ResponseWriter, r *http.Request) {
	volID := r.FormValue("VolumeId")
	if volID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter VolumeId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Volumes.Get(volID); !ok {
		ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", volID), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<EnableVolumeIOResponse %s><requestId>%s</requestId><return>true</return></EnableVolumeIOResponse>`,
		ec2Xmlns(), generateUUID())
}

// ----------------------------------------------------------------------------
// ImportVolume — VM-import conversion task that materializes a new volume.
// ----------------------------------------------------------------------------

func handleImportVolume(w http.ResponseWriter, r *http.Request) {
	az := r.FormValue("AvailabilityZone")
	if az == "" {
		az = awsAvailabilityZone()
	}
	size := ec2AtoiOr(r.FormValue("Volume.Size"), 8)
	desc := r.FormValue("Description")
	vol := EC2Volume{
		VolumeId:         ec2ID("vol"),
		Size:             size,
		AvailabilityZone: az,
		State:            "available",
		CreateTime:       time.Now().UTC().Format(time.RFC3339),
		VolumeType:       "gp2",
	}
	ec2Volumes.Put(vol.VolumeId, vol)
	taskID := ec2ID("import-vol")
	bytes := int64(size) * 1024 * 1024 * 1024
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ImportVolumeResponse %s><requestId>%s</requestId>`+
		`<conversionTask><conversionTaskId>%s</conversionTaskId><expirationTime>%s</expirationTime>`+
		`<importVolume><availabilityZone>%s</availabilityZone><bytesConverted>%d</bytesConverted><description>%s</description>`+
		`<image><format>VMDK</format><size>%d</size><importManifestUrl></importManifestUrl></image>`+
		`<volume><id>%s</id><size>%d</size></volume></importVolume>`+
		`<state>active</state></conversionTask></ImportVolumeResponse>`,
		ec2Xmlns(), generateUUID(), taskID, time.Now().UTC().Add(24*time.Hour).Format(time.RFC3339),
		az, bytes, xmlEscape(desc), bytes, vol.VolumeId, size)
}

// ----------------------------------------------------------------------------
// Recycle Bin — volumes, snapshots, locked snapshots, import-snapshot tasks.
// ----------------------------------------------------------------------------

func handleRestoreVolumeFromRecycleBin(w http.ResponseWriter, r *http.Request) {
	volID := r.FormValue("VolumeId")
	if volID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter VolumeId", http.StatusBadRequest)
		return
	}
	if vol, ok := ec2RecycledVolumes.Get(volID); ok {
		vol.State = "available"
		ec2RecycledVolumes.Delete(volID)
		ec2Volumes.Put(volID, vol)
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, `<RestoreVolumeFromRecycleBinResponse %s><requestId>%s</requestId><return>true</return></RestoreVolumeFromRecycleBinResponse>`,
			ec2Xmlns(), generateUUID())
		return
	}
	ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q is not in the Recycle Bin", volID), http.StatusBadRequest)
}

func handleListVolumesInRecycleBin(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VolumeId")
	var vols []EC2Volume
	for _, vol := range ec2RecycledVolumes.List() {
		if len(ids) > 0 && !ec2StrInValues(vol.VolumeId, ids) {
			continue
		}
		vols = append(vols, vol)
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].VolumeId < vols[j].VolumeId })
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<ListVolumesInRecycleBinResponse %s><requestId>%s</requestId><volumeSet>`, ec2Xmlns(), generateUUID())
	for _, vol := range vols {
		fmt.Fprintf(&b, `<item><volumeId>%s</volumeId><volumeType>%s</volumeType><size>%d</size><availabilityZone>%s</availabilityZone><recycleBinEnterTime>%s</recycleBinEnterTime></item>`,
			vol.VolumeId, vol.VolumeType, vol.Size, vol.AvailabilityZone, vol.CreateTime)
	}
	b.WriteString(`</volumeSet></ListVolumesInRecycleBinResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

func handleListSnapshotsInRecycleBin(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "SnapshotId")
	var snaps []EC2Snapshot
	for _, snap := range ec2RecycledSnapshots.List() {
		if len(ids) > 0 && !ec2StrInValues(snap.SnapshotId, ids) {
			continue
		}
		snaps = append(snaps, snap)
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].SnapshotId < snaps[j].SnapshotId })
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<ListSnapshotsInRecycleBinResponse %s><requestId>%s</requestId><snapshotSet>`, ec2Xmlns(), generateUUID())
	for _, snap := range snaps {
		fmt.Fprintf(&b, `<item><snapshotId>%s</snapshotId><recycleBinEnterTime>%s</recycleBinEnterTime><description>%s</description><volumeId>%s</volumeId></item>`,
			snap.SnapshotId, snap.StartTime, xmlEscape(snap.Description), snap.VolumeId)
	}
	b.WriteString(`</snapshotSet></ListSnapshotsInRecycleBinResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

func handleDescribeLockedSnapshots(w http.ResponseWriter, r *http.Request) {
	// Snapshot Lock is not exercised by the volume/snapshot CRUD slice, so no
	// snapshot is in a locked state and the read-back is an empty set.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeLockedSnapshotsResponse %s><requestId>%s</requestId><snapshotSet/></DescribeLockedSnapshotsResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDescribeImportSnapshotTasks(w http.ResponseWriter, r *http.Request) {
	// No ImportSnapshot task is created by the sim's CRUD slice, so the task set
	// is empty; the shape (importSnapshotTaskSet) round-trips through the SDK.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeImportSnapshotTasksResponse %s><requestId>%s</requestId><importSnapshotTaskSet/></DescribeImportSnapshotTasksResponse>`,
		ec2Xmlns(), generateUUID())
}

// ----------------------------------------------------------------------------
// Replace-root-volume + Mac-dedicated-host tasks.
// ----------------------------------------------------------------------------

func handleCreateReplaceRootVolumeTask(w http.ResponseWriter, r *http.Request) {
	instID := r.FormValue("InstanceId")
	if instID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter InstanceId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Instances.Get(instID); !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID %q does not exist", instID), http.StatusBadRequest)
		return
	}
	task := EC2ReplaceRootVolumeTask{
		ReplaceRootVolumeTaskId:  ec2ID("replacevol"),
		InstanceId:               instID,
		TaskState:                "pending",
		StartTime:                time.Now().UTC().Format(time.RFC3339),
		ImageId:                  r.FormValue("ImageId"),
		SnapshotId:               r.FormValue("SnapshotId"),
		DeleteReplacedRootVolume: ec2BoolStr(r.FormValue("DeleteReplacedRootVolume")),
		Tags:                     parseTags(r),
	}
	ec2ReplaceRootVolumeTasks.Put(task.ReplaceRootVolumeTaskId, task)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateReplaceRootVolumeTaskResponse %s><requestId>%s</requestId><replaceRootVolumeTask>%s</replaceRootVolumeTask></CreateReplaceRootVolumeTaskResponse>`,
		ec2Xmlns(), generateUUID(), replaceRootVolumeTaskBodyXML(task))
}

func replaceRootVolumeTaskBodyXML(t EC2ReplaceRootVolumeTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<replaceRootVolumeTaskId>%s</replaceRootVolumeTaskId><instanceId>%s</instanceId><taskState>%s</taskState><startTime>%s</startTime>`,
		t.ReplaceRootVolumeTaskId, t.InstanceId, t.TaskState, t.StartTime)
	if t.CompleteTime != "" {
		fmt.Fprintf(&b, `<completeTime>%s</completeTime>`, t.CompleteTime)
	}
	if t.ImageId != "" {
		fmt.Fprintf(&b, `<imageId>%s</imageId>`, t.ImageId)
	}
	if t.SnapshotId != "" {
		fmt.Fprintf(&b, `<snapshotId>%s</snapshotId>`, t.SnapshotId)
	}
	fmt.Fprintf(&b, `<deleteReplacedRootVolume>%t</deleteReplacedRootVolume>`, t.DeleteReplacedRootVolume)
	b.WriteString(writeTagSetXML(t.Tags))
	return b.String()
}

func handleDescribeReplaceRootVolumeTasks(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ReplaceRootVolumeTaskId")
	var tasks []EC2ReplaceRootVolumeTask
	for _, t := range ec2ReplaceRootVolumeTasks.List() {
		if len(ids) > 0 && !ec2StrInValues(t.ReplaceRootVolumeTaskId, ids) {
			continue
		}
		// A read-back settles a pending task to succeeded (no synthetic timer),
		// the same way the snapshot/FSR settles advance on first read.
		if t.TaskState == "pending" || t.TaskState == "in-progress" {
			t.TaskState = "succeeded"
			t.CompleteTime = time.Now().UTC().Format(time.RFC3339)
			ec2ReplaceRootVolumeTasks.Put(t.ReplaceRootVolumeTaskId, t)
		}
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ReplaceRootVolumeTaskId < tasks[j].ReplaceRootVolumeTaskId })
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<DescribeReplaceRootVolumeTasksResponse %s><requestId>%s</requestId><replaceRootVolumeTaskSet>`, ec2Xmlns(), generateUUID())
	for _, t := range tasks {
		b.WriteString("<item>" + replaceRootVolumeTaskBodyXML(t) + "</item>")
	}
	b.WriteString(`</replaceRootVolumeTaskSet></DescribeReplaceRootVolumeTasksResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

func handleCreateDelegateMacVolumeOwnershipTask(w http.ResponseWriter, r *http.Request) {
	createMacModificationTask(w, r, "volume-ownership-delegation", nil, "CreateDelegateMacVolumeOwnershipTask")
}

func handleCreateMacSipModificationTask(w http.ResponseWriter, r *http.Request) {
	var sip []EC2MacSipSetting
	for _, k := range []string{"AppleInternal", "BaseSystem", "DebuggingRestrictions", "DTraceRestrictions", "FilesystemProtections", "KextSigning", "NvramProtections"} {
		if v := r.FormValue("MacSystemIntegrityProtectionConfiguration." + k); v != "" {
			sip = append(sip, EC2MacSipSetting{Name: strings.ToLower(k[:1]) + k[1:], Value: v})
		}
	}
	createMacModificationTask(w, r, "sip-modification", sip, "CreateMacSystemIntegrityProtectionModificationTask")
}

func createMacModificationTask(w http.ResponseWriter, r *http.Request, taskType string, sip []EC2MacSipSetting, action string) {
	instID := r.FormValue("InstanceId")
	if instID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter InstanceId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Instances.Get(instID); !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID %q does not exist", instID), http.StatusBadRequest)
		return
	}
	task := EC2MacModificationTask{
		MacModificationTaskId: ec2ID("macmodification"),
		InstanceId:            instID,
		TaskState:             "in-progress",
		TaskType:              taskType,
		StartTime:             time.Now().UTC().Format(time.RFC3339),
		SipConfig:             sip,
		Tags:                  parseTags(r),
	}
	ec2MacModificationTasks.Put(task.MacModificationTaskId, task)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s><requestId>%s</requestId><macModificationTask>%s</macModificationTask></%sResponse>`,
		action, ec2Xmlns(), generateUUID(), macModificationTaskBodyXML(task), action)
}

func macModificationTaskBodyXML(t EC2MacModificationTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<macModificationTaskId>%s</macModificationTaskId><instanceId>%s</instanceId><taskState>%s</taskState><taskType>%s</taskType><startTime>%s</startTime>`,
		t.MacModificationTaskId, t.InstanceId, t.TaskState, t.TaskType, t.StartTime)
	if len(t.SipConfig) > 0 {
		b.WriteString(`<macSystemIntegrityProtectionConfig>`)
		for _, s := range t.SipConfig {
			fmt.Fprintf(&b, `<%s>%s</%s>`, s.Name, xmlEscape(s.Value), s.Name)
		}
		b.WriteString(`</macSystemIntegrityProtectionConfig>`)
	}
	b.WriteString(writeTagSetXML(t.Tags))
	return b.String()
}

// handleDescribeMacModificationTasks lists the Mac volume-ownership / SIP
// modification tasks recorded by the Create* handlers, optionally filtered to a
// set of requested task ids.
func handleDescribeMacModificationTasks(w http.ResponseWriter, r *http.Request) {
	wanted := map[string]bool{}
	for i := 1; ; i++ {
		id := r.FormValue(fmt.Sprintf("MacModificationTaskId.%d", i))
		if id == "" {
			break
		}
		wanted[id] = true
	}
	var items strings.Builder
	for _, t := range ec2MacModificationTasks.List() {
		if len(wanted) > 0 && !wanted[t.MacModificationTaskId] {
			continue
		}
		fmt.Fprintf(&items, "<item>%s</item>", macModificationTaskBodyXML(t))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeMacModificationTasksResponse %s><requestId>%s</requestId><macModificationTaskSet>%s</macModificationTaskSet></DescribeMacModificationTasksResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

// ----------------------------------------------------------------------------
// Customer-Owned IP (CoIP) CIDRs on an existing CoIP pool.
// ----------------------------------------------------------------------------

func handleCreateCoipCidr(w http.ResponseWriter, r *http.Request) {
	cidr := r.FormValue("Cidr")
	poolID := r.FormValue("CoipPoolId")
	if cidr == "" || poolID == "" {
		ec2ErrorXML(w, "MissingParameter", "Cidr and CoipPoolId are required", http.StatusBadRequest)
		return
	}
	pool, ok := ec2CoipPools.Get(poolID)
	if !ok {
		ec2ErrorXML(w, "InvalidCoipPoolId.NotFound", fmt.Sprintf("The CoIP pool %q does not exist", poolID), http.StatusBadRequest)
		return
	}
	pool.PoolCidrs = append(pool.PoolCidrs, cidr)
	ec2CoipPools.Put(poolID, pool)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateCoipCidrResponse %s><requestId>%s</requestId><coipCidr><cidr>%s</cidr><coipPoolId>%s</coipPoolId><localGatewayRouteTableId>%s</localGatewayRouteTableId></coipCidr></CreateCoipCidrResponse>`,
		ec2Xmlns(), generateUUID(), cidr, poolID, pool.LocalGatewayRouteTableId)
}

func handleDeleteCoipCidr(w http.ResponseWriter, r *http.Request) {
	cidr := r.FormValue("Cidr")
	poolID := r.FormValue("CoipPoolId")
	if cidr == "" || poolID == "" {
		ec2ErrorXML(w, "MissingParameter", "Cidr and CoipPoolId are required", http.StatusBadRequest)
		return
	}
	pool, ok := ec2CoipPools.Get(poolID)
	if !ok {
		ec2ErrorXML(w, "InvalidCoipPoolId.NotFound", fmt.Sprintf("The CoIP pool %q does not exist", poolID), http.StatusBadRequest)
		return
	}
	var kept []string
	for _, c := range pool.PoolCidrs {
		if c != cidr {
			kept = append(kept, c)
		}
	}
	pool.PoolCidrs = kept
	ec2CoipPools.Put(poolID, pool)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteCoipCidrResponse %s><requestId>%s</requestId><coipCidr><cidr>%s</cidr><coipPoolId>%s</coipPoolId><localGatewayRouteTableId>%s</localGatewayRouteTableId></coipCidr></DeleteCoipCidrResponse>`,
		ec2Xmlns(), generateUUID(), cidr, poolID, pool.LocalGatewayRouteTableId)
}

// ----------------------------------------------------------------------------
// Default VPC / subnet.
// ----------------------------------------------------------------------------

func handleCreateDefaultVpc(w http.ResponseWriter, r *http.Request) {
	// A real default VPC is the account's 172.31.0.0/16 VPC with isDefault=true,
	// DNS support + hostnames on. Real EC2 raises DefaultVpcAlreadyExists when one
	// already exists, so the sim does the same.
	for _, v := range ec2Vpcs.List() {
		if v.IsDefault {
			ec2ErrorXML(w, "DefaultVpcAlreadyExists", "A default VPC already exists for this account in this region.", http.StatusBadRequest)
			return
		}
	}
	vpc := EC2Vpc{
		VpcId:              ec2ID("vpc"),
		CidrBlock:          "172.31.0.0/16",
		State:              "available",
		OwnerId:            ec2Owner(),
		IsDefault:          true,
		InstanceTenancy:    "default",
		EnableDnsSupport:   true,
		EnableDnsHostnames: true,
	}
	ec2Vpcs.Put(vpc.VpcId, vpc)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateDefaultVpcResponse %s><requestId>%s</requestId><vpc>%s</vpc></CreateDefaultVpcResponse>`,
		ec2Xmlns(), generateUUID(), vpcItemBodyXML(vpc))
}

func handleCreateDefaultSubnet(w http.ResponseWriter, r *http.Request) {
	az := r.FormValue("AvailabilityZone")
	if az == "" {
		az = awsAvailabilityZone()
	}
	// A default subnet lives in the account's default VPC.
	var defVpc EC2Vpc
	found := false
	for _, v := range ec2Vpcs.List() {
		if v.IsDefault {
			defVpc = v
			found = true
			break
		}
	}
	if !found {
		ec2ErrorXML(w, "DefaultVpcDoesNotExist", "No default VPC exists for this account in this region.", http.StatusBadRequest)
		return
	}
	subnet := EC2Subnet{
		SubnetId:                       ec2ID("subnet"),
		VpcId:                          defVpc.VpcId,
		CidrBlock:                      "172.31.0.0/20",
		AvailabilityZone:               az,
		AvailabilityZoneId:             ec2AvailabilityZoneId(az),
		State:                          "available",
		OwnerId:                        ec2Owner(),
		MapPublicIpOnLaunch:            true,
		PrivateDnsHostnameTypeOnLaunch: "ip-name",
	}
	ec2Subnets.Put(subnet.SubnetId, subnet)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateDefaultSubnetResponse %s><requestId>%s</requestId><subnet>%s</subnet></CreateDefaultSubnetResponse>`,
		ec2Xmlns(), generateUUID(), subnetItemBodyXML(subnet))
}

// ----------------------------------------------------------------------------
// Prefix lists (AWS-managed + customer-managed).
// ----------------------------------------------------------------------------

// handleDescribePrefixLists returns the (legacy) DescribePrefixLists view:
// AWS-managed gateway-endpoint prefix lists (S3 / DynamoDB) plus the
// customer-managed lists from the managed-prefix-list store.
func handleDescribePrefixLists(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "PrefixListId")
	filters := ec2Filters(r)
	type plView struct {
		id, name string
		cidrs    []string
	}
	region := awsRegion()
	lists := []plView{
		{id: "pl-" + shortHash("s3-"+region), name: "com.amazonaws." + region + ".s3", cidrs: []string{"52.219.0.0/20", "3.5.0.0/19"}},
		{id: "pl-" + shortHash("dynamodb-"+region), name: "com.amazonaws." + region + ".dynamodb", cidrs: []string{"52.94.0.0/22"}},
	}
	for _, pl := range ec2ManagedPrefixLists.List() {
		var cidrs []string
		for _, e := range pl.Entries {
			cidrs = append(cidrs, e.Cidr)
		}
		lists = append(lists, plView{id: pl.PrefixListId, name: pl.PrefixListName, cidrs: cidrs})
	}
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<DescribePrefixListsResponse %s><requestId>%s</requestId><prefixListSet>`, ec2Xmlns(), generateUUID())
	for _, pl := range lists {
		if len(ids) > 0 && !ec2StrInValues(pl.id, ids) {
			continue
		}
		if names, ok := filters["prefix-list-name"]; ok && !ec2StrInValues(pl.name, names) {
			continue
		}
		fmt.Fprintf(&b, `<item><prefixListId>%s</prefixListId><prefixListName>%s</prefixListName><cidrSet>`, pl.id, pl.name)
		for _, c := range pl.cidrs {
			fmt.Fprintf(&b, `<item>%s</item>`, c)
		}
		b.WriteString(`</cidrSet></item>`)
	}
	b.WriteString(`</prefixListSet></DescribePrefixListsResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

// shortHash gives a stable 8-hex-char id suffix for a deterministic name.
func shortHash(s string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}

func handleGetManagedPrefixListAssociations(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("PrefixListId")
	if _, ok := ec2ManagedPrefixLists.Get(id); !ok {
		ec2ErrorXML(w, "InvalidPrefixListID.NotFound", fmt.Sprintf("The prefix list ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// A managed prefix list is associated with the security-group rules that
	// reference it. Derive the associated resources from the SG store honestly.
	type assocEntry struct{ resID, owner string }
	var assoc []assocEntry
	for _, sg := range ec2SecurityGroups.List() {
		referenced := false
		for _, p := range append(append([]EC2IpPermission{}, sg.IpPermissions...), sg.IpPermissionsEgress...) {
			for _, pl := range p.PrefixListIds {
				if pl.PrefixListId == id {
					referenced = true
				}
			}
		}
		if referenced {
			assoc = append(assoc, assocEntry{sg.GroupId, sg.OwnerId})
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<GetManagedPrefixListAssociationsResponse %s><requestId>%s</requestId><prefixListAssociationSet>`, ec2Xmlns(), generateUUID())
	for _, a := range assoc {
		fmt.Fprintf(&b, `<item><resourceId>%s</resourceId><resourceOwner>%s</resourceOwner></item>`, a.resID, a.owner)
	}
	b.WriteString(`</prefixListAssociationSet></GetManagedPrefixListAssociationsResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

func handleRestoreManagedPrefixListVersion(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("PrefixListId")
	pl, ok := ec2ManagedPrefixLists.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidPrefixListID.NotFound", fmt.Sprintf("The prefix list ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// Restoring a prior version bumps the current version to a new number (real
	// EC2 creates a new version that is a copy of the restored one).
	pl.Version++
	pl.State = "modify-complete"
	ec2ManagedPrefixLists.Put(id, pl)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RestoreManagedPrefixListVersionResponse %s><requestId>%s</requestId><prefixList>%s</prefixList></RestoreManagedPrefixListVersionResponse>`,
		ec2Xmlns(), generateUUID(), managedPrefixListBodyXML(pl))
}

// ----------------------------------------------------------------------------
// Security-group references / stale / for-vpc — derived from the SG store.
// ----------------------------------------------------------------------------

func handleDescribeSecurityGroupReferences(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "GroupId")
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<DescribeSecurityGroupReferencesResponse %s><requestId>%s</requestId><securityGroupReferenceSet>`, ec2Xmlns(), generateUUID())
	// A reference exists when another SG's rules name one of the queried groups in
	// a UserIdGroupPair. Derive these honestly from the SG store.
	for _, target := range ids {
		for _, sg := range ec2SecurityGroups.List() {
			for _, p := range append(append([]EC2IpPermission{}, sg.IpPermissions...), sg.IpPermissionsEgress...) {
				for _, pair := range p.UserIdGroupPairs {
					if pair.GroupId == target {
						fmt.Fprintf(&b, `<item><groupId>%s</groupId><referencingVpcId>%s</referencingVpcId></item>`, target, sg.VpcId)
					}
				}
			}
		}
	}
	b.WriteString(`</securityGroupReferenceSet></DescribeSecurityGroupReferencesResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

func handleDescribeStaleSecurityGroups(w http.ResponseWriter, r *http.Request) {
	vpcID := r.FormValue("VpcId")
	if vpcID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter VpcId", http.StatusBadRequest)
		return
	}
	// A stale rule references a security group that no longer exists (e.g. across
	// a deleted VPC peering). Derive honestly: a rule referencing an absent group.
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<DescribeStaleSecurityGroupsResponse %s><requestId>%s</requestId><staleSecurityGroupSet>`, ec2Xmlns(), generateUUID())
	for _, sg := range ec2SecurityGroups.List() {
		if sg.VpcId != vpcID {
			continue
		}
		staleIn := stalePermissionsXML(sg.IpPermissions)
		staleEg := stalePermissionsXML(sg.IpPermissionsEgress)
		if staleIn == "" && staleEg == "" {
			continue
		}
		fmt.Fprintf(&b, `<item><groupId>%s</groupId><groupName>%s</groupName><description>%s</description><vpcId>%s</vpcId>`,
			sg.GroupId, sg.GroupName, xmlEscape(sg.Description), sg.VpcId)
		if staleIn != "" {
			fmt.Fprintf(&b, `<staleIpPermissions>%s</staleIpPermissions>`, staleIn)
		}
		if staleEg != "" {
			fmt.Fprintf(&b, `<staleIpPermissionsEgress>%s</staleIpPermissionsEgress>`, staleEg)
		}
		b.WriteString(`</item>`)
	}
	b.WriteString(`</staleSecurityGroupSet></DescribeStaleSecurityGroupsResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

// stalePermissionsXML returns the <item> elements for rules referencing a group
// that no longer exists in the store, or "" if none are stale.
func stalePermissionsXML(perms []EC2IpPermission) string {
	var b strings.Builder
	for _, p := range perms {
		var groups strings.Builder
		stale := false
		for _, pair := range p.UserIdGroupPairs {
			if _, ok := ec2SecurityGroups.Get(pair.GroupId); !ok {
				stale = true
				fmt.Fprintf(&groups, `<item><groupId>%s</groupId></item>`, pair.GroupId)
			}
		}
		if !stale {
			continue
		}
		fmt.Fprintf(&b, `<item><ipProtocol>%s</ipProtocol><fromPort>%d</fromPort><toPort>%d</toPort><groups>%s</groups></item>`,
			p.IpProtocol, p.FromPort, p.ToPort, groups.String())
	}
	return b.String()
}

func handleGetSecurityGroupsForVpc(w http.ResponseWriter, r *http.Request) {
	vpcID := r.FormValue("VpcId")
	if vpcID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter VpcId", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<GetSecurityGroupsForVpcResponse %s><requestId>%s</requestId><securityGroupForVpcSet>`, ec2Xmlns(), generateUUID())
	for _, sg := range ec2SecurityGroups.List() {
		if sg.VpcId != vpcID {
			continue
		}
		fmt.Fprintf(&b, `<item><groupId>%s</groupId><groupName>%s</groupName><description>%s</description><ownerId>%s</ownerId><primaryVpcId>%s</primaryVpcId>%s</item>`,
			sg.GroupId, sg.GroupName, xmlEscape(sg.Description), sg.OwnerId, sg.VpcId, writeTagSetXML(sg.Tags))
	}
	b.WriteString(`</securityGroupForVpcSet></GetSecurityGroupsForVpcResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

// ----------------------------------------------------------------------------
// Launch-template data derived from an existing instance + version delete.
// ----------------------------------------------------------------------------

func handleGetLaunchTemplateData(w http.ResponseWriter, r *http.Request) {
	instID := r.FormValue("InstanceId")
	inst, ok := ec2Instances.Get(instID)
	if !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID %q does not exist", instID), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<GetLaunchTemplateDataResponse %s><requestId>%s</requestId><launchTemplateData>`, ec2Xmlns(), generateUUID())
	if inst.ImageId != "" {
		fmt.Fprintf(&b, `<imageId>%s</imageId>`, inst.ImageId)
	}
	if inst.InstanceType != "" {
		fmt.Fprintf(&b, `<instanceType>%s</instanceType>`, inst.InstanceType)
	}
	if inst.KeyName != "" {
		fmt.Fprintf(&b, `<keyName>%s</keyName>`, inst.KeyName)
	}
	fmt.Fprintf(&b, `<ebsOptimized>%t</ebsOptimized>`, inst.EbsOptimized)
	fmt.Fprintf(&b, `<monitoring><enabled>%t</enabled></monitoring>`, inst.Monitoring)
	fmt.Fprintf(&b, `<disableApiTermination>%t</disableApiTermination>`, inst.DisableApiTermination)
	fmt.Fprintf(&b, `<placement><availabilityZone>%s</availabilityZone></placement>`, awsAvailabilityZone())
	if len(inst.SecurityGroupIds) > 0 {
		b.WriteString(`<securityGroupIdSet>`)
		for _, g := range inst.SecurityGroupIds {
			fmt.Fprintf(&b, `<item>%s</item>`, g)
		}
		b.WriteString(`</securityGroupIdSet>`)
	}
	if inst.SubnetId != "" {
		fmt.Fprintf(&b, `<networkInterfaceSet><item><deviceIndex>0</deviceIndex><subnetId>%s</subnetId></item></networkInterfaceSet>`, inst.SubnetId)
	}
	b.WriteString(`</launchTemplateData></GetLaunchTemplateDataResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

func handleDeleteLaunchTemplateVersions(w http.ResponseWriter, r *http.Request) {
	lt, ok := lookupLaunchTemplate(r.FormValue("LaunchTemplateId"), r.FormValue("LaunchTemplateName"))
	if !ok {
		ref := r.FormValue("LaunchTemplateId")
		if ref == "" {
			ref = r.FormValue("LaunchTemplateName")
		}
		ec2ErrorXML(w, "InvalidLaunchTemplateId.NotFound", fmt.Sprintf("Launch template %s does not exist.", ref), http.StatusBadRequest)
		return
	}
	versions := ec2ParamList(r, "LaunchTemplateVersion")
	type item struct {
		num     int64
		errCode string
		errMsg  string
	}
	var success, failure []item
	for _, vs := range versions {
		n := parseInt64(vs)
		if n == lt.DefaultVersionNumber {
			failure = append(failure, item{num: n, errCode: "launchTemplateVersionDeletionFailure",
				errMsg: "The launch template version is the default version and cannot be deleted."})
			continue
		}
		if !ltHasVersion(lt, n) {
			failure = append(failure, item{num: n, errCode: "launchTemplateIdVersionNotFound",
				errMsg: fmt.Sprintf("Launch template version %s does not exist.", vs)})
			continue
		}
		var kept []EC2LaunchTemplateVersion
		for _, v := range lt.Versions {
			if v.VersionNumber != n {
				kept = append(kept, v)
			}
		}
		lt.Versions = kept
		success = append(success, item{num: n})
	}
	ec2LaunchTemplates.Put(lt.LaunchTemplateId, lt)
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<DeleteLaunchTemplateVersionsResponse %s><requestId>%s</requestId><successfullyDeletedLaunchTemplateVersionSet>`, ec2Xmlns(), generateUUID())
	for _, s := range success {
		fmt.Fprintf(&b, `<item><launchTemplateId>%s</launchTemplateId><launchTemplateName>%s</launchTemplateName><versionNumber>%d</versionNumber></item>`,
			lt.LaunchTemplateId, lt.LaunchTemplateName, s.num)
	}
	b.WriteString(`</successfullyDeletedLaunchTemplateVersionSet><unsuccessfullyDeletedLaunchTemplateVersionSet>`)
	for _, f := range failure {
		fmt.Fprintf(&b, `<item><launchTemplateId>%s</launchTemplateId><launchTemplateName>%s</launchTemplateName><versionNumber>%d</versionNumber><responseError><code>%s</code><message>%s</message></responseError></item>`,
			lt.LaunchTemplateId, lt.LaunchTemplateName, f.num, f.errCode, xmlEscape(f.errMsg))
	}
	b.WriteString(`</unsuccessfullyDeletedLaunchTemplateVersionSet></DeleteLaunchTemplateVersionsResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

// ----------------------------------------------------------------------------
// DNS-name-options + VPC endpoint + route-table association + ENI attribute.
// ----------------------------------------------------------------------------

func handleModifyPrivateDnsNameOptions(w http.ResponseWriter, r *http.Request) {
	instID := r.FormValue("InstanceId")
	if instID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter InstanceId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Instances.Get(instID); !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID %q does not exist", instID), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyPrivateDnsNameOptionsResponse %s><requestId>%s</requestId><return>true</return></ModifyPrivateDnsNameOptionsResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleModifyPublicIpDnsNameOptions(w http.ResponseWriter, r *http.Request) {
	eniID := r.FormValue("NetworkInterfaceId")
	if eniID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter NetworkInterfaceId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2NetworkInterfaces.Get(eniID); !ok {
		ec2ErrorXML(w, "InvalidNetworkInterfaceID.NotFound", fmt.Sprintf("The network interface ID %q does not exist", eniID), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyPublicIpDnsNameOptionsResponse %s><requestId>%s</requestId><successful>true</successful></ModifyPublicIpDnsNameOptionsResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleModifyVpcEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("VpcEndpointId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter VpcEndpointId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2VpcEndpoints.Get(id); !ok {
		ec2ErrorXML(w, "InvalidVpcEndpointId.NotFound", fmt.Sprintf("The VPC endpoint ID %q does not exist", id), http.StatusBadRequest)
		return
	}
	ec2VpcEndpoints.Update(id, func(ep *EC2VpcEndpoint) {
		ep.RouteTableIds = append(ep.RouteTableIds, ec2ParamList(r, "AddRouteTableId")...)
		ep.SubnetIds = append(ep.SubnetIds, ec2ParamList(r, "AddSubnetId")...)
		if pd := r.FormValue("PolicyDocument"); pd != "" {
			ep.PolicyDocument = pd
		}
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpcEndpointResponse %s><requestId>%s</requestId><return>true</return></ModifyVpcEndpointResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleReplaceRouteTableAssociation(w http.ResponseWriter, r *http.Request) {
	oldAssoc := r.FormValue("AssociationId")
	newRT := r.FormValue("RouteTableId")
	if oldAssoc == "" || newRT == "" {
		ec2ErrorXML(w, "MissingParameter", "AssociationId and RouteTableId are required", http.StatusBadRequest)
		return
	}
	if _, ok := ec2RouteTables.Get(newRT); !ok {
		ec2ErrorXML(w, "InvalidRouteTableID.NotFound", fmt.Sprintf("The route table ID %q does not exist", newRT), http.StatusBadRequest)
		return
	}
	// Find the route table currently holding the association, capture its subnet,
	// remove it there, then add the same subnet to the new route table.
	var subnetID string
	var main bool
	for _, rt := range ec2RouteTables.List() {
		for _, a := range rt.Associations {
			if a.AssociationId == oldAssoc {
				subnetID = a.SubnetId
				main = a.Main
				rtID := rt.RouteTableId
				ec2RouteTables.Update(rtID, func(rtp *EC2RouteTable) {
					var kept []EC2RouteTableAssociation
					for _, x := range rtp.Associations {
						if x.AssociationId != oldAssoc {
							kept = append(kept, x)
						}
					}
					rtp.Associations = kept
				})
			}
		}
	}
	newAssocID := ec2ID("rtbassoc")
	ec2RouteTables.Update(newRT, func(rtp *EC2RouteTable) {
		rtp.Associations = append(rtp.Associations, EC2RouteTableAssociation{
			AssociationId: newAssocID,
			RouteTableId:  newRT,
			SubnetId:      subnetID,
			Main:          main,
		})
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReplaceRouteTableAssociationResponse %s><requestId>%s</requestId><newAssociationId>%s</newAssociationId><associationState><state>associated</state></associationState></ReplaceRouteTableAssociationResponse>`,
		ec2Xmlns(), generateUUID(), newAssocID)
}

func handleResetNetworkInterfaceAttribute(w http.ResponseWriter, r *http.Request) {
	eniID := r.FormValue("NetworkInterfaceId")
	if eniID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter NetworkInterfaceId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2NetworkInterfaces.Get(eniID); !ok {
		ec2ErrorXML(w, "InvalidNetworkInterfaceID.NotFound", fmt.Sprintf("The network interface ID %q does not exist", eniID), http.StatusBadRequest)
		return
	}
	// Reset returns sourceDestCheck to the default (enabled).
	ec2NetworkInterfaces.Update(eniID, func(eni *EC2NetworkInterface) {
		eni.SourceDestDisabled = false
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ResetNetworkInterfaceAttributeResponse %s><requestId>%s</requestId><return>true</return></ResetNetworkInterfaceAttributeResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDescribeNetworkInterfaceAttribute(w http.ResponseWriter, r *http.Request) {
	eniID := r.FormValue("NetworkInterfaceId")
	attr := r.FormValue("Attribute")
	eni, ok := ec2NetworkInterfaces.Get(eniID)
	if !ok {
		ec2ErrorXML(w, "InvalidNetworkInterfaceID.NotFound", fmt.Sprintf("The network interface ID %q does not exist", eniID), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<DescribeNetworkInterfaceAttributeResponse %s><requestId>%s</requestId><networkInterfaceId>%s</networkInterfaceId>`,
		ec2Xmlns(), generateUUID(), eniID)
	switch attr {
	case "description":
		fmt.Fprintf(&b, `<description><value>%s</value></description>`, xmlEscape(eni.Description))
	case "sourceDestCheck":
		fmt.Fprintf(&b, `<sourceDestCheck><value>%t</value></sourceDestCheck>`, !eni.SourceDestDisabled)
	case "groupSet":
		b.WriteString(`<groupSet>`)
		for _, g := range eni.SecurityGroupIds {
			name := g
			if sg, ok := ec2SecurityGroups.Get(g); ok {
				name = sg.GroupName
			}
			fmt.Fprintf(&b, `<item><groupId>%s</groupId><groupName>%s</groupName></item>`, g, name)
		}
		b.WriteString(`</groupSet>`)
	case "attachment":
		if eni.AttachmentId != "" {
			fmt.Fprintf(&b, `<attachment><attachmentId>%s</attachmentId><instanceId>%s</instanceId><deviceIndex>%d</deviceIndex><status>attached</status><deleteOnTermination>%t</deleteOnTermination></attachment>`,
				eni.AttachmentId, eni.InstanceId, eni.DeviceIndex, eni.DeleteOnTermination)
		}
	default:
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Value (%s) for parameter Attribute is invalid.", attr), http.StatusBadRequest)
		return
	}
	b.WriteString(`</DescribeNetworkInterfaceAttributeResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

func handleSendDiagnosticInterrupt(w http.ResponseWriter, r *http.Request) {
	instID := r.FormValue("InstanceId")
	if instID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter InstanceId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Instances.Get(instID); !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID %q does not exist", instID), http.StatusBadRequest)
		return
	}
	// SendDiagnosticInterrupt has a Unit output: an empty success response.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<SendDiagnosticInterruptResponse %s><requestId>%s</requestId></SendDiagnosticInterruptResponse>`,
		ec2Xmlns(), generateUUID())
}

// ----------------------------------------------------------------------------
// IPv6 address management on an ENI.
// ----------------------------------------------------------------------------

func handleAssignIpv6Addresses(w http.ResponseWriter, r *http.Request) {
	eniID := r.FormValue("NetworkInterfaceId")
	if _, ok := ec2NetworkInterfaces.Get(eniID); !ok {
		ec2ErrorXML(w, "InvalidNetworkInterfaceID.NotFound", fmt.Sprintf("The network interface ID %q does not exist", eniID), http.StatusBadRequest)
		return
	}
	requested := ec2ParamList(r, "Ipv6Addresses")
	count := ec2AtoiOr(r.FormValue("Ipv6AddressCount"), 0)
	prefixes := ec2ParamList(r, "Ipv6Prefix")
	prefixCount := ec2AtoiOr(r.FormValue("Ipv6PrefixCount"), 0)

	var assigned []string
	var assignedPfx []string
	ec2ENIIPv6States.Upsert(eniID, func(state *ec2ENIIPv6State) {
		for _, a := range requested {
			state.Addresses = append(state.Addresses, a)
			assigned = append(assigned, a)
		}
		for i := 0; i < count; i++ {
			a := fmt.Sprintf("2600:1f18:%x::%x", shortNum(eniID), len(state.Addresses)+1)
			state.Addresses = append(state.Addresses, a)
			assigned = append(assigned, a)
		}
		for _, p := range prefixes {
			state.Prefixes = append(state.Prefixes, p)
			assignedPfx = append(assignedPfx, p)
		}
		for i := 0; i < prefixCount; i++ {
			p := fmt.Sprintf("2600:1f18:%x:%x::/80", shortNum(eniID), len(state.Prefixes)+1)
			state.Prefixes = append(state.Prefixes, p)
			assignedPfx = append(assignedPfx, p)
		}
	})

	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<AssignIpv6AddressesResponse %s><requestId>%s</requestId><networkInterfaceId>%s</networkInterfaceId><assignedIpv6Addresses>`,
		ec2Xmlns(), generateUUID(), eniID)
	for _, a := range assigned {
		fmt.Fprintf(&b, `<item>%s</item>`, a)
	}
	b.WriteString(`</assignedIpv6Addresses><assignedIpv6PrefixSet>`)
	for _, p := range assignedPfx {
		fmt.Fprintf(&b, `<item>%s</item>`, p)
	}
	b.WriteString(`</assignedIpv6PrefixSet></AssignIpv6AddressesResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

func handleUnassignIpv6Addresses(w http.ResponseWriter, r *http.Request) {
	eniID := r.FormValue("NetworkInterfaceId")
	if _, ok := ec2NetworkInterfaces.Get(eniID); !ok {
		ec2ErrorXML(w, "InvalidNetworkInterfaceID.NotFound", fmt.Sprintf("The network interface ID %q does not exist", eniID), http.StatusBadRequest)
		return
	}
	toRemove := ec2ParamList(r, "Ipv6Addresses")
	pfxRemove := ec2ParamList(r, "Ipv6Prefix")
	ec2ENIIPv6States.Update(eniID, func(state *ec2ENIIPv6State) {
		state.Addresses = removeStrings(state.Addresses, toRemove)
		state.Prefixes = removeStrings(state.Prefixes, pfxRemove)
	})
	w.Header().Set("Content-Type", "text/xml")
	var b strings.Builder
	fmt.Fprintf(&b, `<UnassignIpv6AddressesResponse %s><requestId>%s</requestId><networkInterfaceId>%s</networkInterfaceId><unassignedIpv6Addresses>`,
		ec2Xmlns(), generateUUID(), eniID)
	for _, a := range toRemove {
		fmt.Fprintf(&b, `<item>%s</item>`, a)
	}
	b.WriteString(`</unassignedIpv6Addresses><unassignedIpv6PrefixSet>`)
	for _, p := range pfxRemove {
		fmt.Fprintf(&b, `<item>%s</item>`, p)
	}
	b.WriteString(`</unassignedIpv6PrefixSet></UnassignIpv6AddressesResponse>`)
	_, _ = w.Write([]byte(b.String()))
}

func handleUnassignPrivateIpAddresses(w http.ResponseWriter, r *http.Request) {
	eniID := r.FormValue("NetworkInterfaceId")
	if _, ok := ec2NetworkInterfaces.Get(eniID); !ok {
		ec2ErrorXML(w, "InvalidNetworkInterfaceID.NotFound", fmt.Sprintf("The network interface ID %q does not exist", eniID), http.StatusBadRequest)
		return
	}
	toRemove := ec2ParamList(r, "PrivateIpAddress")
	ec2NetworkInterfaces.Update(eniID, func(eni *EC2NetworkInterface) {
		eni.SecondaryPrivateIps = removeStrings(eni.SecondaryPrivateIps, toRemove)
	})
	// UnassignPrivateIpAddresses has a Unit output: an empty success response.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<UnassignPrivateIpAddressesResponse %s><requestId>%s</requestId><return>true</return></UnassignPrivateIpAddressesResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDescribeIpv6Pools(w http.ResponseWriter, r *http.Request) {
	// The sim provisions no BYOIP IPv6 pool, so the read-back is an empty set;
	// the ipv6PoolSet shape round-trips through the SDK.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIpv6PoolsResponse %s><requestId>%s</requestId><ipv6PoolSet/></DescribeIpv6PoolsResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleGetAssociatedIpv6PoolCidrs(w http.ResponseWriter, r *http.Request) {
	poolID := r.FormValue("PoolId")
	if poolID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter PoolId", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetAssociatedIpv6PoolCidrsResponse %s><requestId>%s</requestId><ipv6CidrAssociationSet/></GetAssociatedIpv6PoolCidrsResponse>`,
		ec2Xmlns(), generateUUID())
}

// ----------------------------------------------------------------------------
// AZ group / default credit specification / VPC tenancy.
// ----------------------------------------------------------------------------

func handleModifyAvailabilityZoneGroup(w http.ResponseWriter, r *http.Request) {
	group := r.FormValue("GroupName")
	optIn := r.FormValue("OptInStatus")
	if group == "" || optIn == "" {
		ec2ErrorXML(w, "MissingParameter", "GroupName and OptInStatus are required", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyAvailabilityZoneGroupResponse %s><requestId>%s</requestId><return>true</return></ModifyAvailabilityZoneGroupResponse>`,
		ec2Xmlns(), generateUUID())
}

// ec2DefaultCredit is the account-level default credit specification per
// burstable instance family (t2/t3/t3a/t4g).
var ec2DefaultCredit sim.Store[string]

func handleGetDefaultCreditSpecification(w http.ResponseWriter, r *http.Request) {
	family := r.FormValue("InstanceFamily")
	if family == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter InstanceFamily", http.StatusBadRequest)
		return
	}
	cpuCredits, _ := ec2DefaultCredit.Get(family)
	if cpuCredits == "" {
		// AWS default: standard for t2, unlimited for t3/t3a/t4g.
		if family == "t2" {
			cpuCredits = "standard"
		} else {
			cpuCredits = "unlimited"
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetDefaultCreditSpecificationResponse %s><requestId>%s</requestId><instanceFamilyCreditSpecification><instanceFamily>%s</instanceFamily><cpuCredits>%s</cpuCredits></instanceFamilyCreditSpecification></GetDefaultCreditSpecificationResponse>`,
		ec2Xmlns(), generateUUID(), family, cpuCredits)
}

func handleModifyDefaultCreditSpecification(w http.ResponseWriter, r *http.Request) {
	family := r.FormValue("InstanceFamily")
	cpuCredits := r.FormValue("CpuCredits")
	if family == "" || cpuCredits == "" {
		ec2ErrorXML(w, "MissingParameter", "InstanceFamily and CpuCredits are required", http.StatusBadRequest)
		return
	}
	ec2DefaultCredit.Put(family, cpuCredits)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyDefaultCreditSpecificationResponse %s><requestId>%s</requestId><instanceFamilyCreditSpecification><instanceFamily>%s</instanceFamily><cpuCredits>%s</cpuCredits></instanceFamilyCreditSpecification></ModifyDefaultCreditSpecificationResponse>`,
		ec2Xmlns(), generateUUID(), family, cpuCredits)
}

func handleModifyVpcTenancy(w http.ResponseWriter, r *http.Request) {
	vpcID := r.FormValue("VpcId")
	tenancy := r.FormValue("InstanceTenancy")
	vpc, ok := ec2Vpcs.Get(vpcID)
	if !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID '%s' does not exist", vpcID), http.StatusBadRequest)
		return
	}
	if tenancy == "" {
		tenancy = "default"
	}
	vpc.InstanceTenancy = tenancy
	ec2Vpcs.Put(vpcID, vpc)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpcTenancyResponse %s><requestId>%s</requestId><return>true</return></ModifyVpcTenancyResponse>`,
		ec2Xmlns(), generateUUID())
}

// ----------------------------------------------------------------------------
// Interruptible capacity-reservation allocation.
// ----------------------------------------------------------------------------

func handleCreateInterruptibleCapacityReservationAllocation(w http.ResponseWriter, r *http.Request) {
	srcID := r.FormValue("CapacityReservationId")
	if srcID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter CapacityReservationId", http.StatusBadRequest)
		return
	}
	target := ec2AtoiOr(r.FormValue("InstanceCount"), 1)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateInterruptibleCapacityReservationAllocationResponse %s><requestId>%s</requestId><sourceCapacityReservationId>%s</sourceCapacityReservationId><targetInstanceCount>%d</targetInstanceCount><status>active</status><interruptionType>spot</interruptionType></CreateInterruptibleCapacityReservationAllocationResponse>`,
		ec2Xmlns(), generateUUID(), srcID, target)
}

func handleUpdateInterruptibleCapacityReservationAllocation(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CapacityReservationId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter CapacityReservationId", http.StatusBadRequest)
		return
	}
	target := ec2AtoiOr(r.FormValue("TargetInstanceCount"), 1)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<UpdateInterruptibleCapacityReservationAllocationResponse %s><requestId>%s</requestId><interruptibleCapacityReservationId>%s</interruptibleCapacityReservationId><targetInstanceCount>%d</targetInstanceCount><status>updating</status><interruptionType>spot</interruptionType></UpdateInterruptibleCapacityReservationAllocationResponse>`,
		ec2Xmlns(), generateUUID(), id, target)
}

// ----------------------------------------------------------------------------
// Export / import tasks.
// ----------------------------------------------------------------------------

func handleCancelExportTask(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ExportTaskId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ExportTaskId", http.StatusBadRequest)
		return
	}
	// CancelExportTask has a Unit output: an empty success response.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CancelExportTaskResponse %s><requestId>%s</requestId><return>true</return></CancelExportTaskResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDescribeExportTasks(w http.ResponseWriter, r *http.Request) {
	// The sim creates no instance-export task, so the read-back is an empty set;
	// the exportTaskSet shape round-trips through the SDK.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeExportTasksResponse %s><requestId>%s</requestId><exportTaskSet/></DescribeExportTasksResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleCancelImportTask(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ImportTaskId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ImportTaskId", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CancelImportTaskResponse %s><requestId>%s</requestId><importTaskId>%s</importTaskId><previousState>active</previousState><state>deleting</state></CancelImportTaskResponse>`,
		ec2Xmlns(), generateUUID(), id)
}

// ----------------------------------------------------------------------------
// Small shared helpers.
// ----------------------------------------------------------------------------

// removeStrings returns src with every element present in remove dropped.
func removeStrings(src, remove []string) []string {
	if len(src) == 0 {
		return src
	}
	drop := map[string]bool{}
	for _, r := range remove {
		drop[r] = true
	}
	var kept []string
	for _, s := range src {
		if !drop[s] {
			kept = append(kept, s)
		}
	}
	return kept
}

// shortNum derives a small stable number from a string for synthesized IPv6.
func shortNum(s string) uint16 {
	var h uint16
	for i := 0; i < len(s); i++ {
		h = h*31 + uint16(s[i])
	}
	return h
}
