package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/signals"
	"github.com/e6qu/sockerless-cloud/sim/sparse"
)

func handleDescribeVolumes(w http.ResponseWriter, r *http.Request) {
	volumeIDs := ec2ParamList(r, "VolumeId")
	for _, id := range volumeIDs {
		if _, ok := ec2Volumes.Get(id); !ok {
			ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	volumes := make([]EC2Volume, 0)
	for _, vol := range ec2Volumes.List() {
		if len(volumeIDs) > 0 && !ec2StrInValues(vol.VolumeId, volumeIDs) {
			continue
		}
		if !ec2VolumeMatchesFilters(vol, filters) {
			continue
		}
		volumes = append(volumes, vol)
	}
	// MaxResults/NextToken pagination applies only to the list form (not when
	// explicit VolumeIds are requested), matching real EC2. Sort by id for a
	// stable offset cursor across pages.
	nextToken := ""
	if len(volumeIDs) == 0 {
		paged, pageNext, pageOK := awsKeyPage(w, ec2BadToken, volumes, func(it EC2Volume) string { return it.VolumeId }, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0), 0)
		if !pageOK {
			return
		}
		volumes, nextToken = paged, pageNext
	}
	var items strings.Builder
	for _, vol := range volumes {
		items.WriteString(ec2VolumeXML(vol))
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVolumesResponse %s>
  <requestId>%s</requestId>
  <volumeSet>%s</volumeSet>%s
</DescribeVolumesResponse>`, ec2Xmlns(), sim.NewUUID(), items.String(), nextTokenXML)
}

// ebsECSDockerVolumeName returns the Docker named volume name for an ECS-managed EBS volume.
// ECS tasks use Docker named volumes so the data lives in the Docker daemon rather than on
// the sim process's own filesystem, making volumes accessible to sibling task containers
// regardless of whether the sim itself runs on the host or inside a container.
func ebsECSDockerVolumeName(volumeID string) string {
	return "sockerless-ebs-" + volumeID
}

// ebsSnapshotDockerVolumeName returns the Docker named volume name for a snapshot taken
// from an ECS-managed EBS volume.
func ebsSnapshotDockerVolumeName(snapshotID string) string {
	return "sockerless-snap-" + snapshotID
}

// ebsRemoveDockerVolume removes the engine volume behind an Amazon ECS managed
// EBS volume or a snapshot. A volume that is already gone is not an error.
func ebsRemoveDockerVolume(name string) {
	if name == "" {
		return
	}
	if sim.DockerClient() == nil {
		// Process mode (SIM_RUNTIME=process): the managed-EBS volume is
		// host-path-backed, so no engine volume exists to remove.
		return
	}
	_ = sim.RemoveVolume(name)
}

// ebsCopyDockerVolumes copies all content from srcVolume into dstVolume using a
// short-lived Alpine container. The destination volume is auto-created by Docker if
// it does not yet exist.
func ebsCopyDockerVolumes(ctx context.Context, srcVolume, dstVolume string) error {
	handle, err := sim.StartContainerSyncContext(ctx, sim.ContainerConfig{
		Image:        "mirror.gcr.io/library/alpine:latest",
		Architecture: "linux/" + runtime.GOARCH,
		Command:      []string{"sh", "-c", "cp -a /src/. /dst/"},
		Binds: []string{
			srcVolume + ":/src:ro",
			dstVolume + ":/dst",
		},
		// No wall-clock cap: the copy's duration is a function of how much data
		// the volume holds, which the simulator does not get to choose. A fixed
		// timeout here (this was 60s) fails a large but perfectly healthy
		// restore -- an 8 GiB workspace copies in roughly 40s on an idle host
		// and longer under load -- and reports it as an error the caller cannot
		// act on. Real EBS has no equivalent deadline: a volume created from a
		// snapshot is usable immediately and hydrates lazily. Callers bound this
		// instead by the lifecycle they own (a task's transition, which can be
		// stopped), not by a guess made here.
		Timeout: 0,
	}, discardLogSink{})
	if err != nil {
		return fmt.Errorf("start volume copy container: %w", err)
	}
	res := handle.Wait()
	if res.Error != nil {
		return fmt.Errorf("volume copy: %w", res.Error)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("volume copy exited with code %d", res.ExitCode)
	}
	return nil
}

// ebsHostRoot returns the on-disk root for simulated EBS volume backing
// images and snapshot payloads. Resolution order: SIM_EBS_DATA_DIR
// (explicit override), then <SIM_DATA_DIR>/ebs (so volume contents survive
// a simulator restart alongside the SQLite control-plane state), then a
// temp directory.
func ebsHostRoot() string {
	return sim.ScopedDataDir("SIM_EBS_DATA_DIR", "ebs", "sockerless-sim-ebs")
}

func ebsVolumeHostDirPath(volumeID string) string {
	return filepath.Join(ebsHostRoot(), "volumes", volumeID)
}

func ebsSnapshotHostDirPath(snapshotID string) string {
	return filepath.Join(ebsHostRoot(), "snapshots", snapshotID)
}

func EBSVolumeHostDir(volumeID string) string {
	dir := ebsVolumeHostDirPath(volumeID)
	_ = os.MkdirAll(dir, 0o777)
	return dir
}

func ebsPrepareVolumeHostPath(vol *EC2Volume) error {
	if vol.HostPath == "" {
		vol.HostPath = ebsVolumeHostDirPath(vol.VolumeId)
	}
	return os.MkdirAll(vol.HostPath, 0o777)
}

func ec2VolumeXML(vol EC2Volume) string {
	return "<item>" + ec2VolumeFieldsXML(vol) + "</item>"
}

func ec2VolumeFieldsXML(vol EC2Volume) string {
	var attachments strings.Builder
	if len(vol.Attachments) == 0 {
		attachments.WriteString("<attachmentSet/>")
	} else {
		attachments.WriteString("<attachmentSet>")
		for _, att := range vol.Attachments {
			fmt.Fprintf(&attachments, `<item><volumeId>%s</volumeId><instanceId>%s</instanceId><device>%s</device><status>%s</status><attachTime>%s</attachTime><deleteOnTermination>%t</deleteOnTermination></item>`,
				att.VolumeId, att.InstanceId, att.Device, att.State, att.AttachTime, att.DeleteOnTermination)
		}
		attachments.WriteString("</attachmentSet>")
	}
	perf := ""
	if vol.Iops > 0 {
		perf += fmt.Sprintf("<iops>%d</iops>", vol.Iops)
	}
	if vol.Throughput > 0 {
		perf += fmt.Sprintf("<throughput>%d</throughput>", vol.Throughput)
	}
	if vol.KmsKeyId != "" {
		perf += fmt.Sprintf("<kmsKeyId>%s</kmsKeyId>", vol.KmsKeyId)
	}
	return fmt.Sprintf(`
    <volumeId>%s</volumeId>
    <size>%d</size>
    <snapshotId>%s</snapshotId>
    <availabilityZone>%s</availabilityZone>
    <status>%s</status>
    <createTime>%s</createTime>
    %s
    <volumeType>%s</volumeType>%s
    <encrypted>%t</encrypted>
    <multiAttachEnabled>%t</multiAttachEnabled>
    %s
  `, vol.VolumeId, vol.Size, vol.SnapshotId, vol.AvailabilityZone, vol.State, vol.CreateTime,
		attachments.String(), vol.VolumeType, perf, vol.Encrypted, vol.MultiAttachEnabled, writeTagSetXML(vol.Tags))
}

// ec2ResolveVolumePerformance returns the effective iops/throughput for a
// volume, applying the AWS-documented per-type rules (gp3 defaults 3000/125;
// gp2 iops derived from size; io1/io2 use the requested iops; st1/sc1/standard
// have neither). The resolved values are stored so they round-trip.
func ec2ResolveVolumePerformance(volType string, size, reqIops, reqThroughput int) (iops, throughput int) {
	switch volType {
	case "gp3":
		iops = reqIops
		if iops == 0 {
			iops = 3000
		}
		throughput = reqThroughput
		if throughput == 0 {
			throughput = 125
		}
	case "gp2":
		iops = 3 * size
		if iops < 100 {
			iops = 100
		}
		if iops > 16000 {
			iops = 16000
		}
	case "io1", "io2":
		iops = reqIops
	}
	return iops, throughput
}

func ec2VolumeMatchesFilters(vol EC2Volume, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "volume-id":
			if !ec2StrInValues(vol.VolumeId, vals) {
				return false
			}
		case "volume-type":
			if !ec2StrInValues(vol.VolumeType, vals) {
				return false
			}
		case "status":
			if !ec2StrInValues(vol.State, vals) {
				return false
			}
		case "availability-zone":
			if !ec2StrInValues(vol.AvailabilityZone, vals) {
				return false
			}
		case "snapshot-id":
			if !ec2StrInValues(vol.SnapshotId, vals) {
				return false
			}
		case "encrypted":
			if (ec2StrInValues("true", vals)) != vol.Encrypted {
				return false
			}
		case "attachment.instance-id":
			if !ec2VolumeHasAttachment(vol, func(a EC2VolumeAttachment) bool { return ec2StrInValues(a.InstanceId, vals) }) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, vol.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func ec2VolumeHasAttachment(vol EC2Volume, pred func(EC2VolumeAttachment) bool) bool {
	for _, a := range vol.Attachments {
		if pred(a) {
			return true
		}
	}
	return false
}

func handleCreateVolume(w http.ResponseWriter, r *http.Request) {
	// AvailabilityZone is a required CreateVolume parameter; real EC2 rejects a
	// request without it rather than defaulting the AZ.
	az := r.FormValue("AvailabilityZone")
	if az == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter availability_zone", http.StatusBadRequest)
		return
	}
	size := 8
	if v := r.FormValue("Size"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &size); err != nil || size < 1 {
			ec2ErrorXML(w, "InvalidParameterValue", "Size must be a positive integer", http.StatusBadRequest)
			return
		}
	}
	snapshotID := r.FormValue("SnapshotId")
	var snapshotHostPath string
	if snapshotID != "" {
		ec2SettleSnapshot(snapshotID)
		snap, ok := ec2Snapshots.Get(snapshotID)
		if !ok {
			ec2ErrorXML(w, "InvalidSnapshot.NotFound", fmt.Sprintf("The snapshot %q does not exist", snapshotID), http.StatusBadRequest)
			return
		}
		if snap.State != "completed" {
			ec2ErrorXML(w, "IncorrectState", fmt.Sprintf("The snapshot %q is not completed", snapshotID), http.StatusBadRequest)
			return
		}
		if size < snap.VolumeSize {
			size = snap.VolumeSize
		}
		snapshotHostPath = snap.HostPath
	}
	volType := r.FormValue("VolumeType")
	if volType == "" {
		volType = "gp3"
	}
	iops, throughput := ec2ResolveVolumePerformance(volType, size,
		ec2AtoiOr(r.FormValue("Iops"), 0), ec2AtoiOr(r.FormValue("Throughput"), 0))
	vol := EC2Volume{
		VolumeId:           ec2ID("vol"),
		Size:               size,
		SnapshotId:         snapshotID,
		AvailabilityZone:   az,
		State:              "available",
		CreateTime:         time.Now().UTC().Format(time.RFC3339),
		VolumeType:         volType,
		Iops:               iops,
		Throughput:         throughput,
		KmsKeyId:           r.FormValue("KmsKeyId"),
		Encrypted:          r.FormValue("Encrypted") == "true" || r.FormValue("KmsKeyId") != "",
		MultiAttachEnabled: r.FormValue("MultiAttachEnabled") == "true",
		Tags:               parseTags(r),
	}
	if err := ebsPrepareVolumeHostPath(&vol); err != nil {
		ec2ErrorXML(w, "InternalError", fmt.Sprintf("could not create volume data path: %v", err), http.StatusInternalServerError)
		return
	}
	// A volume restored from a snapshot is answered creating and hydrates
	// behind the response, as Amazon EC2 does; it is available once its data
	// is in place.
	if snapshotHostPath != "" {
		vol.State = "creating"
	}
	ec2Volumes.Put(vol.VolumeId, vol)
	if snapshotHostPath != "" {
		ec2HydrateVolume(vol.VolumeId, vol.HostPath, snapshotHostPath)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVolumeResponse %s><requestId>%s</requestId>%s</CreateVolumeResponse>`,
		ec2Xmlns(), sim.NewUUID(), ec2VolumeFieldsXML(vol))
}

// ec2VolumeHydrating holds the volumes whose snapshot data is still
// copying; ec2VolumeHydrations announces a volume when its copy ends.
var (
	ec2VolumeHydrationsMu sync.Mutex
	ec2VolumeHydrating    = map[string]bool{}
	ec2VolumeHydrations   = signals.New[string]()
)

// ec2HydrateVolume copies a snapshot's data into a creating volume and makes
// it available, or puts it in error when the copy fails.
func ec2HydrateVolume(volumeID, destination, source string) {
	ec2VolumeHydrationsMu.Lock()
	ec2VolumeHydrating[volumeID] = true
	ec2VolumeHydrationsMu.Unlock()
	finish := func() {
		ec2VolumeHydrationsMu.Lock()
		delete(ec2VolumeHydrating, volumeID)
		ec2VolumeHydrationsMu.Unlock()
		ec2VolumeHydrations.Announce(volumeID)
	}
	hydrate := func() {
		if err := sparse.CopyTree(destination, source); err != nil {
			log.Printf("Amazon EC2: restore snapshot data into %s: %v", volumeID, err)
			ec2Volumes.Update(volumeID, func(vol *EC2Volume) { vol.State = "error" })
			return
		}
		ec2Volumes.Update(volumeID, func(vol *EC2Volume) {
			if vol.State == "creating" {
				vol.State = "available"
			}
		})
	}
	run, ok := bg.Handoff(hydrate)
	if !ok {
		hydrate()
		finish()
		return
	}
	go func() {
		defer finish()
		run()
	}()
}

// ec2AwaitVolumeHydrated returns once a volume's snapshot data is in place,
// at once for a volume that is not hydrating.
func ec2AwaitVolumeHydrated(ctx context.Context, volumeID string) error {
	return ec2VolumeHydrations.Await(ctx, volumeID, func() bool {
		ec2VolumeHydrationsMu.Lock()
		defer ec2VolumeHydrationsMu.Unlock()
		return !ec2VolumeHydrating[volumeID]
	})
}

func handleAttachVolume(w http.ResponseWriter, r *http.Request) {
	volID := r.FormValue("VolumeId")
	instanceID := r.FormValue("InstanceId")
	device := r.FormValue("Device")
	if device == "" {
		device = "/dev/sdf"
	}
	vol, ok := ec2Volumes.Get(volID)
	if !ok {
		ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", volID), http.StatusBadRequest)
		return
	}
	if _, ok := ec2Instances.Get(instanceID); !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID '%s' does not exist", instanceID), http.StatusBadRequest)
		return
	}
	if len(vol.Attachments) > 0 {
		ec2ErrorXML(w, "IncorrectState", "Volume is already attached", http.StatusBadRequest)
		return
	}
	att := EC2VolumeAttachment{
		VolumeId:   volID,
		InstanceId: instanceID,
		Device:     device,
		State:      "attached",
		AttachTime: time.Now().UTC().Format(time.RFC3339),
	}
	vol.State = "in-use"
	vol.Attachments = []EC2VolumeAttachment{att}
	ec2Volumes.Put(volID, vol)
	ec2AttachmentResponse(w, "AttachVolume", att)
}

func handleDetachVolume(w http.ResponseWriter, r *http.Request) {
	volID := r.FormValue("VolumeId")
	vol, ok := ec2Volumes.Get(volID)
	if !ok {
		ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", volID), http.StatusBadRequest)
		return
	}
	if len(vol.Attachments) == 0 {
		ec2ErrorXML(w, "IncorrectState", "Volume is not attached", http.StatusBadRequest)
		return
	}
	att := vol.Attachments[0]
	att.State = "detached"
	vol.Attachments = nil
	vol.State = "available"
	ec2Volumes.Put(volID, vol)
	ec2AttachmentResponse(w, "DetachVolume", att)
}

func ec2AttachmentResponse(w http.ResponseWriter, action string, att EC2VolumeAttachment) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s><requestId>%s</requestId><volumeId>%s</volumeId><instanceId>%s</instanceId><device>%s</device><status>%s</status><attachTime>%s</attachTime></%sResponse>`,
		action, ec2Xmlns(), sim.NewUUID(), att.VolumeId, att.InstanceId, att.Device, att.State, att.AttachTime, action)
}

func handleDeleteVolume(w http.ResponseWriter, r *http.Request) {
	volID := r.FormValue("VolumeId")
	vol, ok := ec2Volumes.Get(volID)
	if !ok {
		ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", volID), http.StatusBadRequest)
		return
	}
	if len(vol.Attachments) > 0 {
		ec2ErrorXML(w, "VolumeInUse", "Volume is in-use", http.StatusBadRequest)
		return
	}
	if vol.DockerVolumeName != "" {
		ebsRemoveDockerVolume(vol.DockerVolumeName)
	} else {
		_ = os.RemoveAll(vol.HostPath)
	}
	ec2Volumes.Delete(volID)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVolumeResponse %s><requestId>%s</requestId><return>true</return></DeleteVolumeResponse>`, ec2Xmlns(), sim.NewUUID())
}

func handleModifyVolume(w http.ResponseWriter, r *http.Request) {
	volID := r.FormValue("VolumeId")
	vol, ok := ec2Volumes.Get(volID)
	if !ok {
		ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", volID), http.StatusBadRequest)
		return
	}
	mod := EC2VolumeModification{
		VolumeId:           volID,
		ModificationState:  "completed",
		OriginalSize:       vol.Size,
		OriginalVolumeType: vol.VolumeType,
		OriginalIops:       vol.Iops,
		OriginalThroughput: vol.Throughput,
		StartTime:          time.Now().UTC().Format(time.RFC3339),
		EndTime:            time.Now().UTC().Format(time.RFC3339),
	}
	if v := r.FormValue("Size"); v != "" {
		var size int
		if _, err := fmt.Sscanf(v, "%d", &size); err != nil || size < vol.Size {
			ec2ErrorXML(w, "InvalidParameterValue", "Size must be an integer greater than or equal to the current volume size", http.StatusBadRequest)
			return
		}
		vol.Size = size
	}
	if v := r.FormValue("VolumeType"); v != "" {
		vol.VolumeType = v
	}
	if v := r.FormValue("Iops"); v != "" {
		vol.Iops = ec2AtoiOr(v, vol.Iops)
	}
	if v := r.FormValue("Throughput"); v != "" {
		vol.Throughput = ec2AtoiOr(v, vol.Throughput)
	}
	// Re-resolve performance for the (possibly new) type/size so gp2 iops and
	// gp3 defaults stay consistent after a type change.
	vol.Iops, vol.Throughput = ec2ResolveVolumePerformance(vol.VolumeType, vol.Size, vol.Iops, vol.Throughput)
	mod.TargetSize, mod.TargetVolumeType, mod.TargetIops, mod.TargetThroughput = vol.Size, vol.VolumeType, vol.Iops, vol.Throughput
	ec2VolumeMods.Put(volID, mod)
	ec2Volumes.Put(volID, vol)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVolumeResponse %s><requestId>%s</requestId><volumeModification>%s</volumeModification></ModifyVolumeResponse>`,
		ec2Xmlns(), sim.NewUUID(), ec2VolumeModFieldsXML(mod))
}

func ec2VolumeModFieldsXML(m EC2VolumeModification) string {
	return fmt.Sprintf(`<volumeId>%s</volumeId><modificationState>%s</modificationState>`+
		`<targetSize>%d</targetSize><targetVolumeType>%s</targetVolumeType><targetIops>%d</targetIops><targetThroughput>%d</targetThroughput>`+
		`<originalSize>%d</originalSize><originalVolumeType>%s</originalVolumeType><originalIops>%d</originalIops><originalThroughput>%d</originalThroughput>`+
		`<progress>100</progress><startTime>%s</startTime><endTime>%s</endTime>`,
		m.VolumeId, m.ModificationState,
		m.TargetSize, m.TargetVolumeType, m.TargetIops, m.TargetThroughput,
		m.OriginalSize, m.OriginalVolumeType, m.OriginalIops, m.OriginalThroughput,
		m.StartTime, m.EndTime)
}

// handleDescribeVolumesModifications returns the recorded volume modifications.
// terraform-provider-aws polls this after a ModifyVolume to wait for the resize
// to reach `completed`; an unregistered op previously made aws_ebs_volume
// updates error with UnknownOperation.
func handleDescribeVolumesModifications(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VolumeId")
	mods := make([]EC2VolumeModification, 0)
	for _, mod := range ec2VolumeMods.List() {
		if len(ids) > 0 && !ec2StrInValues(mod.VolumeId, ids) {
			continue
		}
		mods = append(mods, mod)
	}
	mods, nextToken, pageOK := awsKeyPage(w, ec2BadToken, mods, func(it EC2VolumeModification) string { return it.VolumeId }, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0), 0)
	if !pageOK {
		return
	}

	var items strings.Builder
	items.WriteString("<volumeModificationSet>")
	for _, mod := range mods {
		items.WriteString("<item>")
		items.WriteString(ec2VolumeModFieldsXML(mod))
		items.WriteString("</item>")
	}
	items.WriteString("</volumeModificationSet>")
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVolumesModificationsResponse %s><requestId>%s</requestId>%s%s</DescribeVolumesModificationsResponse>`,
		ec2Xmlns(), sim.NewUUID(), items.String(), nextTokenXML)
}
