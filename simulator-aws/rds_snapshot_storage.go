package main

import (
	"context"
	"fmt"
	"os"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Amazon RDS snapshots carry the instance's data.
//
// An instance's engine keeps its data directory in the named volume
// sockerless-rds-<instance>. Creating a snapshot captures that volume into
// sockerless-rds-snap-<snapshot>, and restoring clones the snapshot volume
// into the new instance's volume before its engine first starts — so the
// restored engine boots on the captured data, which is the whole point of a
// snapshot. Deleting the snapshot deletes its volume.
//
// The capture is sim.SnapshotVolume's single `cp -a --reflink=auto`: on a
// container engine whose volume store sits on btrfs, XFS with reflinks, or
// OpenZFS with block cloning, the capture clones blocks copy-on-write and is
// effectively instant however large the database; on any other filesystem the
// same command is a real full copy. The RDS API is identical either way —
// the filesystem underneath is the deployment's choice, and the log line
// below is how an operator confirms which they got.
//
// On an API-only host (SIM_RUNTIME=process, no container engine) instances
// themselves are modeled without a running engine, and their snapshots are
// exactly as modeled as they are: metadata, no volume, the same fidelity tier
// as everything else on that host.

func rdsInstanceVolume(instanceID string) string { return "sockerless-rds-" + instanceID }
func rdsSnapshotVolume(snapshotID string) string { return "sockerless-rds-snap-" + snapshotID }

// rdsCaptureSnapshotData captures the instance's volume into the snapshot's
// volume and settles the snapshot's status: available when the data is
// captured, failed — with the reason in the status the API returns — when the
// capture could not happen on a host that runs real engines.
func rdsCaptureSnapshotData(snapshotID, instanceID string) {
	if sim.RequireContainerRuntime("capturing an RDS snapshot") != nil {
		// The modeled tier: no engine, no volume, nothing to capture. The
		// snapshot is as real as its instance, which is metadata.
		rdsSettleSnapshot(snapshotID, "available", "")
		return
	}
	filesystem, err := sim.SnapshotVolume(context.Background(),
		rdsInstanceVolume(instanceID), rdsSnapshotVolume(snapshotID))
	if err != nil {
		rdsSettleSnapshot(snapshotID, "failed", err.Error())
		return
	}
	if sim.VolumeSnapshotIsInstant(filesystem) {
		fmt.Fprintf(os.Stderr, "[sim-rds] snapshot %s captured copy-on-write on %s\n", snapshotID, filesystem)
	} else {
		fmt.Fprintf(os.Stderr, "[sim-rds] snapshot %s captured by full copy on %s (put the engine's volume store on btrfs, XFS with reflinks, or OpenZFS block cloning for instant snapshots)\n", snapshotID, filesystem)
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
