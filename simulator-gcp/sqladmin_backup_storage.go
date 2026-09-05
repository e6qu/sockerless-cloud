package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud SQL backups carry the instance's data.
//
// An instance's engine keeps its data directory in the named volume
// sockerless-cloudsql-<project>-<instance>. A backup run captures that volume
// into a backup volume, and instances.restoreBackup clones the backup volume
// back over the instance's — the engine is stopped for the swap and the next
// connection boots it on the restored data, which is what a restore is.
// Deleting a backup deletes its volume; deleting the instance deletes its
// backup runs and their volumes, as Cloud SQL does.
//
// The capture is sim.SnapshotVolume's single `cp -a --reflink=auto`: on a
// container engine whose volume store sits on btrfs, XFS with reflinks, or
// OpenZFS with block cloning, the capture clones blocks copy-on-write and is
// effectively instant however large the database; on any other filesystem the
// same command is a real full copy. The Cloud SQL Admin API is identical
// either way — the filesystem underneath is the deployment's choice, and the
// log line below is how an operator confirms which they got.
//
// On an API-only host instances are modeled without a running engine, and
// their backups are exactly as modeled as they are: metadata, no volume, the
// same fidelity tier as everything else on that host.

func sqlBackupRunVolume(project, instance string, id int64) string {
	return "sockerless-cloudsql-backup-" + project + "-" + instance + "-" + strconv.FormatInt(id, 10)
}

func sqlBackupVolume(project, backupID string) string {
	return "sockerless-cloudsql-backup-" + project + "-" + backupID
}

// sqlCaptureVolume captures an instance's volume into a backup volume and
// logs whether the filesystem gave the copy-on-write path. A source volume
// that does not exist — the modeled tier, or an engine that never started —
// captures nothing, which is that instance's whole state.
func sqlCaptureVolume(project, instance, backupVolume string) error {
	if sim.RequireContainerRuntime("capturing a Cloud SQL backup") != nil {
		return nil
	}
	if !sim.VolumeExists(sqlInstanceVolume(project, instance)) {
		return nil
	}
	filesystem, err := sim.SnapshotVolume(context.Background(), sqlInstanceVolume(project, instance), backupVolume)
	if err != nil {
		return err
	}
	if sim.VolumeSnapshotIsInstant(filesystem) {
		fmt.Fprintf(os.Stderr, "[sim-cloudsql] backup %s captured copy-on-write on %s\n", backupVolume, filesystem)
	} else {
		fmt.Fprintf(os.Stderr, "[sim-cloudsql] backup %s captured by full copy on %s (put the engine's volume store on btrfs, XFS with reflinks, or OpenZFS block cloning for instant backups)\n", backupVolume, filesystem)
	}
	return nil
}

// sqlRestoreVolume stops the instance's engine, replaces its data volume
// with a clone of the backup volume, and leaves the engine to boot on the
// restored data at the next connection. A backup without a volume — taken on
// the modeled tier — restores the state it captured: nothing.
func sqlRestoreVolume(project, instance, backupVolume string) error {
	if sim.RequireContainerRuntime("restoring a Cloud SQL backup") != nil {
		return nil
	}
	sqlStopEngine(project, instance)
	sqlRemoveVolumeSettled(sqlInstanceVolume(project, instance))
	if !sim.VolumeExists(backupVolume) {
		return nil
	}
	if _, err := sim.SnapshotVolume(context.Background(), backupVolume, sqlInstanceVolume(project, instance)); err != nil {
		return fmt.Errorf("clone backup volume %s into instance %s: %w", backupVolume, instance, err)
	}
	return nil
}

// sqlCloneVolume seeds a cloned instance's volume from its source's, so the
// clone's first engine start finds the source's data.
func sqlCloneVolume(project, source, destination string) error {
	if sim.RequireContainerRuntime("cloning a Cloud SQL instance") != nil {
		return nil
	}
	if !sim.VolumeExists(sqlInstanceVolume(project, source)) {
		return nil
	}
	if _, err := sim.SnapshotVolume(context.Background(), sqlInstanceVolume(project, source), sqlInstanceVolume(project, destination)); err != nil {
		return fmt.Errorf("clone instance volume %s into %s: %w", source, destination, err)
	}
	return nil
}

// sqlRemoveBackupVolume removes a backup's volume when the backup is
// deleted. A volume that never existed — the modeled tier — needs nothing.
func sqlRemoveBackupVolume(name string) {
	if sim.RequireContainerRuntime("deleting a Cloud SQL backup volume") != nil {
		return
	}
	if !sim.VolumeExists(name) {
		return
	}
	sqlRemoveVolumeSettled(name)
}
