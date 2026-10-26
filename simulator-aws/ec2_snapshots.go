package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/sparse"
)

func handleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	volID := r.FormValue("VolumeId")
	vol, ok := ec2Volumes.Get(volID)
	if !ok {
		ec2ErrorXML(w, "InvalidVolume.NotFound", fmt.Sprintf("The volume %q does not exist", volID), http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	snap := EC2Snapshot{
		SnapshotId:    ec2ID("snap"),
		VolumeId:      volID,
		VolumeSize:    vol.Size,
		State:         "pending",
		StartTime:     now.Format(time.RFC3339),
		CompletionDue: now.Add(100 * time.Millisecond).Format(time.RFC3339Nano),
		Progress:      "0%",
		Description:   r.FormValue("Description"),
		OwnerId:       ec2Owner(),
		Encrypted:     vol.Encrypted,
		KmsKeyId:      vol.KmsKeyId,
		Tags:          parseTags(r),
	}
	if vol.DockerVolumeName != "" {
		snap.DockerVolumeName = ebsSnapshotDockerVolumeName(snap.SnapshotId)
	} else {
		snap.HostPath = ebsSnapshotHostDirPath(snap.SnapshotId)
		if err := ebsPrepareVolumeHostPath(&vol); err != nil {
			ec2ErrorXML(w, "InternalError", fmt.Sprintf("could not access volume data path: %v", err), http.StatusInternalServerError)
			return
		}
	}
	// Capture behind the response, as on Amazon EC2, where callers poll
	// DescribeSnapshots; copying in the handler would stall every request for the
	// length of the copy. The capture goroutine, not CompletionDue, completes the
	// snapshot, so push CompletionDue out of reach: a settle must never report a
	// half-written snapshot completed.
	snap.CompletionDue = now.Add(ec2SnapshotCaptureNotBefore).Format(time.RFC3339Nano)
	ec2Volumes.Put(vol.VolumeId, vol)
	ec2Snapshots.Put(snap.SnapshotId, snap)
	bg.Go(func() {
		ec2CaptureSnapshotData(snap.SnapshotId, vol.DockerVolumeName, snap.DockerVolumeName, vol.HostPath, snap.HostPath)
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateSnapshotResponse %s><requestId>%s</requestId>%s</CreateSnapshotResponse>`,
		ec2Xmlns(), sim.NewUUID(), ec2SnapshotFieldsXML(snap))
}

// CopySnapshot is the cross-Region DR primitive; the simulator copies locally
// against the source. The response carries only the new snapshotId.
func handleCopySnapshot(w http.ResponseWriter, r *http.Request) {
	srcID := r.FormValue("SourceSnapshotId")
	if srcID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter SourceSnapshotId", http.StatusBadRequest)
		return
	}
	ec2SettleSnapshot(srcID)
	src, ok := ec2Snapshots.Get(srcID)
	if !ok {
		ec2ErrorXML(w, "InvalidSnapshot.NotFound", fmt.Sprintf("The snapshot %q does not exist", srcID), http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	snap := EC2Snapshot{
		SnapshotId:    ec2ID("snap"),
		VolumeId:      src.VolumeId,
		VolumeSize:    src.VolumeSize,
		State:         "pending",
		StartTime:     now.Format(time.RFC3339),
		CompletionDue: now.Add(100 * time.Millisecond).Format(time.RFC3339Nano),
		Progress:      "0%",
		Description:   r.FormValue("Description"),
		OwnerId:       ec2Owner(),
		Encrypted:     src.Encrypted,
		KmsKeyId:      src.KmsKeyId,
		Tags:          parseTags(r),
	}
	if src.DockerVolumeName != "" {
		snap.DockerVolumeName = ebsSnapshotDockerVolumeName(snap.SnapshotId)
	} else if src.HostPath != "" {
		snap.HostPath = ebsSnapshotHostDirPath(snap.SnapshotId)
	}
	// Asynchronous for the same reason as CreateSnapshot.
	snap.CompletionDue = now.Add(ec2SnapshotCaptureNotBefore).Format(time.RFC3339Nano)
	ec2Snapshots.Put(snap.SnapshotId, snap)
	go ec2CaptureSnapshotData(snap.SnapshotId, src.DockerVolumeName, snap.DockerVolumeName, src.HostPath, snap.HostPath)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CopySnapshotResponse %s><requestId>%s</requestId><snapshotId>%s</snapshotId></CopySnapshotResponse>`,
		ec2Xmlns(), sim.NewUUID(), snap.SnapshotId)
}

// Far enough ahead that no settle completes a capturing snapshot.
const ec2SnapshotCaptureNotBefore = 100 * 365 * 24 * time.Hour

// A test holds the capture here to observe the snapshot before any byte moves.
var ec2SnapshotCaptureStarting = func(snapshotID string) {}

// The source is a volume for CreateSnapshot and a snapshot for CopySnapshot;
// it runs off the request path, then completes the snapshot.
func ec2CaptureSnapshotData(snapshotID, dockerSrc, dockerDst, hostSrc, hostDst string) {
	ec2SnapshotCaptureStarting(snapshotID)
	var err error
	switch {
	case dockerDst != "":
		err = ebsCopyDockerVolumes(context.Background(), dockerSrc, dockerDst)
	case hostDst != "":
		err = sparse.CopyTree(hostDst, hostSrc)
	}
	if err != nil {
		// Amazon EC2 reports an uncapturable snapshot as error and leaves it
		// discoverable; a silent failure would let a restore mount an empty volume.
		fmt.Fprintf(os.Stderr, "[sim-ec2] snapshot %s: could not capture volume data: %v\n", snapshotID, err)
		ec2Snapshots.Update(snapshotID, func(s *EC2Snapshot) {
			s.State = "error"
			s.Progress = "0%"
		})
		return
	}
	ec2Snapshots.Update(snapshotID, func(s *EC2Snapshot) {
		if s.State == "pending" {
			s.State = "completed"
			s.Progress = "100%"
			s.CompletionDue = time.Now().UTC().Format(time.RFC3339Nano)
		}
	})
}

func ec2SettleSnapshot(snapshotID string) {
	ec2Snapshots.Update(snapshotID, func(snap *EC2Snapshot) {
		if snap.State == "pending" && !ec2SnapshotCompletionDue(*snap).After(time.Now().UTC()) {
			snap.State = "completed"
			snap.Progress = "100%"
		}
	})
}

func ec2SnapshotCompletionDue(snap EC2Snapshot) time.Time {
	if snap.CompletionDue != "" {
		if t, err := time.Parse(time.RFC3339Nano, snap.CompletionDue); err == nil {
			return t
		}
	}
	if t, err := time.Parse(time.RFC3339, snap.StartTime); err == nil {
		return t.Add(100 * time.Millisecond)
	}
	return time.Time{}
}

func handleDescribeSnapshots(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "SnapshotId")
	snapshots := make([]EC2Snapshot, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			ec2SettleSnapshot(id)
			snap, ok := ec2Snapshots.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidSnapshot.NotFound", fmt.Sprintf("The snapshot %q does not exist", id), http.StatusBadRequest)
				return
			}
			snapshots = append(snapshots, snap)
		}
	} else {
		for _, snap := range ec2Snapshots.List() {
			ec2SettleSnapshot(snap.SnapshotId)
		}
		filters := ec2Filters(r)
		// The simulator is single-account, so "self" or the account id matches.
		owners := ec2ParamList(r, "Owner")
		self := ec2Owner()
		for _, snap := range ec2Snapshots.List() {
			if len(owners) > 0 {
				matched := false
				for _, o := range owners {
					if o == self || o == "self" {
						matched = true
					}
				}
				if !matched {
					continue
				}
			}
			if !ec2SnapshotMatchesFilters(snap, filters) {
				continue
			}
			snapshots = append(snapshots, snap)
		}
	}
	// Paginate only the list form, not explicit SnapshotIds; sort by id for a
	// stable cursor.
	nextToken := ""
	if len(ids) == 0 {
		paged, pageNext, pageOK := awsKeyPage(w, ec2BadToken, snapshots, func(it EC2Snapshot) string { return it.SnapshotId }, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0), 0)
		if !pageOK {
			return
		}
		snapshots, nextToken = paged, pageNext
	}
	var items strings.Builder
	for _, snap := range snapshots {
		items.WriteString(ec2SnapshotXML(snap))
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSnapshotsResponse %s><requestId>%s</requestId><snapshotSet>%s</snapshotSet>%s</DescribeSnapshotsResponse>`,
		ec2Xmlns(), sim.NewUUID(), items.String(), nextTokenXML)
}

func ec2SnapshotXML(snap EC2Snapshot) string {
	return "<item>" + ec2SnapshotFieldsXML(snap) + "</item>"
}

func ec2SnapshotFieldsXML(snap EC2Snapshot) string {
	kms := ""
	if snap.KmsKeyId != "" {
		kms = fmt.Sprintf("<kmsKeyId>%s</kmsKeyId>", snap.KmsKeyId)
	}
	return fmt.Sprintf(`<snapshotId>%s</snapshotId><volumeId>%s</volumeId><status>%s</status><startTime>%s</startTime><progress>%s</progress><ownerId>%s</ownerId><volumeSize>%d</volumeSize><description>%s</description><encrypted>%t</encrypted>%s%s`,
		snap.SnapshotId, snap.VolumeId, snap.State, snap.StartTime, snap.Progress, snap.OwnerId, snap.VolumeSize, xmlEscape(snap.Description), snap.Encrypted, kms, writeTagSetXML(snap.Tags))
}

func ec2SnapshotMatchesFilters(snap EC2Snapshot, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "snapshot-id":
			if !ec2StrInValues(snap.SnapshotId, vals) {
				return false
			}
		case "volume-id":
			if !ec2StrInValues(snap.VolumeId, vals) {
				return false
			}
		case "status":
			if !ec2StrInValues(snap.State, vals) {
				return false
			}
		case "owner-id":
			if !ec2StrInValues(snap.OwnerId, vals) {
				return false
			}
		case "encrypted":
			if (ec2StrInValues("true", vals)) != snap.Encrypted {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, snap.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("SnapshotId")
	snap, ok := ec2Snapshots.Get(snapID)
	if !ok {
		ec2ErrorXML(w, "InvalidSnapshot.NotFound", fmt.Sprintf("The snapshot %q does not exist", snapID), http.StatusBadRequest)
		return
	}
	if images := ec2ImagesBySnapshot.LookupAll(ec2Images, snapID, func(img EC2Image) []string {
		return []string{img.SnapshotId}
	}); len(images) > 0 {
		ec2ErrorXML(w, "InvalidSnapshot.InUse",
			fmt.Sprintf("The snapshot %s is currently in use by %s", snapID, images[0].ImageId), http.StatusBadRequest)
		return
	}
	ec2Snapshots.Delete(snapID)
	if snap.DockerVolumeName != "" {
		ebsRemoveDockerVolume(snap.DockerVolumeName)
	} else {
		_ = os.RemoveAll(snap.HostPath)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteSnapshotResponse %s><requestId>%s</requestId><return>true</return></DeleteSnapshotResponse>`, ec2Xmlns(), sim.NewUUID())
}
