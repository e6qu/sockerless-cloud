package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon RDS snapshots carry the instance's data. An engine keeps its data
// directory in the volume sockerless-rds-instance_<instance>; a snapshot
// captures it into sockerless-rds-snapshot_<snapshot>, and a restore clones that
// into the new instance's volume before its engine first starts. Deleting the
// snapshot deletes its volume.
//
// sim.SnapshotVolume captures with one `cp -a --reflink=auto` while the engine's
// container is frozen, one crash-consistent point in time: a copy-on-write block
// clone on btrfs, XFS with reflinks or OpenZFS, a full copy elsewhere. The log
// line below tells an operator which.
//
// On an API-only host (SIM_RUNTIME=process) instances run no engine, and their
// snapshots are metadata without a volume.

// rdsVolume names the volume of one RDS resource. The underscore between the
// kind and the identifier occurs in no RDS identifier, so no two resources of
// any kinds share a volume.
func rdsVolume(kind, identifier string) string { return "sockerless-rds-" + kind + "_" + identifier }

func rdsInstanceVolume(instanceID string) string { return rdsVolume("instance", instanceID) }

// rdsSnapshotVolume names a DB snapshot's volume. An automated snapshot's
// identifier starts rds:, and a volume name cannot hold a colon; no manual
// identifier holds a dot.
func rdsSnapshotVolume(snapshotID string) string {
	return rdsVolume("snapshot", strings.ReplaceAll(snapshotID, ":", "."))
}

// rdsCaptureSnapshotData captures the instance's volume into the snapshot's
// volume and settles the snapshot's status: available when the data is
// captured, failed — with the reason in the status the API returns — when the
// capture could not happen on a host that runs real engines. An engine
// starting for the instance's first automated backup finishes first, so the
// capture never holds a data directory the engine is still initialising.
func rdsCaptureSnapshotData(snapshotID, instanceID string) {
	if plane, ok := rdsLoadDataPlane(instanceID); ok {
		release := plane.backups.holdStart()
		defer release()
	}
	if err := sim.CaptureVolume(context.Background(),
		rdsInstanceVolume(instanceID), rdsSnapshotVolume(snapshotID), "rds"); err != nil {
		rdsSettleSnapshot(snapshotID, "failed", err.Error())
		return
	}
	rdsSettleSnapshot(snapshotID, "available", "")
}

func rdsSettleSnapshot(snapshotID, status, reason string) {
	if !rdsSnapshots.Update(snapshotID, func(snapshot *RDSSnapshot) {
		snapshot.Status = status
		snapshot.StatusReason = reason
	}) {
		// The snapshot was deleted while its capture ran; the volume, if the
		// capture made one, goes with it.
		_ = sim.RemoveVolume(rdsSnapshotVolume(snapshotID))
	}
}

// rdsCopySnapshotData clones the source snapshot's volume into the copy's and
// settles the copy — the CopyDBSnapshot half of the capture machinery. A source
// captured on the modeled tier has no volume; the copy is then exactly as
// modeled as its source, and settles available with nothing to clone.
func rdsCopySnapshotData(targetID, sourceID string) {
	if sim.RequireContainerRuntime("copying an RDS snapshot") != nil || !sim.VolumeExists(rdsSnapshotVolume(sourceID)) {
		rdsSettleSnapshot(targetID, "available", "")
		return
	}
	if _, err := sim.SnapshotVolume(context.Background(),
		rdsSnapshotVolume(sourceID), rdsSnapshotVolume(targetID)); err != nil {
		rdsSettleSnapshot(targetID, "failed", err.Error())
		return
	}
	rdsSettleSnapshot(targetID, "available", "")
}

// rdsCloneSnapshotIntoInstance seeds a new instance's volume from a
// snapshot's, so the engine's first start finds the captured data. A snapshot
// without a volume — taken on the modeled tier — seeds nothing, and the
// restored instance is as modeled as its source was.
func rdsCloneSnapshotIntoInstance(snapshotID, instanceID string) error {
	if sim.RequireContainerRuntime("restoring an RDS snapshot") != nil {
		return nil
	}
	if !sim.VolumeExists(rdsSnapshotVolume(snapshotID)) {
		return nil
	}
	_, err := sim.SnapshotVolume(context.Background(),
		rdsSnapshotVolume(snapshotID), rdsInstanceVolume(instanceID))
	if err != nil {
		return fmt.Errorf("clone snapshot %s into instance %s: %w", snapshotID, instanceID, err)
	}
	return nil
}
